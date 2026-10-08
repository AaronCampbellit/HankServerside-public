package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCrossPlatformPairingIsBoundSingleUseAndCSRFProtected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ts, _, _, session, conn := setupServerAndAgent(t, ctx)
	defer ts.Close()
	defer conn.CloseNow()
	for _, platform := range []string{"macos", "windows"} {
		t.Run(platform, func(t *testing.T) {
			path := "/v1/home/agent-enrollments/" + platform
			req, _ := http.NewRequest("POST", ts.URL+path, strings.NewReader(`{}`))
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
			response, err := ts.Client().Do(req)
			must(t, err)
			response.Body.Close()
			if response.StatusCode != 403 {
				t.Fatal("cookie write bypassed CSRF")
			}
			var created struct {
				ID   string `json:"id"`
				Code string `json:"pairing_code"`
			}
			requestJSON(t, ts, session, "POST", path, map[string]any{}, &created)
			if len(created.Code) != 14 {
				t.Fatal("missing pairing code")
			}
			prefix := "mac_"
			if platform == "windows" {
				prefix = "win_"
			}
			body := map[string]any{"device_id": prefix + strings.Repeat("a", 32), "name": "Test device", "agent_type": "worker", "platform": platform, "architecture": "arm64", "credential_hash": hashToken("device-secret-" + platform)}
			consume := func(path string) int {
				data, _ := json.Marshal(body)
				req, _ := http.NewRequest("POST", ts.URL+path, bytes.NewReader(data))
				req.Header.Set("Authorization", "Hank-Enrollment "+created.Code)
				req.Header.Set("Content-Type", "application/json")
				res, err := ts.Client().Do(req)
				must(t, err)
				io.Copy(io.Discard, res.Body)
				res.Body.Close()
				return res.StatusCode
			}
			if consume("/v1/agent/enrollments/linux/consume") != 404 {
				t.Fatal("Linux compatibility endpoint accepted other platform")
			}
			originalID := body["device_id"]
			body["platform"] = "linux"
			body["device_id"] = "linux_" + strings.Repeat("b", 32)
			if consume("/v1/agent/enrollments/consume") != 404 {
				t.Fatal("wrong platform accepted")
			}
			body["platform"] = platform
			body["device_id"] = originalID
			if status := consume("/v1/agent/enrollments/consume"); status != 201 {
				t.Fatalf("consume status %d", status)
			}
			if consume("/v1/agent/enrollments/consume") != 404 {
				t.Fatal("pairing code replay accepted")
			}
			res := doJSONRequest(t, ts, session, "GET", path, nil)
			data := []byte(res.Body)
			if bytes.Contains(data, []byte(created.Code)) || bytes.Contains(data, []byte("device-secret-"+platform)) {
				t.Fatal("history leaked secret")
			}
		})
	}
	var combined struct {
		Enrollments []struct {
			Platform string `json:"platform"`
		} `json:"enrollments"`
	}
	requestJSON(t, ts, session, "GET", "/v1/home/agent-enrollments", nil, &combined)
	if len(combined.Enrollments) != 2 {
		t.Fatal("combined enrollment history missing platforms")
	}
	requestJSON(t, ts, session, "GET", "/v1/home/agent-enrollments/linux", nil, &combined)
	if len(combined.Enrollments) != 0 {
		t.Fatal("Linux history included another platform")
	}
}

func TestFleetDismissalRequiresAuthenticationCSRFAndInactiveGrant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ts, _, agent, session, conn := setupServerAndAgent(t, ctx)
	defer ts.Close()
	defer conn.CloseNow()
	var created struct {
		Grant struct {
			ID string `json:"id"`
		} `json:"grant"`
	}
	requestJSON(t, ts, session, "POST", "/v1/fleet/grants", map[string]any{"agents": []string{agent}, "operations": []string{"job.read"}, "hours": 1}, &created)
	path := "/v1/fleet/grants/" + created.Grant.ID + "/dismiss"
	if res := doJSONRequest(t, ts, session, "POST", path, map[string]any{}); res.StatusCode != 409 {
		t.Fatal("active grant dismissed")
	}
	if res := doJSONRequest(t, ts, "", "POST", path, map[string]any{}); res.StatusCode != 401 {
		t.Fatal("unauthenticated dismissal accepted")
	}
	req, _ := http.NewRequest("POST", ts.URL+path, strings.NewReader(`{}`))
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	res, err := ts.Client().Do(req)
	must(t, err)
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("dismissal bypassed CSRF")
	}
}
