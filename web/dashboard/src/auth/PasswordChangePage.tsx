import { useEffect, useState } from "react";
import { authClient } from "../api/auth";
import { redirectTo } from "../browser/navigation";

export function PasswordChangePage() {
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [sessionState, setSessionState] = useState("Checking session");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let active = true;
    authClient
      .me()
      .then((me) => {
        if (!active) return;
        setSessionState(`Signed in as ${me.user?.email || "unknown"}`);
        if (!me.user?.password_change_required) redirectTo("/dashboard");
      })
      .catch(() => {
        if (active) redirectTo("/");
      });
    return () => {
      active = false;
    };
  }, []);

  async function submit() {
    setBusy(true);
    setMessage("");
    try {
      await authClient.changePassword(currentPassword, newPassword);
      redirectTo("/dashboard");
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Could not change password.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="auth-card" aria-labelledby="password-title">
      <p className="eyebrow">Hank</p>
      <h1 id="password-title">Change Password</h1>
      <p className="notice-state">{sessionState}</p>
      {message ? <p role="alert" className="error-state">{message}</p> : null}
      <form className="quick-link-form" onSubmit={(event) => { event.preventDefault(); void submit(); }}>
        <label>
          <span>Current password</span>
          <input autoComplete="current-password" type="password" value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} />
        </label>
        <label>
          <span>New password</span>
          <input autoComplete="new-password" type="password" value={newPassword} onChange={(event) => setNewPassword(event.target.value)} />
        </label>
        <button disabled={busy} type="submit">Update password</button>
      </form>
    </section>
  );
}
