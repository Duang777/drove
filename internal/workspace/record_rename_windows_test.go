//go:build windows

package workspace

import (
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
