//go:build windows

package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	control "github.com/caigee-cmd/cli2api/internal/update"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type inProcessNativeHost struct{ *NativeExecutor }

func (inProcessNativeHost) RestartHost() {} // A real child host restart is tested separately.

func TestNativeActualGatewayWebConfirmationAndFullPackage(t *testing.T) {
	archive := os.Getenv("CLI2API_NATIVE_APP_PACKAGE")
	if archive == "" {
		t.Skip("set CLI2API_NATIVE_APP_PACKAGE to the real v9.0.2 Windows runtime archive")
	}
	r := newNativeRig(t, "")
	r.e.config.HealthTimeoutSeconds = 30
	ctx := context.Background()
	h, _ := r.e.health(ctx)
	if err := r.e.stop(ctx, h.PID); err != nil {
		t.Fatal(err)
	}
	actual := filepath.Join(r.root, "actual-old.exe")
	cmd := exec.Command("go", "build", "-ldflags", "-X github.com/caigee-cmd/cli2api/internal/buildinfo.Version=v9.0.1 -X github.com/caigee-cmd/cli2api/internal/buildinfo.Commit="+strings.Repeat("1", 40), "-o", actual, "./cmd/server")
	cmd.Dir = filepath.Join("..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	copyNativeTest(t, actual, filepath.Join(r.cfg.ProgramDir, "qoder-api-proxy.exe"))
	r.e.transport.fetch = func(_ context.Context, _ string, name string) ([]byte, error) {
		switch name {
		case NativeAsset:
			return os.ReadFile(archive)
		case NativeChecksums:
			return os.ReadFile(filepath.Join(filepath.Dir(archive), NativeChecksums))
		default:
			return nil, fmt.Errorf("unexpected test asset")
		}
	}
	service := NewService(Config{StatusFile: filepath.Join(r.cfg.StateDir, "status.json"), AuthToken: strings.Repeat("a", 64)}, inProcessNativeHost{r.e})
	agentServer := httptest.NewServer(service.Handler())
	defer agentServer.Close()
	var launch NativeLaunchConfig
	if err := readNativeConfig(r.cfg.LaunchConfig, &launch); err != nil {
		t.Fatal(err)
	}
	launch.AgentURL = agentServer.URL
	if err := saveNativeJSON(r.cfg.LaunchConfig, launch); err != nil {
		t.Fatal(err)
	}
	if err := controlNativeManager(ctx, r.cfg, true); err != nil {
		t.Fatal(err)
	}
	if err := r.e.waitHealth(ctx, "v9.0.1", strings.Repeat("1", 40)); err != nil {
		log, _ := os.ReadFile(filepath.Join(r.root, "gateway.err.log"))
		t.Fatalf("%v; isolated gateway log: %s", err, log)
	}
	agent := control.NewHTTPAgentClient(agentServer.URL, strings.Repeat("a", 64))
	prepared, err := agent.Prepare(ctx, control.PrepareRequest{ClientJobID: "update-1111111111111111", CurrentVersion: "v9.0.1", TargetVersion: "v9.0.2"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		status, err := agent.Status(ctx)
		if err == nil && status.State == "ready_to_apply" {
			break
		}
		if err == nil && strings.HasSuffix(status.State, "failed") {
			t.Fatal(status.Error)
		}
		time.Sleep(100 * time.Millisecond)
	}
	status, err := agent.Status(ctx)
	if err != nil || status.State != "ready_to_apply" {
		t.Fatal("full package did not prepare", status, err)
	}
	// Preparation must leave the real old gateway running until the web confirmation.
	if h, err := r.e.health(ctx); err != nil || h.Version != "v9.0.1" {
		t.Fatal("preparation replaced gateway", h, err)
	}
	base := strings.TrimSuffix(r.cfg.HealthURL, "/health")
	client := &http.Client{Timeout: 45 * time.Second}
	req, _ := http.NewRequest(http.MethodPost, base+"/api/system/update/apply", bytes.NewReader([]byte("{}")))
	req.Header.Set("Authorization", "Bearer fictional-console-secret")
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 202 {
		t.Fatal(response.StatusCode, string(body))
	}
	deadline = time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		status, err = agent.Status(ctx)
		if err == nil && (status.State == "succeeded" || status.State == "rolled_back" || strings.HasSuffix(status.State, "failed")) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if status.State != "succeeded" {
		t.Fatal("real web update failed", status)
	}
	if status.JobID != prepared.JobID {
		t.Fatal("agent changed job identity during confirmation")
	}
	h, err = r.e.health(ctx)
	if err != nil || h.Version != "v9.0.2" || h.Commit != strings.Repeat("2", 40) {
		t.Fatal("real gateway did not run packaged version", h, err)
	}
	r.assertData(t)
	req, _ = http.NewRequest(http.MethodGet, base+"/api/system/update", nil)
	req.Header.Set("Authorization", "Bearer fictional-console-secret")
	response, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode, string(body))
	}
	var info struct {
		Agent  control.AgentStatus `json:"agent"`
		Update *control.Job        `json:"update"`
	}
	if err = json.Unmarshal(body, &info); err != nil {
		t.Fatal(err)
	}
	if info.Update == nil || info.Update.State != "succeeded" || info.Update.AgentJobID != prepared.JobID || info.Update.JobID != "update-1111111111111111" || info.Agent.Backend != NativeBackend {
		t.Fatal("restarted gateway lost persistent update result", string(body))
	}
	t.Logf("real gateway and full runtime: v9.0.1 -> %s; stable job %s; credentials/keys/settings/history preserved", h.Version, info.Update.JobID)
}
