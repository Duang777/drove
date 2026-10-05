package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (m *Manager) initializePreparedWorktree(
	ctx context.Context,
	target *Workspace,
) (result error) {
	root, err := openRealPathRoot(target.Path)
	if err != nil {
		return fmt.Errorf("workspace: open prepared worktree: %w", err)
	}
	defer func() {
		result = errors.Join(result, root.Close())
	}()

	directoryIdentity, err := openedDirectoryIdentity(root)
	if err != nil {
		return fmt.Errorf(
			"workspace: inspect prepared worktree identity: %w",
			err,
		)
	}
	gitDirectory, err := m.worktreeGitDirectoryAtRoot(
		ctx,
		target.Path,
		root,
	)
	if err != nil {
		return err
	}
	if err := verifyRealPathRoot(target.Path, root); err != nil {
		return err
	}
	target.gitDirectory = gitDirectory
	target.directoryIdentity = directoryIdentity

	record, exists, err := m.readWorkspaceRecord(target.Path)
	if err == nil && !exists {
		err = errors.New("workspace: worktree identity record is missing")
	}
	if err == nil {
		record.GitDirectory = gitDirectory
		record.DirectoryIdentity = directoryIdentity
		err = m.replaceWorkspaceRecord(record)
	}
	if err != nil {
		return fmt.Errorf("workspace: persist worktree identity: %w", err)
	}
	if _, err := m.runRootedGit(
		ctx,
		target.Path,
		root,
		"reset",
		"--hard",
	); err != nil {
		return fmt.Errorf("workspace: checkout prepared worktree: %w", err)
	}
	return nil
}

func (m *Manager) worktreeGitDirectoryAtRoot(
	ctx context.Context,
	path string,
	root *os.Root,
) (string, error) {
	output, err := m.runRootedGit(
		ctx,
		path,
		root,
		"rev-parse",
		"--absolute-git-dir",
	)
	if err != nil {
		return "", fmt.Errorf(
			"workspace: inspect Git directory for %q: %w",
			path,
			err,
		)
	}
	gitDirectory := strings.TrimSpace(string(output))
	if !filepath.IsAbs(gitDirectory) {
		return "", fmt.Errorf(
			"workspace: Git directory for %q is not absolute",
			path,
		)
	}
	gitDirectory, err = resolvePath(gitDirectory)
	if err != nil {
		return "", fmt.Errorf(
			"workspace: resolve Git directory for %q: %w",
			path,
			err,
		)
	}
	return gitDirectory, nil
}
