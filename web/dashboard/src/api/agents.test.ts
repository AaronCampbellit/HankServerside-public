import { describe, expect, it } from "vitest";
import {
  AgentsClient,
  agentDisplayName,
  agentHasCapability,
  agentIsOnline,
  agentIsPrimary,
  type HomeAgentEntry,
} from "./agents";
import type { HankSocketEvent } from "../socket/HankSocket";

function fakeSocket() {
  const commands: Array<{ command: string; body: unknown; agentID?: string }> = [];
  const subscriptions: string[][] = [];
  const listeners = new Set<(event: HankSocketEvent) => void>();
  const socket = {
    subscribe: async (topics: string[]) => {
      subscriptions.push(topics);
      return {};
    },
    sendCommand: async <T,>(command: string, body: unknown = {}, options: { agentID?: string } = {}) => {
      commands.push({ command, body, agentID: options.agentID });
      return {} as T;
    },
    onEvent: (listener: (event: HankSocketEvent) => void) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
  };
  return { socket, commands, subscriptions, emit: (event: HankSocketEvent) => listeners.forEach((l) => l(event)) };
}

describe("AgentsClient", () => {
  it("lists agents from the plural endpoint", async () => {
    const calls: string[] = [];
    const request = async <T,>(path: string) => {
      calls.push(path);
      return { agents: [{ agent_id: "a1", status: "online" }] } as T;
    };
    const { socket } = fakeSocket();
    const client = new AgentsClient({ request }, socket);
    const agents = await client.listAgents();
    expect(calls).toEqual(["/v1/home/agents"]);
    expect(agents).toHaveLength(1);
  });

  it("targets commands at a specific agent", async () => {
    const request = async <T,>() => ({}) as T;
    const { socket, commands, subscriptions } = fakeSocket();
    const client = new AgentsClient({ request }, socket);

    await client.subscribeHealth();
    await client.lock("mac-1");
    await client.restart("mac-1");
    await client.wakeOnLAN("mac-1", "AA:BB:CC:DD:EE:FF");
    await client.runShell("mac-1", "uptime");

    expect(subscriptions).toEqual([["agents.health"]]);
    expect(commands.map((c) => [c.command, c.agentID])).toEqual([
      ["host.lock", "mac-1"],
      ["system.restart", "mac-1"],
      ["wol.send", "mac-1"],
      ["shell.exec", "mac-1"],
    ]);
    expect(commands[2].body).toEqual({ mac: "AA:BB:CC:DD:EE:FF" });
    expect(commands[3].body).toEqual({ command: "uptime", timeout_seconds: 60 });
  });

  it("uses the one-time Linux enrollment and credential control endpoints", async () => {
	const calls: Array<{ path: string; method?: string; body?: unknown }> = [];
	const request = async <T,>(path: string, options: { method?: string; body?: unknown } = {}) => {
		calls.push({ path, method: options.method, body: options.body });
		if (path === "/v1/home/agent-enrollments/linux" && options.method === "POST") {
			return { id: "aenroll_1", install_command: "curl -fsSL https://hank.example/install/linux/secret | sudo bash", created_at: "2026-08-13T00:00:00Z", expires_at: "2026-08-13T00:15:00Z" } as T;
		}
		return { enrollments: [] } as T;
	};
	const { socket } = fakeSocket();
	const client = new AgentsClient({ request }, socket);

	await client.listEnrollments();
	await client.createEnrollment("demo server");
	await client.revokeEnrollment("aenroll_1");
	await client.requestCredentialRotation("linux_1");
	await client.revokeAgentCredentials("linux_1");

	expect(calls).toEqual([
		{ path: "/v1/home/agent-enrollments", method: undefined, body: undefined },
		{ path: "/v1/home/agent-enrollments/linux", method: "POST", body: { name_hint: "demo server", pairing: false } },
		{ path: "/v1/home/agent-enrollments/linux/aenroll_1", method: "DELETE", body: undefined },
		{ path: "/v1/home/agents/linux_1/credentials/rotation-request", method: "POST", body: {} },
		{ path: "/v1/home/agents/linux_1/credentials", method: "DELETE", body: undefined },
	]);
  });

  it("opens, attaches, writes, resizes, and closes an interactive terminal", async () => {
    const request = async <T,>() => ({}) as T;
    const { socket, commands, subscriptions } = fakeSocket();
    const client = new AgentsClient({ request }, socket);

    await client.openTerminal("mac-1", "term_test_0001", 100, 30);
    await client.subscribeTerminal("term_test_0001");
    await client.attachTerminal("mac-1", "term_test_0001", 12);
    await client.writeTerminal("mac-1", "term_test_0001", "pwd\n");
    await client.resizeTerminal("mac-1", "term_test_0001", 120, 40);
    await client.closeTerminal("mac-1", "term_test_0001");

    expect(subscriptions).toContainEqual(["shell.session:term_test_0001"]);
    expect(commands.slice(-5).map((value) => value.command)).toEqual([
      "shell.session.open", "shell.session.attach", "shell.session.input", "shell.session.resize", "shell.session.close",
    ]);
    expect(commands.at(-3)?.body).toEqual({ session_id: "term_test_0001", data: "pwd\n" });
  });

  it("decodes agents.health alerts", async () => {
    const request = async <T,>() => ({}) as T;
    const { socket, emit } = fakeSocket();
    const client = new AgentsClient({ request }, socket);
    const received: string[] = [];
    client.onAlert((alert) => received.push(`${alert.agent_id}:${alert.kind}`));

    emit({ topic: "agents.health", event: "agent.offline", body: { agent_id: "mac-1", kind: "agent.offline", severity: "warning", summary: "x" } });
    emit({ topic: "homeassistant.states", event: "x", body: {} });

    expect(received).toEqual(["mac-1:agent.offline"]);
  });
});

describe("agent helpers", () => {
  const worker: HomeAgentEntry = {
    agent_id: "mac-1",
    status: "online",
    agent_type: "worker",
    capabilities: ["files.read", "shell.exec"],
    metadata: { hostname: "studio.local" },
  };

  it("resolves display name, type, online, capabilities", () => {
    expect(agentDisplayName(worker)).toBe("studio.local");
    expect(agentDisplayName({ ...worker, name: "Studio" })).toBe("Studio");
    expect(agentIsPrimary(worker)).toBe(false);
    expect(agentIsPrimary({ ...worker, agent_type: "primary" })).toBe(true);
    expect(agentIsPrimary({ ...worker, agent_type: undefined })).toBe(true);
    expect(agentIsOnline(worker)).toBe(true);
    expect(agentHasCapability(worker, "shell.exec")).toBe(true);
    expect(agentHasCapability(worker, "docker.control")).toBe(false);
  });
});
