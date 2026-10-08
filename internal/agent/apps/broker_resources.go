package apps

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/protocol"
)

func (m *Manager) brokerFor(app InstalledApp, state protocol.AppPermissionState) BrokerHandler {
	return func(ctx context.Context, request BrokerRequest) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		m.mu.RLock()
		current, ok := m.apps[app.Manifest.ID]
		valid := ok && current.Revision == app.Revision
		m.mu.RUnlock()
		if !valid {
			return nil, ErrPermissionRefused
		}
		var permission protocol.AppPermissionTarget
		found := false
		for _, target := range state.Targets {
			if target.ID == request.Permission && target.Fingerprint != "" && app.Grants[target.ID] == target.Fingerprint {
				permission = target
				found = true
				break
			}
		}
		if !found {
			return nil, ErrPermissionRefused
		}
		if permission.Kind == "network" {
			return brokerHTTP(ctx, permission, request)
		}
		files := m.files.Load()
		if files == nil {
			return nil, ErrPermissionRefused
		}
		captured, identity, err := files.CaptureAppSource(permission.Target)
		if err != nil {
			return nil, ErrPermissionRefused
		}
		defer captured.CloseAppSource()
		expected := permissionFingerprint(app.Manifest.ID, state.PackageHash, permission.Kind, permission.Field, permission.Target, identity, []string(nil))
		if expected != permission.Fingerprint {
			return nil, ErrPermissionRefused
		}
		files = captured

		const maxChunk = 256 << 10
		if request.Offset < 0 || len(request.Data) > maxChunk {
			return nil, ErrPermissionRefused
		}
		switch request.Operation {
		case "files.list":
			if permission.Kind != "files_read" {
				break
			}
			items, err := files.ListSource(ctx, permission.Target, request.Path)
			if len(items) > 10000 {
				return nil, errors.New("file listing exceeds broker limit")
			}
			return protocol.FilesListResponse{Items: items}, err
		case "files.stat":
			if permission.Kind != "files_read" {
				break
			}
			return files.StatSource(ctx, permission.Target, request.Path)
		case "files.read":
			if permission.Kind != "files_read" {
				break
			}
			handle, info, err := files.OpenReaderSource(ctx, permission.Target, request.Path, request.Offset)
			if err != nil {
				return nil, err
			}
			defer handle.Close()
			stop := context.AfterFunc(ctx, func() { _ = handle.Close() })
			defer stop()
			data, err := io.ReadAll(io.LimitReader(handle, maxChunk))
			return map[string]any{"data": data, "size": info.Size(), "offset": request.Offset}, err
		case "files.write":
			if permission.Kind != "files_write" {
				break
			}
			handle, offset, err := files.OpenWriterSource(ctx, permission.Target, request.Path, request.Offset)
			if err != nil {
				return nil, err
			}
			stop := context.AfterFunc(ctx, func() { _ = handle.Close() })
			defer stop()
			n, err := handle.Write(request.Data)
			closeErr := handle.Close()
			if err == nil {
				err = closeErr
			}
			return map[string]any{"written": n, "offset": offset}, err
		case "files.mkdir":
			if permission.Kind != "files_write" {
				break
			}
			return nil, files.CreateDirectorySource(ctx, permission.Target, request.Path)
		}
		return nil, ErrPermissionRefused
	}
}

func brokerHTTP(ctx context.Context, permission protocol.AppPermissionTarget, request BrokerRequest) (any, error) {
	if request.Operation != "http.request" || len(request.Data) > 256<<10 {
		return nil, ErrPermissionRefused
	}
	base, err := safeAppURL(permission.Target)
	if err != nil {
		return nil, err
	}
	target, err := safeAppURL(request.URL)
	if err != nil {
		return nil, err
	}
	if base.Scheme != target.Scheme || !strings.EqualFold(base.Host, target.Host) || !(target.Path == base.Path || strings.HasPrefix(target.Path, strings.TrimRight(base.Path, "/")+"/")) {
		return nil, ErrPermissionRefused
	}
	switch request.Method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE":
	default:
		return nil, ErrPermissionRefused
	}
	port := base.Port()
	if port == "" {
		port = "443"
		if base.Scheme == "http" {
			port = "80"
		}
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 32 << 10, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != net.JoinHostPort(base.Hostname(), port) {
			return nil, ErrPermissionRefused
		}
		dialer := net.Dialer{Timeout: 5 * time.Second}
		var err error
		for _, ip := range permission.Addresses {
			if net.ParseIP(ip) == nil {
				return nil, ErrPermissionRefused
			}
			var conn net.Conn
			conn, err = dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip, port))
			if err == nil {
				return conn, nil
			}
		}
		if err == nil {
			err = ErrPermissionRefused
		}
		return nil, err
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, request.Method, target.String(), bytes.NewReader(request.Data))
	if err != nil {
		return nil, ErrPermissionRefused
	}
	for name, value := range request.Headers {
		switch strings.ToLower(name) {
		case "host", "connection", "proxy-authorization", "proxy-connection", "transfer-encoding", "upgrade", "te", "trailer", "content-length":
			return nil, ErrPermissionRefused
		}
		if len(name) > 128 || len(value) > 8192 {
			return nil, ErrPermissionRefused
		}
		req.Header.Set(name, value)
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("app HTTP request failed")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 512<<10+1))
	if err != nil || len(data) > 512<<10 {
		return nil, errors.New("app HTTP response exceeds broker limit")
	}
	return map[string]any{"status": response.StatusCode, "content_type": response.Header.Get("Content-Type"), "data": data}, nil
}
