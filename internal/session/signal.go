package session

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/Duang777/drove/internal/adapter"
	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/event"
)

const (
	// MaxSignalPayloadBytes bounds one raw vendor hook document.
	MaxSignalPayloadBytes = 256 * 1024
	// SignalAgentIDEnv identifies the Drove session to a hook relay.
	SignalAgentIDEnv = "DROVE_AGENT_ID"
	// SignalURLEnv contains the per-session loopback callback URL.
	SignalURLEnv = "DROVE_SIGNAL_URL"
	// SignalTokenEnv contains the in-memory per-session callback credential.
	SignalTokenEnv = "DROVE_SIGNAL_TOKEN"
	// SignalVendorEnv contains the selected adapter vendor.
	SignalVendorEnv = "DROVE_SIGNAL_VENDOR"

	signalTokenBytes             = 32
	defaultHookActivationTimeout = 2 * time.Second
)

var (
	// ErrHookUnavailable indicates that required hook signaling cannot activate.
	ErrHookUnavailable = errors.New("session: required hook signaling is unavailable")
	// ErrSignalUnauthorized indicates a missing or invalid per-session credential.
	ErrSignalUnauthorized = errors.New("session: signal credential rejected")
	// ErrSignalVendorMismatch indicates that a relay named the wrong vendor.
	ErrSignalVendorMismatch = errors.New("session: signal vendor does not match agent")
	// ErrSignalDisabled indicates that the current session rejects hook signals.
	ErrSignalDisabled = errors.New("session: hook signals are disabled")
	// ErrSignalInvalid indicates that the adapter rejected a vendor payload.
	ErrSignalInvalid = errors.New("session: invalid hook signal")
)

// ManagerOption configures runtime-only session behavior.
type ManagerOption func(*Manager)

// WithSignalBaseURL enables per-session hook callback environment injection.
func WithSignalBaseURL(baseURL string) ManagerOption {
	return func(manager *Manager) {
		manager.signalBaseURL = strings.TrimRight(baseURL, "/")
	}
}

// WithHookPolicy configures hook authority for newly started sessions.
func WithHookPolicy(policy detect.Policy) ManagerOption {
	return func(manager *Manager) {
		manager.hookPolicy = policy
	}
}

type signalAuditPayload struct {
	Version      int                  `json:"version"`
	Source       adapter.SignalSource `json:"source"`
	Vendor       string               `json:"vendor,omitempty"`
	VendorEvent  string               `json:"vendor_event"`
	Scope        adapter.SignalScope  `json:"scope"`
	SessionRef   string               `json:"vendor_session_id,omitempty"`
	TurnRef      string               `json:"vendor_turn_id,omitempty"`
	Notification string               `json:"notification_type,omitempty"`
	Evidence     string               `json:"evidence"`
	Confidence   float64              `json:"confidence"`
	DeliveryID   string               `json:"delivery_id,omitempty"`
	OccurredAt   string               `json:"occurred_at,omitempty"`
	ReceivedAt   string               `json:"received_at"`
}

func (m *Manager) prepareRuntime(
	a *agent.Agent,
	entry adapter.Entry,
) (*runningSession, []string, error) {
	if !detect.ValidPolicy(m.hookPolicy) {
		return nil, nil, fmt.Errorf("session: invalid hook policy %q", m.hookPolicy)
	}
	canProvision := entry.SupportsHooks() &&
		m.hookPolicy != detect.PolicyOff &&
		m.signalBaseURL != ""
	if m.hookPolicy == detect.PolicyRequired && !canProvision {
		return nil, nil, fmt.Errorf(
			"%w: vendor=%q callback=%t",
			ErrHookUnavailable,
			a.Vendor(),
			m.signalBaseURL != "",
		)
	}

	running := &runningSession{
		ready:  make(chan struct{}),
		vendor: a.Vendor(),
	}
	detector, err := detect.New(detect.Options{
		Policy:  m.hookPolicy,
		RunMode: a.RunMode(),
		State:   a.State,
		Apply: func(ctx context.Context, decision detect.Decision) error {
			return m.commitDetection(ctx, a, decision)
		},
		IdleDelay:      m.detectorIdleDelay,
		ActivityWindow: m.detectorActivityWindow,
		ActivityCount:  m.detectorActivityCount,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("session: create detector: %w", err)
	}
	running.detector = detector
	if !canProvision {
		return running, nil, nil
	}

	token, err := newSignalToken()
	if err != nil {
		detector.Close()
		return nil, nil, err
	}
	signalURL, err := m.signalURL(a.ID())
	if err != nil {
		detector.Close()
		return nil, nil, err
	}
	running.signalToken = token
	return running, []string{
		SignalAgentIDEnv + "=" + string(a.ID()),
		SignalURLEnv + "=" + signalURL,
		SignalTokenEnv + "=" + token,
		SignalVendorEnv + "=" + a.Vendor(),
	}, nil
}

func (m *Manager) signalURL(id agent.ID) (string, error) {
	parsed, err := url.Parse(m.signalBaseURL)
	if err != nil {
		return "", fmt.Errorf("session: parse signal base URL: %w", err)
	}
	if parsed.Scheme != "http" || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("session: invalid loopback signal base URL %q", m.signalBaseURL)
	}
	host := parsed.Hostname()
	if !strings.EqualFold(host, "localhost") {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return "", fmt.Errorf("session: signal base URL host %q must be loopback", host)
		}
	}
	parsed.Path = "/api/v1/agents/" + url.PathEscape(string(id)) + "/signal"
	return parsed.String(), nil
}

