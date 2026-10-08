package cloud

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dropfile/HankServerside/internal/observability"
)

func TestHTTPMetricsBoundUntrustedLabels(t *testing.T) {
	s := &Server{metrics: observability.NewMetrics()}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
		}
	})
	for _, route := range []string{"/install/linux/", "/v1/me/notes/", "/v1/file-transfers/"} {
		mux.HandleFunc(route, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := s.metricsMiddleware(routeDeadlineMiddleware(mux))
	for i := range 1000 {
		marker := fmt.Sprintf("private-marker-%d", i)
		for _, prefix := range []string{"/", "/install/linux/", "/v1/me/notes/", "/v1/file-transfers/"} {
			for _, method := range []string{http.MethodGet, marker} {
				handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, prefix+marker+"?secret="+marker, nil))
			}
		}
	}
	for _, route := range []string{"/", "/healthz"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, route, nil))
	}
	output := s.metrics.RenderPrometheus()
	if strings.Contains(output, "private-marker") || strings.Contains(output, "secret=") {
		t.Fatal("request-controlled value reached metrics")
	}
	if got := strings.Count(output, "hank_http_requests_total{"); got != 10 {
		t.Fatalf("got %d HTTP series after 8002 requests, want 10", got)
	}
	for _, series := range []string{
		`route="GET unmatched 404"} 1000`, `route="OTHER unmatched 404"} 1000`,
		`route="GET /install/linux/ 401"} 1000`, `route="GET /v1/me/notes/ 401"} 1000`,
		`route="GET / 200"} 1`, `route="GET /healthz 200"} 1`,
	} {
		if !strings.Contains(output, series) {
			t.Errorf("missing series %s", series)
		}
	}
}

func TestHTTPMetricsRedirectsAndEncodedPaths(t *testing.T) {
	s := &Server{metrics: observability.NewMetrics()}
	mux := http.NewServeMux()
	mux.HandleFunc("/", http.NotFound)
	mux.HandleFunc("/v1/me/notes/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	handler := s.metricsMiddleware(mux)
	for i := range 100 {
		for _, path := range []string{
			"/v1/me/notes", "/v1/me/notes/private-marker-%d", "/v1/me//notes/private-marker-%d",
			"/v1/me/notes/../private-marker-%d", "/v1/me/notes/private-marker-%d%%2fchild",
			"/private-marker-%d%%2fmissing", "/v1/me/notes/%%2e%%2e/private-marker-%d",
		} {
			if strings.Contains(path, "%d") {
				path = fmt.Sprintf(path, i)
			}
			for _, method := range []string{http.MethodGet, http.MethodConnect} {
				handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, "http://hank.test"+path, nil))
			}
		}
	}
	output := s.metrics.RenderPrometheus()
	if strings.Contains(output, "private-marker") {
		t.Fatal("redirect or encoded input reached metrics")
	}
	if n := strings.Count(output, "hank_http_requests_total{"); n > 12 {
		t.Fatalf("unbounded redirect series: %d", n)
	}
}

func TestHTTPMetricsWithoutMuxDoesNotUsePath(t *testing.T) {
	s := &Server{metrics: observability.NewMetrics()}
	s.metricsMiddleware(http.HandlerFunc(http.NotFound)).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/private-marker", nil))
	if !strings.Contains(s.metrics.RenderPrometheus(), `route="GET unmatched 404"} 1`) {
		t.Fatal("missing fixed fallback for requests without a mux pattern")
	}
}
