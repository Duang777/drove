package respond

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/notify"
	"github.com/Duang777/drove/internal/session"
)

func TestNewService(t *testing.T) {
	sessions := &fakeSessions{}
	tickets := &fakeTickets{}
	if _, err := NewService(ServiceOptions{
		Sessions: sessions,
		Tickets:  tickets,
	}); err != nil {
		t.Fatalf("create response service: %v", err)
	}
	if _, err := NewService(ServiceOptions{Tickets: tickets}); err == nil {
		t.Fatal("service accepted nil session manager")
	}
	if _, err := NewService(ServiceOptions{Sessions: sessions}); err == nil {
		t.Fatal("service accepted nil ticket service")
	}
}

func TestServiceContext(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.October, 8, 17, 0, 0, 0, time.UTC)
	order := make([]string, 0, 2)
	sessions := &fakeSessions{
		contextResult: session.ActionContext{
			StateSeq: 42,
			Actions: []agent.ActionKind{
				agent.ActionApprove,
				agent.ActionDeny,
			},
		},
		contextOrder: &order,
	}
	tickets := &fakeTickets{
		issueResult: notify.IssuedActionTickets{
			Tickets: []notify.IssuedActionTicket{
				{Action: agent.ActionApprove, Ticket: "approve-ticket"},
				{Action: agent.ActionDeny, Ticket: "deny-ticket"},
			},
			ExpiresAt: now.Add(notify.DefaultActionTicketLifetime),
		},
		order: &order,
	}
	service, err := NewService(ServiceOptions{
		Sessions: sessions,
		Tickets:  tickets,
	})
	if err != nil {
		t.Fatalf("create response service: %v", err)
	}

	result, err := service.Context(ctx, "agent-1", 42, "phone")
	if err != nil {
		t.Fatalf("get action context: %v", err)
	}
	if !reflect.DeepEqual(order, []string{"session.context", "tickets.issue"}) {
		t.Fatalf("call order = %v", order)
	}
	if sessions.contextAgent != "agent-1" || sessions.contextSeq != 42 {
		t.Fatalf(
			"session context input = %q/%d",
			sessions.contextAgent,
			sessions.contextSeq,
		)
	}
	if tickets.issueRequest.AgentID != "agent-1" ||
		tickets.issueRequest.BlockedSeq != 42 ||
		tickets.issueRequest.DeviceID != "phone" ||
		!reflect.DeepEqual(
			tickets.issueRequest.Actions,
			sessions.contextResult.Actions,
		) {
		t.Fatalf("ticket issue request = %+v", tickets.issueRequest)
	}
	if result.StateSeq != 42 ||
		!reflect.DeepEqual(result.Actions, sessions.contextResult.Actions) ||
		!reflect.DeepEqual(result.Tickets, tickets.issueResult.Tickets) ||
		!result.ExpiresAt.Equal(tickets.issueResult.ExpiresAt) {
		t.Fatalf("context result = %+v", result)
	}

	sessions.contextErr = session.ErrActionStale
	order = order[:0]
	if _, err := service.Context(
		ctx,
		"agent-1",
		42,
		"phone",
	); !errors.Is(err, session.ErrActionStale) {
		t.Fatalf("context error = %v, want ErrActionStale", err)
	}
	if !reflect.DeepEqual(order, []string{"session.context"}) {
		t.Fatalf("failed context call order = %v", order)
	}
}

func TestServiceExecute(t *testing.T) {
	ctx := context.Background()
	order := make([]string, 0, 2)
	tickets := &fakeTickets{
		consumeResult: notify.ActionTicketClaims{
			AgentID:    agent.ID("agent-1"),
			BlockedSeq: 42,
			Action:     agent.ActionReply,
			DeviceID:   "phone",
		},
		order: &order,
	}
	sessions := &fakeSessions{
		respondResult: session.ActionResult{
			StateSeq:     42,
			BytesWritten: 8,
			ActionSeq:    43,
			InputSeq:     44,
		},
		respondOrder: &order,
	}
	service, err := NewService(ServiceOptions{
		Sessions: sessions,
		Tickets:  tickets,
	})
	if err != nil {
		t.Fatalf("create response service: %v", err)
	}

	result, err := service.Execute(ctx, "agent-1", "ticket", "ship it")
	if err != nil {
		t.Fatalf("execute action: %v", err)
	}
	if !reflect.DeepEqual(order, []string{"tickets.consume", "session.respond"}) {
		t.Fatalf("call order = %v", order)
	}
	if tickets.consumeAgent != "agent-1" || tickets.consumeToken != "ticket" {
		t.Fatalf(
			"consume input = %q/%q",
			tickets.consumeAgent,
			tickets.consumeToken,
		)
	}
	wantRequest := session.ActionRequest{
		ExpectedStateSeq: 42,
		Kind:             agent.ActionReply,
		Reply:            "ship it",
		Channel:          string(notify.ChannelWebPush),
		DeviceID:         "phone",
	}
	if sessions.respondAgent != "agent-1" ||
		sessions.respondRequest != wantRequest {
		t.Fatalf(
			"respond input = %q/%+v, want %q/%+v",
			sessions.respondAgent,
			sessions.respondRequest,
			"agent-1",
			wantRequest,
		)
	}
	if result != sessions.respondResult {
		t.Fatalf("execute result = %+v, want %+v", result, sessions.respondResult)
	}

	tickets.consumeErr = notify.ErrActionTicketUsed
	order = order[:0]
	if _, err := service.Execute(
		ctx,
		"agent-1",
		"ticket",
		"ship it",
	); !errors.Is(err, notify.ErrActionTicketUsed) {
		t.Fatalf("execute error = %v, want ErrActionTicketUsed", err)
	}
	if !reflect.DeepEqual(order, []string{"tickets.consume"}) {
		t.Fatalf("failed execute call order = %v", order)
	}
}

