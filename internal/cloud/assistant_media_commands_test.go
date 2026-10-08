package cloud

import (
	"testing"

	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestGramatonAppCommandID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		command string
		want    string
		ok      bool
	}{
		{command: protocol.CommandMediaSettingsStatus, want: "settings_status", ok: true},
		{command: protocol.CommandMediaSettingsApply, want: "settings_apply", ok: true},
		{command: protocol.CommandMediaSearch, want: "search", ok: true},
		{command: protocol.CommandMediaPlanDownload, want: "plan_download", ok: true},
		{command: protocol.CommandMediaDownloadStart, want: "download_start", ok: true},
		{command: protocol.CommandMediaDownloadStatus, want: "download_status", ok: true},
		{command: protocol.CommandMediaDownloadJobs, want: "download_jobs", ok: true},
		{command: protocol.CommandMediaDownloadCancel, want: "download_cancel", ok: true},
		{command: protocol.CommandMediaImageFetch, want: "image_fetch", ok: true},
		{command: protocol.CommandSystemPing, ok: false},
	}

	for _, test := range tests {
		test := test
		t.Run(test.command, func(t *testing.T) {
			t.Parallel()

			got, ok := gramatonAppCommandID(test.command)
			if got != test.want || ok != test.ok {
				t.Fatalf("gramatonAppCommandID(%q) = %q, %v; want %q, %v", test.command, got, ok, test.want, test.ok)
			}
		})
	}
}
