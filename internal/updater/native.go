package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caigee-cmd/cli2api/internal/store"
	control "github.com/caigee-cmd/cli2api/internal/update"
	"github.com/caigee-cmd/cli2api/internal/updatenet"
)

type nativeFailure struct {
	State string
	Err   error
}

func (e *nativeFailure) Error() string { return e.Err.Error() }
func (e *nativeFailure) Unwrap() error { return e.Err }

type NativeConfig struct {
	ProgramDir           string `json:"program_dir"`
	DataDir              string `json:"data_dir"`
	StateDir             string `json:"state_dir"`
	HealthURL            string `json:"health_url"`
	InstanceID           string `json:"instance_id"`
	Manager              string `json:"manager"` // task or process (isolated rehearsal)
	TargetName           string `json:"target_name"`
	Launcher             string `json:"launcher"`
	LaunchConfig         string `json:"launch_config"`
	HealthTimeoutSeconds int    `json:"health_timeout_seconds"`
	StopTimeoutSeconds   int    `json:"stop_timeout_seconds"`
}

type nativeHealth struct {
	OK         bool   `json:"ok"`
	Version    string `json:"version"`
	Commit     string `json:"commit"`
	InstanceID string `json:"instance_id"`
	PID        int    `json:"pid"`
}
type nativeJournal struct {
	ID      string          `json:"id"`
	Phase   string          `json:"phase"`
	Request ApplyRequest    `json:"request"`
	Before  nativeHealth    `json:"before"`
	Stage   string          `json:"stage"`
	Old     map[string]bool `json:"old"`
	Error   string          `json:"error,omitempty"`
}

type NativeExecutor struct {
	config    NativeConfig
	transport *Executor
	token     string
	mu        sync.Mutex
	closeLock func()
}

func NewNativeExecutor(c NativeConfig, token, networkFile string) (*NativeExecutor, error) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		return nil, fmt.Errorf("native updates support Windows amd64 only")
	}
	c.ProgramDir = filepath.Clean(c.ProgramDir)
	c.DataDir = filepath.Clean(c.DataDir)
	c.StateDir = filepath.Clean(c.StateDir)
	c.Launcher = filepath.Clean(c.Launcher)
	c.LaunchConfig = filepath.Clean(c.LaunchConfig)
	for name, p := range map[string]string{"program_dir": c.ProgramDir, "data_dir": c.DataDir, "state_dir": c.StateDir, "launcher": c.Launcher, "launch_config": c.LaunchConfig} {
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("%s must be absolute", name)
		}
		if err := noLinks(p); err != nil {
			return nil, err
		}
	}
	if pathsOverlap(c.DataDir, c.ProgramDir) || pathsOverlap(c.DataDir, c.StateDir) || pathsOverlap(c.StateDir, c.ProgramDir) {
		return nil, fmt.Errorf("program, data and updater state directories must be separate")
	}
	u, e := url.Parse(c.HealthURL)
	if e != nil || u.Scheme != "http" || !loopbackHost(u.Hostname()) || u.Path != "/health" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, fmt.Errorf("native health URL must be loopback HTTP /health")
	}
	if len(c.InstanceID) < 20 || len(token) < 32 {
		return nil, fmt.Errorf("native instance identity and updater token required")
	}
	if c.Manager != "task" && c.Manager != "process" {
		return nil, fmt.Errorf("invalid native process manager")
	}
	if c.Manager != "process" && strings.TrimSpace(c.TargetName) == "" {
		return nil, fmt.Errorf("native task/service name required")
	}
	if c.HealthTimeoutSeconds <= 0 {
		c.HealthTimeoutSeconds = 120
	}
	if err := validateNativeLaunch(c, token); err != nil {
		return nil, err
	}
	if c.StopTimeoutSeconds <= 0 {
		c.StopTimeoutSeconds = 30
	}
	if err := os.MkdirAll(c.StateDir, 0700); err != nil {
		return nil, err
	}
	closeLock, err := acquireNativeLock(c.StateDir)
	if err != nil {
		return nil, err
	}
	ownerPath := filepath.Join(c.ProgramDir, ".cli2api-update", "owner.lock")
	if err := os.MkdirAll(filepath.Dir(ownerPath), 0700); err != nil {
		closeLock()
		return nil, err
	}
	releaseProgram, err := acquireNativePathLock(ownerPath)
	if err != nil {
		closeLock()
		return nil, fmt.Errorf("another updater owns this program directory: %w", err)
	}
	release := func() { releaseProgram(); closeLock() }
	return &NativeExecutor{config: c, token: token, closeLock: release, transport: NewExecutor(ExecutorConfig{NetworkFile: networkFile})}, nil
}