func TestServiceExecuteKeepsTicketConsumedWhenSessionFails(t *testing.T) {
	ctx := context.Background()
	tickets := &singleUseTickets{
		claims: notify.ActionTicketClaims{
			AgentID:    agent.ID("agent-1"),
			BlockedSeq: 42,
			Action:     agent.ActionApprove,
			DeviceID:   "phone",
		},
	}
	sessions := &fakeSessions{respondErr: session.ErrActionStale}
	service, err := NewService(ServiceOptions{
		Sessions: sessions,
		Tickets:  tickets,
	})
	if err != nil {
		t.Fatalf("create response service: %v", err)
	}

	if _, err := service.Execute(
		ctx,
		"agent-1",
		"ticket",
		"",
	); !errors.Is(err, session.ErrActionStale) {
		t.Fatalf("first execute error = %v, want ErrActionStale", err)
	}
	if _, err := service.Execute(
		ctx,
		"agent-1",
		"ticket",
		"",
	); !errors.Is(err, notify.ErrActionTicketUsed) {
		t.Fatalf("retry error = %v, want ErrActionTicketUsed", err)
	}
	if sessions.respondCalls != 1 {
		t.Fatalf("session response calls = %d, want 1", sessions.respondCalls)
	}
}

type fakeSessions struct {
	contextResult session.ActionContext
	contextErr    error
	contextAgent  agent.ID
	contextSeq    session.StateSeq
	contextOrder  *[]string

	respondResult  session.ActionResult
	respondErr     error
	respondAgent   agent.ID
	respondRequest session.ActionRequest
	respondOrder   *[]string
	respondCalls   int
}

func (f *fakeSessions) ActionContext(
	_ context.Context,
	id agent.ID,
	sequence session.StateSeq,
) (session.ActionContext, error) {
	if f.contextOrder != nil {
		*f.contextOrder = append(*f.contextOrder, "session.context")
	}
	f.contextAgent = id
	f.contextSeq = sequence
	return f.contextResult, f.contextErr
}

func (f *fakeSessions) Respond(
	_ context.Context,
	id agent.ID,
	request session.ActionRequest,
) (session.ActionResult, error) {
	if f.respondOrder != nil {
		*f.respondOrder = append(*f.respondOrder, "session.respond")
	}
	f.respondCalls++
	f.respondAgent = id
	f.respondRequest = request
	return f.respondResult, f.respondErr
}

type fakeTickets struct {
	issueResult  notify.IssuedActionTickets
	issueErr     error
	issueRequest notify.ActionTicketRequest

	consumeResult notify.ActionTicketClaims
	consumeErr    error
	consumeAgent  agent.ID
	consumeToken  string

	order *[]string
}

func (f *fakeTickets) IssueActionTickets(
	_ context.Context,
	request notify.ActionTicketRequest,
) (notify.IssuedActionTickets, error) {
	if f.order != nil {
		*f.order = append(*f.order, "tickets.issue")
	}
	f.issueRequest = request
	return f.issueResult, f.issueErr
}

func (f *fakeTickets) ConsumeActionTicket(
	_ context.Context,
	id agent.ID,
	token string,
) (notify.ActionTicketClaims, error) {
	if f.order != nil {
		*f.order = append(*f.order, "tickets.consume")
	}
	f.consumeAgent = id
	f.consumeToken = token
	return f.consumeResult, f.consumeErr
}

type singleUseTickets struct {
	claims notify.ActionTicketClaims
	used   bool
}

func (t *singleUseTickets) IssueActionTickets(
	context.Context,
	notify.ActionTicketRequest,
) (notify.IssuedActionTickets, error) {
	return notify.IssuedActionTickets{}, errors.New("unexpected ticket issue")
}

func (t *singleUseTickets) ConsumeActionTicket(
	context.Context,
	agent.ID,
	string,
) (notify.ActionTicketClaims, error) {
	if t.used {
		return notify.ActionTicketClaims{}, notify.ErrActionTicketUsed
	}
	t.used = true
	return t.claims, nil
}

var _ TicketActions = (*singleUseTickets)(nil)
