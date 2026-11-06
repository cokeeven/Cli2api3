package control_test

import (
	"context"
	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/control"
	"github.com/caigee-cmd/cli2api/internal/store"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestAlternativePolicyPersistsAndDefaultsOff(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "settings.db")
	db, err := store.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	poolFlag := &atomic.Bool{}
	system := control.System{Settings: control.NewSettings(db), CrossProviderPool: poolFlag}
	if got := system.Current(ctx).ModelAlternatives; got.Enabled || len(got.Rules) != 0 {
		t.Fatalf("old db default %+v", got)
	}
	p := accounts.ModelAlternativePolicy{Enabled: true, Rules: []accounts.ModelAlternativeRule{{Provider: "workbuddy", Region: "cn", RequestedModel: "a", AlternativeModel: "b"}}}
	if err = system.Patch(ctx, control.SystemSettingsPatch{ModelAlternatives: &p}); err != nil {
		t.Fatal(err)
	}
	bad := accounts.ModelAlternativePolicy{Enabled: true, Rules: append(p.Rules, accounts.ModelAlternativeRule{Provider: "workbuddy", Region: "cn", RequestedModel: "b", AlternativeModel: "a"})}
	if err = system.Patch(ctx, control.SystemSettingsPatch{ModelAlternatives: &bad}); err == nil {
		t.Fatal("cycle accepted")
	}
	db.Close()
	db, err = store.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	system.Settings = control.NewSettings(db)
	got := system.Current(ctx).ModelAlternatives
	if !got.Enabled || len(got.Rules) != 1 || got.Rules[0].AlternativeModel != "b" {
		t.Fatalf("persisted %+v", got)
	}
}
