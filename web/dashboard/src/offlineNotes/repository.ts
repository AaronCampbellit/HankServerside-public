import type { ProfileSettingsResponse } from "../api/profileSettings";
import {
  noteID,
  type NoteAttachment,
  type ProfileNote,
  type ProfileNoteSummary,
  type SaveProfileNoteInput,
} from "../api/profileNotes";
import type { OfflineNotesDatabase } from "./database";
import {
  OfflineRepositoryError,
  type OfflineAttachmentBlob,
  type OfflineMutationRecord,
  type OfflineNoteRecord,
  type OfflineSaveResult,
  type OfflineSyncLease,
  type OfflineWorkspaceRecord,
} from "./types";

function requestResult<T>(request: IDBRequest<T>): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error ?? new Error("IndexedDB request failed"));
  });
}

function profileNote(record: OfflineNoteRecord): ProfileNote {
  return {
    id: record.localKey,
    note_id: record.localKey,
    title: record.title,
    preview: record.bodyMarkdown.slice(0, 160),
    revision: record.serverRevision,
    updated_at: new Date(record.updatedAt).toISOString(),
    page_type: record.pageType,
    parent_id: record.parentLocalKey,
    pinned: record.pinned,
    mcp_excluded: record.mcpExcluded,
    content: record.bodyMarkdown,
    body_markdown: record.bodyMarkdown,
    body_format: "markdown",
    board: record.board,
    attachments: record.attachments,
    offline_state: record.state,
  };
}

function serverRecord(
  userID: string,
  note: ProfileNote,
  localKey: string,
  parentLocalKey: string,
  now: number,
): OfflineNoteRecord {
  const serverNoteID = noteID(note);
  const updatedAt = note.updated_at ? Date.parse(note.updated_at) : now;
  return {
    userID,
    localKey,
    serverNoteID,
    serverRevision: note.revision ?? "",
    baseRevision: note.revision ?? "",
    title: note.title ?? "",
    bodyMarkdown: note.body_markdown ?? note.content ?? "",
    pageType: note.page_type ?? "text",
    parentLocalKey,
    pinned: Boolean(note.pinned),
    mcpExcluded: Boolean(note.mcp_excluded),
    board: note.board ?? null,
    attachments: note.attachments ?? [],
    createdLocally: false,
    state: "clean",
    error: "",
    createdAt: Number.isFinite(updatedAt) ? updatedAt : now,
    updatedAt: Number.isFinite(updatedAt) ? updatedAt : now,
  };
}

function mutationIsPending(mutation: OfflineMutationRecord): boolean {
  return mutation.state === "queued" || mutation.state === "claimed" || mutation.state === "retryable";
}

function mutationIsCoalescable(mutation: OfflineMutationRecord): boolean {
  return mutation.state === "queued" || mutation.state === "retryable" || mutation.state === "failed";
}

async function ownedClaim(
  transaction: IDBTransaction,
  userID: string,
  mutationID: string,
  ownerID: string,
  now: number,
): Promise<OfflineMutationRecord | null> {
  const mutation = await requestResult(
    transaction.objectStore("mutations").get([userID, mutationID]),
  ) as OfflineMutationRecord | undefined;
  const lease = await requestResult(
    transaction.objectStore("sync_leases").get(userID),
  ) as OfflineSyncLease | undefined;
  if (
    !mutation
    || mutation.state !== "claimed"
    || mutation.leaseOwner !== ownerID
    || !lease
    || lease.ownerID !== ownerID
    || lease.expiresAt <= now
  ) return null;
  return mutation;
}

async function ownedSyncLease(
  transaction: IDBTransaction,
  userID: string,
  ownerID: string,
  now: number,
): Promise<boolean> {
  const lease = await requestResult(
    transaction.objectStore("sync_leases").get(userID),
  ) as OfflineSyncLease | undefined;
  return Boolean(lease && lease.ownerID === ownerID && lease.expiresAt > now);
}

export class OfflineNotesRepository {
  private readonly listeners = new Set<(source?: "local" | "external") => void>();

