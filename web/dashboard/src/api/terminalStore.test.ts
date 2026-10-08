import { describe, expect, it } from "vitest";
import { TerminalStore, type TerminalTransport } from "./terminalStore";
import type { TerminalEvent } from "./agents";

function transport(): TerminalTransport & { emit(event: TerminalEvent): void; calls: string[] } {
  let listener: ((event: TerminalEvent) => void) | undefined;
  const calls: string[] = [];
  return {
    calls,
    openTerminal: async () => { calls.push("open"); return { session_id: "term_fixed_0001", cursor: 0 }; },
    subscribeTerminal: async () => { calls.push("subscribe"); return {}; },
    attachTerminal: async (_agent, _session, cursor) => { calls.push(`attach:${cursor}`); return { session_id: "term_fixed_0001", cursor: 5, output: "hello" }; },
    writeTerminal: async (_agent, _session, data) => { calls.push(`write:${data}`); return {}; },
    resizeTerminal: async (_agent, _session, columns, rows) => { calls.push(`resize:${columns}x${rows}`); return {}; },
    closeTerminal: async () => { calls.push("close"); return {}; },
    onTerminalEvent: (_session, next) => { listener = next; return () => { listener = undefined; }; },
    emit: (event) => listener?.(event),
  };
}

describe("TerminalStore", () => {
  it("retains ordered output and reattaches from the last cursor", async () => {
    const client = transport();
    const store = new TerminalStore(client, () => "term_fixed_0001");
    await store.open("agent-1", 80, 24);
    expect(store.snapshot("agent-1")).toMatchObject({ output: "hello", cursor: 5, status: "live" });

    client.emit({ session_id: "term_fixed_0001", cursor: 7, data: "!!", exited: false });
    client.emit({ session_id: "term_fixed_0001", cursor: 7, data: "!!", exited: false });
    expect(store.snapshot("agent-1").output).toBe("hello!!");

    await store.attach("agent-1");
    expect(client.calls).toContain("attach:7");
  });

  it("sends raw input, resize, and explicit close", async () => {
    const client = transport();
    const store = new TerminalStore(client, () => "term_fixed_0001");
    await store.open("agent-1", 80, 24);
    await store.write("agent-1", "\u0003");
    await store.resize("agent-1", 120, 40);
    await store.close("agent-1");
    expect(client.calls).toEqual(expect.arrayContaining(["write:\u0003", "resize:120x40", "close"]));
    expect(store.snapshot("agent-1").status).toBe("closed");
  });

  it("does not duplicate replay that arrives as a live event during attach", async () => {
    const client = transport();
    const store = new TerminalStore(client, () => "term_fixed_0001");
    await store.open("agent-1", 80, 24);

    let release!: () => void;
    client.attachTerminal = async () => {
      await new Promise<void>((resolve) => { release = resolve; });
      return { session_id: "term_fixed_0001", cursor: 7, output: "!!" };
    };
    const attaching = store.attach("agent-1");
    await Promise.resolve();
    client.emit({ session_id: "term_fixed_0001", cursor: 7, data: "!!", exited: false });
    release();
    await attaching;

    expect(store.snapshot("agent-1")).toMatchObject({ output: "hello!!", cursor: 7, status: "live" });
  });
});
