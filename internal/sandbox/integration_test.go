//go:build !nosandbox

package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"

	arapuca "github.com/LeGambiArt/go-arapuca"

	"github.com/LeGambiArt/wtmcp/internal/config"
	"github.com/LeGambiArt/wtmcp/internal/secrets/securefile"
)

var netNSOnce sync.Once
var netNSResult bool

func netNSAvailable() bool {
	netNSOnce.Do(func() {
		netNSResult = arapuca.NetNSAvailable()
	})
	return netNSResult
}

type probeRequest struct {
	Cmd  string `json:"cmd"`
	Path string `json:"path,omitempty"`
	Data string `json:"data,omitempty"`
	Addr string `json:"addr,omitempty"`
	Msg  string `json:"msg,omitempty"`
	Key  string `json:"key,omitempty"`
}

type probeResponse struct {
	OK    bool   `json:"ok"`
	Data  string `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

func buildProbe(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "probe")
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/probe.go") //nolint:gosec // test helper
	cmd.Dir = filepath.Join(projectRoot(), "internal", "sandbox")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build probe: %v\n%s", err, out)
	}
	return bin
}

func projectRoot() string {
	_, f, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(f), "..", "..")
}

func runProbe(t *testing.T, mgr *Manager, info PluginInfo, env map[string]string, req probeRequest) probeResponse {
	t.Helper()
	ctx := context.Background()

	proc, err := mgr.Launch(ctx, info, env)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}

	data, _ := json.Marshal(req)
	data = append(data, '\n')
	if _, err := proc.Stdin().Write(data); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	proc.Stdin().Close() //nolint:errcheck,gosec // test cleanup

	var resp probeResponse
	if err := json.NewDecoder(proc.Stdout()).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	proc.Wait() //nolint:errcheck,gosec // test cleanup
	proc.Cleanup()
	return resp
}

func skipIfIntegrationDisabled(t *testing.T) {
	t.Helper()
	if os.Getenv("WTMCP_SKIP_INTEGRATION") != "" {
		t.Skip("skipped via WTMCP_SKIP_INTEGRATION")
	}
}

func integrationTestManager(t *testing.T) *Manager {
	t.Helper()
	cfg := config.SandboxConfig{
		Defaults: config.SandboxResourceLimits{
			MaxMemoryMB:   256,
			MaxCPUPct:     100,
			MaxPIDs:       32,
			MaxFileSizeMB: 10,
		},
	}
	if runtime.GOOS == "linux" && !netNSAvailable() {
		t.Skip("network namespace isolation unavailable (unshare --user --net not permitted)")
	}
	dataDir := filepath.Join(t.TempDir(), "data")
	mgr, err := NewManager(cfg, dataDir)
	if err != nil {
		t.Logf("NOTICE: sandbox integration tests skipping — sandbox init failed: %v", err)
		t.Skipf("sandbox unavailable: %v", err)
	}
	t.Cleanup(mgr.Close)
	return mgr
}

func TestIntegration_Echo(t *testing.T) {
	skipIfIntegrationDisabled(t)

	bin := buildProbe(t)
	mgr := integrationTestManager(t)

	info := PluginInfo{
		Name:    "echo-test",
		Dir:     filepath.Dir(bin),
		Handler: filepath.Base(bin),
	}

	resp := runProbe(t, mgr, info, nil, probeRequest{Cmd: "echo", Msg: "hello sandbox"})
	if !resp.OK {
		t.Fatalf("echo failed: %s", resp.Error)
	}
	if resp.Data != "hello sandbox" {
		t.Errorf("echo data = %q, want %q", resp.Data, "hello sandbox")
	}
}

func TestIntegration_ReadOwnDir(t *testing.T) {
	skipIfIntegrationDisabled(t)
	if runtime.GOOS == "darwin" {
		t.Skip("Seatbelt profile does not allow temp dir paths used by Go tests")
	}

	bin := buildProbe(t)
	mgr := integrationTestManager(t)

	markerPath := filepath.Join(filepath.Dir(bin), "marker.txt")
	if err := os.WriteFile(markerPath, []byte("readable"), 0o600); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}

	info := PluginInfo{
		Name:    "read-own-dir",
		Dir:     filepath.Dir(bin),
		Handler: filepath.Base(bin),
	}

	resp := runProbe(t, mgr, info, nil, probeRequest{Cmd: "read_file", Path: markerPath})
	if !resp.OK {
		t.Fatalf("read own dir failed: %s", resp.Error)
	}
	if resp.Data != "readable" {
		t.Errorf("data = %q, want %q", resp.Data, "readable")
	}
}

func TestIntegration_CannotReadShadow(t *testing.T) {
	skipIfIntegrationDisabled(t)
	if runtime.GOOS != "linux" {
		t.Skip("Landlock test, Linux only")
	}

	bin := buildProbe(t)
	mgr := integrationTestManager(t)

	info := PluginInfo{
		Name:    "no-shadow",
		Dir:     filepath.Dir(bin),
		Handler: filepath.Base(bin),
	}

	resp := runProbe(t, mgr, info, nil, probeRequest{Cmd: "read_file", Path: "/etc/shadow"})
	if resp.OK {
		t.Error("sandboxed process should NOT be able to read /etc/shadow")
	}
}

func TestIntegration_CannotDialNetwork(t *testing.T) {
	skipIfIntegrationDisabled(t)

	bin := buildProbe(t)
	mgr := integrationTestManager(t)

	info := PluginInfo{
		Name:    "no-network",
		Dir:     filepath.Dir(bin),
		Handler: filepath.Base(bin),
	}

	resp := runProbe(t, mgr, info, nil, probeRequest{Cmd: "dial_tcp", Addr: "1.1.1.1:443"})
	if resp.OK {
		t.Error("sandboxed process should NOT be able to make TCP connections")
	}
}

func TestIntegration_WriteToTmpdir(t *testing.T) {
	skipIfIntegrationDisabled(t)
	if runtime.GOOS == "darwin" {
		t.Skip("Seatbelt profile does not allow temp dir paths used by Go tests")
	}

	bin := buildProbe(t)
	mgr := integrationTestManager(t)

	info := PluginInfo{
		Name:    "write-tmp",
		Dir:     filepath.Dir(bin),
		Handler: filepath.Base(bin),
	}

	tmpDir := mgr.TmpDir("write-tmp")
	testFile := filepath.Join(tmpDir, "test.txt")

	resp := runProbe(t, mgr, info, nil, probeRequest{Cmd: "write_file", Path: testFile, Data: "written"})
	if !resp.OK {
		t.Fatalf("write to tmpdir failed: %s", resp.Error)
	}

	data, err := os.ReadFile(testFile) //nolint:gosec // test verification
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "written" {
		t.Errorf("data = %q, want %q", string(data), "written")
	}

	mgr.CleanupTmpDir("write-tmp")
	if _, err := os.Stat(tmpDir); !os.IsNotExist(err) {
		t.Error("tmpdir should be removed after cleanup")
	}
}

func TestIntegration_CannotWriteOutsideTmpdir(t *testing.T) {
	skipIfIntegrationDisabled(t)
	if runtime.GOOS != "linux" {
		t.Skip("Landlock test, Linux only")
	}

	bin := buildProbe(t)
	mgr := integrationTestManager(t)

	info := PluginInfo{
		Name:    "no-write-escape",
		Dir:     filepath.Dir(bin),
		Handler: filepath.Base(bin),
	}

	resp := runProbe(t, mgr, info, nil, probeRequest{Cmd: "write_file", Path: "/tmp/sandbox-escape.txt", Data: "escaped"})
	if resp.OK {
		os.Remove("/tmp/sandbox-escape.txt") //nolint:errcheck,gosec // cleanup
		t.Error("sandboxed process should NOT be able to write outside its tmpdir")
	}
}

func TestIntegration_CannotReadHomeDir(t *testing.T) {
	skipIfIntegrationDisabled(t)
	if runtime.GOOS != "linux" {
		t.Skip("Landlock test, Linux only")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home directory")
	}

	bin := buildProbe(t)
	mgr := integrationTestManager(t)

	info := PluginInfo{
		Name:    "no-read-home",
		Dir:     filepath.Dir(bin),
		Handler: filepath.Base(bin),
	}

	resp := runProbe(t, mgr, info, nil, probeRequest{Cmd: "read_file", Path: filepath.Join(home, ".bashrc")})
	if resp.OK {
		t.Error("sandboxed process should NOT be able to read files in user's home directory")
	}
}

func TestIntegration_CannotReadOtherPluginCredentials(t *testing.T) {
	skipIfIntegrationDisabled(t)
	if runtime.GOOS != "linux" {
		t.Skip("Landlock test, Linux only")
	}

	bin := buildProbe(t)

	credDir := t.TempDir()
	groupA := filepath.Join(credDir, "plugin-a")
	groupB := filepath.Join(credDir, "plugin-b")
	if err := os.MkdirAll(groupA, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(groupB, 0o700); err != nil {
		t.Fatal(err)
	}
	secretFile := filepath.Join(groupB, "token.env")
	if err := os.WriteFile(secretFile, []byte("SECRET=hunter2"), 0o600); err != nil {
		t.Fatal(err)
	}
	ownSecret := filepath.Join(groupA, "token.env")
	if err := os.WriteFile(ownSecret, []byte("SECRET=hunter2"), 0o600); err != nil {
		t.Fatal(err)
	}

	if runtime.GOOS == "linux" && !netNSAvailable() {
		t.Skip("network namespace isolation unavailable (unshare --user --net not permitted)")
	}
	cfg := config.SandboxConfig{
		Defaults: config.SandboxResourceLimits{
			MaxMemoryMB: 256, MaxCPUPct: 100, MaxPIDs: 32, MaxFileSizeMB: 10,
		},
	}
	mgr, err := NewManager(cfg, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Skipf("sandbox unavailable: %v", err)
	}
	t.Cleanup(mgr.Close)

	info := PluginInfo{
		Name:            "plugin-a",
		Dir:             filepath.Dir(bin),
		Handler:         filepath.Base(bin),
		CredentialGroup: "plugin-a",
	}

	resp := runProbe(t, mgr, info, nil, probeRequest{Cmd: "read_file", Path: secretFile})
	if resp.OK {
		t.Error("plugin-a should NOT be able to read plugin-b's credentials")
	}

	// The credentials dir is not part of the sandbox read profile at
	// all: plugins cannot read their own group's credential files
	// either. Auth and TLS are handled by the core proxy.
	resp = runProbe(t, mgr, info, nil, probeRequest{Cmd: "read_file", Path: ownSecret})
	if resp.OK {
		t.Error("plugin-a should NOT be able to read its own credential group's files")
	}
}

func TestIntegration_LaunchEnv(t *testing.T) {
	skipIfIntegrationDisabled(t)

	bin := buildProbe(t)
	mgr := integrationTestManager(t)

	info := PluginInfo{
		Name:    "env-test",
		Dir:     filepath.Dir(bin),
		Handler: filepath.Base(bin),
	}

	env := map[string]string{"MY_TEST_VAR": "sandbox-value"}
	resp := runProbe(t, mgr, info, env, probeRequest{Cmd: "env", Key: "MY_TEST_VAR"})
	if !resp.OK {
		t.Fatalf("env failed: %s", resp.Error)
	}
	if resp.Data != "sandbox-value" {
		t.Errorf("env data = %q, want %q", resp.Data, "sandbox-value")
	}
}

// TestIntegration_SessionDirAccess verifies that a sandboxed process can
// access files in the session directory. This guards against regressions
// where path confinement functions fail inside the sandbox (e.g., using
// filepath.EvalSymlinks which walks ancestors denied by the sandbox).
//
// Skipped on macOS: Go's t.TempDir() lives under /var/folders/... whose
// ancestor directories are not in the Seatbelt profile. When a macOS CI
// runner is available, consider a variant that uses a stable path with
// known ancestors (e.g., under /tmp which is /private/tmp on macOS).
func TestIntegration_SessionDirAccess(t *testing.T) {
	skipIfIntegrationDisabled(t)
	if runtime.GOOS == "darwin" {
		t.Skip("Seatbelt profile does not allow temp dir ancestor paths used by Go tests")
	}

	bin := buildProbe(t)
	mgr := integrationTestManager(t)

	// Use a separate directory for SessionDir so we can verify the
	// sandbox profile grants access via the SessionDir rule, not
	// just the Dir rule (which always includes the plugin directory).
	sessionDir := t.TempDir()

	markerPath := filepath.Join(sessionDir, "session-marker.txt")
	if err := os.WriteFile(markerPath, []byte("session-accessible"), 0o600); err != nil {
		t.Fatal(err)
	}

	info := PluginInfo{
		Name:       "session-access",
		Dir:        filepath.Dir(bin),
		Handler:    filepath.Base(bin),
		SessionDir: sessionDir,
	}

	t.Run("stat_session_dir", func(t *testing.T) {
		resp := runProbe(t, mgr, info, nil, probeRequest{Cmd: "stat", Path: sessionDir})
		if !resp.OK {
			t.Fatalf("os.Stat(sessionDir) failed inside sandbox: %s", resp.Error)
		}
	})

	t.Run("eval_symlinks_session_dir", func(t *testing.T) {
		// EvalSymlinks succeeds here because sessionDir is in the
		// sandbox ReadPaths and Landlock grants implicit traversal
		// of ancestor directories for path_beneath rules.
		resp := runProbe(t, mgr, info, nil, probeRequest{Cmd: "eval_symlinks", Path: sessionDir})
		if !resp.OK {
			t.Fatalf("filepath.EvalSymlinks(sessionDir) failed inside sandbox: %s", resp.Error)
		}
	})

	t.Run("read_file_in_session_dir", func(t *testing.T) {
		resp := runProbe(t, mgr, info, nil, probeRequest{Cmd: "read_file", Path: markerPath})
		if !resp.OK {
			t.Fatalf("read file in session dir failed: %s", resp.Error)
		}
		if resp.Data != "session-accessible" {
			t.Errorf("data = %q, want %q", resp.Data, "session-accessible")
		}
	})

	t.Run("cannot_write_to_session_dir", func(t *testing.T) {
		resp := runProbe(t, mgr, info, nil, probeRequest{
			Cmd:  "write_file",
			Path: filepath.Join(sessionDir, "hacked.txt"),
			Data: "pwned",
		})
		if resp.OK {
			t.Error("sandboxed process should NOT be able to write to session dir")
		}
	})
}

func TestIntegration_TmpdirOverride(t *testing.T) {
	skipIfIntegrationDisabled(t)

	bin := buildProbe(t)
	mgr := integrationTestManager(t)

	info := PluginInfo{
		Name:    "tmpdir-test",
		Dir:     filepath.Dir(bin),
		Handler: filepath.Base(bin),
	}

	resp := runProbe(t, mgr, info, nil, probeRequest{Cmd: "tmpdir"})
	if !resp.OK {
		t.Fatalf("tmpdir failed: %s", resp.Error)
	}

	expected := mgr.TmpDir("tmpdir-test")
	if resp.Data != expected {
		t.Errorf("TMPDIR = %q, want %q", resp.Data, expected)
	}
}

// fdNumberOf extracts the fd number from a securefile's
// /proc/self/fd/N path.
func fdNumberOf(t *testing.T, path string) int {
	t.Helper()
	var fd int
	if _, err := fmt.Sscanf(path, "/proc/self/fd/%d", &fd); err != nil {
		t.Fatalf("parse fd path %q: %v", path, err)
	}
	return fd
}

// TestIntegration_ChildCannotInheritCloexecMemfd is the end-to-end
// check of the memfd leak fix: a close-on-exec securefile created in
// the core (here: the test process) must not be readable from inside
// a sandboxed child through /proc/self/fd. Before the fix the fd was
// inheritable and every handler's fd table carried every group's
// decrypted credentials.
func TestIntegration_ChildCannotInheritCloexecMemfd(t *testing.T) {
	skipIfIntegrationDisabled(t)
	if runtime.GOOS != "linux" {
		t.Skip("/proc/self/fd fd-table test, Linux only")
	}

	sf, err := securefile.CreateCloexec("leak-check")
	if err != nil {
		t.Fatalf("CreateCloexec: %v", err)
	}
	defer sf.Close() //nolint:errcheck // test cleanup
	if err := sf.Write([]byte("SECRET=hunter2")); err != nil {
		t.Fatalf("write: %v", err)
	}
	fd := fdNumberOf(t, sf.Path())

	bin := buildProbe(t)
	mgr := integrationTestManager(t)

	info := PluginInfo{
		Name:    "fd-leak-check",
		Dir:     filepath.Dir(bin),
		Handler: filepath.Base(bin),
	}

	resp := runProbe(t, mgr, info, nil, probeRequest{Cmd: "read_fd", Key: strconv.Itoa(fd)})
	if resp.OK {
		t.Errorf("sandboxed child must not read the close-on-exec memfd (fd %d): got %q", fd, resp.Data)
	}
}

// TestExecFdInheritanceControl is the sanity oracle for the probe's
// read_fd command: under plain exec (no sandbox), an inheritable fd
// IS readable in the child and a close-on-exec fd is NOT. This proves
// the probe detects inherited fds, so the sandboxed test above cannot
// pass merely because the probe is broken.
func TestExecFdInheritanceControl(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc/self/fd fd-table test, Linux only")
	}

	secret, err := securefile.CreateCloexec("control-secret")
	if err != nil {
		t.Fatalf("CreateCloexec: %v", err)
	}
	defer secret.Close() //nolint:errcheck // test cleanup
	if err := secret.Write([]byte("SECRET=control")); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	canary, err := securefile.Create("control-canary")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer canary.Close() //nolint:errcheck // test cleanup
	if err := canary.Write([]byte("CANARY=live")); err != nil {
		t.Fatalf("write canary: %v", err)
	}

	bin := buildProbe(t)
	run := func(fd int) probeResponse {
		cmd := exec.Command(bin)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatalf("stdin pipe: %v", err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatalf("stdout pipe: %v", err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatalf("start: %v", err)
		}
		req, _ := json.Marshal(probeRequest{Cmd: "read_fd", Key: strconv.Itoa(fd)})
		_, _ = stdin.Write(append(req, '\n'))
		_ = stdin.Close()
		var resp probeResponse
		if err := json.NewDecoder(stdout).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		_ = cmd.Wait()
		return resp
	}

	resp := run(fdNumberOf(t, canary.Path()))
	if !resp.OK || resp.Data != "CANARY=live" {
		t.Errorf("inheritable canary must be readable in the child: ok=%v data=%q err=%q", resp.OK, resp.Data, resp.Error)
	}
	resp = run(fdNumberOf(t, secret.Path()))
	if resp.OK {
		t.Errorf("close-on-exec fd must not be readable in the child: got %q", resp.Data)
	}
}
