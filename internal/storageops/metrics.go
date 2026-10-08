package storageops

import (
	"fmt"
	"strings"
)

func RenderMetrics(status StatusSnapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "hank_db_checksum_enabled %d\n", boolGauge(status.Checksum.Enabled))
	fmt.Fprintf(&b, "hank_db_checksum_corruption_detected %d\n", boolGauge(status.Checksum.CorruptionDetected))
	fmt.Fprintf(&b, "hank_db_checksum_failures_total %d\n", status.Checksum.FailureCount)
	fmt.Fprintf(&b, "hank_db_backup_failures_total %d\n", status.Backup.FailureCount)
	fmt.Fprintf(&b, "hank_db_backup_sets %d\n", len(status.Backup.Backups))
	if status.Backup.LastSuccessfulAt != nil {
		fmt.Fprintf(&b, "hank_db_backup_last_success_unixtime %d\n", status.Backup.LastSuccessfulAt.Unix())
	}
	if status.Restore.LastTestAt != nil {
		fmt.Fprintf(&b, "hank_db_restore_test_last_success_unixtime %d\n", status.Restore.LastTestAt.Unix())
	}
	if status.Restore.LastPrimaryRestoreAt != nil {
		fmt.Fprintf(&b, "hank_db_primary_restore_last_success_unixtime %d\n", status.Restore.LastPrimaryRestoreAt.Unix())
	}
	counts := map[string]int{}
	failures := map[string]int{}
	for _, event := range status.Events {
		counts[event.Operation]++
		if IsFailureEvent(event) {
			failures[event.Operation]++
		}
	}
	for operation, count := range counts {
		fmt.Fprintf(&b, "hank_db_ops_events_total{operation=%q} %d\n", operation, count)
	}
	for operation, count := range failures {
		fmt.Fprintf(&b, "hank_db_ops_failures_total{operation=%q} %d\n", operation, count)
	}
	return b.String()
}

func boolGauge(value bool) int {
	if value {
		return 1
	}
	return 0
}
