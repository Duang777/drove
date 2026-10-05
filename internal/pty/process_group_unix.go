//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package pty

import (
	"errors"
	"syscall"
)

type processGroupSignalFunc func(pid int, signal syscall.Signal) error

func unixProcessGroupSignal(pid int, signal syscall.Signal) error {
	return syscall.Kill(-pid, signal)
}

func terminateProcessGroup(pid int) error {
	return terminateProcessGroupWith(unixProcessGroupSignal, pid)
}

func terminateProcessGroupWith(signal processGroupSignalFunc, pid int) error {
	err := signal(pid, syscall.SIGTERM)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func killProcessGroup(pid int) error {
	_, err := killProcessGroupWith(unixProcessGroupSignal, pid)
	return err
}

func killProcessGroupWith(
	signal processGroupSignalFunc,
	pid int,
) (bool, error) {
	err := signal(pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return true, nil
	}
	return false, err
}

func processGroupAlive(pid int) (bool, error) {
	return processGroupAliveWith(unixProcessGroupSignal, pid)
}

func processGroupAliveWith(signal processGroupSignalFunc, pid int) (bool, error) {
	err := signal(pid, 0)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.EPERM):
		// EPERM proves neither ownership nor disappearance. Keep polling until
		// ESRCH or the caller's bounded grace period resolves the ambiguity.
		return true, nil
	case errors.Is(err, syscall.ESRCH):
		return false, nil
	default:
		return false, err
	}
}
