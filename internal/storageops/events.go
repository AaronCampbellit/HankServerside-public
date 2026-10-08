package storageops

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	DefaultEventLogMaxEntries = 2000
	DefaultEventLogMaxBytes   = 5 * 1024 * 1024

	EventSeverityInfo     = "info"
	EventSeverityWarning  = "warning"
	EventSeverityError    = "error"
	EventSeverityCritical = "critical"

	EventStatusStarted = "started"
	EventStatusSuccess = "success"
	EventStatusFailed  = "failed"
	EventStatusPending = "pending"

	EventOperationChecksum       = "checksum"
	EventOperationAMCheck        = "amcheck"
	EventOperationBackup         = "backup"
	EventOperationRestoreTest    = "restore_test"
	EventOperationPrimaryRestore = "primary_restore"
	EventOperationConfig         = "config"
)

type Event struct {
	ID          string         `json:"id"`
	Time        time.Time      `json:"time"`
	Severity    string         `json:"severity"`
	Operation   string         `json:"operation"`
	Status      string         `json:"status"`
	Message     string         `json:"message"`
	BackupLabel string         `json:"backup_label,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
}

type EventFilter struct {
	Limit        int
	Severity     string
	Operation    string
	FailuresOnly bool
}

func EventLogPath(logDir string) string {
	return filepath.Join(dirOrDefault(logDir, DefaultLogDir), "storage-events.jsonl")
}

func NewEvent(operation string, status string, severity string, message string) Event {
	return Event{
		ID:        newEventID(),
		Time:      time.Now().UTC(),
		Severity:  normalizeSeverity(severity),
		Operation: strings.TrimSpace(operation),
		Status:    strings.TrimSpace(status),
		Message:   strings.TrimSpace(message),
	}
}

func AppendEvent(logDir string, event Event) (Event, error) {
	if event.ID == "" {
		event.ID = newEventID()
	}
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	event.Severity = normalizeSeverity(event.Severity)
	event = RedactEvent(event)
	if err := ensurePrivateDir(dirOrDefault(logDir, DefaultLogDir)); err != nil {
		return Event{}, err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return Event{}, err
	}
	path := EventLogPath(logDir)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, privateFileMode)
	if err != nil {
		return Event{}, err
	}
	_ = os.Chmod(path, privateFileMode)
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return Event{}, err
	}
	if err := file.Close(); err != nil {
		return Event{}, err
	}
	if err := pruneEventLog(logDir, DefaultEventLogMaxEntries, DefaultEventLogMaxBytes); err != nil {
		return Event{}, err
	}
	return event, nil
}

func ListEvents(logDir string, filter EventFilter) ([]Event, error) {
	path := EventLogPath(logDir)
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	var events []Event
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			events = append(events, NewEvent("event_log", EventStatusFailed, EventSeverityWarning, fmt.Sprintf("Skipped unreadable storage log line: %v", err)))
			continue
		}
		event = RedactEvent(event)
		if !eventMatchesFilter(event, filter) {
			continue
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].Time.After(events[j].Time)
	})
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	if len(events) > filter.Limit {
		events = events[:filter.Limit]
	}
	return events, nil
}

func eventMatchesFilter(event Event, filter EventFilter) bool {
	if filter.Severity != "" && normalizeSeverity(event.Severity) != normalizeSeverity(filter.Severity) {
		return false
	}
	if filter.Operation != "" && event.Operation != filter.Operation {
		return false
	}
	if filter.FailuresOnly && event.Status != EventStatusFailed && event.Severity != EventSeverityError && event.Severity != EventSeverityCritical {
		return false
	}
	return true
}

func normalizeSeverity(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case EventSeverityCritical:
		return EventSeverityCritical
	case EventSeverityError:
		return EventSeverityError
	case EventSeverityWarning, "warn":
		return EventSeverityWarning
	default:
		return EventSeverityInfo
	}
}

func IsFailureEvent(event Event) bool {
	return event.Status == EventStatusFailed || event.Severity == EventSeverityError || event.Severity == EventSeverityCritical
}

func ClearEventLog(logDir string) error {
	if err := ensurePrivateDir(dirOrDefault(logDir, DefaultLogDir)); err != nil {
		return err
	}
	err := os.Remove(EventLogPath(logDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func pruneEventLog(logDir string, maxEntries int, maxBytes int64) error {
	if maxEntries <= 0 && maxBytes <= 0 {
		return nil
	}
	path := EventLogPath(logDir)
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	var lines []string
	var sizes []int64
	var totalBytes int64
	totalEntries := 0
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		lineBytes := int64(len(line) + 1)
		lines = append(lines, line)
		sizes = append(sizes, lineBytes)
		totalBytes += lineBytes
		totalEntries++
		if maxEntries > 0 && len(lines) > maxEntries {
			totalBytes -= sizes[0]
			lines = lines[1:]
			sizes = sizes[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	for maxBytes > 0 && totalBytes > maxBytes && len(lines) > 1 {
		totalBytes -= sizes[0]
		lines = lines[1:]
		sizes = sizes[1:]
	}
	if (maxEntries <= 0 || totalEntries <= maxEntries) && (maxBytes <= 0 || info.Size() <= maxBytes) {
		return nil
	}

	tmp := path + ".tmp"
	output := strings.Join(lines, "\n")
	if output != "" {
		output += "\n"
	}
	if err := writePrivateFile(tmp, []byte(output)); err != nil {
		return err
	}
	return renamePrivateFile(tmp, path)
}

func newEventID() string {
	return fmt.Sprintf("sto_%d", time.Now().UTC().UnixNano())
}
