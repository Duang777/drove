//go:build linux || windows

package workspace

import (
	"context"
	"os"
)

func checkoutPreparedWorktree(
	ctx context.Context,
	repository repositoryCapability,
	worktreePath string,
	worktreeRoot *os.Root,
	expectedHeadOID string,
) error {
	_, err := repository.runWorktreeAt(
		ctx,
		worktreePath,
		worktreeRoot,
		"",
		"read-tree",
		"--reset",
		"-u",
		expectedHeadOID,
	)
	return err
}
