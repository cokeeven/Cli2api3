package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type runnerStep struct {
	name          string
	args          []string
	output        []byte
	err           error
	requireActive bool
	beforeReturn  func()
}

// anyArg is a positional wildcard for scripted commands whose exact value is
// not known ahead of time (for example a timestamped preservation path).
const anyArg = "\x00any-arg"

type scriptedRunner struct {
	t     *testing.T
	steps []runnerStep
	index int
}

func (r *scriptedRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.t.Helper()
	if r.index >= len(r.steps) {
		r.t.Fatalf("unexpected command: %s %s", name, strings.Join(args, " "))
	}
	step := r.steps[r.index]
	r.index++
	if name != step.name || !argsMatch(step.args, args) {
		r.t.Fatalf("command %d = %s %q, want %s %q", r.index, name, args, step.name, step.args)
	}
	if step.requireActive && ctx.Err() != nil {
		r.t.Fatalf("command %d received canceled context: %v", r.index, ctx.Err())
	}
	if step.beforeReturn != nil {
		step.beforeReturn()
	}
	return step.output, step.err
}

func argsMatch(want, got []string) bool {
	if len(want) != len(got) {
		return false
	}
	for i := range want {
		if strings.Contains(want[i], anyArg) {
			if strings.TrimSpace(got[i]) == "" {
				return false
			}
			continue
		}
		if want[i] != got[i] {
			return false
		}
	}
	return true
}

func (r *scriptedRunner) assertDone() {
	r.t.Helper()
	if r.index != len(r.steps) {
		r.t.Fatalf("executed %d commands, want %d", r.index, len(r.steps))
	}
}

