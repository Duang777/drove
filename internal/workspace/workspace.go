// Package workspace manages Drove-owned Git worktrees.
package workspace

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
)

const (
	worktreeDirectory = "worktrees"
	repositoryHashLen = 16
)

var (
	// ErrNotRepository means the requested source directory is not in a Git worktree.
	ErrNotRepository = errors.New("workspace: source is not a Git repository")
	// ErrDirty means a managed worktree has uncommitted changes.
	ErrDirty = errors.New("workspace: worktree has uncommitted changes")
	// ErrNotFound means no managed worktree matches the requested Agent ID.
	ErrNotFound = errors.New("workspace: managed worktree not found")
)

// Workspace describes one Drove-managed Git worktree.
type Workspace struct {
	AgentID    string `json:"agent_id"`
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Branch     string `json:"branch"`
	Dirty      bool   `json:"dirty"`

	createdBranch bool
	sourcePath    string
}

// Manager owns worktrees below one Drove data directory.
type Manager struct {
	root string
	git  string
}

// New creates a worktree manager without changing the filesystem.
func New(dataDir string) (*Manager, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("workspace: data directory is empty")
	}
	absolute, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("workspace: resolve data directory %q: %w", dataDir, err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("workspace: find git: %w", err)
	}
	return &Manager{
		root: filepath.Join(filepath.Clean(absolute), worktreeDirectory),
		git:  git,
	}, nil
}

// Prepare creates a worktree for one Agent. An empty branch gets a stable default.
func (m *Manager) Prepare(
	ctx context.Context,
	repository string,
	branch string,
	agentID string,
) (Workspace, error) {
	if err := validateAgentID(agentID); err != nil {
		return Workspace{}, err
	}
	sourcePath, repository, err := m.repositoryPaths(ctx, repository)
	if err != nil {
		return Workspace{}, err
	}
	if branch == "" {
		branch = "drove/" + agentID
	}
	if err := m.validateBranch(ctx, branch); err != nil {
		return Workspace{}, err
	}
	if err := ensureDirectory(m.root); err != nil {
		return Workspace{}, err
	}
	bucket := filepath.Join(m.root, repositoryHash(repository))
	if err := ensureDirectory(bucket); err != nil {
		return Workspace{}, err
	}
	path := filepath.Join(bucket, agentID)
	if err := ensureAbsent(path); err != nil {
		return Workspace{}, err
	}

	exists, err := m.branchExists(ctx, repository, branch)
	if err != nil {
		return Workspace{}, err
	}
	arguments := []string{"-C", sourcePath, "worktree", "add", "--quiet"}
	if !exists {
		arguments = append(arguments, "-b", branch)
	}
	arguments = append(arguments, path)
	if !exists {
		arguments = append(arguments, "HEAD")
	} else {
		arguments = append(arguments, branch)
	}
	if _, err := m.run(ctx, arguments...); err != nil {
		_ = removeEmptyDirectory(bucket)
		return Workspace{}, fmt.Errorf("workspace: create worktree: %w", err)
	}

	result := Workspace{
		AgentID:       agentID,
		Repository:    repository,
		Path:          path,
		Branch:        branch,
		createdBranch: !exists,
		sourcePath:    sourcePath,
	}
	if err := m.copyIncludedFiles(ctx, result); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return Workspace{}, errors.Join(err, m.Discard(cleanupCtx, result))
	}
	return result, nil
}

