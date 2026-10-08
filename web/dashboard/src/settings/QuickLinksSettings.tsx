import { type FormEvent, useEffect, useState } from "react";
import {
  quickLinksClient,
  type HomeQuickLink,
  type QuickLinkInput,
  type QuickLinksPayload,
} from "../api/quickLinks";
import { useConfirmDialog } from "../ui/primitives";

type FormState = QuickLinkInput & { id: string };

type SettingsState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; payload: QuickLinksPayload; form: FormState | null; message: string };

const emptyInput: FormState = {
  id: "",
  title: "",
  url: "",
  description: "",
  health_check_enabled: true,
};

function errorMessage(error: unknown): string {
  return error instanceof Error && error.message ? error.message : "Quick links could not be loaded.";
}

function inputFromLink(link: HomeQuickLink): FormState {
  return {
    id: link.id,
    title: link.title,
    url: link.url,
    description: link.description || "",
    health_check_enabled: link.health_check_enabled,
  };
}

function inputFromForm(form: FormState): QuickLinkInput {
  return {
    title: form.title.trim(),
    url: form.url.trim(),
    description: form.description.trim(),
    health_check_enabled: form.health_check_enabled,
  };
}

function statusText(link: HomeQuickLink): string {
  if (!link.health_check_enabled || link.status === "disabled") return "Not checked";
  if (link.status === "up") return "Up";
  if (link.status === "down") return link.last_error || "Review";
  return "Unchecked";
}

