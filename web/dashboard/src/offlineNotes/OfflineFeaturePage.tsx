export function OfflineFeaturePage({ notesLocked = false }: { notesLocked?: boolean }) {
  return (
    <section className="dashboard-page offline-feature-page" aria-labelledby="offline-feature-title">
      <p className="eyebrow">{notesLocked ? "Notes locked" : "Offline"}</p>
      <h1 id="offline-feature-title">{notesLocked ? "Sign in required" : "Connection required"}</h1>
      <p>{notesLocked
        ? "Sign in again to unlock Notes. Your local changes remain stored on this device."
        : "This part of Hank needs a connection. Your cached Notes workspace remains available."}</p>
      <div className="button-row">
        {!notesLocked ? <a className="button primary" href="/dashboard/profile-notes">Open Notes</a> : null}
        <button type="button" onClick={() => window.location.reload()}>Try again</button>
      </div>
    </section>
  );
}
