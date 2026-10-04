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
	"sync"
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
	// ErrInvalidBranch means a requested local branch name is invalid.
	ErrInvalidBranch = errors.New("workspace: invalid branch")
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
	Detached   bool   `json:"detached,omitempty"`
	Missing    bool   `json:"missing,omitempty"`

	createdBranch bool
	sourcePath    string
}

// Manager owns worktrees below one Drove data directory.
type Manager struct {
	root string
	git  string
	mu   sync.Mutex
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
	absolute, err = resolvePath(absolute)
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
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.prepare(ctx, repository, branch, agentID)
}

func (m *Manager) prepare(
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
	if err := ensureAbsent(workspaceRecordPath(path)); err != nil {
		return Workspace{}, err
	}

	exists, err := m.branchExists(ctx, repository, branch)
	if err != nil {
		return Workspace{}, err
	}
	createdBranch := !exists
	if createdBranch {
		if _, err := m.run(
			ctx,
			"-C",
			sourcePath,
			"branch",
			branch,
			"HEAD",
		); err != nil {
			return Workspace{}, fmt.Errorf(
				"workspace: create branch %q: %w",
				branch,
				err,
			)
		}
	}
	arguments := []string{"-C", sourcePath, "worktree", "add", "--quiet"}
	arguments = append(arguments, path, branch)
	if _, err := m.run(ctx, arguments...); err != nil {
		target := Workspace{
			AgentID:       agentID,
			Repository:    repository,
			Path:          path,
			Branch:        branch,
			createdBranch: createdBranch,
			sourcePath:    sourcePath,
		}
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(),
			10*time.Second,
		)
		defer cancel()
		return Workspace{}, errors.Join(
			fmt.Errorf("workspace: create worktree: %w", err),
			m.rollbackFailedAdd(cleanupCtx, target),
		)
	}

	result := Workspace{
		AgentID:       agentID,
		Repository:    repository,
		Path:          path,
		Branch:        branch,
		createdBranch: createdBranch,
		sourcePath:    sourcePath,
	}
	if err := m.writeWorkspaceRecord(result); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return Workspace{}, errors.Join(err, m.discard(cleanupCtx, result))
	}
	if err := m.copyIncludedFiles(ctx, result); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return Workspace{}, errors.Join(err, m.discard(cleanupCtx, result))
	}
	return result, nil
}

// List returns all valid worktrees below this manager's root.
func (m *Manager) List(ctx context.Context) ([]Workspace, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.list(ctx)
}

func (m *Manager) list(ctx context.Context) ([]Workspace, error) {
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
		candidateIDs := make(map[string]struct{})
		for _, entry := range entries {
			if entry.IsDir() && validateAgentID(entry.Name()) == nil {
				candidateIDs[entry.Name()] = struct{}{}
			}
			if agentID, ok := workspaceRecordAgentID(entry.Name()); ok {
				candidateIDs[agentID] = struct{}{}
			}
		}
		agentIDs := make([]string, 0, len(candidateIDs))
		for agentID := range candidateIDs {
			agentIDs = append(agentIDs, agentID)
		}
		sort.Strings(agentIDs)
		for _, agentID := range agentIDs {
			path := filepath.Join(bucketPath, agentID)
			record, hasRecord, err := m.readWorkspaceRecord(path)
			if err != nil {
				return nil, err
			}
			pathInfo, pathErr := os.Lstat(path)
			if errors.Is(pathErr, os.ErrNotExist) {
				if !hasRecord {
					continue
				}
				record.Missing = true
				result = append(result, record)
				continue
			}
			if pathErr != nil {
				return nil, fmt.Errorf(
					"workspace: inspect managed path %q: %w",
					path,
					pathErr,
				)
			}
			if !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf(
					"workspace: managed path %q is not a real directory",
					path,
				)
			}
			var recorded *Workspace
			if hasRecord {
				recorded = &record
			}
			current, err := m.inspect(ctx, path, recorded)
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
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cleanup(ctx, agentID, force)
}

