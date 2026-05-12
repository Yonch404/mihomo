//go:build windows

package singbox

import (
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	processSynchronize = 0x00100000
	waitTimeout        = 258
)

type managedProcess struct {
	job windows.Handle
}

func prepareCheckCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

func prepareStartCommand(cmd *exec.Cmd) (*managedProcess, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_BREAKAWAY_FROM_JOB,
	}
	return &managedProcess{job: job}, nil
}

func afterStartCommand(handle *managedProcess, cmd *exec.Cmd) error {
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	return windows.AssignProcessToJobObject(handle.job, process)
}

func terminateCommand(cmd *exec.Cmd, _ *managedProcess) error {
	return cmd.Process.Kill()
}

func killCommand(cmd *exec.Cmd, handle *managedProcess) error {
	if handle != nil && handle.job != 0 {
		_ = windows.CloseHandle(handle.job)
		handle.job = 0
	}
	return cmd.Process.Kill()
}

func cleanupManagedProcess(handle *managedProcess) {
	if handle != nil && handle.job != 0 {
		_ = windows.CloseHandle(handle.job)
		handle.job = 0
	}
}

func terminatePID(pid int) error {
	process, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	return windows.TerminateProcess(process, 1)
}

func killPID(pid int) error {
	return terminatePID(pid)
}

func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := windows.OpenProcess(processSynchronize, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(process)
	event, err := windows.WaitForSingleObject(process, 0)
	return err == nil && event == waitTimeout
}

func processPath(pid int) (string, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(process)

	buffer := make([]uint16, syscall.MAX_LONG_PATH)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return "", err
	}
	return syscall.UTF16ToString(buffer[:size]), nil
}
