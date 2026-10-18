package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
)

func (m *Manager) claimMaintenance(ctx context.Context, id, kind string, manual bool) (accounts.DailyTaskRun, error) {
	store, ok := m.store.(accounts.MaintenanceTaskStore)
	if !ok {
		return accounts.DailyTaskRun{}, nil
	}
	trigger := "scheduled"
	if manual {
		trigger = "manual"
	}
	return store.ClaimMaintenanceTaskRun(ctx, id, kind, trigger, time.Now(), 2*time.Minute)
}

func (m *Manager) finishMaintenance(ctx context.Context, run accounts.DailyTaskRun, operationErr error, interval time.Duration) error {
	if run.ID == "" {
		return operationErr
	}
	store := m.store.(accounts.MaintenanceTaskStore)
	finished := time.Now().UTC()
	run.FinishedAt = &finished
	run.Report = providers.DailyTasksReport{AccountID: run.AccountID, RanAt: run.StartedAt.Format(time.RFC3339Nano)}
	status, message := "success", "maintenance completed"
	if operationErr != nil {
		status = "error"
		message = "maintenance failed"
		switch {
		case errors.Is(operationErr, context.Canceled):
			message = "maintenance canceled"
		case errors.Is(operationErr, context.DeadlineExceeded):
			message = "maintenance timed out"
		default:
			var upstream *providers.Error
			if errors.As(operationErr, &upstream) {
				switch upstream.Kind {
				case accounts.KindAuth:
					message = "authentication required"
				case accounts.KindRateLimit:
					message = "upstream rate limited"
				case accounts.KindQuota:
					message = "upstream quota unavailable"
				}
			}
		}
		interval = quotaRefreshBackoff(run.Attempt, quotaRetryAfter(operationErr))
	}
	run.Report.Add(run.Kind, status, message)
	run.Status = run.Report.Status()
	next := finished.Add(interval)
	run.NextRetryAt = &next
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return errors.Join(operationErr, store.FinishDailyTaskRun(recordCtx, run))
}

// MaintainCredentialAccount is an expiry-aware check, never a chat or a forced
// refresh. Manual use is available while automatic maintenance is paused.
func (m *Manager) MaintainCredentialAccount(parent context.Context, id string) error {
	return m.maintainCredential(parent, id, true)
}
func (m *Manager) maintainCredential(parent context.Context, id string, manual bool) (result error) {
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	stop := context.AfterFunc(m.runCtx, cancel)
	defer stop()
	account, err := m.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if !account.Enabled || !quotaRefreshProviderSupported(account.Provider, account.ProviderRegion) {
		return providers.ErrUnsupported
	}
	m.mu.Lock()
	if m.runCtx.Err() != nil {
		m.mu.Unlock()
		return errManagerClosed
	}
	if m.checkinRunning == nil {
		m.checkinRunning = map[string]bool{}
	}
	if m.checkinRunning[id] {
		m.mu.Unlock()
		return accounts.ErrDailyTasksBusy
	}
	m.checkinRunning[id] = true
	m.maintenanceDone.Add(1)
	m.mu.Unlock()
	defer func() { m.mu.Lock(); delete(m.checkinRunning, id); m.mu.Unlock(); m.maintenanceDone.Done() }()
	state := m.quotaRefreshState()
	select {
	case state.sem <- struct{}{}:
		defer func() { <-state.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}
	run, err := m.claimMaintenance(ctx, id, "credential", manual)
	if err != nil {
		return err
	}
	schedule, err := accounts.LoadMaintenanceSchedule(ctx, m.store)
	if err != nil {
		return m.finishMaintenance(ctx, run, err, 15*time.Minute)
	}
	defer func() {
		result = m.finishMaintenance(ctx, run, result, time.Duration(schedule.CredentialIntervalMinutes)*time.Minute)
	}()
	var maintainer providers.CredentialMaintainer
	if m.providers != nil {
		if adapter, ok := m.providers.Get(account.Provider); ok {
			maintainer = adapter.CredentialMaintenance
		}
	}
	if maintainer == nil && account.Provider == "workbuddy" {
		maintainer = m.workbuddy
	}
	if maintainer == nil {
		return fmt.Errorf("credential maintenance unsupported")
	}
	return maintainer.Keepalive(ctx, id)
}

func (m *Manager) maintainDueCredentials(parent context.Context) {
	ctx, cancel, enabled := m.automaticMaintenanceContext(parent)
	if !enabled {
		return
	}
	defer cancel()
	schedule, err := accounts.LoadMaintenanceSchedule(ctx, m.store)
	if err != nil || !schedule.CredentialEnabled {
		return
	}
	items, err := m.store.List(ctx)
	if err != nil {
		return
	}
	m.mu.Lock()
	if m.credentialBatchRunning || m.runCtx.Err() != nil {
		m.mu.Unlock()
		return
	}
	m.credentialBatchRunning = true
	m.maintenanceDone.Add(1)
	m.mu.Unlock()
	// Transfer ownership of the automatic context to the existing maintenance
	// worker group, so a slow credential exchange does not stall other schedules.
	workerCtx, workerCancel, workerEnabled := m.automaticMaintenanceContext(m.runCtx)
	if !workerEnabled {
		m.mu.Lock()
		m.credentialBatchRunning = false
		m.mu.Unlock()
		m.maintenanceDone.Done()
		return
	}
	go func() {
		defer workerCancel()
		defer func() { m.mu.Lock(); m.credentialBatchRunning = false; m.mu.Unlock(); m.maintenanceDone.Done() }()
		sem := make(chan struct{}, 4)
		var pending sync.WaitGroup
		defer pending.Wait()
		for _, account := range items {
			if workerCtx.Err() != nil {
				return
			}
			if !account.Enabled || !quotaRefreshProviderSupported(account.Provider, account.ProviderRegion) {
				continue
			}
			// The persisted lease checks due/backoff before any upstream I/O.
			select {
			case sem <- struct{}{}:
			case <-workerCtx.Done():
				return
			}
			pending.Add(1)
			go func(id string) {
				defer pending.Done()
				defer func() { <-sem }()
				_ = m.maintainCredential(workerCtx, id, false)
			}(account.ID)
		}
	}()
}
