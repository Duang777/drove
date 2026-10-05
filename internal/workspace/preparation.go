package workspace

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// AcknowledgePreparation marks a prepared workspace as owned by durable
// session metadata.
func (m *Manager) AcknowledgePreparation(target Workspace) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.acknowledgePreparation(target)
}

func (m *Manager) acknowledgePreparation(
	target Workspace,
) (result error) {
	record, exists, err := m.readWorkspaceRecord(target.Path)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("workspace: preparation record is missing")
	}
	if !sameWorkspace(record.workspace(), target) {
		return errors.New(
			"workspace: preparation acknowledgement does not match record",
		)
	}
	if record.PreparationCommitted &&
		record.Version < workspaceRecordVersion {
		return target.preparation.Close()
	}
	if record.PreparationCommitted &&
		record.BranchOperationID == "" &&
		record.Removal != nil &&
		record.RepositoryEvidence == nil {
		return target.preparation.Close()
	}
	lease := target.preparation
	temporaryLease := false
	if lease == nil {
		lease, err = openRecordedPreparationLease(
			context.Background(),
			m,
			record,
		)
		if err != nil {
			return err
		}
		temporaryLease = true
		defer func() {
			result = errors.Join(result, lease.Close())
		}()
	}
	verifyRepository := func(stage string) error {
		ctx, cancel := context.WithTimeout(
			context.Background(),
			10*time.Second,
		)
		defer cancel()
		if err := lease.repository.verifyBinding(ctx); err != nil {
			return fmt.Errorf(
				"workspace: verify repository %s: %w",
				stage,
				err,
			)
		}
		return nil
	}
	verifyWorktree := func(stage string) error {
		ctx, cancel := context.WithTimeout(
			context.Background(),
			10*time.Second,
		)
		defer cancel()
		if err := m.verifyPreparedWorktreeForAcknowledgement(
			ctx,
			record.workspace(),
			record,
			lease.repository,
		); err != nil {
			return fmt.Errorf(
				"workspace: verify prepared worktree %s: %w",
				stage,
				err,
			)
		}
		return nil
	}
	verifyCtx, cancelVerify := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	verifyErr := m.verifyPreparedWorktreeForAcknowledgement(
		verifyCtx,
		record.workspace(),
		record,
		lease.repository,
	)
	cancelVerify()
	if verifyErr != nil {
		return fmt.Errorf(
			"workspace: verify preparation acknowledgement: %w",
			verifyErr,
		)
	}
	if !record.PreparationCommitted {
		record.PreparationCommitted = true
		if err := m.replaceWorkspaceRecord(record); err != nil {
			return fmt.Errorf("workspace: commit preparation record: %w", err)
		}
		if err := verifyRepository("after committing preparation"); err != nil {
			return err
		}
		if err := verifyWorktree("after committing preparation"); err != nil {
			return err
		}
	}
	if record.BranchOperationID != "" {
		if err := verifyRepository("before ownership cleanup"); err != nil {
			return err
		}
		if err := verifyWorktree("before ownership cleanup"); err != nil {
			return err
		}
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(),
			10*time.Second,
		)
		defer cancel()
		if err := lease.repository.removeOwnershipMarker(
			cleanupCtx,
			record.BranchOperationID,
		); err != nil {
			return err
		}
		if err := verifyRepository("after ownership cleanup"); err != nil {
			return err
		}
		if err := verifyWorktree("after ownership cleanup"); err != nil {
			return err
		}
		record.BranchOperationID = ""
		if err := m.replaceWorkspaceRecord(record); err != nil {
			return fmt.Errorf(
				"workspace: confirm branch ownership cleanup: %w",
				err,
			)
		}
		if err := verifyRepository(
			"after persisting ownership cleanup",
		); err != nil {
			return err
		}
		if err := verifyWorktree(
			"after persisting ownership cleanup",
		); err != nil {
			return err
		}
	}
	if temporaryLease {
		return nil
	}
	return lease.Close()
}

// ReconcilePreparations adopts preparations referenced by durable sessions and
// discards every other uncommitted preparation.
func (m *Manager) ReconcilePreparations(
	ctx context.Context,
	expected []Workspace,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	byAgentID := make(map[string]Workspace, len(expected))
	for _, target := range expected {
		if err := m.validateManagedPath(target); err != nil {
			return fmt.Errorf(
				"workspace: validate expected preparation: %w",
				err,
			)
		}
		if _, exists := byAgentID[target.AgentID]; exists {
			return fmt.Errorf(
				"workspace: duplicate expected preparation for agent %q",
				target.AgentID,
			)
		}
		byAgentID[target.AgentID] = target
	}

	records, err := m.workspaceRecords()
	if err != nil {
		return err
	}
	matched := make(map[string]struct{}, len(expected))
	for _, record := range records {
		target, expected := byAgentID[record.AgentID]
		if expected {
			if _, duplicate := matched[record.AgentID]; duplicate {
				return fmt.Errorf(
					"workspace: duplicate preparation record for agent %q",
					record.AgentID,
				)
			}
			if !sameWorkspace(record.workspace(), target) {
				return fmt.Errorf(
					"workspace: preparation record for agent %q does not match session metadata",
					record.AgentID,
				)
			}
			matched[record.AgentID] = struct{}{}
			if record.Removal != nil {
				continue
			}
			if err := m.acknowledgePreparation(target); err != nil {
				return fmt.Errorf(
					"workspace: adopt preparation for agent %q: %w",
					record.AgentID,
					err,
				)
			}
			continue
		}
		if record.Removal != nil {
			continue
		}
		if record.PreparationCommitted {
			return fmt.Errorf(
				"workspace: committed preparation for agent %q has no session metadata",
				record.AgentID,
			)
		}
		if err := m.discard(ctx, record.workspace()); err != nil {
			return fmt.Errorf(
				"workspace: discard preparation for agent %q: %w",
				record.AgentID,
				err,
			)
		}
	}
	var missing []string
	for agentID := range byAgentID {
		if _, exists := matched[agentID]; !exists {
			missing = append(missing, agentID)
		}
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		return fmt.Errorf(
			"workspace: preparation records missing for agents %q",
			missing,
		)
	}
	return nil
}

func sameWorkspace(left Workspace, right Workspace) bool {
	if left.gitDirectory != "" &&
		right.gitDirectory != "" &&
		left.gitDirectory != right.gitDirectory {
		return false
	}
	if left.directoryIdentity != "" &&
		right.directoryIdentity != "" &&
		left.directoryIdentity != right.directoryIdentity {
		return false
	}
	return left.AgentID == right.AgentID &&
		left.Repository == right.Repository &&
		left.Path == right.Path &&
		left.Branch == right.Branch
}
