// @vitest-environment node
import "fake-indexeddb/auto";
import { afterEach, describe, expect, it } from "vitest";
import type { NoteAttachment, ProfileNote, SaveProfileNoteInput } from "../api/profileNotes";
import { openOfflineNotesDatabase, type OfflineNotesDatabase } from "./database";
import { OfflineNotesRepository } from "./repository";
import type { OfflineIdentity, OfflineNoteRecord, OfflineWorkspaceRecord } from "./types";

const databases: OfflineNotesDatabase[] = [];
const databaseNames: string[] = [];

afterEach(async () => {
  for (const database of databases.splice(0)) database.close();
  for (const name of databaseNames.splice(0)) {
    await new Promise<void>((resolve, reject) => {
      const request = indexedDB.deleteDatabase(name);
      request.onsuccess = () => resolve();
      request.onerror = () => reject(request.error);
    });
  }
});

const identity: OfflineIdentity = {
  userID: "usr_a",
  email: "a@example.test",
  displayName: "A",
  role: "member",
  permissions: { is_admin: false, can_use_notes: true },
  lastAuthenticatedAt: 1,
  offlineInitializedAt: 1,
};

function note(overrides: Partial<OfflineNoteRecord> = {}): OfflineNoteRecord {
  return {
    userID: "usr_a",
    localKey: "server:daily.md",
    serverNoteID: "daily.md",
    serverRevision: "rev-1",
    baseRevision: "rev-1",
    title: "Daily",
    bodyMarkdown: "one",
    pageType: "text",
    parentLocalKey: "",
    pinned: false,
    mcpExcluded: false,
    board: null,
    attachments: [],
    createdLocally: false,
    state: "clean",
    error: "",
    createdAt: 1,
    updatedAt: 1,
    ...overrides,
  };
}

function input(noteID: string, bodyMarkdown: string, overrides: Partial<SaveProfileNoteInput> = {}): SaveProfileNoteInput {
  return {
    note_id: noteID,
    title: noteID ? "Daily" : "New note",
    body_markdown: bodyMarkdown,
    expected_revision: noteID ? "rev-1" : "",
    page_type: "text",
    parent_id: "",
    pinned: false,
    mcp_excluded: false,
    ...overrides,
  };
}

async function setup(options: { seed?: OfflineNoteRecord; uuids?: string[] } = {}) {
  const name = `offline-repository-${crypto.randomUUID()}`;
  databaseNames.push(name);
  const database = await openOfflineNotesDatabase(name);
  databases.push(database);
  const workspace: OfflineWorkspaceRecord = {
    userID: "usr_a",
    identity,
    profileSettingsRevision: 1,
    defaultBoardLocalKey: "",
    lastReconciledAt: 0,
    nextSequence: 1,
  };
  await database.putWorkspace(workspace);
  if (options.seed) await database.putNote(options.seed);
  const uuids = [...(options.uuids ?? ["uuid-1", "uuid-2", "uuid-3"])];
  return {
    database,
    repository: new OfflineNotesRepository("usr_a", database, () => 1_000, () => uuids.shift() ?? "uuid-last"),
  };
}

