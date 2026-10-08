import { SwipeActionRow } from "../ui/SwipeActionRow";
import { useEffect, useRef, useState } from "react";
import { ApiError } from "../api/client";
import { appsClient, type AppSummary } from "../api/apps";
import {
  hankAIClient,
  terminalTask,
  type HankAITask,
  type HankAIMessage,
  type HankAIPendingActionSummary,
  type HankAIResultCard,
  type HankAIRun,
  type HankAISession,
  type HankAIStatus,
} from "../api/hankAI";
import { useConfirmDialog } from "../ui/primitives";
import { executeHankAIClientTool } from "./hankAIClientTools";
import {
  attachmentOnlyMessageText,
  attachmentPayload,
  formatHankAIBytes,
  hankAIResultCardHref,
  hankAIResultImageURL,
  stageHankAIAttachments,
  type HankAIDraftAttachment,
} from "./hankAIWorkflow";

type SlashCommand = { token: string; hint: string };

type ReadyState = {
  status: "ready";
  assistantStatus: HankAIStatus;
  sessions: HankAISession[];
  selectedSessionID: string;
  messages: HankAIMessage[];
  draft: string;
  draftAttachments: HankAIDraftAttachment[];
  notice: string;
  sending: boolean;
  preparingAttachments: boolean;
  slashCommands: SlashCommand[];
  activeRun?: HankAIRun;
  activeTask?: HankAITask;
  executionVersion?: number;
  taskActivity?: string;
};

type State =
  | { status: "loading" }
  | { status: "error"; message: string }
  | ReadyState;

const BUILT_IN_SLASH_COMMANDS: SlashCommand[] = [
  { token: "/ha", hint: "Find Home Assistant entities and state" },
  { token: "/files", hint: "Search or browse File Server" },
  { token: "/notes", hint: "Find or list notes" },
  { token: "/append", hint: "Append text to a note" },
  { token: "/calendar", hint: "Check the calendar" },
  { token: "/docs", hint: "Ask about Hank and its documentation" },
  { token: "/status", hint: "Report HankAI, agent, sync, or backup status" },
];

function slashCommandsForApps(apps: AppSummary[]): SlashCommand[] {
  const commands = [...BUILT_IN_SLASH_COMMANDS];
  const seen = new Set(commands.map((command) => command.token.toLowerCase()));
  for (const app of apps) {
    if (!app.enabled) continue;
    for (const slashCommand of app.slash_commands || []) {
      const token = slashCommand.command?.trim();
      if (!token?.startsWith("/") || token.includes(" ")) continue;
      const identity = token.toLowerCase();
      if (seen.has(identity)) continue;
      seen.add(identity);
      commands.push({
        token,
        hint: slashCommand.description?.trim() || `Run ${app.name?.trim() || app.id || app.app_id || "installed app"}`,
      });
    }
  }
  return commands;
}

function errorMessage(error: unknown): string {
  return error instanceof Error && error.message ? error.message : "Hank Chat could not be loaded.";
}

function messageText(message: HankAIMessage): string {
  return message.text || message.content || "";
}

function sessionTitle(session: HankAISession): string {
  const title = session.title?.trim();
  return !title || /^new (conversation|chat)$/i.test(title) ? "New chat" : title;
}

function formatSessionTime(value?: string): string {
  if (!value) return "No messages yet";
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? value : parsed.toLocaleString([], { dateStyle: "medium", timeStyle: "short" });
}

function terminalRun(run: HankAIRun): boolean {
  return ["completed", "failed", "cancelled", "canceled"].includes(String(run.state || "").toLowerCase());
}

function cardKindLabel(kind?: string): string {
  return String(kind || "result").replaceAll("_", " ");
}

function ResultCard({ card, onSelect }: { card: HankAIResultCard; onSelect: (value: string) => void }) {
  const imageURL = hankAIResultImageURL(card);
  const href = hankAIResultCardHref(card);
  const mediaSelection = String(card.kind || "").toLowerCase() === "media" && !card.job_id && card.media_option_id
    ? String(card.title || card.search_text || "").trim()
    : "";
  return (
    <article className={`hank-result-card${imageURL ? " has-image" : ""}`}>
      {imageURL ? <img className="hank-result-card-image" src={imageURL} alt="" /> : null}
      <div className="hank-result-card-body">
        <strong>{card.title || "Hank result"}</strong>
        {card.summary ? <p>{card.summary}</p> : null}
        {card.job_id ? <small>Job {card.job_id}</small> : null}
        <footer>
          <span>{cardKindLabel(card.kind)}</span>
          {mediaSelection ? (
            <button type="button" className="button-link" onClick={() => onSelect(mediaSelection)}>
              {card.action_title || "Choose"}
            </button>
          ) : href ? (
            <a className="button-link" href={href}>{card.action_title || "Open"}</a>
          ) : null}
        </footer>
      </div>
    </article>
  );
}

