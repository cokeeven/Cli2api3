package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/buildinfo"

	_ "modernc.org/sqlite"
)

// backupManifestSuffix names the sidecar record that travels with every
// snapshot. It carries the integrity checksum and the exact schema state the
// snapshot was taken at, so a restore can prove what it is about to put back
// instead of trusting a filename.
const backupManifestSuffix = ".manifest.json"

// BackupManifest is the on-disk record for one SQLite snapshot.
type BackupManifest struct {
	Name            string `json:"name"`
	CreatedAt       string `json:"created_at"`
	AppVersion      string `json:"app_version"`
	AppCommit       string `json:"app_commit,omitempty"`
	SchemaMigration string `json:"schema_migration"`
	SchemaChecksum  string `json:"schema_checksum"`
	SchemaCount     int    `json:"schema_count"`
	SizeBytes       int64  `json:"size_bytes"`
	SHA256          string `json:"sha256"`
	IntegrityCheck  string `json:"integrity_check"`
}

func (m BackupManifest) Path(databasePath string) string {
	return databasePath + backupManifestSuffix
}

// LatestSchemaState reports the newest applied migration, so the manifest can
// record the schema version a restore would produce.
func (s *Store) LatestSchemaState(ctx context.Context) (string, string, int, error) {
	if s == nil || s.db == nil {
		return "", "", 0, fmt.Errorf("sqlite store unavailable")
	}
	return readSchemaState(ctx, s.db)
}

func readSchemaState(ctx context.Context, db *sql.DB) (string, string, int, error) {
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		return "", "", 0, fmt.Errorf("count migrations: %w", err)
	}
	var filename, checksum string
	err := db.QueryRowContext(ctx, "SELECT filename, checksum FROM schema_migrations ORDER BY filename DESC LIMIT 1").Scan(&filename, &checksum)
	if err == sql.ErrNoRows {
		return "", "", count, nil
	}
	if err != nil {
		return "", "", 0, fmt.Errorf("read latest migration: %w", err)
	}
	return filename, checksum, count, nil
}

func snapshotSchemaState(ctx context.Context, path string) (string, string, int, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return "", "", 0, err
	}
	defer db.Close()
	return readSchemaState(ctx, db)
}

func (s *Store) Backup(ctx context.Context, directory string, keep int) (accounts.Backup, error) {
	if s == nil || s.db == nil {
		return accounts.Backup{}, fmt.Errorf("sqlite store unavailable")
	}
	directory = filepath.Clean(strings.TrimSpace(directory))
	if directory == "." || directory == "" {
		return accounts.Backup{}, fmt.Errorf("backup directory required")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return accounts.Backup{}, fmt.Errorf("create backup directory: %w", err)
	}
	createdAt := time.Now().UTC()
	name := "qoder-" + createdAt.Format("20060102T150405.000000000Z") + ".db"
	finalPath := filepath.Join(directory, name)
	tempPath := finalPath + ".tmp"
	manifestTemp := finalPath + backupManifestSuffix + ".tmp"
	_ = os.Remove(tempPath)
	_ = os.Remove(manifestTemp)

	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", tempPath); err != nil {
		return accounts.Backup{}, fmt.Errorf("snapshot sqlite: %w", err)
	}
	if err := verifySQLiteBackup(ctx, tempPath); err != nil {
		_ = os.Remove(tempPath)
		return accounts.Backup{}, err
	}
	if err := os.Chmod(tempPath, 0o600); err != nil {
		_ = os.Remove(tempPath)
		return accounts.Backup{}, fmt.Errorf("secure sqlite backup: %w", err)
	}
	// The manifest is built from the verified temp file and written before the
	// snapshot is published, so a snapshot never appears without its record.
	manifest, err := s.buildManifest(ctx, tempPath, name, createdAt)
	if err != nil {
		_ = os.Remove(tempPath)
		return accounts.Backup{}, err
	}
	if err := writeManifest(manifestTemp, manifest); err != nil {
		_ = os.Remove(tempPath)
		_ = os.Remove(manifestTemp)
		return accounts.Backup{}, err
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		_ = os.Remove(tempPath)
		_ = os.Remove(manifestTemp)
		return accounts.Backup{}, fmt.Errorf("publish sqlite backup: %w", err)
	}
	if err := os.Rename(manifestTemp, finalPath+backupManifestSuffix); err != nil {
		_ = os.Remove(finalPath)
		_ = os.Remove(manifestTemp)
		return accounts.Backup{}, fmt.Errorf("publish sqlite backup manifest: %w", err)
	}
	if keep <= 0 {
		keep = 5
	}
	if err := pruneSQLiteBackups(directory, keep); err != nil {
		return accounts.Backup{}, err
	}
	return accounts.Backup{Name: name, Path: finalPath, CreatedAt: createdAt}, nil
}

