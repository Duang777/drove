// Package api 提供 REST 管理接口与 WebSocket 事件流。
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/auth"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/session"
)

// ServerOptions 配置 API server。
type ServerOptions struct {
	// Manager 处理会话逻辑。
	Manager *session.Manager
	// Hub 提供实时事件流。
	Hub *event.Hub
	// EventBuffer 是每个 WS 订阅的缓冲行数。
	EventBuffer int
	// ControlToken 认证 REST 与 WebSocket 控制面请求。
	ControlToken string
	// AllowedOrigins 是 WebSocket 可接受的精确 Origin。
	AllowedOrigins []string
}

// Server 是 HTTP/WS 服务。
type Server struct {
	opts           ServerOptions
	mux            http.Handler
	allowedOrigins map[string]struct{}
}

// Access identifies the transport boundary that accepted a request.
type Access uint8

const (
	// LocalAccess is a same-UID Unix socket request.
	LocalAccess Access = iota + 1
	// BrowserAccess is a loopback TCP request.
	BrowserAccess
)

type accessContextKey struct{}

// NewServer 创建 Server（路由已注册）。
func NewServer(opts ServerOptions) *Server {
	allowedOrigins := make(map[string]struct{}, len(opts.AllowedOrigins))
	for _, origin := range opts.AllowedOrigins {
		allowedOrigins[origin] = struct{}{}
	}
	s := &Server{opts: opts, allowedOrigins: allowedOrigins}
	controlMux := http.NewServeMux()
	s.routes(controlMux)
	rootMux := http.NewServeMux()
	rootMux.HandleFunc("POST /api/v1/agents/{id}/signal", s.handleSignal)
	rootMux.Handle("/", s.authenticate(controlMux))
	s.mux = rootMux
	return s
}

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/agents", s.handleList)
	mux.HandleFunc("POST /api/v1/agents", s.handleCreate)
	mux.HandleFunc("GET /api/v1/agents/{id}", s.handleGet)
	mux.HandleFunc("DELETE /api/v1/agents/{id}", s.handleDelete)
	mux.HandleFunc("GET /api/v1/agents/{id}/explain", s.handleExplain)
	mux.HandleFunc("POST /api/v1/agents/{id}/input", s.handleInput)
	mux.HandleFunc("GET /api/v1/agents/{id}/events", s.handleReplay)
	mux.HandleFunc("GET /ws", s.handleWS)
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("Authorization")
		if len(values) != 1 || !auth.Verify(s.opts.ControlToken, values[0]) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Handler returns the route tree bound to one accepted transport.
func (s *Server) Handler(access Access) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), accessContextKey{}, access)
		s.mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

