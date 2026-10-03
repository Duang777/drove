package session

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/Duang777/drove/internal/adapter"
	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
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

	signalTokenBytes = 32
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
func WithHookPolicy(policy agent.HookPolicy) ManagerOption {
	return func(manager *Manager) {
		manager.hookPolicy = policy
	}
}

func (m *Manager) prepareRuntime(
	a *agent.Agent,
	entry adapter.Entry,
) (*runningSession, []string, error) {
	policy := a.HookPolicy()
	if !agent.ValidHookPolicy(policy) {
		return nil, nil, fmt.Errorf("session: invalid hook policy %q", policy)
	}
	canProvision := entry.SupportsHooks() &&
		policy != agent.HooksOff &&
		m.signalBaseURL != ""
	if policy == agent.HooksRequired && !canProvision {
		return nil, nil, fmt.Errorf(
			"%w: vendor=%q callback=%t",
			ErrHookUnavailable,
			a.Vendor(),
			m.signalBaseURL != "",
		)
	}

	observer, err := newObservationActor(
		a,
		m.committer,
		policy,
		m.detectConfig,
		m.clock,
	)
	if err != nil {
		return nil, nil, err
	}
	running := &runningSession{
		observer:       observer,
		callbacksReady: make(chan struct{}),
		signalReady:    make(chan struct{}),
		vendor:         a.Vendor(),
	}
	if !canProvision {
		return running, nil, nil
	}

	token, err := newSignalToken()
	if err != nil {
		observer.Close()
		return nil, nil, err
	}
	signalURL, err := m.signalURL(a.ID())
	if err != nil {
		observer.Close()
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
		return "", fmt.Errorf(
			"session: invalid loopback signal base URL %q",
			m.signalBaseURL,
		)
	}
	host := parsed.Hostname()
	if !strings.EqualFold(host, "localhost") {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return "", fmt.Errorf(
				"session: signal base URL host %q must be loopback",
				host,
			)
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

func (m *Manager) waitForRequiredHook(
	ctx context.Context,
	running *runningSession,
) error {
	if m.hookPolicy != agent.HooksRequired {
		return nil
	}
	select {
	case <-running.observer.Active():
		return nil
	case <-running.observer.RequiredFailed():
		return fmt.Errorf("%w: no valid signal arrived", ErrHookUnavailable)
	case <-ctx.Done():
		return fmt.Errorf("%w: %v", ErrHookUnavailable, ctx.Err())
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
	a, known := m.agents[id]
	running, attached := m.sessions[id]
	m.mu.RUnlock()
	if closed {
		return ErrManagerClosed
	}
	if !known {
		return fmt.Errorf("%w: %q", ErrUnknownAgent, id)
	}
	if !attached || running.observer == nil {
		return fmt.Errorf("%w: %q", ErrNotAttached, id)
	}
	if a.HookPolicy() == agent.HooksOff {
		return ErrSignalDisabled
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
	entry, supported := m.reg.Lookup(vendor)
	if !supported || !entry.SupportsHooks() {
		return fmt.Errorf("%w: %q", adapter.ErrUnsupportedHook, vendor)
	}

	select {
	case <-running.signalReady:
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

	signal, err := entry.NormalizeHook(adapter.HookInput{
		DeliveryID: deliveryID,
		ReceivedAt: m.clock.Now(),
		Payload:    append([]byte(nil), raw...),
	})
	if err != nil {
		return fmt.Errorf(
			"%w: normalize %s hook: %v",
			ErrSignalInvalid,
			running.vendor,
			err,
		)
	}
	observation, err := detect.ObserveSignal(signal)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSignalInvalid, err)
	}
	if err := running.observer.Deliver(ctx, observation); err != nil {
		if errors.Is(err, detect.ErrHooksDisabled) {
			return ErrSignalDisabled
		}
		if errors.Is(err, errObservationActorClosed) {
			return fmt.Errorf("%w: %q", ErrNotAttached, id)
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
