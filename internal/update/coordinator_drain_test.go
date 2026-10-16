package update

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

type drainTestAgent struct{ calls *[]string }

func (a drainTestAgent) Status(context.Context) (AgentStatus, error) { return AgentStatus{}, nil }
func (a drainTestAgent) Prepare(context.Context, PrepareRequest) (ApplyResponse, error) {
	return ApplyResponse{}, nil
}
func (a drainTestAgent) Apply(context.Context, ApplyRequest) (ApplyResponse, error) {
	*a.calls = append(*a.calls, "apply")
	return ApplyResponse{}, errors.New("stop test after submission")
}
func (a drainTestAgent) ApplyPrepared(ctx context.Context, request ApplyRequest) (ApplyResponse, error) {
	return a.Apply(ctx, request)
}

func TestBothUpdatePathsRequireSuccessfulDrainBeforeBackup(t *testing.T) {
	for _, staged := range []bool{false, true} {
		for _, drained := range []bool{false, true} {
			name := "one-shot"
			if staged {
				name = "staged"
			}
			if drained {
				name += "-idle"
			} else {
				name += "-timeout"
			}
			t.Run(name, func(t *testing.T) {
				var calls []string
				c := &Coordinator{DataDir: t.TempDir(), Job: &Job{JobID: "job", CurrentVersion: "v1.0.0", TargetVersion: "v1.0.1"}}
				c.Agent = drainTestAgent{calls: &calls}
				c.Maintenance.Store(true)
				c.Running.Store(true)
				c.Drain = func(context.Context) bool { calls = append(calls, "drain"); return drained }
				c.Backup = func(context.Context, string, int) (accounts.Backup, error) {
					calls = append(calls, "backup")
					return accounts.Backup{Name: "snapshot.db"}, nil
				}
				if staged {
					c.applyPrepared("job")
				} else {
					c.submitOneShot("job", "v1.0.0", "v1.0.1")
				}
				want := []string{"drain"}
				if drained {
					want = append(want, "backup", "apply")
				}
				if !reflect.DeepEqual(calls, want) {
					t.Fatalf("calls=%v want=%v", calls, want)
				}
				if !drained {
					job := c.Snapshot()
					if job.State != "failed" || !job.DrainTimedOut || c.Maintenance.Load() || c.Running.Load() {
						t.Fatalf("timeout did not stop update and release maintenance: %+v", job)
					}
				}
			})
		}
	}
}
