import { HankSocket, type HankSocketEvent } from "../socket/HankSocket";
import { ApiError, apiClient, type ApiTransport } from "./client";
import { arrayFrom } from "./normalize";

export type FileEntry = {
  name?: string;
  path: string;
  is_directory?: boolean;
  size?: number;
  modified_at?: string;
};

export type FileListResponse = {
  path?: string;
  items?: FileEntry[];
  entries?: FileEntry[];
  status?: "ready" | "indexing" | "partial" | "offline";
  sources?: Array<{ source_id: string; agent_id: string; name: string; indexed: number; pending: number; error: boolean }>;
};

export type FileStatResponse = {
  item?: FileEntry;
};

export type FileTransferSetup = {
  url: string;
  method?: string;
  transfer_token?: string;
  transfer_id?: string;
  job_id?: string;
};

export type FileServerSocket = {
  sendCommand<T>(command: string, body?: unknown, options?: { agentID?: string; timeoutMs?: number }): Promise<T>;
  subscribe(topics: string[]): Promise<unknown>;
  onEvent(listener: (event: HankSocketEvent) => void): () => void;
};

export type FileOperationJob = {
  id: string;
  agent_id?: string;
  operation: "download" | "upload" | "move" | "copy" | "delete" | string;
  source_id?: string;
  destination_source_id?: string;
  from_path?: string;
  to_path?: string;
  path?: string;
  is_directory?: boolean;
  status: string;
  bytes_total?: number | null;
  bytes_done?: number;
  files_total?: number;
  files_done?: number;
  error_message?: string;
  created_at?: string;
  updated_at?: string;
  completed_at?: string | null;
};

export type FileOperationJobsResponse = {
  jobs?: FileOperationJob[];
};

export type FileOperationJobsClearResponse = {
  cleared: number;
};

function normalizePath(path: string): string {
  const trimmed = String(path || "/").trim();
  if (!trimmed || trimmed === ".") return "/";
  return `/${trimmed.replace(/^\/+/, "").replace(/\/+$/, "")}`;
}

export function childPath(parent: string, name: string): string {
  const base = normalizePath(parent);
  const cleanName = name.trim().replace(/^\/+|\/+$/g, "");
  return base === "/" ? `/${cleanName}` : `${base}/${cleanName}`;
}

export class FileServerClient {
  constructor(
    private readonly socket: FileServerSocket = new HankSocket(),
    private readonly transport: ApiTransport = apiClient,
  ) {}

  async list(path: string, sourceID?: string, agentID?: string): Promise<FileListResponse> {
    return normalizeFileList(await this.sendFileCommand<FileListResponse>("files.list", withSourceID({ path: normalizePath(path) }, sourceID), agentID, 125000));
  }

  async search(query: string, sourceID?: string, agentID?: string, signal?: AbortSignal): Promise<FileListResponse> {
    const params = new URLSearchParams({ q: query });
    if (sourceID) params.set("source_id", sourceID);
    if (agentID) params.set("agent_id", agentID);
    return normalizeFileList(await this.transport.request<FileListResponse>(`/v1/home/file-search?${params}`, { signal, timeoutMs: 10000 }));
  }

  async createDirectory(path: string, sourceID?: string, agentID?: string): Promise<unknown> {
    return this.sendFileCommand("files.create_directory", withSourceID({ path: normalizePath(path) }, sourceID), agentID);
  }

  async stat(path: string, sourceID?: string, agentID?: string): Promise<FileStatResponse> {
    return this.sendFileCommand<FileStatResponse>("files.stat", withSourceID({ path: normalizePath(path) }, sourceID), agentID);
  }

  async rename(from: string, to: string, sourceID?: string, agentID?: string): Promise<unknown> {
    return this.sendFileCommand("files.rename", withSourceID({ from: normalizePath(from), to: normalizePath(to) }, sourceID), agentID);
  }

  async move(from: string, to: string, isDirectory: boolean, sourceID?: string, destinationSourceID?: string, agentID?: string): Promise<unknown> {
    const body = withSourceID({ from: normalizePath(from), to: normalizePath(to), is_directory: isDirectory }, sourceID);
    const cleanDestinationSourceID = String(destinationSourceID || "").trim();
    return this.sendFileCommand("files.move", cleanDestinationSourceID ? { ...body, destination_source_id: cleanDestinationSourceID } : body, agentID);
  }

  async deleteItem(path: string, isDirectory: boolean, sourceID?: string, agentID?: string): Promise<unknown> {
    return this.sendFileCommand("files.delete", withSourceID({ path: normalizePath(path), is_directory: isDirectory }, sourceID), agentID);
  }

  async listJobs(limit = 20): Promise<FileOperationJob[]> {
    const payload = await this.transport.request<FileOperationJobsResponse>(`/v1/home/file-jobs?limit=${encodeURIComponent(String(limit))}`);
    return arrayFrom<FileOperationJob>(payload.jobs);
  }

  async listOwnerlessJobs(after = ""): Promise<{ jobs: FileOperationJob[]; next_cursor: string }> {
    const payload = await this.transport.request<{ jobs?: FileOperationJob[]; next_cursor?: string }>(`/v1/home/file-jobs?owner=unknown&after=${encodeURIComponent(after)}`);
    return { jobs: arrayFrom<FileOperationJob>(payload.jobs), next_cursor: payload.next_cursor || "" };
  }

  async reviewJobOwner(jobID: string, agentID: string, updatedAt: string): Promise<{ admin_action_token: string; expires_at: string }> {
    return this.transport.request(`/v1/home/file-jobs/${encodeURIComponent(jobID)}/owner`, {
      method: "POST", body: { agent_id: agentID, expected_updated_at: updatedAt, request_action_token: true },
    });
  }

