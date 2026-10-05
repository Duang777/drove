package workspace

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"
)

const (
	workspaceRecordSuffix             = ".workspace.json"
	legacyWorkspaceRecordVersion      = 1
	protectedWorkspaceRecordVersion   = 2
	preparationWorkspaceRecordVersion = 3
	repositoryWorkspaceRecordVersion  = 4
	workspaceRecordVersion            = 5
	maxWorkspaceRecordSize            = 64 * 1024
)

type workspaceRecord struct {
	Version              int                     `json:"version"`
	AgentID              string                  `json:"agent_id"`
	Repository           string                  `json:"repository"`
	Path                 string                  `json:"path"`
	Branch               string                  `json:"branch"`
	ProtectionKnown      bool                    `json:"protection_known"`
	IncludedPaths        []string                `json:"included_paths"`
	PreparationCommitted bool                    `json:"preparation_committed"`
	CreatedBranch        bool                    `json:"created_branch"`
	BranchOperationID    string                  `json:"branch_operation_id,omitempty"`
	GitDirectory         string                  `json:"git_directory,omitempty"`
	DirectoryIdentity    string                  `json:"directory_identity,omitempty"`
	RepositoryEvidence   *repositoryEvidence     `json:"repository_evidence,omitempty"`
	Removal              *workspaceRemovalRecord `json:"removal,omitempty"`
}

type repositoryEvidence struct {
	SourcePath                 string `json:"source_path"`
	SourceDirectoryIdentity    string `json:"source_directory_identity"`
	GitDirectory               string `json:"git_directory"`
	GitDirectoryIdentity       string `json:"git_directory_identity"`
	CommonGitDirectory         string `json:"common_git_directory"`
	CommonGitDirectoryIdentity string `json:"common_git_directory_identity"`
}

type workspaceRemovalRecord struct {
	OperationID    string `json:"operation_id"`
	DirectoryToken string `json:"directory_token,omitempty"`
	Force          bool   `json:"force"`
	Started        bool   `json:"started"`
	Quarantined    bool   `json:"quarantined,omitempty"`
	PathAbsent     bool   `json:"path_absent,omitempty"`
}

type recordAcknowledgement struct {
	name        string
	operationID string
}

func workspaceRecordPath(worktreePath string) string {
	return worktreePath + workspaceRecordSuffix
}

func workspaceRecordAgentID(name string) (string, bool) {
	if !strings.HasSuffix(name, workspaceRecordSuffix) {
		return "", false
	}
	agentID := strings.TrimSuffix(name, workspaceRecordSuffix)
	if validateAgentID(agentID) != nil {
		return "", false
	}
	return agentID, true
}

func newWorkspaceRecord(target Workspace, includedPaths []string) workspaceRecord {
	return workspaceRecord{
		Version:           workspaceRecordVersion,
		AgentID:           target.AgentID,
		Repository:        target.Repository,
		Path:              target.Path,
		Branch:            target.Branch,
		ProtectionKnown:   true,
		IncludedPaths:     append([]string{}, includedPaths...),
		CreatedBranch:     target.createdBranch,
		BranchOperationID: target.branchOperationID,
		GitDirectory:      target.gitDirectory,
		DirectoryIdentity: target.directoryIdentity,
		RepositoryEvidence: cloneRepositoryEvidence(
			target.repositoryEvidence,
		),
	}
}

func (r workspaceRecord) workspace() Workspace {
	return Workspace{
		AgentID:           r.AgentID,
		Repository:        r.Repository,
		Path:              r.Path,
		Branch:            r.Branch,
		createdBranch:     r.CreatedBranch,
		branchOperationID: r.BranchOperationID,
		gitDirectory:      r.GitDirectory,
		directoryIdentity: r.DirectoryIdentity,
		repositoryEvidence: cloneRepositoryEvidence(
			r.RepositoryEvidence,
		),
	}
}

func cloneRepositoryEvidence(
	evidence *repositoryEvidence,
) *repositoryEvidence {
	if evidence == nil {
		return nil
	}
	cloned := *evidence
	return &cloned
}

func upgradeWorkspaceRecord(record *workspaceRecord) {
	if record.Version >= workspaceRecordVersion {
		return
	}
	record.Version = workspaceRecordVersion
	record.RepositoryEvidence = nil
	if record.PreparationCommitted {
		record.BranchOperationID = ""
	}
}

func (m *Manager) writeWorkspaceRecord(
	target Workspace,
	includedPaths []string,
) error {
	_, err := m.writeWorkspaceRecordState(target, includedPaths)
	return err
}

func (m *Manager) writeWorkspaceRecordState(
	target Workspace,
	includedPaths []string,
) (bool, error) {
	record := newWorkspaceRecord(target, includedPaths)
	return m.installWorkspaceRecordState(record, true)
}

func (m *Manager) replaceWorkspaceRecord(record workspaceRecord) error {
	_, err := m.installWorkspaceRecordState(record, false)
	return err
}