// -- handlers --

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.opts.Manager.List())
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req session.StartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	st, err := s.opts.Manager.Start(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, session.ErrInvalidMode),
			errors.Is(err, session.ErrInvalidHookPolicy),
			errors.Is(err, session.ErrHookUnsupported):
			writeErr(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, session.ErrHookRequired),
			errors.Is(err, session.ErrManagerClosed),
			errors.Is(err, session.ErrEventCommitterUnavailable),
			errors.Is(err, session.ErrSignalOriginUnavailable):
			writeErr(w, http.StatusServiceUnavailable, err.Error())
		default:
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusCreated, st)
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	st, err := s.opts.Manager.Status(agent.ID(id))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleExplain(w http.ResponseWriter, r *http.Request) {
	limit, err := parseExplainLimit(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	explanation, err := s.opts.Manager.Explain(
		r.Context(),
		agent.ID(r.PathValue("id")),
		session.ExplainOptions{Limit: limit},
	)
	if err != nil {
		switch {
		case errors.Is(err, session.ErrInvalidExplainLimit):
			writeErr(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, session.ErrUnknownAgent):
			writeErr(w, http.StatusNotFound, err.Error())
		default:
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, explanation)
}

func parseExplainLimit(r *http.Request) (int, error) {
	query := r.URL.Query()
	for key := range query {
		if key != "limit" {
			return 0, fmt.Errorf("unsupported query parameter %q", key)
		}
	}
	values, exists := query["limit"]
	if !exists {
		return 0, nil
	}
	if len(values) != 1 || values[0] == "" {
		return 0, errors.New("limit must be one positive integer")
	}
	limit, err := strconv.Atoi(values[0])
	if err != nil || limit <= 0 {
		return 0, errors.New("limit must be one positive integer")
	}
	return limit, nil
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.opts.Manager.Stop(agent.ID(id)); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

const maxInputRequestBytes = 6*session.MaxInputBytes + 1024
const maxSignalRequestBytes = session.MaxSignalPayloadBytes + 4096

type inputRequest struct {
	Data string `json:"data"`
}

type signalRequest struct {
	Version    int             `json:"version"`
	Vendor     string          `json:"vendor"`
	DeliveryID string          `json:"delivery_id"`
	Payload    json.RawMessage `json:"payload"`
}

func (s *Server) handleInput(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, "content type must be application/json")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxInputRequestBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeErr(w, http.StatusRequestEntityTooLarge, "request body exceeds maximum size")
			return
		}
		writeErr(w, http.StatusBadRequest, "read request body: "+err.Error())
		return
	}
	if !utf8.Valid(body) {
		writeErr(w, http.StatusBadRequest, session.ErrInputNotUTF8.Error())
		return
	}

	var req inputRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "request body must contain one JSON object")
		return
	}

	if _, err := s.opts.Manager.SendInput(agent.ID(r.PathValue("id")), []byte(req.Data)); err != nil {
		switch {
		case errors.Is(err, session.ErrInputEmpty), errors.Is(err, session.ErrInputNotUTF8):
			writeErr(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, session.ErrInputTooLarge):
			writeErr(w, http.StatusRequestEntityTooLarge, err.Error())
		case errors.Is(err, session.ErrUnknownAgent):
			writeErr(w, http.StatusNotFound, err.Error())
		case errors.Is(err, session.ErrNotAttached):
			writeErr(w, http.StatusConflict, err.Error())
		case errors.Is(err, session.ErrManagerClosed):
			writeErr(w, http.StatusServiceUnavailable, err.Error())
		default:
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSignal(w http.ResponseWriter, r *http.Request) {
	if requestAccess(r) != LocalAccess && !isLoopbackRemote(r.RemoteAddr) {
		writeErr(w, http.StatusForbidden, "signal endpoint accepts loopback requests only")
		return
	}
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	token, ok := strings.CutPrefix(values[0], "Bearer ")
	if !ok || token == "" {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeErr(w, http.StatusBadRequest, "content type must be application/json")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxSignalRequestBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeErr(w, http.StatusRequestEntityTooLarge, "request body exceeds maximum size")
			return
		}
		writeErr(w, http.StatusBadRequest, "read request body: "+err.Error())
		return
	}
	if !utf8.Valid(body) {
		writeErr(w, http.StatusBadRequest, "signal request is not valid UTF-8")
		return
	}

	var req signalRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "request body must contain one JSON object")
		return
	}
	if len(req.Payload) > session.MaxSignalPayloadBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "signal payload exceeds maximum size")
		return
	}
	if err := validateSignalRequest(req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	err = s.opts.Manager.DeliverHook(
		r.Context(),
		agent.ID(r.PathValue("id")),
		session.HookDelivery{
			Token:      token,
			Vendor:     req.Vendor,
			DeliveryID: req.DeliveryID,
			Payload:    req.Payload,
		},
	)
	if err != nil {
		writeSignalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func validateSignalRequest(req signalRequest) error {
	if req.Version != 1 {
		return errors.New("unsupported signal protocol version")
	}
	if req.Vendor == "" || strings.TrimSpace(req.Vendor) != req.Vendor {
		return errors.New("signal vendor is required")
	}
	deliveryID, err := uuid.Parse(req.DeliveryID)
	if err != nil || deliveryID.String() != req.DeliveryID {
		return errors.New("signal delivery ID must be a canonical UUID")
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(req.Payload, &payload); err != nil || payload == nil {
		return errors.New("signal payload must be one JSON object")
	}
	return nil
}

func writeSignalError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, session.ErrHookUnauthorized):
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeErr(w, http.StatusUnauthorized, "unauthorized")
	case errors.Is(err, session.ErrUnknownAgent):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, session.ErrHookDisabled),
		errors.Is(err, session.ErrHookUnsupported),
		errors.Is(err, session.ErrHookVendorMismatch):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, session.ErrHookDetached):
		writeErr(w, http.StatusGone, err.Error())
	case errors.Is(err, session.ErrHookBackpressure):
		w.Header().Set("Retry-After", "1")
		writeErr(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, session.ErrManagerClosed),
		errors.Is(err, session.ErrEventCommitterUnavailable):
		writeErr(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, session.ErrHookInvalid):
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) handleReplay(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rows, err := s.opts.Manager.Replay(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func isLoopbackRemote(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func requestAccess(r *http.Request) Access {
	access, _ := r.Context().Value(accessContextKey{}).(Access)
	return access
}

// -- helpers --

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("write json failed", "err", err)
	}
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
