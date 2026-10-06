package workspace

import (
	"os"
	"path/filepath"
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

	err = removeRecordPathIfSameAfterValidation(
		root,
		"temporary",
		expected,
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
