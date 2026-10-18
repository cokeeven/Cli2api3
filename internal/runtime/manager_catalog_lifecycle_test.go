package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/executor"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/store"
)

type lifecycleCatalog struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
	err     error
}

func (c *lifecycleCatalog) Models(context.Context, string) ([]providers.ModelInfo, error) {
	c.calls.Add(1)
	close(c.started)
	<-c.release // Deliberately emulate an upstream which ignores cancellation.
	return []providers.ModelInfo{{PublicModel: "obsolete", NativeModel: "obsolete"}}, c.err
}

func TestCatalogRefreshRejectsObsoleteAccountGeneration(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "late_success_after_restart"
		if fail {
			name = "late_error_after_delete"
		}
		t.Run(name, func(t *testing.T) {
			db, err := store.OpenStore(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			m := NewManager(ManagerConfig{DataDir: t.TempDir()}, db, nil)
			defer m.Close()
			catalog := &lifecycleCatalog{started: make(chan struct{}), release: make(chan struct{})}
			if fail {
				catalog.err = errors.New("account not found")
			}
			reg := providers.NewRegistry()
			reg.Register(providers.Adapter{ID: "trae", Models: catalog})
			m.SetProviders(reg)
			m.pool.Upsert(executor.Item{ID: "synthetic", Provider: "trae", Region: "cn", Runtime: "in_process"})
			m.EnsureModelCatalogs(context.Background(), true)
			<-catalog.started
			if err := m.StopAccount("synthetic"); err != nil {
				t.Fatal(err)
			}
			if !fail {
				m.pool.Upsert(executor.Item{ID: "synthetic", Provider: "trae", Region: "cn", Runtime: "in_process", Models: []string{"fresh"}})
			}
			close(catalog.release)
			m.maintenanceDone.Wait()
			if !fail {
				item, _ := m.pool.ByID("synthetic")
				if len(item.Models) != 1 || item.Models[0] != "fresh" {
					t.Fatalf("obsolete catalog replaced new generation: %v", item.Models)
				}
			}
			state := m.catalogRefresh()
			state.mu.Lock()
			_, backedOff := state.failures["synthetic"]
			state.mu.Unlock()
			if backedOff {
				t.Fatal("obsolete failure installed backoff on removed/restarted account")
			}
		})
	}
}

func TestQueuedCatalogRefreshSkipsRemovedAccount(t *testing.T) {
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := NewManager(ManagerConfig{DataDir: t.TempDir()}, db, nil)
	defer m.Close()
	catalog := &lifecycleCatalog{started: make(chan struct{}), release: make(chan struct{})}
	close(catalog.release)
	reg := providers.NewRegistry()
	reg.Register(providers.Adapter{ID: "workbuddy", Models: catalog})
	m.SetProviders(reg)
	m.pool.Upsert(executor.Item{ID: "synthetic", Provider: "workbuddy", Region: "global", Runtime: "in_process"})
	state := m.catalogRefresh()
	for i := 0; i < maxConcurrentModelFetches; i++ {
		state.sem <- struct{}{}
	}
	m.EnsureModelCatalogs(context.Background(), true)
	if err := m.StopAccount("synthetic"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxConcurrentModelFetches; i++ {
		<-state.sem
	}
	m.maintenanceDone.Wait()
	if catalog.calls.Load() != 0 {
		t.Fatal("removed account reached upstream after leaving catalog queue")
	}
}
