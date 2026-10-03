package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/event"
)

func TestObservationActorCommitsDecisionAndDeduplicatesDelivery(t *testing.T) {
	target := actorTestAgent(t, agent.StateWorking, agent.HooksAuto)
	st := &memoryCommitStore{}
	hub := event.NewHub(0)
	committer := newCommitter(0, st, hub)
	defer committer.Close()
	actor, err := newObservationActor(
		target,
		committer,
		agent.HooksAuto,
		detect.Config{},
		systemObservationClock{},
	)
	if err != nil {
		t.Fatalf("new observation actor: %v", err)
	}
	defer actor.Close()

	observation := actorHookObservation(
		t,
		"block",
		detect.KindHumanInputRequired,
		"Elicitation",
	)
	if err := actor.Deliver(context.Background(), observation); err != nil {
		t.Fatalf("deliver hook: %v", err)
	}
	rowsBeforeDuplicate := st.Rows()
	if target.State() != agent.StateBlocked ||
		actor.Snapshot().HookStatus() != detect.HookActive ||
		len(rowsBeforeDuplicate) != 2 ||
		rowsBeforeDuplicate[0].Type != string(event.TypeAgentSignal) ||
		rowsBeforeDuplicate[1].Type != string(event.TypeStateChanged) {
		t.Fatalf(
			"Agent=%s Detector=%s rows=%+v",
			target.State(),
			actor.Snapshot().HookStatus(),
			rowsBeforeDuplicate,
		)
	}

	if err := actor.Deliver(context.Background(), observation); err != nil {
		t.Fatalf("deliver duplicate: %v", err)
	}
	if rows := st.Rows(); len(rows) != len(rowsBeforeDuplicate) {
		t.Fatalf("duplicate added events: before=%d after=%d", len(rowsBeforeDuplicate), len(rows))
	}
}

