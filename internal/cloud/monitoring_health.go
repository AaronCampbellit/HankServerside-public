package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type monitoringCheck struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Ready bool   `json:"ready"`
}
type monitoringHealth struct {
	Ready     bool              `json:"ready"`
	CheckedAt time.Time         `json:"checked_at"`
	Checks    []monitoringCheck `json:"checks"`
}

// Only operator configuration selects the internal destination; browser input
// cannot supply URLs or queries. The existing client refuses redirects/proxies.
func (s *Server) ConfigureMonitoringCollection(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("invalid monitoring collection URL")
	}
	if s.monitoring != nil {
		s.monitoring.prometheusURL = strings.TrimRight(raw, "/")
	}
	return nil
}
func (m *monitoringDelivery) readHealth(ctx context.Context, path string, out any) bool {
	if m.prometheusURL == "" {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.prometheusURL+path, nil)
	if err != nil {
		return false
	}
	res, err := m.client.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 2_000_001))
	if err != nil || len(body) > 2_000_000 {
		return false
	}
	return json.Unmarshal(body, out) == nil
}
func (m *monitoringDelivery) health(parent context.Context) monitoringHealth {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	h := monitoringHealth{Ready: true, CheckedAt: time.Now().UTC()}
	add := func(id, label string, ready bool) {
		h.Checks = append(h.Checks, monitoringCheck{id, label, ready})
		h.Ready = h.Ready && ready
	}
	var targets struct {
		Status string `json:"status"`
		Data   struct {
			Targets []struct {
				Labels     map[string]string `json:"labels"`
				Health     string            `json:"health"`
				LastScrape time.Time         `json:"lastScrape"`
			} `json:"activeTargets"`
		} `json:"data"`
	}
	ok := m.readHealth(ctx, "/api/v1/targets", &targets) && targets.Status == "success"
	for _, job := range []struct{ id, label string }{{"hank-cloud", "Hank health statistics"}, {"prometheus", "Monitoring service"}, {"alertmanager", "Alert delivery service"}, {"hank-host", "Server disk monitoring"}} {
		found, healthy := false, ok
		for _, t := range targets.Data.Targets {
			if t.Labels["job"] == job.id {
				found = true
				age := time.Since(t.LastScrape)
				healthy = healthy && t.Health == "up" && age >= 0 && age < 2*time.Minute
			}
		}
		add(job.id, job.label, found && healthy)
	}
	var rules struct {
		Status string `json:"status"`
		Data   struct {
			Groups []struct {
				Rules []struct {
					Name           string    `json:"name"`
					Health         string    `json:"health"`
					LastEvaluation time.Time `json:"lastEvaluation"`
				} `json:"rules"`
			} `json:"groups"`
		} `json:"data"`
	}
	ok = m.readHealth(ctx, "/api/v1/rules", &rules) && rules.Status == "success"
	names := map[string]bool{}
	for _, g := range rules.Data.Groups {
		for _, r := range g.Rules {
			names[r.Name] = true
			age := time.Since(r.LastEvaluation)
			ok = ok && r.Health == "ok" && age >= 0 && age < 2*time.Minute
		}
	}
	for _, name := range []string{"HankCloudScrapeMissing", "HankAlertmanagerScrapeMissing", "HankAlertDeliveryUnconfigured", "HankHostMetricsMissing", "HankAgentOffline", "HankFileJobsRequireRollback", "HankDiskUsageHigh"} {
		ok = ok && names[name]
	}
	add("rules", "Alert rules evaluating", ok)
	// Require real disk/agent metrics and at least one configured destination.
	query := `(min(alertmanager_integrations{job="alertmanager"}) > 0) and on() (min(node_filesystem_size_bytes{job="hank-host",mountpoint="/"}) > 0) and on() (count(node_filesystem_avail_bytes{job="hank-host",mountpoint="/"}) > 0) and on() (count(hank_primary_agent_online{job="hank-cloud"}) > 0)`
	var result struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string            `json:"resultType"`
			Result     []json.RawMessage `json:"result"`
		} `json:"data"`
	}
	ok = m.readHealth(ctx, "/api/v1/query?query="+url.QueryEscape(query), &result) && result.Status == "success" && result.Data.ResultType == "vector" && len(result.Data.Result) > 0
	add("coverage", "Required metrics and alert destination", ok)
	return h
}
