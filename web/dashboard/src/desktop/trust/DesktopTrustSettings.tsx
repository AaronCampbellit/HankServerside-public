import { useEffect, useState } from "react";
import type { DesktopTrustSnapshot } from "../../api/desktop";
import { DesktopTrustClient, desktopTrustClient, type DesktopPendingEnrollment } from "../../api/desktopTrust";
import { IndexedDBDesktopIdentityStore } from "../identityStore";
import { decodeBase64URL, encodeBase64URL } from "../base64url";
import { DesktopDeviceApproval } from "./DesktopDeviceApproval";
import { createDesktopRecoveryOperator, createDesktopTrustBootstrap, createDesktopTrustReset, createDesktopTrustRotation, decryptRecoveryRoot, reviewDesktopApprovalRequest, signDesktopApproval, signDesktopRecoveryEnrollment, type ReviewedDesktopApproval } from "./trustCeremony";

export function DesktopTrustSettings({ client = desktopTrustClient, homeID = "", userID = "", agentID = "", agentName = "this device" }: { client?: DesktopTrustClient; homeID?: string; userID?: string; agentID?: string; agentName?: string }) {
  const [snapshot, setSnapshot] = useState<DesktopTrustSnapshot | null>(null), [message, setMessage] = useState(""), [code, setCode] = useState(""), [typedCode, setTypedCode] = useState(""), [offlineCode, setOfflineCode] = useState(""), [resetText, setResetText] = useState("");
  const [approvalText,setApprovalText]=useState(""), [approval,setApproval]=useState<ReviewedDesktopApproval|null>(null), [pending,setPending]=useState<DesktopPendingEnrollment|null>(null);
  const [localSignerAvailable,setLocalSignerAvailable]=useState<boolean|null>(null);
  const reload = () => client.snapshot().then(setSnapshot).catch(error => setMessage(error instanceof Error ? error.message : "Desktop trust unavailable"));
  useEffect(() => { void reload(); }, []);
  useEffect(() => {
    const onAutoEnrollment = () => void reload();
    const onAutoEnrollmentFailure = (event: Event) => {
      const detail = event instanceof CustomEvent && typeof event.detail === "string" ? event.detail : "desktop_browser_enrollment_failed";
      setMessage(`Automatic browser enrollment failed: ${detail}`);
    };
    window.addEventListener("hank-desktop-auto-enrolled", onAutoEnrollment);
    window.addEventListener("hank-desktop-auto-enrollment-failed", onAutoEnrollmentFailure);
    return () => {
      window.removeEventListener("hank-desktop-auto-enrolled", onAutoEnrollment);
      window.removeEventListener("hank-desktop-auto-enrollment-failed", onAutoEnrollmentFailure);
    };
  }, []);
  useEffect(() => {
    if (!agentID || !snapshot?.configured) { setPending(null); return; }
    let active = true;
    const loadPending = async () => {
      try {
        const value = await client.pendingEndpoint(agentID);
        if (active) setPending(value);
      } catch {
        if (active) setPending(null);
      }
    };
    void loadPending();
    const timer = window.setInterval(() => { void loadPending(); }, 2_000);
    return () => { active = false; window.clearInterval(timer); };
  }, [agentID, client, snapshot?.configured]);
  useEffect(() => {
    let active = true;
    const deviceID = localStorage.getItem("hank-desktop-device-id") || "";
    if (!deviceID) { setLocalSignerAvailable(false); return () => { active = false; }; }
    setLocalSignerAvailable(null);
    void new IndexedDBDesktopIdentityStore().get(deviceID)
      .then(key => { if (active) setLocalSignerAvailable(Boolean(key)); })
      .catch(() => { if (active) setLocalSignerAvailable(false); });
    return () => { active = false; };
  }, [snapshot]);
  async function bootstrap() {
    const store = new IndexedDBDesktopIdentityStore(); let ceremony: Awaited<ReturnType<typeof createDesktopTrustBootstrap>> | undefined, committed = false;
    try {
      const deviceID = `browser-${crypto.randomUUID()}`; ceremony = await createDesktopTrustBootstrap({ homeID, userID, deviceID, store });
      await client.bootstrap(ceremony.body); committed = true; localStorage.setItem("hank-desktop-device-id", deviceID); setCode(ceremony.recoveryCode); setMessage("Your backup code is ready. Save it somewhere safe now; we cannot show it again."); await reload();
    } catch (error) { if (!committed) await ceremony?.cleanup(); setMessage(error instanceof Error ? error.message : "We could not start Remote Desktop setup."); }
  }
  async function revoke(identity: DesktopTrustSnapshot["identities"][number]) {
    if (identity.identity_type === "endpoint") await client.revokeEndpoint(identity.agent_id);
    else { await client.revokeOperator(identity.device_id); await new IndexedDBDesktopIdentityStore().remove(identity.device_id); }
    await reload();
  }
  async function rotate() {
    const store=new IndexedDBDesktopIdentityStore(); let ceremony: Awaited<ReturnType<typeof createDesktopTrustRotation>>|undefined, committed=false;
    try { const root = snapshot?.root; if (!root?.recovery_envelope) throw new Error("desktop_recovery_envelope_unavailable"); const oldKey = await decryptRecoveryRoot(offlineCode, homeID, root.generation, root.recovery_envelope), deviceID=`browser-${crypto.randomUUID()}`; ceremony = await createDesktopTrustRotation({homeID,userID,deviceID,store}, root.generation, oldKey); await client.rotate(ceremony.body); committed=true; await store.removeMany([`root:${homeID}:${root.generation}`, ...(snapshot?.identities.filter(value=>value.identity_type==="operator_device" && value.device_id!==deviceID).map(value=>value.device_id) ?? [])]); localStorage.setItem("hank-desktop-device-id", deviceID); setOfflineCode(""); setCode(ceremony.recoveryCode); setMessage("Trust rotated. Save the new recovery code; endpoints must re-enroll."); await reload(); } catch(error) { if(!committed) await ceremony?.cleanup(); setMessage(error instanceof Error ? error.message : "Rotation failed"); }
  }
  async function recover() {
    const store=new IndexedDBDesktopIdentityStore(); let recovered: Awaited<ReturnType<typeof createDesktopRecoveryOperator>>|undefined, committed=false;
    try { const root=snapshot?.root; if(!root?.recovery_envelope) throw new Error("desktop_recovery_envelope_unavailable"); const key=await decryptRecoveryRoot(offlineCode,homeID,root.generation,root.recovery_envelope), deviceID=`browser-${crypto.randomUUID()}`; recovered=await createDesktopRecoveryOperator({homeID,userID,deviceID,store},root.generation,key); const challenge=await client.recoveryChallenge(root.generation,recovered.operator), signature=await signDesktopRecoveryEnrollment(key,homeID,root.generation,recovered,deviceID,decodeBase64URL(challenge.challenge)); await client.recover({generation:root.generation,operator:recovered.operator,challenge:challenge.challenge,root_signature:signature}); committed=true; localStorage.setItem("hank-desktop-device-id",deviceID); setOfflineCode(""); setMessage("New non-exportable operator identity enrolled through offline recovery."); await reload(); } catch(error) { if(!committed) await recovered?.cleanup(); setMessage(error instanceof Error ? error.message : "Recovery failed"); }
  }
  async function reset() {
    const store=new IndexedDBDesktopIdentityStore(); let ceremony: Awaited<ReturnType<typeof createDesktopTrustReset>>|undefined, committed=false;
    try { const oldGeneration=snapshot?.root?.generation ?? 0, generation=oldGeneration+1, deviceID=`browser-${crypto.randomUUID()}`; ceremony=await createDesktopTrustReset({homeID,userID,deviceID,store},generation); await client.reset(ceremony.body); committed=true; await store.removeMany([`root:${homeID}:${oldGeneration}`, ...(snapshot?.identities.filter(value=>value.identity_type==="operator_device" && value.device_id!==deviceID).map(value=>value.device_id) ?? [])]); localStorage.setItem("hank-desktop-device-id", deviceID); setResetText(""); setCode(ceremony.recoveryCode); setMessage("Desktop trust reset. All old identities and live sessions were revoked; save the new recovery code."); await reload(); } catch(error) { if(!committed) await ceremony?.cleanup(); setMessage(error instanceof Error ? error.message : "Reset failed"); }
  }
  async function createOperatorApprovalRequest() {
    try {
      let deviceID=localStorage.getItem("hank-desktop-device-id")||""; if(!deviceID){deviceID=`browser-${crypto.randomUUID()}`;localStorage.setItem("hank-desktop-device-id",deviceID)}
      const store=new IndexedDBDesktopIdentityStore(), existingSPKI=await store.getPublicSPKI(deviceID), spki=existingSPKI??(await store.create(deviceID)).spki, now=new Date(), expires=new Date(now.getTime()+2*365*24*60*60*1000);
      const request={identity_type:"operator_device",identity_id:`dop_${crypto.randomUUID().replaceAll("-","")}`,device_id:deviceID,public_key_spki:encodeBase64URL(spki),capabilities:["operator.approve","endpoint.approve","trust.recover","trust.rotate"],created_at:now.toISOString(),expires_at:expires.toISOString(),platform:"browser"};
      setApprovalText(JSON.stringify(request)); setApproval(null); setMessage("Copy this approval request to a browser that is already approved, then compare the fingerprint before approving it.");
    } catch(error){setMessage(error instanceof Error?error.message:"We could not create an approval request.")}
  }
  async function reviewApproval() { try { setApproval(await reviewDesktopApprovalRequest(JSON.parse(approvalText))); setMessage("Compare the fingerprint out of band before approval."); } catch(error){setApproval(null);setMessage(error instanceof Error?error.message:"Approval request invalid")} }
  async function approveReviewed() {
    if(!approval||!snapshot?.root) return;
    try {
      const deviceID=localStorage.getItem("hank-desktop-device-id")||"", key=await new IndexedDBDesktopIdentityStore().get(deviceID), signer=snapshot.identities.find(value=>value.identity_type==="operator_device"&&value.device_id===deviceID&&!value.revoked_at);
      if(!key||!signer) throw new Error("desktop_approved_signer_unavailable");
      const body=await signDesktopApproval(approval,key.privateKey,homeID,userID,snapshot.root.generation);
      const replacementConfirmation=changed?{confirmation:"replace changed desktop identity"}:{};
      if(approval.request.identity_type==="endpoint") await client.approveEndpoint(approval.request.agent_id!,{...body,...replacementConfirmation}); else await client.approveOperator({...body,...replacementConfirmation});
      setApproval(null); setApprovalText(""); setMessage("Remote Desktop identity approved."); await reload();
    } catch(error){setMessage(error instanceof Error?error.message:"Desktop identity approval failed")}
  }
  async function approveManagedPending(replacing: boolean) {
    try {
      await client.approveManagedEndpoint(agentID, replacing);
      setPending(null);
      setMessage(`${agentName} is approved for Remote Desktop.`);
      await reload();
    } catch(error) { setMessage(error instanceof Error ? error.message : "Desktop identity approval failed"); }
  }
  const approvalSubject=approval?.request.device_id||approval?.request.agent_id||"", changed=Boolean(approval&&snapshot?.identities.some(value=>!value.revoked_at&&value.identity_type===approval.request.identity_type&&(value.device_id===approvalSubject||value.agent_id===approvalSubject)&&value.fingerprint!==approval.fingerprint));
  const managedEndpointChanged=Boolean(pending?.fingerprint&&snapshot?.identities.some(value=>!value.revoked_at&&value.identity_type==="endpoint"&&value.agent_id===agentID&&value.fingerprint!==pending.fingerprint));
  const currentDeviceID=localStorage.getItem("hank-desktop-device-id")||"", currentDeviceRegistered=Boolean(snapshot?.identities.some(value=>value.identity_type==="operator_device"&&value.device_id===currentDeviceID&&!value.revoked_at)), currentDeviceApproved=localSignerAvailable===true&&currentDeviceRegistered;
  const visibleIdentities = snapshot?.identities.filter(identity => identity.identity_type === "operator_device" || !agentID || identity.agent_id === agentID) ?? [];
  if (snapshot?.root?.authority_mode === "server_managed") return <section id="remote-desktop-settings" className="settings-panel" aria-label="Remote Desktop setup">
    <div className="panel-heading"><h2>Remote Desktop</h2><span className="status-pill">Automatic</span></div>
    <p>Hank authorizes this browser after sign-in. Private device keys remain on each browser and enrolled computer.</p>
    {message && <p className="notice-state">{message}</p>}
    {pending?.pending&&pending.fingerprint&&<section aria-label="Approve Remote Desktop identity"><h3>Connect {agentName}</h3><div className="desktop-pending-enrollment"><strong>{agentName} is ready to approve</strong><p>Compare this fingerprint with HankAgent on the computer, then approve it.</p><code>{pending.fingerprint}</code><button type="button" onClick={()=>void approveManagedPending(managedEndpointChanged)}>{managedEndpointChanged ? `Replace approval for ${agentName}` : `Approve ${agentName}`}</button></div></section>}
    <div className="card-list">{visibleIdentities.map(identity => <article className="dashboard-tile" key={identity.identity_id}><span>{identity.identity_type === "endpoint" ? "Enrolled computer" : "This browser"}</span><strong>{identity.identity_type === "endpoint" ? agentName : "Ready for Remote Desktop"}</strong><small>{identity.identity_type === "endpoint" ? "This computer is connected through its device identity." : "This browser is authorized for the current Hank session."}</small>{!identity.revoked_at && <details><summary>Security details</summary><code>{identity.fingerprint}</code><button type="button" onClick={()=>void revoke(identity)}>Revoke device</button></details>}</article>)}</div>
    <p className="meta-line">Revoking a device ends any active Remote Desktop session. Signing in again creates a fresh browser authorization.</p>
  </section>;
  return <section id="remote-desktop-settings" className="settings-panel" aria-label="Remote Desktop setup">
    <div className="panel-heading"><h2>Remote Desktop setup</h2><span className="status-pill">{snapshot?.configured ? "In progress" : "Not started"}</span></div>
    <p>Set up secure access for {agentName}. This is separate from your Hank password.</p>{message && <p className="notice-state">{message}</p>}
    <section className="desktop-trust-walkthrough" aria-label="Remote Desktop setup walkthrough"><h3>Work through these steps</h3><ol><li data-ready={Boolean(snapshot?.configured)}><strong>Set up this browser</strong><span>Start here once. This browser becomes your first approved Remote Desktop controller.</span></li><li data-ready={Boolean(code)}><strong>Save your backup code</strong><span>Keep it offline. You need it if you lose access to this browser.</span></li><li data-ready={currentDeviceApproved}><strong>Approve browsers and devices</strong><span>Only approve a request after you have compared its fingerprint in person or through another trusted channel.</span></li><li data-ready={false}><strong>Finish the checks above</strong><span>Come back to this device’s checklist, then open Remote Desktop when it is ready.</span></li></ol></section>
    {!snapshot?.configured && <button type="button" disabled={!homeID || !userID} onClick={() => void bootstrap()}>Set up Remote Desktop on this browser</button>}
    {code && <section aria-label="One-time recovery code"><h3>Save your backup code</h3><p>Copy or print this now. It will disappear after you confirm that you saved it.</p><code>{code}</code><div className="button-row"><button type="button" onClick={() => void navigator.clipboard.writeText(code)}>Copy code</button><button type="button" onClick={() => window.print()}>Print code</button></div><label>Type the full code to confirm<input aria-label="Confirm recovery code" value={typedCode} onChange={event=>setTypedCode(event.target.value)}/></label><button type="button" disabled={typedCode !== code} onClick={()=>{setCode("");setTypedCode("");setMessage("Backup code saved. Keep it offline and private.");}}>I saved my backup code</button></section>}
    <div className="card-list">{visibleIdentities.map(identity => <article className="dashboard-tile" key={identity.identity_id}><span>{identity.identity_type === "endpoint" ? "Approved device" : "This browser"}</span><strong>{identity.identity_type === "endpoint" ? agentName : "Ready to control Remote Desktop"}</strong><small>{identity.identity_type === "endpoint" ? "This device can accept Remote Desktop sessions." : "This browser can approve devices and start sessions."}</small>{!identity.revoked_at && <details><summary>Security details</summary><code>{identity.fingerprint}</code><button type="button" onClick={()=>void revoke(identity)}>Remove approval</button></details>}</article>)}</div>
    {snapshot?.configured&&<section aria-label="Approve Remote Desktop identity"><h3>Connect {agentName}</h3>{pending?.pending&&pending.request ? <div className="desktop-pending-enrollment"><strong>{agentName} is ready to approve</strong><p>Confirm that the short fingerprint shown in HankAgent matches this one, then approve this Mac.</p><code>{pending.fingerprint}</code><button type="button" onClick={()=>void reviewDesktopApprovalRequest(pending.request!).then(value=>{setApproval(value);setMessage("Confirm the fingerprint in HankAgent before approving this Mac.");}).catch(()=>setMessage("This Mac sent an invalid enrollment request."))}>Review {agentName}</button></div> : <p>Open HankAgent on this Mac. When it is ready, its approval will appear here automatically.</p>}{currentDeviceRegistered&&localSignerAvailable===false&&<p role="alert">This browser was approved before, but its secure key is no longer available. Use another approved browser or your backup code to set this browser up again.</p>}<details><summary>Advanced: paste an approval request</summary><p>Use this only while upgrading from an older HankAgent.</p><button type="button" disabled={localSignerAvailable===null||currentDeviceApproved} onClick={()=>void createOperatorApprovalRequest()}>Create an approval request for another browser</button><label>Approval request<textarea aria-label="Desktop approval request" value={approvalText} onChange={event=>{setApprovalText(event.target.value);setApproval(null)}}/></label><button type="button" disabled={!approvalText} onClick={()=>void reviewApproval()}>Check this request</button></details>{approval&&<DesktopDeviceApproval details={{home:homeID,subject:approvalSubject,platform:approval.request.platform||"unknown",fingerprint:approval.fingerprint,capabilities:approval.request.capabilities,expiry:approval.request.expires_at,signer:currentDeviceID||"approved administrator",changed,disabledReason:localSignerAvailable===false?"This browser cannot approve requests because its secure key is unavailable. Use another approved browser or your backup code to set it up again.":undefined}} onApprove={()=>void approveReviewed()}/>}</section>}
    {snapshot?.configured && <details className="desktop-recovery-actions"><summary>Lost access or replacing a device?</summary><p>Use your backup code to add a replacement browser or replace every Remote Desktop approval. These actions end existing Remote Desktop sessions.</p><label>Backup code<input aria-label="Offline recovery code" type="password" autoComplete="off" value={offlineCode} onChange={event=>setOfflineCode(event.target.value)}/></label><div className="button-row"><button type="button" disabled={!offlineCode} onClick={()=>void recover()}>Set up a replacement browser</button><button type="button" disabled={!offlineCode} onClick={()=>void rotate()}>Replace all approvals and get a new code</button></div><p>Last resort: remove every approved browser and device, then set up Remote Desktop again.</p><label>Type <code>reset desktop trust</code> to confirm<input aria-label="Reset confirmation" value={resetText} onChange={event=>setResetText(event.target.value)}/></label><button type="button" disabled={resetText !== "reset desktop trust"} onClick={()=>void reset()}>Remove all Remote Desktop approvals</button></details>}
  </section>;
}
