package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/providers/qoder"
)

// Health/probe scheduling. Quota fetch failures must not flip Ready.
// Reads pool items and writes runtime/health via existing Merge/Set helpers.

func (m *Manager) RefreshAccount(ctx context.Context, id string, forceQuota bool) error {
	if m == nil || m.pool == nil {
		return fmt.Errorf("account manager not ready")
	}
	item, ok := m.pool.ByID(id)
	if !ok {
		return fmt.Errorf("account %s is not running", id)
	}
	return m.refreshOne(ctx, item, forceQuota, true)
}

// AccountView returns the same projection the accounts list builds, for one
func (m *Manager) RefreshAll(ctx context.Context, forceQuota bool) error {
	var joined error
	for _, item := range m.pool.Items() {
		if err := m.refreshOne(ctx, item, forceQuota, false); err != nil {
			var quotaErr *QuotaRefreshError
			if errors.As(err, &quotaErr) {
				// Background maintenance must remain best-effort. The manual
				// single-account endpoint still returns this typed error so the
				// console can show that quota refresh did not complete.
				continue
			}
			joined = errors.Join(joined, err)
		}
	}
	return joined
}

func (m *Manager) refreshOne(ctx context.Context, item Item, forceQuota, manual bool) error {
	if item.RuntimeState == "dead" {
		return nil
	}
	if item.Runtime == string(providers.RuntimeInProcess) || strings.TrimSpace(item.URL) == "" {
		err := m.refreshInProcess(ctx, item)
		if err != nil || !manual {
			return err
		}
		latest, ok := m.pool.ByID(item.ID)
		if !ok {
			return fmt.Errorf("account %s is not running", item.ID)
		}
		return m.refreshQuota(ctx, latest, forceQuota, true)
	}
	client := qoder.WorkerClient{HTTP: m.httpClient}
	health, statusCode, err := client.Health(ctx, item.URL)
	if err != nil {
		if statusCode == 0 {
			ready := false
			hot := false
			m.pool.MergeHealth(item.ID, ready, hot, 0, item.Restarts, err.Error())
			_ = m.store.Observe(ctx, item.ID, "", "error", err.Error(), KindUnavailable)
			return fmt.Errorf("health account %s: %w", item.ID, err)
		}
		return fmt.Errorf("decode account %s health: %w", item.ID, err)
	}
	ready := statusCode < 300 && health.OK && health.Ready
	m.pool.MergeHealth(item.ID, ready, health.Hot, health.InFlight, item.Restarts, health.LastError)
	if ready || health.Hot {
		m.resetRestartBackoff(item.ID)
	}
	status := "login_required"
	if ready || health.Hot {
		status = "ready"
	} else if health.LastError != "" {
		status = "error"
	}
	if err := m.store.Observe(ctx, item.ID, health.UID, status, health.LastError, ""); err != nil {
		return err
	}
	// Fetch quota after the health/observe path so a quota endpoint outage
	// never changes readiness. A successful exhausted snapshot may still
	// keep the account out of request routing.
	if health.Hot || ready {
		// Quota and model discovery are independent upstream operations. Run
		// them concurrently so a quota outage cannot prevent catalog refresh.
		quotaDone := make(chan error, 1)
		go func() { quotaDone <- m.refreshQuota(ctx, item, forceQuota, manual) }()
		m.fetchAccountModels(ctx, item)
		quotaErr := <-quotaDone
		if quotaErr != nil {
			return quotaErr
		}
	}
	return nil
}

func (m *Manager) refreshInProcess(ctx context.Context, item Item) error {
	m.mu.Lock()
	epoch := m.accountEpoch[item.ID]
	m.mu.Unlock()
	return m.refreshInProcessAt(ctx, item, epoch)
}

func (m *Manager) refreshInProcessAt(ctx context.Context, item Item, epoch uint64) error {
	adapter, ok := m.providers.Get(item.Provider)
	if !ok || adapter.Prober == nil {
		// No prober registered: leave pool state alone and never hit ""+/health.
		return nil
	}
	health, err := adapter.Prober.Probe(ctx, item.ID)
	m.mu.Lock()
	if ctx.Err() != nil || m.runCtx.Err() != nil || m.accountEpoch[item.ID] != epoch {
		m.mu.Unlock()
		return ctx.Err()
	}
	if err != nil {
		m.pool.MergeHealth(item.ID, false, false, 0, item.Restarts, err.Error())
		_ = m.store.Observe(ctx, item.ID, "", "error", err.Error(), KindUnavailable)
		m.mu.Unlock()
		return fmt.Errorf("probe account %s: %w", item.ID, err)
	}
	m.pool.MergeHealth(item.ID, health.Ready, health.Hot, health.InFlight, item.Restarts, health.LastError)
	if health.Ready || health.Hot {
		delete(m.restartBackoff, item.ID)
	}
	status := "login_required"
	if health.Ready || health.Hot {
		status = "ready"
	} else if health.LastError != "" {
		status = "error"
		if health.LastErrorKind == accounts.KindAuth {
			status = "login_required"
		}
	}
	if err := m.store.Observe(ctx, item.ID, health.UID, status, health.LastError, health.LastErrorKind); err != nil {
		m.mu.Unlock()
		return err
	}
	if health.Ready || health.Hot {
		m.maintenanceDone.Add(1)
		m.mu.Unlock()
		go func(accountID string) {
			defer m.maintenanceDone.Done()
			quotaCtx, cancel := context.WithTimeout(m.runCtx, 5*time.Second)
			defer cancel()
			if latest, ok := m.pool.ByID(accountID); ok {
				_ = m.refreshQuota(quotaCtx, latest, false, false, epoch)
			}
		}(item.ID)
		m.fetchAccountModelsAt(ctx, item, epoch)
	} else {
		m.mu.Unlock()
	}
	return nil
}

func (m *Manager) forceReady(accountID string) bool {
	if m == nil || strings.TrimSpace(accountID) == "" || strings.TrimSpace(m.config.DataDir) == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(m.config.DataDir, "force-ready", accountID))
	return err == nil
}
