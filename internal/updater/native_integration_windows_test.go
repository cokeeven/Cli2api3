//go:build windows

package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/store"
	control "github.com/caigee-cmd/cli2api/internal/update"
	"golang.org/x/sys/windows"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var fixtureBuild sync.Once
var fixtureDir, fixtureError string

func buildNativeFixture(t *testing.T) (string, string) {
	t.Helper()
	if os.Getenv("CLI2API_NATIVE_REHEARSAL") != "1" {
		t.Skip("set CLI2API_NATIVE_REHEARSAL=1 to run real isolated Windows process rehearsals")
	}
	fixtureBuild.Do(func() {
		dir, err := os.MkdirTemp("", "cli2api-native-fixtures-")
		if err != nil {
			fixtureError = err.Error()
			return
		}
		fixtureDir = dir
		host := exec.Command("go", "build", "-o", filepath.Join(dir, "host.exe"), "./cmd/updater")
		host.Dir = filepath.Join("..", "..")
		if out, e := host.CombinedOutput(); e != nil {
			fixtureError = fmt.Sprintf("%v: %s", e, out)
			return
		}
		for _, v := range []string{"1", "2"} {
			commit := strings.Repeat(v, 40)
			cmd := exec.Command("go", "build", "-ldflags", "-X main.version=v9.0."+v+" -X main.commit="+commit, "-o", filepath.Join(dir, "v"+v+".exe"), "./internal/updater/testdata/nativefixture")
			cmd.Dir = filepath.Join("..", "..")
			out, err := cmd.CombinedOutput()
			if err != nil {
				fixtureError = fmt.Sprintf("%v: %s", err, out)
				return
			}
		}
	})
	if fixtureError != "" {
		t.Fatal(fixtureError)
	}
	return filepath.Join(fixtureDir, "v1.exe"), filepath.Join(fixtureDir, "v2.exe")
}
func copyNativeTest(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(dst, b, 0700); err != nil {
		t.Fatal(err)
	}
}
func seedNativeTree(t *testing.T, root, exe, version, commit string) {
	t.Helper()
	required := []string{"qoder-api-proxy.exe", "components/node/node.exe", "components/updater.exe", "components/worker/src/daemon.mjs", "components/worker/src/compat.mjs", "components/worker/node_modules/undici/package.json", "components/cli/node_modules/@qoder-ai/qodercli/bundle/qodercli.js", "components/cli/node_modules/@qodercn-ai/qoderclicn/bundle/qoderclicn.js"}
	files := []NativeFile{}
	for _, p := range required {
		target := filepath.Join(root, filepath.FromSlash(p))
		if strings.HasSuffix(p, ".exe") {
			copyNativeTest(t, exe, target)
		} else {
			_ = os.MkdirAll(filepath.Dir(target), 0700)
			_ = os.WriteFile(target, []byte("fixture-only"), 0600)
		}
		sum, n, err := fileDigest(target)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, NativeFile{Path: p, SHA256: sum, Size: n})
	}
	m := NativeManifest{Format: 1, Backend: NativeBackend, OS: "windows", Arch: "amd64", Version: version, Commit: commit, AgentProtocol: 2, Components: map[string]string{"qodercli": "1.1.32", "qoderclicn": "1.1.32", "node": "fixture"}, Files: files}
	if err := saveNativeJSON(filepath.Join(root, "native-manifest.json"), m); err != nil {
		t.Fatal(err)
	}
}

type nativeRig struct {
	e         *NativeExecutor
	cfg       NativeConfig
	accountID string
	backup    string
	root      string
}

