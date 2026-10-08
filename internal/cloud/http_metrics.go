package cloud

import (
	"net/http"
	"time"
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (s *Server) metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		// ServeMux supplies the registered pattern after dispatch. Never use a
		// request path as a fallback: it can contain credentials or unlimited IDs.
		route := r.Pattern
		if route == "" || (route == "/" && r.URL.Path != "/") {
			route = "unmatched"
		}
		s.metrics.RecordHTTP(route, r.Method, recorder.status, time.Since(start))
	})
}