func TestExecutorApplySuccess(t *testing.T) {
	directory := t.TempDir()
	envPath := filepath.Join(directory, ".env")
	composePath := filepath.Join(directory, "docker-compose.yml")
	if err := os.WriteFile(envPath, []byte("QODER_MAX_INFLIGHT=4\nCLI2API_IMAGE=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(composePath, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	const (
		currentVersion = "v0.2.1"
		targetVersion  = "v0.2.2"
		backupPath     = "/data/backups/qoder-20260824T010203.000000000Z.db"
		repository     = "ghcr.io/cokeeven/ali2api3"
	)
	targetImage := repository + ":" + targetVersion
	currentImage := repository + ":" + currentVersion
	inspectBefore := inspectOutputWithNetworks("sha256:old", "qoder-data", map[string][]string{
		"deploy_default":                 {"qoder-api-proxy", "qoder-api-proxy"},
		"sub2api-deploy_sub2api-network": {"qoder-api-proxy"},
	})
	inspectAfter := inspectOutputWithNetworks("sha256:new", "qoder-data", map[string][]string{
		"deploy_default": {"qoder-api-proxy", "qoder-api-proxy"},
	})

	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"ok":true,"version":%q}`, targetVersion)
	}))
	defer health.Close()

	runner := &scriptedRunner{t: t, steps: []runnerStep{
		{name: "docker", args: []string{"inspect", "qoder-api-proxy"}, output: inspectBefore},
		{name: "docker", args: []string{"image", "tag", "sha256:old", currentImage}},
		{name: "docker", args: []string{"run", "--rm", "-v", "qoder-data:/data", "-e", "BACKUP_PATH=" + backupPath, "--entrypoint", "/bin/sh", currentImage, "-c", backupVerifyScript()}},
		{name: "docker", args: []string{"pull", targetImage}},
		{name: "docker", args: composeArgs(envPath, composePath, "up", "-d", "--no-deps", "--force-recreate", "qoder-api-proxy")},
		{name: "docker", args: []string{"inspect", "qoder-api-proxy"}, output: inspectAfter},
		{name: "docker", args: []string{"network", "connect", "--alias", "qoder-api-proxy", "sub2api-deploy_sub2api-network", "qoder-api-proxy"}},
	}}
	executor := NewExecutor(ExecutorConfig{
		ComposeFile: composePath, EnvFile: envPath, ImageRepository: repository,
		HealthURL: health.URL, HealthTimeout: time.Second,
	})
	executor.runner = runner

	var states []string
	rolledBack, err := executor.Apply(context.Background(), "job-success", ApplyRequest{
		CurrentVersion: currentVersion,
		TargetVersion:  targetVersion,
		BackupPath:     backupPath,
	}, func(state string) {
		states = append(states, state)
	})
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack {
		t.Fatal("successful update reported rollback")
	}
	if want := []string{"preparing", "pulling", "recreating", "checking"}; !reflect.DeepEqual(states, want) {
		t.Fatalf("states = %v, want %v", states, want)
	}
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "CLI2API_IMAGE="+targetImage) {
		t.Fatalf("env = %q", data)
	}
	runner.assertDone()
}

// A failed update must roll the program back while keeping the newest data:
// migrations only append, so the older binary reads the newer database. The
// pre-upgrade snapshot must NOT be copied over the live database here.
func TestExecutorRollbackKeepsNewestDatabase(t *testing.T) {
	directory := t.TempDir()
	envPath := filepath.Join(directory, ".env")
	composePath := filepath.Join(directory, "docker-compose.yml")
	originalEnv := []byte("QODER_MAX_INFLIGHT=4\nCLI2API_IMAGE=old\n")
	if err := os.WriteFile(envPath, originalEnv, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(composePath, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	const (
		currentVersion = "v0.2.1"
		targetVersion  = "v0.2.2"
		backupPath     = "/data/backups/qoder-20260824T010203.000000000Z.db"
		repository     = "ghcr.io/cokeeven/ali2api3"
	)
	targetImage := repository + ":" + targetVersion
	currentImage := repository + ":" + currentVersion
	applyCtx, cancelApply := context.WithCancel(context.Background())
	defer cancelApply()

	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"ok":true,"version":%q}`, currentVersion)
	}))
	defer health.Close()

	runner := &scriptedRunner{t: t, steps: []runnerStep{
		{name: "docker", args: []string{"inspect", "qoder-api-proxy"}, output: inspectOutputWithNetworks("sha256:old", "qoder-data", map[string][]string{
			"deploy_default":                 {"qoder-api-proxy"},
			"sub2api-deploy_sub2api-network": {"qoder-api-proxy"},
		})},
		{name: "docker", args: []string{"image", "tag", "sha256:old", currentImage}},
		{name: "docker", args: []string{"run", "--rm", "-v", "qoder-data:/data", "-e", "BACKUP_PATH=" + backupPath, "--entrypoint", "/bin/sh", currentImage, "-c", backupVerifyScript()}},
		{name: "docker", args: []string{"pull", targetImage}},
		{name: "docker", args: composeArgs(envPath, composePath, "up", "-d", "--no-deps", "--force-recreate", "qoder-api-proxy"), err: errors.New("recreate failed"), beforeReturn: cancelApply},
		{name: "docker", args: composeArgs(envPath, composePath, "stop", "qoder-api-proxy"), requireActive: true},
		{name: "docker", args: composeArgs(envPath, composePath, "up", "-d", "--no-deps", "--force-recreate", "qoder-api-proxy"), requireActive: true},
		{name: "docker", args: []string{"inspect", "qoder-api-proxy"}, output: inspectOutputWithNetworks("sha256:old", "qoder-data", map[string][]string{
			"deploy_default": {"qoder-api-proxy"},
		}), requireActive: true},
		{name: "docker", args: []string{"network", "connect", "--alias", "qoder-api-proxy", "sub2api-deploy_sub2api-network", "qoder-api-proxy"}, requireActive: true},
	}}
	executor := NewExecutor(ExecutorConfig{
		ComposeFile: composePath, EnvFile: envPath, ImageRepository: repository,
		HealthURL: health.URL, HealthTimeout: time.Second,
	})
	executor.runner = runner

	var states []string
	rolledBack, err := executor.Apply(applyCtx, "job-rollback", ApplyRequest{
		CurrentVersion: currentVersion,
		TargetVersion:  targetVersion,
		BackupPath:     backupPath,
	}, func(state string) {
		states = append(states, state)
	})
	if err == nil || !strings.Contains(err.Error(), "was rolled back") {
		t.Fatalf("error = %v", err)
	}
	if !rolledBack {
		t.Fatal("rollback was not reported")
	}
	if want := []string{"preparing", "pulling", "recreating", "rolling_back"}; !reflect.DeepEqual(states, want) {
		t.Fatalf("states = %v, want %v", states, want)
	}
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "CLI2API_IMAGE="+currentImage) {
		t.Fatalf("env = %q, want pinned current image %q", data, currentImage)
	}
	runner.assertDone()
}

