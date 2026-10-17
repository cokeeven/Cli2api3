package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/store"
	control "github.com/caigee-cmd/cli2api/internal/update"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type inProcessDockerHost struct{ *Executor }

func (inProcessDockerHost) RestartHost() {}

// Opt-in: uses only uniquely named containers/volumes, existing public images,
// fictional disabled credentials and an unrelated loopback port.
func TestDockerRealComposeLegacyPreparedProtocol(t *testing.T) {
	if os.Getenv("CLI2API_DOCKER_REHEARSAL") != "1" {
		t.Skip("set CLI2API_DOCKER_REHEARSAL=1 for isolated real Docker regression")
	}
	root := t.TempDir()
	id := fmt.Sprintf("cli2api-isolated-native-%d", time.Now().UnixNano())
	volume := id + "-data"
	container := id + "-gateway"
	helper := id + "-seed"
	repo := "cli2api-isolated/" + id
	run := func(args ...string) []byte {
		t.Helper()
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("isolated docker command failed (%v): %v\n%s", args, err, out)
		}
		return out
	}
	for _, v := range []string{"v0.1.4", "v0.1.5"} {
		run("image", "inspect", "ghcr.io/cokeeven/ali2api3:"+v)
		run("image", "tag", "ghcr.io/cokeeven/ali2api3:"+v, repo+":"+v)
	}
	run("volume", "create", "--label", "cli2api.isolation=native-regression", volume)
	compose := filepath.Join(root, "compose.yml")
	env := filepath.Join(root, "compose.env")
	t.Cleanup(func() {
		_ = exec.Command("docker", "compose", "--env-file", env, "-f", compose, "-p", id, "down", "--remove-orphans").Run()
		_ = exec.Command("docker", "rm", "-f", helper).Run()
		_ = exec.Command("docker", "volume", "rm", volume).Run()
		_ = exec.Command("docker", "image", "rm", repo+":v0.1.4", repo+":v0.1.5").Run()
	})
	seed := filepath.Join(root, "seed")
	_ = os.MkdirAll(seed, 0700)
	db, err := store.OpenStore(filepath.Join(seed, "qoder.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a, err := db.Create(ctx, accounts.CreateAccount{Name: "Docker fictional", Provider: "trae", Region: "cn", Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SaveCredentialPayload(ctx, a.ID, "trae-oauth-v1", []byte(`{"fake":"isolated-only"}`)); err != nil {
		t.Fatal(err)
	}
	if err = db.SetSecret(ctx, "proxy_api_key", "fictional-docker-console-key"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.InsertAPIKey(ctx, accounts.StoredAPIKey{ID: "fictional-docker-key", Name: "fictional", Prefix: "fake", KeyHash: "fictional-only-hash", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	backup, err := db.Backup(ctx, filepath.Join(seed, "backups"), 5)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	run("create", "--name", helper, "-v", volume+":/data", "--entrypoint", "/bin/true", repo+":v0.1.4")
	run("cp", seed+string(filepath.Separator)+".", helper+":/data/")
	run("rm", helper)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	config := fmt.Sprintf("name: %s\nservices:\n  gateway:\n    image: ${CLI2API_IMAGE}\n    container_name: %s\n    ports: [\"127.0.0.1:%d:3010\"]\n    environment:\n      HOST: 0.0.0.0\n      QODER_DATA_DIR: /data\n    volumes: [\"%s:/data\"]\nvolumes:\n  %s:\n    external: true\n", id, container, port, volume, volume)
	if err = os.WriteFile(compose, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(env, []byte("CLI2API_IMAGE="+repo+":v0.1.4\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("compose", "--env-file", env, "-f", compose, "-p", id, "up", "-d")
	e := NewExecutor(ExecutorConfig{ComposeFile: compose, EnvFile: env, ServiceName: "gateway", ContainerName: container, ImageRepository: repo, HealthURL: fmt.Sprintf("http://127.0.0.1:%d/health", port), HealthTimeout: 90 * time.Second})
	if err = e.waitForVersion(ctx, "v0.1.4"); err != nil {
		t.Fatal(err)
	}
	service := NewService(Config{StatusFile: filepath.Join(root, "status.json"), AuthToken: "fictional-loopback-agent-token"}, inProcessDockerHost{e})
	service.status = control.AgentStatus{State: "ready_to_apply", JobID: "legacy-prepared", CurrentVersion: "v0.1.4", TargetVersion: "v0.1.5"}
	agentServer := httptest.NewServer(service.Handler())
	defer agentServer.Close()
	agent := control.NewHTTPAgentClient(agentServer.URL, "fictional-loopback-agent-token")
	if _, err = agent.ApplyPrepared(ctx, ApplyRequest{CurrentVersion: "v0.1.4", TargetVersion: "v0.1.5", BackupPath: "/data/backups/" + backup.Name}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Minute)
	var status control.AgentStatus
	for time.Now().Before(deadline) {
		status, err = agent.Status(ctx)
		if err == nil && (status.State == "succeeded" || status.State == "failed" || status.State == "rolled_back") {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if status.State != "succeeded" {
		t.Fatal("legacy prepared Docker update failed", status)
	}
	for _, endpoint := range []string{"/api/accounts", "/api/keys"} {
		req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", port, endpoint), nil)
		req.Header.Set("Authorization", "Bearer fictional-docker-console-key")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Data []json.RawMessage `json:"data"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 || len(body.Data) != 1 {
			t.Fatalf("Docker data/authentication changed at %s: %d %v", endpoint, resp.StatusCode, err)
		}
	}
	t.Log("real isolated Compose v0.1.4 -> v0.1.5 succeeded through legacy staged request; named data mount, fictional account and key retained")
}
