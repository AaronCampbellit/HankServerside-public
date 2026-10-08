import { useId, useState } from "react";
import { agentDisplayName, agentsClient, type HomeAgentEntry } from "../api/agents";
import { fileServerClient, type FileOperationJob } from "../api/fileServer";
import { useConfirmDialog } from "../ui/primitives";

export function FileJobOwnerAssignment({ job, onSaved }: { job: FileOperationJob; onSaved: () => void }) {
  const selectID = useId();
  const { confirm } = useConfirmDialog();
  const [agents, setAgents] = useState<HomeAgentEntry[] | null>(null);
  const [agentID, setAgentID] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");

  async function openReview() {
    setBusy(true);
    setMessage("");
    try { setAgents(await agentsClient.listAgents()); }
    catch (error) { setMessage(error instanceof Error ? error.message : "Machines could not be loaded."); }
    finally { setBusy(false); }
  }

  async function saveOwner() {
    const agent = agents?.find((candidate) => candidate.agent_id === agentID);
    if (!agent || !job.updated_at || busy) return;
    setBusy(true);
    setMessage("");
    try {
      const review = await fileServerClient.reviewJobOwner(job.id, agentID, job.updated_at);
      const accepted = await confirm({
        title: "Confirm the owning machine",
        message: `Confirm that ${agentDisplayName(agent)} (${agent.agent_id}) performed this move from ${job.source_id || "default source"}:${job.from_path || "/"} to ${job.destination_source_id || job.source_id || "default source"}:${job.to_path || "/"}. Saving the owner does not run recovery.`,
        confirmLabel: "Confirm owner",
      });
      if (!accepted) return;
      await fileServerClient.assignJobOwner(job.id, agentID, job.updated_at, review.admin_action_token);
      setAgents(null);
      onSaved();
    } catch (error) { setMessage(error instanceof Error ? error.message : "The owner could not be saved. Refresh and try again."); }
    finally { setBusy(false); }
  }

  return <div className="file-job-owner-review">
    <p>Owning machine unknown. An administrator must verify which machine performed this move before recovery.</p>
    {agents === null ? <button type="button" className="secondary" disabled={busy} onClick={() => void openReview()}>{busy ? "Loading machines…" : "Review owner"}</button> : <>
      <label htmlFor={selectID}>Machine that performed this move</label>
      <select id={selectID} value={agentID} disabled={busy} onChange={(event) => setAgentID(event.target.value)}>
        <option value="">Select a verified machine</option>
        {agents.map((agent) => <option key={agent.agent_id} value={agent.agent_id}>{agentDisplayName(agent)} · {agent.agent_id} · {agent.status}</option>)}
      </select>
      {!agents.length ? <p>No registered machines are available. Register the owning machine before assigning it.</p> : null}
      <div className="file-transfer-header-actions">
        <button type="button" disabled={busy || !agentID || !job.updated_at} onClick={() => void saveOwner()}>{busy ? "Reviewing…" : "Review and confirm"}</button>
        <button type="button" className="secondary" disabled={busy} onClick={() => { setAgents(null); setAgentID(""); setMessage(""); }}>Cancel</button>
      </div>
    </>}
    {message ? <p role="alert">{message}</p> : null}
  </div>;
}
