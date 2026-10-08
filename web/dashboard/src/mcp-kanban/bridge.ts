import type { KanbanToolName, ToolResult } from "./types";

export interface AppLike {
  ontoolresult?: (result: ToolResult) => void;
  callServerTool(params: { name: string; arguments: Record<string, unknown> }): Promise<ToolResult>;
}

export interface KanbanBridge {
  subscribe(listener: (result: ToolResult) => void): () => void;
  callTool<T>(name: KanbanToolName, args: Record<string, unknown>): Promise<ToolResult<T>>;
}

export function createKanbanBridge(app: AppLike): KanbanBridge {
  const listeners = new Set<(result: ToolResult) => void>();
  app.ontoolresult = (result) => {
    for (const listener of listeners) listener(result);
  };

  return {
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    async callTool<T>(name: KanbanToolName, args: Record<string, unknown>) {
      try {
        return await app.callServerTool({ name, arguments: args }) as ToolResult<T>;
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        throw new Error(`Hank tool call failed: ${message}`, { cause: error });
      }
    },
  };
}
