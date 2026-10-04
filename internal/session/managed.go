package session

import (
	"sync"

	"github.com/Duang777/drove/internal/agent"
)

type managedAgent struct {
	agent *agent.Agent

	workingDir string

	refMu            sync.RWMutex
	vendorSessionRef string
	resumeOnStart    bool
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
