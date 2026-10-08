// @vitest-environment node
import "fake-indexeddb/auto";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api/client";
import type { ProfileSettingsResponse } from "../api/profileSettings";
import type { ProfileNote, SaveProfileNoteInput, SaveProfileNoteResponse } from "../api/profileNotes";
import { openOfflineNotesDatabase, type OfflineNotesDatabase } from "./database";
import { OfflineNotesRepository } from "./repository";
import { classifySyncError, NotesSyncCoordinator } from "./syncCoordinator";
import type { OfflineIdentity, OfflineNoteRecord, OfflineWorkspaceRecord } from "./types";

const databases: OfflineNotesDatabase[] = [];
const names: string[] = [];

afterEach(async () => {
  vi.useRealTimers();
  for (const database of databases.splice(0)) database.close();
  for (const name of names.splice(0)) {
    await new Promise<void>((resolve, reject) => {
      const request = indexedDB.deleteDatabase(name);
      request.onsuccess = () => resolve();
      request.onerror = () => reject(request.error);
    });
  }
});

function saveInput(title: string, noteID = "", parentID = "", board: OfflineNoteRecord["board"] = null): SaveProfileNoteInput {
  return {
    note_id: noteID,
    title,
    body_markdown: board ? "" : `${title} body`,
    expected_revision: noteID ? "rev-1" : "",
    page_type: board ? "kanban" : "text",
    parent_id: parentID,
    pinned: false,
    mcp_excluded: false,
    board,
  };
}

function serverNote(title: string, revision: string, board: OfflineNoteRecord["board"] = null): ProfileNote {
  return {
    note_id: "work.md",
    title,
    body_markdown: board ? "" : `${title} body`,
    revision,
    page_type: board ? "kanban" : "text",
    parent_id: "",
    pinned: false,
    mcp_excluded: false,
    board,
    attachments: [],
    updated_at: "2026-08-18T00:00:00Z",
  };
}

async function setup(seed?: OfflineNoteRecord) {
  const name = `offline-sync-${crypto.randomUUID()}`;
  names.push(name);
  const database = await openOfflineNotesDatabase(name);
  databases.push(database);
  const identity: OfflineIdentity = {
    userID: "usr_a",
    email: "a@example.test",
    displayName: "A",
    role: "member",
    permissions: { is_admin: false, can_use_notes: true },
    lastAuthenticatedAt: 1,
    offlineInitializedAt: 1,
  };
  const workspace: OfflineWorkspaceRecord = {
    userID: "usr_a",
    identity,
    profileSettingsRevision: 2,
    defaultBoardLocalKey: "",
    lastReconciledAt: 0,
    nextSequence: 1,
  };
  await database.putWorkspace(workspace);
  if (seed) await database.putNote(seed);
  const uuids = ["local-1", "mutation-1", "local-2", "mutation-2", "conflict", "conflict-mutation"];
  return {
    database,
    repository: new OfflineNotesRepository("usr_a", database, () => 1_000, () => uuids.shift() ?? crypto.randomUUID()),
  };
}

class FakeNotesClient {
  saved: SaveProfileNoteInput[] = [];
  deleted: Array<{ id: string; revision: string }> = [];
  saveError: unknown = null;
  deleteError: unknown = null;
  deleteStarted: (() => void) | null = null;
  deleteGate: Promise<void> | null = null;
  fetched = new Map<string, ProfileNote>();
  saveStarted: (() => void) | null = null;
  saveGate: Promise<void> | null = null;

  async fetchNote(id: string): Promise<ProfileNote> {
    const note = this.fetched.get(id);
    if (note) return note;
    throw new ApiError(404, "not_found", "not found", { error: "not_found" });
  }

  async saveNote(input: SaveProfileNoteInput): Promise<SaveProfileNoteResponse> {
    this.saved.push(input);
    this.saveStarted?.();
    if (this.saveGate) await this.saveGate;
    if (this.saveError) throw this.saveError;
    const response = { note_id: input.note_id, revision: `synced-${this.saved.length}`, updated_at: "2026-08-18T00:00:00Z" };
    this.fetched.set(input.note_id, {
      note_id: input.note_id,
      title: input.title,
      body_markdown: input.body_markdown,
      revision: response.revision,
      page_type: input.page_type,
      parent_id: input.parent_id,
      pinned: input.pinned,
      mcp_excluded: input.mcp_excluded,
      board: input.board,
      attachments: [],
      updated_at: response.updated_at,
    });
    return response;
  }

