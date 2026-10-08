import { registerSW } from "virtual:pwa-register";

export type PWARegistrationCallbacks = {
  onNeedRefresh: () => void;
  onOfflineReady: () => void;
  onRegisterError: (error: unknown) => void;
};

export function registerPWA(callbacks: PWARegistrationCallbacks) {
  return registerSW({ immediate: true, ...callbacks });
}
