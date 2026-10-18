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

func TestSystemQuotaRefreshIntervalSetting(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _ := newTestServices()
	var cross atomic.Bool
	sys := &System{Settings: svc.Settings, CrossProviderPool: &cross, Mu: &sync.Mutex{}}
	if got := sys.Current(ctx).QuotaRefreshIntervalMinutes; got != accounts.DefaultQuotaRefreshIntervalMinutes {
		t.Fatalf("default quota refresh interval=%d", got)
	}
	interval := 60
	if err := sys.Patch(ctx, SystemSettingsPatch{QuotaRefreshIntervalMinutes: &interval}); err != nil {
		t.Fatal(err)
	}
	if got := sys.Current(ctx).QuotaRefreshIntervalMinutes; got != interval {
		t.Fatalf("quota refresh interval=%d, want %d", got, interval)
	}
	for _, invalid := range []int{0, 4, 1441} {
		if err := sys.Patch(ctx, SystemSettingsPatch{QuotaRefreshIntervalMinutes: &invalid}); err == nil {
			t.Errorf("accepted invalid quota refresh interval %d", invalid)
		}
	}
}

func TestSystemMaintenanceScheduleRoundTrip(t *testing.T) {
	ctx := context.Background()
	svc, _, store, _ := newTestServices()
	var cross atomic.Bool
	sys := &System{Settings: svc.Settings, CrossProviderPool: &cross, Mu: &sync.Mutex{}}
	before := sys.Current(ctx).MaintenanceSchedule
	if before != accounts.DefaultMaintenanceSchedule() {
		t.Fatal("defaults changed")
	}
	schedule := accounts.MaintenanceSchedule{CredentialEnabled: false, CredentialIntervalMinutes: 60, QuotaEnabled: false, DailyEnabled: false, DailyTime: "23:10"}
	if err := sys.Patch(ctx, SystemSettingsPatch{MaintenanceSchedule: &schedule}); err != nil {
		t.Fatal(err)
	}
	if sys.Current(ctx).MaintenanceSchedule != schedule || len(store.accounts) != 0 {
		t.Fatal("setting round trip or account isolation failed")
	}
	schedule.DailyTime = "25:00"
	if err := sys.Patch(ctx, SystemSettingsPatch{MaintenanceSchedule: &schedule}); err == nil {
		t.Fatal("invalid clock accepted")
	}
	if sys.Current(ctx).MaintenanceSchedule.DailyTime != "23:10" {
		t.Fatal("invalid patch partially persisted")
	}
}
