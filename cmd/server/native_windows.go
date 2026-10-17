//go:build windows

package main

import (
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"unsafe"
)

// The native gateway owns only its own descendant workers. Closing this process
// closes the job handle and terminates those workers, never unrelated Node jobs.
func nativeWorkerJob() (func(), error) {
	if os.Getenv("UPDATE_INSTANCE_ID") == "" {
		return func() {}, nil
	}
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	if err == nil {
		err = windows.AssignProcessToJobObject(h, windows.CurrentProcess())
	}
	if err != nil {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("create native worker ownership job: %w", err)
	}
	// Keep the handle until process exit. Closing it in a defer would also
	// terminate this process and turn an orderly shutdown into a task failure.
	return func() {}, nil
}
