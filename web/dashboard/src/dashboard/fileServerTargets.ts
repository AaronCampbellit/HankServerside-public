import {
  agentDisplayName,
  agentHasCapability,
  agentIsOnline,
  agentIsPrimary,
  agentsClient,
  type HomeAgentEntry,
} from "../api/agents";
import { connectionsClient, type ServiceProfile } from "../api/connections";

export type FileTarget = {
  key: string;
  sourceID: string;
  agentID: string;
  name: string;
  detail: string;
  kind: "smb" | "host" | "worker";
};

type ConnectionsSource = Pick<typeof connectionsClient, "listProfiles">;
type AgentsSource = Pick<typeof agentsClient, "listAgents">;

function firstString(...values: unknown[]): string {
  for (const value of values) {
    if (typeof value === "string" && value.trim()) return value.trim();
  }
  return "";
}

function records(value: unknown): Record<string, unknown>[] {
  return Array.isArray(value)
    ? value.filter((entry): entry is Record<string, unknown> => Boolean(entry) && typeof entry === "object")
    : [];
}

function parseConfig(profile: ServiceProfile): Record<string, unknown> {
  try {
    const value = JSON.parse(profile.public_config_json || "{}") as unknown;
    return value && typeof value === "object" ? value as Record<string, unknown> : {};
  } catch {
    return {};
  }
}

function primaryTarget(record: Record<string, unknown>): FileTarget | null {
  const sourceID = firstString(record.id, record.source_id, record.key, record.name);
  if (!sourceID || record.enabled === false) return null;
  const type = firstString(record.type, record.service_type).toLowerCase();
  const host = firstString(record.host, record.smb_host);
  const share = firstString(record.share, record.smb_share);
  const root = firstString(record.root, record.path);
  const kind: FileTarget["kind"] = type === "local" || (!host && !share && Boolean(root)) ? "host" : "smb";
  if (kind === "host" && record.local_root_enabled === false) return null;
  if (kind === "smb" && record.smb_enabled === false) return null;
  return {
    key: `primary:${sourceID}`,
    sourceID,
    agentID: "",
    name: firstString(record.label, record.name, share, sourceID),
    detail: kind === "host" ? firstString(root, "Host folder") : host && share ? `//${host}/${share}` : firstString(share, "SMB source"),
    kind,
  };
}

export function fileTargetsFrom(profiles: ServiceProfile[], agents: HomeAgentEntry[]): FileTarget[] {
  const targets = new Map<string, FileTarget>();
  const profile = profiles.find((candidate) => candidate.service_type === "smb");
  if (profile) {
    const config = parseConfig(profile);
    for (const record of [
      ...records(config.sources),
      ...records(config.file_sources),
      ...records(config.shares),
      ...records(config.folders),
    ]) {
      const target = primaryTarget(record);
      if (target && !targets.has(target.key)) targets.set(target.key, target);
    }
  }

  for (const agent of agents) {
    const fileCapable = agentHasCapability(agent, "files.read") || agentHasCapability(agent, "files.list");
    if (agentIsPrimary(agent) || !agentIsOnline(agent) || !fileCapable) continue;
    const target: FileTarget = {
      key: `worker:${agent.agent_id}`,
      sourceID: "",
      agentID: agent.agent_id,
      name: agentDisplayName(agent),
      detail: "Worker shared folders",
      kind: "worker",
    };
    targets.set(target.key, target);
  }

  return Array.from(targets.values());
}

export async function loadFileTargets(
  connections: ConnectionsSource = connectionsClient,
  agents: AgentsSource = agentsClient,
): Promise<FileTarget[]> {
  const [profilesResult, agentsResult] = await Promise.allSettled([
    connections.listProfiles(),
    agents.listAgents(),
  ]);
  const profiles = profilesResult.status === "fulfilled" ? profilesResult.value.profiles : [];
  const agentEntries = agentsResult.status === "fulfilled" ? agentsResult.value : [];
  return fileTargetsFrom(profiles, agentEntries);
}

export async function loadPrimaryFileTargets(
  connections: ConnectionsSource = connectionsClient,
  agents: AgentsSource = agentsClient,
): Promise<FileTarget[]> {
  return (await loadFileTargets(connections, agents)).filter((target) => !target.agentID);
}