// List returns all valid worktrees below this manager's root.
func (m *Manager) List(ctx context.Context) ([]Workspace, error) {
	info, err := os.Lstat(m.root)
	if errors.Is(err, os.ErrNotExist) {
		return []Workspace{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("workspace: inspect worktree root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("workspace: worktree root %q is not a real directory", m.root)
	}
	buckets, err := os.ReadDir(m.root)
	if err != nil {
		return nil, fmt.Errorf("workspace: read worktree root: %w", err)
	}

	var result []Workspace
	for _, bucket := range buckets {
		if !bucket.IsDir() || !validRepositoryHash(bucket.Name()) {
			continue
		}
		bucketPath := filepath.Join(m.root, bucket.Name())
		entries, err := os.ReadDir(bucketPath)
		if err != nil {
			return nil, fmt.Errorf(
				"workspace: read repository bucket %q: %w",
				bucket.Name(),
				err,
			)
		}
		for _, entry := range entries {
			if !entry.IsDir() || validateAgentID(entry.Name()) != nil {
				continue
			}
			current, err := m.inspect(ctx, filepath.Join(bucketPath, entry.Name()))
			if err != nil {
				return nil, err
			}
			if repositoryHash(current.Repository) != bucket.Name() {
				return nil, fmt.Errorf(
					"workspace: repository hash mismatch for %q",
					current.Path,
				)
			}
			result = append(result, current)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Repository == result[j].Repository {
			return result[i].AgentID < result[j].AgentID
		}
		return result[i].Repository < result[j].Repository
	})
	return result, nil
}

// Cleanup removes one managed worktree and preserves its branch.
func (m *Manager) Cleanup(
	ctx context.Context,
	agentID string,
	force bool,
) (Workspace, error) {
	if err := validateAgentID(agentID); err != nil {
		return Workspace{}, err
	}
	all, err := m.List(ctx)
	if err != nil {
		return Workspace{}, err
	}
	var matches []Workspace
	for _, current := range all {
		if current.AgentID == agentID {
			matches = append(matches, current)
		}
	}
	if len(matches) == 0 {
		return Workspace{}, fmt.Errorf("%w: agent %q", ErrNotFound, agentID)
	}
	if len(matches) > 1 {
		return Workspace{}, fmt.Errorf(
			"workspace: agent %q has %d managed worktrees",
			agentID,
			len(matches),
		)
	}
	target := matches[0]
	if target.Dirty && !force {
		return Workspace{}, fmt.Errorf("%w: %s", ErrDirty, target.Path)
	}
	arguments := []string{"-C", target.Repository, "worktree", "remove"}
	if force {
		arguments = append(arguments, "--force")
	}
	arguments = append(arguments, target.Path)
	if _, err := m.run(ctx, arguments...); err != nil {
		return Workspace{}, fmt.Errorf("workspace: remove worktree: %w", err)
	}
	if err := removeEmptyDirectory(filepath.Dir(target.Path)); err != nil {
		return Workspace{}, err
	}
	return target, nil
}

// Discard rolls back a prepared worktree before its session metadata commits.
func (m *Manager) Discard(ctx context.Context, target Workspace) error {
	if err := m.validateManagedPath(target); err != nil {
		return err
	}
	var result error
	if _, err := os.Lstat(target.Path); err == nil {
		if _, err := m.run(
			ctx,
			"-C",
			target.Repository,
			"worktree",
			"remove",
			"--force",
			target.Path,
		); err != nil {
			result = errors.Join(
				result,
				fmt.Errorf("workspace: discard worktree: %w", err),
			)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		result = errors.Join(
			result,
			fmt.Errorf("workspace: inspect discarded worktree: %w", err),
		)
	}
	if target.createdBranch {
		if _, err := m.run(
			ctx,
			"-C",
			target.Repository,
			"branch",
			"-D",
			target.Branch,
		); err != nil {
			result = errors.Join(
				result,
				fmt.Errorf("workspace: discard branch: %w", err),
			)
		}
	}
	if err := removeEmptyDirectory(filepath.Dir(target.Path)); err != nil {
		result = errors.Join(result, err)
	}
	return result
}

func (m *Manager) inspect(ctx context.Context, path string) (Workspace, error) {
	agentID := filepath.Base(path)
	commonDir, err := m.run(ctx, "-C", path, "rev-parse", "--git-common-dir")
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace: inspect repository for %q: %w", path, err)
	}
	commonPath := strings.TrimSpace(string(commonDir))
	if !filepath.IsAbs(commonPath) {
		commonPath = filepath.Join(path, commonPath)
	}
	commonPath, err = filepath.EvalSymlinks(filepath.Clean(commonPath))
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace: resolve Git directory for %q: %w", path, err)
	}
	if filepath.Base(commonPath) != ".git" {
		return Workspace{}, fmt.Errorf(
			"%w: managed worktree %q uses a bare repository",
			ErrNotRepository,
			path,
		)
	}
	repository := filepath.Dir(commonPath)
	branchOutput, err := m.run(ctx, "-C", path, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace: inspect branch for %q: %w", path, err)
	}
	status, err := m.run(
		ctx,
		"-C",
		path,
		"status",
		"--porcelain=v1",
		"--untracked-files=all",
	)
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace: inspect status for %q: %w", path, err)
	}
	return Workspace{
		AgentID:    agentID,
		Repository: repository,
		Path:       path,
		Branch:     strings.TrimSpace(string(branchOutput)),
		Dirty:      len(status) != 0,
	}, nil
}

