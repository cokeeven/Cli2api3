package runtime

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
	sqlstore "github.com/caigee-cmd/cli2api/internal/store"
)

type delayedOldProber struct {
	count   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (p *delayedOldProber) Probe(ctx context.Context, _ string) (providers.AccountHealth, error) {
	if p.count.Add(1) == 1 {
		close(p.entered)
		select {
		case <-p.release:
		case <-ctx.Done():
			return providers.AccountHealth{}, ctx.Err()
		}
		return providers.AccountHealth{Ready: true, Hot: true}, nil
	}
	return providers.AccountHealth{Ready: false, LastError: "new credential is invalid"}, nil
}
func (*delayedOldProber) Quota(context.Context, string) (*providers.QuotaInfo, error) {
	return nil, nil
}

func TestOldProbeCannotMakeRestartedAccountReady(t *testing.T) {
	ctx := context.Background()
	store, err := sqlstore.OpenStore(filepath.Join(t.TempDir(), "qoder.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a, err := store.Create(ctx, accounts.CreateAccount{Name: "probe-epoch", Provider: "trae", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(ManagerConfig{DataDir: t.TempDir()}, store, nil)
	defer m.Close()
	p := &delayedOldProber{entered: make(chan struct{}), release: make(chan struct{})}
	registry := providers.NewRegistry()
	registry.Register(providers.Adapter{ID: "trae", Prober: p})
	m.SetProviders(registry)
	if err := m.startAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.entered:
	case <-time.After(time.Second):
		t.Fatal("first probe not started")
	}
	if err := m.stopAccount(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.startAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		item, _ := m.pool.ByID(a.ID)
		if item.LastError == "new credential is invalid" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("new probe did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	close(p.release)
	m.maintenanceDone.Wait()
	item, _ := m.pool.ByID(a.ID)
	if item.Ready == nil || *item.Ready || item.Hot == nil || *item.Hot {
		t.Fatalf("stale probe restored readiness: %+v", item)
	}
	stored, err := store.Get(ctx, a.ID)
	if err != nil || stored.Status == "ready" {
		t.Fatalf("stale probe changed stored status: %s, %v", stored.Status, err)
	}
}