func newNativeRig(t *testing.T, mode string) *nativeRig {
	t.Helper()
	old, next := buildNativeFixture(t)
	root := t.TempDir()
	program := filepath.Join(root, "program")
	data := filepath.Join(root, "data")
	state := filepath.Join(root, "updater")
	_ = os.MkdirAll(data, 0700)
	_ = os.MkdirAll(state, 0700)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	token := strings.Repeat("a", 64)
	tokenFile := filepath.Join(state, "token.txt")
	_ = os.WriteFile(tokenFile, []byte(token), 0600)
	cfg := NativeConfig{ProgramDir: program, DataDir: data, StateDir: state, HealthURL: fmt.Sprintf("http://127.0.0.1:%d/health", port), InstanceID: strings.Repeat("b", 32), Manager: "process", Launcher: filepath.Join(state, "native-gateway.ps1"), LaunchConfig: filepath.Join(state, "gateway.json"), HealthTimeoutSeconds: 5, StopTimeoutSeconds: 3}
	cfg.Launcher = filepath.Join(fixtureDir, "host.exe")
	if os.Getenv("CLI2API_NATIVE_TASK_REHEARSAL") == "1" {
		cfg.Manager = "task"
		cfg.TargetName = fmt.Sprintf("Cli2api3-Isolated-%d-%d", port, time.Now().UnixNano())
	}
	launch := map[string]any{"program_dir": program, "data_dir": data, "working_directory": root, "arguments": []string{}, "env_file": "", "runtime_dir": filepath.Join(root, "runtime"), "environment": map[string]string{"HOST": "127.0.0.1", "PORT": fmt.Sprint(port), "NATIVE_FIXTURE_MODE": mode}, "agent_url": "http://127.0.0.1:1", "token_file": tokenFile, "instance_id": cfg.InstanceID, "network_file": "", "log_file": filepath.Join(root, "gateway")}
	if err = saveNativeJSON(cfg.LaunchConfig, launch); err != nil {
		t.Fatal(err)
	}
	seedNativeTree(t, program, old, "v9.0.1", strings.Repeat("1", 40))
	seedNativeTree(t, filepath.Join(program, ".cli2api-update", "stage", "v9.0.2"), next, "v9.0.2", strings.Repeat("2", 40))
	db, err := store.OpenStore(filepath.Join(data, "qoder.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	a, err := db.Create(ctx, accounts.CreateAccount{Name: "Fictional", Provider: "trae", Region: "cn", Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SaveCredentialPayload(ctx, a.ID, "trae-oauth-v1", []byte(`{"fake":"fictional-only"}`)); err != nil {
		t.Fatal(err)
	}
	if err = db.SetSecret(ctx, "proxy_api_key", "fictional-console-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.InsertAPIKey(ctx, accounts.StoredAPIKey{ID: "fictional-key", Name: "fictional", Prefix: "fake", KeyHash: "fictional-hash", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err = db.SetModelContext(ctx, "fictional-model", 12345); err != nil {
		t.Fatal(err)
	}
	if err = db.InsertRequestLog(ctx, accounts.RequestLog{ID: "before-log", AccountID: a.ID, Provider: "trae", Status: "ok", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	e, err := NewNativeExecutor(cfg, token, "")
	if err != nil {
		t.Fatal(err)
	}
	rig := &nativeRig{e: e, cfg: cfg, accountID: a.ID, root: root}
	t.Cleanup(func() {
		pid := findNativeProcess(filepath.Join(program, "qoder-api-proxy.exe"))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = rig.e.stop(ctx, pid)
		rig.e.Close()
	})
	if cfg.Manager == "task" {
		script := "$a=New-ScheduledTaskAction -Execute " + psQuote(cfg.Launcher) + " -Argument " + psQuote("--run-native-gateway \""+cfg.LaunchConfig+"\"") + "; Register-ScheduledTask -TaskName " + psQuote(cfg.TargetName) + " -Action $a -User 'SYSTEM' -RunLevel Highest -Force | Out-Null"
		if err = runNativePowerShell(ctx, script); err != nil {
			t.Fatal("isolated scheduled task registration failed; requires an elevated Windows test runner", err)
		}
		t.Cleanup(func() {
			_ = runNativePowerShell(context.Background(), "Stop-ScheduledTask -TaskName "+psQuote(cfg.TargetName)+" -ErrorAction SilentlyContinue; Unregister-ScheduledTask -TaskName "+psQuote(cfg.TargetName)+" -Confirm:$false")
		})
	}
	if err = controlNativeManager(ctx, cfg, true); err != nil {
		t.Fatal(err)
	}
	if err = e.waitHealth(ctx, "v9.0.1", strings.Repeat("1", 40)); err != nil {
		t.Fatal(err)
	}
	rig.backup, err = SnapshotNativeDatabase(ctx, filepath.Join(data, "qoder.db"), filepath.Join(data, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	return rig
}
func (r *nativeRig) request() ApplyRequest {
	return ApplyRequest{CurrentVersion: "v9.0.1", TargetVersion: "v9.0.2", SkipPull: true, BackupPath: r.backup}
}
func (r *nativeRig) assertData(t *testing.T) {
	t.Helper()
	db, err := store.OpenStore(filepath.Join(r.cfg.DataDir, "qoder.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	a, err := db.Get(ctx, r.accountID)
	if err != nil || a.Name != "Fictional" {
		t.Fatal("account changed", err)
	}
	_, payload, err := db.LoadCredentialPayload(ctx, r.accountID)
	if err != nil || string(payload) != `{"fake":"fictional-only"}` {
		t.Fatal("credential changed", err)
	}
	secret, ok, err := db.GetSecret(ctx, "proxy_api_key")
	if err != nil || !ok || secret != "fictional-console-secret" {
		t.Fatal("administrator key changed")
	}
	keys, err := db.ListAPIKeys(ctx)
	if err != nil || len(keys) != 1 {
		t.Fatal("client key changed")
	}
	n, ok, err := db.GetModelContext(ctx, "fictional-model")
	if err != nil || !ok || n != 12345 {
		t.Fatal("configuration changed")
	}
	var count int
	if err = db.DB().QueryRow("SELECT COUNT(*) FROM request_logs WHERE id='before-log'").Scan(&count); err != nil || count != 1 {
		t.Fatal("request history changed", err)
	}
}

func TestNativeWindowsRealProcessUpgrade(t *testing.T) {
	r := newNativeRig(t, "")
	before, _ := r.e.health(context.Background())
	rolled, err := r.e.Apply(context.Background(), "normal", r.request(), func(string) {})
	if err != nil || rolled {
		t.Fatal(rolled, err)
	}
	after, err := r.e.health(context.Background())
	if err != nil || after.Version != "v9.0.2" || after.PID == before.PID {
		t.Fatal("real process was not upgraded", after, err)
	}
	r.assertData(t)
	if _, err := os.Stat(filepath.Join(r.cfg.ProgramDir, ".cli2api-update", "rollback", "normal", "qoder-api-proxy.exe")); err != nil {
		t.Fatal("old executable not preserved")
	}
}
func TestNativeWindowsFailureRollbackKeepsNewWrites(t *testing.T) {
	for _, mode := range []string{"exit", "write-fail", "crash-after-write"} {
		t.Run(mode, func(t *testing.T) {
			r := newNativeRig(t, mode)
			rolled, err := r.e.Apply(context.Background(), "failure", r.request(), func(string) {})
			if !rolled || err == nil {
				t.Fatal("failure did not roll back", rolled, err)
			}
			if err = r.e.waitHealth(context.Background(), "v9.0.1", strings.Repeat("1", 40)); err != nil {
				t.Fatal(err)
			}
			r.assertData(t)
			if mode == "write-fail" || mode == "crash-after-write" {
				db, e := store.OpenStore(filepath.Join(r.cfg.DataDir, "qoder.db"))
				if e != nil {
					t.Fatal(e)
				}
				defer db.Close()
				var n int
				if e = db.DB().QueryRow("SELECT COUNT(*) FROM native_test_records WHERE value='after-upgrade'").Scan(&n); e != nil || n < 1 {
					t.Fatal("post-upgrade write lost", n, e)
				}
			}
		})
	}
}
func TestNativeWindowsBackupAndFileLockFailures(t *testing.T) {
	t.Run("backup", func(t *testing.T) {
		r := newNativeRig(t, "")
		raw, _ := os.ReadFile(r.backup)
		raw[100] ^= 0xff
		_ = os.WriteFile(r.backup, raw, 0600)
		if _, err := r.e.Apply(context.Background(), "bad-backup", r.request(), func(string) {}); err == nil {
			t.Fatal("damaged snapshot accepted")
		}
		if h, err := r.e.health(context.Background()); err != nil || h.Version != "v9.0.1" {
			t.Fatal("backup failure stopped old gateway")
		}
	})
	t.Run("file-lock", func(t *testing.T) {
		r := newNativeRig(t, "")
		name, _ := windows.UTF16PtrFromString(filepath.Join(r.cfg.ProgramDir, "components", "worker", "src", "daemon.mjs"))
		handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer windows.CloseHandle(handle)
		rolled, err := r.e.Apply(context.Background(), "locked", r.request(), func(string) {})
		if !rolled || err == nil {
			t.Fatal("locked file did not trigger rollback", rolled, err)
		}
		r.assertData(t)
	})
}
func TestNativeWindowsInterruptedInstallRecovery(t *testing.T) {
	r := newNativeRig(t, "")
	before, err := r.e.health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = r.e.stop(context.Background(), before.PID); err != nil {
		t.Fatal(err)
	}
	id := "interrupted"
	j := nativeJournal{ID: id, Phase: "replacing", Request: r.request(), Before: before, Stage: r.e.stagePath("v9.0.2"), Old: map[string]bool{"qoder-api-proxy.exe": true, "components": true, "native-manifest.json": true}}
	if err = saveNativeJSON(r.e.journalPath(), j); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(r.cfg.ProgramDir, ".cli2api-update", "rollback", id, "qoder-api-proxy.exe")
	_ = os.MkdirAll(filepath.Dir(backup), 0700)
	if err = os.Rename(filepath.Join(r.cfg.ProgramDir, "qoder-api-proxy.exe"), backup); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(filepath.Join(j.Stage, "qoder-api-proxy.exe"), filepath.Join(r.cfg.ProgramDir, "qoder-api-proxy.exe")); err != nil {
		t.Fatal(err)
	}
	r.e.Close()
	replacement, err := NewNativeExecutor(r.cfg, strings.Repeat("a", 64), "")
	if err != nil {
		t.Fatal(err)
	}
	r.e = replacement
	state, _, err := r.e.Recover()
	if state != "rolled_back" || err == nil {
		t.Fatal("interrupted replacement not recovered", state, err)
	}
	r.assertData(t)
}

func TestNativeWindowsRejectsSecondUpdaterAndStaleManifest(t *testing.T) {
	r := newNativeRig(t, "")
	if other, err := NewNativeExecutor(r.cfg, strings.Repeat("a", 64), ""); err == nil {
		other.Close()
		t.Fatal("two updater processes can own the same installation")
	}
	otherConfig := r.cfg
	otherConfig.StateDir = filepath.Join(r.root, "other-updater")
	if other, err := NewNativeExecutor(otherConfig, strings.Repeat("a", 64), ""); err == nil {
		other.Close()
		t.Fatal("another state directory bypassed program ownership lock")
	}
	manifestPath := filepath.Join(r.e.stagePath("v9.0.2"), "native-manifest.json")
	raw, _ := os.ReadFile(manifestPath)
	var m NativeManifest
	_ = json.Unmarshal(raw, &m)
	m.Arch = "arm64"
	_ = saveNativeJSON(manifestPath, m)
	if _, err := r.e.Apply(context.Background(), "wrong-arch", r.request(), func(string) {}); err == nil {
		t.Fatal("wrong architecture accepted")
	}
}

func TestNativeWindowsDownloadAndChecksumFailures(t *testing.T) {
	r := newNativeRig(t, "")
	for _, mode := range []string{"network", "checksum"} {
		t.Run(mode, func(t *testing.T) {
			r.e.transport.fetch = func(context.Context, string, string) ([]byte, error) {
				if mode == "network" {
					return nil, fmt.Errorf("isolated download failure")
				}
				return []byte("bad-checksum"), nil
			}
			err := r.e.Prepare(context.Background(), mode, control.PrepareRequest{CurrentVersion: "v9.0.1", TargetVersion: "v9.0.2"}, func(string) {})
			if err == nil {
				t.Fatal("failed download prepared")
			}
			if h, e := r.e.health(context.Background()); e != nil || h.Version != "v9.0.1" {
				t.Fatal("download preparation stopped existing process")
			}
		})
	}
}
