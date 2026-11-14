//go:build windows

package procguard

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type processTree struct{ job, process windows.Handle }

var (
	jobKernel32         = windows.NewLazySystemDLL("kernel32.dll")
	setJobInformation   = jobKernel32.NewProc("SetInformationJobObject")
	queryJobInformation = jobKernel32.NewProc("QueryInformationJobObject")
)

func setProcAttr(cmd *exec.Cmd, _ *cgroupResources) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &windows.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
}

func newProcessTree(pid int, _ *cgroupResources) (*processTree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create job: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	// The x/sys uintptr wrappers lose compiler pointer tracking. Resolve the
	// address first and convert the buffer only in the direct SyscallN call.
	address := setJobInformation.Addr()
	// #nosec G103 -- Windows consumes this fixed-size ABI record synchronously.
	result, _, callErr := syscall.SyscallN(address, uintptr(job), uintptr(windows.JobObjectExtendedLimitInformation), uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	if result == 0 {
		if callErr == 0 {
			callErr = syscall.EINVAL
		}
		return nil, errors.Join(fmt.Errorf("configure job: %w", callErr), windows.CloseHandle(job))
	}
	//nolint:gosec // The OS-provided PID fits Windows' DWORD parameter.
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open child for job: %w", err), windows.CloseHandle(job))
	}
	if err = windows.AssignProcessToJobObject(job, process); err != nil {
		return nil, errors.Join(fmt.Errorf("assign child to job: %w", err), windows.CloseHandle(process), windows.CloseHandle(job))
	}
	return &processTree{job: job, process: process}, nil
}

func (t *processTree) resume() error {
	proc := windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")
	if err := proc.Find(); err != nil {
		return fmt.Errorf("find NtResumeProcess: %w", err)
	}
	status, _, _ := proc.Call(uintptr(t.process))
	if int32(status) < 0 {
		return fmt.Errorf("resume suspended child: NTSTATUS 0x%08x", uint32(status))
	}
	return nil
}

func (t *processTree) close() error {
	if err := errors.Join(windows.CloseHandle(t.job), windows.CloseHandle(t.process)); err != nil {
		return fmt.Errorf("close job handles: %w", err)
	}
	return nil
}
func (t *processTree) terminate() error { return nil }
func (t *processTree) kill() error {
	if err := windows.TerminateJobObject(t.job, 1); err != nil {
		return fmt.Errorf("terminate job: %w", err)
	}
	return nil
}

func (t *processTree) waitExited() error {
	_, err := windows.WaitForSingleObject(t.process, windows.INFINITE)
	if err != nil {
		return fmt.Errorf("observe child exit: %w", err)
	}
	return nil
}
func exitSignal(_ *os.ProcessState) os.Signal { return nil }

func killUnattached(process *os.Process) error {
	if err := process.Kill(); err != nil {
		return fmt.Errorf("kill unassigned child: %w", err)
	}
	return nil
}

type jobBasicAccountingInformation struct {
	TotalUserTime, TotalKernelTime                     int64
	ThisPeriodTotalUserTime, ThisPeriodTotalKernelTime int64
	TotalPageFaultCount, TotalProcesses                uint32
	ActiveProcesses, TotalTerminatedProcesses          uint32
}

func (t *processTree) remaining() (bool, error) {
	var info jobBasicAccountingInformation
	address := queryJobInformation.Addr()
	// #nosec G103 -- Direct SyscallN preserves the address of this local ABI buffer.
	result, _, callErr := syscall.SyscallN(address, uintptr(t.job), uintptr(windows.JobObjectBasicAccountingInformation), uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), 0)
	if result == 0 {
		if callErr == 0 {
			callErr = syscall.EINVAL
		}
		return false, fmt.Errorf("query job active processes: %w", callErr)
	}
	return info.ActiveProcesses != 0, nil
}
