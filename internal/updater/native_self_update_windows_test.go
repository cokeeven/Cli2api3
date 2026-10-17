//go:build windows

package updater

import (
	"bytes"
	"context"
	"fmt"
	control "github.com/caigee-cmd/cli2api/internal/update"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeWindowsRestartKeepsNewerPreparedJob(t *testing.T) {
	r := newNativeRig(t, "")
	before, _ := r.e.health(context.Background())
	old := nativeJournal{ID: "previous-install", Phase: "succeeded", Request: r.request(), Before: before, Stage: r.e.stagePath("v9.0.2"), Old: map[string]bool{"qoder-api-proxy.exe": true, "components": true, "native-manifest.json": true}}
	if err := saveNativeJSON(r.e.journalPath(), old); err != nil {
		t.Fatal(err)
	}
	status := control.AgentStatus{State: "ready_to_apply", JobID: "next-prepared", ClientJobID: "update-4444444444444444", CurrentVersion: "v9.0.1", TargetVersion: "v9.0.2"}
	p := filepath.Join(r.cfg.StateDir, "status.json")
	if err := saveNativeJSON(p, status); err != nil {
		t.Fatal(err)
	}
	service := NewService(Config{StatusFile: p}, inProcessNativeHost{r.e})
	if service.status.State != "ready_to_apply" || service.status.JobID != "next-prepared" {
		t.Fatal("previous completed journal replaced a newer prepared job", service.status)
	}
}

func TestNativeWindowsSupervisorUpdatesLockedHostBinary(t *testing.T) {
	archive := os.Getenv("CLI2API_NATIVE_APP_PACKAGE")
	if archive == "" {
		t.Skip("real application package is required")
	}
	r := newNativeRig(t, "")
	ctx := context.Background()
	h, _ := r.e.health(ctx)
	if err := r.e.stop(ctx, h.PID); err != nil {
		t.Fatal(err)
	}
	r.e.Close()
	seed := filepath.Join(r.root, "immutable-seed.exe")
	server := filepath.Join(r.root, "initial-server.exe")
	flags := "-X github.com/caigee-cmd/cli2api/internal/buildinfo.Version=v9.0.1 -X github.com/caigee-cmd/cli2api/internal/buildinfo.Commit=" + strings.Repeat("1", 40)
	for target, source := range map[string]string{seed: "./cmd/updater", server: "./cmd/server"} {
		cmd := exec.Command("go", "build", "-ldflags", flags, "-o", target, source)
		cmd.Dir = filepath.Join("..", "..")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	copyNativeTest(t, server, filepath.Join(r.cfg.ProgramDir, "qoder-api-proxy.exe"))
	copyNativeTest(t, seed, filepath.Join(r.cfg.ProgramDir, "components", "updater.exe"))
	var m NativeManifest
	if err := readNativeConfig(filepath.Join(r.cfg.ProgramDir, "native-manifest.json"), &m); err != nil {
		t.Fatal(err)
	}
	for i := range m.Files {
		if m.Files[i].Path == "qoder-api-proxy.exe" || m.Files[i].Path == "components/updater.exe" {
			sum, n, err := fileDigest(filepath.Join(r.cfg.ProgramDir, filepath.FromSlash(m.Files[i].Path)))
			if err != nil {
				t.Fatal(err)
			}
			m.Files[i].SHA256 = sum
			m.Files[i].Size = n
		}
	}
	if err := saveNativeJSON(filepath.Join(r.cfg.ProgramDir, "native-manifest.json"), m); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := l.Addr().String()
	l.Close()
	var launch NativeLaunchConfig
	if err = readNativeConfig(r.cfg.LaunchConfig, &launch); err != nil {
		t.Fatal(err)
	}
	launch.AgentURL = "http://" + listen
	if err = saveNativeJSON(r.cfg.LaunchConfig, launch); err != nil {
		t.Fatal(err)
	}
	r.cfg.Launcher = seed
	r.cfg.HealthTimeoutSeconds = 30
	r.e.config = r.cfg
	if r.cfg.Manager == "task" {
		script := "$a=New-ScheduledTaskAction -Execute " + psQuote(seed) + " -Argument " + psQuote("--run-native-gateway \""+r.cfg.LaunchConfig+"\"") + "; Set-ScheduledTask -TaskName " + psQuote(r.cfg.TargetName) + " -Action $a | Out-Null"
		if err = runNativePowerShell(ctx, script); err != nil {
			t.Fatal(err)
		}
	}
	stage := r.e.stagePath("v9.0.2")
	if err = os.Rename(stage, stage+".old-fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err = UnpackNativePackage(archive, filepath.Join(filepath.Dir(archive), NativeChecksums), "v9.0.2", stage); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(r.cfg.StateDir, "native.json")
	statusPath := filepath.Join(r.cfg.StateDir, "status.json")
	supervisorPath := filepath.Join(r.cfg.StateDir, "supervisor.json")
	if err = saveNativeJSON(configPath, r.cfg); err != nil {
		t.Fatal(err)
	}
	if err = saveNativeJSON(statusPath, control.AgentStatus{State: "ready_to_apply", JobID: "self-update", ClientJobID: "update-3333333333333333", CurrentVersion: "v9.0.1", TargetVersion: "v9.0.2"}); err != nil {
		t.Fatal(err)
	}
	cfg := NativeSupervisorConfig{ProgramDir: r.cfg.ProgramDir, UpdaterDir: r.cfg.StateDir, NativeConfig: configPath, InstanceID: r.cfg.InstanceID, Listen: listen, TokenFile: filepath.Join(r.cfg.StateDir, "token.txt"), StatusFile: statusPath, SeedBinary: seed, LogFile: filepath.Join(r.root, "supervisor")}
	if err = saveNativeJSON(supervisorPath, cfg); err != nil {
		t.Fatal(err)
	}
	supervisor := exec.Command(seed, "--supervise-native", supervisorPath)
	hideNativeCommand(supervisor)
	if err = supervisor.Start(); err != nil {
		t.Fatal(err)
	}
	agent := control.NewHTTPAgentClient("http://"+listen, strings.Repeat("a", 64))
	t.Cleanup(func() {
		_ = supervisor.Process.Kill()
		_ = supervisor.Wait()
		s, _ := agent.Status(context.Background())
		if s.PID > 0 {
			image, e := nativeImage(s.PID)
			if e == nil && strings.HasPrefix(strings.ToLower(image), strings.ToLower(r.root)+string(filepath.Separator)) {
				_ = terminateNativeProcess(s.PID, image)
			}
		}
	})
	deadline := time.Now().Add(15 * time.Second)
	var before control.AgentStatus
	for time.Now().Before(deadline) {
		before, err = agent.Status(ctx)
		if err == nil && before.Version == "v9.0.1" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if before.Version != "v9.0.1" || before.PID == 0 {
		t.Fatal("real old host did not start", before, err)
	}
	if err = controlNativeManager(ctx, r.cfg, true); err != nil {
		t.Fatal(err)
	}
	if err = r.e.waitHealth(ctx, "v9.0.1", strings.Repeat("1", 40)); err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSuffix(r.cfg.HealthURL, "/health")
	req, _ := http.NewRequest(http.MethodPost, base+"/api/system/update/apply", bytes.NewBufferString("{}"))
	req.Header.Set("Authorization", "Bearer fictional-console-secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Fatal("confirmation failed", resp.StatusCode)
	}
	deadline = time.Now().Add(2 * time.Minute)
	var after control.AgentStatus
	for time.Now().Before(deadline) {
		after, err = agent.Status(ctx)
		if err == nil && after.Version == "v9.0.2" && after.State == "succeeded" {
			break
		}
		if err == nil && (after.State == "failed" || after.State == "rolled_back" || after.State == "recovery_failed") {
			t.Fatal(after)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if after.Version != "v9.0.2" || after.State != "succeeded" || after.PID == before.PID || after.ClientJobID != "update-3333333333333333" {
		t.Fatal("locked host self-update did not finish", fmt.Sprintf("%+v", after))
	}
	if !nativeProcessAlive(int(supervisor.Process.Pid)) {
		t.Fatal("stable supervisor was replaced/stopped during child update")
	}
	r.assertData(t)
	t.Log("real locked host v9.0.1 -> v9.0.2 restarted from a new immutable path; stable supervisor and latest data retained")
}
