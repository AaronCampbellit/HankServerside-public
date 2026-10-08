import "fake-indexeddb/auto";
import { cleanup, createEvent, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { openOfflineNotesDatabase, type OfflineNotesDatabase } from "../offlineNotes/database";
import { OfflineNotesRepository } from "../offlineNotes/repository";
import type { OfflineNotesContextValue } from "../offlineNotes/OfflineNotesProvider";
import type { OfflineIdentity, OfflineNoteRecord, OfflineWorkspaceRecord } from "../offlineNotes/types";
import { ConfirmDialogProvider, ToastProvider } from "../ui/primitives";
import { ProfileNotesPage } from "./ProfileNotesPage";

const profileNotesClient = vi.hoisted(() => ({
  listNotes: vi.fn(),
  listTrash: vi.fn(),
  restoreNote: vi.fn(),
  permanentlyDeleteNote: vi.fn(),
  fetchNote: vi.fn(),
  saveNote: vi.fn(),
  deleteNote: vi.fn(),
  uploadAttachment: vi.fn(),
  deleteAttachment: vi.fn(),
}));
const profileSettingsClient = vi.hoisted(() => ({
  load: vi.fn(async () => ({ revision: 0, settings: {} })),
  save: vi.fn(async (_revision: number, settings: Record<string, unknown>) => ({ revision: 1, settings })),
}));
const offlineNotesRuntime = vi.hoisted(() => ({
  current: null as OfflineNotesContextValue | null,
}));
const pwaRuntime = vi.hoisted(() => ({ online: true }));
const offlineDatabases: OfflineNotesDatabase[] = [];
const offlineDatabaseNames: string[] = [];

vi.mock("../api/profileNotes", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api/profileNotes")>();
  return {
    ...actual,
    profileNotesClient,
  };
});

vi.mock("../api/profileSettings", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api/profileSettings")>();
  return { ...actual, profileSettingsClient };
});

vi.mock("../offlineNotes/OfflineNotesProvider", () => ({
  useOfflineNotes: () => offlineNotesRuntime.current,
}));
vi.mock("../pwa/PWAProvider", () => ({
  usePWA: () => pwaRuntime,
}));

function renderPage() {
  return render(
    <ToastProvider>
      <ConfirmDialogProvider>
        <ProfileNotesPage />
      </ConfirmDialogProvider>
    </ToastProvider>,
  );
}

function activateOfflineRepository(repository: OfflineNotesRepository, userID: string) {
  offlineNotesRuntime.current = {
    mode: "offline",
    activeUserID: userID,
    repository,
    sync: { pending: 0, syncing: 0, failed: 0, conflicted: 0, authenticationRequired: false, lastSyncedAt: 0 },
    activateAuthenticated: vi.fn(),
    activateOffline: vi.fn(),
    syncNow: vi.fn(),
    purgeActive: vi.fn(),
  };
}

async function setupOfflinePage(records: OfflineNoteRecord[], defaultBoardLocalKey = "") {
  const databaseName = `profile-notes-local-${crypto.randomUUID()}`;
  const database = await openOfflineNotesDatabase(databaseName);
  offlineDatabases.push(database);
  offlineDatabaseNames.push(databaseName);
  const identity: OfflineIdentity = {
    userID: "usr_offline",
    email: "offline@example.test",
    displayName: "Offline",
    role: "member",
    permissions: { is_admin: false, can_use_notes: true },
    lastAuthenticatedAt: 1,
    offlineInitializedAt: 1,
  };
  await database.putWorkspace({
    userID: identity.userID,
    identity,
    profileSettingsRevision: 1,
    defaultBoardLocalKey,
    lastReconciledAt: 1,
    nextSequence: 1,
  });
  for (const record of records) await database.putNote(record);
  const repository = new OfflineNotesRepository(identity.userID, database, () => Date.now());
  activateOfflineRepository(repository, identity.userID);
  return repository;
}

