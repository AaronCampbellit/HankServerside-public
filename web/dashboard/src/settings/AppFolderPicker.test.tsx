import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppFolderPicker } from "./AppFolderPicker";

const fileServerClient = vi.hoisted(() => ({ list: vi.fn() }));

vi.mock("../api/fileServer", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/fileServer")>()),
  fileServerClient,
}));

describe("AppFolderPicker", () => {
  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("requires a source before opening", () => {
    render(<AppFolderPicker disabled={false} label="Movie destination" sourceID="" value="" onChange={vi.fn()} />);

    expect(screen.getByRole("button", { name: "Choose Movie destination folder" })).toBeDisabled();
    expect(screen.getByText("Select a source first.")).toBeInTheDocument();
  });

  it("browses existing folders at arbitrary depth and selects the full path", async () => {
    const onChange = vi.fn();
    fileServerClient.list.mockImplementation(async (path: string) => ({
      path,
      items: path === "/"
        ? [
            { name: "Movies", path: "/Movies", is_directory: true },
            { name: "readme.txt", path: "/readme.txt", is_directory: false },
          ]
        : path === "/Movies"
          ? [{ name: "4K", path: "/Movies/4K", is_directory: true }]
          : path === "/Movies/4K"
            ? [{ name: "Family", path: "/Movies/4K/Family", is_directory: true }]
            : path === "/Movies/4K/Family"
              ? [{ name: "Animated", path: "/Movies/4K/Family/Animated", is_directory: true }]
              : [],
    }));

    render(<AppFolderPicker disabled={false} label="Movie destination" sourceID="media" value="" onChange={onChange} />);
    fireEvent.click(screen.getByRole("button", { name: "Choose Movie destination folder" }));

    const dialog = await screen.findByRole("dialog", { name: "Choose Movie destination folder" });
    expect(within(dialog).queryByText("readme.txt")).not.toBeInTheDocument();
    for (const folder of ["Movies", "4K", "Family", "Animated"]) {
      fireEvent.click(await within(dialog).findByRole("button", { name: `Open ${folder}` }));
    }
    await waitFor(() => expect(fileServerClient.list).toHaveBeenLastCalledWith("/Movies/4K/Family/Animated", "media"));

    fireEvent.click(within(dialog).getByRole("button", { name: "Movies" }));
    await waitFor(() => expect(fileServerClient.list).toHaveBeenLastCalledWith("/Movies", "media"));
    fireEvent.click(within(dialog).getByRole("button", { name: "Use this folder" }));

    expect(onChange).toHaveBeenCalledWith("/Movies");
  });

  it("can select the source root", async () => {
    const onChange = vi.fn();
    fileServerClient.list.mockResolvedValue({ path: "/", items: [] });

    render(<AppFolderPicker disabled={false} label="TV destination" sourceID="tv" value="/" onChange={onChange} />);
    fireEvent.click(screen.getByRole("button", { name: "Choose TV destination folder" }));
    fireEvent.click(await screen.findByRole("button", { name: "Use this folder" }));

    expect(onChange).toHaveBeenCalledWith("/");
  });

  it("keeps the picker open and retries listing errors", async () => {
    fileServerClient.list
      .mockRejectedValueOnce(new Error("Share unavailable"))
      .mockResolvedValueOnce({ path: "/", items: [] });

    render(<AppFolderPicker disabled={false} label="Movie destination" sourceID="media" value="" onChange={vi.fn()} />);
    fireEvent.click(screen.getByRole("button", { name: "Choose Movie destination folder" }));

    const dialog = await screen.findByRole("dialog", { name: "Choose Movie destination folder" });
    expect(await within(dialog).findByText("Share unavailable")).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole("button", { name: "Retry" }));

    await waitFor(() => expect(fileServerClient.list).toHaveBeenCalledTimes(2));
    expect(within(dialog).getByRole("button", { name: "Use this folder" })).toBeEnabled();
  });
});
