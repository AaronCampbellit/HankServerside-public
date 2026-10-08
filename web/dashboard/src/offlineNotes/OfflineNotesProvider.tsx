import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import type { BootstrapState } from "../api/bootstrap";
import { profileSettingsClient } from "../api/profileSettings";
import { profileNotesClient } from "../api/profileNotes";
import { openOfflineNotesDatabase, type OfflineNotesDatabase } from "./database";
import { OfflineIdentityStore } from "./identity";
import { OfflineNotesRepository } from "./repository";
import { NotesSyncCoordinator } from "./syncCoordinator";
import type { OfflineIdentity, OfflineSyncSummary } from "./types";

const EMPTY_SYNC: OfflineSyncSummary = {
  pending: 0,
  syncing: 0,
  failed: 0,
  conflicted: 0,
  authenticationRequired: false,
  lastSyncedAt: 0,
};

export type OfflineRuntimeCoordinator = {
  start(): void;
  stop(): void;
  syncNow(): Promise<OfflineSyncSummary>;
  subscribe(listener: (summary: OfflineSyncSummary) => void): () => void;
};

export type OfflineNotesContextValue = {
  mode: "inactive" | "online" | "offline" | "locked";
  activeUserID: string;
  repository: OfflineNotesRepository | null;
  sync: OfflineSyncSummary;
  activateAuthenticated(bootstrap: BootstrapState): Promise<void>;
  activateOffline(): Promise<OfflineIdentity | null>;
  syncNow(): Promise<void>;
  purgeActive(): Promise<void>;
};

type OfflineNotesProviderProps = {
  children: ReactNode;
  databaseName?: string;
  createCoordinator?: (repository: OfflineNotesRepository) => OfflineRuntimeCoordinator;
};

const OfflineNotesContext = createContext<OfflineNotesContextValue | null>(null);

function defaultCoordinator(repository: OfflineNotesRepository): OfflineRuntimeCoordinator {
  return new NotesSyncCoordinator({ repository, notesClient: profileNotesClient, settingsClient: profileSettingsClient });
}

export function OfflineNotesProvider({
  children,
  databaseName = "hank-offline-notes-v1",
  createCoordinator = defaultCoordinator,
}: OfflineNotesProviderProps) {
  const databasePromise = useMemo(() => openOfflineNotesDatabase(databaseName), [databaseName]);
  const [mode, setMode] = useState<OfflineNotesContextValue["mode"]>("inactive");
  const [activeUserID, setActiveUserID] = useState("");
  const [repository, setRepository] = useState<OfflineNotesRepository | null>(null);
  const [sync, setSync] = useState<OfflineSyncSummary>(EMPTY_SYNC);
  const lockedUserIDRef = useRef("");
  const runtimeRef = useRef<{
    userID: string;
    database: OfflineNotesDatabase;
    repository: OfflineNotesRepository;
    coordinator: OfflineRuntimeCoordinator;
    unsubscribe: () => void;
  } | null>(null);

  const ensureRuntime = useCallback(async (userID: string) => {
    if (runtimeRef.current?.userID === userID) return runtimeRef.current;
    runtimeRef.current?.unsubscribe();
    runtimeRef.current?.coordinator.stop();
    const database = await databasePromise;
    const nextRepository = new OfflineNotesRepository(userID, database);
    const coordinator = createCoordinator(nextRepository);
    let unsubscribe: () => void = () => undefined;
    unsubscribe = coordinator.subscribe((summary) => {
      setSync(summary);
      if (summary.authenticationRequired) {
        unsubscribe();
        coordinator.stop();
        if (runtimeRef.current?.coordinator === coordinator) runtimeRef.current = null;
        lockedUserIDRef.current = userID;
        void new OfflineIdentityStore(database).clearActive().catch(() => undefined);
        setRepository(null);
        setActiveUserID("");
        setMode("locked");
      }
    });
    const runtime = { userID, database, repository: nextRepository, coordinator, unsubscribe };
    runtimeRef.current = runtime;
    lockedUserIDRef.current = "";
    setActiveUserID(userID);
    setRepository(nextRepository);
    setSync(EMPTY_SYNC);
    coordinator.start();
    return runtime;
  }, [createCoordinator, databasePromise]);

  const activateAuthenticated = useCallback(async (bootstrap: BootstrapState) => {
    const database = await databasePromise;
    if (!bootstrap.permissions.can_use_notes) {
      await new OfflineIdentityStore(database).recordAuthenticated(bootstrap);
      runtimeRef.current?.unsubscribe();
      runtimeRef.current?.coordinator.stop();
      runtimeRef.current = null;
      lockedUserIDRef.current = bootstrap.user.id;
      setRepository(null);
      setActiveUserID("");
      setSync(EMPTY_SYNC);
      setMode("locked");
      return;
    }
    const identity = await new OfflineIdentityStore(database).recordAuthenticated(bootstrap);
    await ensureRuntime(identity.userID);
    setMode("online");
  }, [databasePromise, ensureRuntime]);

  const activateOffline = useCallback(async () => {
    const database = await databasePromise;
    const identityStore = new OfflineIdentityStore(database);
    const identity = await identityStore.restoreEligible();
    if (!identity) {
      setMode("inactive");
      return null;
    }
    if (!identity.permissions.can_use_notes) {
      runtimeRef.current?.unsubscribe();
      runtimeRef.current?.coordinator.stop();
      runtimeRef.current = null;
      lockedUserIDRef.current = identity.userID;
      setRepository(null);
      setActiveUserID("");
      setSync(EMPTY_SYNC);
      setMode("locked");
      return null;
    }
    await ensureRuntime(identity.userID);
    setMode("offline");
    return identity;
  }, [databasePromise, ensureRuntime]);

  const syncNow = useCallback(async () => {
    const runtime = runtimeRef.current;
    if (!runtime) return;
    setSync(await runtime.coordinator.syncNow());
  }, []);

  const purgeActive = useCallback(async () => {
    const runtime = runtimeRef.current;
    runtime?.unsubscribe();
    runtime?.coordinator.stop();
    runtimeRef.current = null;
    const database = runtime?.database ?? await databasePromise;
    const identityStore = new OfflineIdentityStore(database);
    const userID = runtime?.userID || lockedUserIDRef.current || await database.getActiveUserID();
    await identityStore.clearActive();
    if (userID) await database.purgeUser(userID);
    lockedUserIDRef.current = "";
    setRepository(null);
    setActiveUserID("");
    setSync(EMPTY_SYNC);
    setMode("inactive");
  }, [databasePromise]);

  useEffect(() => () => {
    const runtime = runtimeRef.current;
    runtime?.unsubscribe();
    runtime?.coordinator.stop();
    runtimeRef.current = null;
    void databasePromise.then((database) => database.close());
  }, [databasePromise]);

  const value = useMemo<OfflineNotesContextValue>(() => ({
    mode,
    activeUserID,
    repository,
    sync,
    activateAuthenticated,
    activateOffline,
    syncNow,
    purgeActive,
  }), [
    mode,
    activeUserID,
    repository,
    sync,
    activateAuthenticated,
    activateOffline,
    syncNow,
    purgeActive,
  ]);

  return <OfflineNotesContext.Provider value={value}>{children}</OfflineNotesContext.Provider>;
}

export function useOfflineNotes(): OfflineNotesContextValue {
  const value = useContext(OfflineNotesContext);
  if (!value) throw new Error("useOfflineNotes must be used within OfflineNotesProvider");
  return value;
}
