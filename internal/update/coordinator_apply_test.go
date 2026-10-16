package update

import (
	"context"
	"errors"
	"github.com/caigee-cmd/cli2api/internal/accounts"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type applyClaimAgent struct{ calls atomic.Int32 }

func (a *applyClaimAgent) Status(context.Context) (AgentStatus, error) {
	return AgentStatus{Available: true, State: "ready_to_apply", JobID: "host-job", CurrentVersion: "v1.0.0", TargetVersion: "v1.0.1"}, nil
}
func (a *applyClaimAgent) Apply(context.Context, ApplyRequest) (ApplyResponse, error) {
	a.calls.Add(1)
	return ApplyResponse{}, errors.New("stop after submission")
}
func (a *applyClaimAgent) ApplyPrepared(ctx context.Context, r ApplyRequest) (ApplyResponse, error) {
	return a.Apply(ctx, r)
}
func (a *applyClaimAgent) Prepare(context.Context, PrepareRequest) (ApplyResponse, error) {
	return ApplyResponse{}, nil
}

func TestConcurrentApplyClaimsPreparedJobOnlyOnce(t *testing.T) {
	const count = 32
	agent := &applyClaimAgent{}
	c := &Coordinator{Agent: agent, DataDir: t.TempDir(), Job: &Job{JobID: "job", AgentJobID: "host-job", State: "ready_to_apply", CurrentVersion: "v1.0.0", TargetVersion: "v1.0.1"}}
	c.Running.Store(true)
	entered, release := make(chan struct{}), make(chan struct{})
	var enterOnce sync.Once
	var backups atomic.Int32
	c.Drain = func(context.Context) bool { enterOnce.Do(func() { close(entered) }); <-release; return true }
	c.Backup = func(context.Context, string, int) (accounts.Backup, error) {
		backups.Add(1)
		return accounts.Backup{Name: "snapshot.db"}, nil
	}
	first := httptest.NewRecorder()
	c.HandleApply(first, httptest.NewRequest(http.MethodPost, "/api/system/update/apply", nil))
	if first.Code != http.StatusAccepted {
		t.Fatalf("first apply=%d", first.Code)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("drain did not start")
	}
	var wg sync.WaitGroup
	var accepted atomic.Int32
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			c.HandleApply(rec, httptest.NewRequest(http.MethodPost, "/api/system/update/apply", nil))
			if rec.Code == http.StatusAccepted {
				accepted.Add(1)
			} else if rec.Code != http.StatusConflict {
				t.Errorf("unexpected status %d", rec.Code)
			}
		}()
	}
	wg.Wait()
	job := c.Snapshot()
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for c.Running.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if accepted.Load() != 0 {
		t.Errorf("accepted %d duplicate confirmations while draining", accepted.Load())
	}
	if job.State != "backing_up" {
		t.Errorf("stale host status changed active drain to %s", job.State)
	}
	if backups.Load() != 1 || agent.calls.Load() != 1 {
		t.Errorf("backups=%d applies=%d", backups.Load(), agent.calls.Load())
	}
}

type delayedPrepareStatusAgent struct {
	applyClaimAgent
	statusCalls      atomic.Int32
	started, release chan struct{}
}

func (a *delayedPrepareStatusAgent) Status(ctx context.Context) (AgentStatus, error) {
	if a.statusCalls.Add(1) == 1 {
		close(a.started)
		<-a.release
	}
	return a.applyClaimAgent.Status(ctx)
}

func TestLatePreparationMonitorCannotReopenActiveApply(t *testing.T) {
	agent := &delayedPrepareStatusAgent{started: make(chan struct{}), release: make(chan struct{})}
	c := &Coordinator{Agent: agent, DataDir: t.TempDir(), Job: &Job{JobID: "job", AgentJobID: "host-job", State: "preparing_image", CurrentVersion: "v1.0.0", TargetVersion: "v1.0.1"}}
	c.Running.Store(true)
	monitorDone := make(chan struct{})
	go func() { c.monitorPrepared("job", "host-job"); close(monitorDone) }()
	<-agent.started
	ready, _ := agent.applyClaimAgent.Status(context.Background())
	c.adoptPrepared(ready) // The console observed readiness before the monitor returned.
	drainStarted, drainRelease := make(chan struct{}), make(chan struct{})
	var enterOnce sync.Once
	c.Drain = func(context.Context) bool { enterOnce.Do(func() { close(drainStarted) }); <-drainRelease; return true }
	c.Backup = func(context.Context, string, int) (accounts.Backup, error) {
		return accounts.Backup{}, errors.New("stop after drain")
	}
	first := httptest.NewRecorder()
	c.HandleApply(first, httptest.NewRequest(http.MethodPost, "/api/system/update/apply", nil))
	if first.Code != http.StatusAccepted {
		t.Fatalf("first apply=%d", first.Code)
	}
	<-drainStarted
	close(agent.release)
	<-monitorDone
	job := c.Snapshot()
	second := httptest.NewRecorder()
	c.HandleApply(second, httptest.NewRequest(http.MethodPost, "/api/system/update/apply", nil))
	close(drainRelease)
	deadline := time.Now().Add(3 * time.Second)
	for c.Running.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if job.State != "backing_up" || second.Code != http.StatusConflict {
		t.Fatalf("late preparation reopened install: state=%s second=%d", job.State, second.Code)
	}
}
