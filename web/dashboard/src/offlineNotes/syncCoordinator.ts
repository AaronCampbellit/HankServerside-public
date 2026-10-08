import { ApiError } from "../api/client";
import {
  mergeDefaultKanbanBoard,
  type ProfileSettingsClient,
  type ProfileSettingsResponse,
} from "../api/profileSettings";
import {
  noteID,
  type ProfileNoteSummary,
  ProfileNote,
  type ProfileNotesClient,
  type SaveProfileNoteInput,
} from "../api/profileNotes";
import type { OfflineNotesRepository } from "./repository";
import type { OfflineMutationRecord, OfflineNoteRecord, OfflineSyncSummary } from "./types";

export type NotesSyncDependencies = {
  repository: OfflineNotesRepository;
  notesClient: Pick<ProfileNotesClient, "listNotes" | "fetchNote" | "saveNote" | "deleteNote">;
  settingsClient: Pick<ProfileSettingsClient, "load" | "save">;
  now?: () => number;
  random?: () => number;
  ownerID?: string;
  leaseHeartbeatMs?: number;
};

export type SyncErrorClass = "retryable" | "authentication" | "conflict" | "permanent";

export function classifySyncError(error: unknown): SyncErrorClass {
  if (error instanceof TypeError) return "retryable";
  if (error instanceof DOMException && error.name === "AbortError") return "retryable";
  if (!(error instanceof ApiError)) return "permanent";
  if (error.status === 401 || error.status === 403) return "authentication";
  if (error.status === 409 && error.code === "note_conflict") return "conflict";
  if (error.status === 408 || error.status === 429 || error.status >= 500) return "retryable";
  return "permanent";
}

export function noteConflictCurrent(error: unknown): ProfileNote | null {
  if (!(error instanceof ApiError) || error.status !== 409 || error.code !== "note_conflict") return null;
  if (!error.payload || typeof error.payload !== "object") return null;
  const current = (error.payload as { current?: unknown }).current;
  return current && typeof current === "object" ? current as ProfileNote : null;
}

function equivalent(payload: SaveProfileNoteInput, current: ProfileNote): boolean {
  return payload.title === (current.title ?? "")
    && payload.body_markdown === (current.body_markdown ?? current.content ?? "")
    && (payload.page_type || "text") === (current.page_type ?? "text")
    && (payload.parent_id || "") === (current.parent_id ?? "")
    && Boolean(payload.pinned) === Boolean(current.pinned)
    && Boolean(payload.mcp_excluded) === Boolean(current.mcp_excluded)
    && JSON.stringify(payload.board ?? null) === JSON.stringify(current.board ?? null);
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "Synchronization failed";
}

export class NotesSyncCoordinator {
  private readonly repository: OfflineNotesRepository;
  private readonly notesClient: NotesSyncDependencies["notesClient"];
  private readonly settingsClient: NotesSyncDependencies["settingsClient"];
  private readonly now: () => number;
  private readonly random: () => number;
  private readonly ownerID: string;
  private readonly leaseTTL = 15_000;
  private readonly leaseHeartbeatMs: number;
  private readonly listeners = new Set<(summary: OfflineSyncSummary) => void>();
  private authenticationRequired = false;
  private lastSyncedAt = 0;
  private started = false;
  private queuedWake = false;
  private lifecycleRun: Promise<void> | null = null;
  private runTail: Promise<void> = Promise.resolve();
  private repositoryUnsubscribe: (() => void) | null = null;
  private onlineListener: (() => void) | null = null;
  private channel: BroadcastChannel | null = null;
  private retryTimer: ReturnType<typeof setTimeout> | null = null;
  private generation = 0;
  private lastSummary: OfflineSyncSummary = {
    pending: 0,
    syncing: 0,
    failed: 0,
    conflicted: 0,
    authenticationRequired: false,
    lastSyncedAt: 0,
  };

  constructor(dependencies: NotesSyncDependencies) {
    this.repository = dependencies.repository;
    this.notesClient = dependencies.notesClient;
    this.settingsClient = dependencies.settingsClient;
    this.now = dependencies.now ?? Date.now;
    this.random = dependencies.random ?? Math.random;
    this.ownerID = dependencies.ownerID ?? crypto.randomUUID();
    this.leaseHeartbeatMs = dependencies.leaseHeartbeatMs ?? 5_000;
  }