  async assignJobOwner(jobID: string, agentID: string, updatedAt: string, token: string): Promise<FileOperationJob> {
    return this.transport.request(`/v1/home/file-jobs/${encodeURIComponent(jobID)}/owner`, {
      method: "POST", body: { agent_id: agentID, expected_updated_at: updatedAt, admin_action_token: token, confirmation: "CONFIRM OWNER" },
    });
  }

  async reviewJobDismissal(jobID: string, updatedAt: string): Promise<{ admin_action_token: string }> {
    return this.transport.request(`/v1/home/file-jobs/${encodeURIComponent(jobID)}/dismiss`, {
      method: "POST", body: { expected_updated_at: updatedAt, request_action_token: true },
    });
  }

  async dismissJob(jobID: string, updatedAt: string, token: string): Promise<{ removed: boolean }> {
    return this.transport.request(`/v1/home/file-jobs/${encodeURIComponent(jobID)}/dismiss`, {
      method: "POST", body: { expected_updated_at: updatedAt, admin_action_token: token, confirmation: "REMOVE HISTORY" },
    });
  }

  async clearFinishedJobs(): Promise<number> {
    const payload = await this.transport.request<FileOperationJobsClearResponse>("/v1/home/file-jobs", { method: "DELETE" });
    return Number(payload.cleared || 0);
  }

  async cancelJob(jobID: string): Promise<FileOperationJob> {
    return this.transport.request<FileOperationJob>(`/v1/home/file-jobs/${encodeURIComponent(jobID)}/cancel`, { method: "POST" });
  }

  async subscribeToJobs(): Promise<unknown> {
    return this.socket.subscribe(["files.jobs"]);
  }

  onJobsChanged(listener: () => void): () => void {
    return this.socket.onEvent((event) => {
      if (event.event === "socket.reconnected" || (event.topic === "files.jobs" && (event.event === "files.job_changed" || event.event === "files.history_changed"))) listener();
    });
  }

  async setupDownload(path: string, sourceID?: string, agentID?: string): Promise<FileTransferSetup> {
    return this.transport.request<FileTransferSetup>("/v1/home/files/downloads", {
      method: "POST",
      body: withAgentID(withSourceID({ path: normalizePath(path) }, sourceID), agentID),
    });
  }

  async setupUpload(path: string, size: number, sourceID?: string, agentID?: string): Promise<FileTransferSetup> {
    return this.transport.request<FileTransferSetup>("/v1/home/files/uploads", {
      method: "POST",
      body: withAgentID(withSourceID({ path: normalizePath(path), size }, sourceID), agentID),
    });
  }

  async uploadFile(file: File, targetFolder: string, sourceID?: string, agentID?: string, targetName = file.name): Promise<unknown> {
    const path = childPath(targetFolder, targetName);
    const setup = await this.setupUpload(path, file.size || 0, sourceID, agentID);
    const headers: Record<string, string> = { "Content-Type": "application/octet-stream" };
    if (setup.transfer_token) headers.Authorization = `Bearer ${setup.transfer_token}`;
    const response = await fetch(setup.url, {
      method: setup.method || "PUT",
      headers,
      body: file,
      credentials: "same-origin",
    });
    const contentType = response.headers.get("Content-Type") || "";
    const payload = contentType.includes("application/json") ? await response.json() : await response.text();
    if (!response.ok) {
      const normalized = normalizeUploadError(payload, response.statusText);
      throw new ApiError(response.status, normalized.code, normalized.message, payload);
    }
    return payload;
  }

  private sendFileCommand<T>(command: string, body: unknown, agentID?: string, timeoutMs?: number): Promise<T> {
    const cleanAgentID = String(agentID || "").trim();
    const options = { ...(cleanAgentID ? { agentID: cleanAgentID } : {}), ...(timeoutMs ? { timeoutMs } : {}) };
    return Object.keys(options).length ? this.socket.sendCommand<T>(command, body, options) : this.socket.sendCommand<T>(command, body);
  }
}

function normalizeUploadError(payload: unknown, fallback: string): { code: string; message: string } {
  if (typeof payload === "string") return { code: "request_failed", message: payload.trim() || fallback };
  if (payload && typeof payload === "object") {
    const record = payload as Record<string, unknown>;
    const error = record.error;
    if (error && typeof error === "object") {
      const detail = error as Record<string, unknown>;
      return {
        code: String(detail.code || "request_failed"),
        message: String(detail.message || record.message || fallback),
      };
    }
    return {
      code: String(record.error || "request_failed"),
      message: String(record.message || record.error || fallback),
    };
  }
  return { code: "request_failed", message: fallback };
}

function withSourceID<T extends Record<string, unknown>>(body: T, sourceID?: string): T & { source_id?: string } {
  const cleanSourceID = String(sourceID || "").trim();
  return cleanSourceID ? { ...body, source_id: cleanSourceID } : body;
}

function withAgentID<T extends Record<string, unknown>>(body: T, agentID?: string): T & { agent_id?: string } {
  const cleanAgentID = String(agentID || "").trim();
  return cleanAgentID ? { ...body, agent_id: cleanAgentID } : body;
}

function normalizeFileList(payload: FileListResponse): FileListResponse {
  const items = arrayFrom<FileEntry>(payload.items || payload.entries);
  return { ...payload, items, entries: arrayFrom<FileEntry>(payload.entries).length ? arrayFrom<FileEntry>(payload.entries) : items };
}

export const fileServerClient = new FileServerClient();
