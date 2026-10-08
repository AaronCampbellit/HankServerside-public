import type { BootstrapPermissions } from "../api/bootstrap";
import type {
  KanbanBoard,
  NoteAttachment,
  SaveProfileNoteInput,
} from "../api/profileNotes";

export type OfflineNoteState =
  | "clean"
  | "queued"
  | "syncing"
  | "conflicted"
  | "deletion_review"
  | "failed";

export type OfflineMutationType = "save_note" | "delete_note" | "set_default_board";
export type OfflineMutationState = "queued" | "claimed" | "retryable" | "conflicted" | "failed";
export type OfflineStoreName =
  | "metadata"
  | "workspaces"
  | "notes"
  | "mutations"
  | "attachment_blobs"
  | "sync_leases";

export type OfflineIdentity = {
  userID: string;
  email: string;
  displayName: string;
  role: string;
  permissions: Pick<BootstrapPermissions, "is_admin" | "can_use_notes">;
  lastAuthenticatedAt: number;
  offlineInitializedAt: number;
};

export type OfflineNoteRecord = {
  userID: string;
  localKey: string;
  serverNoteID: string;
  serverRevision: string;
  baseRevision: string;
  title: string;
  bodyMarkdown: string;
  pageType: string;
  parentLocalKey: string;
  pinned: boolean;
  mcpExcluded: boolean;
  board: KanbanBoard | null;
  attachments: NoteAttachment[];
  createdLocally: boolean;
  state: OfflineNoteState;
  error: string;
  createdAt: number;
  updatedAt: number;
};

export type OfflineMutationRecord = {
  mutationID: string;
  userID: string;
  sequence: number;
  type: OfflineMutationType;
  localKey: string;
  serverNoteID: string;
  dependencyLocalKeys: string[];
  baseRevision: string;
  payload: SaveProfileNoteInput | { boardLocalKey: string } | null;
  state: OfflineMutationState;
  attempts: number;
  nextRetryAt: number;
  leaseOwner: string;
  leaseExpiresAt: number;
  createdAt: number;
  lastAttemptAt: number;
  error: string;
};

export type OfflineWorkspaceRecord = {
  userID: string;
  identity: OfflineIdentity;
  profileSettingsRevision: number;
  defaultBoardLocalKey: string;
  lastReconciledAt: number;
  nextSequence: number;
};

export type OfflineAttachmentBlob = {
  userID: string;
  attachmentID: string;
  noteLocalKey: string;
  filename: string;
  mediaType: string;
  size: number;
  blob: Blob;
  cachedAt: number;
  lastAccessedAt: number;
};

export type OfflineSyncLease = {
  userID: string;
  ownerID: string;
  expiresAt: number;
};

export type OfflineSyncSummary = {
  pending: number;
  syncing: number;
  failed: number;
  conflicted: number;
  authenticationRequired: boolean;
  lastSyncedAt: number;
};

export type OfflineSaveResult = {
  localKey: string;
  noteID: string;
  revision: string;
  status: "saved" | "saved_offline";
};

export type OfflineRepositoryErrorCode =
  | "storage_unavailable"
  | "quota_exceeded"
  | "validation_failed"
  | "authentication_required";

export class OfflineRepositoryError extends Error {
  constructor(readonly code: OfflineRepositoryErrorCode, message: string) {
    super(message);
    this.name = "OfflineRepositoryError";
  }
}
