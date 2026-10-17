package store

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/caigee-cmd/cli2api/internal/accounts"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// BackupDatabase opens an existing database read-only without running migrations.
// VACUUM INTO takes an SQLite-consistent snapshot including committed WAL pages.
func BackupDatabase(ctx context.Context, database, directory string) (accounts.Backup, error) {
	if !filepath.IsAbs(database) {
		return accounts.Backup{}, fmt.Errorf("absolute database path required")
	}
	if _, err := os.Stat(database); err != nil {
		return accounts.Backup{}, err
	}
	p := filepath.ToSlash(database)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return accounts.Backup{}, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	return (&Store{db: db}).Backup(ctx, directory, 5)
}
