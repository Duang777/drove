// Package detect serializes runtime state evidence for one attached session.
package detect

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Duang777/drove/internal/adapter"
	"github.com/Duang777/drove/internal/agent"
)

const (
	defaultConfidenceThreshold = 0.8
	defaultIdleDelay           = 250 * time.Millisecond
	defaultActivityWindow      = 2 * time.Second
	defaultActivityCount       = 2
	maxDeliveryIDs             = 4096
)

// Policy controls whether a session accepts hook and heuristic evidence.
type Policy string

const (
	// PolicyOff disables hook input and uses terminal heuristics.
	PolicyOff Policy = "off"
	// PolicyAuto uses hooks after the first valid delivery and otherwise falls back.
	PolicyAuto Policy = "auto"
	// PolicyRequired accepts only hook-driven state decisions.
	PolicyRequired Policy = "required"
)

var (
	// ErrClosed indicates that the session Detector has stopped.
	ErrClosed = errors.New("detect: detector closed")
	// ErrHooksDisabled indicates that the session policy rejects hook delivery.
	ErrHooksDisabled = errors.New("detect: hooks are disabled")
)

// ValidPolicy reports whether policy is supported.
func ValidPolicy(policy Policy) bool {
	return policy == PolicyOff || policy == PolicyAuto || policy == PolicyRequired
}

// Decision asks the session layer to persist a signal and optionally transition.
type Decision struct {
	Signal adapter.Signal
	Target agent.State
	Reason string
}

// Options configures one Detector actor.
type Options struct {
	Policy  Policy
	RunMode agent.RunMode
	State   func() agent.State
	Apply   func(context.Context, Decision) error

	IdleDelay      time.Duration
	ActivityWindow time.Duration
	ActivityCount  int
	Now            func() time.Time
	After          func(time.Duration) <-chan time.Time
}

type requestKind uint8

const (
	requestSignal requestKind = iota
	requestOutput
)

type request struct {
	kind   requestKind
	ctx    context.Context
	signal adapter.Signal
	at     time.Time
	result chan error
}

// Detector is the single state-signal merger for one attached session.
type Detector struct {
	requests chan request
	stop     chan struct{}
	done     chan struct{}
	active   chan struct{}

	closeOnce  sync.Once
	activeOnce sync.Once
}

