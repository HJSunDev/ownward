//go:build windows

package codexplugin

import (
	"errors"
	"golang.org/x/sys/windows"
	"os/exec"
	"syscall"
	"unsafe"
)

type organizationLifetime struct{ handle windows.Handle }

func configureOrganizationProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000 | 0x00004000 | 0x4} // Hidden, below-normal, suspended until its job owns the whole tree.
}

// Preserve free physical memory for foreground applications before admitting work.
func OrganizationCapacity(p OrganizationProfile) error {
	var s struct {
		Length, Load                                                                         uint32
		Total, Available, TotalPage, AvailablePage, TotalVirtual, AvailableVirtual, Extended uint64
	}
	s.Length = uint32(unsafe.Sizeof(s))
	r, _, e := windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx").Call(uintptr(unsafe.Pointer(&s)))
	if r == 0 {
		return e
	}
	reserve := s.Total / 10
	if reserve < 512<<20 {
		reserve = 512 << 20
	}
	if s.Available < (uint64(p.MemoryMiB)<<20)+reserve {
		return errors.New("本机余量不足，组织待办保留")
	}
	return nil
}
func attachOrganizationProcess(cmd *exec.Cmd, mib int) (*organizationLifetime, error) {
	h, e := windows.CreateJobObject(nil, nil)
	if e != nil {
		return nil, e
	}
	fail := func(e error) (*organizationLifetime, error) { windows.CloseHandle(h); return nil, e }
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_JOB_MEMORY
	limits.JobMemoryLimit = uintptr(mib) << 20
	if _, e = windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); e != nil {
		return fail(e)
	}
	p, e := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|0x0800, false, uint32(cmd.Process.Pid))
	if e != nil {
		return fail(e)
	}
	defer windows.CloseHandle(p)
	if e = windows.AssignProcessToJobObject(h, p); e != nil {
		return fail(e)
	}
	status, _, _ := windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess").Call(uintptr(p))
	if status != 0 {
		return fail(errors.New("无法启动受资源保护的组织进程"))
	}
	return &organizationLifetime{h}, nil
}
func (j *organizationLifetime) close() {
	if j != nil && j.handle != 0 {
		windows.CloseHandle(j.handle)
		j.handle = 0
	}
}
func (j *organizationLifetime) resources() (uint64, uint32) {
	if j == nil || j.handle == 0 {
		return 0, 0
	}
	var m windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	var a struct {
		TotalUser, TotalKernel, PeriodUser, PeriodKernel                 int64
		PageFaults, TotalProcesses, ActiveProcesses, TerminatedProcesses uint32
	}
	windows.QueryInformationJobObject(j.handle, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&m)), uint32(unsafe.Sizeof(m)), nil)
	windows.QueryInformationJobObject(j.handle, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&a)), uint32(unsafe.Sizeof(a)), nil)
	return uint64(m.PeakJobMemoryUsed), a.TotalProcesses
}
