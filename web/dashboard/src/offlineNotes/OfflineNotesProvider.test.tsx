import "fake-indexeddb/auto";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { BootstrapState } from "../api/bootstrap";
import { openOfflineNotesDatabase } from "./database";
import { OfflineNotesProvider, useOfflineNotes, type OfflineRuntimeCoordinator } from "./OfflineNotesProvider";
import type { OfflineSyncSummary } from "./types";

const databaseNames: string[] = [];

afterEach(async () => {
  cleanup();
  for (const name of databaseNames.splice(0)) {
    await new Promise<void>((resolve, reject) => {
      const request = indexedDB.deleteDatabase(name);
      request.onsuccess = () => resolve();
      request.onerror = () => reject(request.error);
    });
  }
});

function bootstrap(userID = "usr_a"): BootstrapState {
  return {
    user: {
      id: userID, email: `${userID}@example.test`, display_name: "Offline User",
      password_change_required: false, created_at: "", updated_at: "",
    },
    session: { id: "session-secret", expires_at: "" },
    home: null,
    membership: { home_id: "", user_id: userID, role: "member", created_at: "", updated_at: "" },
    permissions: {
      is_admin: false, can_manage_people: false, can_manage_settings: false,
      can_use_homeassistant: false, can_use_files: false, can_use_notes: true,
      can_use_assistant: false, can_view_storage: false, can_manage_apps: false,
    },
    agent: null,
    setup_status: { first_setup_visible: false },
    features: { mcp_enabled: false },
    server: { version: "test" },
    navigation: [],
  };
}

class FakeCoordinator implements OfflineRuntimeCoordinator {
  started = 0;
  stopped = 0;
  listeners = new Set<(summary: OfflineSyncSummary) => void>();

