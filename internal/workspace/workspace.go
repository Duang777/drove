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
	"time"

	"github.com/google/uuid"
)

const (
	worktreeDirectory        = "worktrees"
	repositoryHashLen        = 16
	branchOwnershipRefPrefix = "refs/drove/preparations/"
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

	createdBranch     bool
	branchOperationID string
	sourcePath        string
	protectionKnown   bool
	includedPaths     []string
}

// Manager owns worktrees below one Drove data directory.
type Manager struct {
	root         string
	git          string
	dataDirInfo  os.FileInfo
	worktreeInfo os.FileInfo
	bucketInfo   map[string]os.FileInfo
	rootErr      error
	mu           sync.Mutex
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
	var dataDirInfo os.FileInfo
	info, err := os.Lstat(absolute)
	switch {
	case err == nil:
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf(
				"workspace: data directory %q is not a real directory",
				absolute,
			)
		}
		dataDirInfo = info
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, fmt.Errorf(
			"workspace: inspect data directory %q: %w",
			absolute,
			err,
		)
	}
	rootPath := filepath.Join(filepath.Clean(absolute), worktreeDirectory)
	var worktreeInfo os.FileInfo
	bucketInfo := make(map[string]os.FileInfo)
	info, err = os.Lstat(rootPath)
	var rootErr error
	switch {
	case err == nil:
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			rootErr = fmt.Errorf(
				"workspace: worktree root %q is not a real directory",
				rootPath,
			)
			break
		}
		worktreeInfo = info
	case errors.Is(err, os.ErrNotExist):
	default:
		rootErr = fmt.Errorf("workspace: inspect worktree root: %w", err)
	}
	manager := &Manager{
		root:         rootPath,
		git:          "git",
		dataDirInfo:  dataDirInfo,
		worktreeInfo: worktreeInfo,
		bucketInfo:   bucketInfo,
		rootErr:      rootErr,
	}
	if worktreeInfo == nil || rootErr != nil {
		return manager, nil
	}
	root, err := manager.openWorktreeRoot()
	if err != nil {
		manager.rootErr = err
		return manager, nil
	}
	entries, readErr := readRootDirectory(root)
	closeErr := root.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		manager.rootErr = fmt.Errorf(
			"workspace: read worktree root %q: %w",
			rootPath,
			err,
		)
		return manager, nil
	}
	for _, entry := range entries {
		if !validRepositoryHash(entry.Name()) {
			continue
		}
		root, err := manager.openWorktreeRoot()
		if err != nil {
			manager.rootErr = err
			return manager, nil
		}
		bucket, bucketErr := openRealRootFromRoot(root, entry.Name())
		closeRootErr := root.Close()
		if err := errors.Join(bucketErr, closeRootErr); err != nil {
			if bucket != nil {
				_ = bucket.Close()
			}
			manager.rootErr = fmt.Errorf(
				"workspace: open repository bucket %q: %w",
				entry.Name(),
				err,
			)
			return manager, nil
		}
		verifyErr := manager.verifyRepositoryBucket(entry.Name(), bucket)
		closeBucketErr := bucket.Close()
		if err := errors.Join(verifyErr, closeBucketErr); err != nil {
			manager.rootErr = err
			return manager, nil
		}
	}
	return manager, nil
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
	if err := m.ensureManagedRoot(); err != nil {
		return Workspace{}, err
	}
	bucket := filepath.Join(m.root, repositoryHash(repository))
	if err := m.ensureManagedBucket(repository); err != nil {
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
	branchOperationID := ""
	if createdBranch {
		branchOperationID = uuid.NewString()
	}
	result := Workspace{
		AgentID:           agentID,
		Repository:        repository,
		Path:              path,
		Branch:            branch,
		branchOperationID: branchOperationID,
		sourcePath:        sourcePath,
	}
	includedPaths, err := m.includedPaths(ctx, sourcePath)
	if err != nil {
		return Workspace{}, err
	}
	if err := m.writeWorkspaceRecord(result, includedPaths); err != nil {
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(),
			10*time.Second,
		)
		defer cancel()
		cleanupTarget := result
		cleanupTarget.createdBranch = false
		return Workspace{}, errors.Join(
			err,
			m.discard(cleanupCtx, cleanupTarget),
		)
	}
	if createdBranch {
		if err := m.createOwnedBranch(
			ctx,
			sourcePath,
			branch,
			branchOperationID,
		); err != nil {
			cleanupCtx, cancel := context.WithTimeout(
				context.Background(),
				10*time.Second,
			)
			defer cancel()
			return Workspace{}, errors.Join(
				fmt.Errorf(
					"workspace: create branch %q: %w",
					branch,
					err,
				),
				m.discard(cleanupCtx, result),
			)
		}
		result.createdBranch = true
		record, exists, err := m.readWorkspaceRecord(result.Path)
		if err == nil && !exists {
			err = errors.New("workspace: branch ownership record is missing")
		}
		if err != nil {
			cleanupCtx, cancel := context.WithTimeout(
				context.Background(),
				10*time.Second,
			)
			defer cancel()
			return Workspace{}, errors.Join(
				fmt.Errorf(
					"workspace: read branch ownership record: %w",
					err,
				),
				m.discard(cleanupCtx, result),
			)
		}
		record.CreatedBranch = true
		if err := m.replaceWorkspaceRecord(record); err != nil {
			cleanupCtx, cancel := context.WithTimeout(
				context.Background(),
				10*time.Second,
			)
			defer cancel()
			return Workspace{}, errors.Join(
				fmt.Errorf(
					"workspace: confirm branch ownership: %w",
					err,
				),
				m.discard(cleanupCtx, result),
			)
		}
	}
	arguments := []string{"-C", sourcePath, "worktree", "add", "--quiet"}
	arguments = append(arguments, path, branch)
	if _, err := m.run(ctx, arguments...); err != nil {
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(),
			10*time.Second,
		)
		defer cancel()
		return Workspace{}, errors.Join(
			fmt.Errorf("workspace: create worktree: %w", err),
			m.rollbackFailedAdd(cleanupCtx, result),
		)
	}

	if err := m.copyIncludedFiles(result, includedPaths); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return Workspace{}, errors.Join(err, m.discard(cleanupCtx, result))
	}
	result.protectionKnown = true
	result.includedPaths = append([]string(nil), includedPaths...)
	return result, nil
}