func (m *Manager) cleanup(
	ctx context.Context,
	agentID string,
	force bool,
) (Workspace, error) {
	if err := validateAgentID(agentID); err != nil {
		return Workspace{}, err
	}
	all, err := m.list(ctx)
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
	if !target.Missing && !force {
		current, err := m.inspect(ctx, target.Path, &target)
		if err != nil {
			return Workspace{}, err
		}
		if current.Dirty {
			return Workspace{}, fmt.Errorf("%w: %s", ErrDirty, target.Path)
		}
		target = current
	}
	if target.Missing {
		registered, err := m.worktreeRegistered(
			ctx,
			target.Repository,
			target.Path,
		)
		if err != nil {
			return Workspace{}, err
		}
		if registered {
			if _, err := m.run(
				ctx,
				"-C",
				target.Repository,
				"worktree",
				"remove",
				"--force",
				target.Path,
			); err != nil {
				return Workspace{}, fmt.Errorf(
					"workspace: remove missing worktree registration: %w",
					err,
				)
			}
		}
	} else {
		arguments := []string{"-C", target.Repository, "worktree", "remove"}
		if force {
			arguments = append(arguments, "--force")
		}
		arguments = append(arguments, target.Path)
		if _, err := m.run(ctx, arguments...); err != nil {
			return Workspace{}, fmt.Errorf("workspace: remove worktree: %w", err)
		}
	}
	if err := removeWorkspaceRecord(target.Path); err != nil {
		return Workspace{}, err
	}
	if err := removeEmptyDirectory(filepath.Dir(target.Path)); err != nil {
		return Workspace{}, err
	}
	return target, nil
}

// Discard rolls back a prepared worktree before its session metadata commits.
func (m *Manager) Discard(ctx context.Context, target Workspace) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.discard(ctx, target)
}

