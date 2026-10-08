import {
  Component,
  createContext,
  type DependencyList,
  type ErrorInfo,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { ApiError } from "../api/client";

export type AsyncState<T> =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; data: T };

export function messageFromError(error: unknown, fallback = "Request failed."): string {
  if (error instanceof ApiError && error.message) return error.message;
  if (error instanceof Error && error.message) return error.message;
  return fallback;
}

export function useAsyncLoad<T>(
  load: () => Promise<T>,
  dependencies: DependencyList,
  fallbackMessage = "Request failed.",
): AsyncState<T> {
  const [state, setState] = useState<AsyncState<T>>({ status: "loading" });

  useEffect(() => {
    let alive = true;
    setState({ status: "loading" });
    load()
      .then((data) => {
        if (alive) setState({ status: "ready", data });
      })
      .catch((error) => {
        if (alive) setState({ status: "error", message: messageFromError(error, fallbackMessage) });
      });
    return () => {
      alive = false;
    };
  }, dependencies);

  return state;
}

type ErrorBoundaryState = {
  error: Error | null;
};

export class ErrorBoundary extends Component<{ children: ReactNode }, ErrorBoundaryState> {
  state: ErrorBoundaryState = { error: null };

  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { error };
  }

  componentDidCatch(error: Error, errorInfo: ErrorInfo) {
    console.error("Dashboard render failed", error, errorInfo);
  }

  render() {
    if (this.state.error) {
      return (
        <main className="auth-surface">
          <section className="auth-card" role="alert" aria-labelledby="error-boundary-title">
            <p className="eyebrow">Hank</p>
            <h1 id="error-boundary-title">Something went wrong.</h1>
            <p className="empty-state">The dashboard hit a render error. Reloading usually restores the current session.</p>
            <div className="form-actions">
              <button type="button" onClick={() => window.location.reload()}>Reload</button>
            </div>
          </section>
        </main>
      );
    }
    return this.props.children;
  }
}

export function LoadingState({ label = "Loading" }: { label?: string }) {
  return (
    <div className="loading-state" role="status">
      <span className="spinner" aria-hidden="true" />
      <span>{label}</span>
    </div>
  );
}

export function ErrorState({ message }: { message: string }) {
  return (
    <div className="error-state" role="alert">
      {message}
    </div>
  );
}

type Toast = {
  message: string;
  tone: "neutral" | "error";
};

type ToastContextValue = {
  toast: Toast | null;
  showToast: (message: string, tone?: Toast["tone"]) => void;
  clearToast: () => void;
};

const ToastContext = createContext<ToastContextValue | null>(null);

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toast, setToast] = useState<Toast | null>(null);
  const timer = useRef<number | undefined>(undefined);

  const clearToast = useCallback(() => {
    if (timer.current) window.clearTimeout(timer.current);
    setToast(null);
  }, []);

  const showToast = useCallback((message: string, tone: Toast["tone"] = "neutral") => {
    if (timer.current) window.clearTimeout(timer.current);
    setToast({ message, tone });
    timer.current = window.setTimeout(() => setToast(null), 3600);
  }, []);

  useEffect(() => () => { if (timer.current) window.clearTimeout(timer.current); }, []);

  const value = useMemo(() => ({ toast, showToast, clearToast }), [clearToast, showToast, toast]);

  return (
    <ToastContext.Provider value={value}>
      {children}
      {toast ? (
        <div role="status" className={`toast toast-${toast.tone}`}>
          <span className="toast-glyph" aria-hidden="true">{toast.tone === "error" ? "!" : "✓"}</span>
          <span>{toast.message}</span>
        </div>
      ) : null}
    </ToastContext.Provider>
  );
}

export function useToast() {
  const context = useContext(ToastContext);
  if (!context) {
    throw new Error("useToast must be used inside ToastProvider.");
  }
  return context;
}

export type ConfirmOptions = {
  title?: string;
  message: string;
  detail?: string;
  confirmLabel?: string;
  cancelLabel?: string;
  tone?: "default" | "danger";
};

export type PromptOptions = {
  tone?: "default" | "danger";
  title?: string;
  message?: string;
  label?: string;
  placeholder?: string;
  defaultValue?: string;
  confirmLabel?: string;
  cancelLabel?: string;
};

