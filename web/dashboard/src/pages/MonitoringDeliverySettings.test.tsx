import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MonitoringDeliverySettings } from "./MonitoringDeliverySettings";
import { monitoringClient, type MonitoringStatus } from "../api/monitoring";

vi.mock("../api/monitoring", () => ({ monitoringClient: { health: vi.fn().mockResolvedValue({ ready: true, checks: [] }), get: vi.fn(), save: vi.fn(), test: vi.fn() } }));
const initial: MonitoringStatus = { settings: { inbox_enabled: true, inbox_audience: "admins", email_enabled: true, smtp_host: "smtp.example.com", smtp_port: 587, smtp_username: "hank", email_from: "hank@example.com", email_to: "admin@example.com", updated_at: "2026-09-05T00:00:00Z" }, smtp_password_set: true, delivery_status: "ready" };

describe("MonitoringDeliverySettings", () => {
  beforeEach(() => { vi.mocked(monitoringClient.health).mockResolvedValue({ ready: true, checked_at: "", checks: [] }); vi.mocked(monitoringClient.get).mockResolvedValue(structuredClone(initial)); });
  afterEach(() => { cleanup(); vi.resetAllMocks(); });
  it("loads saved configuration without returning a password, saves recipients, and queues a test", async () => {
    vi.mocked(monitoringClient.save).mockResolvedValue({ ...initial, settings: { ...initial.settings, inbox_audience: "members", email_to: "new@example.com" } });
    vi.mocked(monitoringClient.test).mockResolvedValue({ queued: true });
    render(<MonitoringDeliverySettings />);
    await screen.findByDisplayValue("smtp.example.com");
    expect(screen.getByLabelText("SMTP password")).toHaveValue("");
    fireEvent.change(screen.getByLabelText("Recipient email"), { target: { value: "new@example.com" } });
    fireEvent.change(screen.getByLabelText("Inbox recipients"), { target: { value: "members" } });
    expect(screen.getByRole("button", { name: "Send test alert" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Save alert settings" }));
    await screen.findByText("Alert delivery settings saved and applied.");
    expect(monitoringClient.save).toHaveBeenCalledWith(expect.objectContaining({ email_to: "new@example.com", inbox_audience: "members" }), "", false);
    fireEvent.click(screen.getByRole("button", { name: "Send test alert" }));
    await screen.findByText(/Test alert queued/);
    expect(monitoringClient.test).toHaveBeenCalledOnce();
  });
  it("clears password input after saving and distinguishes pending application from success", async () => {
    vi.mocked(monitoringClient.save).mockResolvedValue({ ...initial, delivery_status: "pending" });
    render(<MonitoringDeliverySettings />);
    await screen.findByDisplayValue("smtp.example.com");
    fireEvent.change(screen.getByLabelText("SMTP password"), { target: { value: "replacement-password" } });
    fireEvent.click(screen.getByRole("button", { name: "Save alert settings" }));
    await screen.findByText(/Hank will retry automatically/);
    expect(screen.getByLabelText("SMTP password")).toHaveValue("");
    fireEvent.click(screen.getByLabelText("Remove saved SMTP password"));
    fireEvent.click(screen.getByLabelText("Send server alerts by email"));
    fireEvent.click(screen.getByRole("button", { name: "Save alert settings" }));
    await waitFor(() => expect(monitoringClient.save).toHaveBeenLastCalledWith(expect.objectContaining({ email_enabled: false }), "", true));
  });
  it("shows load errors with retry and preserves edits after a failed save", async () => {
    vi.mocked(monitoringClient.get).mockRejectedValueOnce(new Error("Service unavailable"));
    render(<MonitoringDeliverySettings />);
    await screen.findByRole("alert");
    fireEvent.click(screen.getByRole("button", { name: "Retry alert settings" }));
    await screen.findByDisplayValue("smtp.example.com");
    vi.mocked(monitoringClient.save).mockRejectedValue(new Error("Invalid SMTP address"));
    fireEvent.change(screen.getByLabelText("SMTP hostname"), { target: { value: "invalid-host" } });
    fireEvent.click(screen.getByRole("button", { name: "Save alert settings" }));
    await screen.findByText("Invalid SMTP address");
    expect(screen.getByLabelText("SMTP hostname")).toHaveValue("invalid-host");
  });
});
