//go:build !windows

package updater

import (
	"context"
	"fmt"
	"os/exec"
)

func verifyNativeProcess(int, string) error {
	return fmt.Errorf("Windows native process control unavailable")
}
func nativeProcessAlive(int) bool              { return false }
func terminateNativeProcess(int, string) error { return fmt.Errorf("Windows only") }
func findNativeProcess(string) int             { return 0 }
func controlNativeManager(context.Context, NativeConfig, bool) error {
	return fmt.Errorf("Windows only")
}

func acquireNativeLock(string) (func(), error) { return nil, fmt.Errorf("Windows only") }

func hideNativeCommand(*exec.Cmd)                  {}
func acquireNativePathLock(string) (func(), error) { return nil, fmt.Errorf("Windows only") }
