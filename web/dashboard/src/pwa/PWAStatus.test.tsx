import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PWAStatus } from "./PWAStatus";
import type { PWAContextValue } from "./PWAProvider";
import type { OfflineNotesContextValue } from "../offlineNotes/OfflineNotesProvider";

const pwaState = vi.hoisted(() => ({ current: null as PWAContextValue | null }));
const offlineState = vi.hoisted(() => ({ current: null as OfflineNotesContextValue | null }));

vi.mock("./PWAProvider", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./PWAProvider")>()),
  usePWA: () => pwaState.current,
}));

vi.mock("../offlineNotes/OfflineNotesProvider", () => ({
  useOfflineNotes: () => offlineState.current,
}));

function state(overrides: Partial<PWAContextValue> = {}): PWAContextValue {
  return {
    online: true,
    installMode: "none",
    install: vi.fn().mockResolvedValue(undefined),
    updateAvailable: false,
    updatePending: false,
    updateFailed: false,
    applyUpdate: vi.fn().mockResolvedValue(undefined),
    dismissUpdate: vi.fn(),
    ...overrides,
  };
}

function notesState(overrides: Partial<OfflineNotesContextValue> = {}): OfflineNotesContextValue {
  return {
    mode: "inactive",
    activeUserID: "",
    repository: null,
    sync: {
      pending: 0,
      syncing: 0,
      failed: 0,
      conflicted: 0,
      authenticationRequired: false,
      lastSyncedAt: 0,
    },
    activateAuthenticated: vi.fn().mockResolvedValue(undefined),
    activateOffline: vi.fn().mockResolvedValue(null),
    syncNow: vi.fn().mockResolvedValue(undefined),
    purgeActive: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  };
}

describe("PWAStatus", () => {
  afterEach(() => {
    cleanup();
    pwaState.current = null;
    offlineState.current = null;
  });

  it("renders nothing during normal online operation", () => {
    pwaState.current = state();
    offlineState.current = notesState();
    expect(render(<PWAStatus />).container).toBeEmptyDOMElement();
  });

  it("gives offline state visual priority over an update", () => {
    pwaState.current = state({ online: false, updateAvailable: true });
    offlineState.current = notesState({ mode: "offline" });
    render(<PWAStatus />);

    expect(screen.getByRole("status")).toHaveTextContent("You’re offline. Notes changes are saved on this device.");
    expect(screen.queryByRole("button", { name: "Update now" })).not.toBeInTheDocument();
  });

  it("waits for Update now before applying a waiting worker", () => {
    const applyUpdate = vi.fn().mockResolvedValue(undefined);
    pwaState.current = state({ updateAvailable: true, applyUpdate });
    offlineState.current = notesState();
    render(<PWAStatus />);

    expect(applyUpdate).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Update now" }));
    expect(applyUpdate).toHaveBeenCalledOnce();
  });

  it("allows the waiting update notice to be deferred", () => {
    const dismissUpdate = vi.fn();
    pwaState.current = state({ updateAvailable: true, dismissUpdate });
    offlineState.current = notesState();
    render(<PWAStatus />);

    fireEvent.click(screen.getByRole("button", { name: "Later" }));
    expect(dismissUpdate).toHaveBeenCalledOnce();
  });

  it("shows pending and failed update states", () => {
    pwaState.current = state({ updateAvailable: true, updatePending: true });
    offlineState.current = notesState();
    const { rerender } = render(<PWAStatus />);
    expect(screen.getByRole("status")).toHaveTextContent("Updating Hank");
    expect(screen.getByRole("button", { name: "Update now" })).toBeDisabled();

    pwaState.current = state({ updateAvailable: true, updateFailed: true });
    rerender(<PWAStatus />);
    expect(screen.getByRole("status")).toHaveTextContent("Hank couldn’t update");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("keeps routine queued and in-flight Notes synchronization in the background", () => {
    pwaState.current = state();
    offlineState.current = notesState({
      mode: "online",
      sync: { ...notesState().sync, pending: 3, syncing: 1 },
    });
    const { container } = render(<PWAStatus />);

    expect(container).toBeEmptyDOMElement();
  });

  it("announces conflict copies without exposing note content", () => {
    pwaState.current = state();
    offlineState.current = notesState({
      mode: "online",
      sync: { ...notesState().sync, pending: 1, conflicted: 1 },
    });
    render(<PWAStatus />);

    expect(screen.getByRole("status")).toHaveTextContent("1 Notes conflict copy created and waiting to sync");
    expect(screen.getByRole("status")).not.toHaveTextContent("Daily");
  });

  it("distinguishes revoked Notes access from a sign-in requirement", () => {
    pwaState.current = state();
    offlineState.current = notesState({ mode: "locked" });
    render(<PWAStatus />);
    expect(screen.getByRole("status")).toHaveTextContent("Notes access is unavailable for this account");
    expect(screen.getByRole("status")).not.toHaveTextContent("Sign in");
  });

  it("surfaces authentication and failed synchronization states without note content", () => {
    pwaState.current = state();
    offlineState.current = notesState({
      mode: "locked",
      sync: { ...notesState().sync, failed: 2, authenticationRequired: true },
    });
    render(<PWAStatus />);

    expect(screen.getByRole("status")).toHaveTextContent("Sign in to sync Notes");
    expect(screen.getByRole("status")).not.toHaveTextContent("Daily");
  });
});
