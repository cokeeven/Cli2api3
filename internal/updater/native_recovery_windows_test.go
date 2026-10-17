//go:build windows

package updater

import (
	"context"
	"fmt"
	"github.com/caigee-cmd/cli2api/internal/store"
	control "github.com/caigee-cmd/cli2api/internal/update"
	"golang.org/x/sys/windows"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeWindowsPortConflictDoesNotStartRecovery(t *testing.T) {
	r := newNativeRig(t, "")
	ctx := context.Background()
	before, err := r.e.health(ctx)
	if err != nil {
		t.Fatal(err)
	}
	j := nativeJournal{ID: "port-conflict", Phase: "replacing", Request: r.request(), Before: before, Stage: r.e.stagePath("v9.0.2"), Old: map[string]bool{"qoder-api-proxy.exe": true, "components": true, "native-manifest.json": true}}
	if err = saveNativeJSON(r.e.journalPath(), j); err != nil {
		t.Fatal(err)
	}
	listener := httptest.NewServer(nil)
	defer listener.Close()
	s := NewService(Config{RecoverAfterListen: true, ListenAddress: listener.Listener.Addr().String(), AuthToken: strings.Repeat("a", 64), StatusFile: filepath.Join(r.cfg.StateDir, "status.json")}, inProcessNativeHost{r.e})
	if err = s.Serve(ctx); err == nil {
		t.Fatal("occupied port was taken")
	}
	after, err := r.e.health(ctx)
	current, _ := r.e.readJournal()
	if err != nil || after.PID != before.PID || current.Phase != "replacing" {
		t.Fatal("port conflict ran recovery before acquiring its own listener")
	}
}

func TestNativeWindowsGatewayDeathBeforeReplacementPreservesProgram(t *testing.T) {
	r := newNativeRig(t, "")
	ctx := context.Background()
	h, err := r.e.health(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(r.cfg.ProgramDir, "qoder-api-proxy.exe")
	sum, _, _ := fileDigest(p)
	if err = terminateNativeProcess(h.PID, p); err != nil {
		t.Fatal(err)
	}
	if _, err = r.e.Apply(ctx, "gateway-interrupted", r.request(), func(string) {}); err == nil {
		t.Fatal("dead gateway was treated as a validated current instance")
	}
	got, _, _ := fileDigest(p)
	if got != sum {
		t.Fatal("program changed before current instance could be verified")
	}
	if err = controlNativeManager(ctx, r.cfg, true); err != nil {
		t.Fatal(err)
	}
	if err = r.e.waitHealth(ctx, "v9.0.1", strings.Repeat("1", 40)); err != nil {
		t.Fatal(err)
	}
	r.assertData(t)
}

func TestNativeWindowsCorruptJournalDoesNotTouchProgram(t *testing.T) {
	r := newNativeRig(t, "")
	ctx := context.Background()
	before, err := r.e.health(ctx)
	if err != nil {
		t.Fatal(err)
	}
	j := nativeJournal{ID: "../../outside", Phase: "replacing", Request: r.request(), Before: before, Stage: r.e.stagePath("v9.0.2"), Old: map[string]bool{"qoder-api-proxy.exe": true, "components": true, "native-manifest.json": true}}
	if err = saveNativeJSON(r.e.journalPath(), j); err != nil {
		t.Fatal(err)
	}
	state, _, err := r.e.Recover()
	if state != "recovery_failed" || err == nil {
		t.Fatal("malformed recovery record was accepted")
	}
	after, err := r.e.health(ctx)
	if err != nil || after.PID != before.PID {
		t.Fatal("bad journal stopped the existing program")
	}
	r.assertData(t)
}

func TestNativeWindowsIncompatibleOldProgramRetainsLatestDatabase(t *testing.T) {
	r := newNativeRig(t, "schema-fail")
	rolled, err := r.e.Apply(context.Background(), "schema-failure", r.request(), func(string) {})
	if rolled || err == nil || !strings.Contains(err.Error(), "latest database retained") {
		t.Fatal("incompatible old program reported success", rolled, err)
	}
	if r.e.FailureState() != "recovery_failed" {
		t.Fatal("recovery failure not durable")
	}
	db, err := store.OpenStore(filepath.Join(r.cfg.DataDir, "qoder.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err = db.DB().QueryRow("SELECT COUNT(*) FROM native_test_records").Scan(&n); err != nil || n < 1 {
		t.Fatal("newest data disappeared", err)
	}
}

func TestNativeWindowsKilledUpdaterRecoversPersistedInstall(t *testing.T) {
	r := newNativeRig(t, "")
	r.e.Close()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	l.Close()
	configPath := filepath.Join(r.cfg.StateDir, "native.json")
	statusPath := filepath.Join(r.cfg.StateDir, "agent-status.json")
	tokenPath := filepath.Join(r.cfg.StateDir, "token.txt")
	if err = saveNativeJSON(configPath, r.cfg); err != nil {
		t.Fatal(err)
	}
	if err = saveNativeJSON(statusPath, control.AgentStatus{State: "ready_to_apply", JobID: "killed-host", ClientJobID: "update-2222222222222222", CurrentVersion: "v9.0.1", TargetVersion: "v9.0.2"}); err != nil {
		t.Fatal(err)
	}
	start := func() *exec.Cmd {
		cmd := exec.Command(r.cfg.Launcher, "--backend", NativeBackend, "--native-config", configPath, "--listen", address, "--auth-token-file", tokenPath, "--status-file", statusPath)
		hideNativeCommand(cmd)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		return cmd
	}
	host := start()
	agent := control.NewHTTPAgentClient("http://"+address, strings.Repeat("a", 64))
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s, err := agent.Status(context.Background()); err == nil && s.Available {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	path, _ := windows.UTF16PtrFromString(filepath.Join(r.cfg.ProgramDir, "components", "worker", "src", "daemon.mjs"))
	handle, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			windows.CloseHandle(handle)
		}
	}()
	if _, err = agent.ApplyPrepared(context.Background(), r.request()); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(r.cfg.ProgramDir, ".cli2api-update", "rollback", "killed-host", "qoder-api-proxy.exe")
	deadline = time.Now().Add(8 * time.Second)
	interrupted := false
	for time.Now().Before(deadline) {
		if _, err = os.Stat(backup); err == nil {
			interrupted = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !interrupted {
		t.Fatal("host never reached real replacement window")
	}
	if err = host.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = host.Wait()
	windows.CloseHandle(handle)
	closed = true
	replacement := start()
	defer func() { _ = replacement.Process.Kill(); _ = replacement.Wait() }()
	deadline = time.Now().Add(20 * time.Second)
	var status control.AgentStatus
	for time.Now().Before(deadline) {
		status, err = agent.Status(context.Background())
		if err == nil && (status.State == "rolled_back" || status.State == "recovery_failed") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if status.State != "rolled_back" || status.JobID != "killed-host" || status.ClientJobID != "update-2222222222222222" {
		t.Fatal("restarted real updater lost job/recovery", fmt.Sprintf("%+v", status))
	}
	if err = r.e.waitHealth(context.Background(), "v9.0.1", strings.Repeat("1", 40)); err != nil {
		t.Fatal(err)
	}
	r.assertData(t)
}
