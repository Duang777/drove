package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/event"
)

const observationInboxSize = 64

var errObservationActorClosed = errors.New("session: observation actor closed")

type observationClock interface {
	Now() time.Time
	NewTimer(time.Duration) observationTimer
}

type observationTimer interface {
	C() <-chan time.Time
	Reset(time.Duration) bool
	Stop() bool
}

type systemObservationClock struct{}

func (systemObservationClock) Now() time.Time {
	return time.Now().UTC()
}

func (systemObservationClock) NewTimer(delay time.Duration) observationTimer {
	return &systemObservationTimer{timer: time.NewTimer(delay)}
}

type systemObservationTimer struct {
	timer *time.Timer
}

func (t *systemObservationTimer) C() <-chan time.Time {
	return t.timer.C
}

func (t *systemObservationTimer) Reset(delay time.Duration) bool {
	return t.timer.Reset(delay)
}

func (t *systemObservationTimer) Stop() bool {
	return t.timer.Stop()
}

type observationRequest struct {
	observation detect.Observation
	result      chan error
}

type observationActor struct {
	target    *agent.Agent
	detector  *detect.Detector
	state     *detect.State
	committer *committer
	clock     observationClock

	requests chan observationRequest
	stop     chan struct{}
	done     chan struct{}

	admissionMu sync.RWMutex
	closing     bool
	closeOnce   sync.Once

	active         chan struct{}
	requiredFailed chan struct{}
	activeOnce     sync.Once
	requiredOnce   sync.Once
}

func newObservationActor(
	target *agent.Agent,
	committer *committer,
	policy agent.HookPolicy,
	config detect.Config,
	clock observationClock,
) (*observationActor, error) {
	if target == nil || committer == nil {
		return nil, errors.New("session: observation actor requires Agent and committer")
	}
	detector, err := detect.New(config)
	if err != nil {
		return nil, fmt.Errorf("session: create Detector: %w", err)
	}
	if clock == nil {
		clock = systemObservationClock{}
	}
	state := detect.NewState(policy)
	actor := &observationActor{
		target:         target,
		detector:       detector,
		state:          &state,
		committer:      committer,
		clock:          clock,
		requests:       make(chan observationRequest, observationInboxSize),
		stop:           make(chan struct{}),
		done:           make(chan struct{}),
		active:         make(chan struct{}),
		requiredFailed: make(chan struct{}),
	}
	go actor.run()
	return actor, nil
}

func (a *observationActor) Deliver(
	ctx context.Context,
	observation detect.Observation,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	request := observationRequest{
		observation: observation,
		result:      make(chan error, 1),
	}

	a.admissionMu.RLock()
	if a.closing {
		a.admissionMu.RUnlock()
		return errObservationActorClosed
	}
	select {
	case a.requests <- request:
		a.admissionMu.RUnlock()
	case <-ctx.Done():
		a.admissionMu.RUnlock()
		return fmt.Errorf("session: admit observation: %w", ctx.Err())
	}

	return <-request.result
}

func (a *observationActor) Terminate(observation detect.Observation) error {
	a.admissionMu.Lock()
	if a.closing {
		a.admissionMu.Unlock()
		return errObservationActorClosed
	}
	a.closing = true
	a.admissionMu.Unlock()

	request := observationRequest{
		observation: observation,
		result:      make(chan error, 1),
	}
	select {
	case a.requests <- request:
	case <-a.done:
		return errObservationActorClosed
	}
	return <-request.result
}

func (a *observationActor) Active() <-chan struct{} {
	return a.active
}

func (a *observationActor) RequiredFailed() <-chan struct{} {
	return a.requiredFailed
}

func (a *observationActor) Snapshot() detect.Snapshot {
	return a.state.Snapshot()
}

func (a *observationActor) Close() {
	a.closeOnce.Do(func() {
		a.admissionMu.Lock()
		a.closing = true
		close(a.stop)
		a.admissionMu.Unlock()
		<-a.done
	})
}

