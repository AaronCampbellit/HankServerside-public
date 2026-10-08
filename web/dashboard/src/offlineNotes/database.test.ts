// @vitest-environment node
import "fake-indexeddb/auto";
import { afterEach, describe, expect, it } from "vitest";
import { openOfflineNotesDatabase } from "./database";
import type { OfflineAttachmentBlob, OfflineNoteRecord, OfflineWorkspaceRecord } from "./types";

const opened: Array<{ close(): void }> = [];
const names: string[] = [];

afterEach(async () => {
  for (const database of opened.splice(0)) database.close();
  for (const name of names.splice(0)) {
    await new Promise<void>((resolve, reject) => {
      const request = indexedDB.deleteDatabase(name);
      request.onsuccess = () => resolve();
      request.onerror = () => reject(request.error);
    });
  }
});

function databaseName(label: string) {
  const name = `offline-notes-${label}-${crypto.randomUUID()}`;
  names.push(name);
  return name;
}

function note(userID: string, localKey: string, title: string): OfflineNoteRecord {
  return {
    userID,
    localKey,
    serverNoteID: `${localKey}.md`,
    serverRevision: "rev-1",
    baseRevision: "rev-1",
    title,
    bodyMarkdown: `${title} body`,
    pageType: "text",
    parentLocalKey: "",
    pinned: false,
    mcpExcluded: false,
    board: null,
    attachments: [],
    createdLocally: false,
    state: "clean",
    error: "",
    createdAt: 100,
    updatedAt: 100,
  };
}

function workspace(userID: string): OfflineWorkspaceRecord {
  return {
    userID,
    identity: {
      userID,
      email: `${userID}@example.test`,
      displayName: userID,
      role: "member",
      permissions: { is_admin: false, can_use_notes: true },
      lastAuthenticatedAt: 100,
      offlineInitializedAt: 100,
    },
    profileSettingsRevision: 1,
    defaultBoardLocalKey: "",
    lastReconciledAt: 100,
    nextSequence: 1,
  };
}

async function open(name: string) {
  const database = await openOfflineNotesDatabase(name);
  opened.push(database);
  return database;
}

describe("offline Notes database", () => {
  it("reopens notes without crossing user partitions", async () => {
    const name = databaseName("partition");
    const first = await open(name);
    await first.putNote(note("usr_a", "daily", "A"));
    await first.putNote(note("usr_b", "daily", "B"));
    first.close();
    opened.splice(opened.indexOf(first), 1);

    const reopened = await open(name);
    expect((await reopened.getNote("usr_a", "daily"))?.title).toBe("A");
    expect((await reopened.getNote("usr_b", "daily"))?.title).toBe("B");
    expect(await reopened.listNotes("usr_a")).toHaveLength(1);
  });

  it("stores active identity metadata and purges only the selected user", async () => {
    const database = await open(databaseName("purge"));
    await database.setActiveUserID("usr_a");
    await database.putWorkspace(workspace("usr_a"));
    await database.putWorkspace(workspace("usr_b"));
    await database.putNote(note("usr_a", "daily", "A"));
    await database.putNote(note("usr_b", "daily", "B"));

    await database.purgeUser("usr_a");

    expect(await database.getActiveUserID()).toBe("");
    expect(await database.getWorkspace("usr_a")).toBeUndefined();
    expect(await database.listNotes("usr_a")).toEqual([]);
    expect((await database.getWorkspace("usr_b"))?.identity.userID).toBe("usr_b");
    expect((await database.getNote("usr_b", "daily"))?.title).toBe("B");
  });

  it("round-trips blobs and evicts the least recently accessed payload", async () => {
    const database = await open(databaseName("blobs"));
    const makeBlob = (attachmentID: string, size: number, lastAccessedAt: number): OfflineAttachmentBlob => ({
      userID: "usr_a",
      attachmentID,
      noteLocalKey: "daily",
      filename: `${attachmentID}.txt`,
      mediaType: "text/plain",
      size,
      blob: new Blob([attachmentID]),
      cachedAt: lastAccessedAt,
      lastAccessedAt,
    });
    await database.putAttachment(makeBlob("older", 60, 10));
    await database.putAttachment(makeBlob("newer", 60, 20));

    expect(await (await database.getAttachment("usr_a", "older"))!.blob.text()).toBe("older");
    await database.evictAttachmentBlobs("usr_a", 100);

    expect(await database.getAttachment("usr_a", "older")).toBeUndefined();
    expect(await (await database.getAttachment("usr_a", "newer"))!.blob.text()).toBe("newer");
  });

  it("allows only one unexpired lease owner", async () => {
    const database = await open(databaseName("lease"));

    expect(await database.claimSyncLease("usr_a", "tab-a", 100, 15)).toBe(true);
    expect(await database.claimSyncLease("usr_a", "tab-b", 110, 15)).toBe(false);
    expect(await database.claimSyncLease("usr_a", "tab-b", 116, 15)).toBe(true);
    expect(await database.renewSyncLease("usr_a", "tab-a", 120, 15)).toBe(false);
    expect(await database.renewSyncLease("usr_a", "tab-b", 120, 15)).toBe(true);
    await database.releaseSyncLease("usr_a", "tab-b");
    expect(await database.claimSyncLease("usr_a", "tab-a", 121, 15)).toBe(true);
  });

  it("aborts queued writes when a transaction callback throws", async () => {
    const database = await open(databaseName("atomic-callback"));

    await expect(database.runTransaction(["notes"], "readwrite", async (transaction) => {
      transaction.objectStore("notes").put(note("usr_a", "daily", "Must roll back"));
      throw new Error("application failure");
    })).rejects.toThrow("Offline Notes storage is unavailable");

    expect(await database.getNote("usr_a", "daily")).toBeUndefined();
  });
});