func (m *Manager) discard(ctx context.Context, target Workspace) error {
	if err := m.validateManagedPath(target); err != nil {
		return err
	}
	var result error
	_, pathErr := os.Lstat(target.Path)
	pathExists := pathErr == nil
	pathMissing := errors.Is(pathErr, os.ErrNotExist)
	if pathErr != nil && !errors.Is(pathErr, os.ErrNotExist) {
		result = errors.Join(
			result,
			fmt.Errorf("workspace: inspect discarded worktree: %w", pathErr),
		)
	}
	registered, err := m.worktreeRegistered(
		ctx,
		target.Repository,
		target.Path,
	)
	worktreeRemoved := false
	if err != nil {
		result = errors.Join(result, err)
	} else if registered {
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
		} else {
			worktreeRemoved = true
		}
	} else if pathExists {
		if err := m.removeManagedPath(target); err != nil {
			result = errors.Join(
				result,
				fmt.Errorf("workspace: remove partial worktree: %w", err),
			)
		} else {
			worktreeRemoved = true
		}
	} else if pathMissing {
		worktreeRemoved = true
	}
	if worktreeRemoved {
		if err := removeWorkspaceRecord(target.Path); err != nil {
			result = errors.Join(
				result,
				err,
			)
		}
	}
	if worktreeRemoved && target.createdBranch {
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
	if worktreeRemoved {
		if err := removeEmptyDirectory(filepath.Dir(target.Path)); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (m *Manager) rollbackFailedAdd(
	ctx context.Context,
	target Workspace,
) error {
	return m.discard(ctx, target)
}

func (m *Manager) inspect(
	ctx context.Context,
	path string,
	recorded *Workspace,
) (Workspace, error) {
	agentID := filepath.Base(path)
	repository, err := m.repositoryRoot(ctx, path)
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace: inspect repository for %q: %w", path, err)
	}
	if recorded != nil && recorded.Repository != repository {
		return Workspace{}, fmt.Errorf(
			"workspace: repository mismatch for %q",
			path,
		)
	}

	branchOutput, err := m.run(ctx, "-C", path, "symbolic-ref", "--quiet", "--short", "HEAD")
	detached := false
	branch := strings.TrimSpace(string(branchOutput))
	if isExitCode(err, 1) {
		detached = true
		if recorded != nil {
			branch = recorded.Branch
		} else {
			head, headErr := m.run(ctx, "-C", path, "rev-parse", "--short", "HEAD")
			if headErr != nil {
				return Workspace{}, fmt.Errorf(
					"workspace: inspect detached HEAD for %q: %w",
					path,
					headErr,
				)
			}
			branch = "HEAD@" + strings.TrimSpace(string(head))
		}
	} else if err != nil {
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
	included, err := m.includedPaths(ctx, path)
	if err != nil {
		return Workspace{}, err
	}
	return Workspace{
		AgentID:    agentID,
		Repository: repository,
		Path:       path,
		Branch:     branch,
		Dirty:      len(status) != 0 || len(included) != 0,
		Detached:   detached,
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

	repository, err := m.repositoryRoot(ctx, sourcePath)
	if err != nil {
		return "", "", err
	}
	return sourcePath, repository, nil
}

func (m *Manager) validateBranch(ctx context.Context, branch string) error {
	if branch == "" || strings.HasPrefix(branch, "-") {
		return fmt.Errorf("%w: %q", ErrInvalidBranch, branch)
	}
	if _, err := m.run(ctx, "check-ref-format", "refs/heads/"+branch); err != nil {
		return errors.Join(
			fmt.Errorf("%w: %q", ErrInvalidBranch, branch),
			err,
		)
	}
	return nil
}

func (m *Manager) repositoryRoot(
	ctx context.Context,
	worktreePath string,
) (string, error) {
	bareOutput, err := m.run(
		ctx,
		"-C",
		worktreePath,
		"rev-parse",
		"--is-bare-repository",
	)
	if err != nil {
		return "", fmt.Errorf("workspace: inspect bare repository state: %w", err)
	}
	if strings.TrimSpace(string(bareOutput)) == "true" {
		return "", fmt.Errorf(
			"%w: source %q uses a bare repository",
			ErrNotRepository,
			worktreePath,
		)
	}
	commonOutput, err := m.run(
		ctx,
		"-C",
		worktreePath,
		"rev-parse",
		"--git-common-dir",
	)
	if err != nil {
		return "", fmt.Errorf("workspace: inspect common Git directory: %w", err)
	}
	commonPath := strings.TrimSpace(string(commonOutput))
	if !filepath.IsAbs(commonPath) {
		commonPath = filepath.Join(worktreePath, commonPath)
	}
	commonPath, err = resolvePath(commonPath)
	if err != nil {
		return "", fmt.Errorf(
			"workspace: resolve common Git directory: %w",
			err,
		)
	}

	worktreeOutput, err := m.run(
		ctx,
		"-C",
		worktreePath,
		"config",
		"--path",
		"--get",
		"core.worktree",
	)
	if err == nil {
		configured := strings.TrimSpace(string(worktreeOutput))
		if !filepath.IsAbs(configured) {
			configured = filepath.Join(commonPath, configured)
		}
		resolved, resolveErr := resolvePath(configured)
		if resolveErr != nil {
			return "", fmt.Errorf(
				"workspace: resolve configured worktree: %w",
				resolveErr,
			)
		}
		return resolved, nil
	}
	if !isExitCode(err, 1) {
		return "", fmt.Errorf("workspace: inspect configured worktree: %w", err)
	}

	root := filepath.Dir(commonPath)
	resolved, err := resolvePath(root)
	if err != nil {
		return "", fmt.Errorf("workspace: resolve repository root: %w", err)
	}
	return resolved, nil
}

func (m *Manager) registeredWorktreePaths(
	ctx context.Context,
	repository string,
) ([]string, error) {
	output, err := m.run(
		ctx,
		"-C",
		repository,
		"worktree",
		"list",
		"--porcelain",
		"-z",
	)
	if err != nil {
		return nil, fmt.Errorf("workspace: list Git worktrees: %w", err)
	}
	var paths []string
	for _, field := range strings.Split(string(output), "\x00") {
		const prefix = "worktree "
		if !strings.HasPrefix(field, prefix) {
			continue
		}
		path, err := resolvePath(strings.TrimPrefix(field, prefix))
		if err != nil {
			return nil, fmt.Errorf(
				"workspace: resolve registered worktree path: %w",
				err,
			)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func (m *Manager) worktreeRegistered(
	ctx context.Context,
	repository string,
	path string,
) (bool, error) {
	paths, err := m.registeredWorktreePaths(ctx, repository)
	if err != nil {
		return false, err
	}
	path, err = resolvePath(path)
	if err != nil {
		return false, fmt.Errorf("workspace: resolve worktree path: %w", err)
	}
	for _, registered := range paths {
		if registered == path {
			return true, nil
		}
	}
	return false, nil
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
	if isExitCode(err, 1) {
		return false, nil
	}
	return false, fmt.Errorf("workspace: inspect branch %q: %w", branch, err)
}

func (m *Manager) validateManagedPath(target Workspace) error {
	if err := validateAgentID(target.AgentID); err != nil {
		return err
	}
	if !filepath.IsAbs(target.Repository) ||
		filepath.Clean(target.Repository) != target.Repository {
		return fmt.Errorf(
			"workspace: repository %q is not a clean absolute path",
			target.Repository,
		)
	}
	if !filepath.IsAbs(target.Path) ||
		filepath.Clean(target.Path) != target.Path {
		return fmt.Errorf(
			"workspace: path %q is not a clean absolute path",
			target.Path,
		)
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

func isExitCode(err error, code int) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == code
}

func resolvePath(path string) (string, error) {
	path = filepath.Clean(path)
	var suffix []string
	current := path
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
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
