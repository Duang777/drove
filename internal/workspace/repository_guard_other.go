//go:build !windows

package workspace

import "os"

type repositoryGuard struct{}

func openRepositoryGuard(
	_ string,
	_ *os.Root,
) (*repositoryGuard, error) {
	return &repositoryGuard{}, nil
}

func (g *repositoryGuard) Close() error {
	return nil
}
