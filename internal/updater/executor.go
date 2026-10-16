package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	control "github.com/caigee-cmd/cli2api/internal/update"
	"github.com/caigee-cmd/cli2api/internal/updatenet"
)

type ExecutorConfig struct {
	ComposeFile     string
	EnvFile         string
	ServiceName     string
	ContainerName   string
	ImageRepository string
	HealthURL       string
	HealthTimeout   time.Duration
	HostBinaryPath  string
	GitHubRepo      string
	GitHubToken     string
	NetworkFile     string
	RestartCommand  []string
}

type Executor struct {
	config   ExecutorConfig
	runner   commandRunner
	client   *http.Client
	download *http.Client
	fetch    func(context.Context, string, string) ([]byte, error)

	preserveMu        sync.Mutex
	preservedDatabase string
	networkMu         sync.Mutex
	network           updatenet.Status
}

type commandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

type containerInspect struct {
	Image  string `json:"Image"`
	Mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
	} `json:"Mounts"`
	NetworkSettings struct {
		Networks map[string]struct {
			Aliases []string `json:"Aliases"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

type dataMount struct {
	Type   string
	Name   string
	Source string
}

func NewExecutor(config ExecutorConfig) *Executor {
	if config.ServiceName == "" {
		config.ServiceName = "qoder-api-proxy"
	}
	if config.ContainerName == "" {
		config.ContainerName = "qoder-api-proxy"
	}
	if config.ImageRepository == "" {
		config.ImageRepository = "ghcr.io/cokeeven/ali2api3"
	}
	if config.HealthURL == "" {
		config.HealthURL = "http://127.0.0.1:3010/health"
	}
	if config.HealthTimeout <= 0 {
		config.HealthTimeout = 120 * time.Second
	}
	if strings.TrimSpace(config.GitHubRepo) == "" {
		config.GitHubRepo = "cokeeven/ali2api3"
	}
	return &Executor{
		config:   config,
		runner:   execCommandRunner{},
		client:   &http.Client{Timeout: 3 * time.Second},
		download: &http.Client{Timeout: 10 * time.Minute},
	}
}

func (e *Executor) Prepare(ctx context.Context, _ string, request control.PrepareRequest, progress func(string)) error {
	if err := e.validateConfig(); err != nil {
		return err
	}
	if _, err := control.ParseVersion(request.CurrentVersion); err != nil {
		return fmt.Errorf("invalid current version")
	}
	if _, err := control.ParseVersion(request.TargetVersion); err != nil {
		return fmt.Errorf("invalid target version")
	}
	progress("pulling")
	if err := e.pullTargetImage(ctx, request.TargetVersion); err != nil {
		return fmt.Errorf("pull target image: %w", err)
	}
	return e.stageHostBinary(ctx, request.TargetVersion, progress)
}

func (e *Executor) Apply(ctx context.Context, _ string, request ApplyRequest, progress func(string)) (bool, error) {
	e.setPreservedDatabase("")
	if err := e.validateConfig(); err != nil {
		return false, err
	}
	before, err := e.inspectContainer(ctx)
	if err != nil {
		return false, err
	}
	mount, err := findDataMount(before)
	if err != nil {
		return false, err
	}
	_, envMode, err := readEnvFile(e.config.EnvFile)
	if err != nil {
		return false, err
	}

	currentImage := e.config.ImageRepository + ":" + request.CurrentVersion
	targetImage := e.config.ImageRepository + ":" + request.TargetVersion
	progress("preparing")
	if _, err := e.runner.Run(ctx, "docker", "image", "tag", before.Image, currentImage); err != nil {
		return false, fmt.Errorf("preserve current image: %w", err)
	}
	if err := e.verifyBackupExists(ctx, mount, currentImage, request.BackupPath); err != nil {
		return false, err
	}
	if request.SkipPull {
		progress("image_ready")
		if _, err := e.runner.Run(ctx, "docker", "image", "inspect", targetImage); err != nil {
			return false, fmt.Errorf("prepared image is unavailable: %w", err)
		}
		if err := e.stageHostBinary(ctx, request.TargetVersion, progress); err != nil {
			return false, err
		}
	} else {
		progress("pulling")
		if err := e.pullTargetImage(ctx, request.TargetVersion); err != nil {
			return false, err
		}
		if err := e.stageHostBinary(ctx, request.TargetVersion, progress); err != nil {
			return false, err
		}
	}
	if err := setEnvValueAtomic(e.config.EnvFile, envMode, "CLI2API_IMAGE", targetImage); err != nil {
		return false, err
	}

	progress("recreating")
	if err := e.compose(ctx, "up", "-d", "--no-deps", "--force-recreate", e.config.ServiceName); err != nil {
		return e.rollback(request, before, mount, currentImage, envMode, err, progress)
	}
	after, err := e.inspectContainer(ctx)
	if err != nil {
		return e.rollback(request, before, mount, currentImage, envMode, err, progress)
	}
	if err := e.restoreNetworks(ctx, before, after); err != nil {
		return e.rollback(request, before, mount, currentImage, envMode, err, progress)
	}
	progress("checking")
	if err := e.waitForVersion(ctx, request.TargetVersion); err != nil {
		return e.rollback(request, before, mount, currentImage, envMode, err, progress)
	}
	afterMount, err := findDataMount(after)
	if err != nil || mountIdentity(afterMount) != mountIdentity(mount) {
		if err == nil {
			err = fmt.Errorf("/data mount changed during update")
		}
		return e.rollback(request, before, mount, currentImage, envMode, err, progress)
	}
	if err := e.refreshStagedHostBinaryFromContainer(ctx); err != nil && strings.TrimSpace(e.config.HostBinaryPath) != "" {
		if _, statErr := os.Stat(e.config.HostBinaryPath + ".new"); statErr != nil {
			return e.rollback(request, before, mount, currentImage, envMode, fmt.Errorf("copy host updater from container: %w", err), progress)
		}
	}
	if err := e.CommitHostBinary(); err != nil {
		return e.rollback(request, before, mount, currentImage, envMode, err, progress)
	}
	return false, nil
}

func (e *Executor) rollback(request ApplyRequest, before containerInspect, mount dataMount, currentImage string, envMode os.FileMode, cause error, progress func(string)) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	progress("rolling_back")
	e.discardStagedHostBinary()
	if err := e.compose(ctx, "stop", e.config.ServiceName); err != nil {
		return false, rollbackFailed(cause, err)
	}
	// Program-only rollback preserves all current data. Append-only migration
	// files do not guarantee backward-compatible schemas; health checks must
	// confirm that the previous program can actually run. Replacing
	// the database is an explicit operator choice, and even then the
	// post-upgrade database is preserved first — if that preservation cannot
	// be proven, the rollback stops instead of overwriting newer data.
	if request.RestoreDatabase {
		preservedPath, err := e.preserveDatabase(ctx, mount, currentImage, request)
		if err != nil {
			return false, rollbackFailed(cause, fmt.Errorf("preserve post-upgrade database before restore: %w", err))
		}
		e.setPreservedDatabase(preservedPath)
		if err := e.restoreSQLite(ctx, mount, currentImage, request.BackupPath); err != nil {
			return false, rollbackFailed(cause, err)
		}
	}
	if err := setEnvValueAtomic(e.config.EnvFile, envMode, "CLI2API_IMAGE", currentImage); err != nil {
		return false, rollbackFailed(cause, err)
	}
	if err := e.compose(ctx, "up", "-d", "--no-deps", "--force-recreate", e.config.ServiceName); err != nil {
		return false, rollbackFailed(cause, err)
	}
	after, err := e.inspectContainer(ctx)
	if err != nil {
		return false, rollbackFailed(cause, err)
	}
	if err := e.restoreNetworks(ctx, before, after); err != nil {
		return false, rollbackFailed(cause, err)
	}
	if err := e.waitForVersion(ctx, request.CurrentVersion); err != nil {
		return false, rollbackFailed(cause, err)
	}
	return true, fmt.Errorf("update failed and was rolled back: %w", cause)
}

func rollbackFailed(cause, rollbackErr error) error {
	return fmt.Errorf("update failed: %v; rollback failed: %w", cause, rollbackErr)
}

// verifyBackupExists proves the pre-upgrade snapshot is usable before the
// upgrade touches anything: the file must exist and be non-empty, its
// integrity manifest must accompany it, and the recorded SHA-256 must match
// the file. A missing or mismatched record stops the upgrade instead of
// proceeding on an unverifiable backup. The record is written by the app when
// it takes the snapshot (see store.Backup).
func (e *Executor) verifyBackupExists(ctx context.Context, mount dataMount, image, backupPath string) error {
	script := `set -eu
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
	_, err := e.runner.Run(ctx, "docker", "run", "--rm", "-v", dockerMountArg(mount), "-e", "BACKUP_PATH="+backupPath, "--entrypoint", "/bin/sh", image, "-c", script)
	if err != nil {
		return fmt.Errorf("verify sqlite backup integrity: %w", err)
	}
	return nil
}

// preserveDatabase copies the live database aside before an explicit rollback
// restore overwrites it, and refuses to report success unless the copy is
// byte-identical, non-empty, and hashed. The updater must abort the rollback
// when this fails, because the post-upgrade data would otherwise be lost.
func (e *Executor) preserveDatabase(ctx context.Context, mount dataMount, image string, request ApplyRequest) (string, error) {
	name := fmt.Sprintf("qoder-pre-rollback-%s-%s.db", sanitizeVersion(request.TargetVersion), time.Now().UTC().Format("20060102T150405.000000000Z"))
	target := path.Join("/data/backups", name)
	script := `set -eu
SRC=/data/qoder.db
DEST="$PRESERVE_PATH"
test -f "$SRC"
test -s "$SRC"
mkdir -p /data/backups
# The service is stopped, but a killed SQLite writer may leave committed data
# in WAL or a rollback journal. Keep the recoverable set, not just qoder.db.
# SHM is an index and SQLite can rebuild it from the preserved WAL.
for suffix in '' -wal -journal; do
  test -f "$SRC$suffix" || continue
  test ! -e "$DEST$suffix"
  test ! -e "$DEST$suffix.sha256"
  cp "$SRC$suffix" "$DEST$suffix"
  chmod 600 "$DEST$suffix"
  cmp "$SRC$suffix" "$DEST$suffix"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$DEST$suffix" | awk '{print $1}' | tr 'A-F' 'a-f' > "$DEST$suffix.sha256"
  else
    node -e 'const c=require("crypto"),f=require("fs");process.stdout.write(c.createHash("sha256").update(f.readFileSync(process.argv[1])).digest("hex"))' "$DEST$suffix" > "$DEST$suffix.sha256"
  fi
  chmod 600 "$DEST$suffix.sha256"
  test -s "$DEST$suffix.sha256"
done
sync`
	_, err := e.runner.Run(ctx, "docker", "run", "--rm", "-v", dockerMountArg(mount), "-e", "PRESERVE_PATH="+target, "--entrypoint", "/bin/sh", image, "-c", script)
	if err != nil {
		return "", err
	}
	return target, nil
}

func sanitizeVersion(version string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '.', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, version)
	cleaned = strings.Trim(cleaned, "-")
	if cleaned == "" {
		return "unknown"
	}
	return cleaned
}

