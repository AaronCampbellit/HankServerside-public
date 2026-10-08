//go:build linux

package apps

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dropfile/HankServerside/internal/protocol"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSandboxFailsClosedWithoutRuntime(t *testing.T) {
	_, err := (Runner{RuntimeDir: filepath.Join(t.TempDir(), "missing")}).Invoke(context.Background(), InvokeSpec{WorkDir: t.TempDir(), Executable: "/bin/true", Timeout: time.Second})
	if !errors.Is(err, ErrSandboxUnavailable) {
		t.Fatalf("expected unavailable sandbox, got %v", err)
	}
}
func TestSandboxActualContainment(t *testing.T) {
	if os.Getenv("HANK_TEST_APP_SANDBOX") != "1" {
		t.Skip("requires the disposable sandbox test image and namespace support")
	}
	dir := t.TempDir()
	host := t.TempDir()
	secret := filepath.Join(host, "agent-secret")
	if err := os.WriteFile(secret, []byte("host-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HANK_FAKE_AGENT_TOKEN", "test-secret-that-must-not-leak")
	body := `#!/usr/bin/python3
import os,json,socket
request=json.loads(input())
assert 'HANK_FAKE_AGENT_TOKEN' not in os.environ
for name in ["HOST_SECRET",'/app/.hank-app-state.json','/proc/1/environ','/var/run/docker.sock']:
 try:
  open(name,'rb').read()
  raise AssertionError('host data exposed')
 except (FileNotFoundError,PermissionError,IsADirectoryError): pass
try:
 open('/app/mutated','w').write('bad')
 raise AssertionError('package is writable')
except PermissionError: pass
except OSError as e:
 assert e.errno == 30
for address in [('1.1.1.1',443),('127.0.0.1',80)]:
 try:
  s=socket.socket();s.settimeout(.2)
  assert s.connect_ex(address)!=0
  s.close()
 except PermissionError: pass
try:
 socket.getaddrinfo('example.com',443)
 raise AssertionError('direct DNS worked')
except (socket.gaierror,PermissionError): pass
open('/tmp/private','w').write('private')
broker=socket.socket(fileno=3).makefile('rwb',buffering=0)
broker.write(json.dumps({'version':'hank.app.broker.v1','id':'one','permission':'none','operation':'http.request'}).encode()+b'\n')
assert json.loads(broker.readline())['error']['code']=='app_permission_refused'
print(json.dumps({'request_id':request['request_id'],'ok':True,'output':{'contained':True}}))
`
	body = strings.ReplaceAll(body, "HOST_SECRET", secret)
	exe := filepath.Join(dir, "app.py")
	if err := os.WriteFile(exe, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, appStateFilename), []byte("agent-secret-state"), 0600); err != nil {
		t.Fatal(err)
	}
	response, err := (Runner{}).Invoke(context.Background(), InvokeSpec{Executable: exe, WorkDir: dir, Timeout: 10 * time.Second, Request: AppStdioRequest{RequestID: "containment"}})
	if err != nil || !response.OK {
		diagnosticCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd, cleanup, setupErr := sandboxCommand(diagnosticCtx, InvokeSpec{Executable: exe, WorkDir: dir})
		if setupErr == nil {
			cmd.Stdin = strings.NewReader("{\"request_id\":\"diagnostic\"}\n")
			data, runErr := cmd.CombinedOutput()
			cleanup()
			t.Logf("synthetic sandbox diagnostic: %s (%v)", data, runErr)
		}
		t.Fatalf("real sandbox failed: response=%+v err=%v", response, err)
	}
}

