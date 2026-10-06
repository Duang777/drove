package workspace

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

const (
	workspaceRecordRemovalLegacyPrefix = ".drove-record-remove-"
	workspaceRecordRemovalPrefix       = workspaceRecordRemovalLegacyPrefix + "v1-"
)

type workspaceRecordArtifactKind uint8

const (
	workspaceRecordTemporaryArtifact workspaceRecordArtifactKind = iota + 1
	workspaceRecordRemovalArtifact
)

type workspaceRecordArtifact struct {
	name        string
	agentID     string
	operationID string
	kind        workspaceRecordArtifactKind
}

func workspaceRecordTemporaryName(recordName string) string {
	return "." + recordName + ".tmp-" + uuid.NewString()
}

func workspaceRecordRemovalName(record workspaceRecord) string {
	operationID := "none"
	if record.Removal != nil {
		operationID = record.Removal.OperationID
	}
	return workspaceRecordRemovalPrefix + record.AgentID + "-" +
		operationID + "-" + uuid.NewString()
}

func parseWorkspaceRecordArtifactName(
	name string,
) (workspaceRecordArtifact, bool) {
	if temporary, valid := parseWorkspaceRecordTemporaryName(name); valid {
		return temporary, true
	}
	return parseWorkspaceRecordRemovalName(name)
}

func parseWorkspaceRecordTemporaryName(
	name string,
) (workspaceRecordArtifact, bool) {
	if !strings.HasPrefix(name, ".") {
		return workspaceRecordArtifact{}, false
	}
	const marker = workspaceRecordSuffix + ".tmp-"
	raw := strings.TrimPrefix(name, ".")
	index := strings.Index(raw, marker)
	if index < 0 {
		return workspaceRecordArtifact{}, false
	}
	agentID := raw[:index]
	if validateAgentID(agentID) != nil {
		return workspaceRecordArtifact{}, false
	}
	nonce := raw[index+len(marker):]
	if !canonicalUUID(nonce) {
		return workspaceRecordArtifact{}, false
	}
	return workspaceRecordArtifact{
		name:    name,
		agentID: agentID,
		kind:    workspaceRecordTemporaryArtifact,
	}, true
}

func parseWorkspaceRecordRemovalName(
	name string,
) (workspaceRecordArtifact, bool) {
	raw, found := strings.CutPrefix(name, workspaceRecordRemovalPrefix)
	if !found || len(raw) < 36+1+4+1+36 {
		return workspaceRecordArtifact{}, false
	}
	agentID := raw[:36]
	if validateAgentID(agentID) != nil || raw[36] != '-' {
		return workspaceRecordArtifact{}, false
	}
	raw = raw[37:]
	operationID := "none"
	switch {
	case strings.HasPrefix(raw, "none-"):
		raw = strings.TrimPrefix(raw, "none-")
	default:
		if len(raw) < 36+1+36 || raw[36] != '-' {
			return workspaceRecordArtifact{}, false
		}
		operationID = raw[:36]
		if !canonicalUUID(operationID) {
			return workspaceRecordArtifact{}, false
		}
		raw = raw[37:]
	}
	if !canonicalUUID(raw) {
		return workspaceRecordArtifact{}, false
	}
	return workspaceRecordArtifact{
		name:        name,
		agentID:     agentID,
		operationID: operationID,
		kind:        workspaceRecordRemovalArtifact,
	}, true
}

func canonicalUUID(raw string) bool {
	if len(raw) != 36 {
		return false
	}
	parsed, err := uuid.Parse(raw)
	return err == nil && parsed.String() == raw
}

func (m *Manager) recoverWorkspaceRecordArtifacts(
	bucket *os.Root,
	bucketName string,
	agentFilter string,
) error {
	entries, err := readRootDirectory(bucket)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		artifact, valid := parseWorkspaceRecordArtifactName(entry.Name())
		if !valid {
			if strings.HasPrefix(
				entry.Name(),
				workspaceRecordRemovalLegacyPrefix,
			) ||
				strings.Contains(
					entry.Name(),
					workspaceRecordSuffix+".tmp-",
				) {
				return fmt.Errorf(
					"workspace: record artifact %q has an invalid name",
					entry.Name(),
				)
			}
			continue
		}
		if agentFilter != "" && artifact.agentID != agentFilter {
			continue
		}
		if err := m.recoverWorkspaceRecordArtifact(
			bucket,
			bucketName,
			artifact,
		); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) recoverWorkspaceRecordArtifact(
	bucket *os.Root,
	bucketName string,
	artifact workspaceRecordArtifact,
) (result error) {
	info, err := bucket.Lstat(artifact.name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf(
			"workspace: record artifact %q is not a regular file",
			artifact.name,
		)
	}
	file, err := bucket.Open(artifact.name)
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
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return fmt.Errorf(
			"workspace: record artifact %q changed while opening",
			artifact.name,
		)
	}
	worktreePath := filepath.Join(m.root, bucketName, artifact.agentID)
	recordPath := workspaceRecordPath(worktreePath)
	if artifact.kind == workspaceRecordTemporaryArtifact {
		return removeOwnedRecordPath(
			bucket,
			artifact.name,
			file,
			workspaceRecordTemporaryName(
				artifact.agentID+workspaceRecordSuffix,
			),
			nil,
			nil,
		)
	}
	if artifact.kind != workspaceRecordRemovalArtifact {
		return fmt.Errorf(
			"workspace: record artifact %q has an invalid kind",
			artifact.name,
		)
	}
	record, err := m.decodeWorkspaceRecordFile(
		file,
		recordPath,
		worktreePath,
	)
	if err != nil {
		return err
	}
	if record.AgentID != artifact.agentID {
		return fmt.Errorf(
			"workspace: record artifact %q has the wrong agent",
			artifact.name,
		)
	}
	expectedOperation := "none"
	if record.Removal != nil {
		expectedOperation = record.Removal.OperationID
	}
	if artifact.operationID != expectedOperation {
		return fmt.Errorf(
			"workspace: record artifact %q has the wrong operation",
			artifact.name,
		)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return removeOwnedRecordPath(
		bucket,
		artifact.name,
		file,
		workspaceRecordRemovalName(record),
		func(candidate *os.File) error {
			if _, err := candidate.Seek(0, io.SeekStart); err != nil {
				return err
			}
			current, err := m.decodeWorkspaceRecordFile(
				candidate,
				recordPath,
				worktreePath,
			)
			if err != nil {
				return err
			}
			if current.AgentID != artifact.agentID {
				return errors.New(
					"workspace: record artifact changed ownership",
				)
			}
			currentOperation := "none"
			if current.Removal != nil {
				currentOperation = current.Removal.OperationID
			}
			if currentOperation != artifact.operationID {
				return errors.New(
					"workspace: record artifact changed operation",
				)
			}
			return nil
		},
		nil,
	)
}
