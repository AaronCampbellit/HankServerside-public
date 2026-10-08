// @vitest-environment node
import "fake-indexeddb/auto";
import { afterEach, describe, expect, it } from "vitest";
import type { BootstrapState } from "../api/bootstrap";
import { openOfflineNotesDatabase, type OfflineNotesDatabase } from "./database";
import { OfflineIdentityStore } from "./identity";

const databases: OfflineNotesDatabase[] = [];
const names: string[] = [];

afterEach(async () => {
  for (const database of databases.splice(0)) database.close();
  for (const name of names.splice(0)) {
    await new Promise<void>((resolve, reject) => {
      const request = indexedDB.deleteDatabase(name);
      request.onsuccess = () => resolve();
      request.onerror = () => reject(request.error);
    });
  }
});

function bootstrap(userID: string): BootstrapState {
  return {
    user: {
      id: userID,
      email: `${userID}@example.test`,
      display_name: "Offline User",
      password_change_required: false,
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    },
    session: { id: "secret-session-id", expires_at: "2026-09-01T00:00:00Z" },
    home: { id: "home-secret", user_id: userID, name: "Home", created_at: "", updated_at: "" },
    membership: { home_id: "home-secret", user_id: userID, role: "member", created_at: "", updated_at: "" },
    permissions: {
      is_admin: false,
      can_manage_people: false,
      can_manage_settings: false,
      can_use_homeassistant: true,
      can_use_files: true,
      can_use_notes: true,
      can_use_assistant: true,
      can_view_storage: false,
      can_manage_apps: false,
    },
    agent: { agent_id: "agent-secret", name: "Agent", status: "online", home_id: "home-secret", home_name: "Home" },
    setup_status: { first_setup_visible: false },
    features: { mcp_enabled: true },
    server: { version: "test" },
    navigation: [],
  };
}

async function setup(now = 1_000) {
  const name = `offline-identity-${crypto.randomUUID()}`;
  names.push(name);
  const database = await openOfflineNotesDatabase(name);
  databases.push(database);
  return { database, store: new OfflineIdentityStore(database, () => now) };
}

describe("OfflineIdentityStore", () => {
  it("stores only the identity fields needed to unlock Notes", async () => {
    const { database, store } = await setup();

    const identity = await store.recordAuthenticated(bootstrap("usr_a"));

    expect(identity).toEqual({
      userID: "usr_a",
      email: "usr_a@example.test",
      displayName: "Offline User",
      role: "member",
      permissions: { is_admin: false, can_use_notes: true },
      lastAuthenticatedAt: 1_000,
      offlineInitializedAt: 0,
    });
    const serialized = JSON.stringify(await database.getWorkspace("usr_a"));
    expect(serialized).not.toContain("secret-session-id");
    expect(serialized).not.toContain("home-secret");
    expect(serialized).not.toContain("agent-secret");
    expect(serialized.toLowerCase()).not.toContain("csrf");
    expect(serialized.toLowerCase()).not.toContain("password");
  });

  it("expires offline eligibility after 30 days", async () => {
    let now = 1_000;
    const name = `offline-identity-expiry-${crypto.randomUUID()}`;
    names.push(name);
    const database = await openOfflineNotesDatabase(name);
    databases.push(database);
    const store = new OfflineIdentityStore(database, () => now);
    await store.recordAuthenticated(bootstrap("usr_a"));
    const workspace = (await database.getWorkspace("usr_a"))!;
    await database.putWorkspace({
      ...workspace,
      identity: { ...workspace.identity, offlineInitializedAt: now },
      lastReconciledAt: now,
    });

    now += 30 * 24 * 60 * 60 * 1_000 + 1;

    expect(await store.restoreEligible()).toBeNull();
    expect(await database.getWorkspace("usr_a")).toBeDefined();
  });

  it("restores only the active user and purges that partition on sign-out", async () => {
    const { database, store } = await setup();
    await store.recordAuthenticated(bootstrap("usr_a"));
    const workspace = (await database.getWorkspace("usr_a"))!;
    await database.putWorkspace({
      ...workspace,
      identity: { ...workspace.identity, offlineInitializedAt: 1_000 },
      lastReconciledAt: 1_000,
    });
    await database.putNote({
      userID: "usr_a", localKey: "daily", serverNoteID: "daily.md",
      serverRevision: "1", baseRevision: "1", title: "Daily", bodyMarkdown: "",
      pageType: "text", parentLocalKey: "", pinned: false, mcpExcluded: false,
      board: null, attachments: [], createdLocally: false, state: "clean", error: "",
      createdAt: 1, updatedAt: 1,
    });

    expect((await store.restoreEligible())?.userID).toBe("usr_a");
    await store.purge("usr_a");

    expect(await store.restoreEligible()).toBeNull();
    expect(await database.listNotes("usr_a")).toEqual([]);
  });

  it("does not unlock an offline workspace before its first successful reconciliation", async () => {
    const { store } = await setup();
    await store.recordAuthenticated(bootstrap("usr_a"));

    expect(await store.restoreEligible()).toBeNull();
  });
});
