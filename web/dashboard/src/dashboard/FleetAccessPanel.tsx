import "./FleetAccessPanel.css";
import { useEffect, useState, type FormEvent } from "react";
import { apiClient } from "../api/client";
import type { HomeAgentEntry } from "../api/agents";
import { useConfirmDialog } from "../ui/primitives";

type Grant = {
  id: string; user_id: string; requester_name?: string; agents: string[];
  operations: string[]; state: string; expires_at: string | null;
  mcp_account?: boolean; mcp_token_id?: string;
};
function expiryLabel(value: string | null) {
  return value === null ? "No expiration (until revoked)" : `Expires ${new Date(value).toLocaleString()}`;
}
function isExpired(grant: Grant) {
  return grant.expires_at !== null && new Date(grant.expires_at).getTime() <= Date.now();
}
function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : "Account device access unavailable";
}

export function FleetAccessPanel({ agents, isAdmin, userID }: { agents: HomeAgentEntry[]; isAdmin: boolean; userID: string }) {
  const [grants, setGrants] = useState<Grant[]>([]);
  const [duration, setDuration] = useState("1_month");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [loadFailed, setLoadFailed] = useState(false);
  const { confirm } = useConfirmDialog();
  async function load() {
    const access = await apiClient.request<{ grants: Grant[] }>("/v1/fleet/grants");
    setGrants(access.grants || []); setLoadFailed(false);
  }
  useEffect(() => {
    let active = true;
    void apiClient.request<{ grants: Grant[] }>("/v1/fleet/grants").then((access) => {
      if (active) setGrants(access.grants || []);
    }).catch((error: unknown) => {
      if (active) { setMessage(errorMessage(error)); setLoadFailed(true); }
    }).finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, []);
  async function review(grant: Grant, action: "approve" | "revoke") {
    const path = `/v1/fleet/grants/${encodeURIComponent(grant.id)}/${action}`;
    const review = await apiClient.request<{ confirmation: string; action_token: string; authority: string }>(path, { method: "POST", body: {} });
    const owner = grant.user_id === userID ? "Your account" : grant.requester_name || grant.user_id;
    const scope = grant.mcp_account
      ? `${owner}: all current and future Home devices, with full file and command access across every MCP app connected to this account.`
      : `${owner}: ${grant.agents.map((id) => agents.find((agent) => agent.agent_id === id)?.name || id).join(", ")}. Permissions: ${grant.operations.join(", ")}.`;
    if (!await confirm({
      title: action === "approve" ? "Enable device access?" : "Revoke device access?",
      message: `${scope} ${expiryLabel(grant.expires_at)}. ${grant.operations.includes("job.run") ? review.authority : ""}`,
      confirmLabel: action === "approve" ? "Enable access" : "Revoke access", tone: "danger",
    })) return false;
    await apiClient.request(path, { method: "POST", body: { confirmation: review.confirmation, action_token: review.action_token } });
    return true;
  }
  async function create(event: FormEvent) {
    event.preventDefault(); setBusy(true); setMessage("");
    try {
      const result = await apiClient.request<{ grant: Grant }>("/v1/fleet/grants", { method: "POST", body: { mcp_account: true, duration } });
      const approved = isAdmin && await review(result.grant, "approve");
      await load();
      setMessage(approved ? "Device access enabled for your account and all its MCP apps." : "Request saved. A Home administrator must approve it.");
    } catch (error) { setMessage(errorMessage(error)); } finally { setBusy(false); }
  }
  async function dismiss(grant: Grant) {
    setBusy(true); setMessage("");
    try { await apiClient.request(`/v1/fleet/grants/${encodeURIComponent(grant.id)}/dismiss`, { method: "POST", body: {} }); await load(); }
    catch (error) { setMessage(errorMessage(error)); } finally { setBusy(false); }
  }
  async function change(grant: Grant, action: "approve" | "revoke") {
    setBusy(true); setMessage("");
    try {
      const changed = await review(grant, action); await load();
      if (changed) setMessage(action === "approve" ? "Device access enabled." : "Device access revoked. Running jobs have been asked to stop.");
    } catch (error) { setMessage(errorMessage(error)); } finally { setBusy(false); }
  }
  async function refresh() {
    setBusy(true); setMessage("");
    try { await load(); } catch (error) { setMessage(errorMessage(error)); } finally { setBusy(false); }
  }
  const currentAccess = grants.find((grant) => grant.mcp_account && grant.user_id === userID && grant.state !== "revoked" && !isExpired(grant));
  return <section className="agent-panel fleet-access-panel" aria-labelledby="fleet-access-title">
    <div className="agent-panel-head"><h2 id="fleet-access-title">Account device access</h2><button type="button" disabled={busy || loading} onClick={() => void refresh()}>Refresh access</button></div>
    <p>Enable access once for your Hank account. Every MCP app you connect uses the same approval, including when you reconnect an app.</p>
    <div className="fleet-access-summary"><strong>All devices · Full access</strong><p>Read and write workspace files, run commands, read output, and cancel jobs on all current and future devices in this Home.</p></div>
    <p className="linux-root-warning">Commands run with the agent’s full account permissions, including root for system agents.</p>
    {message && <p role="status">{message}</p>}
    {loading ? <p>Loading account access…</p> : currentAccess ? <p>{currentAccess.state === "approved" ? "Your account has device access." : "Your account’s request is waiting for administrator approval."}</p> : <form className="settings-form" onSubmit={(event) => void create(event)}>
      <label>Access duration<select id="fleet-access-duration" value={duration} disabled={busy || loadFailed} onChange={(event) => setDuration(event.target.value)}><option value="1_month">1 month</option><option value="3_months">3 months</option><option value="6_months">6 months</option><option value="1_year">1 year</option><option value="infinite">No expiration (until revoked)</option></select></label>
      <button disabled={busy || loadFailed} type="submit">{isAdmin ? "Review and enable access" : "Request account access"}</button>
    </form>}
    <div className="card-list">{grants.map((grant) => {
      const expired = isExpired(grant);
      const owner = grant.user_id === userID ? "Your account" : grant.requester_name || grant.user_id;
      return <article className="dashboard-tile" key={grant.id}>
        <strong>{owner}</strong><span>{expired ? "Expired" : grant.state === "approved" ? "Enabled" : grant.state === "pending" ? "Waiting for approval" : "Revoked"}</span>
        {grant.mcp_account ? <span>All devices · Full access · All connected MCP apps</span> : <details><summary>{grant.mcp_token_id ? "Earlier app approval" : "Local fleet client"}</summary><p>{grant.agents.map((id) => agents.find((agent) => agent.agent_id === id)?.name || id).join(", ")}</p><p>{grant.operations.join(", ")}</p>{grant.mcp_token_id && <p>Enable account access above to use every MCP app and device. This earlier approval keeps its original limits until you revoke it.</p>}</details>}
        <small>{expiryLabel(grant.expires_at)}</small><div className="button-row">
          {isAdmin && grant.state === "pending" && !expired && <button disabled={busy} onClick={() => void change(grant, "approve")}>Approve</button>}
          {!expired && grant.state !== "revoked" && <button disabled={busy} onClick={() => void change(grant, "revoke")}>Revoke</button>}
          {(expired || grant.state === "revoked") && <button disabled={busy} onClick={() => void dismiss(grant)}>Dismiss</button>}
        </div>
      </article>;
    })}</div>
  </section>;
}