func loopbackHost(h string) bool { return h == "127.0.0.1" || h == "localhost" || h == "::1" }
func pathsOverlap(a, b string) bool {
	a = strings.ToLower(filepath.Clean(a))
	b = strings.ToLower(filepath.Clean(b))
	return a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator))
}
func (e *NativeExecutor) Backend() string                 { return NativeBackend }
func (e *NativeExecutor) NetworkStatus() updatenet.Status { return e.transport.NetworkStatus() }
func (e *NativeExecutor) stagePath(version string) string {
	return filepath.Join(e.config.ProgramDir, ".cli2api-update", "stage", version)
}
func (e *NativeExecutor) journalPath() string {
	return filepath.Join(e.config.StateDir, "transaction.json")
}
func saveNativeJSON(p string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	return writeEnvFileAtomic(p, 0600, append(b, '\n'))
}
func (e *NativeExecutor) ValidateBackupPath(p string) error {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p || !strings.EqualFold(filepath.Dir(p), filepath.Join(e.config.DataDir, "backups")) || !backupNamePattern.MatchString(filepath.Base(p)) {
		return fmt.Errorf("native backup must be a snapshot in the configured data/backups directory")
	}
	return noLinks(p)
}

func (e *NativeExecutor) Prepare(ctx context.Context, id string, r control.PrepareRequest, progress func(string)) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := validatePrepareRequest(r); err != nil {
		return err
	}
	return e.prepareLocked(ctx, id, r, progress)
}
func (e *NativeExecutor) prepareLocked(ctx context.Context, id string, r control.PrepareRequest, progress func(string)) error {
	if err := e.noUnfinishedTransaction(); err != nil {
		return err
	}
	progress("downloading")
	body, err := e.transport.downloadReleaseFileLimit(ctx, r.TargetVersion, NativeAsset, maxNativeBytes)
	if err != nil {
		return &nativeFailure{State: "download_failed", Err: fmt.Errorf("download application: %w", err)}
	}
	sums, err := e.transport.downloadReleaseFile(ctx, r.TargetVersion, NativeChecksums)
	if err != nil {
		return &nativeFailure{State: "download_failed", Err: err}
	}
	progress("verifying")
	if err = verifyHostBinaryChecksum(NativeAsset, body, sums); err != nil {
		return &nativeFailure{State: "verification_failed", Err: fmt.Errorf("application verification failed: %w", err)}
	}
	stage := e.stagePath(r.TargetVersion)
	if _, err = os.Stat(stage); err == nil {
		// Keep previous staging for diagnosis; do not trust or destructively reuse it.
		if err = os.Rename(stage, stage+".superseded-"+id); err != nil {
			return err
		}
	}
	if err = os.MkdirAll(filepath.Dir(stage), 0700); err != nil {
		return err
	}
	zipPath := stage + "-" + id + ".zip"
	if err = os.WriteFile(zipPath, body, 0600); err != nil {
		return err
	}
	progress("extracting")
	_, err = extractNative(zipPath, stage, r.TargetVersion)
	if err != nil {
		return &nativeFailure{State: "verification_failed", Err: err}
	}
	return nil
}

