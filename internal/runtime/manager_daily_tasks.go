package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
)

func (m *Manager) RunDailyTasksDetailedAccount(ctx context.Context, id string) (accounts.DailyTaskRun, error) {
	return m.runDailyTasksDetailed(ctx, id, "manual")
}

func (m *Manager) runDailyTasksDetailed(ctx context.Context, id, trigger string) (accounts.DailyTaskRun, error) {
	var empty accounts.DailyTaskRun
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	m.mu.Lock()
	if m.runCtx.Err() != nil {
		m.mu.Unlock()
		return empty, errManagerClosed
	}
	if m.checkinRunning == nil {
		m.checkinRunning = make(map[string]bool)
	}
	if m.checkinRunning[id] {
		m.mu.Unlock()
		return empty, accounts.ErrDailyTasksBusy
	}
	m.checkinRunning[id] = true
	m.maintenanceDone.Add(1)
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.checkinRunning, id)
		m.mu.Unlock()
		m.maintenanceDone.Done()
	}()
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	stop := context.AfterFunc(m.runCtx, cancel)
	defer stop()
	account, err := m.store.Get(ctx, id)
	if err != nil {
		return empty, err
	}
	if account.Provider != "workbuddy" {
		return empty, providers.ErrUnsupported
	}
	store, ok := m.store.(accounts.DailyTaskStore)
	if !ok {
		return empty, fmt.Errorf("daily task persistence unavailable")
	}
	runner, ok := m.workbuddy.(providers.DailyTasksRunner)
	if !ok {
		return empty, providers.ErrUnsupported
	}
	now := time.Now()
	run, err := store.ClaimDailyTaskRun(ctx, id, now.Format("2006-01-02"), trigger, now, 2*time.Minute)
	if err != nil {
		return empty, err
	}
	report, runErr := runner.RunDailyTasksReport(ctx, id, run.Report.Steps)
	if report == nil {
		report = &providers.DailyTasksReport{AccountID: id, RanAt: now.UTC().Format(time.RFC3339Nano)}
	}
	if runErr != nil || ctx.Err() != nil {
		message := "daily task execution failed"
		if errors.Is(runErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			message = "daily task execution canceled"
		} else if errors.Is(runErr, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			message = "daily task execution timed out"
		}
		report.Add("execution", "error", message)
	}
	run.Report = *report
	run.Status = report.Status()
	finished := time.Now().UTC()
	run.FinishedAt = &finished
	if trigger == "scheduled" && run.Attempt < 3 && (run.Status == "partial" || run.Status == "error") {
		retry := finished.Add(time.Duration(run.Attempt) * 5 * time.Minute)
		run.NextRetryAt = &retry
	}
	recordCtx, stopRecording := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stopRecording()
	if err := store.FinishDailyTaskRun(recordCtx, run); err != nil {
		return run, fmt.Errorf("save daily task result: %w", err)
	}
	return run, nil
}

// beginDailyBatch serializes automatic batches and coordinates with Close.
func (m *Manager) beginDailyBatch() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dailyBatchRunning || m.runCtx.Err() != nil {
		return false
	}
	m.dailyBatchRunning = true
	m.maintenanceDone.Add(1)
	return true
}

func (m *Manager) finishDailyBatch() {
	m.mu.Lock()
	m.dailyBatchRunning = false
	m.mu.Unlock()
	m.maintenanceDone.Done()
}

func (m *Manager) startDailyWorkBuddyBatch() {
	if !m.beginDailyBatch() {
		return
	}
	go func() {
		defer m.finishDailyBatch()
		m.executeDailyWorkBuddyBatch(m.runCtx, true)
	}()
}

func (m *Manager) runDailyWorkBuddyBatch(ctx context.Context, onlyOptIn bool) {
	if !m.beginDailyBatch() {
		return
	}
	defer m.finishDailyBatch()
	m.executeDailyWorkBuddyBatch(ctx, onlyOptIn)
}

func (m *Manager) executeDailyWorkBuddyBatch(ctx context.Context, onlyOptIn bool) {
	ctx, cancel, enabled := m.automaticMaintenanceContext(ctx)
	if !enabled {
		return
	}
	defer cancel()
	items, err := m.store.List(ctx)
	if err != nil {
		log.Printf("daily task account list unavailable")
		return
	}
	sem := make(chan struct{}, 4)
	var pending sync.WaitGroup
	defer pending.Wait()
	for _, account := range items {
		if account.Provider != "workbuddy" || account.ProviderRegion != "cn" || !account.Enabled || (onlyOptIn && !account.AutoCheckin) {
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		pending.Add(1)
		go func(id string) {
			defer pending.Done()
			defer func() { <-sem }()
			_, err := m.runDailyTasksDetailed(ctx, id, "scheduled")
			if err != nil && !errors.Is(err, accounts.ErrDailyTasksBusy) && !errors.Is(err, accounts.ErrDailyTasksNotDue) && !errors.Is(err, context.Canceled) {
				log.Printf("daily task account=%s: execution or persistence unavailable", id)
			}
		}(account.ID)
	}
}
