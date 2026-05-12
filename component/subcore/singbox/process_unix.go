//go:build !windows && !linux

package singbox

import (
	"errors"
	"os/exec"
	"syscall"
)

type managedProcess struct{}

func prepareCheckCommand(*exec.Cmd) {}

func prepareStartCommand(cmd *exec.Cmd) (*managedProcess, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &managedProcess{}, nil
}

func afterStartCommand(*managedProcess, *exec.Cmd) error {
	return nil
}

func terminateCommand(cmd *exec.Cmd, _ *managedProcess) error {
	return signalPID(cmd.Process.Pid, syscall.SIGTERM)
}

func killCommand(cmd *exec.Cmd, _ *managedProcess) error {
	return signalPID(cmd.Process.Pid, syscall.SIGKILL)
}

func cleanupManagedProcess(*managedProcess) {}

func terminatePID(pid int) error {
	return signalPID(pid, syscall.SIGTERM)
}

func killPID(pid int) error {
	return signalPID(pid, syscall.SIGKILL)
}

func signalPID(pid int, signal syscall.Signal) error {
	if pid <= 0 {
		return nil
	}
	err := syscall.Kill(-pid, signal)
	if err == syscall.ESRCH {
		return syscall.Kill(pid, signal)
	}
	return err
}

func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func processPath(int) (string, error) {
	return "", errors.New("process path is not supported on this platform")
}
