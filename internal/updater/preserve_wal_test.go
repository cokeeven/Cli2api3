package updater

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// Execute the actual preservation script against a private fixture, without
// Docker or any production path. /data is mapped to this test's temp folder.
type preservationShell struct{ directory string }

func (r preservationShell) Run(ctx context.Context, _ string, args ...string) ([]byte, error) {
	script := strings.ReplaceAll(args[len(args)-1], "/data", filepath.ToSlash(r.directory))
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	cmd.Env = os.Environ()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-e" {
			cmd.Env = append(cmd.Env, strings.ReplaceAll(args[i+1], "/data", filepath.ToSlash(r.directory)))
		}
	}
	out, err := cmd.CombinedOutput()
	return out, err
}

func TestPreserveDatabaseIncludesCommittedWAL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux container preservation script")
	}
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("sha256sum required")
	}
	directory := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(directory, "qoder.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, query := range []string{
		"PRAGMA journal_mode=WAL", "PRAGMA wal_autocheckpoint=0",
		"CREATE TABLE retained (value TEXT)", "PRAGMA wal_checkpoint(TRUNCATE)",
		"INSERT INTO retained VALUES ('post-upgrade-write')",
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	// Keep the connection open so the committed row stays in WAL, exactly
	// the recovery case missed by copying only the database's main file.
	e := NewExecutor(ExecutorConfig{})
	e.runner = preservationShell{directory: directory}
	preserved, err := e.preserveDatabase(context.Background(), dataMount{}, "unused", ApplyRequest{TargetVersion: "v1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(directory, "backups", filepath.Base(preserved))
	for _, suffix := range []string{"", "-wal"} {
		data, err := os.ReadFile(dest + suffix)
		if err != nil || len(data) == 0 {
			t.Fatalf("missing preserved %q: %v", suffix, err)
		}
		sum := sha256.Sum256(data)
		recorded, err := os.ReadFile(dest + suffix + ".sha256")
		if err != nil || strings.TrimSpace(string(recorded)) != hex.EncodeToString(sum[:]) {
			t.Fatalf("invalid checksum for %s: %v", suffix, err)
		}
	}
	recovered, err := sql.Open("sqlite", dest)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	var value string
	if err := recovered.QueryRow("SELECT value FROM retained").Scan(&value); err != nil || value != "post-upgrade-write" {
		t.Fatalf("committed WAL data lost: value=%q err=%v", value, err)
	}
}
