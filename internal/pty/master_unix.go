//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package pty

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"golang.org/x/sys/unix"
)

func prepareDeadlineMaster(source *os.File) (*os.File, error) {
	duplicate, err := unix.FcntlInt(source.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("duplicate PTY master: %w", err)
	}
	closeDuplicate := true
	defer func() {
		if closeDuplicate {
			_ = unix.Close(duplicate)
		}
	}()

	if err := unix.SetNonblock(duplicate, true); err != nil {
		return nil, fmt.Errorf("set PTY master nonblocking: %w", err)
	}
	master := os.NewFile(uintptr(duplicate), source.Name())
	if master == nil {
		return nil, errors.New("wrap duplicated PTY master")
	}
	closeDuplicate = false

	if err := master.SetWriteDeadline(time.Now()); err != nil {
		_ = master.Close()
		return nil, fmt.Errorf("probe PTY write deadline: %w", err)
	}
	if err := master.SetWriteDeadline(time.Time{}); err != nil {
		_ = master.Close()
		return nil, fmt.Errorf("clear PTY write deadline probe: %w", err)
	}
	if err := source.Close(); err != nil {
		_ = master.Close()
		return nil, fmt.Errorf("close original PTY master: %w", err)
	}
	return master, nil
}

func cleanupFailedStart(cmd *exec.Cmd, master *os.File) error {
	var cleanupErrors []error
	if master != nil {
		if err := master.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("close PTY master: %w", err))
		}
	}
	if cmd == nil || cmd.Process == nil {
		return errors.Join(cleanupErrors...)
	}
	if err := killProcessGroup(cmd.Process.Pid); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("kill process group: %w", err))
		if directErr := cmd.Process.Kill(); directErr != nil &&
			!errors.Is(directErr, os.ErrProcessDone) {
			cleanupErrors = append(
				cleanupErrors,
				fmt.Errorf("kill direct process: %w", directErr),
			)
		}
	}
	if err := cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("reap process: %w", err))
		}
	}
	return errors.Join(cleanupErrors...)
}
