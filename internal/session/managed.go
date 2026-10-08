package session

import (
	"sync"
	"time"

	"github.com/Duang777/drove/internal/agent"
)

type managedAgent struct {
	agent *agent.Agent

	stateMu  sync.RWMutex
	stateSeq StateSeq

	refMu            sync.RWMutex
	vendorSessionRef string

	workspaceMu sync.RWMutex
	workspace   workspaceRuntimeState

	processGroupMu      sync.Mutex
	processGroupCleanup processGroupCleanupState
}

type workspaceRuntimeState struct {
	workingDir     string
	resumeOnStart  bool
	removalPending bool
	removed        bool
}

type processGroupCleanupState struct {
	pending bool
	pid     int
}

type managedStateView struct {
	state          agent.State
	stateSeq       StateSeq
	createdAt      time.Time
	updatedAt      time.Time
	stateSince     time.Time
	lastError      string
	lastTransition *agent.Evidence
}

func newManagedAgent(target *agent.Agent) *managedAgent {
	return &managedAgent{agent: target}
}

func (m *managedAgent) currentStateSeq() StateSeq {
	m.stateMu.RLock()
	defer m.stateMu.RUnlock()
	return m.stateSeq
}

func (m *managedAgent) setStateSeq(sequence StateSeq) {
	m.stateMu.Lock()
	m.stateSeq = sequence
	m.stateMu.Unlock()
}

func (m *managedAgent) applyCommitted(
	prepared agent.PreparedChange,
	sequence StateSeq,
) error {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	if err := m.agent.ApplyCommitted(prepared); err != nil {
		return err
	}
	if sequence != 0 {
		m.stateSeq = sequence
	}
	return nil
}

func (m *managedAgent) stateView() managedStateView {
	m.stateMu.RLock()
	defer m.stateMu.RUnlock()
	return managedStateView{
		state:          m.agent.State(),
		stateSeq:       m.stateSeq,
		createdAt:      m.agent.CreatedAt(),
		updatedAt:      m.agent.UpdatedAt(),
		stateSince:     m.agent.StateSince(),
		lastError:      m.agent.LastError(),
		lastTransition: m.agent.LastTransition(),
	}
}

func (m *managedAgent) vendorSessionReference() string {
	m.refMu.RLock()
	defer m.refMu.RUnlock()
	return m.vendorSessionRef
}

func (m *managedAgent) setVendorSessionReference(ref string) {
	if ref == "" {
		return
	}
	m.refMu.Lock()
	m.vendorSessionRef = ref
	m.refMu.Unlock()
}

func (m *managedAgent) setWorkspaceState(state workspaceRuntimeState) {
	m.workspaceMu.Lock()
	m.workspace = state
	m.workspaceMu.Unlock()
}

func (m *managedAgent) workspaceState() workspaceRuntimeState {
	m.workspaceMu.RLock()
	defer m.workspaceMu.RUnlock()
	return m.workspace
}

func (m *managedAgent) setWorkspaceRemovalPending() {
	m.workspaceMu.Lock()
	if !m.workspace.removed {
		m.workspace.removalPending = true
	}
	m.workspaceMu.Unlock()
}

func (m *managedAgent) clearWorkspaceRemovalPending() {
	m.workspaceMu.Lock()
	m.workspace.removalPending = false
	m.workspaceMu.Unlock()
}

func (m *managedAgent) applyWorkspaceRemoved() {
	m.workspaceMu.Lock()
	m.workspace.workingDir = ""
	m.workspace.resumeOnStart = false
	m.workspace.removalPending = false
	m.workspace.removed = true
	m.workspaceMu.Unlock()
}

func (m *managedAgent) shouldResumeOnStart() bool {
	if m.processGroupCleanupPending() {
		return false
	}
	return m.hasResumeOnStart()
}

func (m *managedAgent) hasResumeOnStart() bool {
	m.workspaceMu.RLock()
	defer m.workspaceMu.RUnlock()
	return m.workspace.resumeOnStart &&
		!m.workspace.removalPending &&
		!m.workspace.removed
}

func (m *managedAgent) completeResumeOnStart(
	commit func() (commitReceipt, error),
) (bool, error) {
	m.workspaceMu.Lock()
	defer m.workspaceMu.Unlock()
	if !m.workspace.resumeOnStart ||
		m.workspace.removalPending ||
		m.workspace.removed {
		return false, nil
	}
	receipt, err := commit()
	if receipt.Durable {
		m.workspace.resumeOnStart = false
	}
	return true, err
}

func (m *managedAgent) cancelResumeOnStartAfter(
	commit func(pending bool) error,
) error {
	m.workspaceMu.Lock()
	defer m.workspaceMu.Unlock()
	if err := commit(m.workspace.resumeOnStart); err != nil {
		return err
	}
	m.workspace.resumeOnStart = false
	return nil
}

func (m *managedAgent) setProcessGroupCleanupPending(pid int) {
	m.processGroupMu.Lock()
	m.processGroupCleanup = processGroupCleanupState{
		pending: true,
		pid:     pid,
	}
	m.processGroupMu.Unlock()
}

func (m *managedAgent) processGroupCleanupPending() bool {
	m.processGroupMu.Lock()
	defer m.processGroupMu.Unlock()
	return m.processGroupCleanup.pending
}
