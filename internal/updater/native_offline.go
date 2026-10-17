package updater

import (
	"context"
	"fmt"
	"github.com/caigee-cmd/cli2api/internal/store"
	"os"
)

func UnpackNativePackage(archive, checksums, version, destination string) (NativeManifest, error) {
	var m NativeManifest
	info, err := os.Stat(archive)
	if err != nil {
		return m, err
	}
	if info.Size() > maxNativeBytes {
		return m, fmt.Errorf("application archive too large")
	}
	data, err := os.ReadFile(archive)
	if err != nil {
		return m, err
	}
	sums, err := os.ReadFile(checksums)
	if err != nil {
		return m, err
	}
	if err = verifyHostBinaryChecksum(NativeAsset, data, sums); err != nil {
		return m, err
	}
	return extractNative(archive, destination, version)
}
func SnapshotNativeDatabase(ctx context.Context, database, directory string) (string, error) {
	b, err := store.BackupDatabase(ctx, database, directory)
	if err != nil {
		return "", err
	}
	if _, err = new(store.Store).VerifyBackup(ctx, b.Path); err != nil {
		return "", err
	}
	return b.Path, nil
}
