import { useEffect, useMemo, useRef, useState } from "react";
import { FileJobOwnerAssignment } from "./FileJobOwnerAssignment";
import { ApiError } from "../api/client";
import { childPath, fileServerClient, type FileEntry, type FileOperationJob } from "../api/fileServer";
import { useConfirmDialog, useToast } from "../ui/primitives";
import { loadFileTargets, type FileTarget } from "./fileServerTargets";

type FileMeta = FileEntry & {
  dimensions?: string;
  dims?: string;
  owner?: string;
  type?: string;
};

type FileDialog =
  | { kind: "folder" }
  | { kind: "delete"; items: FileMeta[] }
  | { kind: "rename"; item: FileMeta }
  | { kind: "move"; items: FileMeta[]; destinationPath: string; destinationSourceID: string };

type FolderContext = {
  path: string;
  items: FileMeta[];
};

type UploadIssue = {
  summary: string;
  limitations: string[];
};

type State =
  | { status: "loading"; path: string }
  | { status: "error"; path: string; message: string }
  | {
      status: "ready";
      path: string;
      items: FileMeta[];
      folderContext: FolderContext;
      query: string;
      searchItems: FileMeta[] | null;
      searching: boolean;
      searchError: boolean;
      searchStatus: "ready" | "indexing" | "partial" | "offline";
      message: string;
      viewMode: "list" | "grid";
      selectedPaths: string[];
      previewPath: string;
      previewOpen: boolean;
      sharePickerOpen: boolean;
      menuPath: string;
      dialog: FileDialog | null;
      dialogDraft: string;
      refreshingPath?: string;
      uploadIssue: UploadIssue | null;
    };

type TransferJobsState =
  | { status: "loading"; jobs: FileOperationJob[]; message?: string }
  | { status: "ready"; jobs: FileOperationJob[]; message?: string }
  | { status: "error"; jobs: FileOperationJob[]; message: string };

function fileName(item: FileEntry): string {
  return item.name || item.path.split("/").filter(Boolean).pop() || "/";
}

function formatSize(item: FileEntry): string {
  if (item.is_directory) return "—";
  const size = item.size || 0;
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${Math.round(size / 1024)} KB`;
  return `${(size / (1024 * 1024)).toFixed(1)} MB`;
}

function formatBytes(value: number | null | undefined): string {
  const bytes = Number(value || 0);
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`;
}

