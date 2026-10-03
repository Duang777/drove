package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
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
)

const (
	// MaxSignalPayloadBytes bounds one raw vendor hook document.
	MaxSignalPayloadBytes = 1 << 20
	// SignalAgentIDEnv identifies the Drove session to a hook relay.
	SignalAgentIDEnv = "DROVE_AGENT_ID"
	// SignalURLEnv contains the per-session loopback callback URL.
	SignalURLEnv = "DROVE_SIGNAL_URL"
	// SignalTokenEnv contains the in-memory per-session callback credential.
	SignalTokenEnv = "DROVE_SIGNAL_TOKEN"

	signalTokenBytes    = 32
	signalAdmissionWait = 250 * time.Millisecond
)

var (
	// ErrHookUnauthorized indicates a missing or incorrect capability token.
	ErrHookUnauthorized = errors.New("session: hook unauthorized")
	// ErrHookDisabled indicates that this session has hook policy off.
	ErrHookDisabled = errors.New("session: hooks disabled")
	// ErrHookVendorMismatch indicates that the delivery names another vendor.
	ErrHookVendorMismatch = errors.New("session: hook vendor mismatch")
	// ErrHookDetached indicates that the target has no attached hook receiver.
	ErrHookDetached = errors.New("session: hook session detached")
	// ErrHookInvalid indicates malformed or unsupported vendor input.
	ErrHookInvalid = errors.New("session: invalid hook signal")
	// ErrHookUnsupported indicates that the selected adapter has no normalizer.
	ErrHookUnsupported = errors.New("session: hook unsupported")
	// ErrHookBackpressure indicates that the observation inbox remained full.
	ErrHookBackpressure = errors.New("session: hook backpressure")
	// ErrHookRequired indicates that required hook authority was not observed.
	ErrHookRequired = errors.New("session: required hook not observed")
	// ErrInvalidHookPolicy indicates an unsupported request policy.
	ErrInvalidHookPolicy = errors.New("session: invalid hook policy")
	// ErrEventCommitterUnavailable indicates that durable event writes stopped.
	ErrEventCommitterUnavailable = errors.New(
		"session: event committer unavailable",
	)
	// ErrSignalOriginUnavailable indicates that no callback origin was configured.
	ErrSignalOriginUnavailable = errors.New(
		"session: signal origin unavailable",
	)
)

// HookDelivery is the transport-neutral hook request accepted by Manager.
type HookDelivery struct {
	Token      string
	Vendor     string
	DeliveryID string
	Payload    json.RawMessage
}

// ManagerOption configures runtime-only session behavior.
type ManagerOption func(*Manager)

type signalTokenDigest [sha256.Size]byte

type signalCredentialSource func() (string, signalTokenDigest, error)

func generateSignalCredential() (string, signalTokenDigest, error) {
	raw := make([]byte, signalTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", signalTokenDigest{}, fmt.Errorf(
			"session: generate signal token: %w",
			err,
		)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	return token, signalTokenDigest(sha256.Sum256([]byte(token))), nil
}

func normalizeHookPolicy(
	requested agent.HookPolicy,
	supportsHooks bool,
) (agent.HookPolicy, error) {
	if requested == "" {
		if supportsHooks {
			return agent.HooksAuto, nil
		}
		return agent.HooksOff, nil
	}
	if !agent.ValidHookPolicy(requested) {
		return "", fmt.Errorf("%w: %q", ErrInvalidHookPolicy, requested)
	}
	if requested == agent.HooksRequired && !supportsHooks {
		return "", ErrHookUnsupported
	}
	return requested, nil
}

// ConfigureSignalOrigin sets the actual loopback origin used for new hooks.
func (m *Manager) ConfigureSignalOrigin(origin *url.URL) error {
	if origin == nil {
		return ErrSignalOriginUnavailable
	}
	if origin.Scheme != "http" ||
		origin.Host == "" ||
		origin.User != nil ||
		origin.Path != "" ||
		origin.RawPath != "" ||
		origin.RawQuery != "" ||
		origin.ForceQuery ||
		origin.Fragment != "" ||
		!isLoopbackHost(origin.Hostname()) {
		return fmt.Errorf(
			"%w: origin must be an absolute loopback HTTP origin",
			ErrSignalOriginUnavailable,
		)
	}
	copy := *origin
	copy.Path = ""
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrManagerClosed
	}
	if m.originConfigured {
		return fmt.Errorf("%w: origin is already configured", ErrSignalOriginUnavailable)
	}
	m.signalOrigin = strings.TrimRight(copy.String(), "/")
	m.originConfigured = true
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (m *Manager) prepareRuntime(
	a *agent.Agent,
	entry adapter.Entry,
) (*runningSession, []string, string, error) {
	policy := a.HookPolicy()
	if !agent.ValidHookPolicy(policy) {
		return nil, nil, "", fmt.Errorf("%w: %q", ErrInvalidHookPolicy, policy)
	}
	if policy == agent.HooksRequired && !entry.SupportsHooks() {
		return nil, nil, "", fmt.Errorf("%w: %q", ErrHookUnsupported, a.Vendor())
	}

	observer, err := newObservationActor(
		a,
		m.committer,
		policy,
		m.detectConfig,
		m.clock,
	)
	if err != nil {
		return nil, nil, "", err
	}
	running := &runningSession{
		observer:       observer,
		callbacksReady: make(chan struct{}),
		signalReady:    make(chan struct{}),
		vendor:         a.Vendor(),
	}
	if policy == agent.HooksOff || !entry.SupportsHooks() {
		running.output = newOutputProcessor(m, a.ID(), running, entry, "")
		return running, nil, "", nil
	}

	signalURL, err := m.signalURL(a.ID())
	if err != nil {
		observer.Close()
		return nil, nil, "", err
	}
	token, digest, err := m.newCredential()
	if err != nil {
		observer.Close()
		return nil, nil, "", err
	}
	running.signalDigest = digest
	running.hasSignalToken = true
	running.output = newOutputProcessor(m, a.ID(), running, entry, token)
	return running, []string{
		SignalAgentIDEnv + "=" + string(a.ID()),
		SignalURLEnv + "=" + signalURL,
		SignalTokenEnv + "=" + token,
	}, token, nil
}

