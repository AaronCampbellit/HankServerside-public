package cloud

import (
	"strings"

	"github.com/dropfile/HankServerside/internal/domain"
)

func featureForCommand(command string) string {
	switch {
	case strings.HasPrefix(command, "homeassistant."):
		return domain.HomePermissionFeatureHomeAssistant
	case strings.HasPrefix(command, "files."):
		return domain.HomePermissionFeatureFiles
	case strings.HasPrefix(command, "notes."):
		return domain.HomePermissionFeatureNotes
	default:
		return ""
	}
}
