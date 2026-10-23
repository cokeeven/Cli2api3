package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// A healthy pool may replace its physical SQLite connection after cancellation,
// lifetime expiry, or a driver error. Safety settings must survive replacement.
func TestReviewerConnectionReplacementPreservesPragmas(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "review.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var initialBusy, initialForeign int
	if err = s.DB().QueryRow("PRAGMA busy_timeout").Scan(&initialBusy); err != nil {
		t.Fatal(err)
	}
	if err = s.DB().QueryRow("PRAGMA foreign_keys").Scan(&initialForeign); err != nil {
		t.Fatal(err)
	}
	s.DB().SetConnMaxLifetime(time.Nanosecond)
	time.Sleep(time.Millisecond)
	var replacedBusy, replacedForeign int
	if err = s.DB().QueryRow("PRAGMA busy_timeout").Scan(&replacedBusy); err != nil {
		t.Fatal(err)
	}
	if err = s.DB().QueryRow("PRAGMA foreign_keys").Scan(&replacedForeign); err != nil {
		t.Fatal(err)
	}
	t.Logf("initial busy_timeout=%d foreign_keys=%d; replacement busy_timeout=%d foreign_keys=%d", initialBusy, initialForeign, replacedBusy, replacedForeign)
	if replacedBusy < 5000 {
		t.Errorf("busy_timeout lost after physical connection replacement: %d", replacedBusy)
	}
	if replacedForeign != 1 {
		t.Errorf("foreign_keys lost after physical connection replacement: %d", replacedForeign)
	}
}

func TestReviewerReplacedConnectionWaitsForBriefWriter(t *testing.T) {
	file := filepath.Join(t.TempDir(), "contention.db")
	s, err := OpenStore(file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.DB().Exec("CREATE TABLE reviewer_probe(id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.SetMaxOpenConns(1)
	if _, err = other.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	released := make(chan error, 1)
	go func() {
		time.Sleep(200 * time.Millisecond)
		_, e := other.Exec("COMMIT")
		released <- e
	}()
	s.DB().SetConnMaxLifetime(time.Nanosecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	begin := time.Now()
	_, writeErr := s.DB().ExecContext(ctx, "INSERT INTO reviewer_probe VALUES(1)")
	t.Logf("replacement write elapsed=%s error=%v", time.Since(begin), writeErr)
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	if writeErr != nil {
		t.Fatalf("brief external writer should be waited out: %v", writeErr)
	}
}

func TestReviewerBusyWriteObservesContextDeadline(t *testing.T) {
	file := filepath.Join(t.TempDir(), "cancel.db")
	s, err := OpenStore(file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.DB().Exec("CREATE TABLE reviewer_cancel(id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.SetMaxOpenConns(1)
	if _, err = other.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	released := make(chan error, 1)
	go func() {
		time.Sleep(900 * time.Millisecond)
		_, e := other.Exec("COMMIT")
		released <- e
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	begin := time.Now()
	_, writeErr := s.DB().ExecContext(ctx, "INSERT INTO reviewer_cancel VALUES(1)")
	elapsed := time.Since(begin)
	t.Logf("deadline=100ms elapsed=%s error=%v", elapsed, writeErr)
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	if writeErr == nil {
		t.Error("expired write unexpectedly succeeded")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("context cancellation waits for writer lock instead of deadline: %s", elapsed)
	}
}