// List returns all valid worktrees below this manager's root.
func (m *Manager) List(ctx context.Context) ([]Workspace, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.list(ctx)
}

func (m *Manager) list(
	ctx context.Context,
) (result []Workspace, resultErr error) {
	root, err := m.openWorktreeRoot()
	if errors.Is(err, os.ErrNotExist) {
		return []Workspace{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.Close())
	}()
	buckets, err := readRootDirectory(root)
	if err != nil {
		return nil, fmt.Errorf("workspace: read worktree root: %w", err)
	}

	for _, bucket := range buckets {
		if !validRepositoryHash(bucket.Name()) {
			continue
		}
		bucketRoot, err := openRealRootFromRoot(root, bucket.Name())
		if err != nil {
			return nil, fmt.Errorf(
				"workspace: open repository bucket %q: %w",
				bucket.Name(),
				err,
			)
		}
		if err := m.verifyRepositoryBucket(bucket.Name(), bucketRoot); err != nil {
			_ = bucketRoot.Close()
			return nil, err
		}
		current, err := m.listRepositoryBucket(
			ctx,
			bucket.Name(),
			bucketRoot,
		)
		closeErr := bucketRoot.Close()
		if err := errors.Join(err, closeErr); err != nil {
			return nil, err
		}
		result = append(result, current...)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Repository == result[j].Repository {
			return result[i].AgentID < result[j].AgentID
		}
		return result[i].Repository < result[j].Repository
	})
	return result, nil
}

