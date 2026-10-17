//go:build windows

package updater

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"golang.org/x/sys/windows"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

func nativeImage(pid int) (string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, 32768)
	n := uint32(len(buf))
	if err = windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:n]), nil
}
func verifyNativeProcess(pid int, expected string) error {
	got, err := nativeImage(pid)
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Clean(got), filepath.Clean(expected)) {
		return fmt.Errorf("health PID executable does not belong to configured instance")
	}
	return nil
}
func nativeProcessAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	return windows.GetExitCodeProcess(h, &code) == nil && code == 259
}
func terminateNativeProcess(pid int, expected string) error {
	if err := verifyNativeProcess(pid, expected); err != nil {
		return err
	}
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	if err = windows.TerminateProcess(h, 1); err != nil {
		return err
	}
	state, err := windows.WaitForSingleObject(h, 10000)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("gateway process did not stop")
	}
	return nil
}
func findNativeProcess(expected string) int {
	h, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(h)
	var p windows.ProcessEntry32
	p.Size = uint32(unsafe.Sizeof(p))
	found := 0
	for err = windows.Process32First(h, &p); err == nil; err = windows.Process32Next(h, &p) {
		if verifyNativeProcess(int(p.ProcessID), expected) == nil {
			if found != 0 {
				return -1
			}
			found = int(p.ProcessID)
		}
	}
	return found
}
func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
func runNativePowerShell(ctx context.Context, script string) error {
	units := utf16.Encode([]rune("$ErrorActionPreference='Stop'; " + script))
	raw := make([]byte, len(units)*2)
	for i, u := range units {
		binary.LittleEndian.PutUint16(raw[i*2:], u)
	}
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(raw))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if _, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("Windows process manager operation failed: %w (check task/service permissions and configured action)", err)
	}
	return nil
}

type nativeChild struct{ done chan struct{} }

var nativeChildren sync.Map

func hideNativeCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}
func controlNativeManager(ctx context.Context, c NativeConfig, start bool) error {
	if c.Manager == "process" {
		if !start {
			if child, ok := nativeChildren.Load(c.LaunchConfig); ok {
				select {
				case <-child.(*nativeChild).done:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		}
		cmd := exec.Command(c.Launcher, "--run-native-gateway", c.LaunchConfig)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
		if err := cmd.Start(); err != nil {
			return err
		}
		child := &nativeChild{done: make(chan struct{})}
		nativeChildren.Store(c.LaunchConfig, child)
		go func() { _ = cmd.Wait(); close(child.done); nativeChildren.CompareAndDelete(c.LaunchConfig, child) }()
		return nil
	}
	verb := "Stop"
	if start {
		verb = "Start"
	}

	return runNativePowerShell(ctx, "$t=Get-ScheduledTask -TaskName "+psQuote(c.TargetName)+"; if(@($t).Count -ne 1 -or @($t.Actions).Count -ne 1 -or !$t.Actions[0].Arguments.Contains("+psQuote(c.LaunchConfig)+") -or !$t.Actions[0].Execute.Equals("+psQuote(c.Launcher)+",[StringComparison]::OrdinalIgnoreCase)){throw 'Task action does not match configured launcher'}; "+verb+"-ScheduledTask -InputObject $t")
}

func acquireNativeLock(directory string) (func(), error) {
	return acquireNativePathLock(filepath.Join(directory, "native.lock"))
}
func acquireNativePathLock(p string) (func(), error) {
	name, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, fmt.Errorf("native updater already owns this installation or cannot lock it: %w", err)
	}
	return func() { windows.CloseHandle(h) }, nil
}