describe("ProfileNotesPage", () => {
  afterEach(async () => {
    cleanup();
    vi.restoreAllMocks();
    vi.clearAllMocks();
    vi.unstubAllGlobals();
    vi.useRealTimers();
    window.localStorage.clear();
    window.history.replaceState({}, "", "/dashboard/profile-notes");
    Reflect.deleteProperty(document, "execCommand");
    offlineNotesRuntime.current = null;
    pwaRuntime.online = true;
    for (const database of offlineDatabases.splice(0)) database.close();
    for (const name of offlineDatabaseNames.splice(0)) {
      await new Promise<void>((resolve, reject) => {
        const request = indexedDB.deleteDatabase(name);
        request.onsuccess = () => resolve();
        request.onerror = () => reject(request.error);
      });
    }
  });

  function setupDeletionNote(parentID = "house") {
    const note = { note_id: "daily", title: "Daily", body_markdown: "Original", revision: "1", page_type: "text", parent_id: parentID, updated_at: "2026-09-01" };
    const parent = { note_id: "house", title: "House Notebook", revision: "1", page_type: "notebook" };
    profileNotesClient.listNotes.mockResolvedValue({ notes: parentID ? [note, parent] : [note] });
    profileNotesClient.fetchNote.mockImplementation(async (id: string) => id === "house" ? parent : note);
    profileNotesClient.deleteNote.mockResolvedValue({ ok: true });
  }

  async function confirmNoteDeletion() {
    fireEvent.click(screen.getByRole("button", { name: "Delete note" }));
    const confirmation = await screen.findByRole("alertdialog", { name: "Delete note" });
    fireEvent.click(within(confirmation).getByRole("button", { name: "Move to Trash" }));
  }

  it("cancels pending autosave and returns to the deleted note's notebook", async () => {
    setupDeletionNote();
    const page = renderPage();
    const body = await screen.findByLabelText("Note body");
    body.innerHTML = "Pending edit";
    fireEvent.input(body);
    await confirmNoteDeletion();
    await waitFor(() => expect(screen.getByLabelText("Note title")).toHaveValue("House Notebook"));
    expect(profileNotesClient.deleteNote).toHaveBeenCalledWith("daily", "1");
    expect(screen.queryByRole("button", { name: "Daily" })).not.toBeInTheDocument();
    await new Promise((resolve) => setTimeout(resolve, 850));
    page.unmount();
    expect(profileNotesClient.saveNote).not.toHaveBeenCalled();
  });

  it("waits for an active save, discards queued edits, and deletes the latest revision", async () => {
    setupDeletionNote();
    let finishSave!: (value: { note_id: string; revision: string }) => void;
    profileNotesClient.saveNote.mockImplementation(() => new Promise((resolve) => { finishSave = resolve; }));
    renderPage();
    const body = await screen.findByLabelText("Note body");
    body.innerHTML = "Saving edit";
    fireEvent.input(body);
    fireEvent.click(screen.getByRole("button", { name: "Save note" }));
    body.innerHTML = "Queued edit";
    fireEvent.input(body);
    fireEvent.click(screen.getByRole("button", { name: "Save note" }));
    await confirmNoteDeletion();
    expect(profileNotesClient.deleteNote).not.toHaveBeenCalled();
    finishSave({ note_id: "daily", revision: "2" });
    await waitFor(() => expect(profileNotesClient.deleteNote).toHaveBeenCalledWith("daily", "2"));
    await waitFor(() => expect(screen.getByLabelText("Note title")).toHaveValue("House Notebook"));
    expect(profileNotesClient.saveNote).toHaveBeenCalledTimes(1);
  });

  it("returns to the notes browser when deleting a note without a notebook", async () => {
    setupDeletionNote("");
    renderPage();
    await screen.findByLabelText("Note body");
    await confirmNoteDeletion();
    expect(await screen.findByLabelText("Notes browser")).toBeInTheDocument();
    expect(screen.queryByLabelText("Note title")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New note" })).toBeInTheDocument();
  });

  it("keeps the note and its edits open when deletion fails", async () => {
    setupDeletionNote();
    profileNotesClient.deleteNote.mockRejectedValue(new Error("Delete failed"));
    renderPage();
    const body = await screen.findByLabelText("Note body");
    body.innerHTML = "Keep this edit";
    fireEvent.input(body);
    await confirmNoteDeletion();
    expect(await screen.findByText("Delete failed")).toBeInTheDocument();
    expect(screen.getByLabelText("Note title")).toHaveValue("Daily");
    expect(screen.getByLabelText("Note body")).toHaveTextContent("Keep this edit");
    profileNotesClient.saveNote.mockResolvedValue({ note_id: "daily", revision: "2" });
    fireEvent.click(screen.getByRole("button", { name: "Save note" }));
    await waitFor(() => expect(profileNotesClient.saveNote).toHaveBeenCalled());
  });

  it("opens a recycling bin with restorable notes", async () => {
    profileNotesClient.listNotes.mockResolvedValue({ notes: [] });
    profileNotesClient.listTrash.mockResolvedValue({
      notes: [{ note_id: "old.md", title: "Old plan", deleted_at: "2026-08-30T12:00:00Z" }],
    });

    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "Trash" }));

    expect(await screen.findByRole("dialog", { name: "Trash" })).toBeInTheDocument();
    expect(screen.getByText("Old plan")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Restore" })).toBeInTheDocument();
    expect(profileNotesClient.listTrash).toHaveBeenCalledTimes(1);
  });

  it("loads and persists a cached note without using HTTP", async () => {
    const databaseName = `profile-notes-local-${crypto.randomUUID()}`;
    const database = await openOfflineNotesDatabase(databaseName);
    offlineDatabases.push(database);
    offlineDatabaseNames.push(databaseName);
    const identity: OfflineIdentity = {
      userID: "usr_offline",
      email: "offline@example.test",
      displayName: "Offline",
      role: "member",
      permissions: { is_admin: false, can_use_notes: true },
      lastAuthenticatedAt: 1,
      offlineInitializedAt: 1,
    };
    await database.putWorkspace({
      userID: identity.userID,
      identity,
      profileSettingsRevision: 1,
      defaultBoardLocalKey: "",
      lastReconciledAt: 1,
      nextSequence: 1,
    } satisfies OfflineWorkspaceRecord);
    await database.putNote({
      userID: identity.userID,
      localKey: "server:daily.md",
      serverNoteID: "daily.md",
      serverRevision: "rev-1",
      baseRevision: "rev-1",
      title: "Cached Daily",
      bodyMarkdown: "Cached body",
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
    } satisfies OfflineNoteRecord);
    const repository = new OfflineNotesRepository(identity.userID, database, () => 2);
    activateOfflineRepository(repository, identity.userID);

    const first = renderPage();
    expect(await screen.findByDisplayValue("Cached Daily")).toBeInTheDocument();
    const body = screen.getByLabelText("Note body");
    body.innerHTML = "Edited locally";
    fireEvent.input(body);
    fireEvent.click(screen.getByRole("button", { name: "Save note" }));
    expect(await screen.findByText("Saved offline")).toBeInTheDocument();
    expect(profileNotesClient.listNotes).not.toHaveBeenCalled();
    expect(profileNotesClient.fetchNote).not.toHaveBeenCalled();
    expect(profileNotesClient.saveNote).not.toHaveBeenCalled();

    first.unmount();
    renderPage();
    expect(await screen.findByDisplayValue("Cached Daily")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByLabelText("Note body")).toHaveTextContent("Edited locally"));

  });

  it("refreshes the selected editor to the server canonical note after a save conflict", async () => {
    const repository = await setupOfflinePage([{
      userID: "usr_offline", localKey: "server:daily.md", serverNoteID: "daily.md",
      serverRevision: "rev-1", baseRevision: "rev-1", title: "Daily", bodyMarkdown: "Original",
      pageType: "text", parentLocalKey: "", pinned: false, mcpExcluded: false, board: null,
      attachments: [], createdLocally: false, state: "clean", error: "", createdAt: 1, updatedAt: 1,
    }]);
    await repository.saveNote({
      note_id: "daily.md", title: "Daily local", body_markdown: "Local queued body",
      expected_revision: "rev-1", page_type: "text", parent_id: "", pinned: false,
      mcp_excluded: false, board: null,
    });
    renderPage();
    expect(await screen.findByDisplayValue("Daily local")).toBeInTheDocument();
    expect(screen.getByLabelText("Note body")).toHaveTextContent("Local queued body");
    const mutation = (await repository.pendingMutations())[0];
    expect(await repository.claimSyncLease("test-conflict", 100, 1_000)).toBe(true);
    const claimed = await repository.claimMutation(mutation.mutationID, "test-conflict", 100, 1_000);
    expect(claimed).not.toBeNull();

    await repository.resolveSaveConflict(claimed!, {
      note_id: "daily.md", title: "Daily server", body_markdown: "Canonical server body",
      revision: "rev-2", page_type: "text", parent_id: "", pinned: false,
      mcp_excluded: false, board: null, attachments: [], updated_at: "2026-08-18T00:00:00Z",
    }, "test-conflict", 100);

    expect(await screen.findByDisplayValue("Daily server")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByLabelText("Note body")).toHaveTextContent("Canonical server body"));
    expect(screen.getByLabelText("Note body")).not.toHaveTextContent("Local queued body");
    expect(screen.getByText(/Offline conflict/)).toBeInTheDocument();
    expect(screen.getByText("Conflict copy")).toBeInTheDocument();
  });

  it("persists offline Kanban edits, workflow flags, default selection, and deletion", async () => {
    const repository = await setupOfflinePage([
      {
        userID: "usr_offline",
        localKey: "server:house.md",
        serverNoteID: "house.md",
        serverRevision: "rev-house",
        baseRevision: "rev-house",
        title: "House",
        bodyMarkdown: "",
        pageType: "notebook",
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
      },
      {
        userID: "usr_offline",
        localKey: "server:work.md",
        serverNoteID: "work.md",
        serverRevision: "rev-work",
        baseRevision: "rev-work",
        title: "Work Board",
        bodyMarkdown: "",
        pageType: "kanban",
        parentLocalKey: "server:house.md",
        pinned: false,
        mcpExcluded: false,
        board: {
          columns: [
            { id: "inbox", title: "Inbox", sort_order: 0, cards: [{ id: "roof", text: "Review roof", sort_order: 0 }] },
            { id: "doing", title: "Doing", sort_order: 1, cards: [] },
          ],
        },
        attachments: [],
        createdLocally: false,
        state: "clean",
        error: "",
        createdAt: 2,
        updatedAt: 2,
      },
    ]);

    offlineNotesRuntime.current = { ...offlineNotesRuntime.current!, mode: "online" };
    pwaRuntime.online = false;
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Open task Review roof" }));
    expect(screen.getByLabelText("Add screenshot or file")).toBeDisabled();
    expect(screen.getByText("Available when connected")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Task title"), { target: { value: "Call roofer" } });
    fireEvent.click(screen.getByRole("button", { name: "Move task right" }));
    fireEvent.click(screen.getByRole("button", { name: "Close task details" }));
    fireEvent.click(screen.getByRole("button", { name: "Pin note" }));
    fireEvent.click(screen.getByRole("button", { name: "Exclude from MCP" }));
    fireEvent.click(screen.getByRole("button", { name: "Board options" }));
    fireEvent.click(screen.getByRole("button", { name: "Set as default board" }));

    await waitFor(async () => {
      const stored = await repository.getNoteRecord("server:work.md");
      expect(stored).toMatchObject({ pinned: true, mcpExcluded: true, state: "queued" });
      expect(stored?.board?.columns?.find((column) => column.id === "doing")?.cards?.[0]?.text).toBe("Call roofer");
      expect(await repository.getDefaultBoardLocalKey()).toBe("server:work.md");
    });
    expect(await screen.findByText("Saved offline")).toBeInTheDocument();
    expect(profileNotesClient.saveNote).not.toHaveBeenCalled();
    expect(profileSettingsClient.save).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Delete note" }));
    const confirmation = await screen.findByRole("alertdialog", { name: "Delete note" });
    fireEvent.click(within(confirmation).getByRole("button", { name: "Move to Trash" }));

    await waitFor(async () => expect(await repository.getNoteRecord("server:work.md")).toBeUndefined());
    expect(profileNotesClient.deleteNote).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.getByLabelText("Note title")).toHaveValue("House"));
  });

  it("creates a durable child note inside a cached notebook", async () => {
    const repository = await setupOfflinePage([{
      userID: "usr_offline",
      localKey: "server:house.md",
      serverNoteID: "house.md",
      serverRevision: "rev-house",
      baseRevision: "rev-house",
      title: "House Notebook",
      bodyMarkdown: "",
      pageType: "notebook",
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
    }]);

    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "New note in House Notebook" }));
    fireEvent.change(screen.getByLabelText("Note title"), { target: { value: "Roof plan" } });
    const body = screen.getByLabelText("Note body");
    body.innerHTML = "Call the roofer";
    fireEvent.input(body);
    fireEvent.click(screen.getByRole("button", { name: "Save note" }));

    await waitFor(async () => {
      const child = (await repository.listNoteRecords()).find((record) => record.title === "Roof plan");
      expect(child).toMatchObject({
        parentLocalKey: "server:house.md",
        bodyMarkdown: "Call the roofer",
        createdLocally: true,
        state: "queued",
      });
    });
    expect(await screen.findByText("Saved offline")).toBeInTheDocument();
    expect(profileNotesClient.saveNote).not.toHaveBeenCalled();
  });

  it("opens a cached note attachment while offline", async () => {
    const attachment = {
      id: "natt-roof",
      filename: "roof.png",
      content_type: "image/png",
      size_bytes: 5,
      download_url: "/attachments/natt-roof",
      markdown_reference: "![roof](hank-note-attachment://natt-roof)",
    };
    const repository = await setupOfflinePage([{
      userID: "usr_offline",
      localKey: "server:daily.md",
      serverNoteID: "daily.md",
      serverRevision: "rev-1",
      baseRevision: "rev-1",
      title: "Roof notes",
      bodyMarkdown: attachment.markdown_reference,
      pageType: "text",
      parentLocalKey: "",
      pinned: false,
      mcpExcluded: false,
      board: null,
      attachments: [attachment],
      createdLocally: false,
      state: "clean",
      error: "",
      createdAt: 1,
      updatedAt: 1,
    }]);
    await repository.cacheAttachment("server:daily.md", attachment, new Blob(["image"], { type: "image/png" }));
    vi.stubGlobal("URL", { ...URL, createObjectURL: vi.fn(() => "blob:roof-cache"), revokeObjectURL: vi.fn() });

    renderPage();

    expect(await screen.findByRole("link", { name: "Open roof.png" })).toHaveAttribute("href", "blob:roof-cache");
    expect(profileNotesClient.fetchNote).not.toHaveBeenCalled();
  });

  it("uses browser and editor modes for the mobile notes workflow", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [
        { note_id: "daily", title: "Daily", preview: "Today", page_type: "text" },
        { note_id: "ideas", title: "Ideas", preview: "Later", page_type: "text" },
      ],
    });
    profileNotesClient.fetchNote.mockImplementation(async (id: string) => ({
      note_id: id,
      title: id === "ideas" ? "Ideas" : "Daily",
      body_markdown: id === "ideas" ? "Build something" : "Plan today",
      revision: "1",
      page_type: "text",
    }));

    renderPage();

    const layout = await screen.findByTestId("notes-mobile-workspace");
    expect(layout).toHaveAttribute("data-mobile-pane", "browser");

    fireEvent.click(screen.getByRole("button", { name: "Ideas" }));
    await waitFor(() => expect(layout).toHaveAttribute("data-mobile-pane", "editor"));
    expect(screen.getByRole("button", { name: "Back to notes" })).toBeInTheDocument();
    const formatting = screen.getByRole("button", { name: "More formatting" });
    expect(formatting).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(formatting);
    expect(formatting).toHaveAttribute("aria-expanded", "true");

    fireEvent.click(screen.getByRole("button", { name: "Back to notes" }));
    expect(layout).toHaveAttribute("data-mobile-pane", "browser");

    fireEvent.click(screen.getByRole("button", { name: "New note" }));
    expect(layout).toHaveAttribute("data-mobile-pane", "editor");
  });

  it("renders the redesigned editor chrome and kanban/notebook states", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [
        { note_id: "grocery", title: "Grocery List", preview: "Tortillas, ground beef, salsa", page_type: "text" },
        { note_id: "projects", title: "Home Projects", preview: "Kanban · 3 columns", page_type: "kanban" },
        { note_id: "house", title: "House Notebook", preview: "4 notes", page_type: "notebook" },
      ],
    });
    profileNotesClient.fetchNote.mockImplementation(async (id: string) => ({
      note_id: id,
      title: id === "projects" ? "Home Projects" : "Grocery List",
      body_markdown: id === "projects" ? "# Home Projects\n\n## To do\n- Repaint porch\n\n## Doing\n- Order furnace filter" : "- [x] Tortillas\n- [ ] Salsa",
      revision: "1",
      page_type: id === "projects" ? "kanban" : "text",
    }));

    renderPage();

    expect(await screen.findByRole("button", { name: "Home Projects" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New notebook" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Bold" })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Home Projects" }));

    expect(await screen.findByText("To do")).toBeInTheDocument();
    expect(screen.getByText("Doing")).toBeInTheDocument();
    expect(screen.queryByText("Done")).not.toBeInTheDocument();
  });

  it("autosaves interactive kanban changes with board data", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [{ note_id: "work", title: "Client Work", preview: "Kanban", page_type: "kanban", revision: "1" }],
    });
    profileNotesClient.fetchNote.mockResolvedValue({
      note_id: "work",
      title: "Client Work",
      body_markdown: "# Client Work\n\n## Inbox\n- Review brief\n\n## Done",
      revision: "1",
      page_type: "kanban",
      board: {
        columns: [
          { id: "inbox", title: "Inbox", sort_order: 0, cards: [{ id: "brief", text: "Review brief", sort_order: 0 }] },
          { id: "done", title: "Done", sort_order: 1, cards: [] },
        ],
      },
    });
    profileNotesClient.saveNote.mockResolvedValue({ note_id: "work", revision: "2" });

    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Add task to Inbox" }));
    fireEvent.change(screen.getByLabelText("Task title"), { target: { value: "Prepare invoice" } });
    vi.useFakeTimers();
    fireEvent.click(screen.getByRole("button", { name: "Create task" }));
    await vi.advanceTimersByTimeAsync(750);

    expect(profileNotesClient.saveNote).toHaveBeenCalledWith(expect.objectContaining({
      note_id: "work",
      page_type: "kanban",
      board: expect.objectContaining({
        columns: expect.arrayContaining([
          expect.objectContaining({ cards: expect.arrayContaining([expect.objectContaining({ text: "Prepare invoice" })]) }),
        ]),
      }),
    }));
  });

  it("sets and clears the default board without replacing unrelated profile settings", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [{ note_id: "work", title: "Client Work", preview: "Kanban", page_type: "kanban", revision: "1" }],
    });
    profileNotesClient.fetchNote.mockResolvedValue({
      note_id: "work", title: "Client Work", body_markdown: "# Client Work\n\n## Inbox", revision: "1", page_type: "kanban",
      board: { columns: [{ id: "inbox", title: "Inbox", sort_order: 0, cards: [] }] },
    });
    profileSettingsClient.load.mockResolvedValue({ revision: 7, settings: { dashboard: { density: "compact" }, assistant: { model: "gpt" } } });
    profileSettingsClient.save
      .mockImplementationOnce(async (_revision: number, settings: Record<string, unknown>) => ({ revision: 8, settings }))
      .mockImplementationOnce(async (_revision: number, settings: Record<string, unknown>) => ({ revision: 9, settings }));

    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Board options" }));
    fireEvent.click(await screen.findByRole("button", { name: "Set as default board" }));

    await waitFor(() => expect(profileSettingsClient.save).toHaveBeenNthCalledWith(1, 7, {
      dashboard: { density: "compact" }, assistant: { model: "gpt" }, kanban_default_board_id: "work",
    }));
    fireEvent.click(await screen.findByRole("button", { name: "Board options" }));
    fireEvent.click(await screen.findByRole("button", { name: "Clear default board" }));
    await waitFor(() => expect(profileSettingsClient.save).toHaveBeenNthCalledWith(2, 8, {
      dashboard: { density: "compact" }, assistant: { model: "gpt" },
    }));
  });

  it("autosaves the canonical attachment reference after a kanban screenshot paste", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [{ note_id: "work", title: "Client Work", preview: "Kanban", page_type: "kanban", revision: "1" }],
    });
    profileNotesClient.fetchNote.mockResolvedValue({
      note_id: "work",
      title: "Client Work",
      body_markdown: "# Client Work\n\n## Inbox\n- Review brief",
      revision: "1",
      page_type: "kanban",
      board: { columns: [{ id: "inbox", title: "Inbox", sort_order: 0, cards: [{ id: "brief", text: "Review brief", sort_order: 0 }] }] },
      attachments: [],
    });
    profileNotesClient.uploadAttachment.mockResolvedValue({
      id: "natt-1",
      filename: "wireframe.png",
      content_type: "image/png",
      download_url: "/v1/me/notes/work/attachments/natt-1",
      markdown_reference: "![wireframe.png](hank-note-attachment://natt-1?filename=wireframe.png&scope=profile)",
      note_revision: "2",
    });
    profileNotesClient.saveNote.mockResolvedValue({ note_id: "work", revision: "3" });

    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Open task Review brief" }));
    fireEvent.click(screen.getByRole("button", { name: "Edit description" }));
    vi.useFakeTimers();
    const file = new File(["image"], "wireframe.png", { type: "image/png" });
    const description = screen.getByLabelText("Description");
    const paste = createEvent.paste(description, {
      bubbles: true,
      cancelable: true,
      clipboardData: { items: [{ kind: "file", type: "image/png", getAsFile: () => file }] },
    });
    fireEvent(description, paste);
    await vi.advanceTimersByTimeAsync(750);

    expect(profileNotesClient.uploadAttachment).toHaveBeenCalledWith("work", file);
    expect(profileNotesClient.saveNote).toHaveBeenCalledWith(expect.objectContaining({
      note_id: "work",
      expected_revision: "2",
      board: expect.objectContaining({
        columns: [expect.objectContaining({
          cards: [expect.objectContaining({ text: expect.stringContaining("hank-note-attachment://natt-1") })],
        })],
      }),
    }));
  });

  it("saves a task deletion before permanently deleting its exclusive attachment", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [{ note_id: "work", title: "Client Work", preview: "Kanban", page_type: "kanban", revision: "1" }],
    });
    profileNotesClient.fetchNote.mockResolvedValue({
      note_id: "work",
      title: "Client Work",
      body_markdown: "# Client Work\n\n## Inbox\n- Review brief",
      revision: "1",
      page_type: "kanban",
      board: { columns: [{ id: "inbox", title: "Inbox", sort_order: 0, cards: [{ id: "brief", text: "Review brief\n![wireframe](hank-note-attachment://natt-1)", sort_order: 0 }] }] },
      attachments: [{
        id: "natt-1",
        filename: "wireframe.png",
        content_type: "image/png",
        size_bytes: 2_048,
        download_url: "/v1/me/notes/work/attachments/natt-1",
        markdown_reference: "![wireframe.png](hank-note-attachment://natt-1)",
      }],
    });
    profileNotesClient.saveNote.mockResolvedValue({ note_id: "work", revision: "2" });
    profileNotesClient.deleteAttachment.mockResolvedValue({ ok: true, note_revision: "3", cleanup_complete: true });

    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Open task Review brief" }));
    fireEvent.click(screen.getByRole("button", { name: "Delete task" }));
    const confirmation = await screen.findByRole("alertdialog", { name: "Delete from board" });
    expect(within(confirmation).getByText(/wireframe.png/)).toBeInTheDocument();
    fireEvent.click(within(confirmation).getByRole("button", { name: "Delete" }));

    await waitFor(() => expect(profileNotesClient.deleteAttachment).toHaveBeenCalledWith("work", "natt-1"));
    expect(profileNotesClient.saveNote.mock.invocationCallOrder[0]).toBeLessThan(profileNotesClient.deleteAttachment.mock.invocationCallOrder[0]);
    expect(profileNotesClient.saveNote).toHaveBeenCalledWith(expect.objectContaining({
      expected_revision: "1",
      board: expect.objectContaining({ columns: [expect.objectContaining({ cards: [] })] }),
    }));
    expect(screen.queryByRole("button", { name: "Open task Review brief" })).not.toBeInTheDocument();
  });

  it("keeps the task and attachment when the board deletion save fails", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [{ note_id: "work", title: "Client Work", preview: "Kanban", page_type: "kanban", revision: "1" }],
    });
    profileNotesClient.fetchNote.mockResolvedValue({
      note_id: "work",
      title: "Client Work",
      body_markdown: "# Client Work\n\n## Inbox\n- Review brief",
      revision: "1",
      page_type: "kanban",
      board: { columns: [{ id: "inbox", title: "Inbox", sort_order: 0, cards: [{ id: "brief", text: "Review brief", sort_order: 0 }] }] },
      attachments: [],
    });
    profileNotesClient.saveNote.mockRejectedValue(new Error("Save failed"));

    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Open task Review brief" }));
    fireEvent.click(screen.getByRole("button", { name: "Delete task" }));
    fireEvent.click(await screen.findByRole("button", { name: "Delete" }));

    await waitFor(() => expect(profileNotesClient.saveNote).toHaveBeenCalled());
    expect(profileNotesClient.deleteAttachment).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Open task Review brief" })).toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "Task details" })).toBeInTheDocument();
  });

  it("matches the guide notes anatomy with rich toolbar, notebook dialog, rendered text page, and kanban controls", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [
        { note_id: "grocery", title: "Grocery List", preview: "Tortillas, ground beef, salsa", page_type: "text" },
        { note_id: "projects", title: "Home Projects", preview: "Kanban · 3 columns", page_type: "kanban" },
        { note_id: "house", title: "House Notebook", preview: "No pages yet", page_type: "notebook" },
      ],
    });
    profileNotesClient.fetchNote.mockImplementation(async (id: string) => ({
      note_id: id,
      title: id === "projects" ? "Home Projects" : id === "house" ? "House Notebook" : "Grocery List",
      body_markdown: id === "projects"
        ? "# Home Projects\n\n## To do\n- Re-caulk the bathroom\n- Repaint the porch railing\n\n## Doing\n- Clean the gutters\n\n## Done\n- Order furnace filter"
        : "# Taco Night\n- [x] Tortillas\n- [x] Ground beef\n- [ ] Salsa (medium)\n- [ ] Avocados x4\n# Notes\nMaya is bringing dessert. Pick up the order from #print-shop before 5pm.",
      revision: "1",
      page_type: id === "projects" ? "kanban" : id === "house" ? "notebook" : "text",
    }));

    renderPage();

    expect(await screen.findByRole("heading", { level: 1, name: "Notes" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Collapse notes rail" })).toBeInTheDocument();
    const creationActions = screen.getByRole("group", { name: "Create note or notebook" });
    const creationButtons = within(creationActions).getAllByRole("button");
    expect(creationButtons.map((button) => button.getAttribute("aria-label"))).toEqual(["New notebook", "New note"]);
    expect(creationButtons[0]).toHaveClass("notes-new-note");
    expect(creationButtons[1]).toHaveClass("notes-new-note");
    expect(creationButtons[0].querySelector('path[d="M12 5v14M5 12h14"]')).not.toBeNull();
    expect(document.querySelector(".notes-title-kind")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Notebook filter")).not.toBeInTheDocument();
    const search = screen.getByPlaceholderText("Search notes").closest(".notes-search");
    expect(search?.compareDocumentPosition(creationActions) ?? 0).toBe(Node.DOCUMENT_POSITION_FOLLOWING);

    const editorTools = screen.getByLabelText("Editor tools");
    for (const label of ["Delete note", "Undo", "Redo", "Bold", "Italic", "Underline", "Smaller heading", "Heading", "Larger heading", "Bulleted list", "Numbered list", "Text page", "Kanban page", "Tag", "Link"]) {
      expect(within(editorTools).getByRole("button", { name: label })).toBeInTheDocument();
    }
    fireEvent.click(within(editorTools).getByRole("button", { name: "Delete note" }));
    expect(await screen.findByRole("alertdialog", { name: "Delete note" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));

    const body = await screen.findByLabelText("Note body");
    expect(body).toHaveAttribute("contenteditable", "true");
    expect(body).toHaveTextContent("Taco Night");
    expect(body).toHaveTextContent("Tortillas");
    expect(body).toHaveTextContent("Maya is bringing dessert");
    expect(body).not.toHaveTextContent("# Taco Night");
    // The note is a single editable surface — no read-only rendered duplicate.
    expect(screen.queryByLabelText("Rendered note body")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Home Projects" }));
    expect(await screen.findByText("Re-caulk the bathroom")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add card" })).not.toBeInTheDocument();
    const kanbanEditorTools = screen.getByLabelText("Editor tools");
    for (const label of ["Bold", "Italic", "Underline", "Smaller heading", "Heading", "Larger heading", "Bulleted list", "Numbered list"]) {
      expect(within(kanbanEditorTools).queryByRole("button", { name: label })).not.toBeInTheDocument();
    }
    fireEvent.click(screen.getByRole("button", { name: "Open task Re-caulk the bathroom" }));
    const cardFormatting = screen.getByLabelText("Description formatting");
    for (const label of ["Bold", "Italic", "Underline", "Smaller heading", "Heading", "Larger heading", "Bulleted list", "Numbered list", "Link"]) {
      expect(within(cardFormatting).getByRole("button", { name: label })).toBeInTheDocument();
    }
    fireEvent.click(screen.getByRole("button", { name: "Close task details" }));

    fireEvent.click(screen.getByRole("button", { name: "Open notebook House Notebook" }));
    expect(await screen.findByText("No pages in this notebook yet.")).toBeInTheDocument();
    expect(screen.queryByText("Paint colors")).not.toBeInTheDocument();
    expect(screen.queryByText(/shared with 2 people/i)).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "New note" }));
    expect(screen.getByLabelText("Note title")).toHaveValue("");
    expect(screen.queryByText("# Untitled")).not.toBeInTheDocument();
    expect(screen.queryByText("2m ago")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "New notebook" }));
    const dialog = screen.getByRole("dialog", { name: "New notebook" });
    expect(within(dialog).getByLabelText("Notebook name")).toBeInTheDocument();
    expect(within(dialog).queryByLabelText("Share with all home members")).not.toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Create notebook" })).toBeInTheDocument();
  });

  it("opens notebook child pages from the notebook section", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [
        { note_id: "house", title: "House Notebook", preview: "1 page", page_type: "notebook" },
        { note_id: "roof", title: "Roof Warranty", preview: "Expires 2027", page_type: "text", parent_id: "house" },
      ],
    });
    profileNotesClient.fetchNote.mockImplementation(async (id: string) => ({
      note_id: id,
      title: id === "house" ? "House Notebook" : "Roof Warranty",
      body_markdown: id === "house" ? "" : "# Roof Warranty\nExpires in 2027.",
      revision: "1",
      page_type: id === "house" ? "notebook" : "text",
      parent_id: id === "roof" ? "house" : "",
    }));

    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: /Open Roof Warranty/i }));

    expect(await screen.findByDisplayValue("Roof Warranty")).toBeInTheDocument();
    expect(screen.getByLabelText("Note body")).toHaveTextContent("Roof Warranty");
    expect(profileNotesClient.fetchNote).toHaveBeenCalledWith("roof");

    fireEvent.click(screen.getByRole("button", { name: "Open notebook House Notebook" }));
    fireEvent.click(await screen.findByRole("button", { name: "Open Roof Warranty" }));

    expect(await screen.findByDisplayValue("Roof Warranty")).toBeInTheDocument();
    expect(screen.getByLabelText("Note body")).toHaveTextContent("Roof Warranty");
  });

  it("keeps root notes and recently opened notebook pages in the default navigation for 12 hours", async () => {
    const now = Date.parse("2026-07-31T12:00:00Z");
    const nowSpy = vi.spyOn(Date, "now").mockReturnValue(now);
    window.localStorage.clear();
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [
        { note_id: "house", title: "House Notebook", preview: "1 page", page_type: "notebook" },
        { note_id: "roof", title: "Roof Warranty", preview: "Expires 2027", page_type: "text", parent_id: "house" },
        { note_id: "daily", title: "Daily Notes", preview: "Remember milk", page_type: "text" },
      ],
    });
    profileNotesClient.fetchNote.mockImplementation(async (id: string) => ({
      note_id: id,
      title: id === "house" ? "House Notebook" : id === "roof" ? "Roof Warranty" : "Daily Notes",
      body_markdown: id === "house" ? "" : id === "roof" ? "# Roof Warranty\nExpires in 2027." : "Remember milk",
      revision: "1",
      page_type: id === "house" ? "notebook" : "text",
      parent_id: id === "roof" ? "house" : "",
    }));

    const page = renderPage();
    const noteCards = await screen.findByLabelText("Note cards");
    expect(within(noteCards).getByRole("button", { name: "Daily Notes" })).toBeInTheDocument();
    expect(within(noteCards).queryByRole("button", { name: "Roof Warranty" })).not.toBeInTheDocument();

    fireEvent.click(await screen.findByRole("button", { name: "Open Roof Warranty" }));
    expect(await screen.findByDisplayValue("Roof Warranty")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Open notebook House Notebook" }));

    expect(within(screen.getByLabelText("Note cards")).getByRole("button", { name: "Roof Warranty" })).toBeInTheDocument();

    page.unmount();
    nowSpy.mockReturnValue(now + 13 * 60 * 60 * 1000);
    renderPage();

    expect(await screen.findByLabelText("Note cards")).toBeInTheDocument();
    expect(within(screen.getByLabelText("Note cards")).queryByRole("button", { name: "Roof Warranty" })).not.toBeInTheDocument();
    nowSpy.mockRestore();
    window.localStorage.clear();
  });

  it("keeps notebooks and notes reachable from the collapsible rail", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [
        { note_id: "house", title: "House Notebook", preview: "1 page", page_type: "notebook" },
        { note_id: "roof", title: "Roof Warranty", preview: "Expires 2027", page_type: "text", parent_id: "house" },
      ],
    });
    profileNotesClient.fetchNote.mockImplementation(async (id: string) => ({
      note_id: id,
      title: id === "house" ? "House Notebook" : "Roof Warranty",
      body_markdown: id === "house" ? "" : "# Roof Warranty\nExpires in 2027.",
      revision: "1",
      page_type: id === "house" ? "notebook" : "text",
      parent_id: id === "roof" ? "house" : "",
    }));

    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "Open Roof Warranty" }));
    fireEvent.click(screen.getByRole("button", { name: "Open notebook House Notebook" }));
    await waitFor(() => expect(within(screen.getByLabelText("Note cards")).getByRole("button", { name: "Roof Warranty" })).toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: "Collapse notes rail" }));

    expect(screen.getByRole("button", { name: "Expand notes rail" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Open notebook House Notebook" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Open note Roof Warranty" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Open note Roof Warranty" }));

    expect(await screen.findByDisplayValue("Roof Warranty")).toBeInTheDocument();
    expect(await screen.findByLabelText("Note body")).toHaveTextContent("Roof Warranty");
    fireEvent.click(screen.getByRole("button", { name: "Expand notes rail" }));
    expect(screen.getByRole("button", { name: "Collapse notes rail" })).toBeInTheDocument();
  });

  it("keeps notebook organization in the navigation instead of duplicating it in the editor header", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [
        { note_id: "daily", title: "Daily Notes", preview: "Remember milk", page_type: "text" },
      ],
    });
    profileNotesClient.fetchNote.mockResolvedValue({
      note_id: "daily",
      title: "Daily Notes",
      body_markdown: "# Daily\nRemember milk",
      revision: "1",
      page_type: "text",
    });

    renderPage();

    expect(await screen.findByRole("heading", { name: "Notebooks" })).toBeInTheDocument();
    expect(screen.getByText("No notebooks yet.")).toBeInTheDocument();
    expect(screen.queryByLabelText("Notebook filter")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Notebook")).not.toBeInTheDocument();
  });

  it("creates child notes from the notebook panel without a duplicate notebook filter", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [
        { note_id: "house", title: "House Notebook", preview: "1 page", page_type: "notebook" },
        { note_id: "roof", title: "Roof Warranty", preview: "Expires 2027", page_type: "text", parent_id: "house" },
        { note_id: "daily", title: "Daily Notes", preview: "Remember milk", page_type: "text" },
      ],
    });
    profileNotesClient.fetchNote.mockImplementation(async (id: string) => ({
      note_id: id,
      title: id === "house" ? "House Notebook" : id === "roof" ? "Roof Warranty" : "Daily Notes",
      body_markdown: id === "house" ? "" : id === "roof" ? "# Roof Warranty\nExpires in 2027." : "# Daily\nRemember milk",
      revision: "1",
      page_type: id === "house" ? "notebook" : "text",
      parent_id: id === "roof" ? "house" : "",
    }));

    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "Open notebook House Notebook" }));
    fireEvent.click(await screen.findByRole("button", { name: "New note in House Notebook" }));

    expect(screen.getByLabelText("Note title")).toHaveValue("");
    fireEvent.change(screen.getByLabelText("Note title"), { target: { value: "Paint colors" } });
    fireEvent.click(screen.getByRole("button", { name: "Save note" }));
    await waitFor(() => expect(profileNotesClient.saveNote).toHaveBeenCalledWith(expect.objectContaining({
      title: "Paint colors",
      parent_id: "house",
    })));
  });

  it("opens the exact note targeted by global search", async () => {
    window.history.replaceState({}, "", "/dashboard/profile-notes?note=roof");
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [
        { note_id: "daily", title: "Daily Notes", preview: "Remember milk", page_type: "text" },
        { note_id: "roof", title: "Roof Warranty", preview: "Expires 2027", page_type: "text" },
      ],
    });
    profileNotesClient.fetchNote.mockImplementation(async (id: string) => ({
      note_id: id,
      title: id === "roof" ? "Roof Warranty" : "Daily Notes",
      body_markdown: id === "roof" ? "Expires in 2027." : "Remember milk",
      revision: "1",
      page_type: "text",
    }));

    renderPage();

    expect(await screen.findByDisplayValue("Roof Warranty")).toBeInTheDocument();
    expect(profileNotesClient.fetchNote).toHaveBeenCalledWith("roof");
  });

  it("sorts pinned notebook notes first and synchronizes pin controls", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [
        { note_id: "house", title: "House Notebook", preview: "3 pages", page_type: "notebook", updated_at: "2026-07-31T09:00:00Z" },
        { note_id: "alpha", title: "Alpha", preview: "First", page_type: "text", parent_id: "house", pinned: false, updated_at: "2026-07-31T12:00:00Z" },
        { note_id: "pinned", title: "Pinned Guide", preview: "Important", page_type: "text", parent_id: "house", pinned: true, updated_at: "2026-07-31T10:00:00Z" },
        { note_id: "beta", title: "Beta", preview: "Second", page_type: "text", parent_id: "house", pinned: false, updated_at: "2026-07-31T11:00:00Z" },
        { note_id: "daily", title: "Daily", preview: "Root", page_type: "text", pinned: false, updated_at: "2026-07-31T08:00:00Z" },
      ],
    });
    profileNotesClient.fetchNote.mockImplementation(async (id: string) => ({
      note_id: id,
      title: id === "alpha" ? "Alpha" : id === "beta" ? "Beta" : id === "pinned" ? "Pinned Guide" : id === "house" ? "House Notebook" : "Daily",
      body_markdown: id === "house" ? "" : `Body for ${id}`,
      revision: "1",
      page_type: id === "house" ? "notebook" : "text",
      parent_id: ["alpha", "beta", "pinned"].includes(id) ? "house" : "",
      pinned: id === "pinned",
      mcp_excluded: false,
    }));
    profileNotesClient.saveNote.mockResolvedValue({ note_id: "beta", revision: "2", updated_at: "2026-07-31T13:00:00Z" });

    renderPage();

    expect(await screen.findByRole("button", { name: "Pin note" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Pin Daily" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Open notebook House Notebook" }));
    const notebookPages = await screen.findByLabelText("Notebook pages");
    const notebookPageOrder = within(notebookPages).getAllByRole("button")
      .map((button) => button.getAttribute("aria-label"))
      .filter((label) => ["Open Pinned Guide", "Open Alpha", "Open Beta"].includes(label || ""));
    expect(notebookPageOrder).toEqual(["Open Pinned Guide", "Open Alpha", "Open Beta"]);
    fireEvent.click(within(notebookPages).getByRole("button", { name: "Open Beta" }));
    fireEvent.click(await screen.findByRole("button", { name: "Pin note" }));

    await waitFor(() => expect(profileNotesClient.saveNote).toHaveBeenCalledWith(expect.objectContaining({
      note_id: "beta",
      expected_revision: "1",
      parent_id: "house",
      pinned: true,
    })));
    fireEvent.click(screen.getByRole("button", { name: "Open notebook House Notebook" }));
    const updatedPages = await screen.findByLabelText("Notebook pages");
    const updatedOrder = within(updatedPages).getAllByRole("button")
      .map((button) => button.getAttribute("aria-label"))
      .filter((label) => ["Open Pinned Guide", "Open Alpha", "Open Beta"].includes(label || ""));
    expect(updatedOrder).toEqual(["Open Beta", "Open Pinned Guide", "Open Alpha"]);
  });

  it("keeps notebook ordering unchanged when pinning fails", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [
        { note_id: "house", title: "House Notebook", preview: "1 page", page_type: "notebook", updated_at: "2026-07-31T09:00:00Z" },
        { note_id: "roof", title: "Roof Warranty", preview: "Expires 2027", page_type: "text", parent_id: "house", pinned: false, updated_at: "2026-07-31T10:00:00Z" },
      ],
    });
    profileNotesClient.fetchNote.mockImplementation(async (id: string) => ({
      note_id: id,
      title: id === "house" ? "House Notebook" : "Roof Warranty",
      body_markdown: id === "house" ? "" : "Expires in 2027",
      revision: "1",
      page_type: id === "house" ? "notebook" : "text",
      parent_id: id === "roof" ? "house" : "",
      pinned: false,
      mcp_excluded: false,
    }));
    profileNotesClient.saveNote.mockRejectedValue(new Error("Pin save failed"));

    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Open notebook House Notebook" }));
    fireEvent.click(await screen.findByRole("button", { name: "Open Roof Warranty" }));
    fireEvent.click(await screen.findByRole("button", { name: "Pin note" }));

    expect(await screen.findByText("Pin save failed")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Pin note" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Unpin note" })).not.toBeInTheDocument();
  });

  it("lets people type into a text note and save the changed body", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [
        { note_id: "daily", title: "Daily Notes", preview: "Remember milk", page_type: "text", revision: "1" },
      ],
    });
    profileNotesClient.fetchNote.mockResolvedValue({
      note_id: "daily",
      title: "Daily Notes",
      body_markdown: "Remember milk",
      revision: "1",
      page_type: "text",
    });
    profileNotesClient.saveNote.mockResolvedValue({ note_id: "daily", revision: "2", updated_at: "2026-07-03T12:00:00Z" });

    renderPage();

    const body = await screen.findByLabelText("Note body");
    expect(body).toBeVisible();
    expect(body).not.toHaveClass("visually-hidden");

    body.innerHTML = "Remember milk<div>Call Sam</div>";
    fireEvent.input(body);
    fireEvent.click(screen.getByRole("button", { name: "Save note" }));

    expect(profileNotesClient.saveNote).toHaveBeenCalledWith({
      note_id: "daily",
      title: "Daily Notes",
      body_markdown: "Remember milk\nCall Sam",
      expected_revision: "1",
      page_type: "text",
      parent_id: "",
      mcp_excluded: false,
      pinned: false,
      board: undefined,
    });
  });

  it("keeps note row actions off-canvas until a one-second fine-pointer hover", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [
        { note_id: "daily", title: "Daily Notes", preview: "Remember milk", page_type: "text", revision: "1" },
      ],
    });
    profileNotesClient.fetchNote.mockResolvedValue({
      note_id: "daily",
      title: "Daily Notes",
      body_markdown: "Remember milk",
      revision: "1",
      page_type: "text",
    });
    vi.stubGlobal("matchMedia", vi.fn((query: string) => ({ matches: query.includes("pointer: fine") })));

    renderPage();

    const row = (await screen.findByRole("button", { name: "Daily Notes" })).closest(".notes-guide-row") as HTMLDivElement;
    vi.useFakeTimers();
    const scrollTo = vi.fn();
    row.scrollTo = scrollTo as unknown as typeof row.scrollTo;

    fireEvent.mouseEnter(row);
    expect(scrollTo).not.toHaveBeenCalled();
    vi.advanceTimersByTime(999);
    expect(scrollTo).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(scrollTo).toHaveBeenCalledWith({ left: row.scrollWidth, behavior: "smooth" });

    fireEvent.mouseLeave(row);
    expect(scrollTo).toHaveBeenCalledWith({ left: 0, behavior: "smooth" });
  });

  it("cancels a pending note-row reveal when hover ends early", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [{ note_id: "daily", title: "Daily Notes", preview: "Remember milk", page_type: "text", revision: "1" }],
    });
    profileNotesClient.fetchNote.mockResolvedValue({
      note_id: "daily", title: "Daily Notes", body_markdown: "Remember milk", revision: "1", page_type: "text",
    });
    vi.stubGlobal("matchMedia", vi.fn((query: string) => ({ matches: query.includes("pointer: fine") })));
    renderPage();
    const row = (await screen.findByRole("button", { name: "Daily Notes" })).closest(".notes-guide-row") as HTMLDivElement;
    vi.useFakeTimers();
    const scrollTo = vi.fn();
    row.scrollTo = scrollTo as unknown as typeof row.scrollTo;

    fireEvent.mouseEnter(row);
    vi.advanceTimersByTime(500);
    fireEvent.mouseLeave(row);
    vi.advanceTimersByTime(500);

    expect(scrollTo).toHaveBeenCalledTimes(1);
    expect(scrollTo).toHaveBeenCalledWith({ left: 0, behavior: "smooth" });
  });

  it("renders formatting while editing and stores markdown", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [
        { note_id: "daily", title: "Daily Notes", preview: "Remember milk", page_type: "text", revision: "1" },
      ],
    });
    profileNotesClient.fetchNote.mockResolvedValue({
      note_id: "daily",
      title: "Daily Notes",
      body_markdown: "**Remember** milk",
      revision: "1",
      page_type: "text",
    });
    profileNotesClient.saveNote.mockResolvedValue({ note_id: "daily", revision: "2", updated_at: "2026-07-03T12:00:00Z" });

    renderPage();

    const body = await screen.findByLabelText("Note body");
    expect(body).toHaveAttribute("contenteditable", "true");
    expect(within(body).getByText("Remember").tagName).toBe("STRONG");
    expect(body).toHaveTextContent("Remember milk");
    expect(body).not.toHaveTextContent("**Remember** milk");
    fireEvent.click(screen.getByRole("button", { name: "Save note" }));

    expect(profileNotesClient.saveNote).toHaveBeenCalledWith({
      note_id: "daily",
      title: "Daily Notes",
      body_markdown: "**Remember** milk",
      expected_revision: "1",
      page_type: "text",
      parent_id: "",
      mcp_excluded: false,
      pinned: false,
      board: undefined,
    });
  });

  it("applies text tools to the editor and supports undo/redo", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [
        { note_id: "daily", title: "Daily Notes", preview: "Remember milk", page_type: "text", revision: "1" },
      ],
    });
    profileNotesClient.fetchNote.mockResolvedValue({
      note_id: "daily",
      title: "Daily Notes",
      body_markdown: "Remember milk",
      revision: "1",
      page_type: "text",
    });

    renderPage();

    const body = await screen.findByLabelText("Note body");
    expect(screen.getByRole("button", { name: "Undo" })).toBeDisabled();

    fireEvent.click(screen.getByRole("button", { name: "Bold" }));
    expect(within(body).getByText("bold text").tagName).toBe("STRONG");

    fireEvent.click(screen.getByRole("button", { name: "Undo" }));
    expect(body).toHaveTextContent("Remember milk");
    expect(body).not.toHaveTextContent("bold text");

    fireEvent.click(screen.getByRole("button", { name: "Redo" }));
    expect(within(body).getByText("bold text").tagName).toBe("STRONG");

    fireEvent.click(screen.getByRole("button", { name: "Bulleted list" }));
    expect(body.querySelector("ul")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Heading" }));
    expect(body.querySelector("h1")).toBeInTheDocument();
  });

  it("undoes and redoes typing and deletion as separate action groups", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [{ note_id: "daily", title: "Daily Notes", preview: "Original", page_type: "text", revision: "1" }],
    });
    profileNotesClient.fetchNote.mockResolvedValue({
      note_id: "daily",
      title: "Daily Notes",
      body_markdown: "Original",
      revision: "1",
      page_type: "text",
    });

    renderPage();

    const body = await screen.findByLabelText("Note body");
    body.innerHTML = "Original text";
    fireEvent.input(body, { inputType: "insertText" });
    body.innerHTML = "Original tex";
    fireEvent.input(body, { inputType: "deleteContentBackward" });

    fireEvent.click(screen.getByRole("button", { name: "Undo" }));
    expect(body).toHaveTextContent("Original text");

    fireEvent.click(screen.getByRole("button", { name: "Undo" }));
    expect(body).toHaveTextContent("Original");

    fireEvent.click(screen.getByRole("button", { name: "Redo" }));
    expect(body).toHaveTextContent("Original text");
    fireEvent.click(screen.getByRole("button", { name: "Redo" }));
    expect(body).toHaveTextContent("Original tex");
  });

  it("records a rich editor command as one undo action when the browser also fires input", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [{ note_id: "daily", title: "Daily Notes", preview: "Original", page_type: "text", revision: "1" }],
    });
    profileNotesClient.fetchNote.mockResolvedValue({
      note_id: "daily",
      title: "Daily Notes",
      body_markdown: "Original",
      revision: "1",
      page_type: "text",
    });
    Object.defineProperty(document, "execCommand", {
      configurable: true,
      value: vi.fn(() => {
        const body = document.activeElement as HTMLElement;
        body.innerHTML += "<strong>bold text</strong>";
        body.dispatchEvent(new InputEvent("input", { bubbles: true, inputType: "formatBold" }));
        return true;
      }),
    });

    renderPage();
    const body = await screen.findByLabelText("Note body");

    fireEvent.click(screen.getByRole("button", { name: "Bold" }));
    expect(body).toHaveTextContent("bold text");
    fireEvent.click(screen.getByRole("button", { name: "Undo" }));
    expect(body).toHaveTextContent("Original");
    expect(screen.getByRole("button", { name: "Undo" })).toBeDisabled();
  });

  it("keeps exactly 50 undo and redo actions", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [{ note_id: "daily", title: "Daily Notes", preview: "Original", page_type: "text", revision: "1" }],
    });
    profileNotesClient.fetchNote.mockResolvedValue({
      note_id: "daily",
      title: "Daily Notes",
      body_markdown: "Original",
      revision: "1",
      page_type: "text",
    });

    renderPage();
    await screen.findByLabelText("Note body");

    for (let index = 0; index < 55; index++) {
      fireEvent.click(screen.getByRole("button", { name: "Bold" }));
    }

    const undo = screen.getByRole("button", { name: "Undo" });
    for (let index = 0; index < 50; index++) fireEvent.click(undo);
    expect(undo).toBeDisabled();

    const redo = screen.getByRole("button", { name: "Redo" });
    for (let index = 0; index < 50; index++) fireEvent.click(redo);
    expect(redo).toBeDisabled();
  }, 15_000);

  it("moves a note from the list into a selected notebook", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [
        { note_id: "house", title: "House Notebook", preview: "No pages yet", page_type: "notebook" },
        { note_id: "daily", title: "Daily Notes", preview: "Remember milk", page_type: "text", revision: "1" },
      ],
    });
    profileNotesClient.fetchNote.mockImplementation(async (id: string) => ({
      note_id: id,
      title: id === "house" ? "House Notebook" : "Daily Notes",
      body_markdown: id === "house" ? "" : "Remember milk",
      revision: "1",
      page_type: id === "house" ? "notebook" : "text",
      parent_id: "",
    }));
    profileNotesClient.saveNote.mockResolvedValue({ note_id: "daily", revision: "2", updated_at: "2026-07-03T12:00:00Z" });

    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "Move Daily Notes" }));
    const dialog = screen.getByRole("dialog", { name: "Move note" });
    fireEvent.change(within(dialog).getByLabelText("Move to notebook"), { target: { value: "house" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Move note" }));

    expect(profileNotesClient.fetchNote).toHaveBeenCalledWith("daily");
    await waitFor(() => expect(profileNotesClient.saveNote).toHaveBeenCalledWith({
      note_id: "daily",
      title: "Daily Notes",
      body_markdown: "Remember milk",
      expected_revision: "1",
      page_type: "text",
      parent_id: "house",
      mcp_excluded: false,
      pinned: false,
      board: undefined,
    }));
  });

  it("toggles MCP exclusion from the editor toolbar and sends the lock state in save payloads", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [
        { note_id: "daily", title: "Daily", preview: "Original", page_type: "text", revision: "1", mcp_excluded: false },
      ],
    });
    profileNotesClient.fetchNote.mockResolvedValue({
      note_id: "daily",
      title: "Daily",
      body_markdown: "Original",
      revision: "1",
      page_type: "text",
      parent_id: "",
      mcp_excluded: false,
    });
    profileNotesClient.saveNote
      .mockResolvedValueOnce({ note_id: "daily", revision: "2" })
      .mockResolvedValueOnce({ note_id: "daily", revision: "3" });

    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "Exclude from MCP" }));

    await waitFor(() => expect(profileNotesClient.saveNote).toHaveBeenNthCalledWith(1, {
      note_id: "daily",
      title: "Daily",
      body_markdown: "Original",
      expected_revision: "1",
      page_type: "text",
      parent_id: "",
      mcp_excluded: true,
      pinned: false,
      board: undefined,
    }));

    fireEvent.click(await screen.findByRole("button", { name: "Include in MCP" }));

    await waitFor(() => expect(profileNotesClient.saveNote).toHaveBeenNthCalledWith(2, {
      note_id: "daily",
      title: "Daily",
      body_markdown: "Original",
      expected_revision: "2",
      page_type: "text",
      parent_id: "",
      mcp_excluded: false,
      pinned: false,
      board: undefined,
    }));
  });

  it("shows inherited MCP exclusion copy for notes inside an excluded notebook", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [
        { note_id: "roof", title: "Roof Warranty", preview: "Expires 2027", page_type: "text", parent_id: "house", mcp_excluded: false, updated_at: "2026-07-10T12:00:00Z" },
        { note_id: "house", title: "House Notebook", preview: "Notebook", page_type: "notebook", mcp_excluded: true, updated_at: "2026-07-09T12:00:00Z" },
      ],
    });
    profileNotesClient.fetchNote.mockResolvedValue({
      note_id: "roof",
      title: "Roof Warranty",
      body_markdown: "# Roof Warranty\nExpires in 2027.",
      revision: "1",
      page_type: "text",
      parent_id: "house",
      mcp_excluded: false,
    });

    renderPage();

    expect(await screen.findByText("Excluded because its notebook is locked")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Exclude from MCP" })).toBeInTheDocument();
  });

  it("preserves edits made while an earlier save is still running", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [{ note_id: "daily", title: "Daily Notes", preview: "Original", page_type: "text", revision: "1" }],
    });
    profileNotesClient.fetchNote.mockResolvedValue({
      note_id: "daily",
      title: "Daily Notes",
      body_markdown: "Original",
      revision: "1",
      page_type: "text",
      parent_id: "",
    });
    let resolveSave!: (value: { note_id: string; revision: string }) => void;
    profileNotesClient.saveNote.mockReturnValue(new Promise((resolve) => { resolveSave = resolve; }));

    renderPage();

    const body = await screen.findByLabelText("Note body");
    body.innerHTML = "First edit";
    fireEvent.input(body);
    fireEvent.click(screen.getByRole("button", { name: "Save note" }));
    await waitFor(() => expect(profileNotesClient.saveNote).toHaveBeenCalledTimes(1));

    body.innerHTML = "Second edit";
    fireEvent.input(body);
    resolveSave({ note_id: "daily", revision: "2" });

    await waitFor(() => expect(body).toHaveTextContent("Second edit"));
    expect(body).not.toHaveTextContent("First edit");
    expect(screen.getByRole("button", { name: "Save note" })).toHaveTextContent("Unsaved");
  });

  it("autosaves the latest note body after 750ms of idle typing", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [{ note_id: "daily", title: "Daily", preview: "Original", page_type: "text", revision: "1" }],
    });
    profileNotesClient.fetchNote.mockResolvedValue({
      note_id: "daily",
      title: "Daily",
      body_markdown: "Original",
      revision: "1",
      page_type: "text",
      parent_id: "",
    });
    profileNotesClient.saveNote.mockResolvedValue({ note_id: "daily", revision: "2" });

    renderPage();
    const body = await screen.findByLabelText("Note body");
    vi.useFakeTimers();

    body.innerHTML = "First draft";
    fireEvent.input(body);
    await vi.advanceTimersByTimeAsync(400);
    body.innerHTML = "Latest draft";
    fireEvent.input(body);
    await vi.advanceTimersByTimeAsync(749);
    expect(profileNotesClient.saveNote).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(1);
    expect(profileNotesClient.saveNote).toHaveBeenCalledWith({
      note_id: "daily",
      title: "Daily",
      body_markdown: "Latest draft",
      expected_revision: "1",
      page_type: "text",
      parent_id: "",
      mcp_excluded: false,
      pinned: false,
      board: undefined,
    });
    expect(screen.queryByText("Note saved.")).not.toBeInTheDocument();
  });

  it("flushes a pending autosave before opening another note", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [
        { note_id: "first", title: "First", preview: "Original", page_type: "text", revision: "1", updated_at: "2026-07-10T12:00:00Z" },
        { note_id: "second", title: "Second", preview: "Second body", page_type: "text", revision: "1", updated_at: "2026-07-09T12:00:00Z" },
      ],
    });
    profileNotesClient.fetchNote.mockImplementation(async (id: string) => ({
      note_id: id,
      title: id === "first" ? "First" : "Second",
      body_markdown: id === "first" ? "Original" : "Second body",
      revision: "1",
      page_type: "text",
      parent_id: "",
    }));
    profileNotesClient.saveNote.mockResolvedValue({ note_id: "first", revision: "2" });

    renderPage();
    const body = await screen.findByLabelText("Note body");
    body.innerHTML = "Unsaved work";
    fireEvent.input(body);
    profileNotesClient.fetchNote.mockClear();

    fireEvent.click(screen.getByRole("button", { name: "Second" }));

    await waitFor(() => expect(profileNotesClient.saveNote).toHaveBeenCalledWith({
      note_id: "first",
      title: "First",
      body_markdown: "Unsaved work",
      expected_revision: "1",
      page_type: "text",
      parent_id: "",
      mcp_excluded: false,
      pinned: false,
      board: undefined,
    }));
    expect(profileNotesClient.saveNote.mock.invocationCallOrder[0]).toBeLessThan(profileNotesClient.fetchNote.mock.invocationCallOrder[0]);
  });

  it("flushes pending edits when leaving the Notes route", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [{ note_id: "daily", title: "Daily", preview: "Original", page_type: "text", revision: "1" }],
    });
    profileNotesClient.fetchNote.mockResolvedValue({
      note_id: "daily",
      title: "Daily",
      body_markdown: "Original",
      revision: "1",
      page_type: "text",
      parent_id: "",
    });
    profileNotesClient.saveNote.mockResolvedValue({ note_id: "daily", revision: "2" });

    const page = renderPage();
    const body = await screen.findByLabelText("Note body");
    body.innerHTML = "Save before leaving";
    fireEvent.input(body);

    page.unmount();

    expect(profileNotesClient.saveNote).toHaveBeenCalledWith({
      note_id: "daily",
      title: "Daily",
      body_markdown: "Save before leaving",
      expected_revision: "1",
      page_type: "text",
      parent_id: "",
      mcp_excluded: false,
      pinned: false,
      board: undefined,
    });
  });

  it("keeps queued saves for different notes while another save is running", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [
        { note_id: "first", title: "First", preview: "A", page_type: "text", revision: "1", updated_at: "2026-07-10T12:00:00Z" },
        { note_id: "second", title: "Second", preview: "B", page_type: "text", revision: "1", updated_at: "2026-07-09T12:00:00Z" },
      ],
    });
    profileNotesClient.fetchNote.mockImplementation(async (id: string) => ({
      note_id: id,
      title: id === "first" ? "First" : "Second",
      body_markdown: id === "first" ? "A" : "B",
      revision: "1",
      page_type: "text",
      parent_id: "",
    }));
    let resolveFirst!: (value: { note_id: string; revision: string }) => void;
    let resolveSecond!: (value: { note_id: string; revision: string }) => void;
    profileNotesClient.saveNote
      .mockReturnValueOnce(new Promise((resolve) => { resolveFirst = resolve; }))
      .mockReturnValueOnce(new Promise((resolve) => { resolveSecond = resolve; }))
      .mockResolvedValueOnce({ note_id: "second", revision: "2" });

    renderPage();
    const firstBody = await screen.findByLabelText("Note body");
    firstBody.innerHTML = "First save";
    fireEvent.input(firstBody);
    fireEvent.click(screen.getByRole("button", { name: "Save note" }));

    firstBody.innerHTML = "Latest first";
    fireEvent.input(firstBody);
    fireEvent.click(screen.getByRole("button", { name: "Second" }));
    const secondBody = await screen.findByLabelText("Note body");
    await waitFor(() => expect(secondBody).toHaveTextContent("B"));
    secondBody.innerHTML = "Latest second";
    fireEvent.input(secondBody);
    fireEvent.click(screen.getByRole("button", { name: "Save note" }));

    resolveFirst({ note_id: "first", revision: "2" });
    await waitFor(() => expect(profileNotesClient.saveNote).toHaveBeenCalledTimes(2));
    expect(profileNotesClient.saveNote).toHaveBeenNthCalledWith(2, expect.objectContaining({
      note_id: "first",
      body_markdown: "Latest first",
      expected_revision: "2",
    }));

    resolveSecond({ note_id: "first", revision: "3" });
    await waitFor(() => expect(profileNotesClient.saveNote).toHaveBeenCalledTimes(3));
    expect(profileNotesClient.saveNote).toHaveBeenNthCalledWith(3, expect.objectContaining({
      note_id: "second",
      body_markdown: "Latest second",
    }));
  });

  it("flushes the current note before creating a notebook", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [{ note_id: "daily", title: "Daily", preview: "Original", page_type: "text", revision: "1" }],
    });
    profileNotesClient.fetchNote.mockResolvedValue({
      note_id: "daily",
      title: "Daily",
      body_markdown: "Original",
      revision: "1",
      page_type: "text",
      parent_id: "",
    });
    profileNotesClient.saveNote
      .mockResolvedValueOnce({ note_id: "daily", revision: "2" })
      .mockResolvedValueOnce({ note_id: "family", revision: "1" });

    renderPage();
    const body = await screen.findByLabelText("Note body");
    body.innerHTML = "Dirty daily note";
    fireEvent.input(body);
    fireEvent.click(screen.getByRole("button", { name: "New notebook" }));
    fireEvent.change(screen.getByLabelText("Notebook name"), { target: { value: "Family" } });
    fireEvent.click(screen.getByRole("button", { name: "Create notebook" }));

    await waitFor(() => expect(profileNotesClient.saveNote).toHaveBeenCalledTimes(2));
    expect(profileNotesClient.saveNote).toHaveBeenNthCalledWith(1, expect.objectContaining({
      note_id: "daily",
      body_markdown: "Dirty daily note",
    }));
    expect(profileNotesClient.saveNote).toHaveBeenNthCalledWith(2, expect.objectContaining({
      note_id: "",
      title: "Family",
      page_type: "notebook",
    }));
  });

  it("does not queue an identical snapshot while that snapshot is saving", async () => {
    profileNotesClient.listNotes.mockResolvedValue({
      notes: [{ note_id: "daily", title: "Daily", preview: "Original", page_type: "text", revision: "1" }],
    });
    profileNotesClient.fetchNote.mockResolvedValue({
      note_id: "daily",
      title: "Daily",
      body_markdown: "Original",
      revision: "1",
      page_type: "text",
      parent_id: "",
    });
    let resolveSave!: (value: { note_id: string; revision: string }) => void;
    profileNotesClient.saveNote.mockReturnValue(new Promise((resolve) => { resolveSave = resolve; }));

    renderPage();
    const body = await screen.findByLabelText("Note body");
    body.innerHTML = "One snapshot";
    fireEvent.input(body);
    fireEvent.click(screen.getByRole("button", { name: "Save note" }));
    fireEvent.blur(screen.getByLabelText("Note title"));
    resolveSave({ note_id: "daily", revision: "2" });

    await waitFor(() => expect(screen.getByRole("button", { name: "Save note" })).toHaveTextContent("Saved"));
    expect(profileNotesClient.saveNote).toHaveBeenCalledTimes(1);
  });
});
