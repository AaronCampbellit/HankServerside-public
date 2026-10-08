import { useState } from "react";
import { agentDisplayName, agentIsOnline, agentIsPrimary, type HomeAgentEntry } from "../api/agents";
import "./AgentFleetMap.css";

function usage(used?: number, total?: number): string {
  return typeof used === "number" && typeof total === "number" && total > 0
    ? `${Math.round(used / total * 100)}%` : "Unavailable";
}

export function AgentFleetMap({ agents }: { agents: HomeAgentEntry[] }) {
  const [selectedID, setSelectedID] = useState<string | null>(null);
  const selected = agents.find((agent) => agent.agent_id === selectedID);
  const online = agents.filter(agentIsOnline).length;
  const rows = Math.ceil(agents.length / 2);
  return (
    <section className="agent-panel fleet-map" aria-labelledby="fleet-map-title">
      <div className="agent-panel-head">
        <div><p className="eyebrow">Your Home</p><h2 id="fleet-map-title">Fleet connections</h2>
          <p className="meta-line">{online} online · {agents.length - online} offline · Select a device to inspect it.</p></div>
      </div>
      <p className="meta-line">Agents connect through HankServerside. Solid lines show connected agents; dashed lines show offline registrations.</p>
      <div className="fleet-map-layout">
        <div className="fleet-map-scroll" role="group" aria-label="Agent connection diagram">
          <div className="fleet-map-canvas" style={{ height: 120 + rows * 112 }}>
            <svg className="fleet-map-lines" width="100%" height="100%" aria-hidden="true">
              {agents.map((agent, index) => <g key={agent.agent_id} className={agentIsOnline(agent) ? "is-online" : "is-offline"}>
                <line x1="50%" y1="72" x2="50%" y2={160 + Math.floor(index / 2) * 112} />
                <line x1="50%" y1={160 + Math.floor(index / 2) * 112} x2={index % 2 ? "75%" : "25%"} y2={160 + Math.floor(index / 2) * 112} />
              </g>)}
            </svg>
            <div className="fleet-map-hub"><strong>HankServerside</strong><span>Home relay</span></div>
            {agents.map((agent, index) => <button type="button" key={agent.agent_id}
              className={`fleet-map-node ${agentIsOnline(agent) ? "is-online" : "is-offline"}`}
              style={{ top: 120 + Math.floor(index / 2) * 112, left: index % 2 ? "75%" : "25%" }}
              aria-label={`${agentDisplayName(agent)} ${agentIsOnline(agent) ? "Online" : "Offline"} · ${agentIsPrimary(agent) ? "Primary" : "Worker"}`}
              aria-pressed={selected?.agent_id === agent.agent_id} aria-controls="fleet-map-inspector"
              onClick={() => setSelectedID(agent.agent_id)}>
              <strong>{agentDisplayName(agent)}</strong>
              <span>{agentIsOnline(agent) ? "Online" : "Offline"} · {agentIsPrimary(agent) ? "Primary" : "Worker"}</span>
            </button>)}
          </div>
        </div>
        <div id="fleet-map-inspector" className="fleet-map-inspector" aria-live="polite">
          {selected ? <>
            <h3>{agentDisplayName(selected)}</h3>
            <p>{agentIsOnline(selected) ? "Connected to HankServerside" : "Offline · no active connection"}</p>
            <dl>
              <dt>Platform</dt><dd>{selected.metadata?.os_version || selected.metadata?.platform || "Unavailable"}</dd>
              <dt>CPU load (1m)</dt><dd>{agentIsOnline(selected) ? selected.metrics?.cpu_load_1m?.toFixed(2) ?? "Unavailable" : "Unavailable"}</dd>
              <dt>Memory used</dt><dd>{agentIsOnline(selected) ? usage(selected.metrics?.memory_used_bytes, selected.metrics?.memory_total_bytes) : "Unavailable"}</dd>
              <dt>Disk used</dt><dd>{agentIsOnline(selected) ? usage(selected.metrics?.disk_used_bytes, selected.metrics?.disk_total_bytes) : "Unavailable"}</dd>
              <dt>Last seen</dt><dd>{selected.last_seen_at && Number.isFinite(Date.parse(selected.last_seen_at)) ? new Date(selected.last_seen_at).toLocaleString() : "Unavailable"}</dd>
            </dl>
            <a className="button secondary" href={`/dashboard/agents/${encodeURIComponent(selected.agent_id)}`}>Open device workspace</a>
          </> : <><h3>Select a device</h3><p>Inspect its connection, CPU load, memory, and disk usage.</p></>}
        </div>
      </div>
    </section>
  );
}