// An explicit operator choice to restore the pre-upgrade database must copy
// the post-upgrade database aside first, and only then overwrite it. If that
// preservation fails, the rollback must stop instead of destroying newer data.
func TestExecutorRollbackPreservesNewestDatabaseBeforeRestore(t *testing.T) {
	directory := t.TempDir()
	envPath := filepath.Join(directory, ".env")
	composePath := filepath.Join(directory, "docker-compose.yml")
	if err := os.WriteFile(envPath, []byte("CLI2API_IMAGE=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(composePath, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	const (
		currentVersion = "v0.2.1"
		targetVersion  = "v0.2.2"
		backupPath     = "/data/backups/qoder-20260824T010203.000000000Z.db"
		repository     = "ghcr.io/cokeeven/ali2api3"
	)
	targetImage := repository + ":" + targetVersion
	currentImage := repository + ":" + currentVersion

	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"ok":true,"version":%q}`, currentVersion)
	}))
	defer health.Close()

	runner := &scriptedRunner{t: t, steps: []runnerStep{
		{name: "docker", args: []string{"inspect", "qoder-api-proxy"}, output: inspectOutputWithNetworks("sha256:old", "qoder-data", map[string][]string{
			"deploy_default": {"qoder-api-proxy"},
		})},
		{name: "docker", args: []string{"image", "tag", "sha256:old", currentImage}},
		{name: "docker", args: []string{"run", "--rm", "-v", "qoder-data:/data", "-e", "BACKUP_PATH=" + backupPath, "--entrypoint", "/bin/sh", currentImage, "-c", backupVerifyScript()}},
		{name: "docker", args: []string{"pull", targetImage}},
		{name: "docker", args: composeArgs(envPath, composePath, "up", "-d", "--no-deps", "--force-recreate", "qoder-api-proxy"), err: errors.New("recreate failed")},
		{name: "docker", args: composeArgs(envPath, composePath, "stop", "qoder-api-proxy")},
		{name: "docker", args: []string{"run", "--rm", "-v", "qoder-data:/data", "-e", "PRESERVE_PATH=" + anyArg, "--entrypoint", "/bin/sh", currentImage, "-c", anyArg}},
		{name: "docker", args: []string{"run", "--rm", "-v", "qoder-data:/data", "-e", "BACKUP_PATH=" + backupPath, "--entrypoint", "/bin/sh", currentImage, "-c", restoreSQLiteScript()}},
		{name: "docker", args: composeArgs(envPath, composePath, "up", "-d", "--no-deps", "--force-recreate", "qoder-api-proxy")},
		{name: "docker", args: []string{"inspect", "qoder-api-proxy"}, output: inspectOutputWithNetworks("sha256:old", "qoder-data", map[string][]string{
			"deploy_default": {"qoder-api-proxy"},
		})},
	}}
	executor := NewExecutor(ExecutorConfig{
		ComposeFile: composePath, EnvFile: envPath, ImageRepository: repository,
		HealthURL: health.URL, HealthTimeout: time.Second,
	})
	executor.runner = runner

	rolledBack, err := executor.Apply(context.Background(), "job-restore", ApplyRequest{
		CurrentVersion: currentVersion, TargetVersion: targetVersion,
		BackupPath: backupPath, RestoreDatabase: true,
	}, func(string) {})
	if err == nil || !rolledBack || !strings.Contains(err.Error(), "was rolled back") {
		t.Fatalf("rolledBack=%v err=%v", rolledBack, err)
	}
	if preserved := executor.LastPreservedDatabase(); !strings.HasPrefix(preserved, "/data/backups/qoder-pre-rollback-") {
		t.Fatalf("preserved path = %q", preserved)
	}
}

// When the post-upgrade database cannot be preserved, the explicit restore
// must abort and leave /data/qoder.db untouched.
func TestExecutorRollbackAbortsRestoreWhenPreservationFails(t *testing.T) {
	directory := t.TempDir()
	envPath := filepath.Join(directory, ".env")
	composePath := filepath.Join(directory, "docker-compose.yml")
	if err := os.WriteFile(envPath, []byte("CLI2API_IMAGE=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(composePath, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	const (
		currentVersion = "v0.2.1"
		targetVersion  = "v0.2.2"
		backupPath     = "/data/backups/qoder-20260824T010203.000000000Z.db"
		repository     = "ghcr.io/cokeeven/ali2api3"
	)
	targetImage := repository + ":" + targetVersion
	currentImage := repository + ":" + currentVersion

	runner := &scriptedRunner{t: t, steps: []runnerStep{
		{name: "docker", args: []string{"inspect", "qoder-api-proxy"}, output: inspectOutputWithNetworks("sha256:old", "qoder-data", map[string][]string{
			"deploy_default": {"qoder-api-proxy"},
		})},
		{name: "docker", args: []string{"image", "tag", "sha256:old", currentImage}},
		{name: "docker", args: []string{"run", "--rm", "-v", "qoder-data:/data", "-e", "BACKUP_PATH=" + backupPath, "--entrypoint", "/bin/sh", currentImage, "-c", backupVerifyScript()}},
		{name: "docker", args: []string{"pull", targetImage}},
		{name: "docker", args: composeArgs(envPath, composePath, "up", "-d", "--no-deps", "--force-recreate", "qoder-api-proxy"), err: errors.New("recreate failed")},
		{name: "docker", args: composeArgs(envPath, composePath, "stop", "qoder-api-proxy")},
		{name: "docker", args: []string{"run", "--rm", "-v", "qoder-data:/data", "-e", "PRESERVE_PATH=" + anyArg, "--entrypoint", "/bin/sh", currentImage, "-c", anyArg}, err: errors.New("copy failed")},
		// No restore step and no restart: the rollback must stop with the
		// newest database intact.
	}}
	executor := NewExecutor(ExecutorConfig{
		ComposeFile: composePath, EnvFile: envPath, ImageRepository: repository,
		HealthURL: "http://127.0.0.1:1/health", HealthTimeout: time.Second,
	})
	executor.runner = runner

	rolledBack, err := executor.Apply(context.Background(), "job-abort", ApplyRequest{
		CurrentVersion: currentVersion, TargetVersion: targetVersion,
		BackupPath: backupPath, RestoreDatabase: true,
	}, func(string) {})
	if err == nil || rolledBack {
		t.Fatalf("rolledBack=%v err=%v", rolledBack, err)
	}
	if !strings.Contains(err.Error(), "preserve post-upgrade database") {
		t.Fatalf("error = %v", err)
	}
	if preserved := executor.LastPreservedDatabase(); preserved != "" {
		t.Fatalf("failed preservation must not report a path: %q", preserved)
	}
	runner.assertDone()
}

// An upgrade must stop before pulling or recreating anything when the
// pre-upgrade snapshot cannot be verified: the backup is the only way back,
// so an unverifiable one is a hard stop.
func TestExecutorApplyStopsWhenBackupCannotBeVerified(t *testing.T) {
	directory := t.TempDir()
	envPath := filepath.Join(directory, ".env")
	composePath := filepath.Join(directory, "docker-compose.yml")
	if err := os.WriteFile(envPath, []byte("CLI2API_IMAGE=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(composePath, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	const (
		currentVersion = "v0.2.1"
		targetVersion  = "v0.2.2"
		backupPath     = "/data/backups/qoder-20260824T010203.000000000Z.db"
		repository     = "ghcr.io/cokeeven/ali2api3"
	)
	currentImage := repository + ":" + currentVersion

	runner := &scriptedRunner{t: t, steps: []runnerStep{
		{name: "docker", args: []string{"inspect", "qoder-api-proxy"}, output: inspectOutputWithNetworks("sha256:old", "qoder-data", map[string][]string{
			"deploy_default": {"qoder-api-proxy"},
		})},
		{name: "docker", args: []string{"image", "tag", "sha256:old", currentImage}},
		{name: "docker", args: []string{"run", "--rm", "-v", "qoder-data:/data", "-e", "BACKUP_PATH=" + backupPath, "--entrypoint", "/bin/sh", currentImage, "-c", backupVerifyScript()}, err: errors.New("checksum mismatch")},
		// Nothing after this: no pull, no recreate.
	}}
	executor := NewExecutor(ExecutorConfig{
		ComposeFile: composePath, EnvFile: envPath, ImageRepository: repository,
		HealthURL: "http://127.0.0.1:1/health", HealthTimeout: time.Second,
	})
	executor.runner = runner

	rolledBack, err := executor.Apply(context.Background(), "job-backup-failed", ApplyRequest{
		CurrentVersion: currentVersion, TargetVersion: targetVersion, BackupPath: backupPath,
	}, func(string) {})
	if err == nil || rolledBack {
		t.Fatalf("rolledBack=%v err=%v", rolledBack, err)
	}
	if !strings.Contains(err.Error(), "verify sqlite backup integrity") {
		t.Fatalf("error = %v", err)
	}
	runner.assertDone()
}

// A new program that never becomes healthy must be rolled back to the previous
// program while the database stays as it is (post-upgrade writes survive).
func TestExecutorRollbackWhenNewProgramNeverStarts(t *testing.T) {
	directory := t.TempDir()
	envPath := filepath.Join(directory, ".env")
	composePath := filepath.Join(directory, "docker-compose.yml")
	if err := os.WriteFile(envPath, []byte("CLI2API_IMAGE=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(composePath, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	const (
		currentVersion = "v0.2.1"
		targetVersion  = "v0.2.2"
		backupPath     = "/data/backups/qoder-20260824T010203.000000000Z.db"
		repository     = "ghcr.io/cokeeven/ali2api3"
	)
	targetImage := repository + ":" + targetVersion
	currentImage := repository + ":" + currentVersion

	// The replaced container keeps reporting the old version: the new program
	// never comes up.
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"ok":true,"version":%q}`, currentVersion)
	}))
	defer health.Close()

	before := inspectOutputWithNetworks("sha256:old", "qoder-data", map[string][]string{
		"deploy_default": {"qoder-api-proxy"},
	})
	after := inspectOutputWithNetworks("sha256:new", "qoder-data", map[string][]string{
		"deploy_default": {"qoder-api-proxy"},
	})
	runner := &scriptedRunner{t: t, steps: []runnerStep{
		{name: "docker", args: []string{"inspect", "qoder-api-proxy"}, output: before},
		{name: "docker", args: []string{"image", "tag", "sha256:old", currentImage}},
		{name: "docker", args: []string{"run", "--rm", "-v", "qoder-data:/data", "-e", "BACKUP_PATH=" + backupPath, "--entrypoint", "/bin/sh", currentImage, "-c", backupVerifyScript()}},
		{name: "docker", args: []string{"pull", targetImage}},
		{name: "docker", args: composeArgs(envPath, composePath, "up", "-d", "--no-deps", "--force-recreate", "qoder-api-proxy")},
		{name: "docker", args: []string{"inspect", "qoder-api-proxy"}, output: after},
		// Health check for the target version fails (the server reports the
		// old version), so the updater rolls back.
		{name: "docker", args: composeArgs(envPath, composePath, "stop", "qoder-api-proxy"), requireActive: true},
		{name: "docker", args: composeArgs(envPath, composePath, "up", "-d", "--no-deps", "--force-recreate", "qoder-api-proxy"), requireActive: true},
		{name: "docker", args: []string{"inspect", "qoder-api-proxy"}, output: before, requireActive: true},
	}}
	executor := NewExecutor(ExecutorConfig{
		ComposeFile: composePath, EnvFile: envPath, ImageRepository: repository,
		HealthURL: health.URL, HealthTimeout: 300 * time.Millisecond,
	})
	executor.runner = runner

	rolledBack, err := executor.Apply(context.Background(), "job-start-failed", ApplyRequest{
		CurrentVersion: currentVersion, TargetVersion: targetVersion, BackupPath: backupPath,
	}, func(string) {})
	if err == nil || !rolledBack {
		t.Fatalf("rolledBack=%v err=%v", rolledBack, err)
	}
	if !strings.Contains(err.Error(), "was rolled back") {
		t.Fatalf("error = %v", err)
	}
	// Rollback must not have restored the pre-upgrade database.
	if preserved := executor.LastPreservedDatabase(); preserved != "" {
		t.Fatalf("program-only rollback preserved/replaced the database: %q", preserved)
	}
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "CLI2API_IMAGE="+currentImage) {
		t.Fatalf("env = %q, want the previous image", data)
	}
	runner.assertDone()
}