func (m *Manager) installWorkspaceRecord(
	record workspaceRecord,
	requireAbsent bool,
) error {
	_, err := m.installWorkspaceRecordState(record, requireAbsent)
	return err
}

func (m *Manager) replaceWorkspaceRecordState(
	record workspaceRecord,
) (bool, error) {
	return m.installWorkspaceRecordState(record, false)
}

func (m *Manager) installWorkspaceRecordState(
	record workspaceRecord,
	requireAbsent bool,
) (installed bool, result error) {
	if err := m.validateWorkspaceRecord(record); err != nil {
		return false, err
	}
	recordPath := workspaceRecordPath(record.Path)
	bucket, agentID, err := m.openRecordBucket(record.Path)
	if err != nil {
		return false, err
	}
	defer func() {
		result = errors.Join(result, bucket.Close())
	}()
	name := agentID + workspaceRecordSuffix
	if err := checkRecordTarget(bucket, name, recordPath, requireAbsent); err != nil {
		return false, err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return false, fmt.Errorf("workspace: encode record: %w", err)
	}
	if len(raw) > maxWorkspaceRecordSize {
		return false, fmt.Errorf(
			"workspace: encoded record %q exceeds %d bytes",
			recordPath,
			maxWorkspaceRecordSize,
		)
	}

	temporaryName := "." + name + ".tmp-" + uuid.NewString()
	file, err := bucket.OpenFile(
		temporaryName,
		os.O_RDWR|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		return false, fmt.Errorf(
			"workspace: create temporary record %q: %w",
			recordPath,
			err,
		)
	}
	fileOpen := true
	defer func() {
		if temporaryName != "" {
			result = errors.Join(
				result,
				removeRecordPathIfSame(bucket, temporaryName, file),
			)
		}
		if fileOpen {
			result = errors.Join(result, file.Close())
		}
	}()
	written, err := file.Write(raw)
	if err != nil {
		return false, fmt.Errorf(
			"workspace: write temporary record %q: %w",
			recordPath,
			err,
		)
	}
	if written != len(raw) {
		return false, fmt.Errorf(
			"workspace: write temporary record %q: %w",
			recordPath,
			io.ErrShortWrite,
		)
	}
	if err := file.Sync(); err != nil {
		return false, fmt.Errorf(
			"workspace: sync temporary record %q: %w",
			recordPath,
			err,
		)
	}
	if err := checkRecordTarget(bucket, name, recordPath, requireAbsent); err != nil {
		return false, err
	}
	if err := m.verifyRecordBucket(record.Path, bucket); err != nil {
		return false, err
	}
	directory, err := bucket.Open(".")
	if err != nil {
		return false, fmt.Errorf(
			"workspace: open record directory %q: %w",
			recordPath,
			err,
		)
	}
	installed, renameErr := renameRecordFile(
		directory,
		file,
		temporaryName,
		name,
		!requireAbsent,
	)
	if !installed {
		directoryCloseErr := directory.Close()
		if err := errors.Join(renameErr, directoryCloseErr); err != nil {
			return false, fmt.Errorf(
				"workspace: install record %q: %w",
				recordPath,
				err,
			)
		}
		return false, fmt.Errorf(
			"workspace: record %q was not installed",
			recordPath,
		)
	}
	closeErr := file.Close()
	fileOpen = false
	syncErr := syncRecordDirectory(directory)
	directoryCloseErr := directory.Close()
	if err := errors.Join(
		renameErr,
		closeErr,
		syncErr,
		directoryCloseErr,
	); err != nil {
		return true, fmt.Errorf(
			"workspace: finalize installed record %q in %q: %w",
			recordPath,
			filepath.Dir(recordPath),
			err,
		)
	}
	if err := m.verifyRecordBucket(record.Path, bucket); err != nil {
		return true, err
	}
	return true, nil
}