func (e *Executor) setPreservedDatabase(path string) {
	if e == nil {
		return
	}
	e.preserveMu.Lock()
	e.preservedDatabase = path
	e.preserveMu.Unlock()
}

// LastPreservedDatabase reports where the most recent explicit rollback stored
// the post-upgrade database, so the console can tell the operator that the
// newest data still exists.
func (e *Executor) LastPreservedDatabase() string {
	if e == nil {
		return ""
	}
	e.preserveMu.Lock()
	defer e.preserveMu.Unlock()
	return e.preservedDatabase
}

func (e *Executor) restoreSQLite(ctx context.Context, mount dataMount, image, backupPath string) error {
	script := `set -eu
test -f "$BACKUP_PATH"
cp "$BACKUP_PATH" /data/qoder.db.restore
chmod 600 /data/qoder.db.restore
mv /data/qoder.db.restore /data/qoder.db
rm -f /data/qoder.db-wal /data/qoder.db-shm /data/qoder.db-journal
sync`
	_, err := e.runner.Run(ctx, "docker", "run", "--rm", "-v", dockerMountArg(mount), "-e", "BACKUP_PATH="+backupPath, "--entrypoint", "/bin/sh", image, "-c", script)
	if err != nil {
		return fmt.Errorf("restore sqlite backup: %w", err)
	}
	return nil
}

