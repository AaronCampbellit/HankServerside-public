import { agentsClient, type TerminalAttach, type TerminalEvent, type TerminalOpen } from "./agents";

export type TerminalStatus = "closed" | "connecting" | "live" | "reconnecting" | "exited" | "error";
export type TerminalSnapshot = {
  agentID: string;
  sessionID: string | null;
  output: string;
  cursor: number;
  status: TerminalStatus;
  exitCode?: number;
  error?: string;
};

export type TerminalTransport = {
  openTerminal(agentID: string, sessionID: string, columns: number, rows: number): Promise<TerminalOpen>;
  subscribeTerminal(sessionID: string): Promise<unknown>;
  attachTerminal(agentID: string, sessionID: string, afterCursor: number): Promise<TerminalAttach>;
  writeTerminal(agentID: string, sessionID: string, data: string): Promise<unknown>;
  resizeTerminal(agentID: string, sessionID: string, columns: number, rows: number): Promise<unknown>;
  closeTerminal(agentID: string, sessionID: string): Promise<unknown>;
  onTerminalEvent(sessionID: string, listener: (event: TerminalEvent) => void, onReconnect?: () => void): () => void;
};

type Stored = TerminalSnapshot & { unsubscribe?: () => void };

function blank(agentID: string): Stored {
  return { agentID, sessionID: null, output: "", cursor: 0, status: "closed" };
}

function randomSessionID(): string {
  return `term_${crypto.randomUUID().replaceAll("-", "")}`;
}

export class TerminalStore {
  private sessions = new Map<string, Stored>();
  private listeners = new Map<string, Set<() => void>>();

  constructor(private readonly transport: TerminalTransport = agentsClient, private readonly makeID = randomSessionID) {}

  snapshot(agentID: string): TerminalSnapshot {
    let session = this.sessions.get(agentID);
    if (!session) {
      session = blank(agentID);
      this.sessions.set(agentID, session);
    }
    return session;
  }

  subscribe(agentID: string, listener: () => void): () => void {
    const listeners = this.listeners.get(agentID) ?? new Set();
    listeners.add(listener);
    this.listeners.set(agentID, listeners);
    return () => listeners.delete(listener);
  }

  async open(agentID: string, columns: number, rows: number): Promise<void> {
    const current = this.sessions.get(agentID);
    if (current?.sessionID && current.status !== "closed" && current.status !== "exited" && current.status !== "error") {
      await this.attach(agentID);
      return;
    }
    current?.unsubscribe?.();
    const sessionID = this.makeID();
    this.set(agentID, { agentID, sessionID, output: "", cursor: 0, status: "connecting" });
    try {
      await this.transport.openTerminal(agentID, sessionID, columns, rows);
      await this.installSubscription(agentID, sessionID);
      await this.attach(agentID);
    } catch (error) {
      this.fail(agentID, error);
      throw error;
    }
  }

  async attach(agentID: string): Promise<void> {
    const session = this.sessions.get(agentID);
    if (!session?.sessionID || session.status === "closed" || session.status === "exited") return;
    const requestedCursor = session.cursor;
    this.set(agentID, { ...session, status: session.status === "connecting" ? "connecting" : "reconnecting", error: undefined });
    try {
      await this.transport.subscribeTerminal(session.sessionID);
      const attached = await this.transport.attachTerminal(agentID, session.sessionID, requestedCursor);
      const latest = this.sessions.get(agentID) ?? session;
      this.set(agentID, {
        ...latest,
        output: latest.cursor === requestedCursor ? latest.output + (attached.output ?? "") : latest.output,
        cursor: latest.cursor === requestedCursor ? Math.max(latest.cursor, attached.cursor) : latest.cursor,
        status: attached.exited ? "exited" : "live",
        exitCode: attached.exit_code,
      });
    } catch (error) {
      this.fail(agentID, error, "reconnecting");
    }
  }

  async write(agentID: string, data: string): Promise<void> {
    const session = this.requireLive(agentID);
    try { await this.transport.writeTerminal(agentID, session.sessionID!, data); }
    catch (error) { this.fail(agentID, error, "reconnecting"); throw error; }
  }

  async resize(agentID: string, columns: number, rows: number): Promise<void> {
    const session = this.sessions.get(agentID);
    if (!session?.sessionID || session.status === "closed" || session.status === "exited") return;
    await this.transport.resizeTerminal(agentID, session.sessionID, Math.max(20, Math.min(500, columns)), Math.max(5, Math.min(500, rows)));
  }

  async close(agentID: string): Promise<void> {
    const session = this.sessions.get(agentID);
    session?.unsubscribe?.();
    if (session?.sessionID && session.status !== "closed") {
      try { await this.transport.closeTerminal(agentID, session.sessionID); } catch { /* Agent exit already closes it. */ }
    }
    this.set(agentID, { ...blank(agentID), status: "closed" });
  }

  private async installSubscription(agentID: string, sessionID: string) {
    const unsubscribe = this.transport.onTerminalEvent(sessionID, (event) => this.receive(agentID, event), () => { void this.attach(agentID); });
    const current = this.sessions.get(agentID) ?? blank(agentID);
    current.unsubscribe?.();
    this.set(agentID, { ...current, unsubscribe });
  }

  private receive(agentID: string, event: TerminalEvent) {
    const session = this.sessions.get(agentID);
    if (!session || session.sessionID !== event.session_id || event.cursor <= session.cursor) return;
    this.set(agentID, {
      ...session,
      output: session.output + (event.data ?? ""),
      cursor: event.cursor,
      status: event.exited ? "exited" : "live",
      exitCode: event.exit_code,
    });
  }

  private requireLive(agentID: string): Stored {
    const session = this.sessions.get(agentID);
    if (!session?.sessionID || (session.status !== "live" && session.status !== "reconnecting")) throw new Error("Terminal is not open.");
    return session;
  }

  private fail(agentID: string, error: unknown, status: TerminalStatus = "error") {
    const current = this.sessions.get(agentID) ?? blank(agentID);
    this.set(agentID, { ...current, status, error: error instanceof Error ? error.message : "Terminal connection failed." });
  }

  private set(agentID: string, value: Stored) {
    this.sessions.set(agentID, value);
    this.listeners.get(agentID)?.forEach((listener) => listener());
  }
}

export const terminalStore = new TerminalStore();