func checkRecordTarget(
	bucket *os.Root,
	name string,
	path string,
	requireAbsent bool,
) error {
	info, err := bucket.Lstat(name)
	if requireAbsent {
		switch {
		case errors.Is(err, os.ErrNotExist):
			return nil
		case err != nil:
			return fmt.Errorf("workspace: inspect target %q: %w", path, err)
		default:
			return fmt.Errorf("workspace: target %q already exists", path)
		}
	}
	if err != nil {
		return fmt.Errorf("workspace: inspect record %q: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("workspace: record %q is not a regular file", path)
	}
	return nil
}

func removeRecordPathIfSame(
	root *os.Root,
	name string,
	expected *os.File,
) error {
	current, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	opened, err := expected.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(opened, current) {
		return fmt.Errorf(
			"workspace: temporary path %q changed identity",
			name,
		)
	}
	return root.Remove(name)
}

func isolateRecordPathIfSame(
	root *os.Root,
	directory *os.File,
	name string,
	isolatedName string,
	expected *os.File,
	afterValidation func(),
) (bool, error) {
	current, err := root.Lstat(name)
	if err != nil {
		return false, err
	}
	opened, err := expected.Stat()
	if err != nil {
		return false, err
	}
	if !os.SameFile(opened, current) {
		return false, fmt.Errorf(
			"workspace: record path %q changed identity",
			name,
		)
	}
	if afterValidation != nil {
		afterValidation()
	}
	return moveRecordFile(
		directory,
		expected,
		name,
		isolatedName,
	)
}

func (m *Manager) readWorkspaceRecord(
	worktreePath string,
) (_ workspaceRecord, _ bool, result error) {
	recordPath := workspaceRecordPath(worktreePath)
	bucket, agentID, err := m.openRecordBucket(worktreePath)
	if errors.Is(err, os.ErrNotExist) {
		return workspaceRecord{}, false, nil
	}
	if err != nil {
		return workspaceRecord{}, false, fmt.Errorf(
			"workspace: open record directory %q: %w",
			recordPath,
			err,
		)
	}
	defer func() {
		result = errors.Join(result, bucket.Close())
	}()
	return m.readWorkspaceRecordFromBucket(bucket, agentID, worktreePath)
}

func (m *Manager) readWorkspaceRecordFromBucket(
	bucket *os.Root,
	agentID string,
	worktreePath string,
) (_ workspaceRecord, _ bool, result error) {
	recordPath := workspaceRecordPath(worktreePath)
	name := agentID + workspaceRecordSuffix
	info, err := bucket.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return workspaceRecord{}, false, nil
	}
	if err != nil {
		return workspaceRecord{}, false, fmt.Errorf(
			"workspace: inspect record %q: %w",
			recordPath,
			err,
		)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return workspaceRecord{}, false, fmt.Errorf(
			"workspace: record %q is not a regular file",
			recordPath,
		)
	}
	if info.Size() > maxWorkspaceRecordSize {
		return workspaceRecord{}, false, fmt.Errorf(
			"workspace: record %q exceeds %d bytes",
			recordPath,
			maxWorkspaceRecordSize,
		)
	}
	file, err := bucket.Open(name)
	if err != nil {
		return workspaceRecord{}, false, fmt.Errorf(
			"workspace: open record %q: %w",
			recordPath,
			err,
		)
	}
	defer func() {
		result = errors.Join(result, file.Close())
	}()
	opened, err := file.Stat()
	if err != nil {
		return workspaceRecord{}, false, fmt.Errorf(
			"workspace: inspect opened record %q: %w",
			recordPath,
			err,
		)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return workspaceRecord{}, false, fmt.Errorf(
			"workspace: record %q changed while opening",
			recordPath,
		)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxWorkspaceRecordSize+1))
	if err != nil {
		return workspaceRecord{}, false, fmt.Errorf(
			"workspace: read record %q: %w",
			recordPath,
			err,
		)
	}
	if len(raw) > maxWorkspaceRecordSize {
		return workspaceRecord{}, false, fmt.Errorf(
			"workspace: record %q exceeds %d bytes",
			recordPath,
			maxWorkspaceRecordSize,
		)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var record workspaceRecord
	if err := decoder.Decode(&record); err != nil {
		return workspaceRecord{}, false, fmt.Errorf(
			"workspace: decode record %q: %w",
			recordPath,
			err,
		)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return workspaceRecord{}, false, fmt.Errorf(
			"workspace: record %q has trailing data",
			recordPath,
		)
	}
	switch record.Version {
	case legacyWorkspaceRecordVersion:
		if record.ProtectionKnown ||
			len(record.IncludedPaths) != 0 ||
			record.PreparationCommitted ||
			record.CreatedBranch ||
			record.BranchOperationID != "" ||
			record.GitDirectory != "" ||
			record.DirectoryIdentity != "" ||
			record.RepositoryEvidence != nil ||
			record.Removal != nil {
			return workspaceRecord{}, false, fmt.Errorf(
				"workspace: legacy record %q contains newer fields",
				recordPath,
			)
		}
		record.PreparationCommitted = true
	case protectedWorkspaceRecordVersion:
		if record.IncludedPaths == nil {
			return workspaceRecord{}, false, fmt.Errorf(
				"workspace: version 2 record %q has no included paths",
				recordPath,
			)
		}
		if record.PreparationCommitted ||
			record.CreatedBranch ||
			record.BranchOperationID != "" ||
			record.GitDirectory != "" ||
			record.DirectoryIdentity != "" ||
			record.RepositoryEvidence != nil {
			return workspaceRecord{}, false, fmt.Errorf(
				"workspace: version 2 record %q contains version 3 fields",
				recordPath,
			)
		}
		record.PreparationCommitted = true
		if record.Removal != nil {
			record.Removal.Started = true
		}
	case preparationWorkspaceRecordVersion:
		if record.IncludedPaths == nil {
			return workspaceRecord{}, false, fmt.Errorf(
				"workspace: version 3 record %q has no included paths",
				recordPath,
			)
		}
		if record.RepositoryEvidence != nil {
			return workspaceRecord{}, false, fmt.Errorf(
				"workspace: version 3 record %q contains version 4 fields",
				recordPath,
			)
		}
	case repositoryWorkspaceRecordVersion:
		if record.IncludedPaths == nil {
			return workspaceRecord{}, false, fmt.Errorf(
				"workspace: version 4 record %q has no included paths",
				recordPath,
			)
		}
	case workspaceRecordVersion:
		if record.IncludedPaths == nil {
			return workspaceRecord{}, false, fmt.Errorf(
				"workspace: version 5 record %q has no included paths",
				recordPath,
			)
		}
	default:
		return workspaceRecord{}, false, fmt.Errorf(
			"workspace: record %q has unsupported version %d",
			recordPath,
			record.Version,
		)
	}
	if filepath.Clean(worktreePath) != record.Path {
		return workspaceRecord{}, false, fmt.Errorf(
			"workspace: record %q path mismatch",
			recordPath,
		)
	}
	if err := m.validateWorkspaceRecord(record); err != nil {
		return workspaceRecord{}, false, fmt.Errorf(
			"workspace: validate record %q: %w",
			recordPath,
			err,
		)
	}
	return record, true, nil
}