type ConfirmContextValue = {
  // Back-compat: callers may pass a plain string (old window.confirm signature)
  // or a richer ConfirmOptions object.
  confirm: (request: string | ConfirmOptions) => Promise<boolean>;
  // Text-input dialog (replaces window.prompt). Resolves to the trimmed string,
  // or null if cancelled / dismissed / left empty.
  prompt: (request: PromptOptions) => Promise<string | null>;
};

const ConfirmContext = createContext<ConfirmContextValue | null>(null);

type PendingConfirm = ConfirmOptions & { kind: "confirm"; resolve: (value: boolean) => void };
type PendingPrompt = PromptOptions & { kind: "prompt"; resolve: (value: string | null) => void };
type Pending = PendingConfirm | PendingPrompt;

export function ConfirmDialogProvider({ children }: { children: ReactNode }) {
  const [pending, setPending] = useState<Pending | null>(null);
  const [draft, setDraft] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);

  const confirm = useCallback((request: string | ConfirmOptions) => {
    const options: ConfirmOptions = typeof request === "string" ? { message: request } : request;
    return new Promise<boolean>((resolve) => {
      setPending({ kind: "confirm", ...options, resolve });
    });
  }, []);

  const prompt = useCallback((request: PromptOptions) => {
    return new Promise<string | null>((resolve) => {
      setDraft(request.defaultValue ?? "");
      setPending({ kind: "prompt", ...request, resolve });
    });
  }, []);

  const settle = useCallback((result: boolean) => {
    setPending((current) => {
      if (current) {
        if (current.kind === "confirm") {
          current.resolve(result);
        } else {
          current.resolve(result ? (draft.trim() || null) : null);
        }
      }
      return null;
    });
  }, [draft]);

  useEffect(() => {
    if (!pending) return;
    const pendingKind = pending.kind;
    if (pendingKind === "prompt") {
      requestAnimationFrame(() => inputRef.current?.focus());
    }
    function onKey(event: KeyboardEvent) {
      if (event.key === "Escape") settle(false);
      if (event.key === "Enter" && pendingKind === "confirm") settle(true);
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [pending, settle]);

  const value = useMemo(() => ({ confirm, prompt }), [confirm, prompt]);
  const danger = pending?.tone === "danger";

  return (
    <ConfirmContext.Provider value={value}>
      {children}
      {pending ? (
        <div className="confirm-scrim" role="presentation" onClick={() => settle(false)}>
          <div
            className="confirm-card"
            role="alertdialog"
            aria-modal="true"
            aria-label={pending.title ?? (pending.kind === "prompt" ? "Enter a value" : "Confirmation needed")}
            onClick={(e) => e.stopPropagation()}
          >
            <div className="confirm-head">
              <span className={`confirm-badge${danger ? " danger" : ""}`} aria-hidden="true">
                {danger ? "!" : "?"}
              </span>
              <h2>{pending.title ?? (pending.kind === "prompt" ? "Enter a value" : "Confirmation needed")}</h2>
            </div>
            {pending.message ? <p className="confirm-message">{pending.message}</p> : null}
            {pending.kind === "prompt" ? (
              <input
                ref={inputRef}
                className="confirm-input"
                aria-label={pending.label ?? pending.placeholder ?? "Value"}
                placeholder={pending.placeholder}
                value={draft}
                onChange={(event) => setDraft(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === "Enter") {
                    event.preventDefault();
                    settle(true);
                  }
                }}
              />
            ) : null}
            {pending.kind === "confirm" && pending.detail ? <pre className="confirm-detail">{pending.detail}</pre> : null}
            <div className="confirm-actions">
              <button type="button" className="secondary" onClick={() => settle(false)}>
                {pending.cancelLabel ?? "Cancel"}
              </button>
              <button
                type="button"
                className={danger ? "danger-solid" : ""}
                onClick={() => settle(true)}
                autoFocus={pending.kind === "confirm"}
              >
                {pending.confirmLabel ?? "Confirm"}
              </button>
            </div>
          </div>
        </div>
      ) : null}
    </ConfirmContext.Provider>
  );
}

export function useConfirmDialog() {
  const context = useContext(ConfirmContext);
  if (!context) {
    throw new Error("useConfirmDialog must be used inside ConfirmDialogProvider.");
  }
  return context;
}
