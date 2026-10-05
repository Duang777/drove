//go:build aix || darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd || solaris

package workspace

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

func configureIncludeManifestCommand(
	command *exec.Cmd,
	manifest *os.File,
	_ string,
) (string, bool, func() error, error) {
	fd := 3 + len(command.ExtraFiles)
	command.ExtraFiles = append(command.ExtraFiles, manifest)
	if runtime.GOOS == "linux" {
		return fmt.Sprintf("/proc/self/fd/%d", fd), true, noCleanup, nil
	}
	return fmt.Sprintf("/dev/fd/%d", fd), true, noCleanup, nil
}

func noCleanup() error {
	return nil
}
