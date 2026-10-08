import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConfirmDialogProvider } from "../ui/primitives";
import { HankAIPage } from "./HankAIPage";

const hankAIClient = vi.hoisted(() => ({
  status: vi.fn(), stageTaskAttachment: vi.fn(),
  decideTaskApproval: vi.fn(), submitTask: vi.fn(), getTask: vi.fn(), taskEvents: vi.fn(), stopTask: vi.fn(), followupTask: vi.fn(),
  listSessions: vi.fn(),
  listMessages: vi.fn(),
  createSession: vi.fn(),
  sendMessage: vi.fn(),
  getRun: vi.fn(),
  confirmRun: vi.fn(),
  submitClientToolResult: vi.fn(),
  discardAttachment: vi.fn(),
  deleteSession: vi.fn(),
}));

const appsClient = vi.hoisted(() => ({
  listApps: vi.fn(),
}));

vi.mock("../api/hankAI", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api/hankAI")>();
  return {
    ...actual,
    hankAIClient,
  };
});

vi.mock("../api/apps", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api/apps")>();
  return {
    ...actual,
    appsClient,
  };
});

describe("HankAIPage", () => {
  beforeEach(() => {
    appsClient.listApps.mockResolvedValue({ apps: [] });
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it.each([false, true])("always submits agent tasks, including legacy history (%s)", async (legacy) => {
    const task = { schema_version: 2, task_id: "always-agent", session_id: "s1", state: "completed", revision: 1, last_event_sequence: 1, cancel_requested: false };
    hankAIClient.status.mockResolvedValue({ provider: "ollama", chat_configured: true, execution_versions: [1, 2] });
    hankAIClient.listSessions.mockResolvedValue({ sessions: [{ id: "s1", title: "New Conversation" }] });
    hankAIClient.listMessages.mockResolvedValue({ messages: legacy ? [{ role: "assistant", text: "Existing answer" }] : [], execution_version: 1 });
    hankAIClient.submitTask.mockResolvedValue(task);
    render(<ConfirmDialogProvider><HankAIPage /></ConfirmDialogProvider>);
    const composer = await screen.findByRole("textbox", { name: "Message" });
    expect(screen.queryByRole("checkbox", { name: /Agent mode/ })).not.toBeInTheDocument();
    expect(document.querySelector(".chat-panel-header")).toHaveTextContent(/^New chat$/);
    if (legacy) expect(screen.getByText("Existing answer")).toBeInTheDocument();
    fireEvent.change(composer, { target: { value: "Find my notes" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(hankAIClient.submitTask).toHaveBeenCalledWith("s1", "Find my notes", expect.any(String)));
    expect(hankAIClient.sendMessage).not.toHaveBeenCalled();
  });

  it("keeps the draft and never falls back when agent execution is unavailable", async () => {
    hankAIClient.status.mockResolvedValue({ provider: "ollama", chat_configured: true, execution_versions: [1] });
    hankAIClient.listSessions.mockResolvedValue({ sessions: [{ id: "s1", title: "Chat" }] });
    hankAIClient.listMessages.mockResolvedValue({ messages: [], execution_version: 1 });
    hankAIClient.submitTask.mockRejectedValueOnce(new Error("Agent execution unavailable"));
    render(<ConfirmDialogProvider><HankAIPage /></ConfirmDialogProvider>);
    const composer = await screen.findByRole("textbox", { name: "Message" });
    fireEvent.change(composer, { target: { value: "Keep this request" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    expect(await screen.findByText("Agent execution unavailable")).toBeInTheDocument();
    expect(composer).toHaveValue("Keep this request");
    expect(hankAIClient.sendMessage).not.toHaveBeenCalled();
  });

  it("stages a v2 attachment before submitting its task", async () => {
    const task = { schema_version: 2, task_id: "upload-task", session_id: "s1", state: "completed", revision: 3, last_event_sequence: 3, cancel_requested: false };
    hankAIClient.status.mockResolvedValue({ provider: "ollama", chat_configured: true, execution_versions: [1, 2] });
    hankAIClient.listSessions.mockResolvedValue({ sessions: [{ id: "s1", title: "Uploads" }] });
    hankAIClient.listMessages.mockResolvedValue({ messages: [], execution_version: 2 });
    hankAIClient.stageTaskAttachment.mockResolvedValue({ id: "stage-1" });
    hankAIClient.submitTask.mockResolvedValue(task);
    render(<ConfirmDialogProvider><HankAIPage /></ConfirmDialogProvider>);
    const composer = await screen.findByRole("textbox", { name: "Message" });
    const file = new File(["synthetic"], "fixture.txt", { type: "text/plain" });
    Object.defineProperty(file, "arrayBuffer", { value: async () => new TextEncoder().encode("synthetic").buffer });
    fireEvent.change(screen.getByLabelText("Choose files for Hank"), { target: { files: [file] } });
    await screen.findByText("1 attachment ready.");
    fireEvent.change(composer, { target: { value: "Upload this to my chosen folder" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(hankAIClient.submitTask).toHaveBeenCalled());
    expect(hankAIClient.stageTaskAttachment).toHaveBeenCalledWith("s1", expect.objectContaining({ filename: "fixture.txt", size_bytes: 9 }), file);
    expect(hankAIClient.stageTaskAttachment.mock.invocationCallOrder[0]).toBeLessThan(hankAIClient.submitTask.mock.invocationCallOrder[0]);
    expect(hankAIClient.sendMessage).not.toHaveBeenCalled();
  });

  it("shows the exact durable proposal and approves only its digest and revision", async () => {
    const pending = { approval_id: "approval-1", action_digest: "exact-digest", revision: 7, expires_at: "2030-01-01T00:00:00Z", summary: { kind: "note_create", title: "Create note", confirmation_message: "Review the exact content", details: [{ label: "Exact content", value: "  Exact body\n" }] } };
    const task = { schema_version: 2, task_id: "approved-task", session_id: "s1", state: "waiting_approval", revision: 7, last_event_sequence: 7, cancel_requested: false, pending_approval: pending };
    hankAIClient.status.mockResolvedValue({ provider: "ollama", chat_configured: true, execution_versions: [1, 2] });
    hankAIClient.listSessions.mockResolvedValue({ sessions: [{ id: "s1", title: "Exact note" }] });
    hankAIClient.listMessages.mockResolvedValue({ messages: [], active_task: task, execution_version: 2 });
    hankAIClient.getTask.mockResolvedValue(task);
    hankAIClient.taskEvents.mockResolvedValue({ events: [], next_sequence: 7, has_more: false });
    hankAIClient.decideTaskApproval.mockImplementation(async () => {
      const updated = { ...task, state: "queued", revision: 8, pending_approval: undefined };
      hankAIClient.getTask.mockResolvedValue(updated);
      return updated;
    });
    render(<ConfirmDialogProvider><HankAIPage /></ConfirmDialogProvider>);
    const approve = await screen.findByRole("button", { name: /^Approve$/ });
    expect(screen.getByText("Exact body").textContent).toBe("  Exact body\n");
    fireEvent.click(approve);
    await waitFor(() => expect(hankAIClient.decideTaskApproval).toHaveBeenCalledWith("approved-task", "approval-1", 7, "exact-digest", true));
    await waitFor(() => expect(screen.queryByRole("button", { name: /^Approve$/ })).not.toBeInTheDocument());
    expect(hankAIClient.confirmRun).not.toHaveBeenCalled();
  });

  it("recovers a durable task and stops it after refresh", async () => {
    const task = { schema_version: 2, task_id: "task-1", session_id: "s1", state: "waiting_input", revision: 3, last_event_sequence: 4, cancel_requested: false };
    hankAIClient.status.mockResolvedValue({ provider: "ollama", chat_configured: true, execution_versions: [1, 2] });
    hankAIClient.listSessions.mockResolvedValue({ sessions: [{ id: "s1", title: "Durable conversation" }] });
    hankAIClient.listMessages.mockResolvedValue({ messages: [{ role: "assistant", text: "Which folder?" }], active_task: task, execution_version: 2 });
    hankAIClient.getTask.mockResolvedValue(task);
    hankAIClient.taskEvents.mockResolvedValue({ events: [], next_sequence: 4, has_more: false });
    hankAIClient.stopTask.mockResolvedValue({ ...task, state: "cancelled", cancel_requested: true, revision: 4 });
    render(<ConfirmDialogProvider><HankAIPage /></ConfirmDialogProvider>);
    expect(await screen.findByRole("button", { name: "Send follow-up" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Stop task" }));
    expect(await screen.findByText("Stopped")).toBeInTheDocument();
    expect(hankAIClient.stopTask).toHaveBeenCalledWith("task-1");
    expect(hankAIClient.sendMessage).not.toHaveBeenCalled();
  });

  it("sends a follow-up to the durable task with its revision", async () => {
    const task = { schema_version: 2, task_id: "task-2", session_id: "s1", state: "waiting_input", revision: 7, last_event_sequence: 8, cancel_requested: false };
    hankAIClient.status.mockResolvedValue({ provider: "ollama", chat_configured: true, execution_versions: [1, 2] });
    hankAIClient.listSessions.mockResolvedValue({ sessions: [{ id: "s1", title: "Folders" }] });
    hankAIClient.listMessages.mockResolvedValue({ messages: [], active_task: task, execution_version: 2 });
    hankAIClient.getTask.mockResolvedValue(task);
    hankAIClient.taskEvents.mockResolvedValue({ events: [], next_sequence: 8, has_more: false });
    hankAIClient.followupTask.mockResolvedValue({ ...task, state: "queued", revision: 8 });
    render(<ConfirmDialogProvider><HankAIPage /></ConfirmDialogProvider>);
    const composer = await screen.findByRole("textbox", { name: "Message" });
    fireEvent.change(composer, { target: { value: "Use the second folder" } });
    fireEvent.click(screen.getByRole("button", { name: "Send follow-up" }));
    await waitFor(() => expect(hankAIClient.followupTask).toHaveBeenCalledWith("task-2", "Use the second folder", expect.any(String), 7));
    expect(hankAIClient.sendMessage).not.toHaveBeenCalled();
  });

  it("reuses the submission ID after a lost response and renders verified source links", async () => {
    const task = { schema_version: 2, task_id: "task-3", session_id: "s1", state: "completed", revision: 5, last_event_sequence: 5, cancel_requested: false };
    hankAIClient.status.mockResolvedValue({ provider: "ollama", chat_configured: true, execution_versions: [1, 2] });
    hankAIClient.listSessions.mockResolvedValue({ sessions: [{ id: "s1", title: "Notes" }] });
    hankAIClient.listMessages.mockResolvedValue({ messages: [], execution_version: 1 });
    hankAIClient.submitTask.mockRejectedValueOnce(new Error("Response lost")).mockResolvedValue(task);
    render(<ConfirmDialogProvider><HankAIPage /></ConfirmDialogProvider>);
    const composer = await screen.findByRole("textbox", { name: "Message" });
    expect(screen.queryByRole("checkbox", { name: /Agent mode/ })).not.toBeInTheDocument();
    fireEvent.change(composer, { target: { value: "Find my lunch note" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    expect(await screen.findByText("Response lost")).toBeInTheDocument();
    expect(composer).toHaveValue("Find my lunch note");
    hankAIClient.listMessages.mockResolvedValue({ messages: [{ role: "assistant", text: "Found [Lunch](hank://notes/n1).", sources: [{ title: "Lunch source", uri: "hank://notes/n1", href: "/dashboard/profile-notes?note=n1" }] }], execution_version: 2, active_task: task });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    expect(await screen.findByRole("link", { name: "Lunch" })).toHaveAttribute("href", "/dashboard/profile-notes?note=n1");
    const calls = hankAIClient.submitTask.mock.calls;
    expect(calls[0][2]).toBe(calls[1][2]);
    expect(hankAIClient.sendMessage).not.toHaveBeenCalled();
  });

  it("does not show synthetic workflow logs or fake attachment data", async () => {
    hankAIClient.status.mockResolvedValue({ provider: "gpt-4o", ready: true });
    hankAIClient.listSessions.mockResolvedValue({
      sessions: [{ id: "s1", title: "Weekend grocery plan", last_message_at: "now" }],
    });
    hankAIClient.listMessages.mockResolvedValue({
      messages: [{ role: "assistant", text: "Storage looks good for the backup." }],
    });

    render(<ConfirmDialogProvider><HankAIPage /></ConfirmDialogProvider>);

    expect(await screen.findByRole("button", { name: "Weekend grocery plan" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Workflow logs" })).not.toBeInTheDocument();
    expect(screen.queryByRole("complementary", { name: "Workflow logs" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Attach files" })).toBeInTheDocument();
    expect(screen.queryByText("lease-2026.pdf")).not.toBeInTheDocument();
  });

  it("keeps conversations accessible without a separate show button", async () => {
    hankAIClient.status.mockResolvedValue({ provider: "gpt-5-codex", ready: true });
    hankAIClient.listSessions.mockResolvedValue({ sessions: [] });

    render(<ConfirmDialogProvider><HankAIPage /></ConfirmDialogProvider>);

    expect(await screen.findByRole("textbox", { name: "Message" })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Conversations" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New chat" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Show conversations" })).not.toBeInTheDocument();
  });

  it("uses one compact provider and readiness status", async () => {
    hankAIClient.status.mockResolvedValue({ provider: "ollama", chat_configured: true, chat_model: "qwen3:14b" });
    hankAIClient.listSessions.mockResolvedValue({ sessions: [] });

    render(<ConfirmDialogProvider><HankAIPage /></ConfirmDialogProvider>);

    expect(await screen.findByLabelText("Assistant status: Ready, ollama, qwen3:14b")).toBeInTheDocument();
    expect(screen.getAllByText("Ready")).toHaveLength(1);
    expect(screen.getByText("ollama · qwen3:14b")).toBeInTheDocument();
  });

  it("shows live conversation messages without canned tool review cards", async () => {
    hankAIClient.status.mockResolvedValue({ provider: "gpt-5-codex", ready: true });
    hankAIClient.listSessions.mockResolvedValue({
      sessions: [
        { id: "s1", title: "Weekend grocery plan", last_message_at: "now" },
        { id: "s2", title: "Lower thermostat at night", last_message_at: "yesterday" },
        { id: "s3", title: "Find the lease PDF", last_message_at: "Monday" },
      ],
    });
    hankAIClient.listMessages.mockResolvedValue({
      messages: [
        { role: "user", text: "Can you add taco night groceries to the shared list?" },
        { role: "assistant", text: "I found the shared grocery list and staged the taco night items." },
      ],
    });

    render(<ConfirmDialogProvider><HankAIPage /></ConfirmDialogProvider>);

    expect(await screen.findByRole("button", { name: "Weekend grocery plan" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Lower thermostat at night" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Find the lease PDF" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Weekend grocery plan" })).toBeInTheDocument();
    expect(screen.getByText("Can you add taco night groceries to the shared list?")).toBeInTheDocument();
    expect(screen.getByText("I found the shared grocery list and staged the taco night items.")).toBeInTheDocument();
    expect(screen.queryByText("Search shared grocery list")).not.toBeInTheDocument();
    expect(screen.queryByText("Append 6 grocery items")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancel" })).not.toBeInTheDocument();
  });

  it("deletes a conversation from the list after confirmation", async () => {
    hankAIClient.status.mockResolvedValue({ provider: "gpt-5-codex", ready: true });
    hankAIClient.listSessions.mockResolvedValue({
      sessions: [
        { id: "s1", title: "Keep this chat", last_message_at: "now" },
        { id: "s2", title: "Delete this chat", last_message_at: "yesterday" },
      ],
    });
    hankAIClient.listMessages.mockResolvedValue({ messages: [] });
    hankAIClient.deleteSession.mockResolvedValue({ ok: true });

    render(<ConfirmDialogProvider><HankAIPage /></ConfirmDialogProvider>);

    fireEvent.click(await screen.findByRole("button", { name: "Delete Delete this chat" }));
    fireEvent.click(await screen.findByRole("button", { name: "Delete" }));

    await waitFor(() => expect(hankAIClient.deleteSession).toHaveBeenCalledWith("s2"));
    expect(screen.queryByRole("button", { name: "Delete this chat" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Keep this chat" })).toBeInTheDocument();
  });

  it("loads the next conversation when deleting the selected chat", async () => {
    hankAIClient.status.mockResolvedValue({ provider: "gpt-5-codex", ready: true });
    hankAIClient.listSessions.mockResolvedValue({
      sessions: [
        { id: "s1", title: "Selected chat", last_message_at: "now" },
        { id: "s2", title: "Next chat", last_message_at: "yesterday" },
      ],
    });
    hankAIClient.listMessages.mockImplementation(async (id: string) => ({
      messages: id === "s1" ? [{ role: "assistant", text: "Selected message" }] : [{ role: "assistant", text: "Next message" }],
    }));
    hankAIClient.deleteSession.mockResolvedValue({ ok: true });

    render(<ConfirmDialogProvider><HankAIPage /></ConfirmDialogProvider>);

    fireEvent.click(await screen.findByRole("button", { name: "Delete Selected chat" }));
    fireEvent.click(await screen.findByRole("button", { name: "Delete" }));

    await waitFor(() => expect(hankAIClient.listMessages).toHaveBeenCalledWith("s2"));
    expect(await screen.findByText("Next message")).toBeInTheDocument();
  });

  it("keeps a deleted conversation removed when fallback message loading fails", async () => {
    hankAIClient.status.mockResolvedValue({ provider: "gpt-5-codex", ready: true });
    hankAIClient.listSessions.mockResolvedValue({
      sessions: [
        { id: "s1", title: "Selected chat", last_message_at: "now" },
        { id: "s2", title: "Next chat", last_message_at: "yesterday" },
      ],
    });
    hankAIClient.listMessages
      .mockResolvedValueOnce({ messages: [{ role: "assistant", text: "Selected message" }] })
      .mockRejectedValueOnce(new Error("message load failed"));
    hankAIClient.deleteSession.mockResolvedValue({ ok: true });

    render(<ConfirmDialogProvider><HankAIPage /></ConfirmDialogProvider>);

    fireEvent.click(await screen.findByRole("button", { name: "Delete Selected chat" }));
    fireEvent.click(await screen.findByRole("button", { name: "Delete" }));

    await waitFor(() => expect(screen.queryByRole("button", { name: "Selected chat" })).not.toBeInTheDocument());
    expect(screen.getByRole("button", { name: "Next chat" })).toBeInTheDocument();
    expect(screen.getByText("message load failed")).toBeInTheDocument();
  });

  it("shows enabled installed app slash commands and inserts the selected token", async () => {
    hankAIClient.status.mockResolvedValue({ provider: "gpt-5-codex", ready: true });
    hankAIClient.listSessions.mockResolvedValue({ sessions: [] });
    appsClient.listApps.mockResolvedValue({
      apps: [{
        id: "gramaton",
        name: "Gramaton",
        enabled: true,
        slash_commands: [{
          command: "/gramaton",
          command_id: "search",
          description: "Search for a movie or TV show on Gramaton.",
        }],
      }],
    });

    render(<ConfirmDialogProvider><HankAIPage /></ConfirmDialogProvider>);

    const composer = await screen.findByRole("textbox", { name: "Message" });
    fireEvent.change(composer, { target: { value: "/gra" } });
    fireEvent.mouseDown(screen.getByRole("option", { name: /gramaton/i }));

    expect(composer).toHaveValue("/gramaton ");
    expect(appsClient.listApps).toHaveBeenCalledTimes(1);
  });

  it("excludes disabled installed app slash commands", async () => {
    hankAIClient.status.mockResolvedValue({ provider: "gpt-5-codex", ready: true });
    hankAIClient.listSessions.mockResolvedValue({ sessions: [] });
    appsClient.listApps.mockResolvedValue({
      apps: [{
        id: "gramaton",
        name: "Gramaton",
        enabled: false,
        slash_commands: [{ command: "/gramaton", command_id: "search" }],
      }],
    });

    render(<ConfirmDialogProvider><HankAIPage /></ConfirmDialogProvider>);

    const composer = await screen.findByRole("textbox", { name: "Message" });
    fireEvent.change(composer, { target: { value: "/" } });
    expect(screen.queryByRole("option", { name: /gramaton/i })).not.toBeInTheDocument();
  });

  it("keeps built-in commands when installed app discovery fails", async () => {
    hankAIClient.status.mockResolvedValue({ provider: "gpt-5-codex", ready: true });
    hankAIClient.listSessions.mockResolvedValue({ sessions: [] });
    appsClient.listApps.mockRejectedValue(new Error("apps unavailable"));

    render(<ConfirmDialogProvider><HankAIPage /></ConfirmDialogProvider>);

    const composer = await screen.findByRole("textbox", { name: "Message" });
    fireEvent.change(composer, { target: { value: "/fi" } });
    expect(screen.getByRole("option", { name: /files/i })).toBeInTheDocument();
  });

  it("renders grounded result cards with working Hank deep links and tool evidence", async () => {
    hankAIClient.status.mockResolvedValue({ provider: "ollama", chat_configured: true, chat_model: "qwen3:14b" });
    hankAIClient.listSessions.mockResolvedValue({ sessions: [{ id: "s1", title: "Find grocery note" }] });
    hankAIClient.listMessages.mockResolvedValue({
      messages: [{
        id: "m1",
        role: "assistant",
        text: "I found the grocery note.",
        diagnostics: { tool_kind: "notes.search", intent_kind: "notes.search" },
        cards: [{
          kind: "note",
          title: "Grocery List",
          summary: "Milk, eggs, coffee",
          action_title: "Open in Notes",
          note_id: "grocery.md",
        }],
      }],
    });

    render(<ConfirmDialogProvider><HankAIPage /></ConfirmDialogProvider>);

    expect(await screen.findByText("I found the grocery note.")).toBeInTheDocument();
    expect(screen.getByText("Used notes.search")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open in Notes" })).toHaveAttribute("href", "/dashboard/profile-notes?note=grocery.md");
  });

  it("recovers a waiting action after reload and completes it after approval", async () => {
    const waitingRun = {
      id: "run-1",
      state: "waiting_confirmation",
      requires_confirmation: true,
      pending_action_summary: {
        kind: "note_create",
        title: "Create note",
        summary: "Hank will create a personal note.",
        confirmation_message: "Create `Trip list`?",
        details: [{ label: "Title", value: "Trip list" }],
        is_destructive: false,
      },
    };
    hankAIClient.status.mockResolvedValue({ provider: "ollama", chat_configured: true });
    hankAIClient.listSessions.mockResolvedValue({ sessions: [{ id: "s1", title: "Create trip list" }] });
    hankAIClient.listMessages
      .mockResolvedValueOnce({ messages: [{ role: "assistant", text: "I can create that note." }], active_run: waitingRun })
      .mockResolvedValueOnce({ messages: [
        { role: "assistant", text: "I can create that note." },
        { role: "assistant", text: "Created `Trip list`." },
      ] });
    hankAIClient.confirmRun.mockResolvedValue({ id: "run-1", state: "completed" });

    render(<ConfirmDialogProvider><HankAIPage /></ConfirmDialogProvider>);

    expect(await screen.findByRole("region", { name: "Hank action approval" })).toBeInTheDocument();
    expect(screen.getByText("Create `Trip list`?")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Approve" }));

    await waitFor(() => expect(hankAIClient.confirmRun).toHaveBeenCalledWith("run-1", true));
    expect(await screen.findByText("Created `Trip list`.")).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Hank action approval" })).not.toBeInTheDocument();
  });

  it("labels destructive actions clearly and sends an explicit cancellation", async () => {
    hankAIClient.status.mockResolvedValue({ provider: "ollama", chat_configured: true });
    hankAIClient.listSessions.mockResolvedValue({ sessions: [{ id: "s1", title: "Delete appointment" }] });
    hankAIClient.listMessages
      .mockResolvedValueOnce({
        messages: [{ role: "assistant", text: "This needs approval." }],
        active_run: {
          id: "run-delete",
          state: "waiting_confirmation",
          requires_confirmation: true,
          pending_action_summary: {
            kind: "calendar_delete",
            title: "Delete calendar event",
            confirmation_message: "Delete Dentist tomorrow?",
            is_destructive: true,
          },
        },
      })
      .mockResolvedValueOnce({ messages: [{ role: "assistant", text: "This needs approval." }] });
    hankAIClient.confirmRun.mockResolvedValue({ id: "run-delete", state: "completed" });

    render(<ConfirmDialogProvider><HankAIPage /></ConfirmDialogProvider>);

    expect(await screen.findByRole("button", { name: "Approve high-impact action" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));

    await waitFor(() => expect(hankAIClient.confirmRun).toHaveBeenCalledWith("run-delete", false));
    expect(await screen.findByText("Action cancelled.")).toBeInTheDocument();
  });

  it("shows a durable device handoff for client-only actions", async () => {
    hankAIClient.status.mockResolvedValue({ provider: "ollama", chat_configured: true });
    hankAIClient.listSessions.mockResolvedValue({ sessions: [{ id: "s1", title: "Calendar" }] });
    hankAIClient.listMessages.mockResolvedValue({
      messages: [{ role: "assistant", text: "Ready to create the event." }],
      active_run: {
        id: "run-calendar",
        state: "waiting_client_tool",
        requires_client_tools: true,
        client_tool_request: { tool_name: "calendar.create", arguments: { title: "Dentist" } },
      },
    });

    render(<ConfirmDialogProvider><HankAIPage /></ConfirmDialogProvider>);

    expect(await screen.findByRole("region", { name: "Hank client handoff" })).toBeInTheDocument();
    expect(screen.getByText("calendar.create")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Message" })).toBeDisabled();
  });

  it("submits an agent task and replaces the optimistic message with canonical results", async () => {
    hankAIClient.status.mockResolvedValue({ provider: "ollama", chat_configured: true });
    hankAIClient.listSessions
      .mockResolvedValueOnce({ sessions: [] })
      .mockResolvedValueOnce({ sessions: [{ id: "s1", title: "Ask about Hank" }] });
    hankAIClient.createSession.mockResolvedValue({ id: "s1", title: "New conversation" });
    hankAIClient.submitTask.mockResolvedValue({ schema_version: 2, task_id: "task-new", session_id: "s1", state: "completed", revision: 1 });
    hankAIClient.listMessages.mockResolvedValue({
      messages: [
        { id: "u1", role: "user", text: "What is Hank?" },
        { id: "a1", role: "assistant", text: "Hank is your self-hosted home platform." },
      ],
    });

    render(<ConfirmDialogProvider><HankAIPage /></ConfirmDialogProvider>);

    const composer = await screen.findByRole("textbox", { name: "Message" });
    fireEvent.change(composer, { target: { value: "What is Hank?" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() => expect(hankAIClient.submitTask).toHaveBeenCalledWith("s1", "What is Hank?", expect.any(String)));
    expect(hankAIClient.sendMessage).not.toHaveBeenCalled();
    expect(await screen.findByText("Hank is your self-hosted home platform.")).toBeInTheDocument();
    expect(screen.getAllByText("What is Hank?")).toHaveLength(1);
  });
});