// New creates and starts a Detector actor.
func New(options Options) (*Detector, error) {
	if !ValidPolicy(options.Policy) {
		return nil, fmt.Errorf("detect: invalid hook policy %q", options.Policy)
	}
	if !agent.ValidRunMode(options.RunMode) {
		return nil, fmt.Errorf("detect: invalid run mode %q", options.RunMode)
	}
	if options.State == nil {
		return nil, errors.New("detect: state callback is required")
	}
	if options.Apply == nil {
		return nil, errors.New("detect: apply callback is required")
	}
	if options.IdleDelay <= 0 {
		options.IdleDelay = defaultIdleDelay
	}
	if options.ActivityWindow <= 0 {
		options.ActivityWindow = defaultActivityWindow
	}
	if options.ActivityCount <= 0 {
		options.ActivityCount = defaultActivityCount
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	if options.After == nil {
		options.After = time.After
	}

	detector := &Detector{
		requests: make(chan request),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		active:   make(chan struct{}),
	}
	go detector.run(options)
	return detector, nil
}

// Submit synchronously processes one normalized signal.
func (d *Detector) Submit(ctx context.Context, signal adapter.Signal) error {
	return d.submit(request{
		kind:   requestSignal,
		ctx:    ctx,
		signal: signal,
		result: make(chan error, 1),
	})
}

// ObserveOutput records fallback activity without persisting another output event.
func (d *Detector) ObserveOutput(ctx context.Context, at time.Time) error {
	return d.submit(request{
		kind:   requestOutput,
		ctx:    ctx,
		at:     at,
		result: make(chan error, 1),
	})
}

func (d *Detector) submit(req request) error {
	if req.ctx == nil {
		req.ctx = context.Background()
	}
	select {
	case d.requests <- req:
	case <-req.ctx.Done():
		return fmt.Errorf("detect: submit: %w", req.ctx.Err())
	case <-d.done:
		return ErrClosed
	}

	select {
	case err := <-req.result:
		return err
	case <-req.ctx.Done():
		return fmt.Errorf("detect: wait for result: %w", req.ctx.Err())
	case <-d.done:
		return ErrClosed
	}
}

// Active is closed after the first valid hook signal is durably accepted.
func (d *Detector) Active() <-chan struct{} {
	return d.active
}

// Close stops the actor and waits for any in-flight callback.
func (d *Detector) Close() {
	d.closeOnce.Do(func() {
		close(d.stop)
		<-d.done
	})
}

type actorState struct {
	hookActive bool
	seen       map[string]struct{}
	seenOrder  []string

	idleCandidate adapter.Signal
	idleTimer     <-chan time.Time

	activityCount int
	lastActivity  time.Time
}

func (d *Detector) run(options Options) {
	defer close(d.done)

	state := actorState{seen: make(map[string]struct{})}
	for {
		select {
		case req := <-d.requests:
			var err error
			switch req.kind {
			case requestSignal:
				err = d.handleSignal(options, &state, req.ctx, req.signal)
			case requestOutput:
				err = d.handleOutput(options, &state, req.ctx, req.at)
			}
			req.result <- err
		case <-state.idleTimer:
			d.confirmIdle(options, &state)
		case <-d.stop:
			return
		}
	}
}

func (d *Detector) handleSignal(
	options Options,
	state *actorState,
	ctx context.Context,
	signal adapter.Signal,
) error {
	if signal.Source == adapter.SignalSourceHook {
		if options.Policy == PolicyOff {
			return ErrHooksDisabled
		}
		if _, duplicate := state.seen[signal.DeliveryID]; duplicate {
			return nil
		}
	}

	if signal.Source == adapter.SignalSourceHook &&
		(signal.Kind == adapter.SignalWorking ||
			signal.Kind == adapter.SignalBlocked ||
			signal.Kind == adapter.SignalDone) {
		cancelIdle(state)
	}

	if signal.Kind == adapter.SignalIdle &&
		signal.Scope == adapter.SignalScopeRoot &&
		signal.Source == adapter.SignalSourceHook {
		if err := options.Apply(ctx, Decision{Signal: signal}); err != nil {
			return err
		}
		d.acceptHook(state, signal)
		d.markActive()
		state.idleCandidate = signal
		state.idleTimer = options.After(options.IdleDelay)
		return nil
	}

	target := d.target(options, state, signal)
	decision := Decision{
		Signal: signal,
		Target: target,
		Reason: decisionReason(signal),
	}
	if err := options.Apply(ctx, decision); err != nil {
		return err
	}
	if signal.Source == adapter.SignalSourceHook {
		d.acceptHook(state, signal)
		d.markActive()
	}
	return nil
}

func (d *Detector) target(
	options Options,
	state *actorState,
	signal adapter.Signal,
) agent.State {
	if signal.Source == adapter.SignalSourceHeuristic {
		if options.Policy == PolicyRequired ||
			(options.Policy == PolicyAuto && state.hookActive) ||
			signal.Confidence < defaultConfidenceThreshold {
			return ""
		}
	}
	if signal.Scope == adapter.SignalScopeSubagent {
		return ""
	}

	switch signal.Kind {
	case adapter.SignalWorking:
		return agent.StateWorking
	case adapter.SignalBlocked:
		return agent.StateBlocked
	case adapter.SignalDone:
		if options.RunMode == agent.RunModeOneshot {
			return agent.StateDone
		}
	}
	return ""
}

func (d *Detector) handleOutput(
	options Options,
	state *actorState,
	ctx context.Context,
	at time.Time,
) error {
	if options.Policy == PolicyRequired ||
		(options.Policy == PolicyAuto && state.hookActive) {
		return nil
	}
	cancelIdle(state)
	if options.State() != agent.StateBlocked {
		state.activityCount = 0
		state.lastActivity = time.Time{}
		return nil
	}
	if at.IsZero() {
		at = options.Now()
	}
	if state.lastActivity.IsZero() || at.Sub(state.lastActivity) > options.ActivityWindow {
		state.activityCount = 1
	} else {
		state.activityCount++
	}
	state.lastActivity = at
	if state.activityCount < options.ActivityCount {
		return nil
	}

	signal := adapter.Signal{
		Source:      adapter.SignalSourceHeuristic,
		Kind:        adapter.SignalWorking,
		VendorEvent: "output_activity",
		Scope:       adapter.SignalScopeRoot,
		Evidence:    "sustained_output",
		Confidence:  defaultConfidenceThreshold,
		ReceivedAt:  at,
	}
	err := options.Apply(ctx, Decision{
		Signal: signal,
		Target: agent.StateWorking,
		Reason: decisionReason(signal),
	})
	if err == nil {
		state.activityCount = 0
		state.lastActivity = time.Time{}
	}
	return err
}

func (d *Detector) confirmIdle(options Options, state *actorState) {
	candidate := state.idleCandidate
	cancelIdle(state)
	if candidate.VendorEvent == "" {
		return
	}

	signal := adapter.Signal{
		Source:      adapter.SignalSourceTimer,
		Kind:        adapter.SignalIdle,
		Vendor:      candidate.Vendor,
		VendorEvent: "idle_confirmed",
		Scope:       adapter.SignalScopeRoot,
		Evidence:    "idle_confirmation",
		Confidence:  1,
		ReceivedAt:  options.Now(),
	}
	_ = options.Apply(context.Background(), Decision{
		Signal: signal,
		Target: agent.StateIdle,
		Reason: decisionReason(signal),
	})
}

func (d *Detector) acceptHook(state *actorState, signal adapter.Signal) {
	state.hookActive = true
	state.seen[signal.DeliveryID] = struct{}{}
	state.seenOrder = append(state.seenOrder, signal.DeliveryID)
	if len(state.seenOrder) <= maxDeliveryIDs {
		return
	}
	oldest := state.seenOrder[0]
	state.seenOrder = state.seenOrder[1:]
	delete(state.seen, oldest)
}

func (d *Detector) markActive() {
	d.activeOnce.Do(func() {
		close(d.active)
	})
}

func cancelIdle(state *actorState) {
	state.idleCandidate = adapter.Signal{}
	state.idleTimer = nil
}

func decisionReason(signal adapter.Signal) string {
	switch signal.Source {
	case adapter.SignalSourceHook:
		return fmt.Sprintf("hook %s", signal.VendorEvent)
	case adapter.SignalSourceTimer:
		return "hook idle confirmed"
	default:
		return signal.Evidence
	}
}
