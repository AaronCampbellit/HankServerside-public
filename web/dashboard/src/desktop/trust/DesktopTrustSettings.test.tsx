import "fake-indexeddb/auto";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { encodeBase64URL } from "../base64url";
import { IndexedDBDesktopIdentityStore } from "../identityStore";
import { DesktopTrustSettings } from "./DesktopTrustSettings";

describe("DesktopTrustSettings", () => {
  afterEach(cleanup);

  beforeEach(async () => {
    localStorage.clear();
    await new IndexedDBDesktopIdentityStore().clearForTests();
  });

  it("approves a pending Windows endpoint with server-managed trust", async () => {
    const client = {
      snapshot: vi.fn().mockResolvedValue({
        configured: true,
        root: { generation: 3, fingerprint: "root-fp", algorithm: "P-256", public_key_spki: "spki", authority_mode: "server_managed" },
        identities: [],
      }),
      pendingEndpoint: vi.fn().mockResolvedValue({ pending: true, fingerprint: "windows-endpoint-fingerprint", request: {} }),
      approveManagedEndpoint: vi.fn().mockResolvedValue({}),
    };
    render(<DesktopTrustSettings client={client as never} homeID="home" userID="user" agentID="win-win11vm" agentName="WIN11VM" />);

    expect(await screen.findByText("windows-endpoint-fingerprint")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Approve WIN11VM" }));

    await waitFor(() => expect(client.approveManagedEndpoint).toHaveBeenCalledWith("win-win11vm", false));
  });

  it("shows device approvals and keeps the last-resort reset confirmation exact", async () => {
    const client = {
      snapshot: vi.fn().mockResolvedValue({
        configured: true,
        root: { generation: 1, fingerprint: "root-fp", algorithm: "P-256", public_key_spki: "spki" },
        identities: [{ identity_id: "id", identity_type: "endpoint", user_id: "", device_id: "", agent_id: "agent-1", fingerprint: "endpoint-fp", capabilities: ["desktop.view"] }],
      }),
      revokeOperator: vi.fn(),
      revokeEndpoint: vi.fn(),
      reset: vi.fn().mockResolvedValue({}),
    };
    render(<DesktopTrustSettings client={client as never} homeID="home" userID="user" />);
    expect(await screen.findByText("endpoint-fp")).toBeInTheDocument();
    expect(screen.getByText(/Set up secure access for this device/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Reset confirmation"), { target: { value: "wrong" } });
    expect(screen.getByRole("button", { name: "Remove all Remote Desktop approvals" })).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Reset confirmation"), { target: { value: "reset desktop trust" } });
    await waitFor(() => expect(screen.getByRole("button", { name: "Remove all Remote Desktop approvals" })).toBeEnabled());
  });

  it("signs and submits a reviewed browser approval with the approved local identity", async () => {
    const store = new IndexedDBDesktopIdentityStore();
    const signerDeviceID = "browser-approved";
    await store.create(signerDeviceID);
    localStorage.setItem("hank-desktop-device-id", signerDeviceID);
    const requested = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, false, ["sign", "verify"]);
    const requestedSPKI = new Uint8Array(await crypto.subtle.exportKey("spki", requested.publicKey));
    const now = Date.now();
    const approvalRequest = {
      identity_type: "operator_device",
      identity_id: "dop_requested",
      device_id: "browser-requested",
      public_key_spki: encodeBase64URL(requestedSPKI),
      capabilities: ["operator.approve", "endpoint.approve", "trust.recover", "trust.rotate"],
      created_at: new Date(now - 1_000).toISOString(),
      expires_at: new Date(now + 60_000).toISOString(),
      platform: "browser",
    };
    const client = {
      snapshot: vi.fn().mockResolvedValue({
        configured: true,
        root: { generation: 1, fingerprint: "root-fp", algorithm: "P-256", public_key_spki: "spki" },
        identities: [{
          identity_id: "dop_approved",
          identity_type: "operator_device",
          user_id: "user",
          device_id: signerDeviceID,
          agent_id: "",
          fingerprint: "approved-fp",
          capabilities: ["operator.approve", "endpoint.approve", "trust.recover", "trust.rotate"],
        }],
      }),
      approveOperator: vi.fn().mockResolvedValue({}),
    };

    render(<DesktopTrustSettings client={client as never} homeID="home" userID="user" />);
    await screen.findByText("approved-fp");
    fireEvent.change(screen.getByLabelText("Desktop approval request"), { target: { value: JSON.stringify(approvalRequest) } });
    fireEvent.click(screen.getByRole("button", { name: "Check this request" }));
    await screen.findByText("browser-requested");
    fireEvent.click(screen.getByRole("button", { name: "Approve after comparing fingerprint" }));

    await waitFor(() => expect(client.approveOperator).toHaveBeenCalledOnce());
    expect(client.approveOperator).toHaveBeenCalledWith(expect.objectContaining({
      identity_id: "dop_requested",
      device_id: "browser-requested",
      certificate: expect.any(String),
    }));
  });

  it("routes a browser with a lost private key through re-enrollment instead of self-approval", async () => {
    localStorage.setItem("hank-desktop-device-id", "browser-key-lost");
    const client = {
      snapshot: vi.fn().mockResolvedValue({
        configured: true,
        root: { generation: 1, fingerprint: "root-fp", algorithm: "P-256", public_key_spki: "spki" },
        identities: [{
          identity_id: "dop_stale",
          identity_type: "operator_device",
          user_id: "user",
          device_id: "browser-key-lost",
          agent_id: "",
          fingerprint: "stale-fingerprint",
          capabilities: ["operator.approve", "endpoint.approve", "trust.recover", "trust.rotate"],
        }],
      }),
    };

    render(<DesktopTrustSettings client={client as never} homeID="home" userID="user" />);

    await screen.findByText("stale-fingerprint");
    const createRequest = screen.getByRole("button", { name: "Create an approval request for another browser" });
    await waitFor(() => expect(createRequest).toBeEnabled());
    fireEvent.click(createRequest);
    await waitFor(() => expect(screen.getByLabelText("Desktop approval request")).not.toHaveValue(""));
    fireEvent.click(screen.getByRole("button", { name: "Check this request" }));

    expect(await screen.findByText(/cannot approve requests because its secure key is unavailable/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Approve after comparing fingerprint" })).toBeDisabled();
  });
});
