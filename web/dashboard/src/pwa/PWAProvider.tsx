import { createContext, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { registerPWA } from "./registration";

type NativeInstallChoice = {
  outcome: "accepted" | "dismissed";
  platform: string;
};

type BeforeInstallPromptEvent = Event & {
  prompt: () => Promise<void>;
  userChoice: Promise<NativeInstallChoice>;
};

type NavigatorWithStandalone = Navigator & {
  standalone?: boolean;
};

export type InstallMode = "none" | "native" | "ios";

export type PWAContextValue = {
  online: boolean;
  installMode: InstallMode;
  install: () => Promise<void>;
  updateAvailable: boolean;
  updatePending: boolean;
  updateFailed: boolean;
  applyUpdate: () => Promise<void>;
  dismissUpdate: () => void;
};

const PWAContext = createContext<PWAContextValue | null>(null);

function isStandalone(): boolean {
  const navigatorStandalone = Boolean((window.navigator as NavigatorWithStandalone).standalone);
  return navigatorStandalone || window.matchMedia?.("(display-mode: standalone)").matches === true;
}

function isIOSSafari(): boolean {
  const { maxTouchPoints, platform, userAgent, vendor } = window.navigator;
  const appleMobile = /iPad|iPhone|iPod/.test(userAgent)
    || (platform === "MacIntel" && maxTouchPoints > 1);
  const safari = /Safari/.test(userAgent) && !/(CriOS|FxiOS|EdgiOS|OPiOS)/.test(userAgent);
  return appleMobile && safari && /Apple/.test(vendor);
}

export function PWAProvider({ children }: { children: ReactNode }) {
  const [online, setOnline] = useState(() => window.navigator.onLine);
  const [installed, setInstalled] = useState(isStandalone);
  const [installPrompt, setInstallPrompt] = useState<BeforeInstallPromptEvent | null>(null);
  const [updateAvailable, setUpdateAvailable] = useState(false);
  const [updatePending, setUpdatePending] = useState(false);
  const [updateFailed, setUpdateFailed] = useState(false);
  const updateServiceWorker = useRef<((reloadPage?: boolean) => Promise<void>) | null>(null);

  useEffect(() => {
    const displayMode = window.matchMedia?.("(display-mode: standalone)");
    const handleOnline = () => setOnline(true);
    const handleOffline = () => setOnline(false);
    const handleInstalled = () => {
      setInstalled(true);
      setInstallPrompt(null);
    };
    const handleDisplayMode = () => setInstalled(isStandalone());
    const handleInstallPrompt = (event: Event) => {
      event.preventDefault();
      setInstallPrompt(event as BeforeInstallPromptEvent);
    };

    window.addEventListener("online", handleOnline);
    window.addEventListener("offline", handleOffline);
    window.addEventListener("appinstalled", handleInstalled);
    window.addEventListener("beforeinstallprompt", handleInstallPrompt);
    displayMode?.addEventListener("change", handleDisplayMode);
    return () => {
      window.removeEventListener("online", handleOnline);
      window.removeEventListener("offline", handleOffline);
      window.removeEventListener("appinstalled", handleInstalled);
      window.removeEventListener("beforeinstallprompt", handleInstallPrompt);
      displayMode?.removeEventListener("change", handleDisplayMode);
    };
  }, []);

  useEffect(() => {
    updateServiceWorker.current = registerPWA({
      onNeedRefresh: () => {
        setUpdateFailed(false);
        setUpdateAvailable(true);
      },
      onOfflineReady: () => undefined,
      onRegisterError: (error) => console.error("Hank PWA registration failed.", error),
    });
  }, []);

  const installMode: InstallMode = installed
    ? "none"
    : installPrompt
      ? "native"
      : isIOSSafari()
        ? "ios"
        : "none";

  async function install() {
    if (!installPrompt) return;
    try {
      await installPrompt.prompt();
      await installPrompt.userChoice;
    } finally {
      setInstallPrompt(null);
    }
  }

  async function applyUpdate() {
    if (!updateServiceWorker.current || updatePending) return;
    setUpdatePending(true);
    setUpdateFailed(false);
    try {
      await updateServiceWorker.current(true);
      setUpdateAvailable(false);
    } catch {
      setUpdateFailed(true);
    } finally {
      setUpdatePending(false);
    }
  }

  const value: PWAContextValue = {
    online,
    installMode,
    install,
    updateAvailable,
    updatePending,
    updateFailed,
    applyUpdate,
    dismissUpdate: () => setUpdateAvailable(false),
  };

  return <PWAContext.Provider value={value}>{children}</PWAContext.Provider>;
}

export function usePWA(): PWAContextValue {
  const value = useContext(PWAContext);
  if (!value) throw new Error("usePWA must be used within PWAProvider");
  return value;
}
