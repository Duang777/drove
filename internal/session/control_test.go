package session

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/event"
)

func TestStopWaitsForInFlightInput(t *testing.T) {
	manager, _ := newTestManager(t)
	id := agent.ID("agent-1")
	target := agent.New(
		id,
		agent.WithName("agent"),
		agent.WithVendor("generic"),
		agent.WithRunMode(agent.RunModeInteractive),
	)
	process := newBlockingControlProcess()
	running := &runningSession{process: process}
	manager.mu.Lock()
	manager.agents[id] = newManagedAgent(target)
	manager.sessions[id] = running
	manager.mu.Unlock()

	inputDone := make(chan error, 1)
	go func() {
		_, err := manager.SendInput(id, []byte("input"))
		inputDone <- err
	}()
	<-process.writeStarted

	stopDone := make(chan error, 1)
	go func() {
		stopDone <- manager.Stop(id)
	}()

	closedBeforeWriteFinished := false
	select {
	case <-process.closeStarted:
		closedBeforeWriteFinished = true
	case <-time.After(25 * time.Millisecond):
	}
	close(process.writeRelease)

	if err := <-inputDone; err != nil {
		t.Fatalf("send input: %v", err)
	}
	if err := <-stopDone; err != nil {
		t.Fatalf("stop: %v", err)
	}
	if closedBeforeWriteFinished {
		t.Fatal("Stop closed the PTY while input was still being written")
	}
}

func TestTerminalReplyWaitsForInFlightInput(t *testing.T) {
	manager, _ := newTestManager(t)
	id := agent.ID("agent-1")
	target := agent.New(
		id,
		agent.WithName("agent"),
		agent.WithVendor("generic"),
		agent.WithRunMode(agent.RunModeInteractive),
	)
	process := newBlockingControlProcess()
	running := &runningSession{process: process}
	manager.mu.Lock()
	manager.agents[id] = newManagedAgent(target)
	manager.sessions[id] = running
	manager.mu.Unlock()

	inputDone := make(chan error, 1)
	go func() {
		_, err := manager.SendInput(id, []byte("input"))
		inputDone <- err
	}()
	<-process.writeStarted

	replyDone := make(chan error, 1)
	go func() {
		_, err := manager.writeTerminalReply(id, running, []byte("reply"))
		replyDone <- err
	}()

	select {
	case <-process.secondWriteStarted:
		t.Fatal("terminal reply entered the PTY while input was still being written")
	case <-time.After(25 * time.Millisecond):
	}
	close(process.writeRelease)
	if err := <-inputDone; err != nil {
		t.Fatalf("send input: %v", err)
	}
	if err := <-replyDone; err != nil {
		t.Fatalf("write terminal reply: %v", err)
	}
}

func TestObservationWaitsForControlGate(t *testing.T) {
	target := actorTestAgent(t, agent.StateWorking, agent.HooksAuto)
	managed := newManagedAgent(target)
	st := &memoryCommitStore{}
	committer := newCommitter(0, st, event.NewHub(0))
	defer committer.Close()
	var control sync.Mutex
	observer, err := newManagedObservationActorWithControl(
		managed,
		committer,
		agent.HooksAuto,
		detect.Config{},
		systemObservationClock{},
		&control,
	)
	if err != nil {
		t.Fatalf("new observation actor: %v", err)
	}
	defer observer.Close()
	observation := actorHookObservation(
		t,
		"blocked-by-control-gate",
		detect.KindHumanInputRequired,
		"Elicitation",
	)

	control.Lock()
	delivered := make(chan error, 1)
	go func() {
		delivered <- observer.Deliver(context.Background(), observation)
	}()
	select {
	case err := <-delivered:
		t.Fatalf("observation completed while control gate was held: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	if rows := st.Rows(); len(rows) != 0 {
		t.Fatalf("rows committed while control gate was held: %+v", rows)
	}
	control.Unlock()

	if err := <-delivered; err != nil {
		t.Fatalf("deliver observation: %v", err)
	}
	if target.State() != agent.StateBlocked {
		t.Fatalf("state = %s, want %s", target.State(), agent.StateBlocked)
	}
}

type blockingControlProcess struct {
	mu                 sync.Mutex
	writeStarted       chan struct{}
	secondWriteStarted chan struct{}
	writeRelease       chan struct{}
	closeStarted       chan struct{}
	writeOnce          sync.Once
	secondWriteOnce    sync.Once
	closeOnce          sync.Once
	writeCount         int
}

func newBlockingControlProcess() *blockingControlProcess {
	return &blockingControlProcess{
		writeStarted:       make(chan struct{}),
		secondWriteStarted: make(chan struct{}),
		writeRelease:       make(chan struct{}),
		closeStarted:       make(chan struct{}),
	}
}

func (p *blockingControlProcess) Write(data []byte) (int, error) {
	p.mu.Lock()
	p.writeCount++
	count := p.writeCount
	p.mu.Unlock()
	if count == 1 {
		p.writeOnce.Do(func() {
			close(p.writeStarted)
		})
	} else if count == 2 {
		p.secondWriteOnce.Do(func() {
			close(p.secondWriteStarted)
		})
	}
	<-p.writeRelease
	return len(data), nil
}

func (p *blockingControlProcess) Close() error {
	p.closeOnce.Do(func() {
		close(p.closeStarted)
	})
	return nil
}

func (*blockingControlProcess) PID() int {
	return 1
}
