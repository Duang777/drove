//go:build linux || windows

package workspace

import (
	"context"
	"os"
	"os/exec"
)

func rootedRepairWorktreeGitCommand(
	ctx context.Context,
	git string,
	worktreePath string,
	worktreeRoot *os.Root,
	gitPath string,
	gitRoot *os.Root,
	commonPath string,
	commonRoot *os.Root,
) (*exec.Cmd, func() error, error) {
	return rootedWorktreeGitCommand(
		ctx,
		git,
		worktreePath,
		worktreeRoot,
		gitPath,
		gitRoot,
		commonPath,
		commonRoot,
		[]string{"worktree", "repair", "."},
	)
}
