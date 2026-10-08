import { usePWA } from "./PWAProvider";
import { useOfflineNotes } from "../offlineNotes/OfflineNotesProvider";

export function PWAStatus() {
  const {
    applyUpdate,
    dismissUpdate,
    online,
    updateAvailable,
    updateFailed,
    updatePending,
  } = usePWA();
  const offlineNotes = useOfflineNotes();

  if (!online) {
    return (
      <aside className="pwa-status is-offline" role="status">
        You’re offline. Notes changes are saved on this device. Other Hank actions need a connection.
      </aside>
    );
  }
  if (offlineNotes.mode === "locked" && !offlineNotes.sync.authenticationRequired) {
    return <aside className="pwa-status is-offline" role="status">Notes access is unavailable for this account. Ask an administrator to restore access. Local changes remain on this device.</aside>;
  }
  if (offlineNotes.sync.authenticationRequired) {
    return <aside className="pwa-status is-offline" role="status">Sign in to sync Notes. Your local changes are still on this device.</aside>;
  }
  if (offlineNotes.sync.conflicted > 0) {
    return (
      <aside className="pwa-status is-offline" role="status">
        {offlineNotes.sync.conflicted} Notes {offlineNotes.sync.conflicted === 1 ? "conflict copy" : "conflict copies"} created and waiting to sync.
      </aside>
    );
  }
  if (offlineNotes.sync.failed > 0) {
    return (
      <aside className="pwa-status is-offline" role="status">
        <span>{offlineNotes.sync.failed} Notes changes could not sync. Edit the affected note to retry.</span>
      </aside>
    );
  }
  if (!updateAvailable) return null;

  if (updateFailed) {
    return (
      <aside className="pwa-status is-update" role="status">
        <span>Hank couldn’t update. Keep working or try again.</span>
        <button type="button" onClick={() => void applyUpdate()}>Try again</button>
      </aside>
    );
  }

  return (
    <aside className="pwa-status is-update" role="status">
      <span>{updatePending ? "Updating Hank…" : "Update available"}</span>
      <span className="pwa-status-actions">
        <button type="button" disabled={updatePending} onClick={() => void applyUpdate()}>Update now</button>
        {!updatePending ? <button type="button" onClick={dismissUpdate}>Later</button> : null}
      </span>
    </aside>
  );
}