func (m *Manager) repositoryPaths(
	ctx context.Context,
	path string,
) (string, string, error) {
	if path == "" {
		return "", "", fmt.Errorf("%w: source directory is empty", ErrNotRepository)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", "", fmt.Errorf("workspace: resolve repository %q: %w", path, err)
	}
	output, err := m.run(ctx, "-C", absolute, "rev-parse", "--show-toplevel")
	if err != nil {
		if ctx.Err() != nil {
			return "", "", fmt.Errorf(
				"workspace: inspect repository %q: %w",
				absolute,
				ctx.Err(),
			)
		}
		return "", "", fmt.Errorf("%w: %s", ErrNotRepository, absolute)
	}
	sourcePath := strings.TrimSpace(string(output))
	sourcePath, err = filepath.EvalSymlinks(sourcePath)
	if err != nil {
		return "", "", fmt.Errorf(
			"workspace: resolve repository root %q: %w",
			sourcePath,
			err,
		)
	}
	sourcePath = filepath.Clean(sourcePath)

	commonOutput, err := m.run(ctx, "-C", sourcePath, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", "", fmt.Errorf("workspace: inspect common Git directory: %w", err)
	}
	commonPath := strings.TrimSpace(string(commonOutput))
	if !filepath.IsAbs(commonPath) {
		commonPath = filepath.Join(sourcePath, commonPath)
	}
	commonPath, err = filepath.EvalSymlinks(filepath.Clean(commonPath))
	if err != nil {
		return "", "", fmt.Errorf(
			"workspace: resolve common Git directory %q: %w",
			commonPath,
			err,
		)
	}
	if filepath.Base(commonPath) != ".git" {
		return "", "", fmt.Errorf(
			"%w: source %q uses a bare repository",
			ErrNotRepository,
			sourcePath,
		)
	}
	return sourcePath, filepath.Dir(commonPath), nil
}

func (m *Manager) validateBranch(ctx context.Context, branch string) error {
	if branch == "" || strings.HasPrefix(branch, "-") {
		return fmt.Errorf("workspace: invalid branch %q", branch)
	}
	if _, err := m.run(ctx, "check-ref-format", "refs/heads/"+branch); err != nil {
		return fmt.Errorf("workspace: invalid branch %q: %w", branch, err)
	}
	return nil
}

func (m *Manager) branchExists(
	ctx context.Context,
	repository string,
	branch string,
) (bool, error) {
	command := exec.CommandContext(
		ctx,
		m.git,
		"-C",
		repository,
		"show-ref",
		"--verify",
		"--quiet",
		"refs/heads/"+branch,
	)
	err := command.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("workspace: inspect branch %q: %w", branch, err)
}

func (m *Manager) validateManagedPath(target Workspace) error {
	if err := validateAgentID(target.AgentID); err != nil {
		return err
	}
	want := filepath.Join(
		m.root,
		repositoryHash(target.Repository),
		target.AgentID,
	)
	if filepath.Clean(target.Path) != want {
		return fmt.Errorf("workspace: path %q is outside the managed root", target.Path)
	}
	return nil
}

func (m *Manager) run(ctx context.Context, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, m.git, arguments...)
	output, err := command.Output()
	if err == nil {
		return output, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		detail := strings.TrimSpace(string(exitErr.Stderr))
		if len(detail) > 4096 {
			detail = detail[:4096]
		}
		if detail != "" {
			return nil, fmt.Errorf("%s: %w", detail, err)
		}
	}
	return nil, err
}

func validateAgentID(agentID string) error {
	parsed, err := uuid.Parse(agentID)
	if err != nil || parsed.String() != agentID {
		return fmt.Errorf("workspace: invalid Agent ID %q", agentID)
	}
	return nil
}

func repositoryHash(repository string) string {
	digest := sha256.Sum256([]byte(repository))
	return fmt.Sprintf("%x", digest[:])[:repositoryHashLen]
}

func validRepositoryHash(value string) bool {
	if len(value) != repositoryHashLen {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func ensureDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("workspace: create directory %q: %w", path, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("workspace: inspect directory %q: %w", path, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("workspace: path %q is not a real directory", path)
	}
	return nil
}

func ensureAbsent(path string) error {
	_, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("workspace: inspect target %q: %w", path, err)
	default:
		return fmt.Errorf("workspace: target %q already exists", path)
	}
}

func removeEmptyDirectory(path string) error {
	err := os.Remove(path)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) {
		return nil
	}
	return fmt.Errorf("workspace: remove empty directory %q: %w", path, err)
}