  async deleteNote(id: string, revision: string): Promise<{ ok: boolean }> {
    this.deleted.push({ id, revision });
    this.deleteStarted?.();
    if (this.deleteGate) await this.deleteGate;
    if (this.deleteError) throw this.deleteError;
    this.fetched.delete(id);
    return { ok: true };
  }

  async listNotes() {
    return { notes: [...this.fetched.values()] };
  }
}

class FakeSettingsClient {
  saved: Array<{ revision: number; settings: Record<string, unknown> }> = [];

  async load(): Promise<ProfileSettingsResponse> {
    return { revision: 2, settings: {} };
  }

  async save(revision: number, settings: Record<string, unknown>): Promise<ProfileSettingsResponse> {
    this.saved.push({ revision, settings });
    return { revision: revision + 1, settings };
  }
}

describe("NotesSyncCoordinator", () => {
  it("replays a locally-created parent before its child", async () => {
    const { repository } = await setup();
    const parent = await repository.saveNote(saveInput("Parent"));
    await repository.saveNote(saveInput("Child", "", parent.localKey));
    const notesClient = new FakeNotesClient();
    const coordinator = new NotesSyncCoordinator({ repository, notesClient, settingsClient: new FakeSettingsClient() });

    const summary = await coordinator.runOnce();

    expect(notesClient.saved.map((input) => input.title)).toEqual(["Parent", "Child"]);
    expect(notesClient.saved[1].parent_id).toBe("offline-local-1.md");
    expect(await repository.pendingMutations()).toEqual([]);
    expect(summary.pending).toBe(0);
  });

  it("preserves a complete Kanban conflict copy", async () => {
    const serverBoard = { columns: [{ id: "todo", title: "Todo", cards: [{ id: "server", title: "Server card" }] }] };
    const localBoard = { columns: [{ id: "todo", title: "Todo", cards: [{ id: "local", title: "Offline card" }] }] };
    const seed: OfflineNoteRecord = {
      userID: "usr_a", localKey: "server:work.md", serverNoteID: "work.md",
      serverRevision: "rev-1", baseRevision: "rev-1", title: "Work", bodyMarkdown: "",
      pageType: "kanban", parentLocalKey: "", pinned: false, mcpExcluded: false,
      board: serverBoard, attachments: [], createdLocally: false, state: "clean", error: "",
      createdAt: 1, updatedAt: 1,
    };
    const { repository } = await setup(seed);
    await repository.saveNote(saveInput("Work", "work.md", "", localBoard));
    const notesClient = new FakeNotesClient();
    notesClient.saveError = new ApiError(409, "note_conflict", "note conflict", {
      error: "note_conflict",
      current: serverNote("Work", "rev-2", serverBoard),
    });
    const coordinator = new NotesSyncCoordinator({ repository, notesClient, settingsClient: new FakeSettingsClient() });

    await coordinator.runOnce();

    const records = await repository.listNoteRecords();
    expect(records.find((record) => record.serverNoteID === "work.md")?.board).toEqual(serverBoard);
    expect(records.find((record) => record.title.includes("Offline conflict"))?.board).toEqual(localBoard);
    expect(await repository.pendingMutations()).toHaveLength(1);
  });

  it("restores the current server note when a conditional delete conflicts", async () => {
    const seed: OfflineNoteRecord = {
      userID: "usr_a", localKey: "server:work.md", serverNoteID: "work.md",
      serverRevision: "rev-1", baseRevision: "rev-1", title: "Work", bodyMarkdown: "old",
      pageType: "text", parentLocalKey: "", pinned: false, mcpExcluded: false,
      board: null, attachments: [], createdLocally: false, state: "clean", error: "",
      createdAt: 1, updatedAt: 1,
    };
    const { repository } = await setup(seed);
    await repository.deleteNote("work.md");
    const notesClient = new FakeNotesClient();
    notesClient.deleteError = new ApiError(409, "note_conflict", "note conflict", {
      error: "note_conflict",
      current: serverNote("Work restored", "rev-2"),
    });
    const coordinator = new NotesSyncCoordinator({ repository, notesClient, settingsClient: new FakeSettingsClient() });

    await coordinator.runOnce();

    expect(notesClient.deleted).toEqual([{ id: "work.md", revision: "rev-1" }]);
    const records = await repository.listNoteRecords();
    expect(records).toHaveLength(1);
    expect(records[0]).toMatchObject({ title: "Work restored", state: "deletion_review", serverRevision: "rev-2" });
    expect(await repository.pendingMutations()).toEqual([]);
  });

  it("does not restore a conflicted delete after logout purges the workspace", async () => {
    const seed: OfflineNoteRecord = {
      userID: "usr_a", localKey: "server:work.md", serverNoteID: "work.md",
      serverRevision: "rev-1", baseRevision: "rev-1", title: "Work", bodyMarkdown: "old",
      pageType: "text", parentLocalKey: "", pinned: false, mcpExcluded: false,
      board: null, attachments: [], createdLocally: false, state: "clean", error: "",
      createdAt: 1, updatedAt: 1,
    };
    const { repository } = await setup(seed);
    await repository.deleteNote("work.md");
    const notesClient = new FakeNotesClient();
    let releaseDelete!: () => void;
    notesClient.deleteGate = new Promise((resolve) => { releaseDelete = resolve; });
    let markStarted!: () => void;
    const deleteStarted = new Promise<void>((resolve) => { markStarted = resolve; });
    notesClient.deleteStarted = markStarted;
    notesClient.deleteError = new ApiError(409, "note_conflict", "note conflict", {
      error: "note_conflict",
      current: serverNote("Restored after logout", "rev-2"),
    });
    const coordinator = new NotesSyncCoordinator({ repository, notesClient, settingsClient: new FakeSettingsClient() });

    const run = coordinator.runOnce();
    await deleteStarted;
    coordinator.stop();
    await repository.purge();
    releaseDelete();
    await run;

    expect(await repository.listNoteRecords()).toEqual([]);
  });

  it("moves a local create to a fresh stable ID when its first ID already differs on the server", async () => {
    const { repository } = await setup();
    const created = await repository.saveNote(saveInput("Offline draft"));
    const notesClient = new FakeNotesClient();
    notesClient.fetched.set(created.noteID, {
      ...serverNote("Different server note", "rev-existing"),
      note_id: created.noteID,
    });
    const coordinator = new NotesSyncCoordinator({ repository, notesClient, settingsClient: new FakeSettingsClient() });

    await coordinator.runOnce();

    expect(notesClient.saved).toHaveLength(1);
    expect(notesClient.saved[0].note_id).not.toBe(created.noteID);
    expect(notesClient.saved[0].title).toBe("Offline draft");
    expect((await repository.listNoteRecords())[0]).toMatchObject({
      title: "Offline draft",
      state: "clean",
      createdLocally: false,
    });
  });

  it("replays default-board selection after creating its board", async () => {
    const { repository } = await setup();
    const board = await repository.saveNote(saveInput("Work board", "", "", { columns: [] }));
    await repository.setDefaultBoard(board.localKey);
    const notesClient = new FakeNotesClient();
    const settingsClient = new FakeSettingsClient();
    const coordinator = new NotesSyncCoordinator({ repository, notesClient, settingsClient });

    await coordinator.runOnce();

    expect(settingsClient.saved).toEqual([{
      revision: 2,
      settings: { kanban_default_board_id: "offline-local-1.md" },
    }]);
    expect(await repository.pendingMutations()).toEqual([]);
  });

  it("replays clearing the default board without requiring a note", async () => {
    const { repository } = await setup();
    await repository.setDefaultBoard("");
    const settingsClient = new FakeSettingsClient();
    const coordinator = new NotesSyncCoordinator({ repository, notesClient: new FakeNotesClient(), settingsClient });

    await coordinator.runOnce();

    expect(settingsClient.saved).toEqual([{ revision: 2, settings: {} }]);
    expect(await repository.pendingMutations()).toEqual([]);
  });

  it("reloads settings and retries a default-board revision conflict once", async () => {
    const { repository } = await setup();
    await repository.setDefaultBoard("");
    const load = vi.fn()
      .mockResolvedValueOnce({ revision: 2, settings: { theme: "dark" } })
      .mockResolvedValueOnce({ revision: 3, settings: { theme: "light" } })
      .mockResolvedValue({ revision: 4, settings: { theme: "light" } });
    const save = vi.fn()
      .mockRejectedValueOnce(new ApiError(409, "settings_conflict", "changed", {}))
      .mockResolvedValueOnce({ revision: 4, settings: { theme: "light" } });
    const coordinator = new NotesSyncCoordinator({
      repository,
      notesClient: new FakeNotesClient(),
      settingsClient: { load, save },
    });

    await coordinator.runOnce();

    expect(load).toHaveBeenCalledTimes(3);
    expect(save).toHaveBeenNthCalledWith(1, 2, { theme: "dark" });
    expect(save).toHaveBeenNthCalledWith(2, 3, { theme: "light" });
    expect(await repository.pendingMutations()).toEqual([]);
  });

  it("allows only one tab to replay the user outbox", async () => {
    const { repository } = await setup();
    await repository.saveNote(saveInput("One writer"));
    const notesClient = new FakeNotesClient();
    let releaseSave!: () => void;
    notesClient.saveGate = new Promise((resolve) => { releaseSave = resolve; });
    let started!: () => void;
    const saveStarted = new Promise<void>((resolve) => { started = resolve; });
    notesClient.saveStarted = started;
    const settingsClient = new FakeSettingsClient();
    const first = new NotesSyncCoordinator({ repository, notesClient, settingsClient, ownerID: "tab-a" });
    const second = new NotesSyncCoordinator({ repository, notesClient, settingsClient, ownerID: "tab-b" });

    const firstRun = first.runOnce();
    await saveStarted;
    const secondSummary = await second.runOnce();
    releaseSave();
    await firstRun;

    expect(notesClient.saved).toHaveLength(1);
    expect(secondSummary.pending + secondSummary.syncing).toBe(1);
  });

  it("keeps an edit made while an earlier save is in flight and rebases its successor", async () => {
    const seed: OfflineNoteRecord = {
      userID: "usr_a", localKey: "server:work.md", serverNoteID: "work.md",
      serverRevision: "rev-1", baseRevision: "rev-1", title: "Original", bodyMarkdown: "Original body",
      pageType: "text", parentLocalKey: "", pinned: false, mcpExcluded: false,
      board: null, attachments: [], createdLocally: false, state: "clean", error: "",
      createdAt: 1, updatedAt: 1,
    };
    const { repository } = await setup(seed);
    await repository.saveNote(saveInput("First", "work.md"));
    const notesClient = new FakeNotesClient();
    let releaseSave!: () => void;
    notesClient.saveGate = new Promise((resolve) => { releaseSave = resolve; });
    let markStarted!: () => void;
    const saveStarted = new Promise<void>((resolve) => { markStarted = resolve; });
    notesClient.saveStarted = markStarted;
    const coordinator = new NotesSyncCoordinator({ repository, notesClient, settingsClient: new FakeSettingsClient() });

    const firstRun = coordinator.runOnce();
    await saveStarted;
    await repository.saveNote(saveInput("Second", "work.md"));
    releaseSave();
    await firstRun;

    expect(await repository.getNoteRecord("work.md")).toMatchObject({
      title: "Second",
      bodyMarkdown: "Second body",
      serverRevision: "synced-1",
      state: "queued",
    });
    expect(await repository.pendingMutations()).toEqual([
      expect.objectContaining({
        type: "save_note",
        baseRevision: "synced-1",
        payload: expect.objectContaining({ title: "Second", expected_revision: "synced-1" }),
      }),
    ]);

    notesClient.saveGate = null;
    await coordinator.runOnce();
    expect(notesClient.saved.map((input) => input.title)).toEqual(["First", "Second"]);
    expect(await repository.getNoteRecord("work.md")).toMatchObject({ title: "Second", state: "clean" });
  });

  it("preserves the latest edit as one conflict copy when a conflict arrives during a successor save", async () => {
    const seed: OfflineNoteRecord = {
      userID: "usr_a", localKey: "server:work.md", serverNoteID: "work.md",
      serverRevision: "rev-1", baseRevision: "rev-1", title: "Original", bodyMarkdown: "Original body",
      pageType: "text", parentLocalKey: "", pinned: false, mcpExcluded: false,
      board: null, attachments: [], createdLocally: false, state: "clean", error: "",
      createdAt: 1, updatedAt: 1,
    };
    const { database, repository } = await setup(seed);
    await database.putNote({
      ...seed,
      localKey: "server:folder.md",
      serverNoteID: "folder.md",
      title: "Folder",
      pageType: "notebook",
    });
    await repository.saveNote(saveInput("First", "work.md"));
    const notesClient = new FakeNotesClient();
    let releaseSave!: () => void;
    notesClient.saveGate = new Promise((resolve) => { releaseSave = resolve; });
    let markStarted!: () => void;
    const saveStarted = new Promise<void>((resolve) => { markStarted = resolve; });
    notesClient.saveStarted = markStarted;
    notesClient.saveError = new ApiError(409, "note_conflict", "changed", {
      error: "note_conflict",
      current: { ...serverNote("Server", "rev-2"), parent_id: "folder.md" },
    });
    const coordinator = new NotesSyncCoordinator({ repository, notesClient, settingsClient: new FakeSettingsClient() });

    const run = coordinator.runOnce();
    await saveStarted;
    await repository.saveNote(saveInput("Latest", "work.md"));
    releaseSave();
    await run;

    const records = await repository.listNoteRecords();
    expect(records.find((record) => record.serverNoteID === "work.md")).toMatchObject({
      title: "Server",
      parentLocalKey: "server:folder.md",
      state: "clean",
    });
    expect(records.find((record) => record.title.includes("Offline conflict"))).toMatchObject({
      bodyMarkdown: "Latest body",
      state: "conflicted",
    });
    const pending = await repository.pendingMutations();
    expect(pending).toHaveLength(1);
    expect(pending[0].payload).toEqual(expect.objectContaining({ body_markdown: "Latest body" }));
  });

  it("does not acknowledge an ambiguous create when its queued parent differs", async () => {
    const parent: OfflineNoteRecord = {
      userID: "usr_a", localKey: "server:folder.md", serverNoteID: "folder.md",
      serverRevision: "parent-rev", baseRevision: "parent-rev", title: "Folder", bodyMarkdown: "",
      pageType: "notebook", parentLocalKey: "", pinned: false, mcpExcluded: false,
      board: null, attachments: [], createdLocally: false, state: "clean", error: "",
      createdAt: 1, updatedAt: 1,
    };
    const { repository } = await setup(parent);
    const created = await repository.saveNote(saveInput("Draft"));
    await repository.saveNote(saveInput("Draft", created.localKey, parent.localKey));
    const notesClient = new FakeNotesClient();
    notesClient.fetched.set(created.noteID, {
      ...serverNote("Draft", "ambiguous-rev"),
      note_id: created.noteID,
      parent_id: "",
    });
    const coordinator = new NotesSyncCoordinator({ repository, notesClient, settingsClient: new FakeSettingsClient() });

    await coordinator.runOnce();

    expect(notesClient.saved).toHaveLength(1);
    expect(notesClient.saved[0]).toMatchObject({ parent_id: "folder.md", expected_revision: "" });
  });

  it("queues a server delete when a local create is deleted while its save is in flight", async () => {
    const { repository } = await setup();
    const created = await repository.saveNote(saveInput("Temporary"));
    const notesClient = new FakeNotesClient();
    let releaseSave!: () => void;
    notesClient.saveGate = new Promise((resolve) => { releaseSave = resolve; });
    let markStarted!: () => void;
    const saveStarted = new Promise<void>((resolve) => { markStarted = resolve; });
    notesClient.saveStarted = markStarted;
    const coordinator = new NotesSyncCoordinator({ repository, notesClient, settingsClient: new FakeSettingsClient() });

    const createRun = coordinator.runOnce();
    await saveStarted;
    await repository.deleteNote(created.localKey);
    releaseSave();
    await createRun;

    expect(await repository.listNoteRecords()).toEqual([]);
    expect(await repository.pendingMutations()).toEqual([
      expect.objectContaining({
        type: "delete_note",
        serverNoteID: created.noteID,
        baseRevision: "synced-1",
      }),
    ]);

    notesClient.saveGate = null;
    await coordinator.runOnce();
    expect(notesClient.deleted).toEqual([{ id: created.noteID, revision: "synced-1" }]);
    expect(await repository.pendingMutations()).toEqual([]);
  });

  it("renews the lease while a network request is still running", async () => {
    const { repository } = await setup();
    await repository.saveNote(saveInput("Slow save"));
    const notesClient = new FakeNotesClient();
    let releaseSave!: () => void;
    notesClient.saveGate = new Promise((resolve) => { releaseSave = resolve; });
    let markStarted!: () => void;
    const saveStarted = new Promise<void>((resolve) => { markStarted = resolve; });
    notesClient.saveStarted = markStarted;
    let now = 1_000;
    const settingsClient = new FakeSettingsClient();
    const first = new NotesSyncCoordinator({
      repository,
      notesClient,
      settingsClient,
      ownerID: "tab-a",
      now: () => now,
      leaseHeartbeatMs: 1,
    });
    const second = new NotesSyncCoordinator({ repository, notesClient, settingsClient, ownerID: "tab-b", now: () => now });

    const firstRun = first.runOnce();
    await saveStarted;
    now = 7_000;
    await new Promise((resolve) => setTimeout(resolve, 10));
    now = 17_000;
    const secondRun = second.runOnce();
    releaseSave();
    await Promise.all([firstRun, secondRun]);

    expect(notesClient.saved).toHaveLength(1);
  });

  it("publishes a global syncing state while a save request is in flight", async () => {
    const { repository } = await setup();
    const saved = await repository.saveNote(saveInput("Visible sync"));
    const notesClient = new FakeNotesClient();
    let releaseSave!: () => void;
    notesClient.saveGate = new Promise((resolve) => { releaseSave = resolve; });
    const coordinator = new NotesSyncCoordinator({ repository, notesClient, settingsClient: new FakeSettingsClient() });
    let markSyncing!: () => void;
    const syncingPublished = new Promise<void>((resolve) => { markSyncing = resolve; });
    coordinator.subscribe((summary) => {
      if (summary.syncing === 1) markSyncing();
    });

    const run = coordinator.runOnce();
    await syncingPublished;
    expect((await repository.getNoteRecord(saved.localKey))?.state).toBe("syncing");
    releaseSave();
    await run;
  });

  it("serializes repeated Sync now requests behind the active replay", async () => {
    const { repository } = await setup();
    await repository.saveNote(saveInput("One request"));
    const notesClient = new FakeNotesClient();
    let releaseSave!: () => void;
    notesClient.saveGate = new Promise((resolve) => { releaseSave = resolve; });
    let markStarted!: () => void;
    const saveStarted = new Promise<void>((resolve) => { markStarted = resolve; });
    notesClient.saveStarted = markStarted;
    const coordinator = new NotesSyncCoordinator({ repository, notesClient, settingsClient: new FakeSettingsClient() });

    const first = coordinator.syncNow();
    await saveStarted;
    const second = coordinator.syncNow();
    releaseSave();
    await Promise.all([first, second]);

    expect(notesClient.saved).toHaveLength(1);
    expect(await repository.pendingMutations()).toEqual([]);
  });

  it("publishes a visible failure and safely retries when background storage access fails", async () => {
    const { repository } = await setup();
    vi.spyOn(repository, "claimSyncLease").mockRejectedValue(new Error("IndexedDB unavailable"));
    const coordinator = new NotesSyncCoordinator({
      repository,
      notesClient: new FakeNotesClient(),
      settingsClient: new FakeSettingsClient(),
    });
    const failed = new Promise<void>((resolve) => {
      coordinator.subscribe((summary) => {
        if (summary.failed === 1) resolve();
      });
    });

    coordinator.start();
    await failed;
    coordinator.stop();
  });

  it("syncNow bypasses a queued retry delay", async () => {
    const { repository } = await setup();
    await repository.saveNote(saveInput("Retry me"));
    const notesClient = new FakeNotesClient();
    notesClient.saveError = new TypeError("offline");
    const coordinator = new NotesSyncCoordinator({
      repository,
      notesClient,
      settingsClient: new FakeSettingsClient(),
      now: () => 5_000,
      random: () => 0,
    });
    await coordinator.runOnce();
    notesClient.saveError = null;

    const summary = await coordinator.syncNow();

    expect(summary.pending).toBe(0);
    expect(notesClient.saved).toHaveLength(2);
  });

  it("requires a corrective edit before retrying a permanent save failure", async () => {
    const seed: OfflineNoteRecord = {
      userID: "usr_a", localKey: "server:work.md", serverNoteID: "work.md",
      serverRevision: "rev-1", baseRevision: "rev-1", title: "Original", bodyMarkdown: "Original body",
      pageType: "text", parentLocalKey: "", pinned: false, mcpExcluded: false,
      board: null, attachments: [], createdLocally: false, state: "clean", error: "",
      createdAt: 1, updatedAt: 1,
    };
    const { repository } = await setup(seed);
    await repository.saveNote(saveInput("Invalid", "work.md"));
    const notesClient = new FakeNotesClient();
    notesClient.saveError = new ApiError(400, "bad_request", "Invalid note", {});
    const coordinator = new NotesSyncCoordinator({ repository, notesClient, settingsClient: new FakeSettingsClient() });

    const failed = await coordinator.runOnce();
    expect(failed.failed).toBe(1);
    expect(await repository.getNoteRecord("work.md")).toMatchObject({ state: "failed", error: "Invalid note" });
    await coordinator.syncNow();
    expect(notesClient.saved).toHaveLength(1);

    notesClient.saveError = null;
    await repository.saveNote(saveInput("Corrected", "work.md"));
    expect(await repository.pendingMutations()).toHaveLength(1);
    await coordinator.runOnce();

    expect(notesClient.saved.map((input) => input.title)).toEqual(["Invalid", "Corrected"]);
    expect(await repository.getNoteRecord("work.md")).toMatchObject({ state: "clean", error: "" });
  });

  it("releases an authenticated request claim so reauthentication can resume immediately", async () => {
    const seed: OfflineNoteRecord = {
      userID: "usr_a", localKey: "server:work.md", serverNoteID: "work.md",
      serverRevision: "rev-1", baseRevision: "rev-1", title: "Original", bodyMarkdown: "Original body",
      pageType: "text", parentLocalKey: "", pinned: false, mcpExcluded: false,
      board: null, attachments: [], createdLocally: false, state: "clean", error: "",
      createdAt: 1, updatedAt: 1,
    };
    const { repository } = await setup(seed);
    await repository.saveNote(saveInput("Needs sign-in", "work.md"));
    const notesClient = new FakeNotesClient();
    notesClient.saveError = new ApiError(401, "unauthorized", "Sign in", {});
    const coordinator = new NotesSyncCoordinator({ repository, notesClient, settingsClient: new FakeSettingsClient() });

    const summary = await coordinator.runOnce();

    expect(summary.authenticationRequired).toBe(true);
    expect(await repository.pendingMutations()).toEqual([
      expect.objectContaining({ state: "queued", leaseOwner: "", leaseExpiresAt: 0 }),
    ]);
    expect(await repository.getNoteRecord("work.md")).toMatchObject({ state: "queued" });
  });

  it("ignores a stale save response after another tab takes over an expired lease", async () => {
    const seed: OfflineNoteRecord = {
      userID: "usr_a", localKey: "server:work.md", serverNoteID: "work.md",
      serverRevision: "rev-1", baseRevision: "rev-1", title: "Original", bodyMarkdown: "Original body",
      pageType: "text", parentLocalKey: "", pinned: false, mcpExcluded: false,
      board: null, attachments: [], createdLocally: false, state: "clean", error: "",
      createdAt: 1, updatedAt: 1,
    };
    const { repository } = await setup(seed);
    await repository.saveNote(saveInput("Winner", "work.md"));
    let now = 1_000;
    let releaseStale!: () => void;
    const staleGate = new Promise<void>((resolve) => { releaseStale = resolve; });
    let markStarted!: () => void;
    const staleStarted = new Promise<void>((resolve) => { markStarted = resolve; });
    const staleClient = {
      fetchNote: vi.fn(), listNotes: vi.fn(), deleteNote: vi.fn(),
      saveNote: vi.fn(async () => {
        markStarted();
        await staleGate;
        return { note_id: "work.md", revision: "stale-revision" };
      }),
    };
    const winnerClient = new FakeNotesClient();
    const stale = new NotesSyncCoordinator({ repository, notesClient: staleClient, settingsClient: new FakeSettingsClient(), ownerID: "tab-a", now: () => now });
    const winner = new NotesSyncCoordinator({ repository, notesClient: winnerClient, settingsClient: new FakeSettingsClient(), ownerID: "tab-b", now: () => now });

    const staleRun = stale.runOnce();
    await staleStarted;
    now = 17_000;
    await winner.runOnce();
    releaseStale();
    await staleRun;

    expect(await repository.getNoteRecord("work.md")).toMatchObject({ serverRevision: "synced-1", state: "clean" });
    expect(await repository.pendingMutations()).toEqual([]);
  });

  it("wakes when the repository commits a mutation and stops cleanly", async () => {
    const { repository } = await setup();
    const notesClient = new FakeNotesClient();
    const coordinator = new NotesSyncCoordinator({
      repository,
      notesClient,
      settingsClient: new FakeSettingsClient(),
      ownerID: "tab-lifecycle",
    });
    const didSync = new Promise<void>((resolve) => {
      coordinator.subscribe((summary) => {
        if (notesClient.saved.length === 1 && summary.pending === 0) resolve();
      });
    });
    coordinator.start();

    await repository.saveNote(saveInput("Wake coordinator"));
    await didSync;
    coordinator.stop();

    expect(notesClient.saved[0].title).toBe("Wake coordinator");
  });

  it("reconciles the full server note after a drained queue", async () => {
    const stale: OfflineNoteRecord = {
      userID: "usr_a", localKey: "server:work.md", serverNoteID: "work.md",
      serverRevision: "rev-1", baseRevision: "rev-1", title: "Stale", bodyMarkdown: "old",
      pageType: "text", parentLocalKey: "", pinned: false, mcpExcluded: false,
      board: null, attachments: [], createdLocally: false, state: "clean", error: "",
      createdAt: 1, updatedAt: 1,
    };
    const { repository } = await setup(stale);
    const notesClient = new FakeNotesClient();
    notesClient.fetched.set("work.md", serverNote("Current", "rev-2"));
    const coordinator = new NotesSyncCoordinator({ repository, notesClient, settingsClient: new FakeSettingsClient() });

    await coordinator.runOnce();

    expect(await repository.fetchNote("work.md")).toMatchObject({
      title: "Current",
      body_markdown: "Current body",
      revision: "rev-2",
    });
  });

  it("classifies retry, authentication, conflict, and permanent failures", () => {
    expect(classifySyncError(new TypeError("network"))).toBe("retryable");
    expect(classifySyncError(new ApiError(503, "unavailable", "down", {}))).toBe("retryable");
    expect(classifySyncError(new ApiError(401, "unauthorized", "sign in", {}))).toBe("authentication");
    expect(classifySyncError(new ApiError(409, "note_conflict", "conflict", {}))).toBe("conflict");
    expect(classifySyncError(new ApiError(400, "bad_request", "bad", {}))).toBe("permanent");
  });
});
