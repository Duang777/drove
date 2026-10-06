//go:build !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package workspace

import (
	"context"
	"os"
)

func finalizePreparedWorktreeAdd(
	_ context.Context,
	_ repositoryCapability,
	_ string,
	_ *os.Root,
	_ bool,
) error {
	return nil
}

func repairBoundPreparedWorktreeRegistration(
	_ context.Context,
	_ Workspace,
	_ *preparedWorktreeTarget,
	_ repositoryCapability,
	_ string,
) (bool, error) {
	return false, nil
}

func cleanupPreparedWorktreeAddDebris(
	_ context.Context,
	_ repositoryCapability,
	_ Workspace,
	_ workspaceRecord,
) error {
	return nil
}
