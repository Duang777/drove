// Package notify provides durable notification planning and delivery.
package notify

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ChannelKind identifies a notification transport.
type ChannelKind string

const (
	// ChannelWebPush delivers through a browser PushSubscription.
	ChannelWebPush ChannelKind = "web_push"
	// ChannelNtfy delivers to one configured ntfy topic.
	ChannelNtfy ChannelKind = "ntfy"
)

// Notification is the complete metadata allowed to leave Drove by default.
type Notification struct {
	ID         string    `json:"id"`
	AgentID    string    `json:"agent_id"`
	AgentName  string    `json:"agent_name"`
	Vendor     string    `json:"vendor"`
	State      string    `json:"state"`
	BlockedSeq uint64    `json:"blocked_seq"`
	OccurredAt time.Time `json:"occurred_at"`
	DeepLink   string    `json:"deep_link"`
}

// AgentMetadata contains stable labels used in a notification.
type AgentMetadata struct {
	Name   string
	Vendor string
}

// MetadataResolver returns labels for one Agent without exposing its output.
type MetadataResolver interface {
	ResolveNotificationAgent(context.Context, string) (AgentMetadata, error)
}

// MetadataResolverFunc adapts a function to MetadataResolver.
type MetadataResolverFunc func(context.Context, string) (AgentMetadata, error)

// ResolveNotificationAgent implements MetadataResolver.
func (f MetadataResolverFunc) ResolveNotificationAgent(
	ctx context.Context,
	agentID string,
) (AgentMetadata, error) {
	return f(ctx, agentID)
}

// Policy controls Blocked notification suppression.
type Policy struct {
	Debounce        time.Duration
	QuietWhenActive bool
	PresenceTTL     time.Duration
}

func (p Policy) validate() error {
	if p.Debounce < 0 {
		return errors.New("notify: debounce cannot be negative")
	}
	if p.PresenceTTL <= 0 {
		return errors.New("notify: presence TTL must be positive")
	}
	return nil
}

// PushSubscription is one durable browser push target.
type PushSubscription struct {
	ID         string
	Endpoint   string
	P256DH     string
	Auth       string
	DeviceName string
	CreatedAt  time.Time
	RevokedAt  *time.Time
}

func (s PushSubscription) validate() error {
	if s.ID == "" {
		return errors.New("notify: subscription ID is required")
	}
	if s.Endpoint == "" {
		return errors.New("notify: subscription endpoint is required")
	}
	if s.P256DH == "" || s.Auth == "" {
		return errors.New("notify: subscription keys are required")
	}
	if s.DeviceName == "" {
		return errors.New("notify: subscription device name is required")
	}
	if s.CreatedAt.IsZero() {
		return errors.New("notify: subscription creation time is required")
	}
	return nil
}

// Delivery is one claimed outbox item.
type Delivery struct {
	ID           string
	SourceSeq    uint64
	AgentID      string
	Channel      ChannelKind
	TargetID     string
	Notification Notification
	Attempts     int
	Subscription *PushSubscription
}

// SendOutcome tells the worker how to finish a delivery attempt.
type SendOutcome string

const (
	// SendDelivered marks a provider-accepted delivery complete.
	SendDelivered SendOutcome = "delivered"
	// SendRetry returns a delivery to the pending queue.
	SendRetry SendOutcome = "retry"
	// SendPermanent marks a delivery as failed without another attempt.
	SendPermanent SendOutcome = "permanent"
	// SendRevokeTarget revokes the target and cancels its pending deliveries.
	SendRevokeTarget SendOutcome = "revoke_target"
)

// SendResult classifies one provider response.
type SendResult struct {
	Outcome    SendOutcome
	RetryAfter time.Duration
	Code       string
}

func (r SendResult) validate() error {
	switch r.Outcome {
	case SendDelivered, SendRetry, SendPermanent, SendRevokeTarget:
	default:
		return fmt.Errorf("notify: invalid send outcome %q", r.Outcome)
	}
	if r.RetryAfter < 0 {
		return errors.New("notify: retry delay cannot be negative")
	}
	if len(r.Code) > 64 {
		return errors.New("notify: send result code exceeds 64 bytes")
	}
	return nil
}

// Channel sends claimed deliveries to one transport.
type Channel interface {
	Kind() ChannelKind
	Send(context.Context, Delivery) (SendResult, error)
}

type deliveryTarget struct {
	channel ChannelKind
	id      string
}

func validChannelKind(kind ChannelKind) bool {
	switch kind {
	case ChannelWebPush, ChannelNtfy:
		return true
	default:
		return false
	}
}
