package storageops

import (
	"fmt"
	"regexp"
)

var (
	postgresURLPasswordPattern = regexp.MustCompile(`(?i)(postgres(?:ql)?://[^:/\s]+:)([^@\s]+)(@)`)
	sensitivePairPattern       = regexp.MustCompile(`(?i)(repo1-cipher-pass|cipher[_-]?pass|password|passwd|token|secret)(\s*[=:]\s*)([^,\s"']+)`)
	bearerTokenPattern         = regexp.MustCompile(`(?i)(bearer\s+)([A-Za-z0-9._~+/=-]+)`)
)

func RedactSensitive(value string) string {
	value = postgresURLPasswordPattern.ReplaceAllString(value, `${1}[redacted]${3}`)
	value = sensitivePairPattern.ReplaceAllString(value, `${1}${2}[redacted]`)
	value = bearerTokenPattern.ReplaceAllString(value, `${1}[redacted]`)
	return value
}

func RedactEvent(event Event) Event {
	event.Message = RedactSensitive(event.Message)
	event.Details = redactMap(event.Details)
	return event
}

func redactMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	redacted := make(map[string]any, len(values))
	for key, value := range values {
		redacted[key] = redactValue(value)
	}
	return redacted
}

func redactValue(value any) any {
	switch typed := value.(type) {
	case string:
		return RedactSensitive(typed)
	case []string:
		redacted := make([]string, len(typed))
		for index, item := range typed {
			redacted[index] = RedactSensitive(item)
		}
		return redacted
	case []any:
		redacted := make([]any, len(typed))
		for index, item := range typed {
			redacted[index] = redactValue(item)
		}
		return redacted
	case map[string]any:
		return redactMap(typed)
	case fmt.Stringer:
		return RedactSensitive(typed.String())
	default:
		return typed
	}
}