func (m *Manager) validateWorkspaceRecord(record workspaceRecord) error {
	if record.Version != legacyWorkspaceRecordVersion &&
		record.Version != protectedWorkspaceRecordVersion &&
		record.Version != preparationWorkspaceRecordVersion &&
		record.Version != repositoryWorkspaceRecordVersion &&
		record.Version != workspaceRecordVersion {
		return fmt.Errorf("workspace: unsupported record version %d", record.Version)
	}
	if record.Version < repositoryWorkspaceRecordVersion &&
		record.RepositoryEvidence != nil {
		return errors.New(
			"workspace: repository evidence requires record version 4",
		)
	}
	target := record.workspace()
	if err := m.validateManagedPath(target); err != nil {
		return err
	}
	if target.Branch == "" {
		return errors.New("workspace: record has an empty branch")
	}
	previous := ""
	for index, rawPath := range record.IncludedPaths {
		path, err := validateIncludedPath(rawPath)
		if err != nil {
			return err
		}
		if path != rawPath {
			return fmt.Errorf(
				"workspace: included path %q is not canonical",
				rawPath,
			)
		}
		if index > 0 && path <= previous {
			return errors.New(
				"workspace: included paths are not sorted and unique",
			)
		}
		previous = path
	}
	if record.Removal != nil {
		operationID, err := uuid.Parse(record.Removal.OperationID)
		if err != nil || operationID.String() != record.Removal.OperationID {
			return errors.New(
				"workspace: removal operation ID is not a canonical UUID",
			)
		}
		if record.Removal.Quarantined && !record.Removal.Started {
			return errors.New(
				"workspace: quarantined removal has not been started",
			)
		}
		if record.Removal.PathAbsent && record.Removal.Quarantined {
			return errors.New(
				"workspace: absent-path removal cannot be quarantined",
			)
		}
		if record.Removal.DirectoryToken != "" {
			token, err := uuid.Parse(record.Removal.DirectoryToken)
			if err != nil ||
				token.String() != record.Removal.DirectoryToken {
				return errors.New(
					"workspace: removal directory token is not a canonical UUID",
				)
			}
		}
	}
	if record.BranchOperationID != "" {
		operationID, err := uuid.Parse(record.BranchOperationID)
		if err != nil || operationID.String() != record.BranchOperationID {
			return errors.New(
				"workspace: branch operation ID is not a canonical UUID",
			)
		}
	}
	if record.GitDirectory != "" &&
		(!filepath.IsAbs(record.GitDirectory) ||
			filepath.Clean(record.GitDirectory) != record.GitDirectory) {
		return errors.New(
			"workspace: Git directory is not a clean absolute path",
		)
	}
	if len(record.DirectoryIdentity) > 128 ||
		strings.TrimSpace(record.DirectoryIdentity) !=
			record.DirectoryIdentity {
		return errors.New(
			"workspace: directory identity is invalid",
		)
	}
	if record.RepositoryEvidence != nil {
		var err error
		if record.Version == repositoryWorkspaceRecordVersion {
			err = validateLegacyRepositoryEvidence(
				*record.RepositoryEvidence,
			)
		} else {
			err = validateRepositoryEvidence(*record.RepositoryEvidence)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func validateLegacyRepositoryEvidence(
	evidence repositoryEvidence,
) error {
	if !cleanAbsolutePath(evidence.SourcePath) {
		return errors.New(
			"workspace: source repository path is not a clean absolute path",
		)
	}
	if !validDirectoryIdentity(evidence.SourceDirectoryIdentity) {
		return errors.New(
			"workspace: source repository identity is invalid",
		)
	}
	if evidence.GitDirectory != "" ||
		evidence.GitDirectoryIdentity != "" {
		return errors.New(
			"workspace: legacy repository evidence contains version 5 fields",
		)
	}
	if !cleanAbsolutePath(evidence.CommonGitDirectory) {
		return errors.New(
			"workspace: common Git directory is not a clean absolute path",
		)
	}
	if !validDirectoryIdentity(evidence.CommonGitDirectoryIdentity) {
		return errors.New(
			"workspace: common Git directory identity is invalid",
		)
	}
	return nil
}

func validateRepositoryEvidence(evidence repositoryEvidence) error {
	legacy := evidence
	legacy.GitDirectory = ""
	legacy.GitDirectoryIdentity = ""
	if err := validateLegacyRepositoryEvidence(legacy); err != nil {
		return err
	}
	if !cleanAbsolutePath(evidence.GitDirectory) {
		return errors.New(
			"workspace: source Git directory is not a clean absolute path",
		)
	}
	if !validDirectoryIdentity(evidence.GitDirectoryIdentity) {
		return errors.New(
			"workspace: source Git directory identity is invalid",
		)
	}
	return nil
}

func cleanAbsolutePath(path string) bool {
	return path != "" &&
		filepath.IsAbs(path) &&
		filepath.Clean(path) == path
}

func validDirectoryIdentity(identity string) bool {
	return identity != "" &&
		len(identity) <= 128 &&
		strings.TrimSpace(identity) == identity
}

func (m *Manager) removeWorkspaceRecord(
	worktreePath string,
) (result error) {
	recordPath := workspaceRecordPath(worktreePath)
	bucket, agentID, err := m.openRecordBucket(worktreePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf(
			"workspace: open record directory %q: %w",
			recordPath,
			err,
		)
	}
	defer func() {
		result = errors.Join(result, bucket.Close())
	}()
	name := agentID + workspaceRecordSuffix
	info, err := bucket.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("workspace: inspect record %q: %w", recordPath, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf(
			"workspace: record %q is not a regular file",
			recordPath,
		)
	}
	if err := bucket.Remove(name); err != nil {
		return fmt.Errorf("workspace: remove record %q: %w", recordPath, err)
	}
	return syncRecordBucket(bucket, recordPath)
}

func (m *Manager) removeAcknowledgedWorkspaceRecord(
	removal Removal,
) (result error) {
	recordPath := workspaceRecordPath(removal.Workspace.Path)
	bucket, agentID, err := m.openRecordBucket(removal.Workspace.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf(
			"workspace: open record directory %q: %w",
			recordPath,
			err,
		)
	}
	defer func() {
		result = errors.Join(result, bucket.Close())
	}()

	quarantineName, exists, err := findRecordAcknowledgementQuarantine(
		bucket,
		agentID,
		removal.operationID,
	)
	if err != nil {
		return err
	}
	if exists {
		if err := m.removeRecordAcknowledgementQuarantine(
			bucket,
			quarantineName,
			recordPath,
			removal,
		); err != nil {
			return err
		}
	}

	name := agentID + workspaceRecordSuffix
	info, err := bucket.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("workspace: inspect record %q: %w", recordPath, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf(
			"workspace: record %q is not a regular file",
			recordPath,
		)
	}
	file, err := bucket.Open(name)
	if err != nil {
		return fmt.Errorf("workspace: open removal record %q: %w", recordPath, err)
	}
	fileOpen := true
	defer func() {
		if fileOpen {
			result = errors.Join(result, file.Close())
		}
	}()
	opened, err := m.verifyRemovalRecordFile(
		file,
		recordPath,
		removal,
	)
	if err != nil {
		return err
	}

	directory, err := bucket.Open(".")
	if err != nil {
		return fmt.Errorf(
			"workspace: open record directory %q: %w",
			recordPath,
			err,
		)
	}
	quarantineName = recordAcknowledgementPrefix(
		agentID,
		removal.operationID,
	) + uuid.NewString()
	moved, renameErr := moveRecordFile(
		directory,
		file,
		name,
		quarantineName,
	)
	syncErr := syncRecordDirectory(directory)
	closeErr := directory.Close()
	if err := errors.Join(renameErr, syncErr, closeErr); err != nil {
		return fmt.Errorf(
			"workspace: quarantine removal record %q: %w",
			recordPath,
			err,
		)
	}
	if !moved {
		return fmt.Errorf(
			"workspace: removal record %q was not quarantined",
			recordPath,
		)
	}
	current, err := bucket.Lstat(quarantineName)
	if err != nil {
		return fmt.Errorf(
			"workspace: inspect quarantined removal record %q: %w",
			recordPath,
			err,
		)
	}
	if !current.Mode().IsRegular() ||
		current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(opened, current) {
		restoreErr := restoreRecordAcknowledgement(
			bucket,
			quarantineName,
			name,
		)
		return errors.Join(
			errors.New(
				"workspace: removal record changed while entering quarantine",
			),
			restoreErr,
		)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf(
			"workspace: close quarantined removal record %q: %w",
			recordPath,
			err,
		)
	}
	fileOpen = false
	return m.removeRecordAcknowledgementQuarantine(
		bucket,
		quarantineName,
		recordPath,
		removal,
	)
}

func (m *Manager) removalAcknowledgementPending(
	removal Removal,
) (_ bool, result error) {
	recordPath := workspaceRecordPath(removal.Workspace.Path)
	bucket, agentID, err := m.openRecordBucket(removal.Workspace.Path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf(
			"workspace: open record directory %q: %w",
			recordPath,
			err,
		)
	}
	defer func() {
		result = errors.Join(result, bucket.Close())
	}()
	_, exists, err := findRecordAcknowledgementQuarantine(
		bucket,
		agentID,
		removal.operationID,
	)
	if err != nil || exists {
		return exists, err
	}
	_, err = bucket.Lstat(agentID + workspaceRecordSuffix)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	case err != nil:
		return false, fmt.Errorf(
			"workspace: inspect record %q: %w",
			recordPath,
			err,
		)
	default:
		return true, nil
	}
}

func recordAcknowledgementPrefix(
	agentID string,
	operationID string,
) string {
	return "." + agentID + workspaceRecordSuffix + ".ack-" +
		operationID + "-"
}

func recordAcknowledgementIdentity(
	name string,
) (agentID string, operationID string, ok bool) {
	const acknowledgementMarker = workspaceRecordSuffix + ".ack-"
	if !strings.HasPrefix(name, ".") {
		return "", "", false
	}
	raw := strings.TrimPrefix(name, ".")
	index := strings.Index(raw, acknowledgementMarker)
	if index < 0 {
		return "", "", false
	}
	agentID = raw[:index]
	if validateAgentID(agentID) != nil {
		return "", "", false
	}
	suffix := raw[index+len(acknowledgementMarker):]
	if len(suffix) != 73 || suffix[36] != '-' {
		return "", "", false
	}
	operationID = suffix[:36]
	operation, err := uuid.Parse(operationID)
	if err != nil || operation.String() != operationID {
		return "", "", false
	}
	nonce, err := uuid.Parse(suffix[37:])
	if err != nil || nonce.String() != suffix[37:] {
		return "", "", false
	}
	return agentID, operationID, true
}

func (m *Manager) recoverRecordAcknowledgements() (result error) {
	root, err := m.openWorktreeRoot()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, root.Close())
	}()
	buckets, err := readRootDirectory(root)
	if err != nil {
		return fmt.Errorf("workspace: read worktree root: %w", err)
	}
	for _, bucket := range buckets {
		if !validRepositoryHash(bucket.Name()) {
			continue
		}
		bucketRoot, err := openRealRootFromRoot(root, bucket.Name())
		if err != nil {
			return err
		}
		if err := m.verifyRepositoryBucket(bucket.Name(), bucketRoot); err != nil {
			_ = bucketRoot.Close()
			return err
		}
		entries, err := readRootDirectory(bucketRoot)
		if err != nil {
			_ = bucketRoot.Close()
			return err
		}
		normalRecords := make(map[string]struct{})
		acknowledgements := make(map[string][]recordAcknowledgement)
		for _, entry := range entries {
			if agentID, ok := workspaceRecordAgentID(entry.Name()); ok {
				normalRecords[agentID] = struct{}{}
				continue
			}
			agentID, operationID, ok := recordAcknowledgementIdentity(
				entry.Name(),
			)
			if !ok {
				continue
			}
			acknowledgements[agentID] = append(
				acknowledgements[agentID],
				recordAcknowledgement{
					name:        entry.Name(),
					operationID: operationID,
				},
			)
		}
		for agentID, candidates := range acknowledgements {
			if _, exists := normalRecords[agentID]; exists {
				continue
			}
			candidate, err := coalesceRecordAcknowledgements(
				bucketRoot,
				candidates,
			)
			if err != nil {
				_ = bucketRoot.Close()
				return fmt.Errorf(
					"workspace: recover acknowledgement aliases for agent %q: %w",
					agentID,
					err,
				)
			}
			name := agentID + workspaceRecordSuffix
			if err := restoreRecordAcknowledgement(
				bucketRoot,
				candidate.name,
				name,
			); err != nil {
				_ = bucketRoot.Close()
				return fmt.Errorf(
					"workspace: recover acknowledgement for agent %q: %w",
					agentID,
					err,
				)
			}
			path := filepath.Join(m.root, bucket.Name(), agentID)
			record, exists, err := m.readWorkspaceRecordFromBucket(
				bucketRoot,
				agentID,
				path,
			)
			if err != nil {
				_ = bucketRoot.Close()
				return err
			}
			if !exists ||
				record.Removal == nil ||
				record.Removal.OperationID != candidate.operationID {
				_ = bucketRoot.Close()
				return fmt.Errorf(
					"workspace: recovered acknowledgement for agent %q does not match its record",
					agentID,
				)
			}
		}
		if err := bucketRoot.Close(); err != nil {
			return err
		}
	}
	return nil
}