func newSignalToken() (string, error) {
	raw := make([]byte, signalTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("session: generate signal token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func (m *Manager) waitForRequiredHook(ctx context.Context, running *runningSession) error {
	if m.hookPolicy != detect.PolicyRequired {
		return nil
	}
	timer := time.NewTimer(m.hookActivationTimeout)
	defer timer.Stop()
	select {
	case <-running.detector.Active():
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w: %v", ErrHookUnavailable, ctx.Err())
	case <-timer.C:
		return fmt.Errorf(
			"%w: no valid signal arrived within %s",
			ErrHookUnavailable,
			m.hookActivationTimeout,
		)
	}
}

// AcceptSignal authenticates, normalizes, and submits one hook delivery.
func (m *Manager) AcceptSignal(
	ctx context.Context,
	id agent.ID,
	authorization string,
	vendor string,
	deliveryID string,
	raw []byte,
) error {
	m.mu.RLock()
	closed := m.closed
	_, known := m.agents[id]
	running, attached := m.sessions[id]
	m.mu.RUnlock()
	if closed {
		return ErrManagerClosed
	}
	if !known {
		return fmt.Errorf("%w: %q", ErrUnknownAgent, id)
	}
	if !attached || running.detector == nil {
		return fmt.Errorf("%w: %q", ErrNotAttached, id)
	}
	if !verifySignalAuthorization(running.signalToken, authorization) {
		return ErrSignalUnauthorized
	}
	if vendor != running.vendor {
		return fmt.Errorf(
			"%w: got %q, want %q",
			ErrSignalVendorMismatch,
			vendor,
			running.vendor,
		)
	}

	select {
	case <-running.ready:
	case <-ctx.Done():
		return fmt.Errorf("session: wait for signal-ready session: %w", ctx.Err())
	}

	m.mu.RLock()
	current, stillAttached := m.sessions[id]
	closed = m.closed
	exitClaimed := running.exitClaimed
	m.mu.RUnlock()
	if closed {
		return ErrManagerClosed
	}
	if !stillAttached || current != running || exitClaimed {
		return fmt.Errorf("%w: %q", ErrNotAttached, id)
	}

	signal, err := m.reg.For(running.vendor).DecodeHook(raw, deliveryID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("%w: decode %s hook: %v", ErrSignalInvalid, running.vendor, err)
	}
	if err := running.detector.Submit(ctx, signal); err != nil {
		if errors.Is(err, detect.ErrHooksDisabled) {
			return ErrSignalDisabled
		}
		return fmt.Errorf("session: process hook signal: %w", err)
	}
	return nil
}

func verifySignalAuthorization(token, authorization string) bool {
	if token == "" {
		return false
	}
	presented, ok := strings.CutPrefix(authorization, "Bearer ")
	if !ok || len(presented) != len(token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(presented)) == 1
}

func (m *Manager) commitDetection(
	ctx context.Context,
	a *agent.Agent,
	decision detect.Decision,
) error {
	payload, err := encodeSignalAudit(decision.Signal)
	if err != nil {
		return err
	}
	signalDraft := event.NewAgentSignalDraft(
		string(a.ID()),
		string(a.ID()),
		string(payload),
	)
	if decision.Target == "" ||
		decision.Target == a.State() ||
		!agent.CanTransition(a.State(), decision.Target) {
		_, err := m.committer.CommitEvents(ctx, []event.Draft{signalDraft})
		return err
	}

	evidence := agent.Evidence{
		Source:     agent.EvidenceSource(decision.Signal.Source),
		Event:      decision.Signal.VendorEvent,
		Confidence: decision.Signal.Confidence,
		DeliveryID: decision.Signal.DeliveryID,
	}
	_, err = m.committer.CommitAgent(
		ctx,
		a,
		agent.MoveTo(decision.Target, decision.Reason, evidence),
		[]event.Draft{signalDraft},
	)
	return err
}

func encodeSignalAudit(signal adapter.Signal) ([]byte, error) {
	occurredAt := ""
	if !signal.OccurredAt.IsZero() {
		occurredAt = signal.OccurredAt.UTC().Format(time.RFC3339Nano)
	}
	receivedAt := signal.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	}
	payload, err := json.Marshal(signalAuditPayload{
		Version:      1,
		Source:       signal.Source,
		Vendor:       signal.Vendor,
		VendorEvent:  signal.VendorEvent,
		Scope:        signal.Scope,
		SessionRef:   signal.SessionRef,
		TurnRef:      signal.TurnRef,
		Notification: signal.Notification,
		Evidence:     signal.Evidence,
		Confidence:   signal.Confidence,
		DeliveryID:   signal.DeliveryID,
		OccurredAt:   occurredAt,
		ReceivedAt:   receivedAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return nil, fmt.Errorf("session: encode signal audit: %w", err)
	}
	return payload, nil
}
