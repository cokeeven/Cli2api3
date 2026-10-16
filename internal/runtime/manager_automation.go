package runtime

import (
	"context"
	"log"
	"sync"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

// ReloadAutomationMaintenance cancels only automatic work when the master
// switch is disabled or cannot be read. Manual operations use runCtx directly.
func (m *Manager) ReloadAutomationMaintenance(ctx context.Context) error {
	m.automationMu.Lock()
	defer m.automationMu.Unlock()
	enabled, err := accounts.AutomationMaintenanceEnabled(ctx, m.store)
	if !enabled || err != nil {
		for _, cancel := range m.automationRuns {
			cancel()
		}
	}
	return err
}

func (m *Manager) automaticMaintenanceContext(parent context.Context) (context.Context, context.CancelFunc, bool) {
	m.automationMu.Lock()
	defer m.automationMu.Unlock()
	enabled, err := accounts.AutomationMaintenanceEnabled(parent, m.store)
	if err != nil || !enabled || m.runCtx.Err() != nil || parent.Err() != nil {
		if err != nil {
			log.Printf("automatic maintenance setting unavailable")
		}
		return parent, func() {}, false
	}
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(m.runCtx, cancel)
	m.automationNextID++
	id := m.automationNextID
	if m.automationRuns == nil {
		m.automationRuns = make(map[uint64]context.CancelFunc)
	}
	m.automationRuns[id] = cancel
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			cancel()
			stop()
			m.automationMu.Lock()
			delete(m.automationRuns, id)
			m.automationMu.Unlock()
		})
	}, true
}
