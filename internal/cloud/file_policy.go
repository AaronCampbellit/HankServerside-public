package cloud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

type fileAccessPolicy struct {
	Read            *bool    `json:"read"`
	Write           *bool    `json:"write"`
	Delete          *bool    `json:"delete"`
	AllowedPrefixes []string `json:"allowed_prefixes"`
	BlockedPrefixes []string `json:"blocked_prefixes"`
	MaxUploadBytes  int64    `json:"max_upload_bytes"`
}

func (s *Server) authorizeFileCommandPolicy(ctx context.Context, homeID string, command protocol.RoutedCommand) error {
	checks, uploadBytes, err := fileCommandPolicyChecks(command)
	if err != nil || len(checks) == 0 {
		return err
	}
	profile, err := s.store.GetHomeServiceProfile(ctx, homeID, domain.ServiceTypeSMB)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	policy := parseFileAccessPolicy(profile.PublicConfigJSON)
	if uploadBytes > 0 && policy.MaxUploadBytes > 0 && uploadBytes > policy.MaxUploadBytes {
		return errors.New("file source policy upload size limit exceeded")
	}
	for _, check := range checks {
		if err := policy.allow(check.action, check.path); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) maxFileUploadBytes(ctx context.Context, homeID string) (int64, error) {
	profile, err := s.store.GetHomeServiceProfile(ctx, homeID, domain.ServiceTypeSMB)
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return parseFileAccessPolicy(profile.PublicConfigJSON).MaxUploadBytes, nil
}

type filePolicyCheck struct {
	action string
	path   string
}

func fileCommandPolicyChecks(command protocol.RoutedCommand) ([]filePolicyCheck, int64, error) {
	switch command.Command {
	case "files.list", "files.list_page", "files.stat", "files.download", "files.search":
		var request struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(command.Body, &request)
		return []filePolicyCheck{{action: "read", path: request.Path}}, 0, nil
	case "files.upload", "files.create_directory":
		var request struct {
			Path          string `json:"path"`
			ContentBase64 string `json:"content_base64"`
			SizeBytes     int64  `json:"size_bytes"`
		}
		_ = json.Unmarshal(command.Body, &request)
		return []filePolicyCheck{{action: "write", path: request.Path}}, max(decodedBase64Size(request.ContentBase64), request.SizeBytes), nil
	case "files.rename":
		var request struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		_ = json.Unmarshal(command.Body, &request)
		return []filePolicyCheck{{action: "write", path: request.From}, {action: "write", path: request.To}}, 0, nil
	case "files.move":
		var request struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		_ = json.Unmarshal(command.Body, &request)
		return []filePolicyCheck{{action: "delete", path: request.From}, {action: "write", path: request.To}}, 0, nil
	case "files.move_rollback":
		var request struct {
			To string `json:"to"`
		}
		_ = json.Unmarshal(command.Body, &request)
		return []filePolicyCheck{{action: "delete", path: request.To}}, 0, nil
	case "files.delete":
		var request struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(command.Body, &request)
		return []filePolicyCheck{{action: "delete", path: request.Path}}, 0, nil
	default:
		return nil, 0, nil
	}
}

func decodedBase64Size(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	size := base64.StdEncoding.DecodedLen(len(value))
	for strings.HasSuffix(value, "=") && size > 0 {
		size--
		value = strings.TrimSuffix(value, "=")
	}
	return int64(size)
}

func parseFileAccessPolicy(raw string) fileAccessPolicy {
	var wrapper struct {
		Policy fileAccessPolicy `json:"policy"`
	}
	if json.Unmarshal([]byte(raw), &wrapper) == nil && (wrapper.Policy.Read != nil || wrapper.Policy.Write != nil || wrapper.Policy.Delete != nil || len(wrapper.Policy.AllowedPrefixes) > 0 || len(wrapper.Policy.BlockedPrefixes) > 0) {
		return wrapper.Policy
	}
	var policy fileAccessPolicy
	_ = json.Unmarshal([]byte(raw), &policy)
	return policy
}

func (p fileAccessPolicy) allow(action string, rawPath string) error {
	switch action {
	case "read":
		if p.Read != nil && !*p.Read {
			return errors.New("file source policy denies read")
		}
	case "write":
		if p.Write != nil && !*p.Write {
			return errors.New("file source policy denies write")
		}
	case "delete":
		if p.Delete != nil && !*p.Delete {
			return errors.New("file source policy denies delete")
		}
	}
	path := cleanPolicyPath(rawPath)
	for _, prefix := range p.BlockedPrefixes {
		if pathHasPolicyPrefix(path, prefix) {
			return errors.New("file source policy blocks this path")
		}
	}
	if len(p.AllowedPrefixes) > 0 {
		for _, prefix := range p.AllowedPrefixes {
			if pathHasPolicyPrefix(path, prefix) {
				return nil
			}
		}
		return errors.New("file source policy does not allow this path")
	}
	return nil
}

func cleanPolicyPath(value string) string {
	value = filepath.ToSlash(filepath.Clean("/" + strings.TrimSpace(value)))
	return strings.TrimSuffix(value, "/")
}

func pathHasPolicyPrefix(path string, prefix string) bool {
	prefix = cleanPolicyPath(prefix)
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}