func (a *observationActor) run() {
	defer close(a.done)
	var timer observationTimer
	var timerC <-chan time.Time
	var timerGeneration uint64

	for {
		select {
		case request := <-a.requests:
			err := a.handle(request.observation, &timer, &timerC, &timerGeneration)
			request.result <- err
		case firedAt := <-timerC:
			observation, err := detect.ObserveTimer(timerGeneration, firedAt)
			if err == nil {
				_ = a.handle(observation, &timer, &timerC, &timerGeneration)
			}
		case <-a.stop:
			for {
				select {
				case request := <-a.requests:
					err := a.handle(
						request.observation,
						&timer,
						&timerC,
						&timerGeneration,
					)
					request.result <- err
				default:
					if timer != nil {
						stopAndDrainTimer(timer)
					}
					return
				}
			}
		}
	}
}

func (a *observationActor) handle(
	observation detect.Observation,
	timer *observationTimer,
	timerC *<-chan time.Time,
	timerGeneration *uint64,
) error {
	decision, err := a.detector.Decide(
		a.state.Snapshot(),
		a.target.Snapshot(),
		observation,
	)
	if err != nil {
		return err
	}
	if decision.Duplicate() {
		return nil
	}
	signal, outcome, ok := decision.Signal()
	if !ok {
		return errors.New("session: Detector decision has no signal")
	}
	payload, err := encodeSignalAudit(signal, outcome)
	if err != nil {
		return err
	}
	_, err = a.committer.CommitDecision(
		context.Background(),
		a.target,
		a.state,
		decision,
		[]event.Draft{
			event.NewAgentSignalDraft(
				string(a.target.ID()),
				string(a.target.ID()),
				string(payload),
			),
		},
	)
	if err != nil {
		return err
	}
	a.applyTimer(decision.Timer(), timer, timerC, timerGeneration)
	a.notifyStatus()
	return nil
}

func (a *observationActor) applyTimer(
	plan detect.TimerPlan,
	timer *observationTimer,
	timerC *<-chan time.Time,
	timerGeneration *uint64,
) {
	switch plan.Action {
	case detect.TimerKeep:
		return
	case detect.TimerCancel:
		if *timer != nil {
			stopAndDrainTimer(*timer)
		}
		*timerC = nil
		*timerGeneration = plan.Generation
	case detect.TimerArm:
		delay := plan.Deadline.Sub(a.clock.Now())
		if delay < 0 {
			delay = 0
		}
		if *timer == nil {
			*timer = a.clock.NewTimer(delay)
		} else {
			stopAndDrainTimer(*timer)
			(*timer).Reset(delay)
		}
		*timerC = (*timer).C()
		*timerGeneration = plan.Generation
	}
}

func (a *observationActor) notifyStatus() {
	switch a.state.Snapshot().HookStatus() {
	case detect.HookActive:
		a.activeOnce.Do(func() {
			close(a.active)
		})
	case detect.HookRequiredFailed:
		a.requiredOnce.Do(func() {
			close(a.requiredFailed)
		})
	}
}

func stopAndDrainTimer(timer observationTimer) {
	if timer.Stop() {
		return
	}
	select {
	case <-timer.C():
	default:
	}
}

func encodeSignalAudit(
	signal detect.Signal,
	outcome detect.Outcome,
) ([]byte, error) {
	occurredAt := ""
	if !signal.OccurredAt.IsZero() {
		occurredAt = signal.OccurredAt.UTC().Format(time.RFC3339Nano)
	}
	payload := event.SignalPayloadV1{
		Version:         1,
		Source:          string(signal.Source),
		Kind:            string(signal.Kind),
		Vendor:          signal.Vendor,
		VendorEvent:     signal.VendorEvent,
		Scope:           string(signal.Scope),
		VendorSessionID: signal.VendorSessionID,
		VendorTurnID:    signal.VendorTurnID,
		Notification:    signal.Notification,
		Evidence:        signal.Evidence,
		Confidence:      signal.Confidence,
		OccurredAt:      occurredAt,
		ReceivedAt:      signal.ReceivedAt.UTC().Format(time.RFC3339Nano),
		DeliveryID:      signal.DeliveryID,
		Outcome:         string(outcome),
	}
	if signal.Process != nil {
		payload.ExitCode = signal.Process.ExitCode
		payload.ExitKind = string(signal.Process.ExitKind)
	}
	if err := payload.Validate(); err != nil {
		return nil, fmt.Errorf("session: validate signal audit: %w", err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("session: encode signal audit: %w", err)
	}
	return encoded, nil
}