function safeSourceHref(href: string): string {
  return href.startsWith("/dashboard/") || href.startsWith("/v1/home/assistant/tasks/") || href.startsWith("hank://calendar/") ? href : "";
}

function MessageText({ message }: { message: HankAIMessage }) {
  const text = messageText(message);
  const parts: (string | React.JSX.Element)[] = [];
  let offset = 0;
  for (const match of text.matchAll(/\[([^\]\n]+)\]\(([^)\s]+)\)/g)) {
    const index = match.index ?? 0;
    parts.push(text.slice(offset, index));
    const source = message.sources?.find((candidate) => candidate.uri === match[2]);
    const href = source ? safeSourceHref(source.href) : "";
    parts.push(href ? <a href={href} key={index}>{match[1]}</a> : match[0]);
    offset = index + match[0].length;
  }
  parts.push(text.slice(offset));
  return <>{parts}</>;
}

function taskEventLabel(type: string): string {
  const labels: Record<string, string> = { queued: "Queued", searching: "Searching sources", reading: "Reading results", planning: "Planning the next step", verifying: "Checking the result", waiting_input: "Waiting for your reply", waiting_approval: "Waiting for your approval", waiting_client: "Waiting for a connected device", retrying: "Rechecking the operation receipt", waiting_retry: "Waiting to recheck the operation receipt", checking: "Checking the result", preparing: "Preparing an exact action", reconciling: "Checking an uncertain outcome" };
  return labels[type] || "Working";
}

function taskToolLabel(tool: string): string {
  const labels: Record<string, string> = { "notes.create": "Create note", "notes.append": "Update note", "files.create_folder": "Create folder", "files.upload": "Upload file", "attachments.list": "Inspecting attachments", "homeassistant.call_service": "Device change", "machines.services": "Inspecting allowed services", "machines.service_action": "Service change", "apps.invoke": "App request", "notes.search": "Searching notes", "notes.get": "Reading a note", "files.sources": "Finding file sources", "files.search": "Searching files", "files.list": "Listing folders", "files.stat": "Checking a file", "files.read": "Reading a file", "homeassistant.search": "Finding devices", "homeassistant.get": "Checking device state", "calendar.search": "Searching calendar", "calendar.get": "Reading an event", "machines.list": "Finding machines", "machines.status": "Checking a machine", "evidence.search": "Searching sources", "evidence.read": "Reading a source" };
  return labels[tool] || "Using a tool";
}

function taskStateLabel(task: HankAITask, activity?: string): string {
  if (task.state === "completed") return task.effects_uncertain ? "Finished · some action outcomes remain unverified" : "Completed";
  if (task.state === "failed") {
    if (task.error_code === "model_unavailable" || task.error_code === "capability_unavailable") return "The configured model could not complete this task. Check its availability and tool support.";
    if (task.error_code === "budget_exhausted") return "Task reached its execution limit. Send a narrower request to continue.";
    return "Task failed. Your request and progress have been saved.";
  }
  if (task.state === "cancelled") return "Stopped";
  if (task.state === "running") return activity || "Working";
  return taskEventLabel(task.state);
}

function ChatMessage({ message, onSelect }: { message: HankAIMessage; onSelect: (value: string) => void }) {
  const tool = message.diagnostics?.tool_kind || message.diagnostics?.intent_kind;
  return (
    <article className={`chat-msg ${message.role}${message.pending ? " pending" : ""}`}>
      <div className="chat-msg-role">{message.role === "user" ? "You" : "Hank"}</div>
      <div className="chat-bubble"><MessageText message={message} /></div>
      {message.sources?.length ? <ul aria-label="Sources">{message.sources.map((source) => {
        const href = safeSourceHref(source.href);
        return href ? <li key={source.uri}><a href={href}>{source.title || "Open source"}</a></li> : null;
      })}</ul> : null}
      {message.cards?.length ? (
        <div className="hank-result-list">
          {message.cards.map((card, index) => <ResultCard card={card} key={`${card.kind || "result"}-${card.note_id || card.path || card.event_id || card.media_option_id || index}`} onSelect={onSelect} />)}
        </div>
      ) : null}
      {message.role === "assistant" && tool ? <small className="chat-tool-meta">Used {tool}</small> : null}
    </article>
  );
}

