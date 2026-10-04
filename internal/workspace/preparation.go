package workspace

import (
	"context"
	"errors"
	"fmt"
)

// AcknowledgePreparation marks a prepared workspace as owned by durable
// session metadata.
func (m *Manager) AcknowledgePreparation(target Workspace) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.acknowledgePreparation(target)
}

func (m *Manager) acknowledgePreparation(target Workspace) error {
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
	if record.PreparationCommitted {
		return nil
	}
	record.Version = workspaceRecordVersion
	record.PreparationCommitted = true
	if err := m.replaceWorkspaceRecord(record); err != nil {
		return fmt.Errorf("workspace: commit preparation record: %w", err)
	}
	return nil
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
	for _, record := range records {
		if record.PreparationCommitted {
			continue
		}
		target, expected := byAgentID[record.AgentID]
		if expected && sameWorkspace(record.workspace(), target) {
			if err := m.acknowledgePreparation(target); err != nil {
				return fmt.Errorf(
					"workspace: adopt preparation for agent %q: %w",
					record.AgentID,
					err,
				)
			}
			continue
		}
		if err := m.discard(ctx, record.workspace()); err != nil {
			return fmt.Errorf(
				"workspace: discard preparation for agent %q: %w",
				record.AgentID,
				err,
			)
		}
	}
	return nil
}

func sameWorkspace(left Workspace, right Workspace) bool {
	return left.AgentID == right.AgentID &&
		left.Repository == right.Repository &&
		left.Path == right.Path &&
		left.Branch == right.Branch
}
