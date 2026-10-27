//go:build windows

package mcp

import (
	"os/exec"
	"syscall"
	"unsafe"
)

const (
	createNoWindow               = 0x08000000
	jobObjectExtendedLimitInfo   = 9
	jobObjectLimitKillOnJobClose = 0x00002000
	processSetQuotaAndTerminate  = 0x0100 | 0x0001
)

var (
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObjectW         = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
	procTerminateJobObject       = kernel32.NewProc("TerminateJobObject")
)

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type jobExtendedLimitInformation struct {
	BasicLimitInformation jobBasicLimitInformation
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

func configureStdioCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}

// processTree owns a Job Object configured to kill every process in it when
// the handle closes, so wrapper launchers (cmd, python, npx, go run) cannot
// leave orphaned grandchildren behind.
type processTree struct {
	job syscall.Handle
}

func attachProcessTree(cmd *exec.Cmd) *processTree {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	job, _, _ := procCreateJobObjectW.Call(0, 0)
	if job == 0 {
		return nil
	}
	var info jobExtendedLimitInformation
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	ok, _, _ := procSetInformationJobObject.Call(job, jobObjectExtendedLimitInfo, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	if ok == 0 {
		_ = syscall.CloseHandle(syscall.Handle(job))
		return nil
	}
	process, err := syscall.OpenProcess(processSetQuotaAndTerminate, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = syscall.CloseHandle(syscall.Handle(job))
		return nil
	}
	defer syscall.CloseHandle(process)
	ok, _, _ = procAssignProcessToJobObject.Call(job, uintptr(process))
	if ok == 0 {
		_ = syscall.CloseHandle(syscall.Handle(job))
		return nil
	}
	return &processTree{job: syscall.Handle(job)}
}

func (p *processTree) kill(cmd *exec.Cmd) {
	if p != nil && p.job != 0 {
		_, _, _ = procTerminateJobObject.Call(uintptr(p.job), 1)
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func (p *processTree) release() {
	if p == nil || p.job == 0 {
		return
	}
	_ = syscall.CloseHandle(p.job)
	p.job = 0
}
