import { StrictMode, useState } from "react";
import { createRoot } from "react-dom/client";
import { useApp } from "@modelcontextprotocol/ext-apps/react";
import type { App } from "@modelcontextprotocol/ext-apps";
import { createKanbanBridge, type AppLike, type KanbanBridge } from "./bridge";
import { KanbanApp } from "./KanbanApp";
import { KanbanErrorBoundary } from "./KanbanErrorBoundary";

function MCPKanbanRoot() {
  const [bridge, setBridge] = useState<KanbanBridge | null>(null);
  const { error } = useApp({
    appInfo: { name: "Hank Kanban", version: "1.0.0" },
    capabilities: {},
    onAppCreated: (app: App) => setBridge(createKanbanBridge(app as unknown as AppLike)),
  });

  if (error) {
    return <main className="kanban-app kanban-centered" role="alert">Could not connect to Hank: {error.message}</main>;
  }
  if (!bridge) {
    return <main className="kanban-app kanban-centered" aria-live="polite">Connecting to Hank…</main>;
  }
  return <KanbanApp bridge={bridge} />;
}

const root = document.getElementById("hank-kanban-root");
if (!root) throw new Error("Hank Kanban root element was not found.");
createRoot(root).render(
  <StrictMode>
    <KanbanErrorBoundary><MCPKanbanRoot /></KanbanErrorBoundary>
  </StrictMode>,
);