  constructor(
    readonly userID: string,
    private readonly database: OfflineNotesDatabase,
    private readonly now: () => number = Date.now,
    private readonly randomUUID: () => string = () => crypto.randomUUID(),
    private readonly attachmentCapBytes = 100 * 1024 * 1024,
  ) {}

  async listNotes(): Promise<ProfileNoteSummary[]> {
    const records = await this.database.listNotes(this.userID);
    return records
      .map(profileNote)
      .sort((left, right) => String(right.updated_at).localeCompare(String(left.updated_at)));
  }

  listNoteRecords(): Promise<OfflineNoteRecord[]> {
    return this.database.listNotes(this.userID);
  }

  async fetchNote(localOrServerID: string): Promise<ProfileNote> {
    const record = await this.findRecord(localOrServerID);
    if (!record) throw new OfflineRepositoryError("validation_failed", "Note is not available offline");
    return profileNote(record);
  }

  async saveNote(input: SaveProfileNoteInput): Promise<OfflineSaveResult> {
    if (!input.title.trim()) {
      throw new OfflineRepositoryError("validation_failed", "Note title is required");
    }
    const now = this.now();
    const result = await this.database.runTransaction(["workspaces", "notes", "mutations"], "readwrite", async (transaction) => {
      const workspaces = transaction.objectStore("workspaces");
      const notes = transaction.objectStore("notes");
      const mutations = transaction.objectStore("mutations");
      const mutationIndex = mutations.index("mutations_by_user_note");
      const workspace = await requestResult(workspaces.get(this.userID)) as OfflineWorkspaceRecord | undefined;
      if (!workspace) {
        throw new OfflineRepositoryError("storage_unavailable", "Offline Notes workspace is not initialized");
      }

      let record = input.note_id
        ? await requestResult(notes.get([this.userID, input.note_id])) as OfflineNoteRecord | undefined
        : undefined;
      if (!record && input.note_id) {
        record = await requestResult(notes.index("notes_by_user_server_id").get([this.userID, input.note_id])) as OfflineNoteRecord | undefined;
      }

      if (!record) {
        const uuid = this.randomUUID();
        const localKey = `local:${uuid}`;
        const serverNoteID = input.note_id || `offline-${uuid}.md`;
        record = {
          userID: this.userID,
          localKey,
          serverNoteID,
          serverRevision: "",
          baseRevision: "",
          title: input.title,
          bodyMarkdown: "",
          pageType: input.page_type || "text",
          parentLocalKey: "",
          pinned: false,
          mcpExcluded: false,
          board: null,
          attachments: [],
          createdLocally: true,
          state: "queued",
          error: "",
          createdAt: now,
          updatedAt: now,
        };
      }

      let parentLocalKey = "";
      const dependencies: string[] = [];
      if (input.parent_id) {
        const direct = await requestResult(notes.get([this.userID, input.parent_id])) as OfflineNoteRecord | undefined;
        const parent = direct ?? await requestResult(
          notes.index("notes_by_user_server_id").get([this.userID, input.parent_id]),
        ) as OfflineNoteRecord | undefined;
        parentLocalKey = parent?.localKey ?? input.parent_id;
        if (parent?.createdLocally) dependencies.push(parent.localKey);
      }

      const existingMutations = await requestResult(
        mutationIndex.getAll([this.userID, record.localKey]),
      ) as OfflineMutationRecord[];
      const existingSave = existingMutations.find(
        (mutation) => mutation.type === "save_note" && mutationIsCoalescable(mutation),
      );
      const baseRevision = existingSave?.baseRevision ?? record.baseRevision ?? record.serverRevision;
      const payload: SaveProfileNoteInput = {
        ...input,
        note_id: record.serverNoteID,
        parent_id: parentLocalKey,
        expected_revision: baseRevision,
      };
      const mutation: OfflineMutationRecord = existingSave
        ? {
            ...existingSave,
            payload,
            dependencyLocalKeys: dependencies,
            state: "queued",
            nextRetryAt: 0,
            leaseOwner: "",
            leaseExpiresAt: 0,
            error: "",
          }
        : {
            mutationID: this.randomUUID(),
            userID: this.userID,
            sequence: workspace.nextSequence,
            type: "save_note",
            localKey: record.localKey,
            serverNoteID: record.serverNoteID,
            dependencyLocalKeys: dependencies,
            baseRevision,
            payload,
            state: "queued",
            attempts: 0,
            nextRetryAt: 0,
            leaseOwner: "",
            leaseExpiresAt: 0,
            createdAt: now,
            lastAttemptAt: 0,
            error: "",
          };

      const updated: OfflineNoteRecord = {
        ...record,
        baseRevision,
        title: input.title,
        bodyMarkdown: input.body_markdown,
        pageType: input.page_type || "text",
        parentLocalKey,
        pinned: Boolean(input.pinned),
        mcpExcluded: Boolean(input.mcp_excluded),
        board: input.page_type === "kanban" ? input.board ?? { columns: [] } : null,
        state: "queued",
        error: "",
        updatedAt: now,
      };
      notes.put(updated);
      mutations.put(mutation);
      if (!existingSave) workspaces.put({ ...workspace, nextSequence: workspace.nextSequence + 1 });
      return {
        localKey: updated.localKey,
        noteID: updated.serverNoteID,
        revision: updated.serverRevision,
        status: "saved_offline" as const,
      };
    });
    this.notify();
    return result;
  }

