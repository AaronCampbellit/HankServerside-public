import { describe, expect, it, vi } from "vitest";
import type { HomeAgentEntry } from "../api/agents";
import type { ServiceProfile } from "../api/connections";
import { fileTargetsFrom, loadFileTargets, loadPrimaryFileTargets } from "./fileServerTargets";

function profile(config: Record<string, unknown>): ServiceProfile {
  return {
    home_id: "home-1",
    service_type: "smb",
    public_config_json: JSON.stringify(config),
    secret_version: 1,
    applied_version: 1,
    status: "healthy",
    updated_at: "2026-07-13T12:00:00Z",
    updated_by: "admin",
  };
}

describe("file server target discovery", () => {
  it("keeps primary SMB and host sources while collapsing duplicate source ids", () => {
    const targets = fileTargetsFrom([
      profile({
        sources: [
          { id: "media", name: "Media", type: "smb", smb_host: "nas.local", smb_share: "media" },
          { id: "documents", name: "Documents", type: "local", root: "/srv/documents" },
        ],
        file_sources: [
          { id: "media", name: "Duplicate Media", type: "smb", smb_host: "ignored.local", smb_share: "ignored" },
        ],
        shares: [{ id: "archive", name: "Archive", host: "nas.local", share: "archive" }],
        folders: [{ id: "photos", name: "Photos", root: "/srv/photos" }],
      }),
    ], []);

    expect(targets).toEqual([
      { key: "primary:media", sourceID: "media", agentID: "", name: "Media", detail: "//nas.local/media", kind: "smb" },
      { key: "primary:documents", sourceID: "documents", agentID: "", name: "Documents", detail: "/srv/documents", kind: "host" },
      { key: "primary:archive", sourceID: "archive", agentID: "", name: "Archive", detail: "//nas.local/archive", kind: "smb" },
      { key: "primary:photos", sourceID: "photos", agentID: "", name: "Photos", detail: "/srv/photos", kind: "host" },
    ]);
  });

  it("adds only online file-capable workers as virtual-root targets", () => {
    const agents: HomeAgentEntry[] = [
      { agent_id: "primary-1", name: "Home", status: "online", agent_type: "primary", capabilities: ["files.list"] },
      { agent_id: "mac-1", name: "Studio Mac", status: "online", agent_type: "worker", capabilities: ["files.read", "files.write"] },
      { agent_id: "win-1", status: "online", agent_type: "worker", metadata: { hostname: "Office PC" }, capabilities: ["files.list"] },
      { agent_id: "offline-1", name: "Offline", status: "offline", agent_type: "worker", capabilities: ["files.read"] },
      { agent_id: "shell-1", name: "Shell only", status: "online", agent_type: "worker", capabilities: ["shell.exec"] },
    ];

    expect(fileTargetsFrom([], agents)).toEqual([
      { key: "worker:mac-1", sourceID: "", agentID: "mac-1", name: "Studio Mac", detail: "Worker shared folders", kind: "worker" },
      { key: "worker:win-1", sourceID: "", agentID: "win-1", name: "Office PC", detail: "Worker shared folders", kind: "worker" },
    ]);
  });

  it("keeps whichever discovery path succeeds", async () => {
    const connections = { listProfiles: vi.fn().mockRejectedValue(new Error("profiles unavailable")) };
    const agents = {
      listAgents: vi.fn().mockResolvedValue([
        { agent_id: "mac-1", name: "Studio Mac", status: "online", agent_type: "worker", capabilities: ["files.read"] },
      ]),
    };

    await expect(loadFileTargets(connections, agents)).resolves.toEqual([
      { key: "worker:mac-1", sourceID: "", agentID: "mac-1", name: "Studio Mac", detail: "Worker shared folders", kind: "worker" },
    ]);
  });

  it("loads only configured primary-agent sources for installable apps", async () => {
    const connections = {
      listProfiles: vi.fn().mockResolvedValue({
        profiles: [profile({
          sources: [
            { id: "movies", name: "Movies share", type: "smb", host: "nas.local", share: "movies" },
            { id: "tv", name: "TV host folder", type: "local", root: "/srv/tv" },
          ],
        })],
      }),
    };
    const agents = {
      listAgents: vi.fn().mockResolvedValue([
        { agent_id: "worker-1", name: "Worker", status: "online", agent_type: "worker", capabilities: ["files.list"] },
      ]),
    };

    await expect(loadPrimaryFileTargets(connections, agents)).resolves.toEqual([
      { key: "primary:movies", sourceID: "movies", agentID: "", name: "Movies share", detail: "//nas.local/movies", kind: "smb" },
      { key: "primary:tv", sourceID: "tv", agentID: "", name: "TV host folder", detail: "/srv/tv", kind: "host" },
    ]);
  });
});