func (e *Executor) compose(ctx context.Context, args ...string) error {
	commandArgs := []string{"compose", "--env-file", e.config.EnvFile, "-f", e.config.ComposeFile}
	commandArgs = append(commandArgs, args...)
	_, err := e.runner.Run(ctx, "docker", commandArgs...)
	return err
}

func (e *Executor) inspectContainer(ctx context.Context) (containerInspect, error) {
	output, err := e.runner.Run(ctx, "docker", "inspect", e.config.ContainerName)
	if err != nil {
		return containerInspect{}, err
	}
	var containers []containerInspect
	if err := json.Unmarshal(output, &containers); err != nil {
		return containerInspect{}, fmt.Errorf("decode container inspect: %w", err)
	}
	if len(containers) != 1 || strings.TrimSpace(containers[0].Image) == "" {
		return containerInspect{}, fmt.Errorf("container %s not found", e.config.ContainerName)
	}
	return containers[0], nil
}

func (e *Executor) restoreNetworks(ctx context.Context, before, after containerInspect) error {
	names := make([]string, 0, len(before.NetworkSettings.Networks))
	for name := range before.NetworkSettings.Networks {
		if _, exists := after.NetworkSettings.Networks[name]; !exists {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	for _, name := range names {
		args := []string{"network", "connect"}
		aliases := uniqueSorted(before.NetworkSettings.Networks[name].Aliases)
		for _, alias := range aliases {
			args = append(args, "--alias", alias)
		}
		args = append(args, name, e.config.ContainerName)
		if _, err := e.runner.Run(ctx, "docker", args...); err != nil {
			return fmt.Errorf("restore docker network %s: %w", name, err)
		}
	}
	return nil
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func findDataMount(container containerInspect) (dataMount, error) {
	for _, mount := range container.Mounts {
		if mount.Destination == "/data" {
			if mount.Type == "volume" && mount.Name != "" {
				return dataMount{Type: mount.Type, Name: mount.Name, Source: mount.Source}, nil
			}
			if mount.Type == "bind" && mount.Source != "" {
				return dataMount{Type: mount.Type, Source: mount.Source}, nil
			}
		}
	}
	return dataMount{}, fmt.Errorf("container has no persistent /data mount")
}

func mountIdentity(mount dataMount) string {
	if mount.Type == "volume" {
		return "volume:" + mount.Name
	}
	return "bind:" + filepath.Clean(mount.Source)
}

func dockerMountArg(mount dataMount) string {
	if mount.Type == "volume" {
		return mount.Name + ":/data"
	}
	return mount.Source + ":/data"
}

func (e *Executor) waitForVersion(ctx context.Context, expected string) error {
	deadline := time.Now().Add(e.config.HealthTimeout)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.config.HealthURL, nil)
		if err == nil {
			resp, requestErr := e.client.Do(req)
			if requestErr == nil {
				var health struct {
					OK      bool   `json:"ok"`
					Version string `json:"version"`
				}
				decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&health)
				_ = resp.Body.Close()
				if resp.StatusCode < 300 && decodeErr == nil && health.OK && health.Version == expected {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("health check did not reach version %s", expected)
}

func (e *Executor) validateConfig() error {
	for name, value := range map[string]string{
		"compose file":     e.config.ComposeFile,
		"env file":         e.config.EnvFile,
		"image repository": e.config.ImageRepository,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s required", name)
		}
	}
	for name, value := range map[string]string{
		"compose file": e.config.ComposeFile,
		"env file":     e.config.EnvFile,
	} {
		if !filepath.IsAbs(value) {
			return fmt.Errorf("%s must be absolute", name)
		}
	}
	return nil
}

func readEnvFile(path string) ([]byte, os.FileMode, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, fmt.Errorf("stat env file: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("read env file: %w", err)
	}
	return data, info.Mode().Perm(), nil
}

func setEnvValueAtomic(path string, mode os.FileMode, key, value string) error {
	data, _, err := readEnvFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	prefix := key + "="
	found := false
	for index, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			lines[index] = prefix + value
			found = true
		}
	}
	if !found {
		for len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		lines = append(lines, prefix+value, "")
	}
	return writeEnvFileAtomic(path, mode, []byte(strings.Join(lines, "\n")))
}

func writeEnvFileAtomic(path string, mode os.FileMode, data []byte) error {
	directory := filepath.Dir(path)
	temp, err := os.CreateTemp(directory, ".cli2api-env-*")
	if err != nil {
		return fmt.Errorf("create env temp file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if mode == 0 {
		mode = 0o600
	}
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := io.Copy(temp, bytes.NewReader(data)); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace env file: %w", err)
	}
	return nil
}