  async deleteNote(localOrServerID: string): Promise<void> {
    const now = this.now();
    await this.database.runTransaction(["workspaces", "notes", "mutations"], "readwrite", async (transaction) => {
      const workspaces = transaction.objectStore("workspaces");
      const notes = transaction.objectStore("notes");
      const mutations = transaction.objectStore("mutations");
      const record = await this.findRecordInStore(notes, localOrServerID);
      if (!record) return;
      const workspace = await requestResult(workspaces.get(this.userID)) as OfflineWorkspaceRecord | undefined;
      if (!workspace) throw new OfflineRepositoryError("storage_unavailable", "Offline Notes workspace is not initialized");
      const existing = await requestResult(
        mutations.index("mutations_by_user_note").getAll([this.userID, record.localKey]),
      ) as OfflineMutationRecord[];
      const claimedSave = existing.find(
        (mutation) => mutation.type === "save_note" && mutation.state === "claimed",
      );
      for (const mutation of existing) {
        if (mutation.mutationID !== claimedSave?.mutationID) {
          mutations.delete([this.userID, mutation.mutationID]);
        }
      }
      notes.delete([this.userID, record.localKey]);
      if (record.createdLocally && !claimedSave) return;

      const firstSave = existing.find((mutation) => mutation.type === "save_note");
      const baseRevision = firstSave?.baseRevision || record.baseRevision || record.serverRevision;
      mutations.put({
        mutationID: this.randomUUID(),
        userID: this.userID,
        sequence: workspace.nextSequence,
        type: "delete_note",
        localKey: record.localKey,
        serverNoteID: record.serverNoteID,
        dependencyLocalKeys: [],
        baseRevision,
        payload: null,
        state: "queued",
        attempts: 0,
        nextRetryAt: 0,
        leaseOwner: "",
        leaseExpiresAt: 0,
        createdAt: now,
        lastAttemptAt: 0,
        error: "",
      } satisfies OfflineMutationRecord);
      workspaces.put({ ...workspace, nextSequence: workspace.nextSequence + 1 });
    });
    this.notify();
  }

