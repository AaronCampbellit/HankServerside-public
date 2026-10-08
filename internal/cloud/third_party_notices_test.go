package cloud

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowserNoticesAreAvailableWithoutServingOtherTextFiles(t *testing.T) {
	recorder := httptest.NewRecorder()
	serveUIAsset(recorder, httptest.NewRequest(http.MethodGet, "/assets/THIRD_PARTY_NOTICES.txt", nil))
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("notice response = %d %q", recorder.Code, recorder.Header().Get("Content-Type"))
	}
	for _, notice := range []string{"Permission is hereby granted", "Meta Platforms, Inc.", "SIL OPEN FONT LICENSE"} {
		if !strings.Contains(recorder.Body.String(), notice) {
			t.Fatalf("browser distribution is missing notice %q", notice)
		}
	}
	for _, path := range []string{"/assets/arbitrary.txt", "/assets/nested/THIRD_PARTY_NOTICES.txt"} {
		recorder := httptest.NewRecorder()
		serveUIAsset(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("unexpected text asset %q is served with status %d", path, recorder.Code)
		}
	}
}
