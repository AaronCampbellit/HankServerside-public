import { useState } from "react";
import { invitationsClient, type AcceptedInvitationPayload } from "../api/invitations";

function tokenFromURL(): string {
  return new URLSearchParams(window.location.search).get("token") || "";
}

function errorMessage(error: unknown): string {
  return error instanceof Error && error.message ? error.message : "Invitation could not be accepted.";
}

function formatDate(value?: string): string {
  return value ? new Date(value).toLocaleString() : "Unknown";
}

export function JoinHomeSettings() {
  const [token, setToken] = useState(tokenFromURL);
  const [result, setResult] = useState<AcceptedInvitationPayload | null>(null);
  const [message, setMessage] = useState("");

  async function acceptInvitation() {
    if (!token.trim()) {
      setMessage("Enter an invite code.");
      return;
    }
    try {
      setResult(await invitationsClient.acceptHomeInvitation(token.trim()));
      setMessage("Home joined.");
    } catch (error) {
      setMessage(errorMessage(error));
    }
  }

  return (
    <section className="settings-page" aria-labelledby="route-title">
      <header className="dashboard-header">
        <div>
          <p className="eyebrow">Hank</p>
          <h1 id="route-title">Join Home</h1>
          <p className="meta-line">Use an invite code from a home admin.</p>
        </div>
        <span className="status-pill">{result?.home ? "Joined" : "Invite"}</span>
      </header>

      {message ? <p className="notice-state">{message}</p> : null}

      <section className="settings-panel" aria-label="Invite code">
        <h2>Enter your code</h2>
        <label>
          <span>Invite code</span>
          <input onChange={(event) => setToken(event.target.value)} onKeyDown={(event) => {
            if (event.key === "Enter") {
              event.preventDefault();
              void acceptInvitation();
            }
          }} type="text" value={token} />
        </label>
        <div className="button-row">
          <button aria-label="Join Home" onClick={() => void acceptInvitation()} type="button">Join home</button>
        </div>
      </section>

      <section className="settings-panel" aria-label="Join result">
        {result?.home ? (
          <article className="dashboard-tile">
            <span>Joined</span>
            <strong>{result.home.name}</strong>
            <small>{result.home.id} · Created {formatDate(result.home.created_at)}</small>
          </article>
        ) : <p className="empty-state">Ask an admin to create invites in People.</p>}
      </section>
    </section>
  );
}