  async setDefaultBoard(localOrServerID: string): Promise<void> {
    const now = this.now();
    await this.database.runTransaction(["workspaces", "notes", "mutations"], "readwrite", async (transaction) => {
      const workspaces = transaction.objectStore("workspaces");
      const mutations = transaction.objectStore("mutations");
      const note = localOrServerID
        ? await this.findRecordInStore(transaction.objectStore("notes"), localOrServerID)
        : undefined;
      const workspace = await requestResult(workspaces.get(this.userID)) as OfflineWorkspaceRecord | undefined;
      if (!workspace || (localOrServerID && !note)) {
        throw new OfflineRepositoryError("validation_failed", "Board is not available offline");
      }
      const localKey = note?.localKey ?? "";
      const all = await requestResult(
        mutations.index("mutations_by_user_sequence").getAll(
          IDBKeyRange.bound([this.userID, 0], [this.userID, Number.MAX_SAFE_INTEGER]),
        ),
      ) as OfflineMutationRecord[];
      const existing = all.find((mutation) => mutation.type === "set_default_board" && mutationIsCoalescable(mutation));
      mutations.put(existing ? {
        ...existing,
        localKey,
        serverNoteID: note?.serverNoteID ?? "",
        dependencyLocalKeys: note?.createdLocally ? [note.localKey] : [],
        payload: { boardLocalKey: localKey },
        state: "queued",
        nextRetryAt: 0,
        error: "",
      } satisfies OfflineMutationRecord : {
        mutationID: this.randomUUID(),
        userID: this.userID,
        sequence: workspace.nextSequence,
        type: "set_default_board",
        localKey,
        serverNoteID: note?.serverNoteID ?? "",
        dependencyLocalKeys: note?.createdLocally ? [note.localKey] : [],
        baseRevision: String(workspace.profileSettingsRevision),
        payload: { boardLocalKey: localKey },
        state: "queued",
        attempts: 0,
        nextRetryAt: 0,
        leaseOwner: "",
        leaseExpiresAt: 0,
        createdAt: now,
        lastAttemptAt: 0,
        error: "",
      } satisfies OfflineMutationRecord);
      workspaces.put({
        ...workspace,
        defaultBoardLocalKey: localKey,
        nextSequence: existing ? workspace.nextSequence : workspace.nextSequence + 1,
      });
    });
    this.notify();
  }

  pendingMutations(): Promise<OfflineMutationRecord[]> {
    return this.database.listMutations(this.userID);
  }

  subscribe(listener: (source?: "local" | "external") => void): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  refreshFromExternal(): void {
    this.notify("external");
  }

  claimSyncLease(ownerID: string, now: number, ttlMs: number): Promise<boolean> {
    return this.database.claimSyncLease(this.userID, ownerID, now, ttlMs);
  }

  renewSyncLease(ownerID: string, now: number, ttlMs: number): Promise<boolean> {
    return this.database.renewSyncLease(this.userID, ownerID, now, ttlMs);
  }

  releaseSyncLease(ownerID: string): Promise<void> {
    return this.database.releaseSyncLease(this.userID, ownerID);
  }

  async claimMutation(
    mutationID: string,
    ownerID: string,
    now: number,
    ttlMs: number,
  ): Promise<OfflineMutationRecord | null> {
    const claimed = await this.database.runTransaction(["notes", "mutations", "sync_leases"], "readwrite", async (transaction) => {
      if (!await ownedSyncLease(transaction, this.userID, ownerID, now)) return null;
      const store = transaction.objectStore("mutations");
      const mutation = await requestResult(
        store.get([this.userID, mutationID]),
      ) as OfflineMutationRecord | undefined;
      if (!mutation || mutation.state === "failed" || mutation.state === "conflicted") return null;
      if (mutation.state === "claimed" && mutation.leaseOwner !== ownerID && mutation.leaseExpiresAt > now) return null;
      const claimed = {
        ...mutation,
        state: "claimed",
        leaseOwner: ownerID,
        leaseExpiresAt: now + ttlMs,
        lastAttemptAt: now,
      } satisfies OfflineMutationRecord;
      store.put(claimed);
      if (claimed.localKey) {
        const notes = transaction.objectStore("notes");
        const note = await requestResult(notes.get([this.userID, claimed.localKey])) as OfflineNoteRecord | undefined;
        if (note) notes.put({ ...note, state: "syncing", error: "" } satisfies OfflineNoteRecord);
      }
      return claimed;
    });
    if (claimed) this.notify();
    return claimed;
  }

  getNoteRecord(localOrServerID: string): Promise<OfflineNoteRecord | undefined> {
    return this.findRecord(localOrServerID);
  }

  async getDefaultBoardLocalKey(): Promise<string> {
    return (await this.database.getWorkspace(this.userID))?.defaultBoardLocalKey ?? "";
  }

