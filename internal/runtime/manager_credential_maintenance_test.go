package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/store"
)

type keepaliveRecorder struct {
	mu      sync.Mutex
	account []string
	err     error
}

func (*keepaliveRecorder) DailyCheckin(context.Context, string) (string, error)  { return "", nil }
func (*keepaliveRecorder) RunDailyTasks(context.Context, string) (string, error) { return "", nil }
func (r *keepaliveRecorder) Keepalive(_ context.Context, accountID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.account = append(r.account, accountID)
	return r.err
}

func TestCredentialMaintenanceHistoryBackoffAndManualRetry(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenStore(t.TempDir() + "/status.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := NewManager(ManagerConfig{DataDir: t.TempDir()}, db, nil)
	defer m.Close()
	account, err := db.Create(ctx, accounts.CreateAccount{Name: "synthetic", Provider: "workbuddy", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &keepaliveRecorder{err: errors.New("authorization synthetic-sensitive-text")}
	m.SetWorkBuddy(recorder)
	m.KeepaliveWorkBuddy(ctx, false)
	m.KeepaliveWorkBuddy(ctx, false)
	if len(recorder.account) != 1 {
		t.Fatal("scheduled failure bypassed persisted backoff")
	}
	page, err := db.ListMaintenanceLogs(ctx, accounts.MaintenanceLogFilter{Kind: "credential"})
	if err != nil || page.Total != 1 {
		t.Fatal("missing result", err)
	}
	if page.Items[0].Status != "error" || page.Items[0].NextRetryAt == nil || page.Items[0].NextRetryAt.Before(time.Now()) || strings.Contains(page.Items[0].Summary, "sensitive") {
		t.Fatalf("unsafe or incomplete history: %+v", page.Items[0])
	}
	if err = db.SetSecret(ctx, accounts.AutomationMaintenanceEnabledSecret, "0"); err != nil {
		t.Fatal(err)
	}
	recorder.err = nil
	if err = m.MaintainCredentialAccount(ctx, account.ID); err != nil {
		t.Fatal("manual retry blocked", err)
	}
	page, err = db.ListMaintenanceLogs(ctx, accounts.MaintenanceLogFilter{Kind: "credential"})
	if err != nil || page.Total != 2 || page.Items[0].Status != "success" || page.Items[0].Trigger != "manual" {
		t.Fatal("manual result missing", err)
	}
}

func TestCredentialMaintenanceDoesNotRequireCheckinOptIn(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenStore(t.TempDir() + "/maintenance.db")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(ManagerConfig{DataDir: t.TempDir()}, database, nil)
	recorder := &keepaliveRecorder{}
	manager.SetWorkBuddy(recorder)
	t.Cleanup(func() {
		_ = manager.Close()
		_ = database.Close()
	})

	active, err := database.Create(ctx, accounts.CreateAccount{Name: "active", Provider: "workbuddy", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Create(ctx, accounts.CreateAccount{Name: "disabled", Provider: "workbuddy", Region: "cn", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	manager.KeepaliveWorkBuddy(ctx, false)
	if len(recorder.account) != 1 || recorder.account[0] != active.ID {
		t.Fatalf("enabled WorkBuddy maintenance accounts=%v", recorder.account)
	}

	if err := database.SetSecret(ctx, accounts.AutomationMaintenanceEnabledSecret, "0"); err != nil {
		t.Fatal(err)
	}
	manager.KeepaliveWorkBuddy(ctx, false)
	if len(recorder.account) != 1 {
		t.Fatalf("disabled automatic maintenance still refreshed: %v", recorder.account)
	}
}