describe("OfflineNotesRepository", () => {
  it("coalesces saves without losing the original base revision", async () => {
    const { repository } = await setup({ seed: note() });

    await repository.saveNote(input("daily.md", "two"));
    await repository.saveNote(input("daily.md", "three", { expected_revision: "rev-local" }));

    const mutations = await repository.pendingMutations();
    expect(mutations).toHaveLength(1);
    expect(mutations[0].baseRevision).toBe("rev-1");
    expect((mutations[0].payload as SaveProfileNoteInput).body_markdown).toBe("three");
    expect((await repository.fetchNote("daily.md")).body_markdown).toBe("three");
  });

  it("allocates one stable server ID for an offline-created note", async () => {
    const { repository } = await setup({ uuids: ["stable-id"] });

    const saved = await repository.saveNote(input("", "offline body"));
    await repository.saveNote(input(saved.localKey, "edited offline"));

    expect(saved.localKey).toBe("local:stable-id");
    expect(saved.noteID).toBe("offline-stable-id.md");
    expect((await repository.fetchNote(saved.localKey)).body_markdown).toBe("edited offline");
    expect(await repository.pendingMutations()).toHaveLength(1);
  });

  it("exposes the locally selected default board", async () => {
    const { repository } = await setup({ seed: note({ pageType: "kanban" }) });

    await repository.setDefaultBoard("daily.md");

    expect(await repository.getDefaultBoardLocalKey()).toBe("server:daily.md");

    await repository.setDefaultBoard("");

    expect(await repository.getDefaultBoardLocalKey()).toBe("");
    expect(await repository.pendingMutations()).toEqual([
      expect.objectContaining({
        type: "set_default_board",
        localKey: "",
        payload: { boardLocalKey: "" },
      }),
    ]);
  });

  it("tracks a locally-created parent as a save dependency", async () => {
    const { repository } = await setup({ uuids: ["parent", "child"] });
    const parent = await repository.saveNote(input("", "", { title: "Notebook", page_type: "notebook" }));

    await repository.saveNote(input("", "child", { title: "Child", parent_id: parent.localKey }));

    const mutations = await repository.pendingMutations();
    expect(mutations).toHaveLength(2);
    expect(mutations[1].dependencyLocalKeys).toEqual([parent.localKey]);
  });

  it("cancels a create that is deleted before synchronization", async () => {
    const { repository } = await setup({ uuids: ["throwaway"] });
    const saved = await repository.saveNote(input("", "temporary"));

    await repository.deleteNote(saved.localKey);

    expect(await repository.listNotes()).toEqual([]);
    expect(await repository.pendingMutations()).toEqual([]);
  });

  it("queues a conditional delete for a server-backed note", async () => {
    const { repository } = await setup({ seed: note() });
    await repository.saveNote(input("daily.md", "unsynced edit"));

    await repository.deleteNote("daily.md");

    const mutations = await repository.pendingMutations();
    expect(mutations).toHaveLength(1);
    expect(mutations[0]).toMatchObject({
      type: "delete_note",
      localKey: "server:daily.md",
      baseRevision: "rev-1",
      payload: null,
    });
    expect(await repository.listNotes()).toEqual([]);
  });

  it("round-trips cached attachments, touches reads, and evicts blobs without note metadata", async () => {
    const attachment = (id: string): NoteAttachment => ({
      id,
      filename: `${id}.txt`,
      content_type: "text/plain",
      size_bytes: 6,
      download_url: `/attachments/${id}`,
      markdown_reference: `[${id}](hank-note-attachment://${id})`,
    });
    const seeded = note({ attachments: [attachment("old"), attachment("new")] });
    const { database } = await setup({ seed: seeded });
    let now = 10;
    const repository = new OfflineNotesRepository("usr_a", database, () => now++, () => "unused", 10);

    await repository.cacheAttachment(seeded.localKey, attachment("old"), new Blob(["123456"]));
    const cached = await repository.readCachedAttachment("old");
    expect(await cached?.text()).toBe("123456");
    expect((await database.getAttachment("usr_a", "old"))?.lastAccessedAt).toBe(11);

    await repository.cacheAttachment(seeded.localKey, attachment("new"), new Blob(["abcdef"]));

    expect(await repository.readCachedAttachment("old")).toBeNull();
    expect(await (await repository.readCachedAttachment("new"))?.text()).toBe("abcdef");
    expect((await repository.fetchNote("daily.md")).attachments).toHaveLength(2);
  });

  it("reconciles clean server notes without overwriting queued work", async () => {
    const { repository } = await setup({ seed: note() });
    await repository.saveNote(input("daily.md", "offline edit"));
    const serverNotes: ProfileNote[] = [{
      note_id: "daily.md",
      title: "Daily from server",
      body_markdown: "server edit",
      revision: "rev-2",
      page_type: "text",
      parent_id: "",
      pinned: false,
      mcp_excluded: false,
      board: null,
      attachments: [],
      updated_at: "2026-08-18T00:00:00Z",
    }];

    await repository.reconcile(serverNotes, { revision: 4, settings: {} });

    expect((await repository.fetchNote("daily.md")).body_markdown).toBe("offline edit");
    expect(await repository.pendingMutations()).toHaveLength(1);
  });
});