  async runOnce(options: { forceRetry?: boolean } = {}): Promise<OfflineSyncSummary> {
    const runGeneration = this.generation;
    const claimed = await this.repository.claimSyncLease(this.ownerID, this.now(), this.leaseTTL);
    if (!claimed) return this.summary();
    if (runGeneration !== this.generation) {
      await this.repository.releaseSyncLease(this.ownerID);
      return this.summary();
    }
    let leaseHealthy = true;
    const heartbeat = setInterval(() => {
      void this.repository.renewSyncLease(this.ownerID, this.now(), this.leaseTTL)
        .then((renewed) => { leaseHealthy = renewed; })
        .catch(() => { leaseHealthy = false; });
    }, this.leaseHeartbeatMs);
    this.authenticationRequired = false;
    try {
      const processed = new Set<string>();
      let mutations = await this.repository.pendingMutations();
      mutations = [...mutations].sort((left, right) => left.sequence - right.sequence);
      for (const mutation of mutations) {
        if (!leaseHealthy) break;
        if (mutation.state === "failed" || mutation.state === "conflicted") continue;
        if (!options.forceRetry && mutation.nextRetryAt > this.now()) continue;
        if (mutation.dependencyLocalKeys.some((dependency) => !processed.has(dependency))) {
          const dependencyPending = mutations.some(
            (candidate) => mutation.dependencyLocalKeys.includes(candidate.localKey)
              && candidate.mutationID !== mutation.mutationID,
          );
          if (dependencyPending) continue;
        }
        const renewed = await this.repository.renewSyncLease(this.ownerID, this.now(), this.leaseTTL);
        if (!renewed) break;
        const claimedMutation = await this.repository.claimMutation(
          mutation.mutationID,
          this.ownerID,
          this.now(),
          this.leaseTTL,
        );
        if (!claimedMutation) continue;
        this.publish(await this.summary());
        try {
          await this.replay(claimedMutation, runGeneration);
          if (runGeneration !== this.generation) break;
          processed.add(mutation.localKey);
          this.lastSyncedAt = this.now();
        } catch (error) {
          if (runGeneration !== this.generation) break;
          const current = noteConflictCurrent(error);
          if (current && claimedMutation.type === "save_note") {
            await this.repository.resolveSaveConflict(claimedMutation, current, this.ownerID, this.now());
            processed.add(claimedMutation.localKey);
            continue;
          }
          if (current && claimedMutation.type === "delete_note") {
            await this.repository.resolveDeleteConflict(claimedMutation, current, this.ownerID, this.now());
            processed.add(claimedMutation.localKey);
            continue;
          }
          const classification = classifySyncError(error);
          if (classification === "authentication") {
            await this.repository.releaseClaim(claimedMutation.mutationID, this.ownerID, this.now());
            this.authenticationRequired = true;
            break;
          }
          if (classification === "retryable") {
            const delay = Math.min(300_000, 1_000 * 2 ** claimedMutation.attempts);
            const jitter = Math.floor(delay * 0.2 * this.random());
            await this.repository.markRetry(
              claimedMutation.mutationID,
              errorMessage(error),
              this.now() + delay + jitter,
              this.ownerID,
              this.now(),
            );
          } else {
            await this.repository.markFailed(
              claimedMutation.mutationID,
              errorMessage(error),
              this.ownerID,
              this.now(),
            );
          }
        }
      }
      const remaining = await this.repository.pendingMutations();
      if (remaining.length === 0 && !this.authenticationRequired && runGeneration === this.generation) {
        await this.reconcile(runGeneration);
      }
      const summary = await this.summary();
      this.publish(summary);
      this.scheduleRetry();
      return summary;
    } finally {
      clearInterval(heartbeat);
      await this.repository.releaseSyncLease(this.ownerID);
    }
  }

  start(): void {
    if (this.started) return;
    this.started = true;
    this.repositoryUnsubscribe = this.repository.subscribe((source) => {
      if (source !== "external") this.channel?.postMessage({ type: "mutated", userID: this.repository.userID });
      this.wake();
    });
    if (typeof window !== "undefined") {
      this.onlineListener = () => this.wake();
      window.addEventListener("online", this.onlineListener);
      if (typeof BroadcastChannel !== "undefined") {
        this.channel = new BroadcastChannel("hank-offline-notes");
        this.channel.onmessage = (event: MessageEvent<{ userID?: string }>) => {
          if (event.data?.userID !== this.repository.userID) return;
          this.repository.refreshFromExternal();
          void this.summary().then((summary) => this.publish(summary));
        };
      }
    }
    this.wake();
  }

