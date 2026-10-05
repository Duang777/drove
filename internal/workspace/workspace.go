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
	branchOwnershipLogPrefix = "drove preparation "
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

	managerLocks sync.Map
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

	createdBranch      bool
	branchOperationID  string
	gitDirectory       string
	directoryIdentity  string
	protectionKnown    bool
	includedPaths      []string
	preparation        *preparationLease
	repositoryEvidence *repositoryEvidence
}

// Manager owns worktrees below one Drove data directory.
type Manager struct {
	root         string
	git          string
	dataDirInfo  os.FileInfo
	worktreeInfo os.FileInfo
	bucketInfo   map[string]os.FileInfo
	rootErr      error
	mu           *sync.Mutex
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
		mu:           workspaceManagerLock(rootPath),
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
) (_ Workspace, resultErr error) {
	if err := validateAgentID(agentID); err != nil {
		return Workspace{}, err
	}
	sourcePath, repository, err := m.repositoryPaths(ctx, repository)
	if err != nil {
		return Workspace{}, err
	}
	sourceRoot, err := openRealPathRoot(sourcePath)
	if err != nil {
		return Workspace{}, fmt.Errorf(
			"workspace: open source repository: %w",
			err,
		)
	}
	sourceRootOwned := true
	defer func() {
		if sourceRootOwned {
			resultErr = errors.Join(resultErr, sourceRoot.Close())
		}
	}()
	pinnedRepository, err := m.repositoryRootAtRoot(
		ctx,
		sourcePath,
		sourceRoot,
	)
	if err != nil {
		return Workspace{}, err
	}
	if pinnedRepository != repository {
		return Workspace{}, errors.New(
			"workspace: source repository changed while opening",
		)
	}
	lease, err := newPreparationLease(ctx, m, sourcePath, sourceRoot)
	if err != nil {
		return Workspace{}, fmt.Errorf(
			"workspace: retain source repository: %w",
			err,
		)
	}
	sourceRootOwned = false
	leaseReturned := false
	defer func() {
		if !leaseReturned {
			resultErr = errors.Join(resultErr, lease.Close())
		}
	}()
	sourceRepository := lease.repository
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

	exists, err := sourceRepository.branchExists(ctx, branch)
	if err != nil {
		return Workspace{}, err
	}
	createdBranch := !exists
	branchOperationID := uuid.NewString()
	result := Workspace{
		AgentID:           agentID,
		Repository:        repository,
		Path:              path,
		Branch:            branch,
		branchOperationID: branchOperationID,
		repositoryEvidence: cloneRepositoryEvidence(
			&lease.evidence,
		),
	}
	includeSelection, err := m.selectIncludedPaths(
		ctx,
		sourcePath,
		sourceRoot,
		sourceRepository,
	)
	if err != nil {
		return Workspace{}, err
	}
	includedPaths := includeSelection.paths
	recordInstalled, err := m.writeWorkspaceRecordState(result, includedPaths)
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(),
			10*time.Second,
		)
		defer cancel()
		if !recordInstalled {
			return Workspace{}, err
		}
		cleanupTarget := result
		cleanupTarget.createdBranch = false
		return Workspace{}, errors.Join(
			err,
			m.discard(cleanupCtx, cleanupTarget),
		)
	}
	preparedTarget, err := m.createPreparedWorktreeTarget(&result)
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(),
			10*time.Second,
		)
		defer cancel()
		return Workspace{}, errors.Join(
			err,
			m.cleanupPreparedWorktreeTarget(result, preparedTarget),
			m.discardWithRepository(
				cleanupCtx,
				result,
				sourceRepository,
				true,
			),
		)
	}
	defer func() {
		if resultErr != nil {
			cleanupCtx, cancel := context.WithTimeout(
				context.Background(),
				10*time.Second,
			)
			defer cancel()
			cleanupErr := m.cleanupPreparedWorktreeTarget(
				result,
				preparedTarget,
			)
			var pruneErr, discardErr error
			if cleanupErr == nil {
				pruneErr = sourceRepository.pruneWorktrees(cleanupCtx)
				if pruneErr == nil {
					discardErr = m.discardWithRepository(
						cleanupCtx,
						result,
						sourceRepository,
						true,
					)
				}
			}
			resultErr = errors.Join(
				resultErr,
				cleanupErr,
				pruneErr,
				discardErr,
			)
		}
		resultErr = errors.Join(resultErr, preparedTarget.Close())
	}()
	if createdBranch {
		if err := sourceRepository.createOwnedBranch(
			ctx,
			branch,
			branchOperationID,
		); err != nil {
			return Workspace{}, fmt.Errorf(
				"workspace: create branch %q: %w",
				branch,
				err,
			)
		}
		result.createdBranch = true
		record, exists, err := m.readWorkspaceRecord(result.Path)
		if err == nil && !exists {
			err = errors.New("workspace: branch ownership record is missing")
		}
		if err != nil {
			return Workspace{}, fmt.Errorf(
				"workspace: read branch ownership record: %w",
				err,
			)
		}
		record.CreatedBranch = true
		if err := m.replaceWorkspaceRecord(record); err != nil {
			return Workspace{}, fmt.Errorf(
				"workspace: confirm branch ownership: %w",
				err,
			)
		}
	}
	arguments := []string{
		"worktree",
		"add",
		"--quiet",
		"--no-checkout",
		preparedTarget.path,
		branch,
	}
	if _, err := sourceRepository.runForward(
		ctx,
		"",
		arguments...,
	); err != nil {
		return Workspace{}, fmt.Errorf("workspace: create worktree: %w", err)
	}

	if err := m.initializePreparedWorktree(
		ctx,
		&result,
		preparedTarget,
	); err != nil {
		return Workspace{}, err
	}
	if err := m.promotePreparedWorktreeTarget(
		ctx,
		result,
		preparedTarget,
	); err != nil {
		return Workspace{}, err
	}

	if err := verifyRealPathRoot(sourcePath, sourceRoot); err != nil {
		return Workspace{}, fmt.Errorf(
			"workspace: source repository changed: %w",
			err,
		)
	}
	copiedPaths, err := m.copyIncludedSelection(
		ctx,
		sourcePath,
		sourceRoot,
		sourceRepository,
		result,
		includeSelection,
	)
	if err != nil {
		return Workspace{}, err
	}
	if len(copiedPaths) != len(includedPaths) {
		record, exists, err := m.readWorkspaceRecord(result.Path)
		if err == nil && !exists {
			err = errors.New("workspace: preparation record is missing")
		}
		if err == nil {
			record.IncludedPaths = append([]string{}, copiedPaths...)
			err = m.replaceWorkspaceRecord(record)
		}
		if err != nil {
			return Workspace{}, fmt.Errorf(
				"workspace: persist copied include paths: %w",
				err,
			)
		}
	}
	if err := verifyRealPathRoot(sourcePath, sourceRoot); err != nil {
		return Workspace{}, fmt.Errorf(
			"workspace: source repository changed: %w",
			err,
		)
	}
	result.protectionKnown = true
	result.includedPaths = append([]string(nil), copiedPaths...)
	result.preparation = lease
	leaseReturned = true
	return result, nil
}

