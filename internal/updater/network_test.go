package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	control "github.com/caigee-cmd/cli2api/internal/update"
	"github.com/caigee-cmd/cli2api/internal/updatenet"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type networkTransport func(*http.Request) (*http.Response, error)

func (f networkTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func writeNetworkConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "network.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHostBinaryUsesVerifiedMirrorAndNeverLeaksGitHubToken(t *testing.T) {
	payload := []byte("verified mirror updater")
	sum := sha256.Sum256(payload)
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("token leaked to mirror")
		}
		if !strings.HasPrefix(r.URL.Path, "/https://github.com/cokeeven/Cli2api3/releases/download/v0.1.9/") {
			t.Errorf("mirror path = %s", r.URL.Path)
		}
		if strings.HasSuffix(r.URL.Path, "checksums.txt") {
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), hostUpdaterAssetName())
		} else {
			_, _ = w.Write(payload)
		}
	}))
	defer mirror.Close()
	host := filepath.Join(t.TempDir(), "updater")
	e := NewExecutor(ExecutorConfig{HostBinaryPath: host, GitHubToken: "private-token", NetworkFile: writeNetworkConfig(t, fmt.Sprintf(`{"download_mirrors":[%q]}`, mirror.URL))})
	e.download.Transport = networkTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "github.com" {
			if r.Header.Get("Authorization") != "Bearer private-token" {
				t.Error("origin auth missing")
			}
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("failed")), Request: r, Header: make(http.Header)}, nil
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	if err := e.stageHostBinary(context.Background(), "v0.1.9", nil); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(host + ".new")
	if err != nil || string(actual) != string(payload) {
		t.Fatalf("staged payload: %q %v", actual, err)
	}
	if !e.NetworkStatus().Fallback {
		t.Fatal("fallback status missing")
	}
}

