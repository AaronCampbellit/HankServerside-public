import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { InstallHankAction } from "./InstallHankAction";
import type { PWAContextValue } from "./PWAProvider";

const pwaState = vi.hoisted(() => ({ current: null as PWAContextValue | null }));

vi.mock("./PWAProvider", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./PWAProvider")>()),
  usePWA: () => pwaState.current,
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

describe("InstallHankAction", () => {
  afterEach(() => {
    cleanup();
    pwaState.current = null;
  });

  it("invokes native installation and completes the menu action", async () => {
    const install = vi.fn().mockResolvedValue(undefined);
    const onComplete = vi.fn();
    pwaState.current = state({ installMode: "native", install });
    render(<InstallHankAction onComplete={onComplete} />);

    fireEvent.click(screen.getByRole("button", { name: "Install Hank" }));

    await waitFor(() => expect(install).toHaveBeenCalledOnce());
    expect(onComplete).toHaveBeenCalledOnce();
  });

  it("shows iOS Add to Home Screen guidance", () => {
    pwaState.current = state({ installMode: "ios" });
    render(<InstallHankAction />);

    fireEvent.click(screen.getByRole("button", { name: "Install Hank" }));

    const dialog = screen.getByRole("dialog", { name: "Install Hank" });
    expect(dialog).toHaveTextContent("Open Hank in Safari");
    expect(dialog).toHaveTextContent("Add to Home Screen");
  });

  it("closes iOS guidance with Escape and restores focus", () => {
    pwaState.current = state({ installMode: "ios" });
    render(<InstallHankAction />);
    const trigger = screen.getByRole("button", { name: "Install Hank" });
    fireEvent.click(trigger);

    fireEvent.keyDown(document, { key: "Escape" });

    expect(screen.queryByRole("dialog", { name: "Install Hank" })).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });

  it("renders nothing when installation is unavailable", () => {
    pwaState.current = state({ installMode: "none" });
    expect(render(<InstallHankAction />).container).toBeEmptyDOMElement();
  });
});