  async cacheAttachment(noteLocalKey: string, attachment: NoteAttachment, blob: Blob): Promise<void> {
    const now = this.now();
    await this.database.putAttachment({
      userID: this.userID,
      attachmentID: attachment.id,
      noteLocalKey,
      filename: attachment.filename,
      mediaType: blob.type || attachment.content_type,
      size: blob.size,
      blob,
      cachedAt: now,
      lastAccessedAt: now,
    } satisfies OfflineAttachmentBlob);
    await this.database.evictAttachmentBlobs(this.userID, this.attachmentCapBytes);
  }

  async readCachedAttachment(attachmentID: string): Promise<Blob | null> {
    const cached = await this.database.getAttachment(this.userID, attachmentID);
    if (!cached) return null;
    await this.database.putAttachment({ ...cached, lastAccessedAt: this.now() });
    return cached.blob;
  }

  async reassignCreatedNote(
    mutation: OfflineMutationRecord,
    ownerID: string,
    now: number,
  ): Promise<{ note: OfflineNoteRecord; mutation: OfflineMutationRecord } | null> {
    return this.database.runTransaction(["notes", "mutations", "sync_leases"], "readwrite", async (transaction) => {
      const notes = transaction.objectStore("notes");
      const mutations = transaction.objectStore("mutations");
      const note = await requestResult(notes.get([this.userID, mutation.localKey])) as OfflineNoteRecord | undefined;
      const current = await ownedClaim(transaction, this.userID, mutation.mutationID, ownerID, now);
      if (!current) return null;
      if (!note || !current || !note.createdLocally || !current.payload || !("body_markdown" in current.payload)) {
        throw new OfflineRepositoryError("validation_failed", "Only a pending local note can receive a new sync ID");
      }
      const uuid = this.randomUUID();
      const serverNoteID = `offline-${uuid}.md`;
      const updatedNote = { ...note, serverNoteID } satisfies OfflineNoteRecord;
      const updatedMutation = {
        ...current,
        serverNoteID,
        payload: { ...current.payload, note_id: serverNoteID },
      } satisfies OfflineMutationRecord;
      notes.put(updatedNote);
      mutations.put(updatedMutation);
      return { note: updatedNote, mutation: updatedMutation };
    });
  }

  async acknowledgeSave(
    mutation: OfflineMutationRecord,
    response: { note_id: string; revision: string; updated_at?: string },
    ownerID: string,
    now: number,
  ): Promise<void> {
    let committed = false;
    await this.database.runTransaction(["notes", "mutations", "sync_leases"], "readwrite", async (transaction) => {
      const notes = transaction.objectStore("notes");
      const mutations = transaction.objectStore("mutations");
      if (!await ownedClaim(transaction, this.userID, mutation.mutationID, ownerID, now)) return;
      const record = await requestResult(notes.get([this.userID, mutation.localKey])) as OfflineNoteRecord | undefined;
      const related = await requestResult(
        mutations.index("mutations_by_user_note").getAll([this.userID, mutation.localKey]),
      ) as OfflineMutationRecord[];
      const successors = related.filter((candidate) => (
        candidate.mutationID !== mutation.mutationID
        && candidate.sequence > mutation.sequence
        && mutationIsPending(candidate)
      ));
      for (const successor of successors) {
        const payload = successor.type === "save_note" && successor.payload && "body_markdown" in successor.payload
          ? { ...successor.payload, expected_revision: response.revision }
          : successor.payload;
        mutations.put({
          ...successor,
          serverNoteID: response.note_id || successor.serverNoteID,
          baseRevision: successor.type === "save_note" || successor.type === "delete_note"
            ? response.revision
            : successor.baseRevision,
          payload,
        } satisfies OfflineMutationRecord);
      }
      const hasSaveSuccessor = successors.some((successor) => successor.type === "save_note");
      if (record) {
        const updatedAt = response.updated_at ? Date.parse(response.updated_at) : this.now();
        notes.put({
          ...record,
          serverNoteID: response.note_id || record.serverNoteID,
          serverRevision: response.revision,
          baseRevision: response.revision,
          createdLocally: false,
          state: hasSaveSuccessor ? "queued" : "clean",
          error: "",
          updatedAt: hasSaveSuccessor
            ? record.updatedAt
            : Number.isFinite(updatedAt) ? updatedAt : this.now(),
        } satisfies OfflineNoteRecord);
      }
      mutations.delete([this.userID, mutation.mutationID]);
      committed = true;
    });
    if (committed) this.notify();
  }

