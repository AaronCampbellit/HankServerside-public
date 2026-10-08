package observability

import (
	"strings"
	"testing"
)

func TestDesktopMetricsArePayloadFreeAndFixedCardinality(t *testing.T) {
	metrics := NewMetrics()
	metrics.SetDesktopSessions("active", "macos", 2)
	metrics.IncDesktopJoin("browser", "success")
	metrics.IncDesktopReconnect("failed")
	metrics.IncDesktopTerminated("slow_consumer")
	metrics.AddDesktopRelayBytes("agent_to_browser", 4096)
	metrics.IncDesktopRelayBackpressure("agent_to_browser")
	metrics.SetDesktopReadiness("windows", "service", true)
	rendered := metrics.RenderPrometheus()
	for _, required := range []string{"hank_desktop_sessions{platform=\"macos\",state=\"active\"} 2", "hank_desktop_join_total{side=\"browser\",outcome=\"success\"} 1", "hank_desktop_reconnect_total{outcome=\"failed\"} 1", "hank_desktop_terminated_total{reason=\"slow_consumer\"} 1", "hank_desktop_relay_bytes_total{direction=\"agent_to_browser\"} 4096", "hank_desktop_relay_backpressure_total{direction=\"agent_to_browser\"} 1", "hank_desktop_readiness{platform=\"windows\",check=\"service\"} 1"} {
		if !strings.Contains(rendered, required) {
			t.Fatalf("missing %q in %s", required, rendered)
		}
	}
	metrics.IncDesktopTerminated("session-secret-or-user-value")
	if strings.Contains(metrics.RenderPrometheus(), "session-secret") {
		t.Fatal("unbounded reason label accepted")
	}
}

func TestNewMetricsExposeZeroAssistantProviderCounters(t *testing.T) {
	t.Parallel()

	rendered := NewMetrics().RenderPrometheus()
	for _, metric := range []string{
		`hank_assistant_provider_requests_total{provider="unknown"} 0`,
		`hank_assistant_provider_errors_total{provider="unknown"} 0`,
		`hank_desktop_sessions{platform="unknown",state="active"} 0`,
		`hank_desktop_join_total{side="agent",outcome="failed"} 0`,
		`hank_desktop_reconnect_total{outcome="expired"} 0`,
		`hank_desktop_readiness{platform="macos",check="capture"} 0`,
		`hank_desktop_readiness_reported{platform="macos",check="capture"} 0`,
	} {
		if !strings.Contains(rendered, metric) {
			t.Fatalf("RenderPrometheus() missing %q", metric)
		}
	}
}

func TestMCPAttachmentMetricsArePayloadFreeAndFixedCardinality(t *testing.T) {
	metrics := NewMetrics()
	metrics.SetMCPAttachmentSessions("open", 2)
	metrics.SetMCPAttachmentStagedBytes(8192)
	metrics.AddMCPAttachmentFinalizedBytes(4096)
	metrics.IncMCPAttachmentFailure("offset_conflict")
	metrics.IncMCPAttachmentFailure("private-filename.html")
	metrics.AddMCPAttachmentCleanup("staging_files", 3)
	rendered := metrics.RenderPrometheus()
	for _, required := range []string{
		`hank_mcp_note_attachment_sessions{status="open"} 2`,
		`hank_mcp_note_attachment_staged_bytes 8192`,
		`hank_mcp_note_attachment_finalized_bytes_total 4096`,
		`hank_mcp_note_attachment_failures_total{code="offset_conflict"} 1`,
		`hank_mcp_note_attachment_failures_total{code="unknown"} 1`,
		`hank_mcp_note_attachment_cleanup_total{kind="staging_files"} 3`,
	} {
		if !strings.Contains(rendered, required) {
			t.Fatalf("missing %q in %s", required, rendered)
		}
	}
	if strings.Contains(rendered, "private-filename") {
		t.Fatal("unbounded failure label accepted")
	}
}

func TestMCPSubscriptionMetricsArePayloadFreeAndFixedCardinality(t *testing.T) {
	metrics := NewMetrics()
	metrics.SetMCPSubscriptionCounts(2, 3)
	metrics.IncMCPSubscriptionOpened()
	metrics.IncMCPSubscriptionClosed("shutdown")
	metrics.IncMCPSubscriptionClosed("client-supplied-secret")
	metrics.IncMCPSubscriptionRejected("token_limit")
	metrics.IncMCPSubscriptionNotification("tools_changed")
	metrics.IncMCPSubscriptionCoalesced("tools_changed")
	metrics.IncMCPSubscriptionWriteFailure()

	rendered := metrics.RenderPrometheus()
	for _, required := range []string{
		`hank_mcp_subscriptions_active 2`,
		`hank_mcp_subscriptions_peak 3`,
		`hank_mcp_subscriptions_opened_total 1`,
		`hank_mcp_subscriptions_closed_total{reason="shutdown"} 1`,
		`hank_mcp_subscriptions_closed_total{reason="unknown"} 1`,
		`hank_mcp_subscriptions_rejected_total{reason="token_limit"} 1`,
		`hank_mcp_subscription_notifications_total{kind="tools_changed"} 1`,
		`hank_mcp_subscription_coalesced_total{kind="tools_changed"} 1`,
		`hank_mcp_subscription_write_failures_total 1`,
	} {
		if !strings.Contains(rendered, required) {
			t.Fatalf("missing %q in %s", required, rendered)
		}
	}
	if strings.Contains(rendered, "client-supplied-secret") {
		t.Fatal("unbounded close reason label accepted")
	}
}
