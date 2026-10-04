package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestAcknowledgePreparationClearsBranchOwnershipMarker(t *testing.T) {
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
		t.Fatalf("prepare workspace: %v", err)
	}
	if prepared.branchOperationID == "" {
		t.Fatal("prepared workspace has no branch operation ID")
	}
	exists, err := manager.branchOwnershipMarkerExists(
		context.Background(),
		repository,
		prepared.branchOperationID,
	)
	if err != nil || !exists {
		t.Fatalf("branch ownership marker exists = %v, err=%v", exists, err)
	}

	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("acknowledge preparation: %v", err)
	}
	exists, err = manager.branchOwnershipMarkerExists(
		context.Background(),
		repository,
		prepared.branchOperationID,
	)
	if err != nil || exists {
		t.Fatalf("branch ownership marker exists = %v, err=%v", exists, err)
	}
	record, hasRecord, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !hasRecord {
		t.Fatalf("read acknowledged record: exists=%v err=%v", hasRecord, err)
	}
	if !record.PreparationCommitted || record.BranchOperationID != "" {
		t.Fatalf("acknowledged record = %+v", record)
	}
}

func TestReconcilePreparationsRejectsCommittedMetadataMismatch(t *testing.T) {
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
		t.Fatalf("prepare workspace: %v", err)
	}
	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("acknowledge preparation: %v", err)
	}
	otherRepository := filepath.Join(t.TempDir(), "other-repository")
	expected := Workspace{
		AgentID:    prepared.AgentID,
		Repository: otherRepository,
		Path: filepath.Join(
			manager.root,
			repositoryHash(otherRepository),
			prepared.AgentID,
		),
		Branch: prepared.Branch,
	}

	err = manager.ReconcilePreparations(
		context.Background(),
		[]Workspace{expected},
	)
	if err == nil || !strings.Contains(err.Error(), "does not match session metadata") {
		t.Fatalf("reconcile mismatched committed record error = %v", err)
	}
	if _, err := os.Lstat(prepared.Path); err != nil {
		t.Fatalf("mismatched committed workspace was changed: %v", err)
	}
}

func TestReconcilePreparationsRejectsMissingDurableRecord(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	repository := filepath.Join(t.TempDir(), "repository")
	expected := Workspace{
		AgentID:    testAgentID,
		Repository: repository,
		Path: filepath.Join(
			manager.root,
			repositoryHash(repository),
			testAgentID,
		),
		Branch: "missing-record",
	}

	err = manager.ReconcilePreparations(
		context.Background(),
		[]Workspace{expected},
	)
	if err == nil || !strings.Contains(err.Error(), "records missing") {
		t.Fatalf("reconcile missing record error = %v", err)
	}
}

func TestReconcilePreparationsLeavesRemovalForRemovalReconciliation(t *testing.T) {
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
		t.Fatalf("prepare workspace: %v", err)
	}
	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("acknowledge preparation: %v", err)
	}
	result, err := manager.Remove(context.Background(), prepared.AgentID, true)
	if err != nil {
		t.Fatalf("remove workspace: %v", err)
	}

	if err := manager.ReconcilePreparations(context.Background(), nil); err != nil {
		t.Fatalf("reconcile preparations with removal record: %v", err)
	}
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists || record.Removal == nil {
		t.Fatalf(
			"removal record after preparation reconciliation = %+v, exists=%v err=%v",
			record,
			exists,
			err,
		)
	}
	if err := manager.AcknowledgeRemoval(result.Removal); err != nil {
		t.Fatalf("acknowledge removal: %v", err)
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
