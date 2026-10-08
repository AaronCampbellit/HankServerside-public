import { describe, expect, it } from "vitest";
import {
  newShareDraft,
  removeSMBShare,
  smbSourceRecords,
  upsertSMBShare,
  validateSMBShare,
} from "./smbShareEditor";

const config = {
  active_source_id: "media",
  host: "nas.local",
  share: "media",
  username: "aaron",
  folders: [{ id: "local-media", root: "/srv/media" }],
  shares: [
    { id: "media", name: "Media", host: "nas.local", share: "media", username: "aaron", password_set: true, policy: { delete: false } },
    { id: "archive", name: "Archive", host: "backup.local", share: "archive", username: "aaron", password_set: true, policy: { write: false } },
  ],
};

describe("SMB share editor", () => {
  it("parses every SMB share", () => {
    expect(smbSourceRecords(config).map((share) => share.id)).toEqual(["media", "archive"]);
  });

  it("falls back to legacy share lists when canonical shares are empty", () => {
    expect(smbSourceRecords({
      shares: [],
      file_sources: [{ id: "legacy", name: "Legacy", host: "legacy.local", share: "files" }],
    }).map((share) => share.id)).toEqual(["legacy"]);
  });

  it("updates a selected share without changing other shares or unrelated config", () => {
    const updated = upsertSMBShare(config, {
      id: "archive",
      name: "Cold Archive",
      host: "archive.local",
      share: "vault",
      domain: "WORKGROUP",
      username: "backup",
      password_set: true,
    }, "archive");

    expect(updated.folders).toEqual(config.folders);
    expect(updated.active_source_id).toBe("media");
    expect(updated.host).toBe("nas.local");
    expect(updated.shares).toEqual([
      config.shares[0],
      {
        ...config.shares[1],
        id: "archive",
        name: "Cold Archive",
        host: "archive.local",
        share: "vault",
        domain: "WORKGROUP",
        username: "backup",
      },
    ]);
  });

  it("appends a new share with a collision-safe generated id", () => {
    const draft = newShareDraft(smbSourceRecords(config));
    expect(draft.id).toBe("smb-1");

    const updated = upsertSMBShare(config, { ...draft, name: "Projects", host: "projects.local", share: "projects" });
    expect((updated.shares as Array<{ id: string }>).map((share) => share.id)).toEqual(["media", "archive", "smb-1"]);
    expect(updated.active_source_id).toBe("media");
  });

  it("removes only the selected share and falls back when it was active", () => {
    const updated = removeSMBShare(config, "media");
    expect(updated.shares).toEqual([config.shares[1]]);
    expect(updated.active_source_id).toBe("archive");
    expect(updated.host).toBe("backup.local");
    expect(updated.share).toBe("archive");
    expect(updated.folders).toEqual(config.folders);
  });

  it("keeps the active share when removing a different share", () => {
    const updated = removeSMBShare(config, "archive");
    expect(updated.active_source_id).toBe("media");
    expect(updated.host).toBe("nas.local");
  });

  it("validates required fields and duplicate ids", () => {
    const shares = smbSourceRecords(config);
    expect(validateSMBShare({ id: "media", name: "Other", host: "nas2.local", share: "other", domain: "", username: "" }, shares)).toBe("Share ID is already in use.");
    expect(validateSMBShare({ id: "new", name: "", host: "nas2.local", share: "other", domain: "", username: "" }, shares)).toBe("Share label is required.");
    expect(validateSMBShare({ id: "new", name: "New", host: "", share: "other", domain: "", username: "" }, shares)).toBe("Server address is required.");
    expect(validateSMBShare({ id: "new", name: "New", host: "nas2.local", share: "", domain: "", username: "" }, shares)).toBe("Share name is required.");
    expect(validateSMBShare({ id: "media", name: "Media", host: "nas.local", share: "media", domain: "", username: "" }, shares, "media")).toBe("");
  });
});