function formatModified(item: FileEntry): string {
  if (!item.modified_at) return "Unknown";
  const date = new Date(item.modified_at);
  if (Number.isNaN(date.getTime())) return "Unknown";
  return date.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function formatJobTime(value?: string | null): string {
  if (!value) return "Just now";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "Just now";
  return date.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function fileType(item: FileMeta): string {
  if (item.is_directory) return "Folder";
  if (item.type) return item.type;
  const ext = fileName(item).split(".").pop()?.toLowerCase() || "";
  if (["png", "jpg", "jpeg", "gif", "webp", "svg", "heic"].includes(ext)) return ext === "jpg" || ext === "jpeg" ? "JPEG image" : "Image";
  if (["mp4", "mov", "mkv", "avi", "webm"].includes(ext)) return "Video";
  if (["mp3", "wav", "flac", "aac", "m4a"].includes(ext)) return "Audio";
  if (["zip", "tar", "gz", "7z", "rar"].includes(ext)) return "Archive";
  if (["pdf", "doc", "docx", "txt", "md", "rtf"].includes(ext)) return "Document";
  return "File";
}

function fileIcon(item: FileMeta): string {
  if (item.is_directory) return "folder";
  const ext = fileName(item).split(".").pop()?.toLowerCase() || "";
  if (["png", "jpg", "jpeg", "gif", "webp", "svg", "heic"].includes(ext)) return "image";
  if (["mp4", "mov", "mkv", "avi", "webm"].includes(ext)) return "video";
  if (["mp3", "wav", "flac", "aac", "m4a"].includes(ext)) return "audio";
  if (["zip", "tar", "gz", "7z", "rar"].includes(ext)) return "archive";
  return "file";
}

function fileIconTone(item: FileMeta): string {
  switch (fileIcon(item)) {
    case "folder": return "var(--brand-dark)";
    case "image": return "#6fd3a8";
    case "video": return "#c98ad8";
    case "audio": return "#f0bd6b";
    case "archive": return "#f08a8a";
    default: return "var(--muted)";
  }
}

function jobDisplayPath(job: FileOperationJob): string {
  if (job.operation === "move") return job.to_path || job.from_path || job.path || "/";
  return job.path || job.from_path || job.to_path || "/";
}

function jobTitle(job: FileOperationJob): string {
  const raw = jobDisplayPath(job).split("/").filter(Boolean).pop() || jobDisplayPath(job);
  const name = raw || "file";
  switch (job.operation) {
    case "download": return `Download ${name}`;
    case "upload": return `Upload ${name}`;
    case "move": return `Move ${name}`;
    case "copy": return `Copy ${name}`;
    case "delete": return `Delete ${name}`;
    default: return `${job.operation || "File job"} ${name}`;
  }
}

function jobDetail(job: FileOperationJob): string {
  const status = job.status.replaceAll("_", " ");
  if (job.operation === "move" && job.from_path && job.to_path) return `${status} from ${job.from_path} to ${job.to_path}`;
  return `${status} ${jobDisplayPath(job)}`;
}

function jobProgress(job: FileOperationJob): number {
  const total = Number(job.bytes_total || job.files_total || 0);
  const done = Number(job.bytes_total ? job.bytes_done || 0 : job.files_done || 0);
  if (total <= 0) {
    return isTerminalJob(job.status) ? 100 : 0;
  }
  return Math.max(0, Math.min(100, Math.round((done / total) * 100)));
}

function isTerminalJob(status: string): boolean {
  return ["completed", "failed", "cancelled", "rollback_required", "rolled_back"].includes(status);
}

function isClearableJob(job: FileOperationJob): boolean {
  return ["completed", "failed", "cancelled", "rolled_back"].includes(job.status)
    || (job.status === "rollback_required" && ["upload", "download"].includes(job.operation));
}

function fileDimensions(item: FileMeta): string {
  if (item.dimensions) return item.dimensions;
  if (item.dims) return item.dims;
  return "—";
}

function isImageFile(item: FileMeta): boolean {
  return fileIcon(item) === "image";
}

function isVideoFile(item: FileMeta): boolean {
  return fileIcon(item) === "video";
}

function isAudioFile(item: FileMeta): boolean {
  return fileIcon(item) === "audio";
}

function isPDFFile(item: FileMeta): boolean {
  return fileName(item).toLowerCase().endsWith(".pdf");
}

function isHTMLFile(item: FileMeta): boolean {
  const name = fileName(item).toLowerCase();
  return name.endsWith(".html") || name.endsWith(".htm");
}

function isMarkdownFile(item: FileMeta): boolean {
  return [".md", ".markdown"].some((extension) => fileName(item).toLowerCase().endsWith(extension));
}

function parentPath(path: string): string {
  const parts = path.split("/").filter(Boolean);
  parts.pop();
  return parts.length ? `/${parts.join("/")}` : "/";
}

function previewURL(item: FileMeta, sourceID: string, agentID: string): string {
  const params = new URLSearchParams();
  if (sourceID) params.set("source_id", sourceID);
  if (agentID) params.set("agent_id", agentID);
  params.set("path", item.path);
  return `/v1/home/files/preview?${params.toString()}`;
}

function shouldOpenPreviewByDefault(): boolean {
  return typeof window === "undefined"
    || typeof window.matchMedia !== "function"
    || !window.matchMedia("(max-width: 760px)").matches;
}

function startBrowserDownload(url: string, filename: string) {
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  link.rel = "noopener";
  document.body.appendChild(link);
  link.click();
  link.remove();
}

function crumbs(path: string): Array<{ label: string; path: string }> {
  const parts = path.split("/").filter(Boolean);
  const out: Array<{ label: string; path: string }> = [{ label: "Home", path: "/" }];
  let acc = "";
  for (const part of parts) {
    acc += `/${part}`;
    out.push({ label: part, path: acc });
  }
  return out;
}

function errorMessage(error: unknown): string {
  return error instanceof Error && error.message ? error.message : "Files could not be loaded.";
}

function numericPayloadValue(payload: unknown, key: string): number {
  if (!payload || typeof payload !== "object") return 0;
  const value = Number((payload as Record<string, unknown>)[key] || 0);
  return Number.isFinite(value) && value > 0 ? value : 0;
}

function uploadIssue(error: unknown, file: File): UploadIssue {
  const apiError = error instanceof ApiError ? error : null;
  const rawMessage = errorMessage(error).toLowerCase();
  const maxUploadBytes = numericPayloadValue(apiError?.payload, "max_upload_bytes");
  const attemptedUploadBytes = numericPayloadValue(apiError?.payload, "attempted_upload_bytes") || file.size;
  let summary = `Couldn’t upload “${file.name}”.`;

  if (apiError?.code === "upload_too_large" || apiError?.status === 413 || rawMessage.includes("size limit")) {
    summary = maxUploadBytes
      ? `“${file.name}” is ${formatBytes(attemptedUploadBytes)}, but the maximum upload size is ${formatBytes(maxUploadBytes)}.`
      : `“${file.name}” is larger than this file source allows.`;
  } else if (rawMessage.includes("denies write")) {
    summary = `Uploads are turned off for this file source.`;
  } else if (rawMessage.includes("blocks this path") || rawMessage.includes("does not allow this path")) {
    summary = `Uploads aren’t allowed in this folder.`;
  } else if (rawMessage.includes("agent") && rawMessage.includes("offline")) {
    summary = `The upload couldn’t start because the target Hank Agent is offline.`;
  }

  const limitations = [
    maxUploadBytes ? `Maximum file size: ${formatBytes(maxUploadBytes)}.` : "Maximum file size is set by the file source policy.",
    "Uploads must be enabled for the selected file source and destination folder.",
    "The target Hank Agent must stay online while the file is transferred.",
  ];
  return { summary, limitations };
}

function normalizedDashboardPath(raw: string): string {
  const trimmed = raw.trim();
  if (!trimmed || trimmed === ".") return "/";
  return trimmed.startsWith("/") ? trimmed : `/${trimmed}`;
}

function initialLinkFromLocation(): { folderPath: string; previewPath: string; previewOpen: boolean } {
  const params = new URLSearchParams(window.location.search);
  const path = normalizedDashboardPath(params.get("path") || "/");
  const previewOpen = params.get("preview") === "1";
  return {
    folderPath: previewOpen ? parentPath(path) : path,
    previewPath: previewOpen ? path : "",
    previewOpen,
  };
}

function dashboardFileLink(item: FileMeta, sourceID: string, agentID: string): string {
  const params = new URLSearchParams();
  if (sourceID) params.set("source_id", sourceID);
  if (agentID) params.set("agent_id", agentID);
  params.set("path", item.path);
  if (!item.is_directory) params.set("preview", "1");
  return `${window.location.origin}/dashboard/file-server?${params.toString()}`;
}

function initialTargetKey(targets: FileTarget[]): string {
  const params = new URLSearchParams(window.location.search);
  const sourceID = (params.get("source_id") || "").trim();
  const agentID = (params.get("agent_id") || "").trim();
  return targets.find((target) => target.sourceID === sourceID && target.agentID === agentID)?.key || targets[0]?.key || "primary:default";
}

const defaultFileTarget: FileTarget = {
  key: "primary:default",
  sourceID: "",
  agentID: "",
  name: "Home files",
  detail: "Primary Hank Agent files",
  kind: "host",
};

function Icon({ name }: { name: string }) {
  const common = {
    fill: "none",
    stroke: "currentColor",
    strokeLinecap: "round" as const,
    strokeLinejoin: "round" as const,
    strokeWidth: 1.8,
  };
  return (
    <svg className="ui-icon" viewBox="0 0 24 24" aria-hidden="true">
      {name === "hard-drive" ? <><rect x="4" y="5" width="16" height="14" rx="3" {...common} /><path d="M7 15h.01M17 15h.01" {...common} /></> : null}
      {name === "folder" ? <><path d="M3.5 7.5h6l1.6 2H20.5v7.5a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2z" {...common} /><path d="M3.5 9.5h17" {...common} /></> : null}
      {name === "folder-plus" ? <><path d="M3.5 7.5h6l1.6 2H20.5v7.5a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2z" {...common} /><path d="M12 13v4M10 15h4" {...common} /></> : null}
      {name === "upload" ? <><path d="M12 16V5" {...common} /><path d="m7.5 9.5 4.5-4.5 4.5 4.5" {...common} /><path d="M5 18.5h14" {...common} /></> : null}
      {name === "search" ? <><circle cx="11" cy="11" r="6.5" {...common} /><path d="m16.5 16.5 3.5 3.5" {...common} /></> : null}
      {name === "list" ? <><path d="M8 7h12M8 12h12M8 17h12" {...common} /><path d="M4 7h.01M4 12h.01M4 17h.01" {...common} /></> : null}
      {name === "grid" ? <><rect x="4" y="4" width="6" height="6" rx="1.5" {...common} /><rect x="14" y="4" width="6" height="6" rx="1.5" {...common} /><rect x="4" y="14" width="6" height="6" rx="1.5" {...common} /><rect x="14" y="14" width="6" height="6" rx="1.5" {...common} /></> : null}
      {name === "image" ? <><rect x="4" y="5" width="16" height="14" rx="2" {...common} /><path d="m7 16 3.2-3.2 2.3 2.3 2.8-3.1L20 17" {...common} /><circle cx="8.5" cy="8.5" r="1" fill="currentColor" /></> : null}
      {name === "video" ? <><rect x="4" y="6" width="12" height="12" rx="2" {...common} /><path d="m16 10 4-2v8l-4-2z" {...common} /></> : null}
      {name === "audio" ? <><path d="M9 18V7l9-2v11" {...common} /><circle cx="7" cy="18" r="2" {...common} /><circle cx="16" cy="16" r="2" {...common} /></> : null}
      {name === "archive" ? <><rect x="6" y="4" width="12" height="16" rx="2" {...common} /><path d="M10 4v4h4V4M10 13h4" {...common} /></> : null}
      {name === "file" ? <><path d="M7 4h7l4 4v12H7z" {...common} /><path d="M14 4v5h4" {...common} /></> : null}
      {name === "dots" ? <><circle cx="6" cy="12" r="1" fill="currentColor" /><circle cx="12" cy="12" r="1" fill="currentColor" /><circle cx="18" cy="12" r="1" fill="currentColor" /></> : null}
      {name === "download" ? <><path d="M12 4v11" {...common} /><path d="m7.5 10.5 4.5 4.5 4.5-4.5" {...common} /><path d="M5 19h14" {...common} /></> : null}
      {name === "move" ? <><path d="M5 12h14" {...common} /><path d="m14 7 5 5-5 5" {...common} /></> : null}
      {name === "trash" ? <><path d="M5 7h14M9 7V5h6v2M8 10v8M12 10v8M16 10v8" {...common} /></> : null}
      {name === "pencil" ? <><path d="M4 20h4l11-11a2.1 2.1 0 0 0-3-3L5 17z" {...common} /><path d="m14 8 2 2" {...common} /></> : null}
      {name === "x" ? <><path d="M7 7l10 10M17 7 7 17" {...common} /></> : null}
      {name === "check" ? <path d="m5 12 4 4L19 6" {...common} /> : null}
      {name === "minus" ? <path d="M6 12h12" {...common} /> : null}
    </svg>
  );
}

export function FileServerPage({ isAdmin = false }: { isAdmin?: boolean }) {
  const initialLink = initialLinkFromLocation();
  const [state, setState] = useState<State>({ status: "loading", path: initialLink.folderPath });
  const [transferJobs, setTransferJobs] = useState<TransferJobsState>({ status: "loading", jobs: [] });
  const [targets, setTargets] = useState<FileTarget[]>([]);
  const [activeTargetKey, setActiveTargetKey] = useState("");
  const [searchRetry, setSearchRetry] = useState(0);
  const [selectionMode, setSelectionMode] = useState(false);
  const [clearingHistory, setClearingHistory] = useState(false);
  const uploadInputRef = useRef<HTMLInputElement>(null);
  const searchRequestRef = useRef(0);
  const loadRequestRef = useRef(0);
  const loadJobsRequestRef = useRef(0);
  const { confirm } = useConfirmDialog();
  const { showToast } = useToast();
  const targetOptions = targets.length ? targets : [defaultFileTarget];
  const activeTarget = targetOptions.find((target) => target.key === activeTargetKey) || targetOptions[0];

  async function load(
    path = state.path,
    message = "",
    targetKey = activeTargetKey,
    requestedPreviewPath = "",
    requestedPreviewOpen = false,
  ) {
    const loadID = ++loadRequestRef.current;
    const changingTarget = Boolean(targetKey && targetKey !== activeTargetKey);
    searchRequestRef.current++;
    setState((current) => current.status === "ready"
      ? {
          ...current,
          refreshingPath: path,
          message,
          sharePickerOpen: false,
          menuPath: "",
          items: changingTarget ? [] : current.items,
          query: "",
          searchItems: null,
          searching: false,
          searchError: false,
          searchStatus: "ready",
          selectedPaths: changingTarget ? [] : current.selectedPaths,
          previewPath: changingTarget ? "" : current.previewPath,
          dialog: changingTarget ? null : current.dialog,
          folderContext: changingTarget ? { path: "/", items: [] } : current.folderContext,
          uploadIssue: changingTarget ? null : current.uploadIssue,
        }
      : { status: "loading", path });
    try {
      let nextTargets = targets;
      if (!nextTargets.length) {
        nextTargets = await loadFileTargets();
        setTargets(nextTargets);
      }
      const nextTargetKey = targetKey || initialTargetKey(nextTargets);
      const nextTarget = nextTargets.find((target) => target.key === nextTargetKey) || nextTargets[0] || defaultFileTarget;
      if (loadID !== loadRequestRef.current) return;
      setActiveTargetKey(nextTarget.key);
      const cleanPath = normalizedDashboardPath(path);
      const contextPath = cleanPath === "/" ? "/" : parentPath(cleanPath);
      const cachedContext = !changingTarget && state.status === "ready" && state.path === contextPath ? state.items : null;
      const payload = await fileServerClient.list(cleanPath, nextTarget.sourceID || undefined, nextTarget.agentID || undefined);
      if (loadID !== loadRequestRef.current) return;
      const items = (payload.items || payload.entries || []) as FileMeta[];
      const contextItems = contextPath === cleanPath ? items : cachedContext || [];
      const defaultPreview = items.find((item) => !item.is_directory)?.path || items[0]?.path || "";
      const requestedPreview = requestedPreviewPath && items.some((item) => item.path === requestedPreviewPath)
        ? requestedPreviewPath
        : "";
      setState((current) => ({
        status: "ready",
        path: payload.path || path,
        items,
        folderContext: { path: contextPath, items: contextItems },
        query: current.status === "ready" ? current.query : "",
        searchItems: current.status === "ready" ? current.searchItems : null,
        searching: current.status === "ready" ? current.searching : false,
        searchError: current.status === "ready" ? current.searchError : false,
        searchStatus: current.status === "ready" ? current.searchStatus : "ready",
        message,
        viewMode: current.status === "ready" ? current.viewMode : "list",
        selectedPaths: current.status === "ready" ? current.selectedPaths.filter((selectedPath) => items.some((item) => item.path === selectedPath)) : [],
        previewPath: requestedPreview || (current.status === "ready" && items.some((item) => item.path === current.previewPath) ? current.previewPath : defaultPreview),
        previewOpen: requestedPreview ? true : current.status === "ready" ? current.previewOpen : requestedPreviewOpen || shouldOpenPreviewByDefault(),
        sharePickerOpen: false,
        menuPath: "",
        dialog: null,
        dialogDraft: "",
        refreshingPath: "",
        uploadIssue: current.status === "ready" ? current.uploadIssue : null,
      }));
      if (contextPath !== cleanPath && !cachedContext) {
        void fileServerClient.list(contextPath, nextTarget.sourceID || undefined, nextTarget.agentID || undefined)
          .then((contextPayload) => {
            if (loadID !== loadRequestRef.current) return;
            const parentItems = (contextPayload.items || contextPayload.entries || []) as FileMeta[];
            setState((current) => current.status === "ready" && current.path === (payload.path || path)
              ? { ...current, folderContext: { path: contextPayload.path || contextPath, items: parentItems } }
              : current);
          })
          .catch(() => undefined);
      }
    } catch (error) {
      if (loadID !== loadRequestRef.current) return;
      setState((current) => current.status === "ready"
        ? { ...current, refreshingPath: "", message: errorMessage(error) }
        : { status: "error", path, message: errorMessage(error) });
    }
  }

  async function loadTransferJobs() {
    const requestID = ++loadJobsRequestRef.current;
    setTransferJobs((current) => ({ status: current.jobs.length ? "ready" : "loading", jobs: current.jobs }));
    try {
      const jobs = await fileServerClient.listJobs(20);
      if (requestID !== loadJobsRequestRef.current) return;
      setTransferJobs({ status: "ready", jobs });
    } catch (error) {
      if (requestID !== loadJobsRequestRef.current) return;
      setTransferJobs((current) => ({ status: "error", jobs: current.jobs, message: errorMessage(error) }));
    }
  }

  async function clearFinishedTransferJobs() {
    if (clearingHistory) return;
    setClearingHistory(true);
    loadJobsRequestRef.current++;
    try {
      const cleared = await fileServerClient.clearFinishedJobs();
      loadJobsRequestRef.current++;
      setTransferJobs((current) => ({
        status: "ready",
        jobs: current.jobs.filter((job) => !isClearableJob(job)),
      }));
      showToast(cleared === 1 ? "Cleared 1 finished transfer." : `Cleared ${cleared} finished transfers.`);
    } catch (error) {
      setTransferJobs((current) => ({ status: "error", jobs: current.jobs, message: errorMessage(error) }));
      showToast("Finished transfers couldn’t be cleared.", "error");
    } finally { setClearingHistory(false); }
  }

  async function cancelTransferJob(job: FileOperationJob) {
    if (!await confirm({
      title: `Cancel ${jobTitle(job)}?`,
      message: "This stops the transfer and revokes its transfer link. You can clear it from history afterward.",
      confirmLabel: "Cancel transfer",
      tone: "danger",
    })) return;
    try {
      await fileServerClient.cancelJob(job.id);
      await loadTransferJobs();
      showToast("Transfer cancelled. You can clear it from history.");
    } catch (error) {
      showToast(errorMessage(error), "error");
    }
  }

  useEffect(() => {
    void load(initialLink.folderPath, "", "", initialLink.previewPath, initialLink.previewOpen);
    void loadTransferJobs();
    // Initial load only.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function copyDashboardLink(item: FileMeta) {
    try {
      await navigator.clipboard.writeText(dashboardFileLink(item, activeTarget.sourceID, activeTarget.agentID));
      showToast(item.is_directory ? "Folder link copied." : "Preview link copied.");
    } catch (error) {
      showToast(errorMessage(error), "error");
    }
  }

  const searchQuery = state.status === "ready" ? state.query.trim() : "";
  useEffect(() => {
    const query = searchQuery;
    const sourceID = activeTarget.sourceID;
    const agentID = activeTarget.agentID;
    const requestID = ++searchRequestRef.current;
    const controller = new AbortController();
    let refresh: number | undefined;
    if (!query) {
      setState((current) => current.status === "ready" ? { ...current, searchItems: null, searching: false, searchError: false, searchStatus: "ready" } : current);
      return;
    }
    const run = () => {
      void fileServerClient.search(query, sourceID || undefined, agentID || undefined, controller.signal)
        .then((payload) => {
          if (searchRequestRef.current !== requestID) return;
          const items = (payload.items || payload.entries || []) as FileMeta[];
          const status = payload.status || "ready";
          setState((current) => current.status === "ready" ? { ...current, searchItems: items, searching: false, searchError: false, searchStatus: status, selectedPaths: [], previewPath: items[0]?.path || "" } : current);
          if (status === "indexing" || status === "partial" || status === "offline") refresh = window.setTimeout(run, 4000);
        })
        .catch((error) => {
          if (searchRequestRef.current !== requestID) return;
          setState((current) => current.status === "ready" ? { ...current, searchItems: [], searching: false, searchError: true, message: errorMessage(error) } : current);
        });
    };
    const timer = window.setTimeout(run, 400);
    return () => { controller.abort(); window.clearTimeout(timer); if (refresh !== undefined) window.clearTimeout(refresh); };
  }, [activeTarget.agentID, activeTarget.sourceID, searchQuery, searchRetry]);

  useEffect(() => {
    let active = true;
    void fileServerClient.subscribeToJobs().catch(() => undefined);
    const unsubscribe = fileServerClient.onJobsChanged(() => {
      if (active) void loadTransferJobs();
    });
    return () => {
      active = false;
      unsubscribe();
    };
    // Job subscription only.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const visibleItems = useMemo(() => {
    if (state.status !== "ready") return [];
    if (state.searching) return [];
    const query = state.query.trim().toLowerCase();
    const items = [...(state.searchItems ?? state.items)].sort((left, right) => Number(Boolean(right.is_directory)) - Number(Boolean(left.is_directory)) || fileName(left).localeCompare(fileName(right)));
    if (!query || state.searchItems !== null) return items;
    return items.filter((item) => [fileName(item), item.path, fileType(item)].join(" ").toLowerCase().includes(query));
  }, [state]);

  if (state.status === "loading") {
    return (
      <section className="dashboard-page file-server-page" aria-labelledby="route-title">
        <h1 id="route-title">Loading files</h1>
        <p className="loading-state"><span className="spinner" aria-hidden="true" />Loading {state.path}...</p>
        <div key="file-activity" id="file-activity-panel" className="file-activity-region" role="region" aria-label="File activity">
          <TransferJobsPanel clearing={clearingHistory} isAdmin={isAdmin} state={transferJobs} onRefresh={() => void loadTransferJobs()} onClear={() => void clearFinishedTransferJobs()} onCancel={(job) => void cancelTransferJob(job)} />
        </div>
      </section>
    );
  }

  if (state.status === "error") {
    return (
      <section className="dashboard-page file-server-page" aria-labelledby="route-title">
        <h1 id="route-title">File Server</h1>
        <p className="error-state">{state.message}</p>
        <button type="button" onClick={() => void load(state.path, "", activeTarget.key)}>Try loading files again</button>
        <div key="file-activity" id="file-activity-panel" className="file-activity-region" role="region" aria-label="File activity">
          <TransferJobsPanel clearing={clearingHistory} isAdmin={isAdmin} state={transferJobs} onRefresh={() => void loadTransferJobs()} onClear={() => void clearFinishedTransferJobs()} onCancel={(job) => void cancelTransferJob(job)} />
        </div>
      </section>
    );
  }

  const readyState = state;
  const isRefreshing = Boolean(readyState.refreshingPath);
  const commandSourceID = activeTarget.sourceID;
  const commandAgentID = activeTarget.agentID;
  const moveTargets = targetOptions.filter((target) => target.agentID === commandAgentID);

  function setReady(next: Partial<Extract<State, { status: "ready" }>>) {
    setState((current) => current.status === "ready" ? { ...current, ...next } : current);
  }

  async function createFolder() {
    const name = readyState.dialogDraft.trim();
    if (!name) return;
    try {
      await fileServerClient.createDirectory(childPath(readyState.path, name), commandSourceID || undefined, commandAgentID || undefined);
      await load(readyState.path, "", activeTarget.key);
      showToast("Folder created.");
    } catch (error) {
      showToast(errorMessage(error), "error");
    }
  }

  async function deleteItems(items: FileMeta[]) {
    if (!items.length) return;
    try {
      for (const item of items) {
        await fileServerClient.deleteItem(item.path, Boolean(item.is_directory), commandSourceID || undefined, commandAgentID || undefined);
      }
      await load(readyState.path, "", activeTarget.key);
      showToast(items.length === 1 ? "Moved to trash." : `${items.length} items moved to trash.`);
    } catch (error) {
      showToast(errorMessage(error), "error");
    }
  }

  async function uploadFiles(files: FileList | null) {
    const selectedFiles = Array.from(files || []).filter((file) => file.name);
    if (!selectedFiles.length) return;
    let currentFile = selectedFiles[0];
    try {
      for (const file of selectedFiles) {
        currentFile = file;
        await fileServerClient.uploadFile(file, readyState.path, commandSourceID || undefined, commandAgentID || undefined);
      }
      if (uploadInputRef.current) uploadInputRef.current.value = "";
      setReady({ uploadIssue: null });
      await load(readyState.path, "", activeTarget.key);
      showToast(selectedFiles.length === 1 ? `Uploaded ${selectedFiles[0].name}.` : `Uploaded ${selectedFiles.length} files.`);
    } catch (error) {
      const issue = uploadIssue(error, currentFile);
      setReady({ uploadIssue: issue });
      if (uploadInputRef.current) uploadInputRef.current.value = "";
      showToast(issue.summary, "error");
    }
  }

  async function downloadItems(items: FileMeta[]) {
    const files = items.filter((item) => !item.is_directory);
    if (!files.length) {
      showToast("Select one or more files to download.", "error");
      return;
    }
    try {
      for (const item of files) {
        const setup = await fileServerClient.setupDownload(item.path, commandSourceID || undefined, commandAgentID || undefined);
        if (setup.url) startBrowserDownload(setup.url, fileName(item));
      }
      showToast(files.length === 1 ? `Started download for ${fileName(files[0])}.` : `Started ${files.length} downloads.`);
    } catch (error) {
      showToast(errorMessage(error), "error");
    }
  }

  async function renameItem(item: FileMeta, name: string) {
    const cleanName = name.trim();
    if (!cleanName || cleanName.includes("/")) {
      showToast("Use a file name without slashes.", "error");
      return;
    }
    const targetPath = childPath(parentPath(item.path), cleanName);
    try {
      await fileServerClient.rename(item.path, targetPath, commandSourceID || undefined, commandAgentID || undefined);
      await load(readyState.path, "", activeTarget.key);
      showToast("Item renamed.");
    } catch (error) {
      showToast(errorMessage(error), "error");
    }
  }

  async function moveItems(items: FileMeta[], destinationPath: string, destinationSourceID: string) {
    const cleanDestination = destinationPath.trim().startsWith("/") ? destinationPath.trim() : `/${destinationPath.trim()}`;
    if (!cleanDestination || !items.length) return;
    try {
      for (const item of items) {
        await fileServerClient.move(
          item.path,
          childPath(cleanDestination, fileName(item)),
          Boolean(item.is_directory),
          commandSourceID || undefined,
          destinationSourceID || commandSourceID || undefined,
          commandAgentID || undefined,
        );
      }
      await load(readyState.path, "", activeTarget.key);
      showToast(items.length === 1 ? "Move queued." : `${items.length} moves queued.`);
    } catch (error) {
      showToast(errorMessage(error), "error");
    }
  }

  const pathCrumbs = crumbs(readyState.path);
  const folderItems = readyState.folderContext.items
    .filter((item) => item.is_directory)
    .sort((left, right) => fileName(left).localeCompare(fileName(right)));
  const selectedItems = visibleItems.filter((item) => readyState.selectedPaths.includes(item.path));
  const selectedItem = visibleItems.find((item) => item.path === readyState.previewPath) || visibleItems.find((item) => !item.is_directory) || visibleItems[0];
  const selectedCount = readyState.selectedPaths.length;
  const previewItem = selectedItem;

  function selectItem(item: FileMeta) {
    const nextSelected = readyState.selectedPaths.includes(item.path)
      ? readyState.selectedPaths.filter((path: string) => path !== item.path)
      : [...readyState.selectedPaths, item.path];
    setReady({ selectedPaths: nextSelected, previewPath: item.path, previewOpen: shouldOpenPreviewByDefault() });
  }

  function selectAllVisible() {
    const visiblePaths = visibleItems.map((item) => item.path);
    const allSelected = visiblePaths.length > 0 && visiblePaths.every((path) => readyState.selectedPaths.includes(path));
    setReady({ selectedPaths: allSelected ? [] : visiblePaths });
  }

  function openItem(item: FileMeta) {
    if (item.is_directory) {
      void load(item.path, "", activeTarget.key);
      return;
    }
    setReady({ previewPath: item.path, previewOpen: true });
  }

  function openFolderDialog() {
    setReady({
      dialog: { kind: "folder" },
      dialogDraft: "",
      menuPath: "",
    });
  }

  function openDeleteDialog(items: FileMeta[]) {
    if (!items.length) return;
    setReady({
      dialog: { kind: "delete", items },
      dialogDraft: "",
      menuPath: "",
    });
  }

  function openRenameDialog(item: FileMeta) {
    setReady({
      dialog: { kind: "rename", item },
      dialogDraft: fileName(item),
      menuPath: "",
    });
  }

  function openMoveDialog(items: FileMeta[]) {
    if (!items.length) return;
    setReady({
      dialog: { kind: "move", items, destinationPath: readyState.path, destinationSourceID: commandSourceID },
      dialogDraft: readyState.path,
      menuPath: "",
    });
  }

  function rowFor(item: FileMeta) {
    const name = fileName(item);
    const selected = readyState.selectedPaths.includes(item.path);
    return (
      <div className={`file-guide-row${selected ? " selected" : ""}`} key={item.path} onClick={() => openItem(item)} role="row">
        <button className="file-check" type="button" aria-label={`Select ${name}`} aria-pressed={selected} onClick={(event) => { event.stopPropagation(); selectItem(item); }}>
          {selected ? <Icon name="check" /> : null}
        </button>
        <span className="file-guide-name" role="cell" data-label="Name">
          <span className="file-guide-glyph" style={{ color: fileIconTone(item) }} aria-hidden="true"><Icon name={fileIcon(item)} /></span>
          <span className="file-entry-title">
            <button className="file-name-button" type="button" onClick={(event) => { event.stopPropagation(); openItem(item); }}>{name}</button>
            <small className="file-entry-path" title={item.path}>{item.path}</small>
          </span>
        </span>
        <span className="file-guide-mono" role="cell" data-label="Size"><span className="file-mobile-type">{fileType(item)}{item.is_directory ? "" : " · "}</span><span className={item.is_directory ? "file-folder-size" : undefined}>{formatSize(item)}</span></span>
        <span className="file-guide-muted" role="cell" data-label="Type">{fileType(item)}</span>
        <span className="file-guide-mono" role="cell" data-label="Modified">{formatModified(item)}</span>
        <span className="file-guide-menu-cell" role="cell" data-label="Actions">
          <button className="file-menu-button" type="button" aria-label={`More actions for ${name}`} onClick={(event) => { event.stopPropagation(); setReady({ menuPath: readyState.menuPath === item.path ? "" : item.path }); }}>
            <Icon name="dots" />
          </button>
        </span>
      </div>
    );
  }

  return (
    <section className={`dashboard-page file-server-page file-guide-surface${selectionMode || readyState.selectedPaths.length ? " is-selecting" : ""}`} aria-labelledby="route-title">
      <h1 id="route-title" className="visually-hidden">File Server</h1>
      <div className="file-guide-command-surface">
        <div className="file-guide-tools">
          <div className="file-guide-pathbar">
            <div className="file-share-wrap">
              <button className="file-share-button" type="button" aria-expanded={readyState.sharePickerOpen} onClick={() => setReady({ sharePickerOpen: !readyState.sharePickerOpen })}>
                <Icon name="hard-drive" />
                <span className="visually-hidden">Active share</span>
                <span>{activeTarget.name}</span>
                <span className="file-caret" aria-hidden="true">⌄</span>
              </button>
              {readyState.sharePickerOpen ? (
                <div className="file-share-menu" role="menu" aria-label="File shares">
                  {targetOptions.map((target) => (
                    <button
                      key={target.key}
                      role="menuitem"
                      type="button"
                      onClick={() => {
                        setReady({ sharePickerOpen: false });
                        void load("/", "", target.key);
                      }}
                    >
                      <Icon name="hard-drive" />
                      <span><strong>{target.name}</strong><small>{target.detail}</small></span>
                      {target.key === activeTarget.key ? <Icon name="check" /> : null}
                    </button>
                  ))}
                </div>
              ) : null}
            </div>
            <nav className="file-guide-crumbs" aria-label="Path">
              {pathCrumbs.map((crumb, index) => (
                <span className="file-crumb" key={crumb.path}>
                  {index === 0 ? <Icon name="folder" /> : <span className="file-crumb-sep">/</span>}
                  {index === pathCrumbs.length - 1 ? (
                    <span className="file-crumb-current">{crumb.label}</span>
                  ) : (
                    <button type="button" className="file-crumb-link" onClick={() => void load(crumb.path, "", activeTarget.key)}>{crumb.label}</button>
                  )}
                </span>
              ))}
            </nav>
          </div>
          <div className="file-guide-toolbar-cluster">
            <label className="file-search">
              <Icon name="search" />
              <span className="visually-hidden">Search files</span>
              <input type="search" placeholder="Search files" value={readyState.query} onChange={(event) => {
                searchRequestRef.current++;
                setReady({ query: event.target.value, searchItems: null, searching: Boolean(event.target.value.trim()), searchError: false, searchStatus: "ready", message: "" });
              }} />
            </label>
            <div className="file-view-toggle" aria-label="View mode">
              <button type="button" aria-label="List" aria-current={readyState.viewMode === "list" ? "true" : undefined} onClick={() => setReady({ viewMode: "list" })}><Icon name="list" /></button>
              <button type="button" aria-label="Grid" aria-current={readyState.viewMode === "grid" ? "true" : undefined} onClick={() => setReady({ viewMode: "grid" })}><Icon name="grid" /></button>
            </div>
            <div className="file-guide-actions">
              <button className="secondary" type="button" onClick={openFolderDialog}><Icon name="folder-plus" />New folder</button>
              <button type="button" onClick={() => uploadInputRef.current?.click()}><Icon name="upload" />Upload</button>
              <input ref={uploadInputRef} className="visually-hidden" type="file" multiple aria-label="Choose files to upload" onChange={(event) => void uploadFiles(event.currentTarget.files)} />
            </div>
          </div>
        </div>
      </div>

      {readyState.message ? <p className="notice-state">{readyState.message}</p> : null}
      {readyState.uploadIssue ? (
        <div className="file-upload-issue" role="alert">
          <div>
            <strong>{readyState.uploadIssue.summary}</strong>
            <span>Upload requirements</span>
          </div>
          <ul>
            {readyState.uploadIssue.limitations.map((limitation) => <li key={limitation}>{limitation}</li>)}
          </ul>
          <button className="file-icon-action" type="button" aria-label="Dismiss upload error" onClick={() => setReady({ uploadIssue: null })}><Icon name="x" /></button>
        </div>
      ) : null}

      <div className={`file-guide-panes${readyState.previewOpen ? "" : " preview-closed"}`}>
        <aside className="file-tree-pane" aria-labelledby="file-folders-heading">
          <h2 id="file-folders-heading" className="file-pane-label">Folders</h2>
          <button
            className="file-tree-item file-tree-context"
            type="button"
            aria-label={`Open ${readyState.folderContext.path === "/" ? "Home" : readyState.folderContext.path}`}
            aria-current={readyState.path === readyState.folderContext.path ? "page" : undefined}
            onClick={() => void load(readyState.folderContext.path, "", activeTarget.key)}
          >
            <Icon name="folder" />{readyState.folderContext.path === "/" ? "Home" : fileName({ path: readyState.folderContext.path, is_directory: true })}
          </button>
          {folderItems.length ? (
            <div className="file-tree-dynamic" aria-label={`Folders in ${readyState.folderContext.path}`}>
              {folderItems.map((item) => {
                const name = fileName(item);
                return <button aria-label={`Open ${name}`} aria-current={item.path === readyState.path ? "page" : undefined} className="file-tree-item" key={item.path} type="button" onClick={() => void load(item.path, "", activeTarget.key)}><Icon name="folder" />{name}</button>;
              })}
            </div>
          ) : null}
        </aside>

        <section className={`file-list-pane${isRefreshing ? " is-refreshing" : ""}`} aria-label="Files" aria-busy={isRefreshing}>
          <h2 className="visually-hidden">File list</h2>
          <div className="file-mobile-listbar">
            <span>{visibleItems.length} {visibleItems.length === 1 ? "item" : "items"}</span>
            <button className="secondary" type="button" aria-pressed={selectionMode || selectedCount > 0} onClick={() => {
              const selecting = selectionMode || selectedCount > 0;
              setSelectionMode(!selecting);
              if (selecting) setReady({ selectedPaths: [] });
            }}>{selectionMode || selectedCount > 0 ? "Done selecting" : "Select files"}</button>
          </div>
          {isRefreshing ? (
            <p className="file-refresh-status"><span className="spinner" aria-hidden="true" />Opening {readyState.refreshingPath}</p>
          ) : null}
          <div className="file-list-scroll">
            {searchQuery && !readyState.searching && readyState.searchStatus !== "ready" ? (
              <p className="file-refresh-status" role="status">{readyState.searchStatus === "indexing" ? "Indexing this share; results are still arriving." : readyState.searchStatus === "partial" ? "Some folders could not be indexed. Showing available results." : "This agent is offline. Search will resume when it reconnects."}</p>
            ) : null}
            {readyState.searching ? (
              <p className="file-refresh-status" role="status"><span className="spinner" aria-hidden="true" />Searching this share...</p>
            ) : visibleItems.length && readyState.viewMode === "list" ? (
              <div className="file-guide-table" role="table" aria-label="Files">
                <div className="file-guide-head" role="row">
                  <button className="file-check" type="button" aria-label="Select all visible files" onClick={selectAllVisible}>
                    {selectedCount === 0 ? null : selectedCount === visibleItems.length ? <Icon name="check" /> : <Icon name="minus" />}
                  </button>
                  <span role="columnheader">Name</span>
                  <span role="columnheader">Size</span>
                  <span role="columnheader">Type</span>
                  <span role="columnheader">Modified</span>
                  <span />
                </div>
                <div>{visibleItems.map((item) => rowFor(item))}</div>
              </div>
            ) : visibleItems.length && readyState.viewMode === "grid" ? (
              <div className="file-guide-card-grid" aria-label="File grid">
                {visibleItems.map((item) => {
                  const name = fileName(item);
                  const selected = readyState.selectedPaths.includes(item.path);
                  return (
                    <article className={`file-guide-card${selected ? " selected" : ""}`} key={item.path}>
                      <button className="file-card-select" type="button" aria-label={`Select ${name}`} aria-pressed={selected} onClick={() => selectItem(item)}>
                        {selected ? <Icon name="check" /> : null}
                      </button>
                      <button className="file-card-menu" type="button" aria-label={`More actions for ${name}`} onClick={() => setReady({ menuPath: readyState.menuPath === item.path ? "" : item.path })}><Icon name="dots" /></button>
                      <button className="file-card-thumb" type="button" aria-label={`Open ${name}`} onClick={() => openItem(item)}>
                        <span style={{ color: fileIconTone(item) }}><Icon name={fileIcon(item)} /></span>
                      </button>
                      <div className="file-card-copy">
                        <div className="file-entry-title">
                          <strong>{name}</strong>
                          <small className="file-entry-path" title={item.path}>{item.path}</small>
                        </div>
                        <span>{item.is_directory ? "Folder" : `${formatSize(item)} · ${fileType(item)}`}</span>
                      </div>
                    </article>
                  );
                })}
              </div>
            ) : (
              <div className="file-empty-state">
                <Icon name={searchQuery ? "search" : "folder"} />
                <strong>{!searchQuery ? "This folder is empty" : readyState.searchError ? "Search could not finish" : searchQuery.length < 2 ? "Type at least two characters" : readyState.searchStatus === "ready" ? "No files match your search" : "No indexed matches yet"}</strong>
                {readyState.searchError ? (
                  <button type="button" onClick={() => { setReady({ searching: true, searchError: false, message: "" }); setSearchRetry((value) => value + 1); }}>Try search again</button>
                ) : !searchQuery ? <span>Upload files or create a folder to get started.</span> : searchQuery.length < 2 ? null : <span>Try a different name or clear the search field.</span>}
              </div>
            )}
          </div>
          <footer className="file-selection-bar">
            {selectedCount === 0 ? (
              <>
                <span>{visibleItems.length} {visibleItems.length === 1 ? "item" : "items"}</span>
              </>
            ) : (
              <>
                <span className="file-selection-count">{selectedCount} selected</span>
                <div className="file-selection-actions">
                  <button type="button" aria-label="Download selected" onClick={() => void downloadItems(selectedItems)}><Icon name="download" />Download</button>
                  <button className="secondary" type="button" aria-label="Move selected" onClick={() => openMoveDialog(selectedItems)}><Icon name="move" />Move</button>
                  <button className="danger-link" type="button" aria-label="Delete selected" onClick={() => openDeleteDialog(selectedItems)}><Icon name="trash" />Delete</button>
                  <button className="file-icon-action" type="button" aria-label="Clear selection" onClick={() => setReady({ selectedPaths: [] })}><Icon name="x" /></button>
                </div>
              </>
            )}
          </footer>
        </section>

        {readyState.previewOpen && previewItem ? (
          <aside className="file-preview-panel" aria-label="File preview">
            <h2 className="visually-hidden">Preview</h2>
            <header className="file-preview-heading">
              <span>Preview</span>
              <button className="file-preview-close" type="button" aria-label="Close preview" onClick={() => setReady({ previewOpen: false })}><Icon name="x" /></button>
            </header>
            <FilePreviewMedia key={previewURL(previewItem, commandSourceID, commandAgentID)} item={previewItem} sourceID={commandSourceID} agentID={commandAgentID} />
            <div className="file-preview-info">
              <strong>{fileName(previewItem)}</strong>
              <code>{previewItem.path}</code>
              <dl>
                <div><dt>Size</dt><dd>{formatSize(previewItem)}</dd></div>
                <div><dt>Type</dt><dd>{fileType(previewItem)}</dd></div>
                {isImageFile(previewItem) && fileDimensions(previewItem) !== "—" ? <div><dt>Dimensions</dt><dd>{fileDimensions(previewItem)}</dd></div> : null}
                <div><dt>Modified</dt><dd>{formatModified(previewItem)}</dd></div>
                <div><dt>Owner</dt><dd>{previewItem.owner || "Primary Hank Agent"}</dd></div>
              </dl>
            </div>
            <div className="file-preview-actions">
              {!previewItem.is_directory ? (
                <button type="button" aria-label="Download preview" onClick={() => void downloadItems([previewItem])}><Icon name="download" />Download</button>
              ) : (
                <button type="button" aria-label={`Open ${fileName(previewItem)}`} onClick={() => openItem(previewItem)}><Icon name="folder" />Open</button>
              )}
              <button className="secondary" type="button" aria-label="Rename preview" onClick={() => openRenameDialog(previewItem)}><Icon name="pencil" />Rename</button>
              <button className="secondary" type="button" aria-label="Move preview" onClick={() => openMoveDialog([previewItem])}><Icon name="move" />Move</button>
              <button className="danger-link" type="button" aria-label={`Delete ${fileName(previewItem)}`} onClick={() => openDeleteDialog([previewItem])}><Icon name="trash" />Delete</button>
            </div>
          </aside>
        ) : null}
      </div>

      <div
        key="file-activity"
        id="file-activity-panel"
        className="file-activity-region"
        role="region"
        aria-label="File activity"
      >
        <TransferJobsPanel clearing={clearingHistory} isAdmin={isAdmin} state={transferJobs} onRefresh={() => void loadTransferJobs()} onClear={() => void clearFinishedTransferJobs()} onCancel={(job) => void cancelTransferJob(job)} />
      </div>

      {readyState.menuPath ? (
        <FileActionMenu
          item={visibleItems.find((item) => item.path === readyState.menuPath)}
          onClose={() => setReady({ menuPath: "" })}
          onDownload={(item) => void downloadItems([item])}
          onDelete={(item) => openDeleteDialog([item])}
          onMove={(item) => openMoveDialog([item])}
          onOpen={(item) => setReady({ previewPath: item.path, previewOpen: true, menuPath: "" })}
          onRename={openRenameDialog}
          onCopyLink={(item) => void copyDashboardLink(item)}
        />
      ) : null}
      {readyState.dialog ? (
        <FileDialogCard
          dialog={readyState.dialog}
          draft={readyState.dialogDraft}
          onDraft={(dialogDraft) => setReady({ dialogDraft })}
          onClose={() => setReady({ dialog: null, dialogDraft: "" })}
          onCreate={() => void createFolder()}
          onDelete={(items) => void deleteItems(items)}
          onMove={(items, destinationPath, destinationSourceID) => void moveItems(items, destinationPath, destinationSourceID)}
          onMoveSource={(destinationSourceID) => setReady({
            dialog: readyState.dialog?.kind === "move" ? { ...readyState.dialog, destinationSourceID } : readyState.dialog,
          })}
          onRename={(item, name) => void renameItem(item, name)}
          sources={moveTargets}
        />
      ) : null}
    </section>
  );
}

function TransferJobsPanel({ clearing, state, onRefresh, onClear, onCancel, isAdmin }: { clearing: boolean; isAdmin: boolean; state: TransferJobsState; onRefresh: () => void; onClear: () => void; onCancel: (job: FileOperationJob) => void }) {
  const activeJobs = state.jobs.filter((job) => !isTerminalJob(job.status));
  const recentJobs = state.jobs.filter((job) => isTerminalJob(job.status)).slice(0, 8);
  const clearableJobCount = state.jobs.filter(isClearableJob).length;
  const visibleJobs = [...activeJobs, ...recentJobs].slice(0, 10);
  return (
    <section className="file-transfers-panel" aria-labelledby="file-transfers-heading">
      <header>
        <div>
          <h2 id="file-transfers-heading">Transfers</h2>
          <p>{activeJobs.length ? `${activeJobs.length} active` : "No active transfers"}</p>
        </div>
        <div className="file-transfer-header-actions">
          <button className="secondary" type="button" onClick={onRefresh}>Refresh</button>
          <button className="secondary" type="button" disabled={clearing || !clearableJobCount} onClick={onClear}>{clearing ? "Clearing…" : "Clear finished"}</button>
        </div>
      </header>
      {state.jobs.some((job) => job.operation === "move" && job.status === "rollback_required") ? <p className="file-transfer-help">Interrupted moves need review. Clear finished keeps these records until an administrator removes their history.</p> : null}
      {isAdmin ? <HistoricalJobOwners refreshVersion={state.jobs} onSaved={onRefresh} /> : null}
      {state.status === "loading" && !visibleJobs.length ? (
        <p className="file-transfer-empty"><span className="spinner" aria-hidden="true" />Loading transfers...</p>
      ) : null}
      {state.status === "error" ? <p className="error-state">{state.message}</p> : null}
      {state.status !== "loading" && !visibleJobs.length ? (
        <p className="file-transfer-empty">Downloads, uploads, and moves will appear here.</p>
      ) : null}
      {visibleJobs.length ? (
        <div className="file-transfer-list">
          {visibleJobs.map((job) => <TransferJobRow key={job.id} job={job} isAdmin={isAdmin} onOwnerSaved={onRefresh} onCancel={onCancel} />)}
        </div>
      ) : null}
    </section>
  );
}

function TransferJobRow({ job, isAdmin, onOwnerSaved, onCancel }: { job: FileOperationJob; isAdmin: boolean; onOwnerSaved: () => void; onCancel?: (job: FileOperationJob) => void }) {
  const progress = jobProgress(job);
  const updated = job.completed_at || job.updated_at || job.created_at;
  const bytesTotal = Number(job.bytes_total || 0);
  const bytesDone = Number(job.bytes_done || 0);
  return (
    <article className={`file-transfer-job status-${job.status.replaceAll("_", "-")}`}>
      <span className="file-transfer-icon" aria-hidden="true"><Icon name={job.operation === "upload" ? "upload" : job.operation === "move" ? "move" : "download"} /></span>
      <div className="file-transfer-main">
        <div className="file-transfer-title">
          <strong>{jobTitle(job)}</strong>
          <span>{formatJobTime(updated)}</span>
        </div>
        <p>{jobDetail(job)}</p>
        {job.agent_id ? <p>Owning machine: {job.agent_id}</p> : null}
        {isAdmin && !onCancel && !job.agent_id && job.operation === "move" && ["failed", "cancelled", "rollback_required"].includes(job.status)
          ? <FileJobOwnerAssignment job={job} onSaved={onOwnerSaved} /> : null}
        {job.error_message ? <p className="file-transfer-error">{job.error_message}</p> : null}
        {onCancel && ["upload", "download"].includes(job.operation) && ["queued", "running"].includes(job.status)
          ? <button className="secondary file-transfer-cancel" type="button" onClick={() => onCancel(job)}>Cancel {jobTitle(job)}</button> : null}
        {job.status === "rollback_required" && job.operation === "move" && isAdmin ? <RemoveFileJobHistory job={job} onRemoved={onOwnerSaved} /> : null}
        {!isTerminalJob(job.status) ? <div className="file-transfer-progress" aria-label={`${jobTitle(job)} progress`}>
          <span style={{ width: `${progress}%` }} />
        </div> : null}
        {bytesTotal > 0 ? <small>{formatBytes(bytesDone)} of {formatBytes(bytesTotal)}</small> : null}
      </div>
      <span className="status-pill">{job.status === "rollback_required" ? "Needs review" : job.status.replaceAll("_", " ")}</span>
    </article>
  );
}

function FileActionMenu({
  item,
  onClose,
  onDownload,
  onDelete,
  onMove,
  onOpen,
  onRename,
  onCopyLink,
}: {
  item?: FileMeta;
  onClose: () => void;
  onDownload: (item: FileMeta) => void;
  onDelete: (item: FileMeta) => void;
  onMove: (item: FileMeta) => void;
  onOpen: (item: FileMeta) => void;
  onRename: (item: FileMeta) => void;
  onCopyLink: (item: FileMeta) => void;
}) {
  if (!item) return null;
  return (
    <div className="file-context-menu" role="menu" aria-label="File actions">
      <button role="menuitem" type="button" onClick={() => { onOpen(item); onClose(); }}><Icon name="file" />Open</button>
      <button role="menuitem" type="button" onClick={() => { onCopyLink(item); onClose(); }}><Icon name="file" />{item.is_directory ? "Copy link" : "Copy preview link"}</button>
      {!item.is_directory ? <button role="menuitem" type="button" onClick={() => { onDownload(item); onClose(); }}><Icon name="download" />Download</button> : null}
      <button role="menuitem" type="button" onClick={() => { onRename(item); onClose(); }}><Icon name="pencil" />Rename</button>
      <button role="menuitem" type="button" onClick={() => { onMove(item); onClose(); }}><Icon name="move" />Move</button>
      <button role="menuitem" type="button" className="danger" onClick={() => onDelete(item)}><Icon name="trash" />Delete</button>
    </div>
  );
}

function FileDialogCard({
  dialog,
  draft,
  onDraft,
  onClose,
  onCreate,
  onDelete,
  onMove,
  onMoveSource,
  onRename,
  sources,
}: {
  dialog: FileDialog;
  draft: string;
  onDraft: (value: string) => void;
  onClose: () => void;
  onCreate: () => void;
  onDelete: (items: FileMeta[]) => void;
  onMove: (items: FileMeta[], destinationPath: string, destinationSourceID: string) => void;
  onMoveSource: (destinationSourceID: string) => void;
  onRename: (item: FileMeta, name: string) => void;
  sources: FileTarget[];
}) {
  const title = dialog.kind === "folder"
    ? "New folder"
    : dialog.kind === "rename"
      ? "Rename"
      : dialog.kind === "move"
        ? dialog.items.length === 1 ? `Move "${fileName(dialog.items[0])}"` : `Move ${dialog.items.length} items`
        : dialog.items.length === 1
          ? `Delete "${fileName(dialog.items[0])}"?`
          : `Delete ${dialog.items.length} items?`;
  const icon = dialog.kind === "folder" ? "folder-plus" : dialog.kind === "rename" ? "pencil" : dialog.kind === "move" ? "move" : "trash";
  return (
    <div className="guide-dialog-scrim" role="presentation" onClick={onClose}>
      <section className="guide-dialog" role="dialog" aria-modal="true" aria-label={title} onClick={(event) => event.stopPropagation()}>
        <header>
          <span className="guide-dialog-icon" aria-hidden="true"><Icon name={icon} /></span>
          <h2>{title}</h2>
          <button className="file-icon-action" type="button" aria-label="Close dialog" onClick={onClose}><Icon name="x" /></button>
        </header>
        {dialog.kind === "folder" || dialog.kind === "rename" ? (
          <label className="guide-dialog-field">
            <span>{dialog.kind === "folder" ? "Folder name" : "File name"}</span>
            <input autoFocus placeholder="Untitled folder" value={draft} onChange={(event) => onDraft(event.target.value)} />
          </label>
        ) : null}
        {dialog.kind === "move" ? (
          <>
            <label className="guide-dialog-field">
              <span>Destination path</span>
              <input autoFocus value={draft} onChange={(event) => onDraft(event.target.value)} />
            </label>
            {sources.length > 1 ? (
              <label className="guide-dialog-field">
                <span>Destination share</span>
                <select value={dialog.destinationSourceID} onChange={(event) => onMoveSource(event.target.value)}>
                  {sources.map((source) => <option key={source.key} value={source.sourceID}>{source.name}</option>)}
                </select>
              </label>
            ) : null}
            <p className="guide-dialog-copy">Move {dialog.items.length === 1 ? fileName(dialog.items[0]) : `${dialog.items.length} selected items`} into {draft || dialog.destinationPath}.</p>
          </>
        ) : null}
        {dialog.kind === "delete" ? (
          <p className="guide-dialog-copy">This will delete {dialog.items.length === 1 ? fileName(dialog.items[0]) : `${dialog.items.length} selected items`} from the active source.</p>
        ) : null}
        <footer>
          <button className="secondary" type="button" onClick={onClose}>Cancel</button>
          {dialog.kind === "folder" ? <button type="button" onClick={onCreate}>Create folder</button> : null}
          {dialog.kind === "rename" ? <button type="button" onClick={() => onRename(dialog.item, draft)}>Rename</button> : null}
          {dialog.kind === "move" ? <button type="button" onClick={() => onMove(dialog.items, draft || dialog.destinationPath, dialog.destinationSourceID)}>Move here</button> : null}
          {dialog.kind === "delete" ? <button className="danger-solid" type="button" onClick={() => onDelete(dialog.items)}>Delete</button> : null}
        </footer>
      </section>
    </div>
  );
}

function HistoricalJobOwners({ refreshVersion, onSaved }: { refreshVersion: FileOperationJob[]; onSaved: () => void }) {
  const [queue, setQueue] = useState<{ jobs: FileOperationJob[]; next_cursor: string }>({ jobs: [], next_cursor: "" });
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [reload, setReload] = useState(0);
  useEffect(() => {
    let active = true;
    setLoading(true);
    setError("");
    void fileServerClient.listOwnerlessJobs().then((result) => { if (active) setQueue(result); })
      .catch((reason: unknown) => { if (active) setError(errorMessage(reason)); })
      .finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [refreshVersion, reload]);
  async function loadMore() {
    setLoading(true); setError("");
    try {
      const next = await fileServerClient.listOwnerlessJobs(queue.next_cursor);
      setQueue((current) => ({ jobs: [...current.jobs, ...next.jobs.filter((job) => !current.jobs.some((existing) => existing.id === job.id))], next_cursor: next.next_cursor }));
    } catch (reason) { setError(errorMessage(reason)); }
    finally { setLoading(false); }
  }
  if (!loading && !error && !queue.jobs.length) return null;
  return <section aria-label="Historical move owner reviews" className="file-transfer-list">
    <h3>Moves needing owner review</h3>
    <p>These moves need a verified machine before recovery. Older moves remain available here.</p>
    {error ? <p role="alert">{error} <button type="button" onClick={() => setReload((value) => value + 1)}>Retry owner reviews</button></p> : null}
    {queue.jobs.map((job) => <TransferJobRow key={job.id} job={job} isAdmin onOwnerSaved={() => { setReload((value) => value + 1); onSaved(); }} />)}
    {loading ? <p role="status">Loading owner reviews…</p> : null}
    {queue.next_cursor ? <button type="button" className="secondary" disabled={loading} onClick={() => void loadMore()}>Load more owner reviews</button> : null}
  </section>;
}

function RemoveFileJobHistory({ job, onRemoved }: { job: FileOperationJob; onRemoved: () => void }) {
  const { confirm } = useConfirmDialog();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function remove() {
    if (busy || !job.updated_at) return;
    setBusy(true); setError("");
    try {
      const review = await fileServerClient.reviewJobDismissal(job.id, job.updated_at);
      if (!await confirm({ title: "Remove interrupted move from history?", message: `Remove the record for ${jobTitle(job)}? This does not move, delete, or roll back any files. Any partial copy remains. Removing the record also removes its retry and rollback information. Check the source and destination before continuing.`, confirmLabel: "Remove history", tone: "danger" })) return;
      await fileServerClient.dismissJob(job.id, job.updated_at, review.admin_action_token);
      onRemoved();
    } catch (reason) { setError(errorMessage(reason)); }
    finally { setBusy(false); }
  }
  return <div className="file-history-removal">
    <button className="secondary" type="button" disabled={busy || !job.updated_at} onClick={() => void remove()}>{busy ? "Reviewing…" : "Remove from history"}</button>
    {error ? <p role="alert">{error}</p> : null}
  </div>;
}

function FilePreviewMedia({ item, sourceID, agentID }: { item: FileMeta; sourceID: string; agentID: string }) {
  const [failed, setFailed] = useState(false);
  const src = previewURL(item, sourceID, agentID);
  const label = `Preview ${fileName(item)}`;
  return <div className={`file-preview-media${isPDFFile(item) || isMarkdownFile(item) || isHTMLFile(item) ? " is-document" : ""}`}>
    {failed ? <div className="file-preview-unavailable"><Icon name="file" /><strong>Preview couldn’t load</strong><span>Download the file to open it on your device.</span></div>
      : isImageFile(item) ? <img src={src} alt={label} onError={() => setFailed(true)} />
      : isVideoFile(item) ? <video src={src} controls playsInline preload="metadata" aria-label={label} onError={() => setFailed(true)} />
      : isAudioFile(item) ? <div className="file-audio-preview"><Icon name="audio" /><audio src={src} controls preload="metadata" aria-label={label} onError={() => setFailed(true)} /></div>
      : isPDFFile(item) || isMarkdownFile(item) || isHTMLFile(item) ? <iframe sandbox={isHTMLFile(item) ? "" : undefined} src={src} title={label} />
      : <div className="file-preview-unavailable"><span style={{ color: fileIconTone(item) }}><Icon name={fileIcon(item)} /></span><strong>{item.is_directory ? "Folder" : "No preview available"}</strong><span>{item.is_directory ? "Open this folder to browse its files." : "Download the file to open it on your device."}</span></div>}
  </div>;
}