  start() { this.started += 1; }
  stop() { this.stopped += 1; }
  async syncNow() {
    const summary = {
      pending: 0, syncing: 0, failed: 0, conflicted: 0,
      authenticationRequired: false, lastSyncedAt: 100,
    };
    for (const listener of this.listeners) listener(summary);
    return summary;
  }
  subscribe(listener: (summary: OfflineSyncSummary) => void) {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  requireAuthentication() {
    const summary = {
      pending: 1, syncing: 0, failed: 0, conflicted: 0,
      authenticationRequired: true, lastSyncedAt: 0,
    };
    for (const listener of this.listeners) listener(summary);
  }
}

function Probe() {
  const runtime = useOfflineNotes();
  return (
    <div>
      <output data-testid="mode">{runtime.mode}</output>
      <output data-testid="user">{runtime.activeUserID}</output>
      <output data-testid="repository">{String(Boolean(runtime.repository))}</output>
      <output data-testid="last-sync">{runtime.sync.lastSyncedAt}</output>
      <button type="button" onClick={() => void runtime.activateAuthenticated(bootstrap())}>Activate online</button>
      <button type="button" onClick={() => {
        const denied = bootstrap();
        denied.permissions.can_use_notes = false;
        void runtime.activateAuthenticated(denied);
      }}>Revoke Notes</button>
      <button type="button" onClick={() => void runtime.activateOffline()}>Activate offline</button>
      <button type="button" onClick={() => void runtime.syncNow()}>Sync now</button>
      <button type="button" onClick={() => void runtime.purgeActive()}>Purge</button>
    </div>
  );
}

describe("OfflineNotesProvider", () => {
  it("activates one coordinator and publishes sync state", async () => {
    const name = `provider-${crypto.randomUUID()}`;
    databaseNames.push(name);
    const coordinator = new FakeCoordinator();
    render(
      <OfflineNotesProvider databaseName={name} createCoordinator={() => coordinator}>
        <Probe />
      </OfflineNotesProvider>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Activate online" }));
    await waitFor(() => expect(screen.getByTestId("mode")).toHaveTextContent("online"));
    expect(screen.getByTestId("user")).toHaveTextContent("usr_a");
    expect(screen.getByTestId("repository")).toHaveTextContent("true");
    expect(coordinator.started).toBe(1);

    fireEvent.click(screen.getByRole("button", { name: "Sync now" }));
    await waitFor(() => expect(screen.getByTestId("last-sync")).toHaveTextContent("100"));
  });

  it("restores the eligible active user in a later provider", async () => {
    const name = `provider-restore-${crypto.randomUUID()}`;
    databaseNames.push(name);
    const firstCoordinator = new FakeCoordinator();
    const first = render(
      <OfflineNotesProvider databaseName={name} createCoordinator={() => firstCoordinator}>
        <Probe />
      </OfflineNotesProvider>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Activate online" }));
    await waitFor(() => expect(screen.getByTestId("mode")).toHaveTextContent("online"));
    const database = await openOfflineNotesDatabase(name);
    const workspace = (await database.getWorkspace("usr_a"))!;
    await database.putWorkspace({
      ...workspace,
      identity: { ...workspace.identity, offlineInitializedAt: Date.now() },
      lastReconciledAt: Date.now(),
    });
    database.close();
    first.unmount();
    expect(firstCoordinator.stopped).toBe(1);

    render(
      <OfflineNotesProvider databaseName={name} createCoordinator={() => new FakeCoordinator()}>
        <Probe />
      </OfflineNotesProvider>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Activate offline" }));

    await waitFor(() => expect(screen.getByTestId("mode")).toHaveTextContent("offline"));
    expect(screen.getByTestId("user")).toHaveTextContent("usr_a");
  });

  it("purges before returning to inactive mode", async () => {
    const name = `provider-purge-${crypto.randomUUID()}`;
    databaseNames.push(name);
    const coordinator = new FakeCoordinator();
    render(
      <OfflineNotesProvider databaseName={name} createCoordinator={() => coordinator}>
        <Probe />
      </OfflineNotesProvider>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Activate online" }));
    await waitFor(() => expect(screen.getByTestId("mode")).toHaveTextContent("online"));

    fireEvent.click(screen.getByRole("button", { name: "Purge" }));

    await waitFor(() => expect(screen.getByTestId("mode")).toHaveTextContent("inactive"));
    expect(screen.getByTestId("user")).toBeEmptyDOMElement();
    expect(coordinator.stopped).toBe(1);
  });

  it("locks but retains cached Notes when current authorization revokes Notes access", async () => {
    const name = `provider-revoked-${crypto.randomUUID()}`;
    databaseNames.push(name);
    const coordinator = new FakeCoordinator();
    render(
      <OfflineNotesProvider databaseName={name} createCoordinator={() => coordinator}>
        <Probe />
      </OfflineNotesProvider>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Activate online" }));
    await waitFor(() => expect(screen.getByTestId("mode")).toHaveTextContent("online"));
    const database = await openOfflineNotesDatabase(name);
    await database.putNote({
      userID: "usr_a", localKey: "server:draft.md", serverNoteID: "draft.md",
      serverRevision: "rev-1", baseRevision: "rev-1", title: "Queued draft", bodyMarkdown: "Keep me",
      pageType: "text", parentLocalKey: "", pinned: false, mcpExcluded: false, board: null,
      attachments: [], createdLocally: false, state: "queued", error: "", createdAt: 1, updatedAt: 2,
    });

    fireEvent.click(screen.getByRole("button", { name: "Revoke Notes" }));

    await waitFor(() => expect(screen.getByTestId("mode")).toHaveTextContent("locked"));
    expect(screen.getByTestId("repository")).toHaveTextContent("false");
    expect(screen.getByTestId("user")).toBeEmptyDOMElement();
    expect(coordinator.stopped).toBe(1);
    expect((await database.listNotes("usr_a"))[0]).toMatchObject({ title: "Queued draft", state: "queued" });

    fireEvent.click(screen.getByRole("button", { name: "Activate online" }));
    await waitFor(() => expect(screen.getByTestId("mode")).toHaveTextContent("online"));
    expect(screen.getByTestId("repository")).toHaveTextContent("true");
    expect((await database.listNotes("usr_a"))[0]).toMatchObject({ title: "Queued draft", state: "queued" });
    database.close();
  });

  it("removes cached Notes from view after sync authentication fails and purges them on logout", async () => {
    const name = `provider-auth-lock-${crypto.randomUUID()}`;
    databaseNames.push(name);
    const coordinator = new FakeCoordinator();
    render(
      <OfflineNotesProvider databaseName={name} createCoordinator={() => coordinator}>
        <Probe />
      </OfflineNotesProvider>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Activate online" }));
    await waitFor(() => expect(screen.getByTestId("mode")).toHaveTextContent("online"));
    const database = await openOfflineNotesDatabase(name);
    await database.putNote({
      userID: "usr_a", localKey: "server:private.md", serverNoteID: "private.md",
      serverRevision: "rev-1", baseRevision: "rev-1", title: "Private", bodyMarkdown: "Hidden after lock",
      pageType: "text", parentLocalKey: "", pinned: false, mcpExcluded: false, board: null,
      attachments: [], createdLocally: false, state: "queued", error: "", createdAt: 1, updatedAt: 2,
    });

    coordinator.requireAuthentication();

    await waitFor(() => expect(screen.getByTestId("mode")).toHaveTextContent("locked"));
    expect(screen.getByTestId("repository")).toHaveTextContent("false");
    expect((await database.listNotes("usr_a"))[0]?.title).toBe("Private");
    await waitFor(async () => expect(await database.getActiveUserID()).toBe(""));

    fireEvent.click(screen.getByRole("button", { name: "Purge" }));
    await waitFor(() => expect(screen.getByTestId("mode")).toHaveTextContent("inactive"));
    expect(await database.listNotes("usr_a")).toEqual([]);
    database.close();
  });
});
