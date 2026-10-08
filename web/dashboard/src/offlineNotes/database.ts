import {
  OfflineRepositoryError,
  type OfflineAttachmentBlob,
  type OfflineMutationRecord,
  type OfflineNoteRecord,
  type OfflineStoreName,
  type OfflineSyncLease,
  type OfflineWorkspaceRecord,
} from "./types";

const DATABASE_VERSION = 1;
const ACTIVE_USER_KEY = "active_user";
const DEFAULT_ATTACHMENT_CAP_BYTES = 100 * 1024 * 1024;

type MetadataRecord = { key: string; value: string };

export interface OfflineNotesDatabase {
  close(): void;
  getActiveUserID(): Promise<string>;
  setActiveUserID(userID: string): Promise<void>;
  getWorkspace(userID: string): Promise<OfflineWorkspaceRecord | undefined>;
  putWorkspace(record: OfflineWorkspaceRecord): Promise<void>;
  listNotes(userID: string): Promise<OfflineNoteRecord[]>;
  getNote(userID: string, localKey: string): Promise<OfflineNoteRecord | undefined>;
  getNoteByServerID(userID: string, serverNoteID: string): Promise<OfflineNoteRecord | undefined>;
  putNote(record: OfflineNoteRecord): Promise<void>;
  deleteNote(userID: string, localKey: string): Promise<void>;
  listMutations(userID: string): Promise<OfflineMutationRecord[]>;
  putMutation(record: OfflineMutationRecord): Promise<void>;
  deleteMutation(userID: string, mutationID: string): Promise<void>;
  runTransaction<T>(
    stores: OfflineStoreName[],
    mode: IDBTransactionMode,
    operation: (transaction: IDBTransaction) => Promise<T>,
  ): Promise<T>;
  putAttachment(record: OfflineAttachmentBlob): Promise<void>;
  getAttachment(userID: string, attachmentID: string): Promise<OfflineAttachmentBlob | undefined>;
  evictAttachmentBlobs(userID: string, softCapBytes?: number): Promise<void>;
  claimSyncLease(userID: string, ownerID: string, now: number, ttlMs: number): Promise<boolean>;
  renewSyncLease(userID: string, ownerID: string, now: number, ttlMs: number): Promise<boolean>;
  releaseSyncLease(userID: string, ownerID: string): Promise<void>;
  purgeUser(userID: string): Promise<void>;
}

function requestResult<T>(request: IDBRequest<T>): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error ?? new Error("IndexedDB request failed"));
  });
}

function transactionComplete(transaction: IDBTransaction): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    transaction.oncomplete = () => resolve();
    transaction.onerror = () => reject(transaction.error ?? new Error("IndexedDB transaction failed"));
    transaction.onabort = () => reject(transaction.error ?? new Error("IndexedDB transaction aborted"));
  });
}

function userRange(userID: string): IDBKeyRange {
  return IDBKeyRange.bound([userID, ""], [userID, "\uffff"]);
}

function storageError(error: unknown): OfflineRepositoryError {
  if (error instanceof OfflineRepositoryError) return error;
  if (error instanceof DOMException && error.name === "QuotaExceededError") {
    return new OfflineRepositoryError("quota_exceeded", "Offline Notes storage is full");
  }
  return new OfflineRepositoryError("storage_unavailable", "Offline Notes storage is unavailable");
}

async function safely<T>(operation: () => Promise<T>): Promise<T> {
  try {
    return await operation();
  } catch (error) {
    throw storageError(error);
  }
}

class IndexedDBOfflineNotesDatabase implements OfflineNotesDatabase {
  constructor(private readonly database: IDBDatabase) {}

  close() {
    this.database.close();
  }

  async runTransaction<T>(
    stores: OfflineStoreName[],
    mode: IDBTransactionMode,
    operation: (transaction: IDBTransaction) => Promise<T>,
  ): Promise<T> {
    return safely(async () => {
      const transaction = this.database.transaction(stores, mode);
      const completion = transactionComplete(transaction);
      let result: T;
      try {
        result = await operation(transaction);
      } catch (error) {
        try {
          transaction.abort();
        } catch {
          // The transaction may already have aborted because of an IndexedDB request error.
        }
        await completion.catch(() => undefined);
        throw error;
      }
      await completion;
      return result;
    });
  }

