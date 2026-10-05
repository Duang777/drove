//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package pty

import (
	"errors"
	"syscall"
)

func terminateProcessGroup(pid int) error {
	err := syscall.Kill(-pid, syscall.SIGTERM)
	if processGroupUnavailable(err) {
		return nil
	}
	return err
}

func killProcessGroup(pid int) error {
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if processGroupUnavailable(err) {
		return nil
	}
	return err
}

// Darwin may return EPERM when a process group disappears between probing and
// signaling; it is also safer to leave a recycled, foreign group untouched.
func processGroupUnavailable(err error) bool {
	return errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.EPERM)
}

func processGroupAlive(pid int) (bool, error) {
	err := syscall.Kill(-pid, 0)
	switch {
	case err == nil:
		return true, nil
	case processGroupUnavailable(err):
		return false, nil
	default:
		return false, err
	}
}