func TestObservationActorRunsCommittedTimerPlan(t *testing.T) {
	target := actorTestAgent(t, agent.StateStarting, agent.HooksAuto)
	st := &memoryCommitStore{}
	committer := newCommitter(0, st, event.NewHub(0))
	defer committer.Close()
	clock := newActorFakeClock(time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC))
	actor, err := newObservationActor(
		target,
		committer,
		agent.HooksAuto,
		detect.Config{},
		clock,
	)
	if err != nil {
		t.Fatalf("new observation actor: %v", err)
	}
	defer actor.Close()

	started, err := processObservation(
		detect.KindProcessStarted,
		clock.Now(),
		&detect.ProcessFact{HookAvailable: true},
	)
	if err != nil {
		t.Fatalf("process observation: %v", err)
	}
	if err := actor.Deliver(context.Background(), started); err != nil {
		t.Fatalf("deliver process start: %v", err)
	}
	timer := clock.lastTimer(t)
	if timer.delay != 5*time.Second {
		t.Fatalf("activation delay = %s, want 5s", timer.delay)
	}

	clock.advance(5 * time.Second)
	timer.fire(clock.Now())
	deadline := time.Now().Add(time.Second)
	for actor.Snapshot().HookStatus() != detect.HookFallback {
		if time.Now().After(deadline) {
			t.Fatalf("hook status = %s, want fallback", actor.Snapshot().HookStatus())
		}
		time.Sleep(time.Millisecond)
	}

	rows := st.Rows()
	if len(rows) != 3 ||
		rows[0].Type != string(event.TypeAgentSignal) ||
		rows[1].Type != string(event.TypeStateChanged) ||
		rows[2].Type != string(event.TypeAgentSignal) {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestObservationActorRejectsAfterTerminalAdmissionClose(t *testing.T) {
	target := actorTestAgent(t, agent.StateWorking, agent.HooksOff)
	committer := newCommitter(0, &memoryCommitStore{}, event.NewHub(0))
	defer committer.Close()
	actor, err := newObservationActor(
		target,
		committer,
		agent.HooksOff,
		detect.Config{},
		systemObservationClock{},
	)
	if err != nil {
		t.Fatalf("new observation actor: %v", err)
	}
	defer actor.Close()

	code := 0
	exited, err := processObservation(
		detect.KindProcessExited,
		time.Now().UTC(),
		&detect.ProcessFact{ExitCode: &code, ExitKind: detect.ExitSuccess},
	)
	if err != nil {
		t.Fatalf("exit observation: %v", err)
	}
	if err := actor.Terminate(exited); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	output, err := detect.ObserveOutput("claude", time.Now().UTC())
	if err != nil {
		t.Fatalf("output observation: %v", err)
	}
	if err := actor.Deliver(context.Background(), output); !errors.Is(
		err,
		errObservationActorClosed,
	) {
		t.Fatalf("delivery error = %v, want closed actor", err)
	}
}

func TestObservationActorInboxHasFixedCapacity(t *testing.T) {
	target := actorTestAgent(t, agent.StateWorking, agent.HooksOff)
	committer := newCommitter(0, &memoryCommitStore{}, event.NewHub(0))
	defer committer.Close()
	actor, err := newObservationActor(
		target,
		committer,
		agent.HooksOff,
		detect.Config{},
		systemObservationClock{},
	)
	if err != nil {
		t.Fatalf("new observation actor: %v", err)
	}
	defer actor.Close()
	if cap(actor.requests) != 64 {
		t.Fatalf("observation inbox capacity = %d, want 64", cap(actor.requests))
	}
}

func actorTestAgent(
	t *testing.T,
	state agent.State,
	policy agent.HookPolicy,
) *agent.Agent {
	t.Helper()
	now := time.Now().UTC()
	target, err := agent.Restore(agent.RestoreSnapshot{
		ID:         "actor-agent",
		Name:       "actor",
		Vendor:     "claude",
		RunMode:    agent.RunModeInteractive,
		HookPolicy: policy,
		State:      state,
		CreatedAt:  now.Add(-time.Minute),
		UpdatedAt:  now,
	})
	if err != nil {
		t.Fatalf("restore Agent: %v", err)
	}
	return target
}

func actorHookObservation(
	t *testing.T,
	label string,
	kind detect.Kind,
	vendorEvent string,
) detect.Observation {
	t.Helper()
	signal, err := detect.NewHookSignal(detect.Signal{
		Kind:            kind,
		Vendor:          "claude",
		VendorEvent:     vendorEvent,
		Scope:           detect.ScopeRoot,
		VendorSessionID: "vendor-session",
		Confidence:      1,
		ReceivedAt:      time.Now().UTC(),
		DeliveryID: uuid.NewSHA1(
			uuid.NameSpaceOID,
			[]byte(label),
		).String(),
	})
	if err != nil {
		t.Fatalf("new hook signal: %v", err)
	}
	observation, err := detect.ObserveSignal(signal)
	if err != nil {
		t.Fatalf("observe hook: %v", err)
	}
	return observation
}

type actorFakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*actorFakeTimer
}

func newActorFakeClock(now time.Time) *actorFakeClock {
	return &actorFakeClock{now: now}
}

func (c *actorFakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *actorFakeClock) NewTimer(delay time.Duration) observationTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &actorFakeTimer{
		channel: make(chan time.Time, 1),
		active:  true,
		delay:   delay,
	}
	c.timers = append(c.timers, timer)
	return timer
}

func (c *actorFakeClock) advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	c.mu.Unlock()
}

func (c *actorFakeClock) lastTimer(t *testing.T) *actorFakeTimer {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.timers) == 0 {
		t.Fatal("no timer was created")
	}
	return c.timers[len(c.timers)-1]
}

type actorFakeTimer struct {
	mu      sync.Mutex
	channel chan time.Time
	active  bool
	delay   time.Duration
}

func (t *actorFakeTimer) C() <-chan time.Time {
	return t.channel
}

func (t *actorFakeTimer) Reset(delay time.Duration) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	wasActive := t.active
	t.active = true
	t.delay = delay
	return wasActive
}

func (t *actorFakeTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	wasActive := t.active
	t.active = false
	return wasActive
}

func (t *actorFakeTimer) fire(at time.Time) {
	t.mu.Lock()
	if !t.active {
		t.mu.Unlock()
		return
	}
	t.active = false
	t.mu.Unlock()
	t.channel <- at
}