function ActionSummary({ summary }: { summary?: HankAIPendingActionSummary }) {
  if (!summary) return null;
  return (
    <>
      {summary.summary ? <p>{summary.summary}</p> : null}
      {summary.details?.length ? (
        <dl className="hank-action-details">
          {summary.details.map((detail) => (
            <div key={`${detail.label}-${detail.value}`}><dt>{detail.label}</dt><dd>{detail.value}</dd></div>
          ))}
        </dl>
      ) : null}
    </>
  );
}

function PendingAction({ run, busy, onDecision }: { run: HankAIRun; busy: boolean; onDecision: (approved: boolean) => void }) {
  const summary = run.pending_action_summary;
  if (run.requires_confirmation) {
    const destructive = summary?.is_destructive === true;
    return (
      <section className={`hank-action-card${destructive ? " destructive" : ""}`} aria-label="Hank action approval" aria-live="polite">
        <div className="hank-action-heading">
          <span className="hank-action-icon" aria-hidden="true">{destructive ? "!" : "✓"}</span>
          <div><small>Approval required</small><strong>{summary?.title || "Confirm Hank action"}</strong></div>
        </div>
        <p className="hank-action-confirmation">{summary?.confirmation_message || run.assistant_message?.text || "Review this action before Hank continues."}</p>
        <ActionSummary summary={summary} />
        <div className="hank-action-buttons">
          <button type="button" className={destructive ? "danger" : ""} disabled={busy} onClick={() => onDecision(true)}>
            {destructive ? "Approve high-impact action" : "Approve"}
          </button>
          <button type="button" className="secondary" disabled={busy} onClick={() => onDecision(false)}>Cancel</button>
        </div>
      </section>
    );
  }
  if (run.requires_client_tools) {
    const toolName = run.client_tool_request?.tool_name || "a device action";
    return (
      <section className="hank-action-card handoff" aria-label="Hank client handoff" aria-live="polite">
        <div className="hank-action-heading">
          <span className="hank-action-icon" aria-hidden="true">↗</span>
          <div><small>Waiting on a Hank client</small><strong>Finish this action on a supported device</strong></div>
        </div>
        <p>Hank is waiting for <code>{toolName}</code>. Open this conversation in Hank on a device that supports the requested capability.</p>
        <ActionSummary summary={summary} />
      </section>
    );
  }
  return null;
}