func (m *Manager) listRepositoryBucket(
	ctx context.Context,
	bucketName string,
	bucket *os.Root,
) ([]Workspace, error) {
	entries, err := readRootDirectory(bucket)
	if err != nil {
		return nil, fmt.Errorf(
			"workspace: read repository bucket %q: %w",
			bucketName,
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

	result := make([]Workspace, 0, len(agentIDs))
	for _, agentID := range agentIDs {
		path := filepath.Join(m.root, bucketName, agentID)
		record, hasRecord, err := m.readWorkspaceRecordFromBucket(
			bucket,
			agentID,
			path,
		)
		if err != nil {
			return nil, err
		}
		pathInfo, pathErr := bucket.Lstat(agentID)
		if errors.Is(pathErr, os.ErrNotExist) {
			if !hasRecord {
				continue
			}
			missing := record.workspace()
			missing.Missing = true
			missing.protectionKnown = record.ProtectionKnown
			missing.includedPaths = append(
				[]string(nil),
				record.IncludedPaths...,
			)
			result = append(result, missing)
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
		workspaceRoot, err := openRealRootFromRoot(bucket, agentID)
		if err != nil {
			return nil, fmt.Errorf(
				"workspace: open managed path %q: %w",
				path,
				err,
			)
		}
		if err := m.pinDataDirectory(); err != nil {
			_ = workspaceRoot.Close()
			return nil, err
		}
		var recorded *workspaceRecord
		if hasRecord {
			recorded = &record
		}
		current, inspectErr := m.inspect(ctx, path, recorded)
		verifyErr := verifyRootEntryUnchanged(
			bucket,
			agentID,
			workspaceRoot,
		)
		closeErr := workspaceRoot.Close()
		if err := errors.Join(inspectErr, verifyErr, closeErr); err != nil {
			return nil, err
		}
		if repositoryHash(current.Repository) != bucketName {
			return nil, fmt.Errorf(
				"workspace: repository hash mismatch for %q",
				current.Path,
			)
		}
		result = append(result, current)
	}
	return result, nil
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
	} else {
		pathRemoved := pathMissing
		if pathExists {
			if err := m.removeManagedPath(target); err != nil {
				result = errors.Join(
					result,
					fmt.Errorf("workspace: remove partial worktree: %w", err),
				)
			} else {
				pathRemoved = true
			}
		}
		if pathRemoved && registered {
			if _, err := m.run(
				ctx,
				"-C",
				target.Repository,
				"worktree",
				"prune",
				"--expire",
				"now",
			); err != nil {
				result = errors.Join(
					result,
					fmt.Errorf("workspace: prune discarded worktree: %w", err),
				)
				pathRemoved = false
			}
		}
		if pathRemoved {
			stillRegistered, err := m.worktreeRegistered(
				ctx,
				target.Repository,
				target.Path,
			)
			if err != nil {
				result = errors.Join(result, err)
			} else if stillRegistered {
				result = errors.Join(
					result,
					errors.New(
						"workspace: discarded worktree remains registered",
					),
				)
			} else {
				worktreeRemoved = true
			}
		}
	}
	branchCleaned := !worktreeRemoved
	if worktreeRemoved {
		if err := m.cleanupPreparedBranch(ctx, target); err != nil {
			result = errors.Join(result, err)
		} else {
			branchCleaned = true
		}
	}
	recordRemoved := false
	if worktreeRemoved && branchCleaned {
		if err := m.removeWorkspaceRecord(target.Path, ""); err != nil {
			result = errors.Join(result, err)
		} else {
			recordRemoved = true
		}
	}
	if recordRemoved {
		if err := m.removeManagedBucketIfEmpty(target); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (m *Manager) cleanupPreparedBranch(
	ctx context.Context,
	target Workspace,
) error {
	if target.branchOperationID == "" {
		return nil
	}
	if err := m.validateBranch(ctx, target.Branch); err != nil {
		return err
	}
	markerRef := branchOwnershipRef(target.branchOperationID)
	markerOID, markerExists, err := m.refOID(
		ctx,
		target.Repository,
		markerRef,
	)
	if err != nil || !markerExists {
		return err
	}
	branchRef := "refs/heads/" + target.Branch
	branchOID, branchExists, err := m.refOID(ctx, target.Repository, branchRef)
	if err != nil {
		return err
	}
	var input string
	if branchExists && branchOID == markerOID {
		input = fmt.Sprintf(
			"start\ndelete %s %s\ndelete %s %s\nprepare\ncommit\n",
			branchRef,
			branchOID,
			markerRef,
			markerOID,
		)
	} else {
		input = fmt.Sprintf(
			"start\ndelete %s %s\nprepare\ncommit\n",
			markerRef,
			markerOID,
		)
	}
	if _, err := m.runInput(
		ctx,
		input,
		"-C",
		target.Repository,
		"update-ref",
		"--stdin",
	); err != nil {
		return fmt.Errorf("workspace: discard owned branch transaction: %w", err)
	}
	return nil
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
	recorded *workspaceRecord,
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
	protectionKnown := recorded != nil && recorded.ProtectionKnown
	var includedPaths []string
	includedDirty := !protectionKnown
	if recorded != nil {
		includedPaths = append([]string(nil), recorded.IncludedPaths...)
		for _, relative := range includedPaths {
			_, pathErr := os.Lstat(filepath.Join(path, relative))
			switch {
			case pathErr == nil:
				includedDirty = true
			case errors.Is(pathErr, os.ErrNotExist):
			case pathErr != nil:
				return Workspace{}, fmt.Errorf(
					"workspace: inspect included path %q: %w",
					relative,
					pathErr,
				)
			}
		}
	}
	return Workspace{
		AgentID:         agentID,
		Repository:      repository,
		Path:            path,
		Branch:          branch,
		Dirty:           len(status) != 0 || includedDirty,
		Detached:        detached,
		protectionKnown: protectionKnown,
		includedPaths:   includedPaths,
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
	info, err := os.Stat(absolute)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", "", fmt.Errorf("%w: %s", ErrNotRepository, absolute)
	case err != nil:
		return "", "", fmt.Errorf(
			"workspace: inspect repository path %q: %w",
			absolute,
			err,
		)
	case !info.IsDir():
		return "", "", fmt.Errorf("%w: %s is not a directory", ErrNotRepository, absolute)
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
		if strings.Contains(err.Error(), "fatal: not a git repository") {
			return "", "", fmt.Errorf("%w: %s", ErrNotRepository, absolute)
		}
		return "", "", fmt.Errorf(
			"workspace: inspect repository %q: %w",
			absolute,
			err,
		)
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
		if isExitCode(err, 1) {
			return errors.Join(
				fmt.Errorf("%w: %q", ErrInvalidBranch, branch),
				err,
			)
		}
		return fmt.Errorf("workspace: validate branch %q: %w", branch, err)
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

type registeredWorktree struct {
	path     string
	head     string
	branch   string
	detached bool
}

func (m *Manager) registeredWorktrees(
	ctx context.Context,
	repository string,
) ([]registeredWorktree, error) {
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
	var (
		worktrees []registeredWorktree
		current   *registeredWorktree
	)
	for _, field := range strings.Split(string(output), "\x00") {
		if field == "" {
			if current != nil {
				worktrees = append(worktrees, *current)
				current = nil
			}
			continue
		}
		switch {
		case strings.HasPrefix(field, "worktree "):
			if current != nil {
				return nil, errors.New(
					"workspace: malformed Git worktree listing",
				)
			}
			path := filepath.Clean(strings.TrimPrefix(field, "worktree "))
			if !filepath.IsAbs(path) {
				return nil, fmt.Errorf(
					"workspace: registered worktree path %q is not absolute",
					path,
				)
			}
			current = &registeredWorktree{path: path}
		case current == nil:
			return nil, errors.New(
				"workspace: malformed Git worktree listing",
			)
		case strings.HasPrefix(field, "HEAD "):
			current.head = strings.TrimPrefix(field, "HEAD ")
		case strings.HasPrefix(field, "branch "):
			current.branch = strings.TrimPrefix(field, "branch ")
		case field == "detached":
			current.detached = true
		}
	}
	if current != nil {
		worktrees = append(worktrees, *current)
	}
	return worktrees, nil
}

func (m *Manager) worktreeRegistration(
	ctx context.Context,
	repository string,
	path string,
) (registeredWorktree, bool, error) {
	worktrees, err := m.registeredWorktrees(ctx, repository)
	if err != nil {
		return registeredWorktree{}, false, err
	}
	path = filepath.Clean(path)
	for _, registered := range worktrees {
		if registered.path == path {
			return registered, true, nil
		}
	}
	return registeredWorktree{}, false, nil
}

func (m *Manager) worktreeRegistered(
	ctx context.Context,
	repository string,
	path string,
) (bool, error) {
	_, registered, err := m.worktreeRegistration(ctx, repository, path)
	return registered, err
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

func (m *Manager) createOwnedBranch(
	ctx context.Context,
	repository string,
	branch string,
	operationID string,
) error {
	head, err := m.run(ctx, "-C", repository, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("workspace: resolve branch start: %w", err)
	}
	commit := strings.TrimSpace(string(head))
	input := fmt.Sprintf(
		"start\ncreate refs/heads/%s %s\ncreate %s %s\nprepare\ncommit\n",
		branch,
		commit,
		branchOwnershipRef(operationID),
		commit,
	)
	if _, err := m.runInput(
		ctx,
		input,
		"-C",
		repository,
		"update-ref",
		"--stdin",
	); err != nil {
		return fmt.Errorf("workspace: create owned branch transaction: %w", err)
	}
	return nil
}

func (m *Manager) branchOwnershipMarkerExists(
	ctx context.Context,
	repository string,
	operationID string,
) (bool, error) {
	if operationID == "" {
		return false, nil
	}
	_, exists, err := m.refOID(
		ctx,
		repository,
		branchOwnershipRef(operationID),
	)
	return exists, err
}

func (m *Manager) removeBranchOwnershipMarker(
	ctx context.Context,
	repository string,
	operationID string,
) error {
	if operationID == "" {
		return nil
	}
	markerRef := branchOwnershipRef(operationID)
	markerOID, exists, err := m.refOID(ctx, repository, markerRef)
	if err != nil || !exists {
		return err
	}
	if _, err := m.run(
		ctx,
		"-C",
		repository,
		"update-ref",
		"-d",
		markerRef,
		markerOID,
	); err != nil {
		return fmt.Errorf(
			"workspace: remove branch ownership marker: %w",
			err,
		)
	}
	return nil
}

func (m *Manager) refOID(
	ctx context.Context,
	repository string,
	ref string,
) (string, bool, error) {
	output, err := m.run(
		ctx,
		"-C",
		repository,
		"rev-parse",
		"--verify",
		"--quiet",
		ref,
	)
	switch {
	case err == nil:
		oid := strings.TrimSpace(string(output))
		if oid == "" || strings.ContainsAny(oid, " \t\r\n") {
			return "", false, fmt.Errorf(
				"workspace: inspect ref %q returned an invalid object ID",
				ref,
			)
		}
		return oid, true, nil
	case isExitCode(err, 1):
		return "", false, nil
	default:
		return "", false, fmt.Errorf("workspace: inspect ref %q: %w", ref, err)
	}
}

func branchOwnershipRef(operationID string) string {
	return branchOwnershipRefPrefix + operationID
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
	return m.runInput(ctx, "", arguments...)
}

func (m *Manager) runInput(
	ctx context.Context,
	input string,
	arguments ...string,
) ([]byte, error) {
	command := exec.CommandContext(ctx, m.git, arguments...)
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	if input != "" {
		command.Stdin = strings.NewReader(input)
	}
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
