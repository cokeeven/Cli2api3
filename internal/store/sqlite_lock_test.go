package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

func openSQLiteBusyFixture(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sqlite lock fixture.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func TestOpenStoreConnectionReplacementPreservesPragmas(t *testing.T) {
	store, _ := openSQLiteBusyFixture(t)
	readPragmas := func() (busyTimeout, foreignKeys int) {
		t.Helper()
		if err := store.DB().QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
			t.Fatal(err)
		}
		if err := store.DB().QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
			t.Fatal(err)
		}
		return busyTimeout, foreignKeys
	}
	if busyTimeout, foreignKeys := readPragmas(); busyTimeout != 5000 || foreignKeys != 1 {
		t.Fatalf("initial busy_timeout=%d foreign_keys=%d; want 5000 and 1", busyTimeout, foreignKeys)
	}

	store.DB().SetConnMaxLifetime(time.Nanosecond)
	time.Sleep(2 * time.Millisecond)
	if busyTimeout, foreignKeys := readPragmas(); busyTimeout != 5000 || foreignKeys != 1 {
		t.Fatalf("replacement busy_timeout=%d foreign_keys=%d; want 5000 and 1", busyTimeout, foreignKeys)
	}
}

func TestInsertRequestLogRetriesTransientSQLiteBusy(t *testing.T) {
	store, path := openSQLiteBusyFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	lockDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer lockDB.Close()
	lockDB.SetMaxOpenConns(1)
	lockConn, err := lockDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lockConn.Close()
	if _, err := lockConn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	rollback := func() { _, _ = lockConn.ExecContext(context.Background(), `ROLLBACK`) }
	t.Cleanup(rollback)
	time.AfterFunc(80*time.Millisecond, rollback)

	err = store.InsertRequestLog(ctx, accounts.RequestLog{
		ID:        "req-transient-lock",
		Status:    accounts.RequestStatusStarted,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("InsertRequestLog did not recover from a transient external writer lock: %v", err)
	}
	var count int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM request_logs WHERE id=?`, "req-transient-lock").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("persisted request count=%d, want exactly one", count)
	}
}

func TestInsertRequestLogRetryStopsOnContextCancellation(t *testing.T) {
	store, path := openSQLiteBusyFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lockDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer lockDB.Close()
	lockDB.SetMaxOpenConns(1)
	lockConn, err := lockDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lockConn.Close()
	if _, err := lockConn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	rollback := func() { _, _ = lockConn.ExecContext(context.Background(), `ROLLBACK`) }
	t.Cleanup(rollback)
	time.AfterFunc(250*time.Millisecond, rollback)
	time.AfterFunc(20*time.Millisecond, cancel)

	err = store.InsertRequestLog(ctx, accounts.RequestLog{
		ID:        "req-canceled-lock",
		Status:    accounts.RequestStatusStarted,
		CreatedAt: time.Now().UTC(),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("InsertRequestLog error=%v, want context cancellation during busy retry", err)
	}
	var count int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM request_logs WHERE id=?`, "req-canceled-lock").Scan(&count); err == nil && count != 0 {
		t.Fatalf("canceled request unexpectedly persisted %d times", count)
	}
}

func TestReserveUsageRetriesBusyTransactionByReservationID(t *testing.T) {
	store, path := openSQLiteBusyFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	account, err := store.Create(ctx, accounts.CreateAccount{Name: "synthetic lock test", Provider: "workbuddy", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	lockDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer lockDB.Close()
	lockDB.SetMaxOpenConns(1)
	lockConn, err := lockDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lockConn.Close()
	if _, err := lockConn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	rollback := func() { _, _ = lockConn.ExecContext(context.Background(), `ROLLBACK`) }
	t.Cleanup(rollback)
	time.AfterFunc(80*time.Millisecond, rollback)

	err = store.ReserveUsage(ctx, accounts.UsageReservation{ID: "reserve-transient-lock", AccountID: account.ID, Model: "glm-5.3", EstimatedTokens: 7})
	if err != nil {
		t.Fatalf("ReserveUsage did not recover from a transient external writer lock: %v", err)
	}
	var count int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_reservations WHERE id=?`, "reserve-transient-lock").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("persisted usage reservation count=%d, want exactly one", count)
	}
}

func TestSaveCooldownsRetriesBusyStatementsInsideTransaction(t *testing.T) {
	store, path := openSQLiteBusyFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	account, err := store.Create(ctx, accounts.CreateAccount{Name: "synthetic cooldown lock test", Provider: "workbuddy", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	lockDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer lockDB.Close()
	lockDB.SetMaxOpenConns(1)
	lockConn, err := lockDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lockConn.Close()
	if _, err := lockConn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	rollback := func() { _, _ = lockConn.ExecContext(context.Background(), `ROLLBACK`) }
	t.Cleanup(rollback)
	time.AfterFunc(80*time.Millisecond, rollback)

	row := accounts.CooldownRow{AccountID: account.ID, Model: "glm-5.3", DownUntil: time.Now().UTC().Add(time.Minute), Kind: accounts.KindRateLimit}
	if err := store.SaveCooldowns(ctx, account.ID, []accounts.CooldownRow{row}); err != nil {
		t.Fatalf("SaveCooldowns did not recover from a transient external writer lock: %v", err)
	}
	rows, err := store.LoadCooldowns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].AccountID != account.ID || rows[0].Model != "glm-5.3" {
		t.Fatalf("persisted cooldown rows=%+v, want the single synthetic row", rows)
	}
}

func TestInsertRequestLogReportsBusyRetryExhaustion(t *testing.T) {
	store, path := openSQLiteBusyFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	lockDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer lockDB.Close()
	lockDB.SetMaxOpenConns(1)
	lockConn, err := lockDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lockConn.Close()
	if _, err := lockConn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	stopRelease := make(chan struct{})
	released := make(chan error, 1)
	go func() {
		timer := time.NewTimer(6 * time.Second)
		defer timer.Stop()
		select {
		case <-stopRelease:
		case <-timer.C:
		}
		_, rollbackErr := lockConn.ExecContext(context.Background(), `ROLLBACK`)
		released <- rollbackErr
	}()
	started := time.Now()
	err = store.InsertRequestLog(ctx, accounts.RequestLog{
		ID:        "req-lock-exhausted",
		Status:    accounts.RequestStatusStarted,
		CreatedAt: time.Now().UTC(),
	})
	close(stopRelease)
	if rollbackErr := <-released; rollbackErr != nil {
		t.Fatal(rollbackErr)
	}
	if err == nil || !strings.Contains(err.Error(), "sqlite busy/locked retry exhausted") {
		t.Fatalf("InsertRequestLog error=%v, want an observable bounded busy retry exhaustion", err)
	}
	if !isSQLiteBusyLocked(err) {
		t.Fatalf("retry exhaustion did not retain the SQLite busy cause: %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 6*time.Second {
		t.Fatalf("busy retry exceeded its 5 second budget: %s", elapsed)
	}
}
