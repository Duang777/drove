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
)

const (
	workspaceRecordSuffix  = ".workspace.json"
	workspaceRecordVersion = 1
	maxWorkspaceRecordSize = 64 * 1024
)

type workspaceRecord struct {
	Version    int    `json:"version"`
	AgentID    string `json:"agent_id"`
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Branch     string `json:"branch"`
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

func (m *Manager) writeWorkspaceRecord(target Workspace) error {
	recordPath := workspaceRecordPath(target.Path)
	if err := ensureAbsent(recordPath); err != nil {
		return err
	}
	raw, err := json.Marshal(workspaceRecord{
		Version:    workspaceRecordVersion,
		AgentID:    target.AgentID,
		Repository: target.Repository,
		Path:       target.Path,
		Branch:     target.Branch,
	})
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
	if err := ensureAbsent(recordPath); err != nil {
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
) (Workspace, bool, error) {
	recordPath := workspaceRecordPath(worktreePath)
	info, err := os.Lstat(recordPath)
	if errors.Is(err, os.ErrNotExist) {
		return Workspace{}, false, nil
	}
	if err != nil {
		return Workspace{}, false, fmt.Errorf(
			"workspace: inspect record %q: %w",
			recordPath,
			err,
		)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Workspace{}, false, fmt.Errorf(
			"workspace: record %q is not a regular file",
			recordPath,
		)
	}
	if info.Size() > maxWorkspaceRecordSize {
		return Workspace{}, false, fmt.Errorf(
			"workspace: record %q exceeds %d bytes",
			recordPath,
			maxWorkspaceRecordSize,
		)
	}
	raw, err := os.ReadFile(recordPath)
	if err != nil {
		return Workspace{}, false, fmt.Errorf(
			"workspace: read record %q: %w",
			recordPath,
			err,
		)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var record workspaceRecord
	if err := decoder.Decode(&record); err != nil {
		return Workspace{}, false, fmt.Errorf(
			"workspace: decode record %q: %w",
			recordPath,
			err,
		)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Workspace{}, false, fmt.Errorf(
			"workspace: record %q has trailing data",
			recordPath,
		)
	}
	if record.Version != workspaceRecordVersion {
		return Workspace{}, false, fmt.Errorf(
			"workspace: record %q has unsupported version %d",
			recordPath,
			record.Version,
		)
	}
	target := Workspace{
		AgentID:    record.AgentID,
		Repository: record.Repository,
		Path:       record.Path,
		Branch:     record.Branch,
	}
	if filepath.Clean(worktreePath) != target.Path {
		return Workspace{}, false, fmt.Errorf(
			"workspace: record %q path mismatch",
			recordPath,
		)
	}
	if target.Branch == "" {
		return Workspace{}, false, fmt.Errorf(
			"workspace: record %q has an empty branch",
			recordPath,
		)
	}
	if err := m.validateManagedPath(target); err != nil {
		return Workspace{}, false, fmt.Errorf(
			"workspace: validate record %q: %w",
			recordPath,
			err,
		)
	}
	return target, true, nil
}

func removeWorkspaceRecord(worktreePath string) error {
	recordPath := workspaceRecordPath(worktreePath)
	if err := os.Remove(recordPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("workspace: remove record %q: %w", recordPath, err)
	}
	return nil
}
