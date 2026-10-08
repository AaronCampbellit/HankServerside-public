import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConfirmDialogProvider, ToastProvider } from "../ui/primitives";
import { ApiError } from "../api/client";
import { FileServerPage } from "./FileServerPage";

const fileServerClient = vi.hoisted(() => ({
  list: vi.fn(),
  search: vi.fn(),
  listJobs: vi.fn(),
  listOwnerlessJobs: vi.fn(),
  clearFinishedJobs: vi.fn(),
  cancelJob: vi.fn(),
  reviewJobDismissal: vi.fn(),
  dismissJob: vi.fn(),
  subscribeToJobs: vi.fn(),
  onJobsChanged: vi.fn(),
  stat: vi.fn(),
  createDirectory: vi.fn(),
  rename: vi.fn(),
  move: vi.fn(),
  deleteItem: vi.fn(),
  setupDownload: vi.fn(),
  uploadFile: vi.fn(),
}));

const connectionsClient = vi.hoisted(() => ({
  listProfiles: vi.fn(),
}));

const agentsClient = vi.hoisted(() => ({
  listAgents: vi.fn(),
}));

vi.mock("../api/fileServer", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api/fileServer")>();
  return {
    ...actual,
    fileServerClient,
  };
});

vi.mock("../api/connections", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api/connections")>();
  return {
    ...actual,
    connectionsClient,
  };
});

vi.mock("../api/agents", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api/agents")>();
  return {
    ...actual,
    agentsClient,
  };
});

function mockDemoShares() {
  connectionsClient.listProfiles.mockResolvedValue({
    profiles: [
      {
        home_id: "home-demo",
        service_type: "smb",
        public_config_json: JSON.stringify({
          active_source_id: "hankdemo",
          shares: [
            { id: "hankdemo", name: "Hankdemo", host: "192.168.86.137", share: "Hankdemo", username: "Hankdemo" },
            { id: "hankdemo2", name: "Hankdemo2", host: "192.168.86.137", share: "Hankdemo2", username: "Hankdemo" },
          ],
        }),
        secret_version: 1,
        applied_version: 1,
        status: "healthy",
        updated_at: "2026-07-01T12:00:00Z",
        updated_by: "admin",
      },
    ],
  });
}

function renderPage(isAdmin = false) {
  return render(
    <ToastProvider>
      <ConfirmDialogProvider>
        <FileServerPage isAdmin={isAdmin} />
      </ConfirmDialogProvider>
    </ToastProvider>,
  );
}