func findRecordAcknowledgementQuarantine(
	bucket *os.Root,
	agentID string,
	operationID string,
) (string, bool, error) {
	entries, err := readRootDirectory(bucket)
	if err != nil {
		return "", false, err
	}
	prefix := recordAcknowledgementPrefix(agentID, operationID)
	var candidates []recordAcknowledgement
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		candidates = append(candidates, recordAcknowledgement{
			name:        entry.Name(),
			operationID: operationID,
		})
	}
	if len(candidates) == 0 {
		return "", false, nil
	}
	matched, err := coalesceRecordAcknowledgements(bucket, candidates)
	if err != nil {
		return "", false, err
	}
	return matched.name, true, nil
}

func coalesceRecordAcknowledgements(
	bucket *os.Root,
	candidates []recordAcknowledgement,
) (recordAcknowledgement, error) {
	return coalesceRecordAcknowledgementsAfterValidation(
		bucket,
		candidates,
		nil,
	)
}

func coalesceRecordAcknowledgementsAfterValidation(
	bucket *os.Root,
	candidates []recordAcknowledgement,
	afterValidation func(),
) (_ recordAcknowledgement, result error) {
	if len(candidates) == 0 {
		return recordAcknowledgement{}, errors.New(
			"workspace: acknowledgement candidate list is empty",
		)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].name < candidates[j].name
	})
	operationID := candidates[0].operationID
	var expected *os.File
	for _, candidate := range candidates {
		if candidate.operationID != operationID {
			return recordAcknowledgement{}, errors.New(
				"workspace: acknowledgement aliases have different operation IDs",
			)
		}
		info, err := bucket.Lstat(candidate.name)
		if err != nil {
			return recordAcknowledgement{}, err
		}
		if !info.Mode().IsRegular() ||
			info.Mode()&os.ModeSymlink != 0 {
			return recordAcknowledgement{}, fmt.Errorf(
				"workspace: acknowledgement quarantine %q is not a regular file",
				candidate.name,
			)
		}
		file, err := bucket.Open(candidate.name)
		if err != nil {
			return recordAcknowledgement{}, err
		}
		opened, statErr := file.Stat()
		if statErr == nil &&
			(!opened.Mode().IsRegular() || !os.SameFile(info, opened)) {
			statErr = fmt.Errorf(
				"workspace: acknowledgement quarantine %q changed while opening",
				candidate.name,
			)
		}
		if statErr == nil && expected != nil {
			expectedInfo, expectedErr := expected.Stat()
			if expectedErr != nil {
				statErr = expectedErr
			} else if !os.SameFile(expectedInfo, opened) {
				statErr = errors.New(
					"workspace: acknowledgement aliases refer to different records",
				)
			}
		}
		if statErr != nil {
			_ = file.Close()
			return recordAcknowledgement{}, statErr
		}
		if expected == nil {
			expected = file
			defer func() {
				result = errors.Join(result, expected.Close())
			}()
			continue
		}
		if err := file.Close(); err != nil {
			return recordAcknowledgement{}, err
		}
	}
	agentID, parsedOperationID, ok := recordAcknowledgementIdentity(
		candidates[0].name,
	)
	if !ok || parsedOperationID != operationID {
		return recordAcknowledgement{}, errors.New(
			"workspace: acknowledgement candidate name is invalid",
		)
	}
	for _, duplicate := range candidates[1:] {
		directory, err := bucket.Open(".")
		if err != nil {
			return recordAcknowledgement{}, err
		}
		isolatedName := recordAcknowledgementPrefix(
			agentID,
			operationID,
		) + uuid.NewString()
		isolated, isolateErr := isolateRecordPathIfSame(
			bucket,
			directory,
			duplicate.name,
			isolatedName,
			expected,
			afterValidation,
		)
		afterValidation = nil
		closeErr := directory.Close()
		if err := errors.Join(isolateErr, closeErr); err != nil {
			return recordAcknowledgement{}, fmt.Errorf(
				"workspace: isolate acknowledgement alias %q: %w",
				duplicate.name,
				err,
			)
		}
		if !isolated {
			return recordAcknowledgement{}, fmt.Errorf(
				"workspace: acknowledgement alias %q was not isolated",
				duplicate.name,
			)
		}
		if err := removeRecordPathIfSame(
			bucket,
			isolatedName,
			expected,
		); err != nil {
			return recordAcknowledgement{}, fmt.Errorf(
				"workspace: remove isolated acknowledgement alias %q: %w",
				isolatedName,
				err,
			)
		}
	}
	if len(candidates) > 1 {
		if err := syncRecordBucket(bucket, candidates[0].name); err != nil {
			return recordAcknowledgement{}, err
		}
	}
	return candidates[0], nil
}

