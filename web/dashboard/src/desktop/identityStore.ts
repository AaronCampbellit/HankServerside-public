export interface DesktopIdentityStore {
  get(deviceID: string): Promise<CryptoKeyPair | null>;
  create(deviceID: string): Promise<{ keyPair: CryptoKeyPair; spki: Uint8Array }>;
  install(deviceID: string, privateKeyPKCS8: Uint8Array, publicKeySPKI: Uint8Array): Promise<CryptoKeyPair>;
  getPublicSPKI(deviceID: string): Promise<Uint8Array | null>;
  remove(deviceID: string): Promise<void>;
  removeMany(deviceIDs: readonly string[]): Promise<void>;
}

interface IdentityRecord { deviceID: string; privateKey: CryptoKey; publicKey: CryptoKey; publicKeySPKI?: Uint8Array }
const databaseName = "hank-desktop-v1", storeName = "operator-identities";

export class IndexedDBDesktopIdentityStore implements DesktopIdentityStore {
  async get(deviceID: string): Promise<CryptoKeyPair | null> {
    const db = await openDatabase();
    try {
      const value = await request<IdentityRecord | undefined>(db.transaction(storeName).objectStore(storeName).get(deviceID));
      return value ? { privateKey: value.privateKey, publicKey: value.publicKey } : null;
    } finally { db.close(); }
  }

  async create(deviceID: string): Promise<{ keyPair: CryptoKeyPair; spki: Uint8Array }> {
    if (!deviceID.trim()) throw new Error("desktop_device_id_required");
    const keyPair = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, false, ["sign", "verify"]);
    const spki = new Uint8Array(await crypto.subtle.exportKey("spki", keyPair.publicKey));
    const db = await openDatabase();
    try {
      const transaction = db.transaction(storeName, "readwrite");
      transaction.objectStore(storeName).put({ deviceID, privateKey: keyPair.privateKey, publicKey: keyPair.publicKey, publicKeySPKI: spki } satisfies IdentityRecord);
      await transactionDone(transaction);
    } finally { db.close(); }
    return { keyPair, spki };
  }

  async install(deviceID: string, privateKeyPKCS8: Uint8Array, publicKeySPKI: Uint8Array): Promise<CryptoKeyPair> {
    if (!deviceID.trim()) throw new Error("desktop_device_id_required");
    const privateKey = await crypto.subtle.importKey("pkcs8", ownedBuffer(privateKeyPKCS8), { name: "ECDSA", namedCurve: "P-256" }, false, ["sign"]);
    const publicKey = await crypto.subtle.importKey("spki", ownedBuffer(publicKeySPKI), { name: "ECDSA", namedCurve: "P-256" }, false, ["verify"]);
    const db = await openDatabase();
    try {
      const transaction = db.transaction(storeName, "readwrite");
      transaction.objectStore(storeName).put({ deviceID, privateKey, publicKey, publicKeySPKI: publicKeySPKI.slice() } satisfies IdentityRecord);
      await transactionDone(transaction);
    } finally { db.close(); }
    return { privateKey, publicKey };
  }

  async getPublicSPKI(deviceID: string): Promise<Uint8Array | null> {
    const db = await openDatabase();
    try { const value = await request<IdentityRecord | undefined>(db.transaction(storeName).objectStore(storeName).get(deviceID)); return value?.publicKeySPKI ? new Uint8Array(value.publicKeySPKI) : null; }
    finally { db.close(); }
  }

  async remove(deviceID: string): Promise<void> {
    const db = await openDatabase();
    try { const tx = db.transaction(storeName, "readwrite"); tx.objectStore(storeName).delete(deviceID); await transactionDone(tx); }
    finally { db.close(); }
  }

  async removeMany(deviceIDs: readonly string[]): Promise<void> {
    const unique = [...new Set(deviceIDs.filter(value => value.trim()))];
    if (unique.length === 0) return;
    const db = await openDatabase();
    try { const tx = db.transaction(storeName, "readwrite"); const store = tx.objectStore(storeName); for (const deviceID of unique) store.delete(deviceID); await transactionDone(tx); }
    finally { db.close(); }
  }

  async clearForTests(): Promise<void> {
    const db = await openDatabase();
    try { const tx = db.transaction(storeName, "readwrite"); tx.objectStore(storeName).clear(); await transactionDone(tx); }
    finally { db.close(); }
  }
}

function openDatabase(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const open = indexedDB.open(databaseName, 1);
    open.onupgradeneeded = () => { if (!open.result.objectStoreNames.contains(storeName)) open.result.createObjectStore(storeName, { keyPath: "deviceID" }); };
    open.onsuccess = () => resolve(open.result); open.onerror = () => reject(open.error);
  });
}

function request<T>(value: IDBRequest<T>): Promise<T> { return new Promise((resolve, reject) => { value.onsuccess = () => resolve(value.result); value.onerror = () => reject(value.error); }); }
function transactionDone(value: IDBTransaction): Promise<void> { return new Promise((resolve, reject) => { value.oncomplete = () => resolve(); value.onerror = () => reject(value.error); value.onabort = () => reject(value.error); }); }
function ownedBuffer(value: Uint8Array): ArrayBuffer { const copy = new Uint8Array(value.byteLength); copy.set(value); return copy.buffer; }