func TestSetEnvValueAtomicPreservesExistingSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("QODER_MAX_INFLIGHT=4\nCLI2API_IMAGE=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	original, mode, err := readEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := setEnvValueAtomic(path, mode, "CLI2API_IMAGE", "ghcr.io/cokeeven/ali2api3:v0.2.2"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "QODER_MAX_INFLIGHT=4") || !strings.Contains(text, "CLI2API_IMAGE=ghcr.io/cokeeven/ali2api3:v0.2.2") {
		t.Fatalf("env = %q", text)
	}
	if err := writeEnvFileAtomic(path, mode, original); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(original) {
		t.Fatalf("restored env = %q", restored)
	}
}

func TestExecutorConfigRequiresAbsoluteFiles(t *testing.T) {
	for _, config := range []ExecutorConfig{
		{ComposeFile: "deploy/docker-compose.yml", EnvFile: "/tmp/.env"},
		{ComposeFile: "/tmp/docker-compose.yml", EnvFile: "deploy/.env"},
	} {
		if err := NewExecutor(config).validateConfig(); err == nil {
			t.Fatalf("config unexpectedly accepted: %+v", config)
		}
	}
}

func TestValidateApplyRequestRejectsUnsafeInput(t *testing.T) {
	valid := ApplyRequest{CurrentVersion: "v0.2.1", TargetVersion: "v0.2.2", BackupPath: "/data/backups/qoder-20260824T010203.000000000Z.db"}
	if err := validateApplyRequest(valid); err != nil {
		t.Fatal(err)
	}
	for _, request := range []ApplyRequest{
		{CurrentVersion: "v0.2.1", TargetVersion: "latest", BackupPath: valid.BackupPath},
		{CurrentVersion: "v0.2.1", TargetVersion: "v0.2.1", BackupPath: valid.BackupPath},
		{CurrentVersion: "v0.2.1", TargetVersion: "v0.2.2", BackupPath: "/etc/passwd"},
		{CurrentVersion: "v0.2.1", TargetVersion: "v0.2.2", BackupPath: "/data/backups/../qoder.db"},
	} {
		if err := validateApplyRequest(request); err == nil {
			t.Fatalf("request unexpectedly accepted: %+v", request)
		}
	}
}