export function QuickLinksSettings() {
  const [state, setState] = useState<SettingsState>({ status: "loading" });
  const dialog = useConfirmDialog();

  async function load(message = "") {
    try {
      const payload = await quickLinksClient.list();
      setState({ status: "ready", payload, form: null, message });
    } catch (error) {
      setState({ status: "error", message: errorMessage(error) });
    }
  }

  useEffect(() => {
    void load();
  }, []);

  if (state.status === "loading") {
    return (
      <section className="settings-page" aria-labelledby="route-title">
        <p className="eyebrow">Hank</p>
        <h1 id="route-title">Quick Links</h1>
        <p className="loading-state">Loading quick links...</p>
      </section>
    );
  }

  if (state.status === "error") {
    return (
      <section className="settings-page" aria-labelledby="route-title">
        <p className="eyebrow">Hank</p>
        <h1 id="route-title">Quick Links</h1>
        <p className="error-state">{state.message}</p>
      </section>
    );
  }

  const { payload, form } = state;

  function setReady(next: Partial<Extract<SettingsState, { status: "ready" }>>) {
    setState((current) => current.status === "ready" ? { ...current, ...next } : current);
  }

  async function saveQuickLink(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!form) return;
    try {
      const input = inputFromForm(form);
      if (form.id) {
        await quickLinksClient.update(form.id, input);
      } else {
        await quickLinksClient.create(input);
      }
      await load("Quick link saved.");
    } catch (error) {
      setReady({ message: errorMessage(error) });
    }
  }

  async function deleteQuickLink(link: HomeQuickLink) {
    const confirmed = await dialog.confirm({
      title: "Delete quick link",
      message: `Delete "${link.title}"? This can't be undone.`,
      confirmLabel: "Delete",
      tone: "danger",
    });
    if (!confirmed) return;
    try {
      await quickLinksClient.remove(link.id);
      await load("Quick link deleted.");
    } catch (error) {
      setReady({ message: errorMessage(error) });
    }
  }

  async function moveQuickLink(index: number, direction: number) {
    const nextIndex = index + direction;
    if (nextIndex < 0 || nextIndex >= payload.links.length) return;
    const links = [...payload.links];
    [links[index], links[nextIndex]] = [links[nextIndex], links[index]];
    try {
      const nextPayload = await quickLinksClient.reorder(links.map((link) => link.id));
      setReady({ payload: nextPayload, message: "Quick links reordered." });
    } catch (error) {
      setReady({ message: errorMessage(error) });
    }
  }

  async function refreshChecks() {
    try {
      const nextPayload = await quickLinksClient.check();
      setReady({ payload: nextPayload, message: "Quick links refreshed." });
    } catch (error) {
      setReady({ message: errorMessage(error) });
    }
  }

  return (
    <section className="settings-page" aria-labelledby="route-title">
      <header className="dashboard-header">
        <div>
          <p className="eyebrow">Hank</p>
          <h1 id="route-title">Quick Links</h1>
          <p className="meta-line">Shortcuts shown on your home dashboard.</p>
        </div>
        <div className="settings-actions">
          <button type="button" className="secondary" onClick={refreshChecks}>
            Refresh status checks
          </button>
          {payload.can_edit ? (
            <button type="button" aria-label="Add quick link" onClick={() => setReady({ form: { ...emptyInput }, message: "" })}>
              Add link
            </button>
          ) : null}
        </div>
      </header>

      {state.message ? <p className="notice-state">{state.message}</p> : null}

      {form ? (
        <form className="quick-link-form" onSubmit={saveQuickLink}>
          <label>
            <span>Name</span>
            <input
              maxLength={80}
              onChange={(event) => setReady({ form: { ...form, title: event.target.value } })}
              type="text"
              value={form.title}
            />
          </label>
          <label>
            <span>URL</span>
            <input
              maxLength={2048}
              inputMode="url"
              onChange={(event) => setReady({ form: { ...form, url: event.target.value } })}
              required
              type="text"
              value={form.url}
            />
          </label>
          <label>
            <span>Description</span>
            <input
              maxLength={180}
              onChange={(event) => setReady({ form: { ...form, description: event.target.value } })}
              type="text"
              value={form.description}
            />
          </label>
          <label className="checkbox-field">
            <input
              checked={form.health_check_enabled}
              onChange={(event) => setReady({ form: { ...form, health_check_enabled: event.target.checked } })}
              type="checkbox"
            />
            <span>Status checks</span>
          </label>
          <div className="form-actions">
            <button type="submit">Save</button>
            <button type="button" className="secondary" onClick={() => setReady({ form: null, message: "" })}>
              Cancel
            </button>
          </div>
        </form>
      ) : null}

      <section className="settings-panel" aria-label="Links">
        <div className="panel-heading">
          <h2>Links</h2>
          <span className="status-pill">{payload.links.length} link{payload.links.length === 1 ? "" : "s"}</span>
        </div>
        <div aria-label="Saved quick links" className="quick-links-list settings-list" role="list">
          {payload.links.length > 0 ? payload.links.map((link, index) => (
            <article className="quick-link-row" key={link.id} role="listitem">
              <a aria-label={link.title} className="quick-link-copy" href={link.url}>
                <strong>{link.title}</strong>
                <span>{link.description || link.url}</span>
                <small>{statusText(link)}</small>
              </a>
              {payload.can_edit ? (
                <div className="row-actions">
                  <button
                    aria-label={`Move ${link.title} up`}
                    className="secondary"
                    disabled={index === 0}
                    onClick={() => void moveQuickLink(index, -1)}
                    type="button"
                  >
                    Up
                  </button>
                  <button
                    aria-label={`Move ${link.title} down`}
                    className="secondary"
                    disabled={index === payload.links.length - 1}
                    onClick={() => void moveQuickLink(index, 1)}
                    type="button"
                  >
                    Down
                  </button>
                  <button type="button" className="secondary" onClick={() => setReady({ form: inputFromLink(link), message: "" })}>
                    Edit {link.title}
                  </button>
                  <button type="button" className="danger-link" onClick={() => void deleteQuickLink(link)}>
                    Delete {link.title}
                  </button>
                </div>
              ) : null}
            </article>
          )) : (
            <p className="empty-state">No quick links saved.</p>
          )}
        </div>
      </section>
    </section>
  );
}
