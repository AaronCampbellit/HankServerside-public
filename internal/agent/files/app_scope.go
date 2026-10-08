package files

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
)

// CaptureAppSource snapshots one exact source and its policy under the same lock.
// Later configuration changes cannot retarget an already authorized operation.
// Credentials remain in the agent; the identity contains only public scope data.
func (s *Service) CaptureAppSource(sourceID string) (*Service, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sourceID == "" {
		return nil, "", ErrDisabled
	}
	identity := func(scope any) string {
		data, _ := json.Marshal(scope)
		hash := sha256.Sum256(data)
		return hex.EncodeToString(hash[:])
	}
	for _, cfg := range s.localSources {
		if cfg.ID == sourceID && cfg.Enabled() {
			resolved, err := filepath.EvalSymlinks(cfg.Root)
			if err != nil {
				return nil, "", err
			}
			resolved, err = filepath.Abs(resolved)
			if err != nil {
				return nil, "", err
			}
			cfg.Root = resolved
			return NewWithConfig(Config{LocalSources: []LocalConfig{cfg}}), identity(cfg), nil
		}
	}
	for _, cfg := range s.smbShares {
		if cfg.ID == sourceID && cfg.Enabled() {
			public := cfg
			public.Password = ""
			return NewWithConfig(Config{Shares: []SMBConfig{cfg}}), identity(public), nil
		}
	}
	return nil, "", errors.New("configured app file source is unavailable")
}

// CloseAppSource releases network connections owned by a captured source only.
func (s *Service) CloseAppSource() { s.ApplySMBConfigs(nil) }