describe("FileServerPage", () => {
  beforeEach(() => {
    agentsClient.listAgents.mockResolvedValue([]);
    fileServerClient.listJobs.mockResolvedValue([]);
 fileServerClient.listOwnerlessJobs.mockResolvedValue({jobs:[],next_cursor:""});
    fileServerClient.clearFinishedJobs.mockResolvedValue(0);
    fileServerClient.subscribeToJobs.mockResolvedValue({});
    fileServerClient.onJobsChanged.mockReturnValue(() => undefined);
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.clearAllMocks();
    window.history.pushState({}, "", "/dashboard/file-server");
  });

  it("starts in the file list on mobile instead of opening a default preview", async () => {
    vi.stubGlobal("matchMedia", vi.fn().mockImplementation((query: string) => ({
      matches: query === "(max-width: 760px)",
      media: query,
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })));
    mockDemoShares();
    fileServerClient.list.mockResolvedValue({
      path: "/",
      items: [{ path: "/readme.txt", name: "readme.txt", size: 120 }],
    });

    renderPage();

    expect(await screen.findByRole("button", { name: "readme.txt" })).toBeInTheDocument();
    expect(screen.queryByLabelText("File preview")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Select readme.txt" }));
    expect(screen.queryByLabelText("File preview")).not.toBeInTheDocument();
    expect(screen.getByText("1 selected")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "readme.txt" }));
    expect(screen.getByLabelText("File preview")).toBeInTheDocument();
  });

  it("keeps transfer activity visible at the bottom without a toolbar disclosure", async () => {
    mockDemoShares();
    fileServerClient.list.mockResolvedValue({ path: "/", items: [{ path: "/Photos", name: "Photos", is_directory: true }] });
    fileServerClient.listJobs.mockResolvedValue([{
      id: "job-1",
      operation: "download",
      status: "running",
      path: "/Photos/archive.zip",
      bytes_done: 10,
      bytes_total: 100,
      created_at: "2026-07-19T10:00:00Z",
      updated_at: "2026-07-19T10:00:01Z",
    }]);

    renderPage();

    expect(await screen.findByText("Download archive.zip")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /file activity/i })).not.toBeInTheDocument();
    expect(screen.getByRole("region", { name: "File activity" })).toHaveClass("file-activity-region");
  });

  it("supports grid/list switching and a dismissible preview pane", async () => {
    mockDemoShares();
    fileServerClient.list.mockResolvedValue({
      path: "/Media/Photos",
      items: [
        { path: "/Media/Photos", name: "Photos", is_directory: true },
        { path: "/Media/Photos/sunset-beach.jpg", name: "sunset-beach.jpg", size: 4404019, modified_at: "2026-06-14T16:47:00Z" },
        { path: "/Media/Photos/birthday-party.mp4", name: "birthday-party.mp4", size: 228589568, modified_at: "2026-06-11T09:12:00Z" },
      ],
    });

    renderPage();

    expect(await screen.findByRole("button", { name: "sunset-beach.jpg" })).toBeInTheDocument();
    const filesTable = screen.getByRole("table", { name: "Files" });
    const rows = within(filesTable).getAllByRole("row");
    expect(rows[1].querySelector('[data-label="Name"]')).not.toBeNull();
    expect(rows[1].querySelector('[data-label="Size"]')).not.toBeNull();
    expect(rows[1].querySelector('[data-label="Type"]')).not.toBeNull();
    expect(rows[1].querySelector('[data-label="Modified"]')).not.toBeNull();
    const gridButton = screen.getByRole("button", { name: "Grid" });
    expect(gridButton).toBeEnabled();
    fireEvent.click(gridButton);
    expect(screen.getByRole("button", { name: "List" })).toBeEnabled();
    expect(screen.getByLabelText("File grid")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Close preview" }));
    await waitFor(() => expect(screen.queryByLabelText("File preview")).not.toBeInTheDocument());
  });

  it("renders Markdown files in the preview pane", async () => {
    mockDemoShares();
    fileServerClient.list.mockResolvedValue({
      path: "/Docs",
      items: [{ path: "/Docs/readme.md", name: "readme.md", size: 120 }],
    });

    renderPage();

    const preview = await screen.findByLabelText("File preview");
    expect(within(preview).getByTitle("Preview readme.md")).toHaveAttribute(
      "src",
      "/v1/home/files/preview?source_id=hankdemo&path=%2FDocs%2Freadme.md",
    );
  });

  it("loads live SMB shares and sends the selected source id", async () => {
    mockDemoShares();
    fileServerClient.list.mockResolvedValue({
      path: "/Media/Photos",
      items: [
        { path: "/Media", name: "Media", is_directory: true },
        { path: "/Media/Photos", name: "Photos", is_directory: true },
        {
          path: "/Media/Photos/sunset-beach.jpg",
          name: "sunset-beach.jpg",
          size: 4404019,
          modified_at: "2026-06-14T16:47:00Z",
          owner: "Aaron D.",
          dimensions: "4032 x 3024",
        },
        { path: "/Media/Photos/reunion-clip.mp4", name: "reunion-clip.mp4", size: 228589568, modified_at: "2026-06-11T09:12:00Z" },
      ],
    });

    renderPage();

    expect(await screen.findByRole("heading", { name: "File Server" })).toHaveClass("visually-hidden");
    await waitFor(() => expect(fileServerClient.list).toHaveBeenCalledWith("/", "hankdemo", undefined));
    const shareButton = screen.getByRole("button", { name: /Hankdemo/i });
    expect(shareButton.closest(".file-guide-pathbar")).not.toBeNull();
    const newFolderButton = screen.getByRole("button", { name: "New folder" });
    const uploadButton = screen.getByRole("button", { name: "Upload" });
    expect(newFolderButton.closest(".file-guide-toolbar-cluster")).not.toBeNull();
    expect(uploadButton.closest(".file-guide-toolbar-cluster")).not.toBeNull();
    expect(uploadButton).toBeEnabled();
    expect(screen.queryByRole("button", { name: /file activity/i })).not.toBeInTheDocument();
    expect(screen.queryByText("nas-attic")).not.toBeInTheDocument();
    expect(screen.queryByText("backups-vault")).not.toBeInTheDocument();
    expect(screen.queryByText("1.85 GB")).not.toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "Transfers" })).toBeInTheDocument();
    expect(screen.getByText("No active transfers")).toBeInTheDocument();
    expect(screen.queryByText("1 active")).not.toBeInTheDocument();
    expect(screen.queryByText("4 photos uploaded")).not.toBeInTheDocument();

    fireEvent.click(shareButton);
    const shareMenu = screen.getByRole("menu", { name: "File shares" });
    expect(within(shareMenu).getByText("Hankdemo")).toBeInTheDocument();
    expect(within(shareMenu).getByText("Hankdemo2")).toBeInTheDocument();
    expect(within(shareMenu).getAllByText("//192.168.86.137/Hankdemo")).toHaveLength(1);
    expect(within(shareMenu).getAllByText("//192.168.86.137/Hankdemo2")).toHaveLength(1);

    fireEvent.click(within(shareMenu).getByRole("menuitem", { name: /Hankdemo2/i }));
    await waitFor(() => expect(fileServerClient.list).toHaveBeenLastCalledWith("/", "hankdemo2", undefined));

    const preview = screen.getByLabelText("File preview");
    expect(within(preview).getByText("sunset-beach.jpg")).toBeInTheDocument();
    expect(within(preview).getByText("/Media/Photos/sunset-beach.jpg")).toBeInTheDocument();
    expect(within(preview).getByRole("img", { name: "Preview sunset-beach.jpg" })).toHaveAttribute("src", "/v1/home/files/preview?source_id=hankdemo2&path=%2FMedia%2FPhotos%2Fsunset-beach.jpg");
    expect(within(preview).getByText("Dimensions")).toBeInTheDocument();
    expect(within(preview).getByText("4032 x 3024")).toBeInTheDocument();
    expect(within(preview).getByText("Owner")).toBeInTheDocument();
    expect(within(preview).getByText("Aaron D.")).toBeInTheDocument();
    expect(within(preview).getByRole("button", { name: "Download preview" })).toBeInTheDocument();
    expect(within(preview).getByRole("button", { name: "Rename preview" })).toBeInTheDocument();
    expect(within(preview).getByRole("button", { name: "Move preview" })).toBeInTheDocument();

    fireEvent.click(screen.getByLabelText("Select reunion-clip.mp4"));
    expect(screen.getByText("1 selected")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Download selected" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Move selected" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete selected" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Clear selection" })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "More actions for sunset-beach.jpg" }));
    const menu = screen.getByRole("menu", { name: "File actions" });
    expect(within(menu).getByRole("menuitem", { name: "Open" })).toBeInTheDocument();
    expect(within(menu).getByRole("menuitem", { name: "Rename" })).toBeInTheDocument();
    expect(within(menu).getByRole("menuitem", { name: "Move" })).toBeInTheDocument();
    expect(within(menu).getByRole("menuitem", { name: "Download" })).toBeInTheDocument();
    expect(within(menu).getByRole("menuitem", { name: "Delete" })).toBeInTheDocument();
  });

  it("shows primary host folders and routes worker shared folders to their owning agent", async () => {
    connectionsClient.listProfiles.mockResolvedValue({
      profiles: [{
        home_id: "home-demo",
        service_type: "smb",
        public_config_json: JSON.stringify({
          sources: [
            { id: "host-media", name: "Host Media", type: "local", root: "/srv/media", local_root_enabled: true },
            { id: "hankdemo", name: "Hankdemo", type: "smb", smb_host: "nas.local", smb_share: "Hankdemo", smb_enabled: true },
          ],
        }),
        secret_version: 1,
        applied_version: 1,
        status: "healthy",
        updated_at: "2026-07-13T12:00:00Z",
        updated_by: "admin",
      }],
    });
    agentsClient.listAgents.mockResolvedValue([
      { agent_id: "primary-1", name: "Home", status: "online", agent_type: "primary", capabilities: ["files.list"] },
      { agent_id: "worker-1", name: "Studio Mac", status: "online", agent_type: "worker", capabilities: ["files.read", "files.write"] },
    ]);
    fileServerClient.list.mockImplementation(async (_path: string, sourceID?: string, agentID?: string) => agentID === "worker-1"
      ? { path: "/", items: [{ path: "/shared-photo.jpg", name: "shared-photo.jpg", size: 42 }] }
      : { path: "/", items: [{ path: `/${sourceID || "default"}.txt`, name: `${sourceID || "default"}.txt`, size: 4 }] });
    fileServerClient.setupDownload.mockResolvedValue({ url: "/v1/file-transfers/download-worker" });
    fileServerClient.uploadFile.mockResolvedValue({ ok: true });

    renderPage();

    await waitFor(() => expect(fileServerClient.list).toHaveBeenCalledWith("/", "host-media", undefined));
    expect(screen.getByRole("button", { name: /Host Media/i })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Host Media/i }));
    const targets = screen.getByRole("menu", { name: "File shares" });
    expect(within(targets).getByRole("menuitem", { name: /Studio Mac/i })).toBeInTheDocument();
    expect(within(targets).getByText("/srv/media")).toBeInTheDocument();

    fireEvent.click(within(targets).getByRole("menuitem", { name: /Studio Mac/i }));
    await waitFor(() => expect(fileServerClient.list).toHaveBeenLastCalledWith("/", undefined, "worker-1"));

    const preview = await screen.findByLabelText("File preview");
    expect(within(preview).getByRole("img", { name: "Preview shared-photo.jpg" })).toHaveAttribute(
      "src",
      "/v1/home/files/preview?agent_id=worker-1&path=%2Fshared-photo.jpg",
    );
    fireEvent.click(within(preview).getByRole("button", { name: "Download preview" }));
    await waitFor(() => expect(fileServerClient.setupDownload).toHaveBeenCalledWith("/shared-photo.jpg", undefined, "worker-1"));

    const file = new File(["worker upload"], "worker.txt", { type: "text/plain" });
    fireEvent.change(screen.getByLabelText("Choose files to upload"), { target: { files: [file] } });
    await waitFor(() => expect(fileServerClient.uploadFile).toHaveBeenCalledWith(file, "/", undefined, "worker-1"));

    fireEvent.click(within(preview).getByRole("button", { name: "Move preview" }));
    expect(screen.queryByLabelText("Destination share")).not.toBeInTheDocument();
  });

  it("opens worker deep links on the targeted device", async () => {
    window.history.pushState({}, "", "/dashboard/file-server?agent_id=worker-1&path=/Photos");
    connectionsClient.listProfiles.mockResolvedValue({ profiles: [] });
    agentsClient.listAgents.mockResolvedValue([
      { agent_id: "worker-1", name: "Studio Mac", status: "online", agent_type: "worker", capabilities: ["files.read"] },
    ]);
    fileServerClient.list.mockResolvedValue({ path: "/Photos", items: [] });

    renderPage();

    await waitFor(() => expect(fileServerClient.list).toHaveBeenCalledWith("/Photos", undefined, "worker-1"));
    expect(screen.getByRole("button", { name: /Studio Mac/i })).toBeInTheDocument();
  });

  it("loads a folder deep link with its selected source", async () => {
    window.history.pushState({}, "", "/dashboard/file-server?source_id=hankdemo2&path=%2FMedia%2FPhotos");
    mockDemoShares();
    fileServerClient.list.mockResolvedValue({ path: "/Media/Photos", items: [] });

    renderPage();

    await waitFor(() => expect(fileServerClient.list).toHaveBeenCalledWith("/Media/Photos", "hankdemo2", undefined));
  });

  it("opens a file preview deep link from its parent folder", async () => {
    window.history.pushState({}, "", "/dashboard/file-server?source_id=hankdemo2&path=%2FMedia%2FPhotos%2Fsunset-beach.jpg&preview=1");
    mockDemoShares();
    fileServerClient.list.mockResolvedValue({
      path: "/Media/Photos",
      items: [
        { path: "/Media/Photos/first.jpg", name: "first.jpg", size: 10 },
        { path: "/Media/Photos/sunset-beach.jpg", name: "sunset-beach.jpg", size: 20 },
      ],
    });

    renderPage();

    await waitFor(() => expect(fileServerClient.list).toHaveBeenCalledWith("/Media/Photos", "hankdemo2", undefined));
    const preview = await screen.findByRole("complementary", { name: "File preview" });
    expect(within(preview).getByRole("img", { name: "Preview sunset-beach.jpg" })).toHaveAttribute(
      "src",
      "/v1/home/files/preview?source_id=hankdemo2&path=%2FMedia%2FPhotos%2Fsunset-beach.jpg",
    );
  });

  it("copies file and folder dashboard links", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    const originalClipboard = navigator.clipboard;
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    mockDemoShares();
    fileServerClient.list.mockResolvedValue({
      path: "/Media/Photos",
      items: [
        { path: "/Media/Photos/Albums", name: "Albums", is_directory: true },
        { path: "/Media/Photos/sunset-beach.jpg", name: "sunset-beach.jpg", size: 20 },
      ],
    });

    try {
      renderPage();

      fireEvent.click(await screen.findByRole("button", { name: "More actions for Albums" }));
      fireEvent.click(screen.getByRole("menuitem", { name: "Copy link" }));
      expect(writeText).toHaveBeenCalledWith(expect.stringContaining(
        "/dashboard/file-server?source_id=hankdemo&path=%2FMedia%2FPhotos%2FAlbums",
      ));

      fireEvent.click(screen.getByRole("button", { name: "More actions for sunset-beach.jpg" }));
      fireEvent.click(screen.getByRole("menuitem", { name: "Copy preview link" }));
      expect(writeText).toHaveBeenCalledWith(expect.stringContaining(
        "/dashboard/file-server?source_id=hankdemo&path=%2FMedia%2FPhotos%2Fsunset-beach.jpg&preview=1",
      ));
    } finally {
      Object.defineProperty(navigator, "clipboard", { configurable: true, value: originalClipboard });
    }
  });

  it("renders html files in a sandboxed preview iframe", async () => {
    window.history.pushState({}, "", "/dashboard/file-server?source_id=hankdemo2&path=%2FMedia%2FDocs%2Findex.html&preview=1");
    mockDemoShares();
    fileServerClient.list.mockResolvedValue({
      path: "/Media/Docs",
      items: [{ path: "/Media/Docs/index.html", name: "index.html", size: 120 }],
    });

    renderPage();

    const preview = await screen.findByRole("complementary", { name: "File preview" });
    const frame = within(preview).getByTitle("Preview index.html");
    expect(frame).toHaveAttribute("src", "/v1/home/files/preview?source_id=hankdemo2&path=%2FMedia%2FDocs%2Findex.html");
    expect(frame).toHaveAttribute("sandbox", "");
  });

  it("keeps the file pane mounted while opening folders and hides the root sidebar row", async () => {
    mockDemoShares();
    let resolveFolder!: (value: { path: string; items: Array<{ path: string; name: string; is_directory?: boolean; size?: number }> }) => void;
    const folderLoad = new Promise<{ path: string; items: Array<{ path: string; name: string; is_directory?: boolean; size?: number }> }>((resolve) => {
      resolveFolder = resolve;
    });
    fileServerClient.list.mockImplementation((path: string) => {
      if (path === "/Media") return folderLoad;
      return Promise.resolve({
        path: "/",
        items: [
          { path: "/Media", name: "Media", is_directory: true },
          { path: "/readme.txt", name: "readme.txt", size: 1200 },
        ],
      });
    });

    renderPage();

    expect(await screen.findByRole("button", { name: "readme.txt" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Root/i })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Open Media" }));

    expect(screen.queryByRole("heading", { name: "Loading files" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "readme.txt" })).toBeInTheDocument();
    expect(screen.getByText("Opening /Media")).toBeInTheDocument();
    await waitFor(() => expect(fileServerClient.list).toHaveBeenCalledWith("/Media", "hankdemo", undefined));

    resolveFolder({
      path: "/Media",
      items: [{ path: "/Media/photo.jpg", name: "photo.jpg", size: 2400 }],
    });

    expect(await screen.findByRole("button", { name: "photo.jpg" })).toBeInTheDocument();
    expect(screen.queryByText("Opening /Media")).not.toBeInTheDocument();
  });

  it("searches the selected share instead of filtering only the open folder", async () => {
    mockDemoShares();
    fileServerClient.list.mockResolvedValue({
      path: "/Current",
      items: [{ path: "/Current/readme.txt", name: "readme.txt", size: 4 }],
    });
    fileServerClient.search.mockResolvedValue({
      items: [{ path: "/Archive/2024/needle.pdf", name: "needle.pdf", size: 42 }],
    });

    renderPage();

    fireEvent.change(await screen.findByLabelText("Search files"), { target: { value: "needle" } });

    await waitFor(() => expect(fileServerClient.search).toHaveBeenCalledWith("needle", "hankdemo", undefined, expect.any(AbortSignal)));
    expect(await screen.findByRole("button", { name: "needle.pdf" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "readme.txt" })).not.toBeInTheDocument();
  });

  it("shows partial indexed results without claiming the share has no matches", async () => {
    mockDemoShares();
    fileServerClient.list.mockResolvedValue({ path: "/", items: [] });
    fileServerClient.search.mockResolvedValue({ items: [], status: "indexing" });
    renderPage();
    fireEvent.change(await screen.findByLabelText("Search files"), { target: { value: "invoice" } });
    expect(await screen.findByText("No indexed matches yet")).toBeInTheDocument();
    expect(screen.getByText("Indexing this share; results are still arriving.")).toBeInTheDocument();
  });

  it("clears search results before switching to another share", async () => {
    mockDemoShares();
    fileServerClient.list
      .mockResolvedValueOnce({
        path: "/",
        items: [{ path: "/needle-local.txt", name: "needle-local.txt", size: 4 }],
      })
      .mockReturnValueOnce(new Promise(() => undefined));
    fileServerClient.search.mockResolvedValue({
      items: [{ path: "/Archive/needle.pdf", name: "needle.pdf", size: 42 }],
    });

    renderPage();

    fireEvent.change(await screen.findByLabelText("Search files"), { target: { value: "needle" } });
    expect(await screen.findByRole("button", { name: "needle.pdf" })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /Hankdemo/i }));
    fireEvent.click(within(screen.getByRole("menu", { name: "File shares" })).getByRole("menuitem", { name: /Hankdemo2/i }));

    expect(screen.queryByRole("button", { name: "needle.pdf" })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Select needle.pdf")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "needle-local.txt" })).not.toBeInTheDocument();
  });

  it("shows active and recently completed file transfer jobs", async () => {
    mockDemoShares();
    fileServerClient.list.mockResolvedValue({
      path: "/Media/Photos",
      items: [
        { path: "/Media/Photos/sunset-beach.jpg", name: "sunset-beach.jpg", size: 4404019, modified_at: "2026-06-14T16:47:00Z" },
      ],
    });
    fileServerClient.listJobs.mockResolvedValue([
      {
        id: "filejob_upload",
        operation: "upload",
        from_path: "/Media/Photos/family.jpg",
        status: "running",
        bytes_done: 512,
        bytes_total: 1024,
        updated_at: "2026-07-03T12:00:00Z",
      },
      {
        id: "filejob_move",
        operation: "move",
        from_path: "/Media/Photos/sunset-beach.jpg",
        to_path: "/Media/Archive/sunset-beach.jpg",
        status: "completed",
        files_done: 1,
        files_total: 1,
        completed_at: "2026-07-03T12:01:00Z",
      },
      {
        id: "filejob_download",
        operation: "download",
        from_path: "/Media/Photos/report.pdf",
        status: "failed",
        error_message: "agent offline",
        updated_at: "2026-07-03T12:02:00Z",
      },
    ]);

    renderPage();

    expect(await screen.findByRole("heading", { name: "Transfers" })).toBeInTheDocument();
    expect(screen.getByText("1 active")).toBeInTheDocument();
    expect(screen.getByText("Upload family.jpg")).toBeInTheDocument();
    expect(screen.getByText("512 B of 1 KB")).toBeInTheDocument();
    expect(screen.getByText("Move sunset-beach.jpg")).toBeInTheDocument();
    expect(screen.getByText("Download report.pdf")).toBeInTheDocument();
    expect(screen.getByText("agent offline")).toBeInTheDocument();
    expect(fileServerClient.subscribeToJobs).toHaveBeenCalled();
    expect(fileServerClient.onJobsChanged).toHaveBeenCalled();
  });

  it("shows parent-folder context instead of duplicating the open folder", async () => {
    window.history.pushState({}, "", "/dashboard/file-server?source_id=hankdemo&path=%2FBackups");
    mockDemoShares();
    fileServerClient.list.mockImplementation(async (path: string) => path === "/Backups"
      ? {
          path,
          items: [
            { path: "/Backups/dump", name: "dump", is_directory: true },
            { path: "/Backups/archive.zip", name: "archive.zip", size: 20 },
          ],
        }
      : {
          path: "/",
          items: [
            { path: "/Backups", name: "Backups", is_directory: true },
            { path: "/Photos", name: "Photos", is_directory: true },
          ],
        });

    renderPage();

    const folders = await screen.findByRole("complementary", { name: "Folders" });
    expect(within(folders).getByRole("button", { name: "Open Backups" })).toHaveAttribute("aria-current", "page");
    expect(within(folders).getByRole("button", { name: "Open Photos" })).toBeInTheDocument();
    expect(within(folders).queryByRole("button", { name: "Open dump" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "dump" })).toBeInTheDocument();
  });

  it("opens a folder without waiting for its parent listing", async () => {
    window.history.pushState({}, "", "/dashboard/file-server?source_id=hankdemo&path=%2FBackups");
    mockDemoShares();
    fileServerClient.list.mockImplementation((path: string) => path === "/Backups"
      ? Promise.resolve({ path, items: [{ path: "/Backups/report.pdf", name: "report.pdf" }] })
      : new Promise(() => undefined));

    renderPage();

    expect(await screen.findByRole("button", { name: "report.pdf" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Open Home" })).toBeInTheDocument();
    expect(screen.queryByText("Opening /Backups")).not.toBeInTheDocument();
  });

  it("recovers the file list after a listing timeout", async () => {
    mockDemoShares();
    fileServerClient.list.mockRejectedValueOnce(new Error("files.list timed out"))
      .mockResolvedValue({ path: "/", items: [{ path: "/ready.txt", name: "ready.txt" }] });

    renderPage();

    expect(await screen.findByText("files.list timed out")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try loading files again" }));
    expect(await screen.findByRole("button", { name: "ready.txt" })).toBeInTheDocument();
  });

  it("shows deep file search results after a complete search", async () => {
    mockDemoShares();
    fileServerClient.list.mockResolvedValue({ path: "/", items: [{ path: "/Taxes", name: "Taxes", is_directory: true }] });
    let finishSearch: (value: { items: { path: string; name: string }[] }) => void = () => undefined;
    fileServerClient.search.mockReturnValue(new Promise((resolve) => { finishSearch = resolve; }));

    renderPage();
    await screen.findByRole("button", { name: "Taxes" });
    fireEvent.change(screen.getByPlaceholderText("Search files"), { target: { value: "report" } });
    expect(screen.getByRole("status")).toHaveTextContent("Searching this share");
    expect(screen.queryByRole("button", { name: "Taxes" })).not.toBeInTheDocument();

    await waitFor(() => expect(fileServerClient.search).toHaveBeenCalledWith("report", "hankdemo", undefined, expect.any(AbortSignal)));
    finishSearch({ items: [{ path: "/Taxes/2025/report.pdf", name: "report.pdf" }] });
    expect(await screen.findByRole("button", { name: "report.pdf" })).toBeInTheDocument();
    expect(screen.queryByText("Searching this share...")).not.toBeInTheDocument();
  });

  it("offers a retry when recursive search times out", async () => {
    mockDemoShares();
    fileServerClient.list.mockResolvedValue({ path: "/", items: [] });
    fileServerClient.search.mockRejectedValueOnce(new Error("files.search timed out"))
      .mockResolvedValue({ items: [{ path: "/deep/needle.txt", name: "needle.txt" }] });

    renderPage();
    await screen.findByPlaceholderText("Search files");
    fireEvent.change(screen.getByPlaceholderText("Search files"), { target: { value: "needle" } });

    expect(await screen.findByText("Search could not finish")).toBeInTheDocument();
    expect(screen.queryByText("No files match your search")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try search again" }));
    expect(await screen.findByRole("button", { name: "needle.txt" })).toBeInTheDocument();
  });

  it("opens a folder while search is running and ignores the late search response", async () => {
    mockDemoShares();
    let finishSearch: (value: { items: { path: string; name: string }[] }) => void = () => undefined;
    fileServerClient.search.mockReturnValue(new Promise((resolve) => { finishSearch = resolve; }));
    fileServerClient.list.mockImplementation((path: string) => Promise.resolve(path === "/Photos"
      ? { path, items: [{ path: "/Photos/headshot.jpg", name: "headshot.jpg" }] }
      : { path: "/", items: [{ path: "/Photos", name: "Photos", is_directory: true }] }));

    renderPage();
    await screen.findByRole("button", { name: "Photos" });
    fireEvent.change(screen.getByPlaceholderText("Search files"), { target: { value: "headshot" } });
    await waitFor(() => expect(fileServerClient.search).toHaveBeenCalled());
    fireEvent.click(screen.getByRole("button", { name: "Open Photos" }));

    expect(await screen.findByRole("button", { name: "headshot.jpg" })).toBeInTheDocument();
    expect(screen.getByPlaceholderText("Search files")).toHaveValue("");
    await act(async () => { finishSearch({ items: [{ path: "/Other/stale.jpg", name: "stale.jpg" }] }); });
    expect(screen.queryByRole("button", { name: "stale.jpg" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "headshot.jpg" })).toBeInTheDocument();
  });

  it("recovers normal browsing after a search error", async () => {
    mockDemoShares();
    fileServerClient.list.mockImplementation((path: string) => Promise.resolve(path === "/Photos"
      ? { path, items: [{ path: "/Photos/headshot.jpg", name: "headshot.jpg" }] }
      : { path: "/", items: [{ path: "/Photos", name: "Photos", is_directory: true }] }));
    fileServerClient.search.mockRejectedValue(new Error("unreadable folder"));

    renderPage();
    await screen.findAllByRole("button", { name: "Open Photos" });
    fireEvent.change(screen.getByPlaceholderText("Search files"), { target: { value: "headshot" } });
    expect(await screen.findByText("Search could not finish")).toBeInTheDocument();
    fireEvent.click(screen.getAllByRole("button", { name: "Open Photos" })[0]);

    expect(await screen.findByRole("button", { name: "headshot.jpg" })).toBeInTheDocument();
    expect(screen.queryByText("Search could not finish")).not.toBeInTheDocument();
    expect(screen.queryByText("unreadable folder")).not.toBeInTheDocument();
  });

  it("keeps the newest folder when an earlier folder request finishes late", async () => {
    mockDemoShares();
    let finishFirst: (value: { path: string; items: { path: string; name: string }[] }) => void = () => undefined;
    fileServerClient.list.mockImplementation((path: string) => {
      if (path === "/First") return new Promise((resolve) => { finishFirst = resolve; });
      if (path === "/Second") return Promise.resolve({ path, items: [{ path: "/Second/current.txt", name: "current.txt" }] });
      return Promise.resolve({ path: "/", items: [
        { path: "/First", name: "First", is_directory: true },
        { path: "/Second", name: "Second", is_directory: true },
      ] });
    });

    renderPage();
    await screen.findAllByRole("button", { name: "Open First" });
    fireEvent.click(screen.getAllByRole("button", { name: "Open First" })[0]);
    fireEvent.click(screen.getAllByRole("button", { name: "Open Second" })[0]);
    expect(await screen.findByRole("button", { name: "current.txt" })).toBeInTheDocument();
    await act(async () => { finishFirst({ path: "/First", items: [{ path: "/First/stale.txt", name: "stale.txt" }] }); });
    expect(screen.queryByRole("button", { name: "stale.txt" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "current.txt" })).toBeInTheDocument();
  });

  it("clears finished transfer history without removing active transfers", async () => {
    mockDemoShares();
    fileServerClient.list.mockResolvedValue({ path: "/", items: [] });
    fileServerClient.listJobs.mockResolvedValue([
      { id: "active", operation: "upload", from_path: "/active.bin", status: "running" },
      { id: "finished", operation: "download", from_path: "/done.pdf", status: "completed" },
      { id: "old-transfer", operation: "upload", from_path: "/old.bin", status: "rollback_required" },
      { id: "move-review", operation: "move", from_path: "/move.bin", status: "rollback_required" },
    ]);
    fileServerClient.clearFinishedJobs.mockResolvedValue(2);

    renderPage();

    expect(await screen.findByText("Download done.pdf")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Clear finished" }));

    await waitFor(() => expect(fileServerClient.clearFinishedJobs).toHaveBeenCalledTimes(1));
    expect(screen.queryByText("Download done.pdf")).not.toBeInTheDocument();
    expect(screen.queryByText("Upload old.bin")).not.toBeInTheDocument();
    expect(screen.getByText("Upload active.bin")).toBeInTheDocument();
    expect(screen.getByText("Move move.bin")).toBeInTheDocument();
    expect(await screen.findByText("Cleared 2 finished transfers.")).toBeInTheDocument();
  });

  it("cancels a stalled transfer after confirmation so it can be cleared", async () => {
    mockDemoShares();
    fileServerClient.list.mockResolvedValue({ path: "/", items: [] });
    fileServerClient.listJobs.mockResolvedValueOnce([{ id: "stalled", operation: "upload", from_path: "/stalled.bin", status: "running" }])
      .mockResolvedValue([{ id: "stalled", operation: "upload", from_path: "/stalled.bin", status: "cancelled" }]);
    fileServerClient.cancelJob.mockResolvedValue({ id: "stalled", operation: "upload", status: "cancelled" });

    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "Cancel Upload stalled.bin" }));
    expect(fileServerClient.cancelJob).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Cancel transfer" }));
    await waitFor(() => expect(fileServerClient.cancelJob).toHaveBeenCalledWith("stalled"));
    expect(await screen.findByText("Transfer cancelled. You can clear it from history.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Clear finished" })).toBeEnabled();
  });

  it("explains upload limits in human-readable terms after a failed upload", async () => {
    mockDemoShares();
    fileServerClient.list.mockResolvedValue({ path: "/Media", items: [] });
    fileServerClient.uploadFile.mockRejectedValue(new ApiError(413, "upload_too_large", "This file is larger than the maximum upload size.", {
      max_upload_bytes: 1024,
      attempted_upload_bytes: 2048,
    }));
    const file = new File([new Uint8Array(2048)], "large.bin");

    renderPage();
    await screen.findByRole("button", { name: "Upload" });
    fireEvent.change(screen.getByLabelText("Choose files to upload"), { target: { files: [file] } });

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("“large.bin” is 2 KB, but the maximum upload size is 1 KB.");
    expect(alert).toHaveTextContent("Maximum file size: 1 KB.");
    expect(alert).toHaveTextContent("Uploads must be enabled for the selected file source and destination folder.");
    expect(alert).not.toHaveTextContent("file source policy upload size limit exceeded");
  });

  it("uploads device files into the current folder and refreshes the chosen source", async () => {
    mockDemoShares();
    fileServerClient.list.mockResolvedValue({
      path: "/Media/Photos",
      items: [
        { path: "/Media/Photos/sunset-beach.jpg", name: "sunset-beach.jpg", size: 4404019, modified_at: "2026-06-14T16:47:00Z" },
      ],
    });
    fileServerClient.uploadFile.mockResolvedValue({ ok: true });
    const file = new File(["new image"], "new-photo.jpg", { type: "image/jpeg" });

    renderPage();

    expect(await screen.findByRole("button", { name: "sunset-beach.jpg" })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Choose files to upload"), { target: { files: [file] } });

    await waitFor(() => expect(fileServerClient.uploadFile).toHaveBeenCalledWith(file, "/Media/Photos", "hankdemo", undefined));
    await waitFor(() => expect(fileServerClient.list).toHaveBeenCalledWith("/Media/Photos", "hankdemo", undefined));
    expect(await screen.findByText("Uploaded new-photo.jpg.")).toBeInTheDocument();
  });

  it("renames, moves, and downloads files from the preview actions", async () => {
    mockDemoShares();
    fileServerClient.list.mockResolvedValue({
      path: "/Media/Photos",
      items: [
        { path: "/Media/Photos/sunset-beach.jpg", name: "sunset-beach.jpg", size: 4404019, modified_at: "2026-06-14T16:47:00Z" },
        { path: "/Media/Archive", name: "Archive", is_directory: true, modified_at: "2026-06-14T16:47:00Z" },
      ],
    });
    fileServerClient.rename.mockResolvedValue({ ok: true });
    fileServerClient.move.mockResolvedValue({ ok: true, job_id: "filejob_1" });
    fileServerClient.setupDownload.mockResolvedValue({ url: "/v1/file-transfers/download-1" });

    renderPage();

    const preview = await screen.findByLabelText("File preview");
    fireEvent.click(within(preview).getByRole("button", { name: "Download preview" }));
    await waitFor(() => expect(fileServerClient.setupDownload).toHaveBeenCalledWith("/Media/Photos/sunset-beach.jpg", "hankdemo", undefined));

    fireEvent.click(within(preview).getByRole("button", { name: "Rename preview" }));
    fireEvent.change(await screen.findByLabelText("File name"), { target: { value: "sunrise.jpg" } });
    fireEvent.click(screen.getByRole("button", { name: "Rename" }));
    await waitFor(() => expect(fileServerClient.rename).toHaveBeenCalledWith("/Media/Photos/sunset-beach.jpg", "/Media/Photos/sunrise.jpg", "hankdemo", undefined));

    fireEvent.click(within(preview).getByRole("button", { name: "Move preview" }));
    fireEvent.change(await screen.findByLabelText("Destination path"), { target: { value: "/Media/Archive" } });
    fireEvent.click(screen.getByRole("button", { name: "Move here" }));
    await waitFor(() => expect(fileServerClient.move).toHaveBeenCalledWith("/Media/Photos/sunset-beach.jpg", "/Media/Archive/sunset-beach.jpg", false, "hankdemo", "hankdemo", undefined));
  });
  it("keeps historical owner reviews available when browsing is offline, including older pages", async () => {
    mockDemoShares();
    fileServerClient.list.mockRejectedValue(new Error("The primary agent is offline"));
    fileServerClient.listJobs.mockResolvedValue(Array.from({ length: 20 }, (_, i) => ({id: `recent-${i}`, operation: "upload", status: "completed"})));
    const unknown = {id:"older-move",operation:"move",status:"rollback_required",from_path:"/old-source",to_path:"/old-copy",updated_at:"2026-08-13T00:00:00Z"};
    fileServerClient.listOwnerlessJobs.mockImplementation(async (after?: string) => after ? {jobs:[{...unknown,id:"oldest-move",to_path:"/oldest-copy"}],next_cursor:""} : {jobs:[unknown],next_cursor:"older-move"});
    renderPage(true);
    expect(await screen.findByText("The primary agent is offline")).toBeInTheDocument();
    const queue = await screen.findByRole("region", {name:"Historical move owner reviews"});
    expect(await within(queue).findByRole("button",{name:"Review owner"})).toBeInTheDocument();
    fireEvent.click(within(queue).getByRole("button",{name:"Load more owner reviews"}));
    await waitFor(() => expect(within(queue).getAllByRole("button",{name:"Review owner"})).toHaveLength(2));
    expect(fileServerClient.listOwnerlessJobs).toHaveBeenCalledWith("older-move");
  });

});


describe("file history recovery review", () => {
  afterEach(() => { cleanup(); vi.clearAllMocks(); });
  it("requires an explicit admin confirmation to remove interrupted move history", async () => {
    mockDemoShares();
    agentsClient.listAgents.mockResolvedValue([]);
    fileServerClient.list.mockResolvedValue({ path: "/", items: [] });
    fileServerClient.listOwnerlessJobs.mockResolvedValue({jobs:[],next_cursor:""});
    fileServerClient.subscribeToJobs.mockResolvedValue({});
    fileServerClient.onJobsChanged.mockReturnValue(() => undefined);
    const job = { id: "old-move", operation: "move", from_path: "/old.bin", status: "rollback_required", updated_at: "2026-06-01T12:00:00Z" };
    fileServerClient.listJobs.mockResolvedValue([job]);
    fileServerClient.reviewJobDismissal.mockResolvedValue({admin_action_token:"review"});
    fileServerClient.dismissJob.mockResolvedValue({removed:true});
    renderPage(true);
    fireEvent.click((await screen.findAllByRole("button", { name: "Remove from history" }))[0]);
    expect(await screen.findByRole("alertdialog")).toHaveTextContent("does not move, delete, or roll back any files");
    expect(fileServerClient.dismissJob).not.toHaveBeenCalled();
    fileServerClient.listJobs.mockResolvedValue([]);
    fireEvent.click(screen.getByRole("button", { name: "Remove history" }));
    await waitFor(() => expect(fileServerClient.dismissJob).toHaveBeenCalledWith(job.id, job.updated_at, "review"));
    await waitFor(() => expect(screen.queryByText("Move old.bin")).not.toBeInTheDocument());
  });
  it("does not restore cleared history when an older refresh finishes late", async () => {
    mockDemoShares();
    agentsClient.listAgents.mockResolvedValue([]);
    fileServerClient.list.mockResolvedValue({ path: "/", items: [] });
    fileServerClient.subscribeToJobs.mockResolvedValue({});
    fileServerClient.onJobsChanged.mockReturnValue(() => undefined);
    const jobs = [{ id: "finished", operation: "download", from_path: "/done.pdf", status: "completed" }];
    fileServerClient.listJobs.mockResolvedValue(jobs);
    fileServerClient.clearFinishedJobs.mockResolvedValue(1);
    renderPage();
    await screen.findByText("Download done.pdf");
    let finishRefresh!: (value: typeof jobs) => void;
    fileServerClient.listJobs.mockReturnValueOnce(new Promise((resolve) => { finishRefresh = resolve; }));
    act(() => fileServerClient.onJobsChanged.mock.calls[0][0]());
    fireEvent.click(screen.getByRole("button", { name: "Clear finished" }));
    await screen.findByText("Cleared 1 finished transfer.");
    await act(async () => finishRefresh(jobs));
    expect(screen.queryByText("Download done.pdf")).not.toBeInTheDocument();
  });

});
