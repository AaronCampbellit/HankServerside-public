import { useEffect, useState } from "react";
import { authClient } from "../api/auth";
import { redirectTo } from "../browser/navigation";

type Mode = "login" | "register";

export function LoginPage() {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [message, setMessage] = useState(() => (
    new URLSearchParams(window.location.search).get("expired") === "1" ? "Session expired. Sign in again." : ""
  ));
  const [busy, setBusy] = useState<Mode | null>(null);

  useEffect(() => {
    let active = true;
    authClient
      .me()
      .then(() => {
        if (active) redirectTo("/dashboard");
      })
      .catch(() => {});
    return () => {
      active = false;
    };
  }, []);

  async function submit(mode: Mode) {
    const trimmedEmail = email.trim();
    if (!trimmedEmail || !password) {
      setMessage("Email and password are required.");
      return;
    }
    setBusy(mode);
    setMessage("");
    try {
      const payload = mode === "login" ? await authClient.login(trimmedEmail, password) : await authClient.register(trimmedEmail, password);
      redirectTo(payload.user?.password_change_required ? "/password-change" : "/dashboard");
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Authentication failed.");
    } finally {
      setBusy(null);
    }
  }

  async function signInWithMicrosoft() {
    setBusy("login");
    setMessage("");
    try {
      window.location.assign((await authClient.startEntra()).authorization_url);
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Microsoft sign-in is unavailable.");
      setBusy(null);
    }
  }

  return (
    <section className="auth-card" aria-labelledby="auth-title">
      <p className="eyebrow">Hank</p>
      <h1 id="auth-title">Sign in to Hank</h1>
      <p className="empty-state">Use your Hank account to manage the server and primary Hank Agent.</p>
      {message ? <p role="alert" className="error-state">{message}</p> : null}
      <form className="quick-link-form" onSubmit={(event) => { event.preventDefault(); void submit("login"); }}>
        <label>
          <span>Email</span>
          <input autoComplete="email" type="email" value={email} onChange={(event) => setEmail(event.target.value)} />
        </label>
        <label>
          <span>Password</span>
          <input autoComplete="current-password" type="password" value={password} onChange={(event) => setPassword(event.target.value)} />
        </label>
        <div className="form-actions">
          <button disabled={busy !== null} type="submit">Sign in</button>
          <button disabled={busy !== null} className="secondary" type="button" onClick={() => void submit("register")}>
            Create account
          </button>
        </div>
		<button disabled={busy !== null} className="secondary" type="button" onClick={() => void signInWithMicrosoft()}>Continue with Microsoft</button>
      </form>
    </section>
  );
}