func (e *NativeExecutor) noUnfinishedTransaction() error {
	j, err := e.readJournal()
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if j.Phase != "succeeded" && j.Phase != "rolled_back" && j.Phase != "aborted" {
		return fmt.Errorf("unfinished native transaction requires recovery (%s)", j.Phase)
	}
	return nil
}
func (e *NativeExecutor) readJournal() (nativeJournal, error) {
	var j nativeJournal
	b, err := os.ReadFile(e.journalPath())
	if err == nil {
		err = json.Unmarshal(b, &j)
		if err == nil {
			err = e.validateJournal(j)
		}
	}
	return j, err
}
func (e *NativeExecutor) validateJournal(j nativeJournal) error {
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,95}$`).MatchString(j.ID) {
		return fmt.Errorf("invalid native recovery job identity")
	}
	if _, err := control.ParseVersion(j.Request.TargetVersion); err != nil {
		return fmt.Errorf("invalid native recovery version")
	}
	root := filepath.Join(e.config.ProgramDir, ".cli2api-update", "stage")
	if !filepath.IsAbs(j.Stage) || !strings.EqualFold(filepath.Dir(filepath.Clean(j.Stage)), root) {
		return fmt.Errorf("native recovery staging path escaped installation")
	}
	if filepath.Base(j.Stage) != j.Request.TargetVersion && !(strings.HasPrefix(j.ID, "bootstrap-") && filepath.Base(j.Stage) == j.ID) {
		return fmt.Errorf("native recovery staging identity mismatch")
	}
	if len(j.Old) != len(nativeRoots) {
		return fmt.Errorf("native recovery ownership record missing")
	}
	for _, name := range nativeRoots {
		if _, ok := j.Old[name]; !ok {
			return fmt.Errorf("native recovery component record missing")
		}
	}
	switch j.Phase {
	case "stopping", "replacing", "starting", "rolling_back", "succeeded", "rolled_back", "aborted", "recovery_failed":
	default:
		return fmt.Errorf("unknown native recovery phase")
	}
	return noLinks(j.Stage)
}
func (e *NativeExecutor) writeJournal(j *nativeJournal, phase string) error {
	j.Phase = phase
	return saveNativeJSON(e.journalPath(), j)
}

func (e *NativeExecutor) health(ctx context.Context) (nativeHealth, error) {
	var h nativeHealth
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.config.HealthURL, nil)
	if err != nil {
		return h, err
	}
	client := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return h, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return h, fmt.Errorf("health HTTP %d", resp.StatusCode)
	}
	if err = json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 64<<10)).Decode(&h); err != nil {
		return h, err
	}
	if !h.OK || h.InstanceID != e.config.InstanceID || h.PID <= 0 {
		return h, fmt.Errorf("health belongs to another instance")
	}
	if err = verifyNativeProcess(h.PID, filepath.Join(e.config.ProgramDir, "qoder-api-proxy.exe")); err != nil {
		return h, err
	}
	return h, nil
}

func (e *NativeExecutor) waitHealth(ctx context.Context, version, commit string) error {
	deadline := time.Now().Add(time.Duration(e.config.HealthTimeoutSeconds) * time.Second)
	stablePID := 0
	var stableSince time.Time
	for time.Now().Before(deadline) {
		h, err := e.health(ctx)
		if err == nil && h.Version == version && h.Commit == commit {
			if stablePID != h.PID {
				stablePID = h.PID
				stableSince = time.Now()
			} else if time.Since(stableSince) >= time.Second {
				return nil
			}
		} else {
			stablePID = 0
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("instance did not reach version %s / commit %s", version, commit)
}

func (e *NativeExecutor) stop(ctx context.Context, pid int) error {
	if pid < 0 {
		return fmt.Errorf("multiple gateway processes use this executable; refusing to stop an ambiguous instance")
	}
	if pid > 0 {
		if err := verifyNativeProcess(pid, filepath.Join(e.config.ProgramDir, "qoder-api-proxy.exe")); err == nil {
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(e.config.HealthURL, "/health")+"/internal/update/shutdown", nil)
			req.Header.Set("Authorization", "Bearer "+e.token)
			resp, _ := (&http.Client{Timeout: 3 * time.Second}).Do(req)
			if resp != nil {
				resp.Body.Close()
			}
			deadline := time.Now().Add(time.Duration(e.config.StopTimeoutSeconds) * time.Second)
			for time.Now().Before(deadline) && nativeProcessAlive(pid) {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(100 * time.Millisecond):
				}
			}
			if nativeProcessAlive(pid) {
				if err := terminateNativeProcess(pid, filepath.Join(e.config.ProgramDir, "qoder-api-proxy.exe")); err != nil {
					return err
				}
			}
		} else if nativeProcessAlive(pid) {
			return err
		}
	}
	return controlNativeManager(ctx, e.config, false)
}

func (e *NativeExecutor) Apply(ctx context.Context, id string, r ApplyRequest, progress func(string)) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if r.RestoreDatabase {
		return false, fmt.Errorf("native updates never automatically restore an old database; use an explicit offline recovery procedure")
	}
	if err := e.noUnfinishedTransaction(); err != nil {
		return false, err
	}
	if err := e.ValidateBackupPath(r.BackupPath); err != nil {
		return false, err
	}
	progress("verifying")
	if _, err := new(store.Store).VerifyBackup(ctx, r.BackupPath); err != nil {
		return false, fmt.Errorf("backup verification failed: %w", err)
	}
	if !r.SkipPull {
		current, ce := control.ParseVersion(r.CurrentVersion)
		target, te := control.ParseVersion(r.TargetVersion)
		if ce != nil || te != nil || target.Compare(current) >= 0 {
			return false, fmt.Errorf("native upgrades require separate preparation and explicit confirmation")
		}
		if err := e.prepareLocked(ctx, id, control.PrepareRequest{CurrentVersion: r.CurrentVersion, TargetVersion: r.TargetVersion}, progress); err != nil {
			return false, err
		}
	}
	stage := e.stagePath(r.TargetVersion)
	m, err := verifyNativeTree(stage, r.TargetVersion)
	if err != nil {
		return false, err
	}
	before, err := e.health(ctx)
	if err != nil {
		return false, err
	}
	if before.Version != r.CurrentVersion {
		return false, fmt.Errorf("current version changed since preparation")
	}
	var installed NativeManifest
	if err := readNativeConfig(filepath.Join(e.config.ProgramDir, "native-manifest.json"), &installed); err != nil || installed.Format != 1 || installed.Backend != NativeBackend || installed.Version != before.Version || installed.Commit != before.Commit {
		return false, fmt.Errorf("current program lacks a matching managed manifest; complete first native onboarding")
	}

	old := map[string]bool{}
	for _, name := range nativeRoots {
		_, err := os.Stat(filepath.Join(e.config.ProgramDir, name))
		if err != nil && !os.IsNotExist(err) {
			return false, err
		}
		old[name] = err == nil
	}
	j := nativeJournal{ID: id, Request: r, Before: before, Stage: stage, Old: old}
	if err = e.writeJournal(&j, "stopping"); err != nil {
		return false, err
	}
	progress("stopping")
	if err = e.stop(ctx, before.PID); err != nil {
		return false, e.abort(&j, err)
	}
	if err = e.writeJournal(&j, "replacing"); err != nil {
		return false, err
	}
	progress("replacing")
	for _, name := range nativeRoots {
		dst := filepath.Join(e.config.ProgramDir, name)
		backup := filepath.Join(e.config.ProgramDir, ".cli2api-update", "rollback", id, name)
		if err = noLinks(dst); err != nil {
			return e.rollbackNative(&j, err, progress)
		}
		if old[name] {
			if err = os.MkdirAll(filepath.Dir(backup), 0700); err != nil {
				return e.rollbackNative(&j, err, progress)
			}
			if err = renameNative(ctx, dst, backup); err != nil {
				return e.rollbackNative(&j, err, progress)
			}
		}
		if err = renameNative(ctx, filepath.Join(stage, name), dst); err != nil {
			return e.rollbackNative(&j, err, progress)
		}
	}
	if err = e.writeJournal(&j, "starting"); err != nil {
		return e.rollbackNative(&j, err, progress)
	}
	progress("starting")
	if err = controlNativeManager(ctx, e.config, true); err != nil {
		return e.rollbackNative(&j, err, progress)
	}
	progress("checking")
	if err = e.waitHealth(ctx, m.Version, m.Commit); err != nil {
		return e.rollbackNative(&j, err, progress)
	}
	if err = e.writeJournal(&j, "succeeded"); err != nil {
		return false, err
	}
	return false, nil
}

func (e *NativeExecutor) abort(j *nativeJournal, cause error) error {
	j.Error = cause.Error()
	if err := e.writeJournal(j, "aborted"); err != nil {
		return fmt.Errorf("%v; persist abort: %w", cause, err)
	}
	return cause
}
func renameNative(ctx context.Context, from, to string) error {
	deadline := time.Now().Add(5 * time.Second)
	var err error
	for time.Now().Before(deadline) {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("file remains occupied or cannot be replaced: %w", err)
}

func (e *NativeExecutor) rollbackNative(j *nativeJournal, cause error, progress func(string)) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	progress("rolling_back")
	if err := e.writeJournal(j, "rolling_back"); err != nil {
		return false, err
	}
	h, healthErr := e.health(ctx)
	pid := 0
	if healthErr == nil {
		pid = h.PID
	} else {
		pid = findNativeProcess(filepath.Join(e.config.ProgramDir, "qoder-api-proxy.exe"))
	}
	if err := e.stop(ctx, pid); err != nil {
		return false, e.recoveryFailure(j, cause, err)
	}
	for _, name := range nativeRoots {
		dst := filepath.Join(e.config.ProgramDir, name)
		backup := filepath.Join(e.config.ProgramDir, ".cli2api-update", "rollback", j.ID, name)
		if err := noLinks(backup); err != nil {
			return false, e.recoveryFailure(j, cause, err)
		}
		if err := noLinks(dst); err != nil {
			return false, e.recoveryFailure(j, cause, err)
		}
		_, err := os.Stat(backup)
		hasBackup := err == nil
		_, stageErr := os.Stat(filepath.Join(j.Stage, name))
		if hasBackup || (!j.Old[name] && os.IsNotExist(stageErr)) {
			if _, err := os.Stat(dst); err == nil {
				failed := filepath.Join(e.config.ProgramDir, ".cli2api-update", "failed", j.ID+"-"+fmt.Sprint(time.Now().UnixNano()), name)
				if err = os.MkdirAll(filepath.Dir(failed), 0700); err != nil {
					return false, e.recoveryFailure(j, cause, err)
				}
				if err = renameNative(ctx, dst, failed); err != nil {
					return false, e.recoveryFailure(j, cause, err)
				}
			}
			if hasBackup {
				if err = renameNative(ctx, backup, dst); err != nil {
					return false, e.recoveryFailure(j, cause, err)
				}
			}
		}
	}
	if err := controlNativeManager(ctx, e.config, true); err != nil {
		return false, e.recoveryFailure(j, cause, err)
	}
	if err := e.waitHealth(ctx, j.Before.Version, j.Before.Commit); err != nil {
		return false, e.recoveryFailure(j, cause, fmt.Errorf("previous program may be incompatible with latest database; latest database retained: %w", err))
	}
	j.Error = cause.Error()
	if err := e.writeJournal(j, "rolled_back"); err != nil {
		return false, err
	}
	return true, fmt.Errorf("program rolled back; latest database retained: %w", cause)
}
func (e *NativeExecutor) recoveryFailure(j *nativeJournal, cause, recovery error) error {
	j.Error = fmt.Sprintf("%v; recovery failed: %v; latest database retained", cause, recovery)
	_ = e.writeJournal(j, "recovery_failed")
	return fmt.Errorf("%s", j.Error)
}

// Recover is called before opening the agent port. An interrupted install is
// reconciled from its journal; no installation step is blindly replayed.
func (e *NativeExecutor) Recover() (string, string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	j, err := e.readJournal()
	if os.IsNotExist(err) {
		return "", "", nil
	}
	if err != nil {
		return "recovery_failed", "", err
	}
	switch j.Phase {
	case "succeeded":
		return "succeeded", j.ID, nil
	case "rolled_back":
		return "rolled_back", j.ID, fmt.Errorf("%s", j.Error)
	case "aborted":
		return "failed", j.ID, fmt.Errorf("%s", j.Error)
	case "recovery_failed":
		return "recovery_failed", j.ID, fmt.Errorf("%s", j.Error)
	}
	ok, err := e.rollbackNative(&j, fmt.Errorf("updater interrupted during %s", j.Phase), func(string) {})
	if ok {
		return "rolled_back", j.ID, err
	}
	return "recovery_failed", j.ID, err
}

// The independent installer supervisor starts the verified updater component
// from a new hash-addressed directory after this process exits.
func (e *NativeExecutor) RestartHost() { osExit(0) }

func (e *NativeExecutor) Close() {
	if e.closeLock != nil {
		e.closeLock()
		e.closeLock = nil
	}
}

func (e *NativeExecutor) FailureState() string {
	j, err := e.readJournal()
	if err == nil && j.Phase == "recovery_failed" {
		return "recovery_failed"
	}
	return "failed"
}

func (e *NativeExecutor) RecoveryDetails() control.AgentStatus {
	j, err := e.readJournal()
	if err != nil {
		return control.AgentStatus{}
	}
	return control.AgentStatus{JobID: j.ID, ClientJobID: j.Request.ClientJobID, CurrentVersion: j.Request.CurrentVersion, TargetVersion: j.Request.TargetVersion, BackupPath: j.Request.BackupPath}
}

// A completed older transaction must not overwrite a newer prepared/cancelled
// status when the agent restarts before the next user confirmation.
func (e *NativeExecutor) RecoverForStatus(status control.AgentStatus) (string, string, error) {
	j, err := e.readJournal()
	terminal := err == nil && (j.Phase == "succeeded" || j.Phase == "rolled_back" || j.Phase == "aborted")
	if terminal && status.JobID != j.ID && (status.JobID != "" || status.FinishedAt != "") {
		return "", "", nil
	}
	return e.Recover()
}

func ValidateNativeListen(address string) error {
	host, port, err := net.SplitHostPort(address)
	n, e := strconv.Atoi(port)
	if err != nil || e != nil || n < 1 || n > 65535 || !loopbackHost(strings.ToLower(host)) {
		return fmt.Errorf("native updater requires an explicit loopback TCP listen address and a free nonzero port")
	}
	return nil
}

func pathWithin(p, root string) bool {
	p = strings.ToLower(filepath.Clean(p))
	root = strings.ToLower(filepath.Clean(root))
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}