  async acknowledgeDelete(mutation: OfflineMutationRecord, ownerID: string, now: number): Promise<void> {
    let committed = false;
    await this.database.runTransaction(["mutations", "sync_leases"], "readwrite", async (transaction) => {
      if (!await ownedClaim(transaction, this.userID, mutation.mutationID, ownerID, now)) return;
      transaction.objectStore("mutations").delete([this.userID, mutation.mutationID]);
      committed = true;
    });
    if (committed) this.notify();
  }

  async resolveSaveConflict(
    mutation: OfflineMutationRecord,
    current: ProfileNote,
    ownerID: string,
    claimedAt: number,
  ): Promise<OfflineNoteRecord | null> {
    const now = this.now();
    let committed = false;
    const conflict = await this.database.runTransaction(
      ["workspaces", "notes", "mutations", "sync_leases"],
      "readwrite",
      async (transaction) => {
      const workspaces = transaction.objectStore("workspaces");
      const notes = transaction.objectStore("notes");
      const mutations = transaction.objectStore("mutations");
      if (!await ownedClaim(transaction, this.userID, mutation.mutationID, ownerID, claimedAt)) return null;
      const local = await requestResult(notes.get([this.userID, mutation.localKey])) as OfflineNoteRecord | undefined;
      const workspace = await requestResult(workspaces.get(this.userID)) as OfflineWorkspaceRecord | undefined;
      const related = await requestResult(
        mutations.index("mutations_by_user_note").getAll([this.userID, mutation.localKey]),
      ) as OfflineMutationRecord[];
      if (!local) {
        const laterDelete = related.find((candidate) => (
          candidate.type === "delete_note" && candidate.sequence > mutation.sequence
        ));
        if (laterDelete) {
          mutations.put({
            ...laterDelete,
            serverNoteID: noteID(current) || laterDelete.serverNoteID,
            baseRevision: current.revision ?? laterDelete.baseRevision,
          } satisfies OfflineMutationRecord);
          mutations.delete([this.userID, mutation.mutationID]);
          committed = true;
          return null;
        }
      }
      if (!local || !workspace) {
        throw new OfflineRepositoryError("storage_unavailable", "Conflict source is not available offline");
      }
      const parent = current.parent_id
        ? await requestResult(notes.index("notes_by_user_server_id").get([this.userID, current.parent_id])) as OfflineNoteRecord | undefined
        : undefined;
      const canonicalParentLocalKey = current.parent_id ? parent?.localKey ?? `server:${current.parent_id}` : "";
      const canonical = serverRecord(this.userID, current, local.localKey, canonicalParentLocalKey, now);
      notes.put(canonical);
      for (const candidate of related) {
        if (candidate.type === "save_note" && candidate.sequence >= mutation.sequence) {
          mutations.delete([this.userID, candidate.mutationID]);
        }
      }

      const uuid = this.randomUUID();
      const conflict: OfflineNoteRecord = {
        ...local,
        localKey: `local:${uuid}`,
        serverNoteID: `offline-${uuid}.md`,
        serverRevision: "",
        baseRevision: "",
        title: `${local.title} (Offline conflict ${new Intl.DateTimeFormat(undefined, {
          dateStyle: "medium",
          timeStyle: "short",
        }).format(new Date(now))})`,
        createdLocally: true,
        state: "conflicted",
        error: "",
        createdAt: now,
        updatedAt: now,
      };
      const conflictMutation: OfflineMutationRecord = {
        mutationID: this.randomUUID(),
        userID: this.userID,
        sequence: workspace.nextSequence,
        type: "save_note",
        localKey: conflict.localKey,
        serverNoteID: conflict.serverNoteID,
        dependencyLocalKeys: conflict.parentLocalKey ? [conflict.parentLocalKey] : [],
        baseRevision: "",
        payload: {
          note_id: conflict.serverNoteID,
          title: conflict.title,
          body_markdown: conflict.bodyMarkdown,
          expected_revision: "",
          page_type: conflict.pageType,
          parent_id: conflict.parentLocalKey,
          pinned: conflict.pinned,
          mcp_excluded: conflict.mcpExcluded,
          board: conflict.board,
        },
        state: "queued",
        attempts: 0,
        nextRetryAt: 0,
        leaseOwner: "",
        leaseExpiresAt: 0,
        createdAt: now,
        lastAttemptAt: 0,
        error: "",
      };
      notes.put(conflict);
      mutations.put(conflictMutation);
      workspaces.put({ ...workspace, nextSequence: workspace.nextSequence + 1 });
      committed = true;
      return conflict;
    });
    if (committed) this.notify();
    return conflict;
  }

