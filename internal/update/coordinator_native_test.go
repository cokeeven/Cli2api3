package update

import (
	"context"
	"github.com/caigee-cmd/cli2api/internal/accounts"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestNativeDrainTimeoutDoesNotBackupOrInstall(t *testing.T) {
	calls := []string{}
	c := &Coordinator{ExpectedBackend: "windows-native", DataDir: t.TempDir(), Agent: drainTestAgent{calls: &calls}, Job: &Job{JobID: "native-drain", State: "ready_to_apply", CurrentVersion: "v1.0.0", TargetVersion: "v1.0.1"}}
	c.Running.Store(true)
	c.Maintenance.Store(true)
	c.Drain = func(context.Context) bool { return false }
	c.Backup = func(context.Context, string, int) (accounts.Backup, error) {
		t.Fatal("backup started after drain timeout")
		return accounts.Backup{}, nil
	}
	c.applyPrepared("native-drain")
	if len(calls) != 0 || c.Running.Load() || c.Maintenance.Load() || !c.Snapshot().DrainTimedOut {
		t.Fatal("native drain timeout did not stop installation")
	}
}
func TestNativeBackendRejectsDockerAndKeepsNativeBackupLocation(t *testing.T) {
	c := &Coordinator{ExpectedBackend: "windows-native", Agent: &applyClaimAgent{}}
	rec := httptest.NewRecorder()
	c.HandleApply(rec, httptest.NewRequest(http.MethodPost, "/api/system/update/apply", nil))
	if rec.Code != 409 || c.Maintenance.Load() {
		t.Fatal("native gateway accepted a Docker agent", rec.Code)
	}
	backup := accounts.Backup{Name: "snapshot.db", Path: filepath.Join(t.TempDir(), "backups", "snapshot.db")}
	if c.agentBackupPath(backup) != backup.Path {
		t.Fatal("native backup path was mapped into a Docker volume")
	}
	if err := (&Coordinator{}).checkBackend(AgentStatus{ProtocolVersion: 1}); err != nil {
		t.Fatal("legacy Docker protocol compatibility lost", err)
	}
}
