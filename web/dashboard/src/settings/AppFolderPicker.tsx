import { useCallback, useEffect, useState } from "react";
import { fileServerClient, type FileEntry } from "../api/fileServer";

export type AppFolderPickerProps = {
  disabled: boolean;
  label: string;
  sourceID: string;
  value: string;
  onChange: (path: string) => void;
};

type ListingState =
  | { status: "loading"; folders: FileEntry[]; message: "" }
  | { status: "ready"; folders: FileEntry[]; message: "" }
  | { status: "error"; folders: FileEntry[]; message: string };

export function normalizeFolderPath(value: string): string {
  const parts: string[] = [];
  for (const part of String(value || "").replaceAll("\\", "/").split("/")) {
    if (!part || part === ".") continue;
    if (part === "..") {
      parts.pop();
      continue;
    }
    parts.push(part);
  }
  return parts.length ? `/${parts.join("/")}` : "/";
}

export function parentFolderPath(value: string): string {
  const parts = normalizeFolderPath(value).split("/").filter(Boolean);
  parts.pop();
  return parts.length ? `/${parts.join("/")}` : "/";
}

export function folderBreadcrumbs(value: string): Array<{ label: string; path: string }> {
  const parts = normalizeFolderPath(value).split("/").filter(Boolean);
  return [
    { label: "Root", path: "/" },
    ...parts.map((label, index) => ({ label, path: `/${parts.slice(0, index + 1).join("/")}` })),
  ];
}

function errorMessage(error: unknown): string {
  return error instanceof Error && error.message ? error.message : "Folder listing failed.";
}

export function AppFolderPicker({ disabled, label, sourceID, value, onChange }: AppFolderPickerProps) {
  const [open, setOpen] = useState(false);
  const [path, setPath] = useState("/");
  const [listing, setListing] = useState<ListingState>({ status: "ready", folders: [], message: "" });

  const load = useCallback(async (nextPath: string) => {
    const normalizedPath = normalizeFolderPath(nextPath);
    setPath(normalizedPath);
    setListing((current) => ({ status: "loading", folders: current.folders, message: "" }));
    try {
      const payload = await fileServerClient.list(normalizedPath, sourceID);
      const folders = (payload.items || [])
        .filter((item) => item.is_directory)
        .slice()
        .sort((a, b) => String(a.name || a.path).localeCompare(String(b.name || b.path)));
      setListing({ status: "ready", folders, message: "" });
    } catch (error) {
      setListing({ status: "error", folders: [], message: errorMessage(error) });
    }
  }, [sourceID]);

  useEffect(() => {
    if (!open) return;
    void load(value || "/");
  }, [load, open, value]);

  function chooseFolder() {
    onChange(path);
    setOpen(false);
  }

  const chooserDisabled = disabled || !sourceID;

  return (
    <div className="app-folder-picker">
      <button
        aria-label={`Choose ${label} folder`}
        className="secondary"
        disabled={chooserDisabled}
        onClick={() => setOpen(true)}
        type="button"
      >
        {value || "Choose folder"}
      </button>
      {!sourceID ? <small>Select a source first.</small> : null}
      {open ? (
        <div className="guide-dialog-scrim" role="presentation" onClick={() => setOpen(false)}>
          <section
            aria-label={`Choose ${label} folder`}
            aria-modal="true"
            className="guide-dialog app-folder-dialog"
            onClick={(event) => event.stopPropagation()}
            role="dialog"
          >
            <header>
              <div>
                <p className="eyebrow">Existing folders</p>
                <h2>{label}</h2>
              </div>
              <button aria-label="Close folder picker" className="secondary" onClick={() => setOpen(false)} type="button">Close</button>
            </header>
            <nav aria-label="Folder breadcrumbs" className="app-folder-breadcrumbs">
              {folderBreadcrumbs(path).map((crumb) => (
                <button disabled={listing.status === "loading" || crumb.path === path} key={crumb.path} onClick={() => void load(crumb.path)} type="button">
                  {crumb.label}
                </button>
              ))}
            </nav>
            <p className="meta-line">Current folder: <code>{path}</code></p>
            {listing.status === "loading" ? <p className="loading-state">Loading folders...</p> : null}
            {listing.status === "error" ? (
              <div className="error-state">
                <p>{listing.message}</p>
                <button onClick={() => void load(path)} type="button">Retry</button>
              </div>
            ) : null}
            {listing.status === "ready" ? (
              listing.folders.length ? (
                <div aria-label="Folders" className="app-folder-list">
                  {listing.folders.map((folder) => {
                    const folderPath = normalizeFolderPath(folder.path);
                    const name = String(folder.name || folderPath.split("/").filter(Boolean).at(-1) || folderPath);
                    return <button aria-label={`Open ${name}`} key={folderPath} onClick={() => void load(folderPath)} type="button">{name}</button>;
                  })}
                </div>
              ) : <p className="empty-state">No folders inside this folder.</p>
            ) : null}
            <footer className="button-row">
              <button className="secondary" onClick={() => setOpen(false)} type="button">Cancel</button>
              <button disabled={listing.status !== "ready"} onClick={chooseFolder} type="button">Use this folder</button>
            </footer>
          </section>
        </div>
      ) : null}
    </div>
  );
}
