package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Duang777/drove/internal/adapter"
	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/notify"
	"github.com/Duang777/drove/internal/respond"
	"github.com/Duang777/drove/internal/session"
)

const (
	maxActionContextRequestBytes = 1024
	maxActionRequestBytes        = 8 * 1024
	maxActionTicketFieldBytes    = 4096
	maxActionReplyBytes          = 4096
)

// ActionService is the API-facing remote response orchestration.
type ActionService interface {
	Context(
		context.Context,
		agent.ID,
		session.StateSeq,
		string,
	) (respond.Context, error)
	Execute(
		context.Context,
		agent.ID,
		string,
		string,
	) (respond.Result, error)
}

type actionContextRequest struct {
	BlockedSeq session.StateSeq `json:"blocked_seq"`
	DeviceID   string           `json:"device_id"`
}

type actionRequest struct {
	Ticket string `json:"ticket"`
	Reply  string `json:"reply,omitempty"`
}

type actionResponse struct {
	StateSeq     session.StateSeq `json:"state_seq"`
	BytesWritten int              `json:"bytes_written"`
	ActionSeq    string           `json:"action_seq"`
	InputSeq     string           `json:"input_seq"`
}

func (s *Server) handleActionContext(w http.ResponseWriter, r *http.Request) {
	if !rejectNotificationQuery(w, r) {
		return
	}
	if s.opts.Actions == nil {
		writeErr(w, http.StatusServiceUnavailable, "remote actions are unavailable")
		return
	}
	var request actionContextRequest
	if !decodeNotificationJSON(
		w,
		r,
		maxActionContextRequestBytes,
		&request,
	) {
		return
	}
	if request.BlockedSeq == 0 {
		writeErr(w, http.StatusBadRequest, "blocked_seq must be greater than zero")
		return
	}
	deviceID, err := canonicalUUIDField(request.DeviceID, "device_id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.opts.Actions.Context(
		r.Context(),
		agent.ID(r.PathValue("id")),
		request.BlockedSeq,
		deviceID,
	)
	if err != nil {
		writeActionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	if !rejectNotificationQuery(w, r) {
		return
	}
	if s.opts.Actions == nil {
		writeErr(w, http.StatusServiceUnavailable, "remote actions are unavailable")
		return
	}
	var request actionRequest
	if !decodeNotificationJSON(w, r, maxActionRequestBytes, &request) {
		return
	}
	if err := validateActionRequest(request); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.opts.Actions.Execute(
		r.Context(),
		agent.ID(r.PathValue("id")),
		request.Ticket,
		request.Reply,
	)
	if err != nil {
		writeActionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, actionResponse{
		StateSeq:     result.StateSeq,
		BytesWritten: result.BytesWritten,
		ActionSeq:    strconv.FormatUint(result.ActionSeq, 10),
		InputSeq:     strconv.FormatUint(result.InputSeq, 10),
	})
}

func validateActionRequest(request actionRequest) error {
	if request.Ticket == "" ||
		len(request.Ticket) > maxActionTicketFieldBytes ||
		request.Ticket != strings.TrimSpace(request.Ticket) {
		return errors.New("ticket must be a bounded non-empty string")
	}
	if !utf8.ValidString(request.Reply) || len(request.Reply) > maxActionReplyBytes {
		return errors.New("reply must be valid UTF-8 and at most 4096 bytes")
	}
	for _, char := range request.Reply {
		if unicode.IsControl(char) {
			return errors.New("reply must not contain control characters")
		}
	}
	return nil
}

func writeActionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, adapter.ErrApprovalReplyInvalid):
		writeErr(w, http.StatusBadRequest, "invalid action reply")
	case errors.Is(err, notify.ErrActionTicketInvalid),
		errors.Is(err, notify.ErrPushSubscriptionNotFound),
		errors.Is(err, notify.ErrPushSubscriptionRevoked):
		writeErr(w, http.StatusForbidden, "invalid action authorization")
	case errors.Is(err, session.ErrUnknownAgent):
		writeErr(w, http.StatusNotFound, "agent not found")
	case errors.Is(err, notify.ErrActionTicketExpired),
		errors.Is(err, session.ErrNotAttached):
		writeErr(w, http.StatusGone, "action target is no longer available")
	case errors.Is(err, notify.ErrActionTicketUsed),
		errors.Is(err, session.ErrActionStale),
		errors.Is(err, session.ErrActionUnavailable),
		errors.Is(err, session.ErrActionAlreadyAnswered):
		writeErr(w, http.StatusConflict, "action is no longer available")
	case errors.Is(err, adapter.ErrApprovalActionUnsupported):
		writeErr(w, http.StatusUnprocessableEntity, "action is not supported")
	case errors.Is(err, notify.ErrActionTicketsUnavailable),
		errors.Is(err, session.ErrActionBackpressure),
		errors.Is(err, session.ErrManagerClosed):
		writeErr(w, http.StatusServiceUnavailable, "action is temporarily unavailable")
	case errors.Is(err, session.ErrActionWrite),
		errors.Is(err, session.ErrActionAudit):
		writeErr(w, http.StatusInternalServerError, "action execution failed")
	default:
		writeErr(w, http.StatusInternalServerError, "remote action failed")
	}
}

var _ ActionService = (*respond.Service)(nil)