  async resolveDeleteConflict(
    mutation: OfflineMutationRecord,
    current: ProfileNote,
    ownerID: string,
    claimedAt: number,
  ): Promise<void> {
    const now = this.now();
    let committed = false;
    await this.database.runTransaction(["notes", "mutations", "sync_leases"], "readwrite", async (transaction) => {
      if (!await ownedClaim(transaction, this.userID, mutation.mutationID, ownerID, claimedAt)) return;
      const notes = transaction.objectStore("notes");
      const parent = current.parent_id
        ? await requestResult(notes.index("notes_by_user_server_id").get([this.userID, current.parent_id])) as OfflineNoteRecord | undefined
        : undefined;
      const parentLocalKey = current.parent_id ? parent?.localKey ?? `server:${current.parent_id}` : "";
      const restored = {
        ...serverRecord(this.userID, current, mutation.localKey, parentLocalKey, now),
        state: "deletion_review",
        error: "Deletion needs review",
      } satisfies OfflineNoteRecord;
      notes.put(restored);
      transaction.objectStore("mutations").delete([this.userID, mutation.mutationID]);
      committed = true;
    });
    if (committed) this.notify();
  }

  async markRetry(
    mutationID: string,
    error: string,
    nextRetryAt: number,
    ownerID: string,
    now: number,
  ): Promise<void> {
    let changed = false;
    await this.database.runTransaction(["notes", "mutations", "sync_leases"], "readwrite", async (transaction) => {
      const mutation = await ownedClaim(transaction, this.userID, mutationID, ownerID, now);
      if (!mutation) return;
      transaction.objectStore("mutations").put({
        ...mutation,
        state: "retryable",
        attempts: mutation.attempts + 1,
        nextRetryAt,
        lastAttemptAt: this.now(),
        leaseOwner: "",
        leaseExpiresAt: 0,
        error,
      } satisfies OfflineMutationRecord);
      if (mutation.localKey) {
        const notes = transaction.objectStore("notes");
        const note = await requestResult(notes.get([this.userID, mutation.localKey])) as OfflineNoteRecord | undefined;
        if (note) notes.put({ ...note, state: "queued", error } satisfies OfflineNoteRecord);
      }
      changed = true;
    });
    if (changed) this.notify();
  }

  async releaseClaim(mutationID: string, ownerID: string, now: number): Promise<void> {
    let changed = false;
    await this.database.runTransaction(["notes", "mutations", "sync_leases"], "readwrite", async (transaction) => {
      const mutation = await ownedClaim(transaction, this.userID, mutationID, ownerID, now);
      if (!mutation) return;
      transaction.objectStore("mutations").put({
        ...mutation,
        state: "queued",
        leaseOwner: "",
        leaseExpiresAt: 0,
      } satisfies OfflineMutationRecord);
      if (mutation.localKey) {
        const notes = transaction.objectStore("notes");
        const note = await requestResult(notes.get([this.userID, mutation.localKey])) as OfflineNoteRecord | undefined;
        if (note?.state === "syncing") notes.put({ ...note, state: "queued" } satisfies OfflineNoteRecord);
      }
      changed = true;
    });
    if (changed) this.notify();
  }

