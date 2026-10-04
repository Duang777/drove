package workspace

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

const (
	workspaceRecordSuffix           = ".workspace.json"
	legacyWorkspaceRecordVersion    = 1
	protectedWorkspaceRecordVersion = 2
	workspaceRecordVersion          = 3
	maxWorkspaceRecordSize          = 64 * 1024
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
	Removal              *workspaceRemovalRecord `json:"removal,omitempty"`
}

type workspaceRemovalRecord struct {
	OperationID string `json:"operation_id"`
	Force       bool   `json:"force"`
	Started     bool   `json:"started"`
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
	}
}

func (m *Manager) writeWorkspaceRecord(
	target Workspace,
	includedPaths []string,
) error {
	record := newWorkspaceRecord(target, includedPaths)
	return m.installWorkspaceRecord(record, true)
}

func (m *Manager) replaceWorkspaceRecord(record workspaceRecord) error {
	return m.installWorkspaceRecord(record, false)
}

func (m *Manager) installWorkspaceRecord(
	record workspaceRecord,
	requireAbsent bool,
) (result error) {
	if err := m.validateWorkspaceRecord(record); err != nil {
		return err
	}
	recordPath := workspaceRecordPath(record.Path)
	bucket, agentID, err := m.openRecordBucket(record.Path)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, bucket.Close())
	}()
	name := agentID + workspaceRecordSuffix
	if err := checkRecordTarget(bucket, name, recordPath, requireAbsent); err != nil {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("workspace: encode record: %w", err)
	}
	if len(raw) > maxWorkspaceRecordSize {
		return fmt.Errorf(
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
		return fmt.Errorf("workspace: create temporary record %q: %w", recordPath, err)
	}
	fileOpen := true
	defer func() {
		if fileOpen {
			result = errors.Join(result, file.Close())
		}
		if temporaryName != "" {
			if err := bucket.Remove(temporaryName); !errors.Is(
				err,
				os.ErrNotExist,
			) {
				result = errors.Join(result, err)
			}
		}
	}()
	written, err := file.Write(raw)
	if err != nil {
		return fmt.Errorf("workspace: write temporary record %q: %w", recordPath, err)
	}
	if written != len(raw) {
		return fmt.Errorf(
			"workspace: write temporary record %q: %w",
			recordPath,
			io.ErrShortWrite,
		)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("workspace: sync temporary record %q: %w", recordPath, err)
	}
	if err := checkRecordTarget(bucket, name, recordPath, requireAbsent); err != nil {
		return err
	}
	directory, err := bucket.Open(".")
	if err != nil {
		return fmt.Errorf("workspace: open record directory %q: %w", recordPath, err)
	}
	if err := renameRecordFile(
		directory,
		file,
		temporaryName,
		name,
	); err != nil {
		_ = directory.Close()
		return fmt.Errorf("workspace: install record %q: %w", recordPath, err)
	}
	temporaryName = ""
	closeErr := file.Close()
	fileOpen = false
	if closeErr != nil {
		_ = directory.Close()
		return fmt.Errorf(
			"workspace: close installed record %q: %w",
			recordPath,
			closeErr,
		)
	}
	syncErr := syncRecordDirectory(directory)
	directoryCloseErr := directory.Close()
	if err := errors.Join(syncErr, directoryCloseErr); err != nil {
		return fmt.Errorf(
			"workspace: sync record directory %q: %w",
			filepath.Dir(recordPath),
			err,
		)
	}
	return nil
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
			record.BranchOperationID != "" {
			return workspaceRecord{}, false, fmt.Errorf(
				"workspace: version 2 record %q contains version 3 fields",
				recordPath,
			)
		}
		record.PreparationCommitted = true
		if record.Removal != nil {
			record.Removal.Started = true
		}
	case workspaceRecordVersion:
		if record.IncludedPaths == nil {
			return workspaceRecord{}, false, fmt.Errorf(
				"workspace: version 3 record %q has no included paths",
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
		record.Version != workspaceRecordVersion {
		return fmt.Errorf("workspace: unsupported record version %d", record.Version)
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
	}
	if record.BranchOperationID != "" {
		operationID, err := uuid.Parse(record.BranchOperationID)
		if err != nil || operationID.String() != record.BranchOperationID {
			return errors.New(
				"workspace: branch operation ID is not a canonical UUID",
			)
		}
	}
	return nil
}

func (m *Manager) removeWorkspaceRecord(
	worktreePath string,
	expectedRemovalID string,
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
	if expectedRemovalID != "" {
		if err := verifyRemovalRecord(
			bucket,
			name,
			recordPath,
			expectedRemovalID,
		); err != nil {
			return err
		}
	}
	if err := bucket.Remove(name); err != nil {
		return fmt.Errorf("workspace: remove record %q: %w", recordPath, err)
	}
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

func verifyRemovalRecord(
	bucket *os.Root,
	name string,
	path string,
	expectedOperationID string,
) (result error) {
	file, err := bucket.Open(name)
	if err != nil {
		return fmt.Errorf("workspace: open removal record %q: %w", path, err)
	}
	defer func() {
		result = errors.Join(result, file.Close())
	}()
	opened, err := file.Stat()
	if err != nil {
		return fmt.Errorf(
			"workspace: inspect opened removal record %q: %w",
			path,
			err,
		)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxWorkspaceRecordSize+1))
	if err != nil {
		return fmt.Errorf("workspace: read removal record %q: %w", path, err)
	}
	if len(raw) > maxWorkspaceRecordSize {
		return fmt.Errorf(
			"workspace: removal record %q exceeds %d bytes",
			path,
			maxWorkspaceRecordSize,
		)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var record workspaceRecord
	if err := decoder.Decode(&record); err != nil {
		return fmt.Errorf("workspace: decode removal record %q: %w", path, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf(
			"workspace: removal record %q has trailing data",
			path,
		)
	}
	if record.Removal == nil ||
		record.Removal.OperationID != expectedOperationID {
		return errors.New(
			"workspace: removal record changed before acknowledgement",
		)
	}
	current, err := bucket.Lstat(name)
	if err != nil {
		return fmt.Errorf("workspace: reinspect removal record %q: %w", path, err)
	}
	if !os.SameFile(opened, current) {
		return errors.New(
			"workspace: removal record changed before acknowledgement",
		)
	}
	return nil
}
