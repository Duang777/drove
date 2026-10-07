package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/event"
)

func (m *Manager) resolveProcessGroupCleanup(
	ctx context.Context,
	id agent.ID,
	managed *managedAgent,
) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	managed.processGroupMu.Lock()
	defer managed.processGroupMu.Unlock()

	state := managed.processGroupCleanup
	if !state.pending {
		return false, nil
	}
	if state.pid <= 0 {
		return true, fmt.Errorf(
			"%w: agent %q has invalid process group PID %d",
			errProcessGroupCleanupPending,
			id,
			state.pid,
		)
	}
	alive, err := m.processGroupAlive(state.pid)
	if err != nil {
		return true, errors.Join(
			errProcessGroupCleanupPending,
			fmt.Errorf(
				"session: inspect process group %d for agent %q: %w",
				state.pid,
				id,
				err,
			),
		)
	}
	if alive {
		return true, nil
	}

	payload, err := json.Marshal(processGroupCleanupPayload{
		Version: 1,
		PID:     state.pid,
	})
	if err != nil {
		return true, fmt.Errorf(
			"session: encode process group cleanup completion: %w",
			err,
		)
	}
	receipt, commitErr := m.committer.CommitEvents(
		ctx,
		[]event.Draft{event.NewSessionLifecycleDraft(
			string(id),
			string(id),
			processGroupCleanupCompleted,
			string(payload),
		)},
	)
	if receipt.Durable {
		managed.processGroupCleanup = processGroupCleanupState{}
	}
	if commitErr != nil {
		return !receipt.Durable, fmt.Errorf(
			"session: persist process group cleanup completion: %w",
			commitErr,
		)
	}
	return false, nil
}
