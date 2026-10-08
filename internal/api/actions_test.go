package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/adapter"
	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/notify"
	"github.com/Duang777/drove/internal/respond"
	"github.com/Duang777/drove/internal/session"
)

func TestActionRoutesParseAndReturnBoundedContracts(t *testing.T) {
	server, _, _ := newTestServer(t)
	expiresAt := time.Date(2026, time.October, 8, 18, 10, 0, 0, time.UTC)
	actions := &fakeActionService{
		contextResult: respond.Context{
			StateSeq: 42,
			Actions: []agent.ActionKind{
				agent.ActionApprove,
				agent.ActionDeny,
			},
			Tickets: []notify.IssuedActionTicket{
				{Action: agent.ActionApprove, Ticket: "approve-ticket"},
				{Action: agent.ActionDeny, Ticket: "deny-ticket"},
			},
			ExpiresAt: expiresAt,
		},
		executeResult: respond.Result{
			StateSeq:     42,
			BytesWritten: 1,
			ActionSeq:    9_007_199_254_740_993,
			InputSeq:     9_007_199_254_740_994,
		},
	}
	server.opts.Actions = actions

	contextRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/agents/agent-1/action-context",
		strings.NewReader(
			`{"blocked_seq":"42","device_id":"`+testSubscriptionID+`"}`,
		),
	)
	contextRequest.Header.Set("Content-Type", "application/json")
	contextResponse := httptest.NewRecorder()
	serveAuthorized(server, contextResponse, contextRequest)
	if contextResponse.Code != http.StatusOK {
		t.Fatalf(
			"context response = %d %q",
			contextResponse.Code,
			contextResponse.Body.String(),
		)
	}
	if actions.contextAgent != "agent-1" ||
		actions.contextSeq != 42 ||
		actions.contextDevice != testSubscriptionID {
		t.Fatalf(
			"context input = %q/%d/%q",
			actions.contextAgent,
			actions.contextSeq,
			actions.contextDevice,
		)
	}
	if got := contextResponse.Body.String(); !strings.Contains(got, `"state_seq":"42"`) ||
		!strings.Contains(got, `"ticket":"approve-ticket"`) ||
		!strings.Contains(got, expiresAt.Format(time.RFC3339)) {
		t.Fatalf("context body = %q", got)
	}

	actionRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/agents/agent-1/actions",
		strings.NewReader(`{"ticket":"reply-ticket","reply":"ship it"}`),
	)
	actionRequest.Header.Set("Content-Type", "application/json")
	actionResponse := httptest.NewRecorder()
	serveAuthorized(server, actionResponse, actionRequest)
	if actionResponse.Code != http.StatusOK {
		t.Fatalf(
			"action response = %d %q",
			actionResponse.Code,
			actionResponse.Body.String(),
		)
	}
	if actions.executeAgent != "agent-1" ||
		actions.executeTicket != "reply-ticket" ||
		actions.executeReply != "ship it" {
		t.Fatalf(
			"execute input = %q/%q/%q",
			actions.executeAgent,
			actions.executeTicket,
			actions.executeReply,
		)
	}
	wantBody := `{"state_seq":"42","bytes_written":1,` +
		`"action_seq":"9007199254740993","input_seq":"9007199254740994"}` + "\n"
	if actionResponse.Body.String() != wantBody {
		t.Fatalf("action body = %q, want %q", actionResponse.Body.String(), wantBody)
	}
}

func TestActionRoutesStrictlyValidateRequests(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		contentType string
		body        string
		wantStatus  int
	}{
		{
			name:        "context requires JSON",
			path:        "/api/v1/agents/agent-1/action-context",
			contentType: "text/plain",
			body:        `{"blocked_seq":"42","device_id":"` + testSubscriptionID + `"}`,
			wantStatus:  http.StatusUnsupportedMediaType,
		},
		{
			name:        "context rejects numeric sequence",
			path:        "/api/v1/agents/agent-1/action-context",
			contentType: "application/json",
			body:        `{"blocked_seq":42,"device_id":"` + testSubscriptionID + `"}`,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "context rejects noncanonical device",
			path:        "/api/v1/agents/agent-1/action-context",
			contentType: "application/json",
			body:        `{"blocked_seq":"42","device_id":"550E8400-E29B-41D4-A716-446655440001"}`,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "action rejects unknown field",
			path:        "/api/v1/agents/agent-1/actions",
			contentType: "application/json",
			body:        `{"ticket":"ticket","reply":"","action":"approve"}`,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "action rejects control reply",
			path:        "/api/v1/agents/agent-1/actions",
			contentType: "application/json",
			body:        `{"ticket":"ticket","reply":"line\nbreak"}`,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "action rejects trailing value",
			path:        "/api/v1/agents/agent-1/actions",
			contentType: "application/json",
			body:        `{"ticket":"ticket"} {}`,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "action rejects oversized body",
			path:        "/api/v1/agents/agent-1/actions",
			contentType: "application/json",
			body: `{"ticket":"` +
				strings.Repeat("a", maxActionRequestBytes) +
				`"}`,
			wantStatus: http.StatusRequestEntityTooLarge,
		},
		{
			name:        "action rejects invalid UTF-8",
			path:        "/api/v1/agents/agent-1/actions",
			contentType: "application/json",
			body: string(append(
				[]byte(`{"ticket":"ticket","reply":"`),
				[]byte{0xff, '"', '}'}...,
			)),
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _, _ := newTestServer(t)
			server.opts.Actions = &fakeActionService{}
			request := httptest.NewRequest(
				http.MethodPost,
				test.path,
				strings.NewReader(test.body),
			)
			request.Header.Set("Content-Type", test.contentType)
			response := httptest.NewRecorder()
			serveAuthorized(server, response, request)
			if response.Code != test.wantStatus {
				t.Fatalf(
					"response = %d %q, want %d",
					response.Code,
					response.Body.String(),
					test.wantStatus,
				)
			}
		})
	}
}

func TestActionRoutesMapStableErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "invalid reply", err: adapter.ErrApprovalReplyInvalid, wantStatus: http.StatusBadRequest},
		{name: "invalid ticket", err: notify.ErrActionTicketInvalid, wantStatus: http.StatusForbidden},
		{name: "unknown agent", err: session.ErrUnknownAgent, wantStatus: http.StatusNotFound},
		{name: "expired ticket", err: notify.ErrActionTicketExpired, wantStatus: http.StatusGone},
		{name: "detached agent", err: session.ErrNotAttached, wantStatus: http.StatusGone},
		{name: "used ticket", err: notify.ErrActionTicketUsed, wantStatus: http.StatusConflict},
		{name: "stale state", err: session.ErrActionStale, wantStatus: http.StatusConflict},
		{name: "prompt absent", err: session.ErrActionUnavailable, wantStatus: http.StatusConflict},
		{name: "already answered", err: session.ErrActionAlreadyAnswered, wantStatus: http.StatusConflict},
		{name: "unsupported action", err: adapter.ErrApprovalActionUnsupported, wantStatus: http.StatusUnprocessableEntity},
		{name: "backpressure", err: session.ErrActionBackpressure, wantStatus: http.StatusServiceUnavailable},
		{name: "partial write", err: session.ErrActionWrite, wantStatus: http.StatusInternalServerError},
		{name: "audit failure", err: session.ErrActionAudit, wantStatus: http.StatusInternalServerError},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeActionError(response, errors.Join(errors.New("wrapped"), test.err))
			if response.Code != test.wantStatus {
				t.Fatalf(
					"response = %d %q, want %d",
					response.Code,
					response.Body.String(),
					test.wantStatus,
				)
			}
		})
	}
}

func TestActionRoutesRequireAuthenticationAndExactCookieOrigin(t *testing.T) {
	server, _, _ := newTestServer(t)
	server.opts.Actions = &fakeActionService{}

	code, err := server.opts.Auth.IssueLoginCode()
	if err != nil {
		t.Fatalf("issue login code: %v", err)
	}
	cookieValue, _, err := server.opts.Auth.ExchangeLoginCode(code)
	if err != nil {
		t.Fatalf("exchange login code: %v", err)
	}
	handler := server.Handler(BrowserAccess, "127.0.0.1:7373")

	routes := []struct {
		name string
		path string
		body string
	}{
		{
			name: "context",
			path: "/api/v1/agents/agent-1/action-context",
			body: `{"blocked_seq":"42","device_id":"` + testSubscriptionID + `"}`,
		},
		{
			name: "action",
			path: "/api/v1/agents/agent-1/actions",
			body: `{"ticket":"ticket"}`,
		},
	}
	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			unauthorized := httptest.NewRecorder()
			server.mux.ServeHTTP(
				unauthorized,
				httptest.NewRequest(
					http.MethodPost,
					route.path,
					strings.NewReader(route.body),
				),
			)
			if unauthorized.Code != http.StatusUnauthorized {
				t.Fatalf("unauthorized status = %d, want 401", unauthorized.Code)
			}

			for _, test := range []struct {
				name       string
				origin     string
				wantStatus int
			}{
				{name: "missing", wantStatus: http.StatusForbidden},
				{
					name:       "cross origin",
					origin:     "https://example.com",
					wantStatus: http.StatusForbidden,
				},
				{
					name:       "exact",
					origin:     "http://localhost:5173",
					wantStatus: http.StatusOK,
				},
			} {
				t.Run(test.name, func(t *testing.T) {
					request := httptest.NewRequest(
						http.MethodPost,
						route.path,
						strings.NewReader(route.body),
					)
					request.Host = "127.0.0.1:7373"
					request.Header.Set("Content-Type", "application/json")
					request.AddCookie(&http.Cookie{
						Name:  sessionCookieName,
						Value: cookieValue,
					})
					if test.origin != "" {
						request.Header.Set("Origin", test.origin)
					}
					response := httptest.NewRecorder()
					handler.ServeHTTP(response, request)
					if response.Code != test.wantStatus {
						t.Fatalf(
							"response = %d %q, want %d",
							response.Code,
							response.Body.String(),
							test.wantStatus,
						)
					}
				})
			}
		})
	}
}

type fakeActionService struct {
	contextResult respond.Context
	contextErr    error
	contextAgent  agent.ID
	contextSeq    session.StateSeq
	contextDevice string

	executeResult respond.Result
	executeErr    error
	executeAgent  agent.ID
	executeTicket string
	executeReply  string
}

func (f *fakeActionService) Context(
	_ context.Context,
	id agent.ID,
	sequence session.StateSeq,
	deviceID string,
) (respond.Context, error) {
	f.contextAgent = id
	f.contextSeq = sequence
	f.contextDevice = deviceID
	return f.contextResult, f.contextErr
}

func (f *fakeActionService) Execute(
	_ context.Context,
	id agent.ID,
	ticket string,
	reply string,
) (respond.Result, error) {
	f.executeAgent = id
	f.executeTicket = ticket
	f.executeReply = reply
	return f.executeResult, f.executeErr
}

var _ ActionService = (*fakeActionService)(nil)
