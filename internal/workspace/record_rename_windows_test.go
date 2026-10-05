//go:build windows

package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallAndReplaceWorkspaceRecordOnWindows(t *testing.T) {
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
		Branch: "windows-record",
	}
	if err := manager.writeWorkspaceRecord(target, nil); err != nil {
		t.Fatalf("install workspace record: %v", err)
	}
	record, exists, err := manager.readWorkspaceRecord(target.Path)
	if err != nil || !exists {
		t.Fatalf("read installed record: exists=%v err=%v", exists, err)
	}
	record.PreparationCommitted = true
	if err := manager.replaceWorkspaceRecord(record); err != nil {
		t.Fatalf("replace workspace record: %v", err)
	}
	replaced, exists, err := manager.readWorkspaceRecord(target.Path)
	if err != nil || !exists {
		t.Fatalf("read replaced record: exists=%v err=%v", exists, err)
	}
	if !replaced.PreparationCommitted {
		t.Fatalf("replaced record = %+v", replaced)
	}
}

func TestNoReplaceRenamesPreserveWindowsTargets(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()
	source, err := root.OpenFile(
		"source",
		os.O_RDWR|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	defer source.Close()
	if _, err := source.WriteString("source\n"); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(rootPath, "target"),
		[]byte("target\n"),
		0o600,
	); err != nil {
		t.Fatalf("write target: %v", err)
	}
	directory, err := os.Open(rootPath)
	if err != nil {
		t.Fatalf("open directory: %v", err)
	}
	installed, renameErr := renameRecordFile(
		directory,
		source,
		"source",
		"target",
		false,
	)
	closeErr := directory.Close()
	if renameErr == nil || installed || closeErr != nil {
		t.Fatalf(
			"record no-replace = installed %v, rename %v, close %v",
			installed,
			renameErr,
			closeErr,
		)
	}
	assertFileContents(t, filepath.Join(rootPath, "source"), "source\n")
	assertFileContents(t, filepath.Join(rootPath, "target"), "target\n")

	if err := os.Mkdir(filepath.Join(rootPath, "source-dir"), 0o700); err != nil {
		t.Fatalf("create source directory: %v", err)
	}
	if err := os.Mkdir(filepath.Join(rootPath, "target-dir"), 0o700); err != nil {
		t.Fatalf("create target directory: %v", err)
	}
	sourceInfo, err := os.Lstat(filepath.Join(rootPath, "source-dir"))
	if err != nil {
		t.Fatalf("inspect source directory: %v", err)
	}
	directory, err = os.Open(rootPath)
	if err != nil {
		t.Fatalf("reopen directory: %v", err)
	}
	moved, renameErr := renameDirectoryNoReplace(
		directory,
		sourceInfo,
		"source-dir",
		"target-dir",
	)
	closeErr = directory.Close()
	if renameErr == nil || moved || closeErr != nil {
		t.Fatalf(
			"directory no-replace = moved %v, rename %v, close %v",
			moved,
			renameErr,
			closeErr,
		)
	}
	if _, err := os.Stat(filepath.Join(rootPath, "source-dir")); err != nil {
		t.Fatalf("source directory changed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootPath, "target-dir")); err != nil {
		t.Fatalf("target directory changed: %v", err)
	}
}

func TestRenameWindowsHandleAcceptsReadOnlyFile(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()
	source, err := root.OpenFile(
		"source",
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o444,
	)
	if err != nil {
		t.Fatalf("create read-only source: %v", err)
	}
	defer source.Close()
	if _, err := source.WriteString("source\n"); err != nil {
		t.Fatalf("write read-only source handle: %v", err)
	}
	if err := source.Sync(); err != nil {
		t.Fatalf("sync read-only source handle: %v", err)
	}
	directory, err := os.Open(rootPath)
	if err != nil {
		t.Fatalf("open directory: %v", err)
	}
	installed, renameErr := renameRecordFile(
		directory,
		source,
		"source",
		"target",
		false,
	)
	closeErr := directory.Close()
	if renameErr != nil || !installed || closeErr != nil {
		t.Fatalf(
			"read-only rename = installed %v, rename %v, close %v",
			installed,
			renameErr,
			closeErr,
		)
	}
	assertFileContents(t, filepath.Join(rootPath, "target"), "source\n")
}

func TestMoveRecordFileFlushesThroughRenameHandle(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(rootPath, "source"),
		[]byte("source\n"),
		0o600,
	); err != nil {
		t.Fatalf("write source: %v", err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()
	source, err := root.Open("source")
	if err != nil {
		t.Fatalf("open read-only source: %v", err)
	}
	defer source.Close()
	directory, err := os.Open(rootPath)
	if err != nil {
		t.Fatalf("open directory: %v", err)
	}
	moved, moveErr := moveRecordFile(
		directory,
		source,
		"source",
		"target",
	)
	closeErr := directory.Close()
	if moveErr != nil || !moved || closeErr != nil {
		t.Fatalf(
			"move read-only source = moved %v, move %v, close %v",
			moved,
			moveErr,
			closeErr,
		)
	}
	assertFileContents(t, filepath.Join(rootPath, "target"), "source\n")
}
