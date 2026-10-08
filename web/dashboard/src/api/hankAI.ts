import { apiClient, type ApiTransport } from "./client";
import { arrayFrom } from "./normalize";

export type HankAISession = {
  id: string;
  title?: string;
  last_message_at?: string;
  created_at?: string;
  updated_at?: string;
};

export type HankAIResultCard = {
  kind?: string;
  title?: string;
  summary?: string;
  action_title?: string;
  note_id?: string;
  event_id?: string;
  source_id?: string;
  target_date?: string;
  path?: string;
  is_directory?: boolean;
  search_text?: string;
  image_url?: string;
  media_option_id?: string;
  media_type?: string;
  episode_count?: number;
  year?: number;
  job_id?: string;
};

export type HankAIDiagnostics = {
  tool_kind?: string;
  intent_kind?: string;
  query?: string;
  app_id?: string;
  command_id?: string;
  slash_command?: string;
};

export type HankAIAttachment = {
  client_attachment_id: string;
  filename: string;
  content_type: string;
  size_bytes: number;
  checksum_sha256: string;
  kind: string;
};

export type HankAIMessage = {
  sources?: HankAISource[];
  id?: string;
  role: "user" | "assistant" | string;
  text?: string;
  content?: string;
  created_at?: string;
  cards?: HankAIResultCard[];
  diagnostics?: HankAIDiagnostics;
  pending?: boolean;
};

export type HankAIClientToolRequest = {
  tool_name: string;
  arguments?: Record<string, unknown>;
};

export type HankAIPendingActionDetail = {
  label: string;
  value: string;
};

export type HankAIPendingActionSummary = {
  kind: string;
  title: string;
  summary?: string;
  confirmation_message: string;
  details?: HankAIPendingActionDetail[];
  is_destructive?: boolean;
};

export type HankAIRun = {
  id: string;
  state: string;
  requires_client_tools?: boolean;
  requires_confirmation?: boolean;
  assistant_message?: HankAIMessage;
  client_tool_request?: HankAIClientToolRequest;
  pending_action_summary?: HankAIPendingActionSummary;
  diagnostics?: HankAIDiagnostics;
};

export type HankAIStatus = {
  execution_versions?: number[];
  execution_mode?: string;
  home_id?: string;
  provider?: string;
  ready?: boolean;
  chat_configured?: boolean;
  embedding_configured?: boolean;
  chat_model?: string;
  chat_model_default?: string;
  chat_model_override?: string;
  embedding_model?: string;
  vector_store?: string;
  embedding_warning?: string;
  index?: Record<string, unknown>;
};

export type HankAIClientToolResult = {
  tool_name: string;
  result?: Record<string, unknown>;
  error?: string;
};

export type HankAISendOptions = {
  attachments?: HankAIAttachment[];
  timezone?: string;
};

export class HankAIClient {
  constructor(private readonly api: ApiTransport = apiClient) {}

  decideTaskApproval(taskID: string, approvalID: string, expectedRevision: number, actionDigest: string, approved: boolean) {
    return this.api.request<HankAITask>(`/v1/home/assistant/tasks/${encodeURIComponent(taskID)}/approvals/${encodeURIComponent(approvalID)}`, {
      method: "POST", body: { expected_revision: expectedRevision, action_digest: actionDigest, approved },
    });
  }

  status() {
    return this.api.request<HankAIStatus>("/v1/home/assistant/status");
  }

  async listSessions(): Promise<{ sessions: HankAISession[] }> {
    const payload = await this.api.request<{ sessions?: HankAISession[] }>("/v1/home/assistant/sessions");
    return { sessions: arrayFrom<HankAISession>(payload.sessions) };
  }

  createSession() {
    return this.api.request<HankAISession>("/v1/home/assistant/sessions", { method: "POST" });
  }

  async listMessages(sessionID: string): Promise<{ messages: HankAIMessage[]; active_run?: HankAIRun; active_task?: HankAITask; execution_version?: number }> {
    const payload = await this.api.request<{ messages?: HankAIMessage[]; active_run?: HankAIRun; active_task?: HankAITask; execution_version?: number }>(`/v1/home/assistant/sessions/${encodeURIComponent(sessionID)}/messages`);
    return { messages: arrayFrom<HankAIMessage>(payload.messages), active_run: payload.active_run, active_task: payload.active_task, execution_version: payload.execution_version };
  }