  async markFailed(mutationID: string, error: string, ownerID: string, now: number): Promise<void> {
    let changed = false;
    await this.database.runTransaction(["notes", "mutations", "sync_leases"], "readwrite", async (transaction) => {
      const mutation = await ownedClaim(transaction, this.userID, mutationID, ownerID, now);
      if (!mutation) return;
      transaction.objectStore("mutations").put({
        ...mutation,
        state: "failed",
        attempts: mutation.attempts + 1,
        lastAttemptAt: this.now(),
        leaseOwner: "",
        leaseExpiresAt: 0,
        error,
      } satisfies OfflineMutationRecord);
      if (mutation.localKey) {
        const notes = transaction.objectStore("notes");
        const note = await requestResult(notes.get([this.userID, mutation.localKey])) as OfflineNoteRecord | undefined;
        if (note) notes.put({ ...note, state: "failed", error } satisfies OfflineNoteRecord);
      }
      changed = true;
    });
    if (changed) this.notify();
  }

  async reconcile(
    notesFromServer: ProfileNote[],
    settings: ProfileSettingsResponse,
    ownership?: { ownerID: string; now: number },
  ): Promise<void> {
    const now = this.now();
    await this.database.runTransaction(
      ownership ? ["workspaces", "notes", "sync_leases"] : ["workspaces", "notes"],
      "readwrite",
      async (transaction) => {
      if (ownership && !await ownedSyncLease(transaction, this.userID, ownership.ownerID, ownership.now)) return;
      const notes = transaction.objectStore("notes");
      const workspaces = transaction.objectStore("workspaces");
      const workspace = await requestResult(workspaces.get(this.userID)) as OfflineWorkspaceRecord | undefined;
      if (!workspace) throw new OfflineRepositoryError("storage_unavailable", "Offline Notes workspace is not initialized");
      const existing = await requestResult(
        notes.getAll(IDBKeyRange.bound([this.userID, ""], [this.userID, "\uffff"])),
      ) as OfflineNoteRecord[];
      const existingByServerID = new Map(existing.map((record) => [record.serverNoteID, record]));
      const localKeyByServerID = new Map<string, string>();
      for (const serverNote of notesFromServer) {
        const serverID = noteID(serverNote);
        if (!serverID) continue;
        localKeyByServerID.set(serverID, existingByServerID.get(serverID)?.localKey ?? `server:${serverID}`);
      }
      const seen = new Set<string>();
      for (const serverNote of notesFromServer) {
        const serverID = noteID(serverNote);
        if (!serverID) continue;
        seen.add(serverID);
        const current = existingByServerID.get(serverID);
        if (current && current.state !== "clean") continue;
        const localKey = localKeyByServerID.get(serverID)!;
        const parentLocalKey = localKeyByServerID.get(serverNote.parent_id ?? "") ?? "";
        notes.put(serverRecord(this.userID, serverNote, localKey, parentLocalKey, now));
      }
      for (const current of existing) {
        if (current.state === "clean" && !current.createdLocally && !seen.has(current.serverNoteID)) {
          notes.delete([this.userID, current.localKey]);
        }
      }
      const defaultServerID = typeof settings.settings.kanban_default_board_id === "string"
        ? settings.settings.kanban_default_board_id
        : "";
      workspaces.put({
        ...workspace,
        identity: {
          ...workspace.identity,
          offlineInitializedAt: workspace.identity.offlineInitializedAt || now,
        },
        profileSettingsRevision: settings.revision,
        defaultBoardLocalKey: localKeyByServerID.get(defaultServerID) ?? "",
        lastReconciledAt: now,
      });
    });
    this.notify();
  }

  purge(): Promise<void> {
    return this.database.purgeUser(this.userID);
  }

  private async findRecord(localOrServerID: string): Promise<OfflineNoteRecord | undefined> {
    return await this.database.getNote(this.userID, localOrServerID)
      ?? await this.database.getNoteByServerID(this.userID, localOrServerID);
  }

  private notify(source: "local" | "external" = "local"): void {
    for (const listener of this.listeners) listener(source);
  }

  private async findRecordInStore(
    notes: IDBObjectStore,
    localOrServerID: string,
  ): Promise<OfflineNoteRecord | undefined> {
    return await requestResult(notes.get([this.userID, localOrServerID])) as OfflineNoteRecord | undefined
      ?? await requestResult(
        notes.index("notes_by_user_server_id").get([this.userID, localOrServerID]),
      ) as OfflineNoteRecord | undefined;
  }
}