  stop(): void {
    this.generation += 1;
    this.started = false;
    this.queuedWake = false;
    this.repositoryUnsubscribe?.();
    this.repositoryUnsubscribe = null;
    if (this.onlineListener && typeof window !== "undefined") {
      window.removeEventListener("online", this.onlineListener);
    }
    this.onlineListener = null;
    this.channel?.close();
    this.channel = null;
    if (this.retryTimer) clearTimeout(this.retryTimer);
    this.retryTimer = null;
  }

  syncNow(): Promise<OfflineSyncSummary> {
    return this.enqueueRun({ forceRetry: true });
  }

  subscribe(listener: (summary: OfflineSyncSummary) => void): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  private async replay(mutation: OfflineMutationRecord, runGeneration: number): Promise<void> {
    if (mutation.type === "save_note") {
      await this.replaySave(mutation, runGeneration);
      return;
    }
    if (mutation.type === "delete_note") {
      try {
        await this.notesClient.deleteNote(mutation.serverNoteID, mutation.baseRevision);
      } catch (error) {
        if (!(error instanceof ApiError && error.status === 404)) throw error;
      }
      if (runGeneration !== this.generation) return;
      await this.repository.acknowledgeDelete(mutation, this.ownerID, this.now());
      return;
    }
    await this.replayDefaultBoard(mutation, runGeneration);
  }

  private async replaySave(mutation: OfflineMutationRecord, runGeneration: number): Promise<void> {
    let activeMutation = mutation;
    let record = await this.repository.getNoteRecord(mutation.localKey);
    if (!record || !activeMutation.payload || !("body_markdown" in activeMutation.payload)) {
      await this.repository.acknowledgeDelete(mutation, this.ownerID, this.now());
      return;
    }
    let payload = { ...activeMutation.payload } as SaveProfileNoteInput;
    if (payload.parent_id) {
      const parent = await this.repository.getNoteRecord(payload.parent_id);
      if (parent) payload.parent_id = parent.serverNoteID;
    }
    payload.note_id = record.serverNoteID;
    payload.expected_revision = activeMutation.baseRevision;
    if (record.createdLocally) {
      try {
        const current = await this.notesClient.fetchNote(record.serverNoteID);
        if (runGeneration !== this.generation) return;
        if (equivalent(payload, current)) {
          await this.repository.acknowledgeSave(mutation, {
            note_id: record.serverNoteID,
            revision: current.revision ?? "",
            updated_at: current.updated_at,
          }, this.ownerID, this.now());
          return;
        }
        const reassigned = await this.repository.reassignCreatedNote(activeMutation, this.ownerID, this.now());
        if (!reassigned) return;
        record = reassigned.note;
        activeMutation = reassigned.mutation;
        payload.note_id = record.serverNoteID;
      } catch (error) {
        if (!(error instanceof ApiError && error.status === 404)) throw error;
      }
    }
    payload.note_id = record.serverNoteID;
    payload.expected_revision = activeMutation.baseRevision;
    const response = await this.notesClient.saveNote(payload);
    if (runGeneration !== this.generation) return;
    await this.repository.acknowledgeSave(activeMutation, response, this.ownerID, this.now());
  }

  private async replayDefaultBoard(mutation: OfflineMutationRecord, runGeneration: number): Promise<void> {
    const note = mutation.localKey ? await this.repository.getNoteRecord(mutation.localKey) : undefined;
    if (mutation.localKey && !note) {
      await this.repository.acknowledgeDelete(mutation, this.ownerID, this.now());
      return;
    }
    let current = await this.settingsClient.load();
    if (runGeneration !== this.generation) return;
    for (let attempt = 0; attempt < 2; attempt += 1) {
      try {
        const response = await this.settingsClient.save(
          current.revision,
          mergeDefaultKanbanBoard(current.settings, note?.serverNoteID ?? ""),
        );
        if (runGeneration !== this.generation) return;
        await this.acknowledgeSettings(mutation, response);
        return;
      } catch (error) {
        if (!(attempt === 0 && error instanceof ApiError && error.status === 409)) throw error;
        current = await this.settingsClient.load();
        if (runGeneration !== this.generation) return;
      }
    }
  }

