package session

import (
	"sync"

	"github.com/Duang777/drove/internal/agent"
)

type managedAgent struct {
	agent *agent.Agent

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

func newManagedAgent(target *agent.Agent) *managedAgent {
	return &managedAgent{agent: target}
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

func (m *managedAgent) cancelResumeOnStartAfter(commit func() error) error {
	m.workspaceMu.Lock()
	defer m.workspaceMu.Unlock()
	if err := commit(); err != nil {
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