  getActiveUserID(): Promise<string> {
    return this.read("metadata", async (store) => {
      const record = await requestResult(store.get(ACTIVE_USER_KEY)) as MetadataRecord | undefined;
      return record?.value ?? "";
    });
  }

  setActiveUserID(userID: string): Promise<void> {
    return this.write("metadata", (store) => {
      if (userID) store.put({ key: ACTIVE_USER_KEY, value: userID } satisfies MetadataRecord);
      else store.delete(ACTIVE_USER_KEY);
    });
  }

  getWorkspace(userID: string): Promise<OfflineWorkspaceRecord | undefined> {
    return this.read("workspaces", (store) => requestResult(store.get(userID)));
  }

  putWorkspace(record: OfflineWorkspaceRecord): Promise<void> {
    return this.write("workspaces", (store) => { store.put(record); });
  }

  listNotes(userID: string): Promise<OfflineNoteRecord[]> {
    return this.read("notes", (store) => requestResult(store.getAll(userRange(userID))));
  }

  getNote(userID: string, localKey: string): Promise<OfflineNoteRecord | undefined> {
    return this.read("notes", (store) => requestResult(store.get([userID, localKey])));
  }

  getNoteByServerID(userID: string, serverNoteID: string): Promise<OfflineNoteRecord | undefined> {
    return this.read("notes", async (store) => {
      const index = store.index("notes_by_user_server_id");
      return requestResult(index.get([userID, serverNoteID]));
    });
  }

  putNote(record: OfflineNoteRecord): Promise<void> {
    return this.write("notes", (store) => { store.put(record); });
  }

  deleteNote(userID: string, localKey: string): Promise<void> {
    return this.write("notes", (store) => { store.delete([userID, localKey]); });
  }

  listMutations(userID: string): Promise<OfflineMutationRecord[]> {
    return this.read("mutations", async (store) => {
      const range = IDBKeyRange.bound([userID, 0], [userID, Number.MAX_SAFE_INTEGER]);
      const records = await requestResult(store.index("mutations_by_user_sequence").getAll(range));
      return records;
    });
  }

  putMutation(record: OfflineMutationRecord): Promise<void> {
    return this.write("mutations", (store) => { store.put(record); });
  }

  deleteMutation(userID: string, mutationID: string): Promise<void> {
    return this.write("mutations", (store) => { store.delete([userID, mutationID]); });
  }

  putAttachment(record: OfflineAttachmentBlob): Promise<void> {
    return this.write("attachment_blobs", (store) => { store.put(record); });
  }

  getAttachment(userID: string, attachmentID: string): Promise<OfflineAttachmentBlob | undefined> {
    return this.read("attachment_blobs", (store) => requestResult(store.get([userID, attachmentID])));
  }

  async evictAttachmentBlobs(userID: string, softCapBytes = DEFAULT_ATTACHMENT_CAP_BYTES): Promise<void> {
    await this.runTransaction(["attachment_blobs"], "readwrite", async (transaction) => {
      const store = transaction.objectStore("attachment_blobs");
      const records = await requestResult<OfflineAttachmentBlob[]>(store.getAll(userRange(userID)));
      let total = records.reduce((sum, record) => sum + Math.max(0, record.size), 0);
      records.sort((left, right) => left.lastAccessedAt - right.lastAccessedAt);
      for (const record of records) {
        if (total <= softCapBytes) break;
        store.delete([userID, record.attachmentID]);
        total -= Math.max(0, record.size);
      }
    });
  }

  claimSyncLease(userID: string, ownerID: string, now: number, ttlMs: number): Promise<boolean> {
    return this.runTransaction(["sync_leases"], "readwrite", async (transaction) => {
      const store = transaction.objectStore("sync_leases");
      const current = await requestResult(store.get(userID)) as OfflineSyncLease | undefined;
      if (current && current.ownerID !== ownerID && current.expiresAt > now) return false;
      store.put({ userID, ownerID, expiresAt: now + ttlMs } satisfies OfflineSyncLease);
      return true;
    });
  }

