package cloud

import (
	"context"
	"github.com/dropfile/HankServerside/internal/store"
	"net/http"
	"testing"
	"time"
)

func TestFleetGrantCalendarExpiry(t *testing.T) {
	for _, tc := range []struct{ start, duration, want string }{
		{"2026-01-31T12:34:56Z", "1_month", "2026-02-28T12:34:56Z"},
		{"2028-01-31T12:34:56Z", "1_month", "2028-02-29T12:34:56Z"},
		{"2026-11-30T12:34:56Z", "3_months", "2027-02-28T12:34:56Z"},
		{"2026-08-31T12:34:56Z", "6_months", "2027-02-28T12:34:56Z"},
		{"2028-02-29T12:34:56Z", "1_year", "2029-02-28T12:34:56Z"},
		{"2026-09-26T12:34:56Z", "infinite", ""},
	} {
		t.Run(tc.start+tc.duration, func(t *testing.T) {
			now, _ := time.Parse(time.RFC3339, tc.start)
			got, err := fleetGrantExpiry(now, tc.duration, nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if got != nil {
					t.Fatal("infinite grant has expiry")
				}
				return
			}
			if got == nil || got.Format(time.RFC3339) != tc.want {
				t.Fatalf("expiry=%v want %s", got, tc.want)
			}
		})
	}
	now := time.Now()
	for _, duration := range []string{"", "forever", "12_months"} {
		if _, err := fleetGrantExpiry(now, duration, nil); err == nil {
			t.Fatalf("accepted %q", duration)
		}
	}
	for _, hours := range []int{0, -1, 169} {
		if _, err := fleetGrantExpiry(now, "", &hours); err == nil {
			t.Fatalf("accepted hours %d", hours)
		}
	}
	hours := 24
	if _, err := fleetGrantExpiry(now, "infinite", &hours); err == nil {
		t.Fatal("ambiguous request accepted")
	}
	got, err := fleetGrantExpiry(now, "", &hours)
	if err != nil || got == nil || !got.Equal(now.Add(24*time.Hour)) {
		t.Fatal("legacy hours changed")
	}
}

func TestFleetGrantDurationAPI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ts, _, agent, session, conn := setupServerAndAgent(t, ctx)
	defer ts.Close()
	defer conn.CloseNow()
	for _, duration := range []string{"1_month", "3_months", "6_months", "1_year", "infinite"} {
		t.Run(duration, func(t *testing.T) {
			var result struct {
				Grant store.FleetGrant `json:"grant"`
			}
			requestJSON(t, ts, session, "POST", "/v1/fleet/grants", map[string]any{"agents": []string{agent}, "operations": []string{"job.read"}, "duration": duration}, &result)
			if result.Grant.ID == "" || result.Grant.State != "pending" {
				t.Fatal("missing pending grant")
			}
			if (result.Grant.ExpiresAt == nil) != (duration == "infinite") {
				t.Fatal("incorrect expiry")
			}
			if result.Grant.ExpiresAt != nil && result.Grant.ExpiresAt.Before(time.Now().Add(27*24*time.Hour)) {
				t.Fatal("duration truncated")
			}
		})
	}
	for _, body := range []map[string]any{{"duration": "forever"}, {"duration": "infinite", "hours": 1}, {}} {
		body["agents"] = []string{agent}
		body["operations"] = []string{"job.read"}
		response := doJSONRequest(t, ts, session, "POST", "/v1/fleet/grants", body)
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("bad request returned %d", response.StatusCode)
		}
	}
}
