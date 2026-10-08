import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api/client";
import {
  ConfirmDialogProvider,
  ErrorBoundary,
  messageFromError,
  useAsyncLoad,
  useConfirmDialog,
} from "./primitives";

function ThrowingChild() {
  throw new Error("render failed");
  return null;
}

describe("ui primitives", () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("renders an error panel when a child throws", () => {
    vi.spyOn(console, "error").mockImplementation(() => {});

    render(
      <ErrorBoundary>
        <ThrowingChild />
      </ErrorBoundary>,
    );

    expect(screen.getByRole("alert")).toHaveTextContent("Something went wrong.");
    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
  });

  it("uses API error messages before generic errors", () => {
    expect(messageFromError(new ApiError(400, "bad_request", "Bad input", {}))).toBe("Bad input");
    expect(messageFromError(new Error("Plain failure"))).toBe("Plain failure");
    expect(messageFromError(null, "Fallback")).toBe("Fallback");
  });

  it("loads async data through the shared loading state helper", async () => {
    function AsyncProbe() {
      const state = useAsyncLoad(async () => "ready value", [], "Fallback failure");
      if (state.status === "loading") return <p>Loading</p>;
      if (state.status === "error") return <p role="alert">{state.message}</p>;
      return <p>{state.data}</p>;
    }

    render(<AsyncProbe />);

    expect(screen.getByText("Loading")).toBeInTheDocument();
    expect(await screen.findByText("ready value")).toBeInTheDocument();
  });

  it("normalizes async load failures through shared error messages", async () => {
    function AsyncProbe() {
      const state = useAsyncLoad(async () => {
        throw new ApiError(401, "unauthorized", "Session expired.", {});
      }, [], "Fallback failure");
      if (state.status === "loading") return <p>Loading</p>;
      if (state.status === "error") return <p role="alert">{state.message}</p>;
      return <p>{state.data}</p>;
    }

    render(<AsyncProbe />);

    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("Session expired."));
  });

  it("resolves prompt dialog input through the shared confirm provider", async () => {
    const onValue = vi.fn();

    function PromptProbe() {
      const dialog = useConfirmDialog();
      return (
        <button type="button" onClick={async () => onValue(await dialog.prompt({ label: "Folder name", confirmLabel: "Create folder" }))}>
          Open prompt
        </button>
      );
    }

    render(
      <ConfirmDialogProvider>
        <PromptProbe />
      </ConfirmDialogProvider>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Open prompt" }));
    fireEvent.change(await screen.findByLabelText("Folder name"), { target: { value: "Projects" } });
    fireEvent.click(screen.getByRole("button", { name: "Create folder" }));

    await waitFor(() => expect(onValue).toHaveBeenCalledWith("Projects"));
  });
});
