//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package workspace

import (
	"context"
)

func preparePreparedWorktreeAdd(
	_ Workspace,
	_ *preparedWorktreeTarget,
	_ repositoryCapability,
) (bool, error) {
	return false, nil
}

func finalizePreparedWorktreeAdd(
	_ context.Context,
	_ repositoryCapability,
	_ Workspace,
	_ *preparedWorktreeTarget,
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