func (m *Manager) signalURL(id agent.ID) (string, error) {
	m.mu.RLock()
	origin := m.signalOrigin
	configured := m.originConfigured
	m.mu.RUnlock()
	if !configured {
		return "", ErrSignalOriginUnavailable
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return "", fmt.Errorf("%w: parse configured origin: %v", ErrSignalOriginUnavailable, err)
	}
	parsed.Path = "/api/v1/agents/" + url.PathEscape(string(id)) + "/signal"
	return parsed.String(), nil
}

func (m *Manager) waitForRequiredHook(
	ctx context.Context,
	a *agent.Agent,
	running *runningSession,
) error {
	if a.HookPolicy() != agent.HooksRequired {
		return nil
	}
	select {
	case <-running.observer.Active():
		return nil
	case <-running.observer.RequiredFailed():
		return ErrHookRequired
	case <-ctx.Done():
		return fmt.Errorf("%w: %v", ErrHookRequired, ctx.Err())
	}
}

// DeliverHook authenticates, normalizes, and durably submits one hook delivery.
func (m *Manager) DeliverHook(
	ctx context.Context,
	id agent.ID,
	delivery HookDelivery,
) error {
	m.mu.RLock()
	closed := m.closed
	a, known := m.agents[id]
	running, attached := m.sessions[id]
	var signalDigest signalTokenDigest
	hasSignalToken := false
	if attached {
		signalDigest = running.signalDigest
		hasSignalToken = running.hasSignalToken
	}
	m.mu.RUnlock()
	if closed {
		return ErrManagerClosed
	}
	if !known {
		return fmt.Errorf("%w: %q", ErrUnknownAgent, id)
	}
	if !attached || running.observer == nil {
		return fmt.Errorf("%w: %q", ErrHookDetached, id)
	}
	if a.HookPolicy() == agent.HooksOff {
		return ErrHookDisabled
	}
	if !verifySignalToken(signalDigest, hasSignalToken, delivery.Token) {
		return ErrHookUnauthorized
	}
	if delivery.Vendor != running.vendor {
		return fmt.Errorf(
			"%w: got %q, want %q",
			ErrHookVendorMismatch,
			delivery.Vendor,
			running.vendor,
		)
	}
	entry, supported := m.reg.Lookup(delivery.Vendor)
	if !supported || !entry.SupportsHooks() {
		return fmt.Errorf("%w: %q", ErrHookUnsupported, delivery.Vendor)
	}

	select {
	case <-running.signalReady:
	case <-ctx.Done():
		return fmt.Errorf("%w: %v", ErrHookDetached, ctx.Err())
	}

	m.mu.RLock()
	current, stillAttached := m.sessions[id]
	closed = m.closed
	exitClaimed := running.exitClaimed
	hasToken := running.hasSignalToken
	m.mu.RUnlock()
	if closed {
		return ErrManagerClosed
	}
	if !stillAttached || current != running || exitClaimed || !hasToken {
		return fmt.Errorf("%w: %q", ErrHookDetached, id)
	}

	signal, err := entry.NormalizeHook(adapter.HookInput{
		DeliveryID: delivery.DeliveryID,
		ReceivedAt: m.clock.Now(),
		Payload:    append(json.RawMessage(nil), delivery.Payload...),
	})
	if err != nil {
		if errors.Is(err, adapter.ErrIgnoredHookPayload) {
			return nil
		}
		if errors.Is(err, adapter.ErrUnsupportedHook) {
			return fmt.Errorf("%w: %v", ErrHookUnsupported, err)
		}
		return fmt.Errorf("%w: %v", ErrHookInvalid, err)
	}
	observation, err := detect.ObserveSignal(signal)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrHookInvalid, err)
	}
	admissionCtx, cancel := context.WithTimeout(context.Background(), signalAdmissionWait)
	defer cancel()
	if err := running.observer.Deliver(admissionCtx, observation); err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return fmt.Errorf("%w: %v", ErrHookBackpressure, err)
		case errors.Is(err, errObservationActorClosed):
			return fmt.Errorf("%w: %q", ErrHookDetached, id)
		case errors.Is(err, errObservationCommit),
			errors.Is(err, errCommitterClosed),
			errors.Is(err, errCommitterFailed):
			return fmt.Errorf("%w: %v", ErrEventCommitterUnavailable, err)
		default:
			return err
		}
	}
	return nil
}

func verifySignalToken(
	expected signalTokenDigest,
	present bool,
	token string,
) bool {
	if !present || token == "" {
		return false
	}
	actual := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(expected[:], actual[:]) == 1
}
