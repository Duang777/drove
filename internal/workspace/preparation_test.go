package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestReconcilePreparationsAdoptsDurableAndDiscardsOrphan(t *testing.T) {
	repository := newTestRepository(t)
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	durable, err := manager.Prepare(
		context.Background(),
		repository,
		"durable-preparation",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare durable workspace: %v", err)
	}
	orphan, err := manager.Prepare(
		context.Background(),
		repository,
		"orphan-preparation",
		secondTestAgentID,
	)
	if err != nil {
		t.Fatalf("prepare orphan workspace: %v", err)
	}

	if err := manager.ReconcilePreparations(
		context.Background(),
		[]Workspace{durable},
	); err != nil {
		t.Fatalf("reconcile preparations: %v", err)
	}

	record, exists, err := manager.readWorkspaceRecord(durable.Path)
	if err != nil || !exists || !record.PreparationCommitted {
		t.Fatalf(
			"durable preparation record = %+v, exists=%v err=%v",
			record,
			exists,
			err,
		)
	}
	if _, err := os.Lstat(durable.Path); err != nil {
		t.Fatalf("durable preparation was removed: %v", err)
	}
	runGit(
		t,
		repository,
		"show-ref",
		"--verify",
		"refs/heads/"+durable.Branch,
	)

	if _, err := os.Lstat(orphan.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan workspace remains or inspect failed: %v", err)
	}
	if _, err := os.Lstat(workspaceRecordPath(orphan.Path)); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("orphan preparation record remains or inspect failed: %v", err)
	}
	command := exec.Command(
		"git",
		"-C",
		repository,
		"show-ref",
		"--verify",
		"--quiet",
		"refs/heads/"+orphan.Branch,
	)
	if err := command.Run(); err == nil {
		t.Fatalf("orphan branch %q remains", orphan.Branch)
	}
}

func TestVersionTwoRecordLoadsAsCommittedWithStartedRemoval(t *testing.T) {
	repository := newTestRepository(t)
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	path := filepath.Join(
		manager.root,
		repositoryHash(repository),
		testAgentID,
	)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create record directory: %v", err)
	}
	raw, err := json.Marshal(struct {
		Version         int                     `json:"version"`
		AgentID         string                  `json:"agent_id"`
		Repository      string                  `json:"repository"`
		Path            string                  `json:"path"`
		Branch          string                  `json:"branch"`
		ProtectionKnown bool                    `json:"protection_known"`
		IncludedPaths   []string                `json:"included_paths"`
		Removal         *workspaceRemovalRecord `json:"removal,omitempty"`
	}{
		Version:         protectedWorkspaceRecordVersion,
		AgentID:         testAgentID,
		Repository:      repository,
		Path:            path,
		Branch:          "legacy-removal",
		ProtectionKnown: true,
		IncludedPaths:   []string{},
		Removal: &workspaceRemovalRecord{
			OperationID: "99999999-9999-4999-8999-999999999999",
		},
	})
	if err != nil {
		t.Fatalf("encode version 2 record: %v", err)
	}
	if err := os.WriteFile(workspaceRecordPath(path), raw, 0o600); err != nil {
		t.Fatalf("write version 2 record: %v", err)
	}

	record, exists, err := manager.readWorkspaceRecord(path)
	if err != nil || !exists {
		t.Fatalf("read version 2 record: exists=%v err=%v", exists, err)
	}
	if !record.PreparationCommitted ||
		record.Removal == nil ||
		!record.Removal.Started {
		t.Fatalf("migrated version 2 record = %+v", record)
	}
}
