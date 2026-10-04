//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package pty

import (
	"errors"
	"syscall"
)

func terminateProcessGroup(pid int) error {
	err := syscall.Kill(-pid, syscall.SIGTERM)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func killProcessGroup(pid int) error {
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func processGroupAlive(pid int) (bool, error) {
	err := syscall.Kill(-pid, 0)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.ESRCH), errors.Is(err, syscall.EPERM):
		return false, nil
	default:
		return false, err
	}
}
