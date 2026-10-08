package cloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMonitoringHealthRequiresFreshCollectionAndRules(t *testing.T) {
	stale, missing, badRule, coverage := false, false, false, true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := time.Now().UTC()
		if stale {
			now = now.Add(-3 * time.Minute)
		}
		var data any
		switch r.URL.Path {
		case "/api/v1/targets":
			targets := []any{}
			for _, job := range []string{"hank-cloud", "prometheus", "alertmanager", "hank-host"} {
				if missing && job == "hank-host" {
					continue
				}
				targets = append(targets, map[string]any{"labels": map[string]string{"job": job}, "health": "up", "lastScrape": now})
			}
			data = map[string]any{"activeTargets": targets}
		case "/api/v1/rules":
			rules := []any{}
			for _, name := range []string{"HankCloudScrapeMissing", "HankAlertmanagerScrapeMissing", "HankAlertDeliveryUnconfigured", "HankHostMetricsMissing", "HankAgentOffline", "HankFileJobsRequireRollback", "HankDiskUsageHigh"} {
				health := "ok"
				if badRule {
					health = "err"
				}
				rules = append(rules, map[string]any{"name": name, "health": health, "lastEvaluation": now})
			}
			data = map[string]any{"groups": []any{map[string]any{"rules": rules}}}
		case "/api/v1/query":
			result := []any{}
			if coverage {
				result = append(result, map[string]any{"value": []any{time.Now().Unix(), "1"}})
			}
			data = map[string]any{"resultType": "vector", "result": result}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": data})
	}))
	defer srv.Close()
	m := monitoringDelivery{client: srv.Client(), prometheusURL: srv.URL}
	if !m.health(context.Background()).Ready {
		t.Fatal("healthy monitoring rejected")
	}
	stale = true
	if m.health(context.Background()).Ready {
		t.Fatal("stale collection accepted")
	}
	stale = false
	missing = true
	if m.health(context.Background()).Ready {
		t.Fatal("missing host accepted")
	}
	missing = false
	badRule = true
	if m.health(context.Background()).Ready {
		t.Fatal("failed rules accepted")
	}
	badRule = false
	coverage = false
	if m.health(context.Background()).Ready {
		t.Fatal("missing metrics/destination accepted")
	}
	m.prometheusURL = ""
	if m.health(context.Background()).Ready {
		t.Fatal("unconfigured monitoring accepted")
	}
}
func TestMonitoringCollectionRejectsBrowserStyleURLs(t *testing.T) {
	s := &Server{}
	for _, raw := range []string{"file:///etc/passwd", "http://user:pass@host", "http://host/path", "http://host?query=x", "http://host/#fragment"} {
		if s.ConfigureMonitoringCollection(raw) == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}