func TestSandboxBrokerSettingsApplyAndRevocation(t *testing.T) {
	if os.Getenv("HANK_TEST_APP_SANDBOX") != "1" {
		t.Skip("requires disposable sandbox image")
	}
	ctx := context.Background()
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write([]byte("granted response")) }))
	defer target.Close()
	m := installManagerHermesPackageWithScript(t, false, `#!/usr/bin/python3
import os,json,socket,time
request=json.loads(input())
broker=socket.socket(fileno=3).makefile('rwb',buffering=0)
broker.write(json.dumps({'version':'hank.app.broker.v1','id':'request','permission':'network:base','operation':'http.request','method':'GET','url':request['config']['base']+'/check'}).encode()+b'\n')
response=json.loads(broker.readline())
if (request.get("input") or {}).get("wait"): time.sleep(30)
print(json.dumps({'request_id':request['request_id'],'ok':response['ok'],'output':response.get('output'),'error':response.get('error')}))
`)
	m.runner = Runner{}
	app := m.apps["hermes"]
	app.Manifest.Permissions.Network = []NetworkPermission{{Kind: networkPermissionConfiguredBaseURL, Field: "base"}}
	app.Manifest.Commands = append(app.Manifest.Commands, Command{ID: "settings_apply", TimeoutSeconds: 5})
	config, _ := json.Marshal(map[string]string{"base": target.URL + "/api"})
	disabled := false
	if _, err := m.ConfigApply(ctx, protocol.AppsConfigApplyRequest{AppID: "hermes", PublicConfig: config, Enable: &disabled}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := m.ConfigApply(ctx, protocol.AppsConfigApplyRequest{AppID: "hermes", Enable: &enabled}); err == nil {
		t.Fatal("settings_apply accessed ungranted target")
	}
	if hits.Load() != 0 {
		t.Fatal("ungranted target contacted")
	}
	state, err := m.permissionState(ctx, *app)
	if err != nil {
		t.Fatal(err)
	}
	grants := map[string]string{state.Targets[0].ID: state.Targets[0].Fingerprint}
	if _, err := m.ConfigApply(ctx, protocol.AppsConfigApplyRequest{AppID: "hermes", Enable: &enabled, PermissionGrants: &grants}); err != nil {
		t.Fatal("granted settings_apply failed", err)
	}
	if _, err := m.Invoke(ctx, protocol.AppsInvokeRequest{ActorRole: "admin", AppID: "hermes", CommandID: "chat"}); err != nil {
		t.Fatal("granted invocation failed", err)
	}
	if hits.Load() != 2 {
		t.Fatal("settings and invocation did not use broker", hits.Load())
	}
	running := make(chan error, 1)
	go func() {
		_, err := m.Invoke(ctx, protocol.AppsInvokeRequest{ActorRole: "admin", AppID: "hermes", CommandID: "chat", Input: json.RawMessage(`{"wait":true}`)})
		running <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for hits.Load() != 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if hits.Load() != 3 {
		t.Fatal("running invocation did not reach its granted resource")
	}
	empty := map[string]string{}
	if _, err := m.ConfigApply(ctx, protocol.AppsConfigApplyRequest{AppID: "hermes", PermissionGrants: &empty}); err != nil {
		t.Fatal("app vetoed revocation", err)
	}
	select {
	case err := <-running:
		if err == nil {
			t.Fatal("revocation did not cancel running invocation")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("running invocation survived revocation")
	}
	if _, err := m.Invoke(ctx, protocol.AppsInvokeRequest{ActorRole: "admin", AppID: "hermes", CommandID: "chat"}); err == nil {
		t.Fatal("revoked invocation reached resource")
	}
	if hits.Load() != 3 {
		t.Fatal("revoked grant contacted target", hits.Load())
	}
}

func TestSandboxKillsDetachedDescendants(t *testing.T) {
	if os.Getenv("HANK_TEST_APP_SANDBOX") != "1" {
		t.Skip("requires disposable sandbox image")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "app.py")
	script := `#!/usr/bin/python3
import os,json,socket,time
r=json.loads(input())
pid=os.fork()
if pid==0:
 os.setsid()
 if os.fork()!=0:os._exit(0)
 b=socket.socket(fileno=3).makefile('rwb',buffering=0)
 while True:
  b.write(json.dumps({'version':'hank.app.broker.v1','id':'ping','permission':'test','operation':'ping'}).encode()+b'\n')
  b.readline()
  time.sleep(.02)
else:
 time.sleep(.1)
 print(json.dumps({'request_id':r['request_id'],'ok':True,'output':{}}))
`
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	started := time.Now()
	_, err := (Runner{}).Invoke(context.Background(), InvokeSpec{Executable: exe, WorkDir: dir, Timeout: time.Second, Request: AppStdioRequest{RequestID: "detach"}, Broker: func(context.Context, BrokerRequest) (any, error) { calls.Add(1); return nil, nil }})
	if err != nil && !strings.Contains(err.Error(), "timed out") {
		t.Fatal(err)
	}
	if time.Since(started) > 4*time.Second {
		t.Fatal("detached child kept invocation alive")
	}
	count := calls.Load()
	if count == 0 {
		t.Fatal("descendant control did not run")
	}
	time.Sleep(150 * time.Millisecond)
	if calls.Load() != count {
		t.Fatal("detached descendant survived invocation")
	}
}

func TestSandboxRejectsOrdinaryCgroupDirectory(t *testing.T) {
	if _, release, err := newAppCgroup(t.TempDir()); err == nil {
		release()
		t.Fatal("accepted ordinary filesystem as cgroup controller")
	}
}

func TestSandboxResourceLimits(t *testing.T) {
	if os.Getenv("HANK_TEST_APP_SANDBOX") != "1" {
		t.Skip("requires disposable resource-limited sandbox image")
	}
	group, release, err := newAppCgroup("")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"memory.max": "536870912", "memory.swap.max": "0", "memory.oom.group": "1", "pids.max": "64", "cpu.max": "100000 100000"} {
		got, err := os.ReadFile(filepath.Join(group.Name(), name))
		if err != nil || strings.TrimSpace(string(got)) != want {
			t.Fatalf("%s: %q %v", name, got, err)
		}
	}
	release()
	for _, probe := range []struct {
		name, body string
		success    bool
	}{
		{"processes", `children=[]
try:
 for i in range(90):
  pid=os.fork()
  if pid==0: time.sleep(20);os._exit(0)
  children.append(pid)
 raise AssertionError('process limit not enforced')
except BlockingIOError: pass
finally:
 for pid in children: os.kill(pid,9)
 for pid in children: os.waitpid(pid,0)
assert len(children)<64
`, true},
		{"memory", `data=bytearray(700*1024*1024)
`, false},
		{"control", `assert 2+2==4
`, true},
	} {
		t.Run(probe.name, func(t *testing.T) {
			dir := t.TempDir()
			exe := filepath.Join(dir, "probe.py")
			body := "#!/usr/bin/python3\nimport os,json,time\nrequest=json.loads(input())\n" + probe.body + "print(json.dumps({'request_id':request['request_id'],'ok':True}))\n"
			if err := os.WriteFile(exe, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			response, err := (Runner{}).Invoke(context.Background(), InvokeSpec{Executable: exe, WorkDir: dir, Timeout: 5 * time.Second, Request: AppStdioRequest{RequestID: probe.name}})
			if probe.success && (err != nil || !response.OK) {
				t.Fatalf("legitimate control failed: %+v %v", response, err)
			}
			if !probe.success && err == nil {
				t.Fatal("memory exhaustion was not stopped")
			}
			entries, err := os.ReadDir(defaultAppCgroupRoot)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.IsDir() {
					t.Fatalf("invocation cgroup leaked: %s", entry.Name())
				}
			}
		})
	}
}

func TestSandboxExampleBrokerSDK(t *testing.T) {
	if os.Getenv("HANK_TEST_APP_SANDBOX") != "1" {
		t.Skip("requires disposable sandbox image and example fixture")
	}
	root := "/example"
	var calls atomic.Int64
	response, err := (Runner{}).Invoke(context.Background(), InvokeSpec{
		WorkDir: root, Executable: filepath.Join(root, "bin", "example"), Timeout: 5 * time.Second,
		Request: AppStdioRequest{RequestID: "example", Config: json.RawMessage(`{"base_url":"https://example.invalid/api"}`)},
		Broker: func(ctx context.Context, req BrokerRequest) (any, error) {
			if req.Permission != "network:base_url" || req.Operation != "http.request" || req.URL != "https://example.invalid/api" {
				return nil, ErrPermissionRefused
			}
			calls.Add(1)
			return map[string]any{"status": 200, "data": "b2s="}, nil
		},
	})
	if err != nil || !response.OK || calls.Load() != 1 || !strings.Contains(string(response.Output), "HTTP 200") {
		t.Fatalf("example SDK: %+v %v calls=%d", response, err, calls.Load())
	}
}