  private async reconcile(runGeneration: number): Promise<void> {
    try {
      const list = await this.notesClient.listNotes();
      const summaries = list.notes as ProfileNoteSummary[];
      const fullNotes: ProfileNote[] = [];
      for (let index = 0; index < summaries.length; index += 4) {
        const batch = summaries.slice(index, index + 4);
        const fetched = await Promise.all(batch.map((note) => this.notesClient.fetchNote(noteID(note))));
        fullNotes.push(...fetched);
      }
      const settings = await this.settingsClient.load();
      if (runGeneration !== this.generation) return;
      await this.repository.reconcile(fullNotes, settings, { ownerID: this.ownerID, now: this.now() });
    } catch (error) {
      if (classifySyncError(error) === "authentication") this.authenticationRequired = true;
    }
  }

  private async acknowledgeSettings(
    mutation: OfflineMutationRecord,
    _response: ProfileSettingsResponse,
  ): Promise<void> {
    await this.repository.acknowledgeDelete(mutation, this.ownerID, this.now());
  }

  private async summary(): Promise<OfflineSyncSummary> {
    const [mutations, notes] = await Promise.all([
      this.repository.pendingMutations(),
      this.repository.listNoteRecords(),
    ]);
    return {
      pending: mutations.filter((mutation) => mutation.state === "queued" || mutation.state === "retryable").length,
      syncing: mutations.filter((mutation) => mutation.state === "claimed").length,
      failed: mutations.filter((mutation) => mutation.state === "failed").length,
      conflicted: notes.filter((note) => note.state === "conflicted").length,
      authenticationRequired: this.authenticationRequired,
      lastSyncedAt: this.lastSyncedAt,
    };
  }

  private publish(summary: OfflineSyncSummary): void {
    this.lastSummary = summary;
    for (const listener of this.listeners) listener(summary);
  }

  private wake(): void {
    if (!this.started) return;
    if (this.lifecycleRun) {
      this.queuedWake = true;
      return;
    }
    this.lifecycleRun = this.enqueueRun()
      .then(() => undefined)
      .finally(() => {
        this.lifecycleRun = null;
        if (this.queuedWake) {
          this.queuedWake = false;
          this.wake();
        }
      });
  }

  private enqueueRun(options: { forceRetry?: boolean } = {}): Promise<OfflineSyncSummary> {
    const requestedGeneration = this.generation;
    const scheduled = this.runTail.then(() => (
      requestedGeneration === this.generation ? this.runOnce(options) : this.summary()
    )).catch(() => {
      const fallback = this.backgroundFailureSummary();
      this.publish(fallback);
      this.scheduleBackgroundRecovery();
      return fallback;
    });
    this.runTail = scheduled.then(() => undefined, () => undefined);
    return scheduled;
  }

  private scheduleRetry(): void {
    if (!this.started) return;
    if (this.retryTimer) clearTimeout(this.retryTimer);
    this.retryTimer = null;
    void this.repository.pendingMutations().then((mutations) => {
      if (!this.started) return;
      const nextRetryAt = mutations
        .filter((mutation) => mutation.state === "retryable" && mutation.nextRetryAt > this.now())
        .reduce((minimum, mutation) => Math.min(minimum, mutation.nextRetryAt), Number.POSITIVE_INFINITY);
      if (!Number.isFinite(nextRetryAt)) return;
      this.retryTimer = setTimeout(() => this.wake(), Math.max(0, nextRetryAt - this.now()));
    }).catch(() => {
      this.publish(this.backgroundFailureSummary());
      this.scheduleBackgroundRecovery();
    });
  }

  private backgroundFailureSummary(): OfflineSyncSummary {
    return {
      ...this.lastSummary,
      syncing: 0,
      failed: Math.max(1, this.lastSummary.failed),
    };
  }

  private scheduleBackgroundRecovery(): void {
    if (!this.started) return;
    if (this.retryTimer) clearTimeout(this.retryTimer);
    this.retryTimer = setTimeout(() => this.wake(), 5_000);
  }
}