func (m *Manager) removeRecordAcknowledgementQuarantine(
	bucket *os.Root,
	name string,
	recordPath string,
	removal Removal,
) (result error) {
	file, err := bucket.Open(name)
	if err != nil {
		return fmt.Errorf(
			"workspace: open quarantined removal record %q: %w",
			recordPath,
			err,
		)
	}
	fileOpen := true
	defer func() {
		if fileOpen {
			result = errors.Join(result, file.Close())
		}
	}()
	if _, err := m.verifyRemovalRecordFile(
		file,
		recordPath,
		removal,
	); err != nil {
		return err
	}
	directory, err := bucket.Open(".")
	if err != nil {
		return fmt.Errorf(
			"workspace: open quarantined record directory %q: %w",
			recordPath,
			err,
		)
	}
	deleteName := recordAcknowledgementPrefix(
		removal.Workspace.AgentID,
		removal.operationID,
	) + uuid.NewString()
	moved, moveErr := moveRecordFile(
		directory,
		file,
		name,
		deleteName,
	)
	syncErr := syncRecordDirectory(directory)
	closeErr := directory.Close()
	if err := errors.Join(moveErr, syncErr, closeErr); err != nil {
		return fmt.Errorf(
			"workspace: isolate quarantined record %q: %w",
			recordPath,
			err,
		)
	}
	if !moved {
		return fmt.Errorf(
			"workspace: quarantined record %q was not isolated",
			recordPath,
		)
	}
	if err := removeRecordPathIfSame(bucket, deleteName, file); err != nil {
		return fmt.Errorf(
			"workspace: remove quarantined record %q: %w",
			recordPath,
			err,
		)
	}
	return syncRecordBucket(bucket, recordPath)
}