  sendMessage(sessionID: string, content: string, options: HankAISendOptions = {}) {
    return this.api.request<HankAIRun>(`/v1/home/assistant/sessions/${encodeURIComponent(sessionID)}/messages`, {
      method: "POST",
      body: {
        content,
        attachments: options.attachments || [],
        device_context: {
          device_id: "hankserverside-dashboard",
          timezone: options.timezone || "UTC",
        },
      },
    });
  }

  stageTaskAttachment(sessionID: string, attachment: HankAIAttachment, file: File) {
    return this.api.request<{ id: string }>(`/v1/home/assistant/sessions/${encodeURIComponent(sessionID)}/staging`, {
      method: "POST", body: file, headers: {
        "Content-Type": "application/octet-stream",
        "X-Hank-Attachment-ID": attachment.client_attachment_id,
        "X-Hank-Filename": encodeURIComponent(attachment.filename),
        "X-Hank-Content-Type": attachment.content_type,
        "X-Hank-Size-Bytes": String(attachment.size_bytes),
        "X-Hank-Checksum-SHA256": attachment.checksum_sha256 || "",
      },
    });
  }

  submitTask(sessionID: string, content: string, submissionID: string) {
    return this.api.request<HankAITask>(`/v1/home/assistant/sessions/${encodeURIComponent(sessionID)}/messages`, {
      method: "POST", headers: { "X-Hank-Assistant-Execution": "2" },
      body: { content, submission_id: submissionID },
    });
  }

  getTask(taskID: string, signal?: AbortSignal) {
    return this.api.request<HankAITask>(`/v1/home/assistant/tasks/${encodeURIComponent(taskID)}`, { signal });
  }

  taskEvents(taskID: string, after: number, signal?: AbortSignal) {
    return this.api.request<{ events: HankAITaskEvent[]; next_sequence: number; has_more: boolean; snapshot_required: boolean }>(`/v1/home/assistant/tasks/${encodeURIComponent(taskID)}/events?after_sequence=${after}&limit=100`, { signal });
  }

  stopTask(taskID: string) {
    return this.api.request<HankAITask>(`/v1/home/assistant/tasks/${encodeURIComponent(taskID)}/stop`, { method: "POST" });
  }

  followupTask(taskID: string, text: string, submissionID: string, revision: number) {
    return this.api.request<HankAITask>(`/v1/home/assistant/tasks/${encodeURIComponent(taskID)}/followups`, {
      method: "POST", body: { text, submission_id: submissionID, expected_revision: revision },
    });
  }

  getRun(runID: string) {
    return this.api.request<HankAIRun>(`/v1/home/assistant/runs/${encodeURIComponent(runID)}`);
  }

  confirmRun(runID: string, approved: boolean) {
    return this.api.request<HankAIRun>(`/v1/home/assistant/runs/${encodeURIComponent(runID)}/confirm`, {
      method: "POST",
      body: { approved },
    });
  }

  submitClientToolResult(runID: string, result: HankAIClientToolResult) {
    return this.api.request<HankAIRun>(`/v1/home/assistant/runs/${encodeURIComponent(runID)}/client-tool-results`, {
      method: "POST",
      body: result,
    });
  }

  discardAttachment(sessionID: string, clientAttachmentID: string) {
    return this.api.request<{ ok: boolean; status: string }>(
      `/v1/home/assistant/sessions/${encodeURIComponent(sessionID)}/attachments/${encodeURIComponent(clientAttachmentID)}/discard`,
      { method: "DELETE" },
    );
  }

  deleteSession(sessionID: string) {
    return this.api.request<{ ok: boolean }>(`/v1/home/assistant/sessions/${encodeURIComponent(sessionID)}`, {
      method: "DELETE",
    });
  }
}

export const hankAIClient = new HankAIClient();

export type HankAISource = { title: string; uri: string; href: string };
export type HankAITask = {
  schema_version: 2;
  task_id: string;
  session_id: string;
  state: "queued" | "running" | "waiting_approval" | "waiting_input" | "waiting_client" | "waiting_retry" | "reconciling" | "completed" | "failed" | "cancelled";
  revision: number;
  last_event_sequence: number;
  cancel_requested: boolean;
  effects_uncertain?: boolean;
  pending_approval?: { approval_id: string; action_digest: string; revision: number; expires_at: string; summary: HankAIPendingActionSummary };
  error_code?: string;
  calls?: { call_id: string; tool: string; state: string; attempts: number }[];
};
export type HankAITaskEvent = { sequence: number; type: string; call_id?: string; created_at: string };
export function terminalTask(task: HankAITask): boolean {
  return ["completed", "failed", "cancelled"].includes(task.state);
}