func TestDownloadRedirectRejectsUnconfiguredHostBeforeSendingToken(t *testing.T) {
	e := NewExecutor(ExecutorConfig{GitHubToken: "private-token"})
	e.download.Transport = networkTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "github.com" {
			t.Fatal("untrusted host received request")
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"https://untrusted.example/file"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	if _, err := e.downloadReleaseFile(context.Background(), "v0.1.9", "updater"); err == nil {
		t.Fatal("untrusted redirect accepted")
	}
}

func TestMirrorChecksumMismatchDoesNotStageBinary(t *testing.T) {
	host := filepath.Join(t.TempDir(), "updater")
	e := NewExecutor(ExecutorConfig{HostBinaryPath: host})
	e.fetch = func(context.Context, string, string) ([]byte, error) { return []byte("tampered"), nil }
	if err := e.stageHostBinary(context.Background(), "v0.1.9", nil); err == nil {
		t.Fatal("tampered binary accepted")
	}
	if _, err := os.Stat(host + ".new"); !os.IsNotExist(err) {
		t.Fatal("tampered binary staged")
	}
}

func TestImageFallbackPullsPublishedDigestBeforeRetagging(t *testing.T) {
	e := NewExecutor(ExecutorConfig{NetworkFile: writeNetworkConfig(t, `{"image_mirrors":["mirror.example/cokeeven/ali2api3"]}`)})
	digest := "sha256:" + strings.Repeat("a", 64)
	e.fetch = func(_ context.Context, version, name string) ([]byte, error) {
		if name != imageManifestName || version != "v0.1.9" {
			t.Fatalf("unexpected manifest: %s %s", version, name)
		}
		return json.Marshal(imageManifest{Version: version, Repository: e.config.ImageRepository, Digest: digest})
	}
	target := e.config.ImageRepository + ":v0.1.9"
	ref := "mirror.example/cokeeven/ali2api3@" + digest
	runner := &scriptedRunner{t: t, steps: []runnerStep{
		{name: "docker", args: []string{"pull", target}, err: errors.New("primary unavailable")},
		{name: "docker", args: []string{"pull", ref}},
		{name: "docker", args: []string{"image", "tag", ref, target}},
	}}
	e.runner = runner
	if err := e.pullTargetImage(context.Background(), "v0.1.9"); err != nil {
		t.Fatal(err)
	}
	runner.assertDone()
	if !e.NetworkStatus().Fallback {
		t.Fatal("fallback source missing")
	}
}

func TestInvalidImageManifestCannotPullOrTagMirror(t *testing.T) {
	for _, manifest := range []imageManifest{
		{Version: "v0.1.8", Repository: "ghcr.io/cokeeven/ali2api3", Digest: "sha256:" + strings.Repeat("a", 64)},
		{Version: "v0.1.9", Repository: "ghcr.io/other/app", Digest: "sha256:" + strings.Repeat("a", 64)},
		{Version: "v0.1.9", Repository: "ghcr.io/cokeeven/ali2api3", Digest: "latest"},
	} {
		e := NewExecutor(ExecutorConfig{NetworkFile: writeNetworkConfig(t, `{"image_mirrors":["mirror.example/app"]}`)})
		e.fetch = func(context.Context, string, string) ([]byte, error) { return json.Marshal(manifest) }
		runner := &scriptedRunner{t: t, steps: []runnerStep{{name: "docker", args: []string{"pull", "ghcr.io/cokeeven/ali2api3:v0.1.9"}, err: errors.New("offline")}}}
		e.runner = runner
		if err := e.pullTargetImage(context.Background(), "v0.1.9"); err == nil {
			t.Fatal("invalid manifest accepted")
		}
		runner.assertDone()
	}
}

type deadlineRunner struct{ calls int }

func (r *deadlineRunner) Run(ctx context.Context, _ string, _ ...string) ([]byte, error) {
	r.calls++
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestImagePullTimeoutIsBoundedAndDoesNotRecreateService(t *testing.T) {
	e := NewExecutor(ExecutorConfig{NetworkFile: writeNetworkConfig(t, `{"pull_timeout_seconds":1}`)})
	runner := &deadlineRunner{}
	e.runner = runner
	start := time.Now()
	if err := e.pullTargetImage(context.Background(), "v0.1.9"); err == nil {
		t.Fatal("timeout accepted")
	}
	if runner.calls != 1 || time.Since(start) > 5*time.Second {
		t.Fatalf("unbounded or extra commands: %d", runner.calls)
	}
}

func TestFailedImagePreparationKeepsConfigurationAndData(t *testing.T) {
	directory := t.TempDir()
	envPath, dbPath := filepath.Join(directory, ".env"), filepath.Join(directory, "qoder.db")
	before := []byte("CLI2API_IMAGE=ghcr.io/cokeeven/ali2api3:v0.1.8\n")
	for path, body := range map[string][]byte{envPath: before, dbPath: []byte("latest business data")} {
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e := NewExecutor(ExecutorConfig{ComposeFile: filepath.Join(directory, "compose.yml"), EnvFile: envPath, NetworkFile: writeNetworkConfig(t, `{"image_mirrors":["mirror.example/app"]}`)})
	e.fetch = func(context.Context, string, string) ([]byte, error) {
		return nil, errors.New("all metadata unavailable")
	}
	runner := &scriptedRunner{t: t, steps: []runnerStep{{name: "docker", args: []string{"pull", "ghcr.io/cokeeven/ali2api3:v0.1.9"}, err: errors.New("offline")}}}
	e.runner = runner
	if err := e.Prepare(context.Background(), "network-failure", control.PrepareRequest{CurrentVersion: "v0.1.8", TargetVersion: "v0.1.9"}, func(string) {}); err == nil {
		t.Fatal("offline preparation succeeded")
	}
	runner.assertDone()
	actual, _ := os.ReadFile(envPath)
	if string(actual) != string(before) {
		t.Fatal("image configuration changed")
	}
	data, _ := os.ReadFile(dbPath)
	if string(data) != "latest business data" {
		t.Fatal("data changed")
	}
}

func TestNetworkSourcesSurviveUpdaterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	e := NewExecutor(ExecutorConfig{})
	e.recordNetworkSource("image", "mirror.example/app", true)
	e.recordNetworkSource("download", "github.com", false)
	s := NewService(Config{StatusFile: path}, e)
	s.status.JobID = "test-job"
	s.updateState("test-job", "succeeded", "", true)
	restarted := NewService(Config{StatusFile: path}, NewExecutor(ExecutorConfig{}))
	rr := httptest.NewRecorder()
	restarted.handleStatus(rr, httptest.NewRequest("GET", "/v1/status", nil))
	var status struct {
		Network *updatenet.Status `json:"network"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Network == nil || status.Network.ImageSource != "mirror.example/app" || status.Network.DownloadSource != "github.com" || !status.Network.ImageFallback || status.Network.DownloadFallback {
		t.Fatalf("network state lost after restart: %s", rr.Body.String())
	}
}
