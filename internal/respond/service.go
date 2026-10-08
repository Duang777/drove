// Package respond coordinates one-time action tickets with live sessions.
package respond

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/notify"
	"github.com/Duang777/drove/internal/session"
)

// SessionActions is the session behavior required by remote responses.
type SessionActions interface {
	ActionContext(
		context.Context,
		agent.ID,
		session.StateSeq,
	) (session.ActionContext, error)
	Respond(
		context.Context,
		agent.ID,
		session.ActionRequest,
	) (session.ActionResult, error)
}

// TicketActions is the notification behavior required by remote responses.
type TicketActions interface {
	IssueActionTickets(
		context.Context,
		notify.ActionTicketRequest,
	) (notify.IssuedActionTickets, error)
	ConsumeActionTicket(
		context.Context,
		agent.ID,
		string,
	) (notify.ActionTicketClaims, error)
}

// ServiceOptions configures the cross-domain response service.
type ServiceOptions struct {
	Sessions SessionActions
	Tickets  TicketActions
}

// Service preserves ticket and PTY operation ordering.
type Service struct {
	sessions SessionActions
	tickets  TicketActions
}

// Context contains a live screen plus fresh action-bound tickets.
type Context struct {
	StateSeq  session.StateSeq            `json:"state_seq"`
	Actions   []agent.ActionKind          `json:"actions"`
	Tickets   []notify.IssuedActionTicket `json:"tickets"`
	ExpiresAt time.Time                   `json:"expires_at"`
	Screen    *session.ExplainScreen      `json:"screen,omitempty"`
}

// Result is the session result for one executed remote action.
type Result = session.ActionResult

// NewService validates dependencies and constructs a response service.
func NewService(options ServiceOptions) (*Service, error) {
	if options.Sessions == nil {
		return nil, errors.New("respond: session actions are required")
	}
	if options.Tickets == nil {
		return nil, errors.New("respond: ticket actions are required")
	}
	return &Service{
		sessions: options.Sessions,
		tickets:  options.Tickets,
	}, nil
}

// Context confirms actionability before issuing tickets for an active device.
func (s *Service) Context(
	ctx context.Context,
	id agent.ID,
	expected session.StateSeq,
	deviceID string,
) (Context, error) {
	actionContext, err := s.sessions.ActionContext(ctx, id, expected)
	if err != nil {
		return Context{}, fmt.Errorf("respond: read action context: %w", err)
	}
	issued, err := s.tickets.IssueActionTickets(
		ctx,
		notify.ActionTicketRequest{
			AgentID:    id,
			BlockedSeq: uint64(actionContext.StateSeq),
			Actions:    append([]agent.ActionKind(nil), actionContext.Actions...),
			DeviceID:   deviceID,
		},
	)
	if err != nil {
		return Context{}, fmt.Errorf("respond: issue action tickets: %w", err)
	}
	return Context{
		StateSeq:  actionContext.StateSeq,
		Actions:   append([]agent.ActionKind(nil), actionContext.Actions...),
		Tickets:   append([]notify.IssuedActionTicket(nil), issued.Tickets...),
		ExpiresAt: issued.ExpiresAt,
		Screen:    actionContext.Screen,
	}, nil
}

// Execute consumes the ticket before asking the session to write the action.
func (s *Service) Execute(
	ctx context.Context,
	id agent.ID,
	ticket string,
	reply string,
) (Result, error) {
	claims, err := s.tickets.ConsumeActionTicket(ctx, id, ticket)
	if err != nil {
		return Result{}, fmt.Errorf("respond: consume action ticket: %w", err)
	}
	result, err := s.sessions.Respond(
		ctx,
		claims.AgentID,
		session.ActionRequest{
			ExpectedStateSeq: session.StateSeq(claims.BlockedSeq),
			Kind:             claims.Action,
			Reply:            reply,
			Channel:          string(notify.ChannelWebPush),
			DeviceID:         claims.DeviceID,
		},
	)
	if err != nil {
		return Result{}, fmt.Errorf("respond: execute session action: %w", err)
	}
	return result, nil
}

var (
	_ SessionActions = (*session.Manager)(nil)
	_ TicketActions  = (*notify.Service)(nil)
)
