import type { BootstrapState } from "../api/bootstrap";
import type { OfflineNotesDatabase } from "./database";
import type { OfflineIdentity, OfflineWorkspaceRecord } from "./types";

const DEFAULT_ELIGIBILITY_MS = 30 * 24 * 60 * 60 * 1_000;

export class OfflineIdentityStore {
  constructor(
    private readonly database: OfflineNotesDatabase,
    private readonly now: () => number = Date.now,
  ) {}

  async recordAuthenticated(bootstrap: BootstrapState): Promise<OfflineIdentity> {
    const now = this.now();
    const existing = await this.database.getWorkspace(bootstrap.user.id);
    const identity: OfflineIdentity = {
      userID: bootstrap.user.id,
      email: bootstrap.user.email,
      displayName: bootstrap.user.display_name,
      role: bootstrap.membership?.role ?? "",
      permissions: {
        is_admin: bootstrap.permissions.is_admin,
        can_use_notes: bootstrap.permissions.can_use_notes,
      },
      lastAuthenticatedAt: now,
      offlineInitializedAt: existing?.identity.offlineInitializedAt ?? 0,
    };
    const workspace: OfflineWorkspaceRecord = existing
      ? { ...existing, identity }
      : {
          userID: identity.userID,
          identity,
          profileSettingsRevision: 0,
          defaultBoardLocalKey: "",
          lastReconciledAt: 0,
          nextSequence: 1,
        };
    await this.database.putWorkspace(workspace);
    await this.database.setActiveUserID(identity.userID);
    return identity;
  }

  async restoreEligible(maxAgeMs = DEFAULT_ELIGIBILITY_MS): Promise<OfflineIdentity | null> {
    const activeUserID = await this.database.getActiveUserID();
    if (!activeUserID) return null;
    const workspace = await this.database.getWorkspace(activeUserID);
    if (!workspace) return null;
    if (!workspace.identity.offlineInitializedAt || !workspace.lastReconciledAt) return null;
    if (this.now() - workspace.identity.lastAuthenticatedAt > maxAgeMs) return null;
    return workspace.identity;
  }

  clearActive(): Promise<void> {
    return this.database.setActiveUserID("");
  }

  purge(userID: string): Promise<void> {
    return this.database.purgeUser(userID);
  }
}
