//go:build windows

package keepalive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var nativeGetProcessIDOfThread = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessIdOfThread")

// A suspended start closes the fork-before-assignment race. The unnamed job
// cannot be inherited and does not permit breakaway, so normal native wrapper
// children remain contained until their termination has been observed.
func runNativeCommand(ctx context.Context, command *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := nativeGetProcessIDOfThread.Find(); err != nil {
		return fmt.Errorf("locate Windows thread ownership API: %w", err)
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("create native process job: %w", err)
	}
	defer windows.CloseHandle(job)
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return fmt.Errorf("configure native process job: %w", err)
	}
	// Confirm the accounting API and structure before any process is created.
	if _, err := nativeWindowsActiveProcesses(job); err != nil {
		return fmt.Errorf("query native process job: %w", err)
	}

	var attrs syscall.SysProcAttr
	if command.SysProcAttr != nil {
		attrs = *command.SysProcAttr
	}
	attrs.CreationFlags |= windows.CREATE_SUSPENDED
	command.SysProcAttr = &attrs
	var setup sync.Mutex
	terminate := func() error {
		setup.Lock()
		defer setup.Unlock()
		jobErr := windows.TerminateJobObject(job, 1)
		// Cancellation can arrive after Start but before job assignment. Kill
		// the direct process as well, including while its thread is suspended.
		killErr := command.Process.Kill()
		if errors.Is(killErr, os.ErrProcessDone) {
			killErr = nil
		}
		return errors.Join(jobErr, killErr)
	}
	command.Cancel = terminate
	if err := command.Start(); err != nil {
		return err
	}

	err = func() error {
		setup.Lock()
		defer setup.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(command.Process.Pid))
		if err != nil {
			return fmt.Errorf("open suspended native process: %w", err)
		}
		defer windows.CloseHandle(process)
		if err := windows.AssignProcessToJobObject(job, process); err != nil {
			return fmt.Errorf("assign suspended native process to job: %w", err)
		}
		return resumeNativeWindowsProcess(ctx, uint32(command.Process.Pid))
	}()
	if err != nil {
		// Every setup failure kills and reaps the suspended child. Its exit
		// status is a consequence of cleanup, not a native CLI result.
		_ = terminate()
		_ = command.Wait()
		return errors.Join(err, drainNativeWindowsJob(job))
	}

	waitErr := command.Wait()
	// Wait has also joined os/exec's cancellation callback, so the job handle
	// stays valid throughout cancellation and no closer races the callback.
	// A successful wrapper exit still requires termination of its children.
	return errors.Join(waitErr, drainNativeWindowsJob(job))
}

func resumeNativeWindowsProcess(ctx context.Context, pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("inspect suspended native thread: %w", err)
	}
	defer windows.CloseHandle(snapshot)
	var entry windows.ThreadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	var threadID uint32
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.Size < uint32(unsafe.Offsetof(entry.OwnerProcessID)+unsafe.Sizeof(entry.OwnerProcessID)) {
			return errors.New("Windows thread snapshot omitted process ownership")
		}
		if entry.OwnerProcessID == pid {
			if threadID != 0 {
				return errors.New("suspended native process has multiple initial threads")
			}
			threadID = entry.ThreadID
		}
		entry.Size = uint32(unsafe.Sizeof(entry))
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return fmt.Errorf("enumerate suspended native threads: %w", err)
	}
	if threadID == 0 {
		return errors.New("suspended native process has no initial thread")
	}
	thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME|windows.THREAD_QUERY_LIMITED_INFORMATION, false, threadID)
	if err != nil {
		return fmt.Errorf("open suspended native thread: %w", err)
	}
	defer windows.CloseHandle(thread)
	// Verify the opened object, not just the snapshot's numeric thread ID.
	// An external termination could otherwise permit thread ID reuse.
	owner, _, _ := nativeGetProcessIDOfThread.Call(uintptr(thread))
	if owner != uintptr(pid) {
		return errors.New("native thread ownership changed before resume")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	previous, err := windows.ResumeThread(thread)
	if err != nil {
		return fmt.Errorf("resume contained native process: %w", err)
	}
	if previous != 1 {
		return errors.New("native initial thread had an unexpected suspend count")
	}
	return nil
}

// This layout is JOBOBJECT_BASIC_ACCOUNTING_INFORMATION from winnt.h. x/sys
// exposes the information class but does not define the corresponding struct.
type nativeWindowsJobAccounting struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

func nativeWindowsActiveProcesses(job windows.Handle) (uint32, error) {
	var info nativeWindowsJobAccounting
	err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil)
	return info.ActiveProcesses, err
}

func drainNativeWindowsJob(job windows.Handle) error {
	terminationErr := windows.TerminateJobObject(job, 1)
	deadline := time.Now().Add(5 * time.Second)
	for {
		active, queryErr := nativeWindowsActiveProcesses(job)
		if queryErr == nil && active == 0 {
			return nil
		}
		if !time.Now().Before(deadline) {
			// Closing the last handle remains the kill-on-close fallback. A
			// failed proof is reported rather than treated as successful renewal.
			return fmt.Errorf("%w: Windows job still has %d active processes (terminate: %v; query: %v)", errNativeCleanup, active, terminationErr, queryErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
