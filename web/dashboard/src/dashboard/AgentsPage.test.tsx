import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AgentsPage } from "./AgentsPage";
import { ConfirmDialogProvider, ToastProvider } from "../ui/primitives";

const agentsClient = vi.hoisted(() => ({
  listAgents: vi.fn(),
  subscribeHealth: vi.fn(),
  onAlert: vi.fn(),
  lock: vi.fn(),
  restart: vi.fn(),
  wakeOnLAN: vi.fn(),
	listEnrollments: vi.fn(),
	createEnrollment: vi.fn(),
	revokeEnrollment: vi.fn(),
	requestCredentialRotation: vi.fn(),
	revokeAgentCredentials: vi.fn(),
}));
const bootstrapClient = vi.hoisted(() => ({ load: vi.fn() }));
const homeClient = vi.hoisted(() => ({
  listAgentTokens: vi.fn(),
  createAgentToken: vi.fn(),
  removeAgent: vi.fn(),
  revokeAgentToken: vi.fn(),
}));
const desktopAuditClient = vi.hoisted(() => ({ readiness: vi.fn() }));

vi.mock("../api/agents", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/agents")>()),
  agentsClient,
}));
vi.mock("../api/bootstrap", () => ({ bootstrapClient }));
vi.mock("../api/home", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/home")>()),
  homeClient,
}));
vi.mock("../api/desktopAudit", () => ({ desktopAuditClient }));
vi.mock("../desktop/trust/DesktopTrustSettings", () => ({
  DesktopTrustSettings: () => <section aria-label="Remote Desktop trust">Trust settings</section>,
}));
vi.mock("../desktop/DesktopViewerPage", () => ({
  DesktopViewerPage: ({ agentID, embedded }: { agentID: string; embedded?: boolean }) => (
    <section aria-label="Embedded Remote Desktop">{agentID}:{String(embedded)}</section>
  ),
}));

function renderPage(tab: "overview" | "desktop" | "terminal" | "security") {
  return render(
    <ToastProvider>
      <ConfirmDialogProvider>
        <AgentsPage initialAgentID="agent_1" initialTab={tab} />
      </ConfirmDialogProvider>
    </ToastProvider>,
  );
}

function renderListPage() {
	return render(
		<ToastProvider>
			<ConfirmDialogProvider>
				<AgentsPage />
			</ConfirmDialogProvider>
		</ToastProvider>,
	);
}

