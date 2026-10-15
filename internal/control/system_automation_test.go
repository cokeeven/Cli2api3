package control

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

func TestSystemAutomationSwitchPreservesAccountSettings(t *testing.T) {
	svc, _, store, _ := newTestServices()
	store.accounts["enabled"] = accounts.Account{ID: "enabled", Enabled: true, AutoCheckin: true}
	store.accounts["disabled"] = accounts.Account{ID: "disabled", Enabled: false, AutoCheckin: false}
	before := make(map[string]accounts.Account)
	for id, account := range store.accounts {
		before[id] = account
	}
	var cross atomic.Bool
	sys := &System{Settings: svc.Settings, Accounts: svc.Accounts, CrossProviderPool: &cross, Mu: &sync.Mutex{}}
	ctx := context.Background()
	if !sys.Current(ctx).AutomationMaintenanceEnabled {
		t.Fatal("absent switch must preserve previous behavior")
	}
	for _, enabled := range []bool{false, true} {
		if err := sys.Patch(ctx, SystemSettingsPatch{AutomationMaintenanceEnabled: &enabled}); err != nil {
			t.Fatal(err)
		}
		if sys.Current(ctx).AutomationMaintenanceEnabled != enabled {
			t.Fatalf("current switch != %v", enabled)
		}
		if !reflect.DeepEqual(before, store.accounts) {
			t.Fatal("master switch changed account settings")
		}
	}
	store.secrets[accounts.AutomationMaintenanceEnabledSecret] = "invalid"
	if sys.Current(ctx).AutomationMaintenanceEnabled {
		t.Fatal("corrupt switch must fail closed")
	}
}