func restoreRecordAcknowledgement(
	bucket *os.Root,
	quarantineName string,
	recordName string,
) (result error) {
	file, err := bucket.Open(quarantineName)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, file.Close())
	}()
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	directory, err := bucket.Open(".")
	if err != nil {
		return err
	}
	restored, renameErr := renameRecordFile(
		directory,
		file,
		quarantineName,
		recordName,
		false,
	)
	syncErr := syncRecordDirectory(directory)
	closeErr := directory.Close()
	if err := errors.Join(renameErr, syncErr, closeErr); err != nil {
		return err
	}
	if !restored {
		return errors.New(
			"workspace: quarantined removal record was not restored",
		)
	}
	current, err := bucket.Lstat(recordName)
	if err != nil {
		return err
	}
	if !os.SameFile(opened, current) {
		return errors.New(
			"workspace: restored removal record changed identity",
		)
	}
	return nil
}

func syncRecordBucket(
	bucket *os.Root,
	recordPath string,
) error {
	directory, err := bucket.Open(".")
	if err != nil {
		return fmt.Errorf(
			"workspace: open record directory %q for sync: %w",
			filepath.Dir(recordPath),
			err,
		)
	}
	syncErr := syncRecordDirectory(directory)
	closeErr := directory.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf(
			"workspace: sync record directory %q: %w",
			filepath.Dir(recordPath),
			err,
		)
	}
	return nil
}

