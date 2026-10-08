package storageops

import (
	"strconv"
	"strings"
	"time"
)

func CronMatches(spec string, at time.Time) bool {
	fields := strings.Fields(strings.TrimSpace(spec))
	if len(fields) != 5 {
		return false
	}
	values := []int{at.Minute(), at.Hour(), at.Day(), int(at.Month()), int(at.Weekday())}
	for index, field := range fields {
		if !cronFieldMatches(field, values[index]) {
			if index == 4 && values[index] == 0 && cronFieldMatches(field, 7) {
				continue
			}
			return false
		}
	}
	return true
}

func cronFieldMatches(field string, value int) bool {
	for _, part := range strings.Split(field, ",") {
		if cronPartMatches(part, value) {
			return true
		}
	}
	return false
}

func cronPartMatches(part string, value int) bool {
	part = strings.TrimSpace(part)
	if part == "" {
		return false
	}
	step := 1
	if strings.Contains(part, "/") {
		chunks := strings.Split(part, "/")
		if len(chunks) != 2 {
			return false
		}
		parsedStep, err := strconv.Atoi(chunks[1])
		if err != nil || parsedStep <= 0 {
			return false
		}
		step = parsedStep
		part = chunks[0]
	}
	min, max := 0, 0
	switch {
	case part == "*":
		min, max = 0, value
		max = value + (step - value%step)
	case strings.Contains(part, "-"):
		chunks := strings.Split(part, "-")
		if len(chunks) != 2 {
			return false
		}
		start, errStart := strconv.Atoi(chunks[0])
		end, errEnd := strconv.Atoi(chunks[1])
		if errStart != nil || errEnd != nil {
			return false
		}
		min, max = start, end
	default:
		parsed, err := strconv.Atoi(part)
		if err != nil {
			return false
		}
		return parsed == value
	}
	return value >= min && value <= max && (value-min)%step == 0
}
