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
}

type workspaceRuntimeState struct {
	workingDir     string
	resumeOnStart  bool
	removalPending bool
	removed        bool
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
	m.workspace.resumeOnStart = false
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
	m.workspaceMu.RLock()
	defer m.workspaceMu.RUnlock()
	return m.workspace.resumeOnStart &&
		!m.workspace.removalPending &&
		!m.workspace.removed
}

func (m *managedAgent) consumeResumeOnStart() {
	m.workspaceMu.Lock()
	m.workspace.resumeOnStart = false
	m.workspaceMu.Unlock()
}