func workspaceManagerLock(root string) *sync.Mutex {
	key := workspaceManagerLockKey(root)
	lock, _ := managerLocks.LoadOrStore(key, &sync.Mutex{})
	return lock.(*sync.Mutex)
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
		current, inspectErr := m.inspectAtRoot(
			ctx,
			path,
			workspaceRoot,
			recorded,
		)
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

func (m *Manager) discard(
	ctx context.Context,
	target Workspace,
) (result error) {
	lease := target.preparation
	temporaryLease := false
	if lease == nil {
		record, exists, err := m.readWorkspaceRecord(target.Path)
		if err != nil {
			return err
		}
		if !exists {
			return errors.New("workspace: preparation record is missing")
		}
		if target.branchOperationID == "" ||
			record.BranchOperationID != target.branchOperationID ||
			!sameWorkspace(record.workspace(), target) {
			return errors.New(
				"workspace: preparation record does not match discard request",
			)
		}
		lease, err = openRecordedPreparationLease(ctx, m, record)
		if err != nil {
			return err
		}
		temporaryLease = true
		defer func() {
			result = errors.Join(result, lease.Close())
		}()
	}
	if err := m.discardWithRepository(
		ctx,
		target,
		lease.repository,
		target.preparation != nil,
	); err != nil {
		return err
	}
	if temporaryLease {
		return nil
	}
	return lease.Close()
}

func (m *Manager) discardWithRepository(
	ctx context.Context,
	target Workspace,
	repository repositoryCapability,
	allowIncompleteGitIdentity bool,
) error {
	if err := m.validateManagedPath(target); err != nil {
		return err
	}
	record, exists, err := m.readWorkspaceRecord(target.Path)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("workspace: preparation record is missing")
	}
	if record.PreparationCommitted {
		return errors.New("workspace: committed preparation cannot be discarded")
	}
	if target.branchOperationID == "" ||
		record.BranchOperationID != target.branchOperationID ||
		!sameWorkspace(record.workspace(), target) {
		return errors.New(
			"workspace: preparation record does not match discard request",
		)
	}
	stagingPath, stagingExists, err :=
		m.preparedWorktreeStagingPath(target, record)
	if err != nil {
		return err
	}
	_, pathErr := os.Lstat(target.Path)
	pathExists := pathErr == nil
	if pathErr != nil && !errors.Is(pathErr, os.ErrNotExist) {
		return fmt.Errorf(
			"workspace: inspect discarded worktree: %w",
			pathErr,
		)
	}
	if pathExists && stagingExists {
		return errors.New(
			"workspace: preparation has both public and staging worktree paths",
		)
	}
	activePath := target.Path
	if stagingExists {
		activePath = stagingPath
		pathExists = true
	}
	if pathExists &&
		(record.DirectoryIdentity == "" ||
			(record.GitDirectory == "" && !allowIncompleteGitIdentity)) {
		return errors.New(
			"workspace: preparation record has incomplete worktree identity",
		)
	}
	registrationPaths := []string{target.Path}
	if stagingPath != target.Path {
		registrationPaths = append(registrationPaths, stagingPath)
	}
	anyRegistered := func() (bool, error) {
		for _, path := range registrationPaths {
			registered, err := repository.worktreeRegistered(ctx, path)
			if err != nil {
				return false, err
			}
			if registered {
				return true, nil
			}
		}
		return false, nil
	}
	registered, err := anyRegistered()
	var result error
	worktreeRemoved := false
	if err != nil {
		result = errors.Join(result, err)
	} else {
		pathRemoved := !pathExists
		if pathExists {
			var removeErr error
			if activePath != target.Path {
				removeErr = m.removeStagedPreparedWorktree(
					ctx,
					target,
					record,
					activePath,
				)
			} else if repository.root != nil {
				removeErr = m.removePreparedWorktreeAtRoot(
					ctx,
					target,
					record,
					repository,
					allowIncompleteGitIdentity,
				)
			} else {
				removeErr = m.removeDiscardedPath(
					ctx,
					target,
					record,
				)
			}
			if removeErr != nil {
				result = errors.Join(
					result,
					fmt.Errorf(
						"workspace: remove partial worktree: %w",
						removeErr,
					),
				)
			} else {
				if !registered {
					registered, removeErr = anyRegistered()
					if removeErr != nil {
						result = errors.Join(result, removeErr)
					}
				}
				pathRemoved = true
			}
		}
		if pathRemoved && registered {
			if err := repository.pruneWorktrees(ctx); err != nil {
				result = errors.Join(
					result,
					err,
				)
				pathRemoved = false
			}
		}
		if pathRemoved {
			stillRegistered, err := anyRegistered()
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
		if err := repository.cleanupOwnedBranch(
			ctx,
			target.Branch,
			target.branchOperationID,
		); err != nil {
			result = errors.Join(result, err)
		} else {
			branchCleaned = true
		}
	}
	if worktreeRemoved && branchCleaned {
		if err := m.removeWorkspaceRecord(target.Path); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (m *Manager) removeDiscardedPath(
	ctx context.Context,
	target Workspace,
	record workspaceRecord,
) (result error) {
	bucket, err := m.openManagedBucketRoot(target)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, bucket.Close())
	}()
	opened, err := openRealRootFromRoot(bucket, target.AgentID)
	if err != nil {
		return err
	}
	openedOwned := true
	defer func() {
		if openedOwned {
			result = errors.Join(result, opened.Close())
		}
	}()
	current, inspectErr := m.inspectAtRoot(
		ctx,
		target.Path,
		opened,
		&record,
	)
	verifyErr := verifyRootEntryUnchanged(
		bucket,
		target.AgentID,
		opened,
	)
	if err := errors.Join(inspectErr, verifyErr); err != nil {
		return err
	}
	if !sameWorkspace(current, target) {
		return errors.New(
			"workspace: discarded worktree identity changed",
		)
	}
	openedOwned = false
	return removeOpenedDirectoryFromRoot(
		bucket,
		target.AgentID,
		opened,
	)
}

func (m *Manager) runPreparedRefTransaction(
	ctx context.Context,
	repository string,
	commands []string,
	validate func() (bool, error),
) (bool, error) {
	capability := pathRepositoryCapability(m, repository)
	return capability.runPreparedRefTransaction(ctx, commands, validate)
}

func updateRefError(err error, stderr string) error {
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(stderr)
	if len(detail) > 4096 {
		detail = detail[:4096]
	}
	if detail == "" {
		return err
	}
	return fmt.Errorf("%s: %w", detail, err)
}

func (m *Manager) inspect(
	ctx context.Context,
	path string,
	recorded *workspaceRecord,
) (_ Workspace, result error) {
	root, err := openRealPathRoot(path)
	if err != nil {
		return Workspace{}, err
	}
	defer func() {
		result = errors.Join(result, root.Close())
	}()
	current, err := m.inspectAtRoot(ctx, path, root, recorded)
	verifyErr := verifyRealPathRoot(path, root)
	return current, errors.Join(err, verifyErr)
}

func (m *Manager) inspectAtRoot(
	ctx context.Context,
	path string,
	root *os.Root,
	recorded *workspaceRecord,
) (Workspace, error) {
	agentID := filepath.Base(path)
	directoryIdentity, err := openedDirectoryIdentity(root)
	if err != nil {
		return Workspace{}, err
	}
	if recorded != nil &&
		recorded.DirectoryIdentity != "" &&
		recorded.DirectoryIdentity != directoryIdentity {
		return Workspace{}, fmt.Errorf(
			"workspace: directory identity mismatch for %q",
			path,
		)
	}
	repository, err := m.repositoryRootAtRoot(ctx, path, root)
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace: inspect repository for %q: %w", path, err)
	}
	if recorded != nil && recorded.Repository != repository {
		return Workspace{}, fmt.Errorf(
			"workspace: repository mismatch for %q",
			path,
		)
	}
	gitDirectory, err := m.worktreeGitDirectoryAtRoot(ctx, path, root)
	if err != nil {
		return Workspace{}, err
	}
	if recorded != nil &&
		recorded.GitDirectory != "" &&
		recorded.GitDirectory != gitDirectory {
		return Workspace{}, fmt.Errorf(
			"workspace: Git directory mismatch for %q",
			path,
		)
	}

	branchOutput, err := m.runRootedGit(
		ctx,
		path,
		root,
		"symbolic-ref",
		"--quiet",
		"--short",
		"HEAD",
	)
	detached := false
	branch := strings.TrimSpace(string(branchOutput))
	if isExitCode(err, 1) {
		detached = true
		if recorded != nil {
			branch = recorded.Branch
		} else {
			head, headErr := m.runRootedGit(
				ctx,
				path,
				root,
				"rev-parse",
				"--short",
				"HEAD",
			)
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
	status, err := m.runRootedGit(
		ctx,
		path,
		root,
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
			_, pathErr := root.Lstat(relative)
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
		AgentID:           agentID,
		Repository:        repository,
		Path:              path,
		Branch:            branch,
		Dirty:             workspaceStatusDirty(status, recorded) || includedDirty,
		Detached:          detached,
		gitDirectory:      gitDirectory,
		directoryIdentity: directoryIdentity,
		protectionKnown:   protectionKnown,
		includedPaths:     includedPaths,
	}, nil
}

func workspaceStatusDirty(
	status []byte,
	recorded *workspaceRecord,
) bool {
	ignored := ""
	if recorded != nil &&
		recorded.Removal != nil &&
		recorded.Removal.DirectoryToken != "" {
		ignored = "?? " + removalMarkerName(*recorded)
	}
	for _, line := range strings.Split(
		strings.TrimSpace(string(status)),
		"\n",
	) {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || line == ignored {
			continue
		}
		return true
	}
	return false
}

func (m *Manager) worktreeGitDirectory(
	ctx context.Context,
	path string,
) (string, error) {
	output, err := m.run(
		ctx,
		"-C",
		path,
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
	gitDirectory := trimGitLineTerminator(output)
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

func worktreeDirectoryIdentity(path string) (result string, resultErr error) {
	root, err := openRealPathRoot(path)
	if err != nil {
		return "", fmt.Errorf(
			"workspace: open worktree identity for %q: %w",
			path,
			err,
		)
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.Close())
	}()
	identity, err := openedDirectoryIdentity(root)
	if err != nil {
		return "", fmt.Errorf(
			"workspace: inspect worktree identity for %q: %w",
			path,
			err,
		)
	}
	if err := verifyRealPathRoot(path, root); err != nil {
		return "", err
	}
	return identity, nil
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
	sourcePath := trimGitLineTerminator(output)
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
	if branch == "" ||
		strings.HasPrefix(branch, "-") ||
		strings.ContainsRune(branch, '\x00') {
		return fmt.Errorf("%w: %q", ErrInvalidBranch, branch)
	}
	if _, err := m.run(ctx, "check-ref-format", "--branch", branch); err != nil {
		if isExitCode(err, 1) || isExitCode(err, 128) {
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
	return m.repositoryRootWith(
		worktreePath,
		func(arguments ...string) ([]byte, error) {
			return m.run(
				ctx,
				append([]string{"-C", worktreePath}, arguments...)...,
			)
		},
	)
}

func (m *Manager) repositoryRootAtRoot(
	ctx context.Context,
	worktreePath string,
	root *os.Root,
) (string, error) {
	return m.repositoryRootWith(
		worktreePath,
		func(arguments ...string) ([]byte, error) {
			return m.runRootedGit(
				ctx,
				worktreePath,
				root,
				arguments...,
			)
		},
	)
}

func (m *Manager) repositoryRootWith(
	worktreePath string,
	run func(...string) ([]byte, error),
) (string, error) {
	bareOutput, err := run(
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
	commonOutput, err := run(
		"rev-parse",
		"--git-common-dir",
	)
	if err != nil {
		return "", fmt.Errorf("workspace: inspect common Git directory: %w", err)
	}
	commonPath := trimGitLineTerminator(commonOutput)
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

	worktreeOutput, err := run(
		"config",
		"--path",
		"--get",
		"core.worktree",
	)
	if err == nil {
		configured := trimGitLineTerminator(worktreeOutput)
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
	return pathRepositoryCapability(m, repository).registeredWorktrees(ctx)
}

func parseRegisteredWorktrees(output []byte) ([]registeredWorktree, error) {
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
		if sameRegisteredWorktreePath(registered.path, path) {
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
	command.Env = rootedGitEnvironment(command.Environ())
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
		"--create-reflog",
		"-m",
		branchOwnershipLogMessage(operationID),
		"--stdin",
	); err != nil {
		return fmt.Errorf("workspace: create owned branch transaction: %w", err)
	}
	return nil
}

func branchOwnershipLogMessage(operationID string) string {
	return branchOwnershipLogPrefix + operationID
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
	command.Env = rootedGitEnvironment(command.Environ())
	return runGitCommand(command, input)
}

func isExitCode(err error, code int) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == code
}

func trimGitLineTerminator(output []byte) string {
	return strings.TrimSuffix(string(output), "\n")
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
