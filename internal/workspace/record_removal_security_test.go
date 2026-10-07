package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveRecordPathPreservesReplacementAfterValidation(t *testing.T) {
	rootPath := t.TempDir()
	recordPath := filepath.Join(rootPath, "temporary")
	if err := os.WriteFile(
		recordPath,
		[]byte("original\n"),
		0o600,
	); err != nil {
		t.Fatalf("write temporary record: %v", err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatalf("open record root: %v", err)
	}
	defer root.Close()
	expected, err := root.Open("temporary")
	if err != nil {
		t.Fatalf("open temporary record: %v", err)
	}
	defer expected.Close()
	originalPath := recordPath + ".original"

	err = removeOwnedRecordPath(
		root,
		"temporary",
		expected,
		"temporary-owned-removal",
		nil,
		func() {
			if err := os.Rename(recordPath, originalPath); err != nil {
				t.Fatalf("move validated temporary record: %v", err)
			}
			if err := os.WriteFile(
				recordPath,
				[]byte("replacement\n"),
				0o600,
			); err != nil {
				t.Fatalf("install replacement temporary record: %v", err)
			}
		},
	)
	if err == nil {
		t.Fatal("temporary record removal accepted a replacement path")
	}
	assertFileContents(t, recordPath, "replacement\n")
	assertFileContents(t, originalPath, "original\n")
}

func TestRemoveWorkspaceRecordPreservesReplacementAfterValidation(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	repository := filepath.Join(t.TempDir(), "repository")
	if err := manager.ensureManagedRoot(); err != nil {
		t.Fatalf("ensure managed root: %v", err)
	}
	if err := manager.ensureManagedBucket(repository); err != nil {
		t.Fatalf("ensure managed bucket: %v", err)
	}
	target := Workspace{
		AgentID:    testAgentID,
		Repository: repository,
		Path: filepath.Join(
			manager.root,
			repositoryHash(repository),
			testAgentID,
		),
		Branch: "record-removal",
	}
	if err := manager.writeWorkspaceRecord(target, nil); err != nil {
		t.Fatalf("write workspace record: %v", err)
	}
	recordPath := workspaceRecordPath(target.Path)
	original, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read workspace record: %v", err)
	}
	originalPath := recordPath + ".original"

	err = manager.removeWorkspaceRecordAfterValidation(
		target.Path,
		func() {
			if err := os.Rename(recordPath, originalPath); err != nil {
				t.Fatalf("move validated record: %v", err)
			}
			if err := os.WriteFile(
				recordPath,
				[]byte("replacement\n"),
				0o600,
			); err != nil {
				t.Fatalf("install replacement record: %v", err)
			}
		},
	)
	if err == nil {
		t.Fatal("record removal accepted a replacement sidecar")
	}
	assertFileContents(t, recordPath, "replacement\n")
	gotOriginal, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatalf("read original sidecar: %v", err)
	}
	if string(gotOriginal) != string(original) {
		t.Fatal("original sidecar changed")
	}
}

func TestAcknowledgedRecordRemovalPreservesQuarantineReplacement(t *testing.T) {
	repository := newTestRepository(t)
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	prepared, err := manager.Prepare(
		context.Background(),
		repository,
		"",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare worktree: %v", err)
	}
	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("acknowledge preparation: %v", err)
	}
	result, err := manager.Remove(
		context.Background(),
		prepared.AgentID,
		true,
	)
	if err != nil || result.State != RemovalComplete {
		t.Fatalf("remove workspace = %+v, %v", result, err)
	}

	recordPath := workspaceRecordPath(prepared.Path)
	recordContents, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read removal record: %v", err)
	}
	prefix := recordAcknowledgementPrefix(
		prepared.AgentID,
		result.Removal.operationID,
	)
	var quarantinePath, originalPath string
	err = manager.removeAcknowledgedWorkspaceRecordAfterQuarantine(
		result.Removal,
		func() error {
			entries, err := os.ReadDir(filepath.Dir(recordPath))
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), prefix) {
					quarantinePath = filepath.Join(
						filepath.Dir(recordPath),
						entry.Name(),
					)
					break
				}
			}
			if quarantinePath == "" {
				return errors.New("acknowledgement quarantine is missing")
			}
			originalPath = quarantinePath + ".original"
			if err := os.Rename(quarantinePath, originalPath); err != nil {
				return err
			}
			return os.WriteFile(quarantinePath, recordContents, 0o600)
		},
	)
	if err == nil {
		t.Fatal("acknowledgement removed a replacement quarantine")
	}
	assertFileContents(t, originalPath, string(recordContents))
	assertFileContents(t, quarantinePath, string(recordContents))
}