describe("AgentsPage device workspace", () => {
  beforeEach(() => {
    bootstrapClient.load.mockResolvedValue({
      user: { id: "user_1" },
      home: { id: "home_1" },
      permissions: { is_admin: true },
    });
    agentsClient.listAgents.mockResolvedValue([{
      agent_id: "agent_1",
      name: "Mac Studio",
      status: "online",
      agent_type: "worker",
      capabilities: ["desktop.session.open", "desktop.view", "shell.session.open", "host.lock"],
      metadata: { hostname: "mac-studio", platform: "darwin", os_version: "macOS 26" },
      metrics: { cpu_load_1m: 0.4, memory_used_bytes: 8, memory_total_bytes: 16, disk_used_bytes: 30, disk_total_bytes: 100 },
    }]);
    agentsClient.subscribeHealth.mockResolvedValue(undefined);
    agentsClient.onAlert.mockReturnValue(() => {});
	agentsClient.listEnrollments.mockResolvedValue([]);
	agentsClient.createEnrollment.mockResolvedValue({
		id: "aenroll_1",
		install_command: "curl -fsSL \"https://hank.example/install/linux/one-time-secret\" | sudo bash",
		created_at: "2026-08-13T00:00:00Z",
		expires_at: new Date(Date.now() + 900000).toISOString(),
	});
	agentsClient.revokeEnrollment.mockResolvedValue({ ok: true });
	agentsClient.requestCredentialRotation.mockResolvedValue({});
	agentsClient.revokeAgentCredentials.mockResolvedValue({ ok: true });
    homeClient.listAgentTokens.mockResolvedValue({ tokens: [] });
    desktopAuditClient.readiness.mockResolvedValue({
      agent_id: "agent_1",
      online: true,
      platform: "darwin",
      trusted: true,
      checks: {},
      capabilities: [],
      active_session: false,
    });
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("offers fleet node inspection on the device list without issuing commands", async () => {
    renderListPage();
    fireEvent.click(await screen.findByRole("button", { name: "Mac Studio Online · Worker" }));
    expect(screen.getByRole("heading", { name: "Fleet connections" })).toBeVisible();
    expect(screen.getByRole("link", { name: "Open device workspace" })).toHaveAttribute("href", "/dashboard/agents/agent_1");
    expect(agentsClient.lock).not.toHaveBeenCalled();
    expect(agentsClient.restart).not.toHaveBeenCalled();
  });

  it("presents a route-backed device workspace with a focused overview", async () => {
    renderPage("overview");

    expect(await screen.findByRole("heading", { name: "Mac Studio", level: 1 })).toBeInTheDocument();
    expect(screen.getByRole("navigation", { name: "Device workspace" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Overview" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Remote Desktop" })).toHaveAttribute("href", "/dashboard/agents/agent_1/desktop");
    expect(screen.getByText("Details")).toBeInTheDocument();
    expect(screen.queryByLabelText("Remote Desktop trust")).not.toBeInTheDocument();
    expect(screen.queryByText("Live shell")).not.toBeInTheDocument();
  });

  it("keeps Remote Desktop embedded in the selected device workspace", async () => {
    renderPage("desktop");

    expect(await screen.findByLabelText("Embedded Remote Desktop")).toHaveTextContent("agent_1:true");
    expect(screen.getByRole("link", { name: "Remote Desktop" })).toHaveAttribute("aria-current", "page");
  });

  it("separates terminal and security controls into their own tabs", async () => {
    const page = renderPage("terminal");
    expect(await screen.findByText("Live shell")).toBeInTheDocument();
    expect(screen.queryByLabelText("Remote Desktop trust")).not.toBeInTheDocument();

    page.unmount();
    renderPage("security");
    expect(await screen.findByLabelText("Remote Desktop trust")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Capabilities" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Remove device" })).toBeInTheDocument();
    expect(screen.queryByText("Live shell")).not.toBeInTheDocument();
  });

  it("fails closed to overview when a member opens an admin-only device tab", async () => {
    bootstrapClient.load.mockResolvedValue({
      user: { id: "user_1" },
      home: { id: "home_1" },
      permissions: { is_admin: false },
    });

    renderPage("security");

    expect(await screen.findByRole("link", { name: "Overview" })).toHaveAttribute("aria-current", "page");
    expect(screen.queryByRole("link", { name: "Security" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remove device" })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Remote Desktop trust")).not.toBeInTheDocument();
  });

  it("keeps loaded devices usable when live health subscription is unavailable", async () => {
    agentsClient.subscribeHealth.mockRejectedValue(new Error("not found"));

    renderPage("overview");

    expect(await screen.findByRole("heading", { name: "Mac Studio", level: 1 })).toBeInTheDocument();
    expect(screen.queryByText(/doesn't support multiple agents/i)).not.toBeInTheDocument();
  });

	it("creates a one-machine privileged Linux install command without persisting it", async () => {
		renderListPage();

		fireEvent.click(await screen.findByRole("button", { name: "Add device" }));
		expect(screen.getByText(/unrestricted root access/i)).toBeVisible();
		fireEvent.click(screen.getByRole("checkbox", { name: "Pair an installed Linux agent with a short code" }));
 fireEvent.click(screen.getByRole("button", { name: "Create one-time link" }));

		const command = await screen.findByDisplayValue(/curl -fsSL .*sudo bash/);
		expect(command).toHaveAttribute("readonly");
		expect(agentsClient.createEnrollment).toHaveBeenCalledWith("", false, "linux");
		expect(window.localStorage.length).toBe(0);
	});

  it.each(["macos", "windows"] as const)("creates a platform-bound %s pairing code", async (platform) => {
    agentsClient.createEnrollment.mockResolvedValue({id:"pair",pairing_code:"ABCD-EFGH-IJKL",server_url:"https://test.example",expires_at:new Date(Date.now()+900000).toISOString()});
    renderListPage();
    fireEvent.click(await screen.findByRole("button",{name:"Add device"}));
    fireEvent.change(screen.getByLabelText("Device platform"),{target:{value:platform}});
    expect(screen.queryByText(/Unrestricted root access/)).toBeNull();
    fireEvent.click(screen.getByRole("button",{name:"Create pairing code"}));
    expect(await screen.findByDisplayValue("ABCD-EFGH-IJKL")).toHaveAttribute("readonly");
    expect(agentsClient.createEnrollment).toHaveBeenCalledWith("",true,platform);
    expect(window.localStorage.length).toBe(0);
  });

	it("exposes credential rotation and revocation in the device security workspace", async () => {
		renderPage("security");

		expect(await screen.findByRole("button", { name: "Rotate credentials" })).toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "Rotate credentials" }));
		await waitFor(() => expect(agentsClient.requestCredentialRotation).toHaveBeenCalledWith("agent_1"));
		expect(screen.getByRole("button", { name: "Revoke credentials" })).toBeInTheDocument();
	});
});
