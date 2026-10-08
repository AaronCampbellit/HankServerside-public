import { useEffect, useState } from "react";
import { authClient, type InvitationPreview } from "../api/auth";
import { redirectTo } from "../browser/navigation";

function tokenFromLocation(): string {
  const hashParams = new URLSearchParams(window.location.hash.replace(/^#/, ""));
  const queryParams = new URLSearchParams(window.location.search);
  return hashParams.get("token") || queryParams.get("token") || "";
}

export function JoinPage() {
  const [token, setToken] = useState(() => tokenFromLocation());
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [preview, setPreview] = useState<InvitationPreview | null>(null);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);

  async function previewInvite(nextToken = token) {
    const trimmedToken = nextToken.trim();
    if (!trimmedToken) return;
    setMessage("");
    try {
      const nextPreview = await authClient.previewInvitation(trimmedToken);
      setPreview(nextPreview);
      setEmail((current) => current || nextPreview.email);
    } catch (error) {
      setPreview(null);
      setMessage(error instanceof Error ? error.message : "Invite could not be found.");
    }
  }

  useEffect(() => {
    void previewInvite(token);
    // token is read once from the URL on mount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function submit() {
    setBusy(true);
    setMessage("");
    try {
      await authClient.signupInvitation(token.trim(), email.trim(), password);
      redirectTo("/dashboard");
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Could not join home.");
    } finally {
      setBusy(false);
    }
  }

  async function signInWithMicrosoft() {
    setBusy(true);
    setMessage("");
    try {
      window.location.assign((await authClient.startEntra(token.trim())).authorization_url);
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Microsoft sign-in is unavailable.");
      setBusy(false);
    }
  }

  return (
    <section className="auth-card" aria-labelledby="join-title">
      <p className="eyebrow">Hank</p>
      <h1 id="join-title">Join Home</h1>
      <p className="empty-state">Use an invitation token to create your account and join a home.</p>
      {message ? <p role="alert" className="error-state">{message}</p> : null}
      {preview ? <p className="notice-state">{preview.email} can join as {preview.role}.</p> : null}
      <form className="quick-link-form" onSubmit={(event) => { event.preventDefault(); void submit(); }}>
        <label>
          <span>Invite token</span>
          <input value={token} onBlur={() => void previewInvite()} onChange={(event) => setToken(event.target.value)} />
        </label>
        <label>
          <span>Email</span>
          <input autoComplete="email" type="email" value={email} onChange={(event) => setEmail(event.target.value)} />
        </label>
        <label>
          <span>Password</span>
          <input autoComplete="new-password" type="password" value={password} onChange={(event) => setPassword(event.target.value)} />
        </label>
        <button disabled={busy} type="submit">Join home</button>
		<button disabled={busy || !token.trim()} className="secondary" type="button" onClick={() => void signInWithMicrosoft()}>Join with Microsoft</button>
      </form>
    </section>
  );
}
