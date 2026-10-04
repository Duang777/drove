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
	workspaceRecordSuffix        = ".workspace.json"
	legacyWorkspaceRecordVersion = 1
	workspaceRecordVersion       = 2
	maxWorkspaceRecordSize       = 64 * 1024
)

type workspaceRecord struct {
	Version         int                     `json:"version"`
	AgentID         string                  `json:"agent_id"`
	Repository      string                  `json:"repository"`
	Path            string                  `json:"path"`
	Branch          string                  `json:"branch"`
	ProtectionKnown bool                    `json:"protection_known"`
	IncludedPaths   []string                `json:"included_paths"`
	Removal         *workspaceRemovalRecord `json:"removal,omitempty"`
}

type workspaceRemovalRecord struct {
	OperationID string `json:"operation_id"`
	Force       bool   `json:"force"`
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
		Version:         workspaceRecordVersion,
		AgentID:         target.AgentID,
		Repository:      target.Repository,
		Path:            target.Path,
		Branch:          target.Branch,
		ProtectionKnown: true,
		IncludedPaths:   append([]string{}, includedPaths...),
	}
}

func (r workspaceRecord) workspace() Workspace {
	return Workspace{
		AgentID:    r.AgentID,
		Repository: r.Repository,
		Path:       r.Path,
		Branch:     r.Branch,
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
) error {
	if err := m.validateWorkspaceRecord(record); err != nil {
		return err
	}
	recordPath := workspaceRecordPath(record.Path)
	if requireAbsent {
		if err := ensureAbsent(recordPath); err != nil {
			return err
		}
	} else if err := ensureRegularRecord(recordPath); err != nil {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("workspace: encode record: %w", err)
	}

	directory := filepath.Dir(recordPath)
	file, err := os.CreateTemp(
		directory,
		"."+filepath.Base(recordPath)+".tmp-*",
	)
	if err != nil {
		return fmt.Errorf("workspace: create temporary record %q: %w", recordPath, err)
	}
	temporaryPath := file.Name()
	defer func() {
		if temporaryPath != "" {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("workspace: secure temporary record %q: %w", recordPath, err)
	}
	written, err := file.Write(raw)
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("workspace: write temporary record %q: %w", recordPath, err)
	}
	if written != len(raw) {
		_ = file.Close()
		return fmt.Errorf(
			"workspace: write temporary record %q: %w",
			recordPath,
			io.ErrShortWrite,
		)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("workspace: sync temporary record %q: %w", recordPath, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("workspace: close temporary record %q: %w", recordPath, err)
	}
	if requireAbsent {
		if err := ensureAbsent(recordPath); err != nil {
			return err
		}
	} else if err := ensureRegularRecord(recordPath); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, recordPath); err != nil {
		return fmt.Errorf("workspace: install record %q: %w", recordPath, err)
	}
	temporaryPath = ""
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("workspace: sync record directory %q: %w", directory, err)
	}
	return nil
}

func ensureRegularRecord(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("workspace: inspect record %q: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("workspace: record %q is not a regular file", path)
	}
	return nil
}

func syncDirectory(path string) (result error) {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, directory.Close())
	}()
	return directory.Sync()
}

func (m *Manager) readWorkspaceRecord(
	worktreePath string,
) (workspaceRecord, bool, error) {
	recordPath := workspaceRecordPath(worktreePath)
	info, err := os.Lstat(recordPath)
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
	raw, err := os.ReadFile(recordPath)
	if err != nil {
		return workspaceRecord{}, false, fmt.Errorf(
			"workspace: read record %q: %w",
			recordPath,
			err,
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
			record.Removal != nil {
			return workspaceRecord{}, false, fmt.Errorf(
				"workspace: legacy record %q contains version 2 fields",
				recordPath,
			)
		}
	case workspaceRecordVersion:
		if record.IncludedPaths == nil {
			return workspaceRecord{}, false, fmt.Errorf(
				"workspace: version 2 record %q has no included paths",
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
	return nil
}

func removeWorkspaceRecord(worktreePath string) error {
	recordPath := workspaceRecordPath(worktreePath)
	if err := os.Remove(recordPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("workspace: remove record %q: %w", recordPath, err)
	}
	if err := syncDirectory(filepath.Dir(recordPath)); err != nil {
		return fmt.Errorf(
			"workspace: sync record directory %q: %w",
			filepath.Dir(recordPath),
			err,
		)
	}
	return nil
}
