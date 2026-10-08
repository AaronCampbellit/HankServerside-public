import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PWAProvider, usePWA } from "./PWAProvider";
import type { PWARegistrationCallbacks } from "./registration";

const registration = vi.hoisted(() => ({
  callbacks: null as PWARegistrationCallbacks | null,
  update: vi.fn<(reloadPage?: boolean) => Promise<void>>(),
}));

vi.mock("./registration", () => ({
  registerPWA: vi.fn((callbacks: PWARegistrationCallbacks) => {
    registration.callbacks = callbacks;
    return registration.update;
  }),
}));

function Probe() {
  const pwa = usePWA();
  return (
    <div>
      <output data-testid="online">{String(pwa.online)}</output>
      <output data-testid="install-mode">{pwa.installMode}</output>
      <output data-testid="update-available">{String(pwa.updateAvailable)}</output>
      <output data-testid="update-pending">{String(pwa.updatePending)}</output>
      <output data-testid="update-failed">{String(pwa.updateFailed)}</output>
      <button type="button" onClick={() => void pwa.install()}>Install</button>
      <button type="button" onClick={() => void pwa.applyUpdate()}>Apply update</button>
      <button type="button" onClick={pwa.dismissUpdate}>Dismiss update</button>
    </div>
  );
}

function setNavigatorValue(key: string, value: unknown) {
  Object.defineProperty(window.navigator, key, { configurable: true, value });
}

describe("PWAProvider", () => {
  beforeEach(() => {
    registration.callbacks = null;
    registration.update.mockReset().mockResolvedValue(undefined);
    setNavigatorValue("onLine", true);
    setNavigatorValue("userAgent", "Mozilla/5.0 Chrome/140 Safari/537.36");
    setNavigatorValue("vendor", "Google Inc.");
    setNavigatorValue("platform", "Linux x86_64");
    setNavigatorValue("maxTouchPoints", 0);
    setNavigatorValue("standalone", false);
    vi.stubGlobal("matchMedia", vi.fn(() => ({
      matches: false,
      media: "(display-mode: standalone)",
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })));
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("tracks browser connectivity events", () => {
    render(<PWAProvider><Probe /></PWAProvider>);

    act(() => window.dispatchEvent(new Event("offline")));
    expect(screen.getByTestId("online")).toHaveTextContent("false");

    act(() => window.dispatchEvent(new Event("online")));
    expect(screen.getByTestId("online")).toHaveTextContent("true");
  });

  it("exposes a waiting update without applying it automatically", () => {
    render(<PWAProvider><Probe /></PWAProvider>);

    act(() => registration.callbacks?.onNeedRefresh());

    expect(screen.getByTestId("update-available")).toHaveTextContent("true");
    expect(registration.update).not.toHaveBeenCalled();
  });

  it("applies and dismisses updates only through explicit actions", async () => {
    render(<PWAProvider><Probe /></PWAProvider>);
    act(() => registration.callbacks?.onNeedRefresh());

    fireEvent.click(screen.getByRole("button", { name: "Dismiss update" }));
    expect(screen.getByTestId("update-available")).toHaveTextContent("false");

    act(() => registration.callbacks?.onNeedRefresh());
    fireEvent.click(screen.getByRole("button", { name: "Apply update" }));

    await waitFor(() => expect(registration.update).toHaveBeenCalledWith(true));
    expect(screen.getByTestId("update-pending")).toHaveTextContent("false");
  });

  it("reports update activation failures without breaking its children", async () => {
    registration.update.mockRejectedValue(new Error("activation failed"));
    render(<PWAProvider><Probe /></PWAProvider>);
    act(() => registration.callbacks?.onNeedRefresh());

    fireEvent.click(screen.getByRole("button", { name: "Apply update" }));

    await waitFor(() => expect(screen.getByTestId("update-failed")).toHaveTextContent("true"));
    expect(screen.getByRole("button", { name: "Install" })).toBeInTheDocument();
  });

  it("captures and invokes the native install prompt", async () => {
    const prompt = vi.fn<() => Promise<void>>().mockResolvedValue(undefined);
    const installEvent = Object.assign(new Event("beforeinstallprompt"), {
      prompt,
      userChoice: Promise.resolve({ outcome: "accepted", platform: "web" }),
    });
    render(<PWAProvider><Probe /></PWAProvider>);

    act(() => window.dispatchEvent(installEvent));
    expect(screen.getByTestId("install-mode")).toHaveTextContent("native");

    fireEvent.click(screen.getByRole("button", { name: "Install" }));
    await waitFor(() => expect(prompt).toHaveBeenCalledOnce());
    expect(screen.getByTestId("install-mode")).toHaveTextContent("none");
  });

  it("offers manual installation only to iOS Safari outside standalone mode", () => {
    setNavigatorValue("userAgent", "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Version/18.0 Mobile/15E148 Safari/604.1");
    setNavigatorValue("vendor", "Apple Computer, Inc.");

    const { unmount } = render(<PWAProvider><Probe /></PWAProvider>);
    expect(screen.getByTestId("install-mode")).toHaveTextContent("ios");
    unmount();

    setNavigatorValue("standalone", true);
    render(<PWAProvider><Probe /></PWAProvider>);
    expect(screen.getByTestId("install-mode")).toHaveTextContent("none");
  });

  it("keeps registration failures non-fatal", () => {
    const error = new Error("registration failed");
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);
    render(<PWAProvider><Probe /></PWAProvider>);

    act(() => registration.callbacks?.onRegisterError(error));

    expect(screen.getByRole("button", { name: "Install" })).toBeInTheDocument();
    expect(consoleError).toHaveBeenCalledWith("Hank PWA registration failed.", error);
  });
});
