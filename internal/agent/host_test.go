package agent

import "testing"

func TestParseProcMemoryReportsHostUsedAndTotalBytes(t *testing.T) {
	used, total, ok := parseProcMemory(`MemTotal:       16384000 kB
MemFree:         1024000 kB
MemAvailable:    4096000 kB
Buffers:          128000 kB
`)
	if !ok {
		t.Fatal("valid proc memory was rejected")
	}
	if total != 16384000*1024 {
		t.Fatalf("total = %d", total)
	}
	if used != (16384000-4096000)*1024 {
		t.Fatalf("used = %d", used)
	}
}

func TestParseProcMemoryRejectsSampleWithoutAvailableMemory(t *testing.T) {
	if _, _, ok := parseProcMemory("MemTotal: 1024 kB\nMemFree: 256 kB\n"); ok {
		t.Fatal("memory sample without MemAvailable was accepted as 100% used")
	}
}

func TestParseProcUptime(t *testing.T) {
	if got := parseProcUptime("12345.67 890.12\n"); got != 12345 {
		t.Fatalf("uptime = %d", got)
	}
}