func (m *Manager) verifyRemovalRecordFile(
	file *os.File,
	path string,
	removal Removal,
) (os.FileInfo, error) {
	opened, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf(
			"workspace: inspect opened removal record %q: %w",
			path,
			err,
		)
	}
	if !opened.Mode().IsRegular() {
		return nil, fmt.Errorf(
			"workspace: removal record %q is not a regular file",
			path,
		)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxWorkspaceRecordSize+1))
	if err != nil {
		return nil, fmt.Errorf(
			"workspace: read removal record %q: %w",
			path,
			err,
		)
	}
	if len(raw) > maxWorkspaceRecordSize {
		return nil, fmt.Errorf(
			"workspace: removal record %q exceeds %d bytes",
			path,
			maxWorkspaceRecordSize,
		)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var record workspaceRecord
	if err := decoder.Decode(&record); err != nil {
		return nil, fmt.Errorf(
			"workspace: decode removal record %q: %w",
			path,
			err,
		)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf(
			"workspace: removal record %q has trailing data",
			path,
		)
	}
	if err := m.validateWorkspaceRecord(record); err != nil {
		return nil, fmt.Errorf(
			"workspace: validate removal record %q: %w",
			path,
			err,
		)
	}
	if !sameWorkspace(record.workspace(), removal.Workspace) {
		return nil, errors.New(
			"workspace: removal acknowledgement does not match record",
		)
	}
	if record.Removal == nil ||
		record.Removal.OperationID != removal.operationID {
		return nil, errors.New(
			"workspace: removal record changed before acknowledgement",
		)
	}
	return opened, nil
}