func (s *Store) buildManifest(ctx context.Context, databasePath, name string, createdAt time.Time) (BackupManifest, error) {
	info, err := os.Stat(databasePath)
	if err != nil {
		return BackupManifest{}, fmt.Errorf("stat sqlite snapshot: %w", err)
	}
	sum, err := fileSHA256(databasePath)
	if err != nil {
		return BackupManifest{}, err
	}
	migration, checksum, count, err := snapshotSchemaState(ctx, databasePath)
	if err != nil {
		return BackupManifest{}, err
	}
	return BackupManifest{
		Name: name, CreatedAt: createdAt.Format(time.RFC3339Nano),
		AppVersion: buildinfo.Version, AppCommit: buildinfo.Commit,
		SchemaMigration: migration, SchemaChecksum: checksum, SchemaCount: count,
		SizeBytes: info.Size(), SHA256: sum, IntegrityCheck: "ok",
	}, nil
}

func writeManifest(path string, manifest BackupManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode backup manifest: %w", err)
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(filepath.Dir(path), ".manifest-*")
	if err != nil {
		return fmt.Errorf("create manifest temp file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write backup manifest: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("publish backup manifest: %w", err)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open sqlite snapshot: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash sqlite snapshot: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// VerifyBackup re-verifies one published snapshot against its manifest: the
// file hash, the recorded schema state, and SQLite's own integrity check. A
// restore path must call this before overwriting a live database.
func (s *Store) VerifyBackup(ctx context.Context, path string) (BackupManifest, error) {
	raw, err := os.ReadFile(path + backupManifestSuffix)
	if err != nil {
		return BackupManifest{}, fmt.Errorf("read backup manifest: %w", err)
	}
	var manifest BackupManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return BackupManifest{}, fmt.Errorf("decode backup manifest: %w", err)
	}
	if manifest.SHA256 == "" {
		return BackupManifest{}, fmt.Errorf("backup manifest has no checksum")
	}
	sum, err := fileSHA256(path)
	if err != nil {
		return BackupManifest{}, err
	}
	if !strings.EqualFold(sum, manifest.SHA256) {
		return BackupManifest{}, fmt.Errorf("backup checksum mismatch")
	}
	if err := verifySQLiteBackup(ctx, path); err != nil {
		return BackupManifest{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return BackupManifest{}, err
	}
	migration, checksum, count, err := snapshotSchemaState(ctx, path)
	if err != nil {
		return BackupManifest{}, err
	}
	if manifest.Name != filepath.Base(path) || manifest.SizeBytes != info.Size() || manifest.IntegrityCheck != "ok" ||
		manifest.SchemaMigration != migration || manifest.SchemaChecksum != checksum || manifest.SchemaCount != count {
		return BackupManifest{}, fmt.Errorf("backup manifest metadata does not match snapshot")
	}
	return manifest, nil
}

func verifySQLiteBackup(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("open sqlite backup: %w", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("verify sqlite backup: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("verify sqlite backup: %s", result)
	}
	return nil
}

func pruneSQLiteBackups(directory string, keep int) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("list sqlite backups: %w", err)
	}
	type candidate struct {
		name string
		time time.Time
	}
	backups := make([]candidate, 0, len(entries))
	snapshots := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "qoder-") || strings.HasPrefix(entry.Name(), "qoder-pre-rollback-") || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		snapshots[entry.Name()] = true
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect sqlite backup: %w", err)
		}
		backups = append(backups, candidate{name: entry.Name(), time: info.ModTime()})
	}
	// Manifests whose snapshot no longer exists are orphans; remove them so a
	// later restore cannot pair a record with the wrong file.
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), backupManifestSuffix) {
			continue
		}
		snapshot := strings.TrimSuffix(entry.Name(), backupManifestSuffix)
		if !snapshots[snapshot] {
			if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil {
				return fmt.Errorf("prune orphan backup manifest %s: %w", entry.Name(), err)
			}
		}
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i].time.After(backups[j].time) })
	if len(backups) <= keep {
		return nil
	}
	for _, backup := range backups[keep:] {
		if err := os.Remove(filepath.Join(directory, backup.name)); err != nil {
			return fmt.Errorf("prune sqlite backup %s: %w", backup.name, err)
		}
		if err := os.Remove(filepath.Join(directory, backup.name+backupManifestSuffix)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("prune sqlite backup manifest %s: %w", backup.name, err)
		}
	}
	return nil
}
