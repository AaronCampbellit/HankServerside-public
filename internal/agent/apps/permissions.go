package apps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	agentfiles "github.com/dropfile/HankServerside/internal/agent/files"
	"github.com/dropfile/HankServerside/internal/protocol"
)

func (m *Manager) SetFilesService(files *agentfiles.Service) {
	m.files.Store(files)
}
func permissionFingerprint(values ...any) string {
	b, _ := json.Marshal(values)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (m *Manager) permissionState(ctx context.Context, app InstalledApp) (protocol.AppPermissionState, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	hash, err := snapshotPackage(ctx, app.Path, "")
	if err != nil {
		return protocol.AppPermissionState{}, err
	}
	state := protocol.AppPermissionState{PackageHash: hash, Grants: cloneGrants(app.Grants), Sandbox: "restricted.v1"}
	config := map[string]any{}
	if len(app.PublicConfig) > 0 && json.Unmarshal(app.PublicConfig, &config) != nil {
		return state, ErrPermissionRefused
	}
	add := func(kind, field, target, details string, addresses []string) {
		p := protocol.AppPermissionTarget{ID: kind + ":" + field, Kind: kind, Field: field, Target: target, Addresses: addresses}
		if target != "" && details != "" {
			p.Fingerprint = permissionFingerprint(app.Manifest.ID, hash, kind, field, target, details, addresses)
		}
		state.Targets = append(state.Targets, p)
	}
	for _, permission := range app.Manifest.Permissions.Network {
		raw, _ := config[permission.Field].(string)
		raw = strings.TrimSpace(raw)
		parsed, err := safeAppURL(raw)
		var addresses []string
		if err == nil {
			lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			ips, e := net.DefaultResolver.LookupIPAddr(lookupCtx, parsed.Hostname())
			cancel()
			if e == nil {
				for _, ip := range ips {
					if ip.IP.IsUnspecified() || ip.IP.IsMulticast() || ip.IP.IsLinkLocalUnicast() || ip.Zone != "" {
						addresses = nil
						break
					}
					addresses = append(addresses, ip.IP.String())
				}
				sort.Strings(addresses)
			}
		}
		details := ""
		if len(addresses) > 0 {
			details = raw
		}
		add("network", permission.Field, raw, details, addresses)
	}
	for _, permission := range app.Manifest.Permissions.Files {
		source, _ := config[permission.Field].(string)
		details := ""
		if files := m.files.Load(); files != nil {
			snapshot, identity, err := files.CaptureAppSource(source)
			if err == nil {
				details = identity
				snapshot.CloseAppSource()
			}
		}

		// Separate read and write grants make destructive scope visible to admins.
		add("files_read", permission.Field, source, details, nil)
		add("files_write", permission.Field, source, details, nil)
	}
	return state, nil
}
func safeAppURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || strings.ContainsAny(u.Path, "\\%\x00\r\n") {
		return nil, ErrPermissionRefused
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == ".." || segment == "." {
			return nil, ErrPermissionRefused
		}
	}
	return u, nil
}
func cloneGrants(grants map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range grants {
		out[k] = v
	}
	return out
}
func validateGrants(state protocol.AppPermissionState, grants map[string]string) error {
	for id, fp := range grants {
		found := false
		for _, p := range state.Targets {
			if p.ID == id && p.Fingerprint != "" && p.Fingerprint == fp {
				found = true
			}
		}
		if !found {
			return ErrPermissionRefused
		}
	}
	return nil
}
func appGrantsPath(appPath string) string {
	return filepath.Join(filepath.Dir(filepath.Dir(appPath)), filepath.Base(filepath.Dir(appPath))+"-permissions", filepath.Base(appPath)+".json")
}
func readAppGrants(appPath string) (map[string]string, error) {
	path := appGrantsPath(appPath)
	if err := validateGrantDirectory(filepath.Dir(path)); errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	} else if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 128<<10 {
		return nil, ErrPermissionRefused
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var grants map[string]string
	err = json.Unmarshal(data, &grants)
	return grants, err
}
func writeAppGrants(app InstalledApp) error {
	path := appGrantsPath(app.Path)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := validateGrantDirectory(dir); err != nil {
		return err
	}
	data, err := json.Marshal(cloneGrants(app.Grants))
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".grants-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	folder, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer folder.Close()
	return folder.Sync()
}

func (m *Manager) cancelAppLocked(appID string) {
	for _, cancel := range m.active[appID] {
		cancel()
	}
}
func (m *Manager) invokeApp(ctx context.Context, app InstalledApp, spec InvokeSpec) (AppStdioResponse, error) {
	m.mu.Lock()
	current, ok := m.apps[app.Manifest.ID]
	if !ok || current.Revision != app.Revision {
		m.mu.Unlock()
		return AppStdioResponse{}, ErrPermissionRefused
	}
	runCtx, cancel := context.WithCancel(ctx)
	m.nextInvocation++
	id := m.nextInvocation
	if m.active == nil {
		m.active = map[string]map[uint64]context.CancelFunc{}
	}
	if m.active[app.Manifest.ID] == nil {
		m.active[app.Manifest.ID] = map[uint64]context.CancelFunc{}
	}
	m.active[app.Manifest.ID][id] = cancel
	m.mu.Unlock()
	defer func() { cancel(); m.mu.Lock(); delete(m.active[app.Manifest.ID], id); m.mu.Unlock() }()
	return m.runner.Invoke(runCtx, spec)
}

func validateGrantDirectory(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || resolved != dir || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return ErrPermissionRefused
	}
	return nil
}