func inspectOutputWithNetworks(image, volume string, networks map[string][]string) []byte {
	payload := []map[string]any{{
		"Image": image,
		"Mounts": []map[string]string{{
			"Type": "volume", "Name": volume,
			"Source": "/var/lib/docker/volumes/" + volume + "/_data", "Destination": "/data",
		}},
		"NetworkSettings": map[string]any{"Networks": networkPayload(networks)},
	}}
	data, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return data
}

func networkPayload(networks map[string][]string) map[string]any {
	payload := make(map[string]any, len(networks))
	for name, aliases := range networks {
		payload[name] = map[string]any{"Aliases": aliases}
	}
	return payload
}

func composeArgs(envPath, composePath string, args ...string) []string {
	result := []string{"compose", "--env-file", envPath, "-f", composePath}
	return append(result, args...)
}

func restoreSQLiteScript() string {
	return `set -eu
test -f "$BACKUP_PATH"
cp "$BACKUP_PATH" /data/qoder.db.restore
chmod 600 /data/qoder.db.restore
mv /data/qoder.db.restore /data/qoder.db
rm -f /data/qoder.db-wal /data/qoder.db-shm /data/qoder.db-journal
sync`
}

// backupVerifyScript mirrors Executor.verifyBackupExists: the snapshot must
// exist, be non-empty, carry its integrity manifest, and match the recorded
// SHA-256. Kept in the test so a changed script fails loudly.
func backupVerifyScript() string {
	return `set -eu
test -n "$BACKUP_PATH"
test -f "$BACKUP_PATH"
test -s "$BACKUP_PATH"
MANIFEST="$BACKUP_PATH.manifest.json"
test -f "$MANIFEST"
recorded="$(sed -n 's/.*"sha256"[[:space:]]*:[[:space:]]*"\([0-9a-fA-F]\{64\}\)".*/\1/p' "$MANIFEST" | head -n 1 | tr 'A-F' 'a-f')"
test -n "$recorded"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$BACKUP_PATH" | awk '{print $1}' | tr 'A-F' 'a-f')"
else
  actual="$(node -e 'const c=require("crypto"),f=require("fs");process.stdout.write(c.createHash("sha256").update(f.readFileSync(process.argv[1])).digest("hex"))' "$BACKUP_PATH")"
fi
test "$recorded" = "$actual"
`
}
