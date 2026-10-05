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
	Quarantined bool   `json:"quarantined,omitempty"`
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
	_, err := m.installWorkspaceRecordState(record, true)
	return err
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
	if renameErr != nil {
		_ = directory.Close()
		return installed, fmt.Errorf(
			"workspace: install record %q: %w",
			recordPath,
			renameErr,
		)
	}
	closeErr := file.Close()
	fileOpen = false
	if closeErr != nil {
		_ = directory.Close()
		return true, fmt.Errorf(
			"workspace: close installed record %q: %w",
			recordPath,
			closeErr,
		)
	}
	syncErr := syncRecordDirectory(directory)
	directoryCloseErr := directory.Close()
	if err := errors.Join(syncErr, directoryCloseErr); err != nil {
		return true, fmt.Errorf(
			"workspace: sync record directory %q: %w",
			filepath.Dir(recordPath),
			err,
		)
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
		if record.Removal.Quarantined && !record.Removal.Started {
			return errors.New(
				"workspace: quarantined removal has not been started",
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

func recordAcknowledgementPrefix(
	agentID string,
	operationID string,
) string {
	return "." + agentID + workspaceRecordSuffix + ".ack-" +
		operationID + "-"
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
	var matched string
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		if matched != "" {
			return "", false, fmt.Errorf(
				"workspace: removal acknowledgement %q has multiple quarantined records",
				operationID,
			)
		}
		info, err := bucket.Lstat(entry.Name())
		if err != nil {
			return "", false, err
		}
		if !info.Mode().IsRegular() ||
			info.Mode()&os.ModeSymlink != 0 {
			return "", false, fmt.Errorf(
				"workspace: acknowledgement quarantine %q is not a regular file",
				entry.Name(),
			)
		}
		matched = entry.Name()
	}
	return matched, matched != "", nil
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
	if err := file.Close(); err != nil {
		return fmt.Errorf(
			"workspace: close quarantined removal record %q: %w",
			recordPath,
			err,
		)
	}
	fileOpen = false
	if err := bucket.Remove(name); err != nil {
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