  renewSyncLease(userID: string, ownerID: string, now: number, ttlMs: number): Promise<boolean> {
    return this.runTransaction(["sync_leases"], "readwrite", async (transaction) => {
      const store = transaction.objectStore("sync_leases");
      const current = await requestResult(store.get(userID)) as OfflineSyncLease | undefined;
      if (!current || current.ownerID !== ownerID || current.expiresAt <= now) return false;
      store.put({ userID, ownerID, expiresAt: now + ttlMs } satisfies OfflineSyncLease);
      return true;
    });
  }

  releaseSyncLease(userID: string, ownerID: string): Promise<void> {
    return this.runTransaction(["sync_leases"], "readwrite", async (transaction) => {
      const store = transaction.objectStore("sync_leases");
      const current = await requestResult(store.get(userID)) as OfflineSyncLease | undefined;
      if (current?.ownerID === ownerID) store.delete(userID);
    });
  }

  async purgeUser(userID: string): Promise<void> {
    await this.runTransaction(
      ["metadata", "workspaces", "notes", "mutations", "attachment_blobs", "sync_leases"],
      "readwrite",
      async (transaction) => {
        const metadata = transaction.objectStore("metadata");
        const active = await requestResult(metadata.get(ACTIVE_USER_KEY)) as MetadataRecord | undefined;
        if (active?.value === userID) metadata.delete(ACTIVE_USER_KEY);
        transaction.objectStore("workspaces").delete(userID);
        transaction.objectStore("sync_leases").delete(userID);
        for (const storeName of ["notes", "mutations", "attachment_blobs"] as const) {
          const store = transaction.objectStore(storeName);
          const keys = await requestResult(store.getAllKeys(userRange(userID)));
          for (const key of keys) store.delete(key);
        }
      },
    );
  }

  private read<T>(
    storeName: OfflineStoreName,
    operation: (store: IDBObjectStore) => Promise<T>,
  ): Promise<T> {
    return this.runTransaction([storeName], "readonly", (transaction) => operation(transaction.objectStore(storeName)));
  }

  private write(storeName: OfflineStoreName, operation: (store: IDBObjectStore) => void): Promise<void> {
    return this.runTransaction([storeName], "readwrite", async (transaction) => {
      operation(transaction.objectStore(storeName));
    });
  }
}

function upgrade(database: IDBDatabase): void {
  if (!database.objectStoreNames.contains("metadata")) {
    database.createObjectStore("metadata", { keyPath: "key" });
  }
  if (!database.objectStoreNames.contains("workspaces")) {
    database.createObjectStore("workspaces", { keyPath: "userID" });
  }
  if (!database.objectStoreNames.contains("notes")) {
    const notes = database.createObjectStore("notes", { keyPath: ["userID", "localKey"] });
    notes.createIndex("notes_by_user_server_id", ["userID", "serverNoteID"], { unique: true });
  }
  if (!database.objectStoreNames.contains("mutations")) {
    const mutations = database.createObjectStore("mutations", { keyPath: ["userID", "mutationID"] });
    mutations.createIndex("mutations_by_user_sequence", ["userID", "sequence"], { unique: true });
    mutations.createIndex("mutations_by_user_note", ["userID", "localKey"], { unique: false });
  }
  if (!database.objectStoreNames.contains("attachment_blobs")) {
    const attachments = database.createObjectStore("attachment_blobs", { keyPath: ["userID", "attachmentID"] });
    attachments.createIndex("attachments_by_user_access", ["userID", "lastAccessedAt"], { unique: false });
  }
  if (!database.objectStoreNames.contains("sync_leases")) {
    database.createObjectStore("sync_leases", { keyPath: "userID" });
  }
}

export function openOfflineNotesDatabase(name = "hank-offline-notes-v1"): Promise<OfflineNotesDatabase> {
  return safely(() => new Promise<OfflineNotesDatabase>((resolve, reject) => {
    const request = indexedDB.open(name, DATABASE_VERSION);
    request.onupgradeneeded = () => upgrade(request.result);
    request.onsuccess = () => resolve(new IndexedDBOfflineNotesDatabase(request.result));
    request.onerror = () => reject(request.error ?? new Error("Unable to open offline Notes storage"));
    request.onblocked = () => reject(new Error("Offline Notes storage upgrade is blocked"));
  }));
}
