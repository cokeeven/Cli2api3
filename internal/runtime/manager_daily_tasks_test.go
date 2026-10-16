package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
	sqlstore "github.com/caigee-cmd/cli2api/internal/store"
)

type dailyTaskFunc func(context.Context, string, []providers.DailyTaskStep) (*providers.DailyTasksReport, error)

func (f dailyTaskFunc) RunDailyTasksReport(ctx context.Context, id string, previous []providers.DailyTaskStep) (*providers.DailyTasksReport, error) {
	return f(ctx, id, previous)
}
func (f dailyTaskFunc) DailyCheckin(context.Context, string) (string, error)  { return "", nil }
func (f dailyTaskFunc) Keepalive(context.Context, string) error               { return nil }
func (f dailyTaskFunc) RunDailyTasks(context.Context, string) (string, error) { return "", nil }

func newDailyTaskManager(t *testing.T, runner dailyTaskFunc) (*Manager, Account) {
	t.Helper()
	store, err := sqlstore.OpenStore(filepath.Join(t.TempDir(), "daily.db"))
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(ManagerConfig{DataDir: t.TempDir()}, store, nil)
	m.SetWorkBuddy(runner)
	t.Cleanup(func() { _ = m.Close(); _ = store.Close() })
	enabled := true
	a, err := store.Create(context.Background(), accounts.CreateAccount{Name: "test", Provider: "workbuddy", Region: "cn", Enabled: true, AutoCheckin: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	return m, a
}

func TestRestoredDailyTaskRunPersistsAndDeduplicates(t *testing.T) {
	var calls atomic.Int64
	m, a := newDailyTaskManager(t, func(_ context.Context, id string, _ []providers.DailyTaskStep) (*providers.DailyTasksReport, error) {
		calls.Add(1)
		r := &providers.DailyTasksReport{AccountID: id}
		r.Add("checkin", "success", "confirmed")
		return r, nil
	})
	run, err := m.runDailyTasksDetailed(context.Background(), a.ID, "scheduled")
	if err != nil || run.Status != "success" {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	_, err = m.runDailyTasksDetailed(context.Background(), a.ID, "scheduled")
	if !errors.Is(err, accounts.ErrDailyTasksNotDue) || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
	runs, err := m.store.(accounts.DailyTaskStore).ListDailyTaskRuns(context.Background(), a.ID, 20)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	updated, err := m.store.Get(context.Background(), a.ID)
	if err != nil || updated.LastCheckinStatus != "success" {
		t.Fatalf("account=%+v err=%v", updated, err)
	}
}

func TestRestoredDailyTaskCancellationPersistsAndExcludesConcurrentRun(t *testing.T) {
	entered := make(chan struct{})
	m, a := newDailyTaskManager(t, func(ctx context.Context, _ string, _ []providers.DailyTaskStep) (*providers.DailyTasksReport, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan accounts.DailyTaskRun, 1)
	go func() { run, _ := m.RunDailyTasksDetailedAccount(ctx, a.ID); done <- run }()
	<-entered
	if _, err := m.RunDailyTasksDetailedAccount(context.Background(), a.ID); !errors.Is(err, accounts.ErrDailyTasksBusy) {
		t.Fatalf("overlap err=%v", err)
	}
	cancel()
	select {
	case run := <-done:
		if run.Status != "error" || run.FinishedAt == nil {
			t.Fatalf("run=%+v", run)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled task did not finish")
	}
	runs, err := m.store.(accounts.DailyTaskStore).ListDailyTaskRuns(context.Background(), a.ID, 20)
	if err != nil || len(runs) != 1 || runs[0].Status != "error" {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
}

func TestRestoredAutomaticMaintenanceSwitchCancelsAutomaticOnly(t *testing.T) {
	var calls atomic.Int64
	m, a := newDailyTaskManager(t, func(_ context.Context, id string, _ []providers.DailyTaskStep) (*providers.DailyTasksReport, error) {
		calls.Add(1)
		r := &providers.DailyTasksReport{AccountID: id}
		r.Add("growth", "already", "confirmed")
		return r, nil
	})
	ctx, cancel, enabled := m.automaticMaintenanceContext(context.Background())
	defer cancel()
	if !enabled {
		t.Fatal("default switch is disabled")
	}
	if err := m.store.SetSecret(context.Background(), accounts.AutomationMaintenanceEnabledSecret, "0"); err != nil {
		t.Fatal(err)
	}
	if err := m.ReloadAutomationMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("automatic context not canceled")
	}
	m.runDailyWorkBuddyBatch(context.Background(), true)
	if calls.Load() != 0 {
		t.Fatal("disabled automation called provider")
	}
	run, err := m.RunDailyTasksDetailedAccount(context.Background(), a.ID)
	if err != nil || run.Status != "success" || calls.Load() != 1 {
		t.Fatalf("manual run=%+v err=%v calls=%d", run, err, calls.Load())
	}
}