export function HankAIPage() {
  const [state, setState] = useState<State>({ status: "loading" });
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const attachmentInputRef = useRef<HTMLInputElement>(null);
  const threadRef = useRef<HTMLDivElement>(null);
  const submittedAttachmentsRef = useRef(new Map<string, HankAIDraftAttachment>());
  const submittedAttachmentSessionsRef = useRef(new Map<string, string>());
  const dialog = useConfirmDialog();
  const submissionRef = useRef<{ session: string; content: string; id: string; taskID?: string; revision?: number } | null>(null);

  function setReady(update: Partial<ReadyState> | ((current: ReadyState) => Partial<ReadyState>)) {
    setState((current) => {
      if (current.status !== "ready") return current;
      const next = typeof update === "function" ? update(current) : update;
      return { ...current, ...next };
    });
  }

  async function load() {
    try {
      const [assistantStatus, sessionPayload, appsPayload] = await Promise.all([
        hankAIClient.status(),
        hankAIClient.listSessions(),
        appsClient.listApps().catch(() => ({ apps: [] })),
      ]);
      const sessions = sessionPayload.sessions || [];
      const requestedSession = new URLSearchParams(window.location.search).get("session");
      const selectedSessionID = sessions.find((session) => session.id === requestedSession)?.id || sessions[0]?.id || "";
      const messagePayload = selectedSessionID
        ? await hankAIClient.listMessages(selectedSessionID)
        : { messages: [] as HankAIMessage[], active_run: undefined, active_task: undefined, execution_version: 1 };
      setState({
        status: "ready",
        assistantStatus,
        sessions,
        selectedSessionID,
        messages: messagePayload.messages || [],
        draft: "",
        draftAttachments: [],
        notice: "",
        sending: false,
        preparingAttachments: false,
        slashCommands: slashCommandsForApps(appsPayload.apps),
        activeRun: messagePayload.active_run,
        activeTask: messagePayload.active_task,
        executionVersion: messagePayload.execution_version || 1,

      });
      if (messagePayload.active_run?.requires_client_tools && messagePayload.active_run.client_tool_request?.tool_name === "attachments.commit") {
        void continueRun(messagePayload.active_run, selectedSessionID);
      }
    } catch (error) {
      setState({ status: "error", message: errorMessage(error) });
    }
  }

  useEffect(() => {
    void load();
  }, []);

  const messageCount = state.status === "ready" ? state.messages.length : 0;
  const activeRunID = state.status === "ready" ? state.activeRun?.id : undefined;
  useEffect(() => {
    if (!threadRef.current) return;
    threadRef.current.scrollTop = threadRef.current.scrollHeight;
  }, [activeRunID, messageCount]);

  const taskID = state.status === "ready" ? state.activeTask?.task_id : undefined;
  const taskSessionID = state.status === "ready" ? state.selectedSessionID : "";
  const taskIsTerminal = state.status === "ready" && state.activeTask ? terminalTask(state.activeTask) : true;
  useEffect(() => {
    if (!taskID || taskIsTerminal) return;
    let active = true;
    let cursor = 0;
    let revision = -1;
    let timer: number | undefined;
    const controller = new AbortController();
    async function poll() {
      try {
        const [task, events] = await Promise.all([
          hankAIClient.getTask(taskID!, controller.signal),
          hankAIClient.taskEvents(taskID!, cursor, controller.signal),
        ]);
        if (!active) return;
        cursor = events.next_sequence;
        const messages = task.revision !== revision && (terminalTask(task) || task.state === "waiting_input" || events.events.some((event) => event.type === "queued"))
          ? await hankAIClient.listMessages(taskSessionID) : undefined;
        if (!active) return;
        revision = task.revision;
        setReady((current) => current.selectedSessionID === taskSessionID && current.activeTask?.task_id === taskID ? {
          activeTask: task,
          ...(events.events.length ? { taskActivity: taskEventLabel(events.events[events.events.length - 1].type) } : {}),
          ...(messages ? { messages: messages.messages } : {}),
        } : {});
        if (!terminalTask(task)) timer = window.setTimeout(() => void poll(), events.has_more ? 0 : 1000);
      } catch (error) {
        if (!active) return;
        setReady((current) => current.selectedSessionID === taskSessionID ? { notice: errorMessage(error) } : {});
        timer = window.setTimeout(() => void poll(), 3000);
      }
    }
    void poll();
    return () => { active = false; controller.abort(); window.clearTimeout(timer); };
  }, [taskID, taskSessionID, taskIsTerminal]);

  async function decideTaskApproval(approved: boolean) {
    if (state.status !== "ready" || !state.activeTask?.pending_approval) return;
    const task = state.activeTask;
    const approval = task.pending_approval!;
    setReady({ sending: true, notice: "" });
    try {
      const updated = await hankAIClient.decideTaskApproval(task.task_id, approval.approval_id, approval.revision, approval.action_digest, approved);
      setReady((current) => current.activeTask?.task_id === task.task_id ? { activeTask: updated } : {});
    } catch (error) {
      setReady({ notice: errorMessage(error) });
      try {
        const latest = await hankAIClient.getTask(task.task_id);
        setReady((current) => current.activeTask?.task_id === task.task_id ? { activeTask: latest } : {});
      } catch { /* The next poll can recover a temporarily unavailable task. */ }
    } finally { setReady({ sending: false }); }
  }

  async function stopTask() {
    if (state.status !== "ready" || !state.activeTask) return;
    const id = state.activeTask.task_id;
    try {
      const task = await hankAIClient.stopTask(id);
      setReady((current) => current.activeTask?.task_id === id ? { activeTask: task, taskActivity: undefined } : {});
    } catch (error) { setReady({ notice: errorMessage(error) }); }
  }

  async function refreshSession(sessionID: string, notice?: string): Promise<HankAIRun | undefined> {
    const [sessionPayload, messagePayload] = await Promise.all([
      hankAIClient.listSessions(),
      hankAIClient.listMessages(sessionID),
    ]);
    setReady((current) => ({
      sessions: sessionPayload.sessions || [],
      ...(current.selectedSessionID === sessionID ? {
        messages: messagePayload.messages || [],
        activeRun: messagePayload.active_run,
        activeTask: messagePayload.active_task,
        executionVersion: messagePayload.execution_version || 1,

        ...(notice !== undefined ? { notice } : {}),
      } : {}),
    }));
    return messagePayload.active_run;
  }

  async function continueRun(initialRun: HankAIRun, sessionID: string, previousSummary?: HankAIPendingActionSummary): Promise<void> {
    let run = previousSummary && !initialRun.pending_action_summary ? { ...initialRun, pending_action_summary: previousSummary } : initialRun;
    for (let attempt = 0; attempt < 20; attempt += 1) {
      if (run.requires_confirmation) {
        await refreshSession(sessionID);
        setReady((current) => current.selectedSessionID === sessionID ? { activeRun: run, notice: "Review the action before Hank continues." } : {});
        return;
      }
      if (run.requires_client_tools) {
        const request = run.client_tool_request;
        if (request?.tool_name === "attachments.commit") {
          setReady({ notice: "Storing the attachment…" });
          const result = await executeHankAIClientTool(request, submittedAttachmentsRef.current);
          for (const attachmentID of submittedAttachmentSessionsRef.current.keys()) {
            if (!submittedAttachmentsRef.current.has(attachmentID)) submittedAttachmentSessionsRef.current.delete(attachmentID);
          }
          run = await hankAIClient.submitClientToolResult(run.id, result);
          continue;
        }
        await refreshSession(sessionID);
        setReady((current) => current.selectedSessionID === sessionID ? {
          activeRun: run,
          notice: "This action is waiting for a supported Hank client.",
        } : {});
        return;
      }
      if (terminalRun(run)) {
        await refreshSession(sessionID, run.state === "completed" ? "Done." : `Run ${run.state}.`);
        setReady((current) => current.selectedSessionID === sessionID ? { activeRun: undefined } : {});
        return;
      }
      await new Promise((resolve) => window.setTimeout(resolve, 500));
      run = await hankAIClient.getRun(run.id);
    }
    setReady((current) => current.selectedSessionID === sessionID ? { activeRun: run, notice: "Hank is still working on this request." } : {});
  }

  async function selectSession(sessionID: string) {
    if (state.status !== "ready") return;
    try {
      const payload = await hankAIClient.listMessages(sessionID);
      setReady({ selectedSessionID: sessionID, messages: payload.messages || [], activeRun: payload.active_run, activeTask: payload.active_task, executionVersion: payload.execution_version || 1, taskActivity: undefined, notice: "" });
      if (payload.active_run?.requires_client_tools && payload.active_run.client_tool_request?.tool_name === "attachments.commit") {
        setReady({ sending: true });
        try {
          await continueRun(payload.active_run, sessionID);
        } finally {
          setReady({ sending: false });
        }
      }
    } catch (error) {
      setReady({ notice: errorMessage(error) });
    }
  }

  async function createSession() {
    try {
      const session = await hankAIClient.createSession();
      setReady((current) => ({
        sessions: [session, ...current.sessions.filter((candidate) => candidate.id !== session.id)],
        selectedSessionID: session.id,
        messages: [],
        activeRun: undefined, activeTask: undefined, executionVersion: 1, taskActivity: undefined,

        notice: "",
      }));
      requestAnimationFrame(() => textareaRef.current?.focus());
    } catch (error) {
      setReady({ notice: errorMessage(error) });
    }
  }

  async function deleteSession(session: HankAISession) {
    if (state.status !== "ready") return;
    const title = sessionTitle(session);
    const confirmed = await dialog.confirm({
      title: "Delete conversation",
      message: `Delete ${title}? This cannot be undone.`,
      confirmLabel: "Delete",
      tone: "danger",
    });
    if (!confirmed) return;
    try {
      await hankAIClient.deleteSession(session.id);
      for (const [attachmentID, ownerSessionID] of submittedAttachmentSessionsRef.current) {
        if (ownerSessionID !== session.id) continue;
        submittedAttachmentSessionsRef.current.delete(attachmentID);
        submittedAttachmentsRef.current.delete(attachmentID);
      }
      const remaining = state.sessions.filter((candidate) => candidate.id !== session.id);
      const changedSelection = state.selectedSessionID === session.id;
      const nextSessionID = changedSelection ? remaining[0]?.id || "" : state.selectedSessionID;
      setReady({
        sessions: remaining,
        selectedSessionID: nextSessionID,
        messages: changedSelection ? [] : state.messages,
        activeRun: changedSelection ? undefined : state.activeRun,
        activeTask: changedSelection ? undefined : state.activeTask,
        executionVersion: changedSelection ? 1 : state.executionVersion,

        notice: "Conversation deleted.",
      });
      if (changedSelection && nextSessionID) {
        const payload = await hankAIClient.listMessages(nextSessionID);
        setReady((current) => current.selectedSessionID === nextSessionID
          ? { messages: payload.messages || [], activeRun: payload.active_run, activeTask: payload.active_task, executionVersion: payload.execution_version || 1 }
          : {});
      }
    } catch (error) {
      setReady({ notice: errorMessage(error) });
    }
  }

  async function sendMessage(contentOverride?: string) {
    if (state.status !== "ready" || state.sending || state.activeRun) return;
    const fromComposer = contentOverride === undefined;
    const attachments = fromComposer ? [...state.draftAttachments] : [];
    const typedContent = String(contentOverride ?? state.draft).trim();
    const content = typedContent || attachmentOnlyMessageText(attachments);
    if (!content || (!typedContent && attachments.length === 0)) return;
    let sessionID = state.selectedSessionID;
    let accepted = false;
    const optimisticID = `pending-${Date.now()}-${Math.random().toString(16).slice(2)}`;
    setReady({ sending: true, notice: "" });
    try {
      if (!sessionID) {
        const session = await hankAIClient.createSession();
        sessionID = session.id;
        setReady((current) => ({
          sessions: [session, ...current.sessions.filter((candidate) => candidate.id !== session.id)],
          selectedSessionID: session.id,
          messages: [],
        }));
      }
      const userMessage: HankAIMessage = {
        id: optimisticID,
        role: "user",
        text: content,
        created_at: new Date().toISOString(),
        pending: true,
      };
      const targetSessionID = sessionID;
      setReady((current) => current.selectedSessionID === targetSessionID ? {
        draft: fromComposer ? "" : current.draft,
        draftAttachments: fromComposer ? [] : current.draftAttachments,
        messages: [...current.messages, userMessage],
      } : {});
      for (const attachment of attachments) {
        await hankAIClient.stageTaskAttachment(sessionID, attachmentPayload(attachment), attachment.file);
      }
      if (submissionRef.current?.session !== sessionID || submissionRef.current.content !== content) {
        submissionRef.current = { session: sessionID, content, id: crypto.randomUUID(), ...(state.activeTask && !terminalTask(state.activeTask) ? { taskID: state.activeTask.task_id, revision: state.activeTask.revision } : {}) };
      }
      const submission = submissionRef.current;
      const task = submission.taskID
        ? await hankAIClient.followupTask(submission.taskID, content, submission.id, submission.revision!)
        : await hankAIClient.submitTask(sessionID, content, submission.id);
      accepted = true;
      submissionRef.current = null;
      setReady((current) => current.selectedSessionID === targetSessionID ? { activeTask: task, executionVersion: 2 } : {});
      await refreshSession(sessionID);
    } catch (error) {
      if (!accepted && error instanceof ApiError && error.status === 409 && submissionRef.current) {
        const pending = submissionRef.current;
        if (pending.taskID) {
          const current = await hankAIClient.getTask(pending.taskID).catch(() => undefined);
          if (current) {
            pending.revision = current.revision;
            setReady((ready) => ready.selectedSessionID === sessionID ? { activeTask: current } : {});
          }
        } else {
          submissionRef.current = null;
          await refreshSession(sessionID).catch(() => undefined);
        }
      }
      if (!accepted) {
        const targetSessionID = sessionID;
        setReady((current) => current.selectedSessionID === targetSessionID ? {
          messages: current.messages.filter((message) => message.id !== optimisticID),
          draft: fromComposer && !current.draft ? typedContent : current.draft,
          draftAttachments: fromComposer && current.draftAttachments.length === 0 ? attachments : current.draftAttachments,
        } : {});
      } else if (sessionID) {
        await refreshSession(sessionID).catch(() => undefined);
      }
      setReady({ notice: errorMessage(error) });
    } finally {
      setReady({ sending: false });
    }
  }

  async function decidePendingAction(approved: boolean) {
    if (state.status !== "ready" || !state.activeRun?.requires_confirmation || state.sending) return;
    const run = state.activeRun;
    const sessionID = state.selectedSessionID;
    setReady({ sending: true, notice: approved ? "Running the approved action…" : "Cancelling the action…" });
    try {
      const nextRun = await hankAIClient.confirmRun(run.id, approved);
      if (approved) {
        await continueRun(nextRun, sessionID, run.pending_action_summary);
      } else {
        await refreshSession(sessionID, "Action cancelled.");
        setReady({ activeRun: undefined });
      }
    } catch (error) {
      setReady({ notice: errorMessage(error) });
    } finally {
      setReady({ sending: false });
    }
  }

  async function addFiles(files: FileList | null) {
    const selected = Array.from(files || []);
    if (!selected.length) return;
    setReady({ preparingAttachments: true, notice: "Preparing attachment metadata…" });
    try {
      const staged = await stageHankAIAttachments(selected);
      setReady((current) => ({
        draftAttachments: [...current.draftAttachments, ...staged.attachments],
        notice: staged.rejected.length
          ? `${staged.rejected.join(", ")} exceeded the 100 MB attachment limit.`
          : `${staged.attachments.length} attachment${staged.attachments.length === 1 ? "" : "s"} ready.`,
      }));
    } catch (error) {
      setReady({ notice: errorMessage(error) });
    } finally {
      setReady({ preparingAttachments: false });
      if (attachmentInputRef.current) attachmentInputRef.current.value = "";
    }
  }

  function removeDraftAttachment(id: string) {
    setReady((current) => ({ draftAttachments: current.draftAttachments.filter((attachment) => attachment.id !== id) }));
  }

  function applyCommand(token: string) {
    setReady({ draft: `${token} ` });
    requestAnimationFrame(() => textareaRef.current?.focus());
  }

  if (state.status === "loading") {
    return (
      <section className="dashboard-page" aria-labelledby="route-title">
        <p className="eyebrow">Hank</p>
        <h1 id="route-title">Chat</h1>
        <p className="loading-state"><span className="spinner" aria-hidden="true" />Loading Hank Chat…</p>
      </section>
    );
  }

  if (state.status === "error") {
    return (
      <section className="dashboard-page" aria-labelledby="route-title">
        <p className="eyebrow">Hank</p>
        <h1 id="route-title">Chat</h1>
        <p className="error-state">{state.message}</p>
        <button type="button" className="secondary" onClick={() => { setState({ status: "loading" }); void load(); }}>Try again</button>
      </section>
    );
  }

  const provider = state.assistantStatus.provider || "local tools";
  const model = state.assistantStatus.chat_model || "";
  const chatConfigured = state.assistantStatus.chat_configured ?? state.assistantStatus.ready ?? false;
  const awaitingApproval = state.activeRun?.requires_confirmation === true;
  const awaitingClient = state.activeRun?.requires_client_tools === true;
  const blockedByAction = awaitingApproval || awaitingClient;
  const taskRunning = Boolean(state.activeTask && !terminalTask(state.activeTask));
  const assistantStateLabel = taskRunning ? (state.activeTask?.state === "waiting_input" ? "Needs reply" : "Working") : state.sending
    ? "Working"
    : awaitingApproval
      ? "Needs approval"
      : awaitingClient
        ? "Waiting on device"
        : chatConfigured
          ? "Ready"
          : "Hank tools ready";
  const draftTrimmed = state.draft.trim();
  const showPalette = draftTrimmed.startsWith("/") && !draftTrimmed.includes(" ");
  const paletteMatches = showPalette
    ? state.slashCommands.filter((command) => command.token.toLowerCase().startsWith(draftTrimmed.toLowerCase()))
    : [];

  return (
    <section className={`dashboard-page hank-page${state.notice ? " has-notice" : ""}`} aria-labelledby="route-title">
      <header className="dashboard-header">
        <div>
          <p className="eyebrow">Hank</p>
          <h1 id="route-title">Chat</h1>
        </div>
        <div className="hank-chat-header-actions">
          <span
            className={`status-pill hank-assistant-status ${chatConfigured && !state.sending && !blockedByAction && !taskRunning ? "status-online" : ""}`}
            aria-label={`Assistant status: ${assistantStateLabel}, ${provider}${model ? `, ${model}` : ""}`}
          >
            <span className={`status-dot ${state.sending || blockedByAction || taskRunning ? "warn" : "ok"}`} aria-hidden="true" />
            <span>{state.sending ? "Working…" : assistantStateLabel}</span>
            <small>{provider}{model ? ` · ${model}` : ""}</small>
          </span>
        </div>
      </header>

      {state.notice ? <p className="notice-state" role="status">{state.notice}</p> : null}

      <div className="hank-layout">
        <section className="settings-panel conversations-panel" aria-label="Conversations">
          <div className="panel-heading">
            <h2>Conversations</h2>
            <button type="button" className="secondary" disabled={state.sending} onClick={() => void createSession()}>New chat</button>
          </div>
          {state.sessions.length ? (
            <div className="conversation-list">
              {state.sessions.map((session) => (
                <SwipeActionRow key={session.id} actions={
                  <button className="icon-button danger conversation-delete" type="button" aria-label={`Delete ${sessionTitle(session)}`} title="Delete conversation" disabled={state.sending} onClick={() => void deleteSession(session)}>
                    <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" aria-hidden="true"><path d="M4 7h16M9 7V4h6v3M6 7l1 14h10l1-14M10 11v6M14 11v6" /></svg>
                  </button>
                }>
                  <button
                    aria-label={sessionTitle(session)}
                    className={session.id === state.selectedSessionID ? "conversation-select active" : "conversation-select"}
                    aria-current={session.id === state.selectedSessionID ? "true" : undefined}
                    onClick={() => void selectSession(session.id)}
                    type="button"
                  >
                    <strong>{sessionTitle(session)}</strong>
                    <span>{formatSessionTime(session.updated_at || session.last_message_at)}</span>
                  </button>
                </SwipeActionRow>
              ))}
            </div>
          ) : (
            <p className="empty-state">No conversations yet.</p>
          )}
        </section>

        <section className={`settings-panel chat-panel${blockedByAction ? " has-active-action" : ""}`} aria-label="Conversation">
          <div className="chat-panel-header">
            <h2>{sessionTitle(state.sessions.find((session) => session.id === state.selectedSessionID) || { id: "" })}</h2>
          </div>
          <div className="chat-thread" ref={threadRef}>
            {state.messages.length ? (
              state.messages.map((message, index) => (
                <ChatMessage message={message} key={message.id || `${message.role}-${index}`} onSelect={(value) => void sendMessage(value)} />
              ))
            ) : (
              <div className="chat-empty">
                <strong>Ask Hank to help</strong>
                <span>Ask about Hank, your notes, files, calendar, devices, agents, or backups. Hank will show a review step before actions that need approval.</span>
                <span>Type <code>/</code> for direct commands.</span>
              </div>
            )}
          </div>

          {state.activeTask ? (
            <div className="hank-task-activity" aria-label="Task activity">
              <p role="status">{taskStateLabel(state.activeTask, state.taskActivity)}</p>
              {state.activeTask.effects_uncertain ? <p>An action may have happened. Its outcome still needs checking.</p> : null}
              {state.activeTask.calls?.length ? <ul>{state.activeTask.calls.map((call) => <li key={call.call_id}>{taskToolLabel(call.tool)} · {call.state.replaceAll("_", " ")}</li>)}</ul> : null}
              {!terminalTask(state.activeTask) ? <button type="button" className="secondary" onClick={() => void stopTask()}>Stop task</button> : null}
            </div>
          ) : null}
          {state.activeTask?.pending_approval ? <PendingAction run={{ id: state.activeTask.task_id, state: "waiting_confirmation", requires_confirmation: true, pending_action_summary: state.activeTask.pending_approval.summary }} busy={state.sending} onDecision={(approved) => void decideTaskApproval(approved)} /> : null}
          {state.activeRun ? <PendingAction run={state.activeRun} busy={state.sending} onDecision={(approved) => void decidePendingAction(approved)} /> : null}

          <form className="chat-composer" onSubmit={(event) => { event.preventDefault(); void sendMessage(); }}>
            {state.draftAttachments.length ? (
              <div className="hank-attachment-tray" aria-label="Attachments ready to send">
                {state.draftAttachments.map((attachment) => (
                  <div className="hank-attachment-chip" key={attachment.id}>
                    <span aria-hidden="true">{attachment.kind === "image" ? "IMG" : attachment.kind === "pdf" ? "PDF" : "DOC"}</span>
                    <div><strong>{attachment.filename}</strong><small>{formatHankAIBytes(attachment.size_bytes)}</small></div>
                    <button type="button" className="icon-button" aria-label={`Remove ${attachment.filename}`} onClick={() => removeDraftAttachment(attachment.id)}>×</button>
                  </div>
                ))}
              </div>
            ) : null}
            {showPalette && paletteMatches.length ? (
              <div className="cmd-palette" role="listbox" aria-label="Slash commands">
                {paletteMatches.map((command) => (
                  <button
                    type="button"
                    role="option"
                    aria-selected={false}
                    key={command.token}
                    className="cmd-item"
                    onMouseDown={(event) => { event.preventDefault(); applyCommand(command.token); }}
                  >
                    <span className="cmd-token">{command.token}</span>
                    <span className="cmd-hint">{command.hint}</span>
                  </button>
                ))}
              </div>
            ) : null}
            <div className="composer-bar">
              <button
                type="button"
                className="secondary hank-attach-button"
                aria-label="Attach files"
                title="Attach files"
                disabled={state.sending || state.preparingAttachments || blockedByAction}
                onClick={() => attachmentInputRef.current?.click()}
              >
                +
              </button>
              <input ref={attachmentInputRef} className="visually-hidden" type="file" multiple aria-label="Choose files for Hank" onChange={(event) => void addFiles(event.currentTarget.files)} />
              <textarea
                ref={textareaRef}
                aria-label="Message"
                placeholder={blockedByAction ? "Finish or cancel the pending action first" : "Ask Hank anything…  (type / for commands)"}
                rows={1}
                value={state.draft}
                disabled={state.sending || blockedByAction}
                onChange={(event) => setReady({ draft: event.target.value })}
                onKeyDown={(event) => {
                  if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) {
                    event.preventDefault();
                    void sendMessage();
                  }
                }}
              />
              <button disabled={state.sending || state.preparingAttachments || blockedByAction || (!state.draft.trim() && state.draftAttachments.length === 0)} type="submit" className="composer-send">{state.activeTask && !terminalTask(state.activeTask) ? "Send follow-up" : "Send"}</button>
            </div>
          </form>
        </section>
      </div>
    </section>
  );
}
