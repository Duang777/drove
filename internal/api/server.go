// Package api 提供 REST 管理接口与 WebSocket 事件流。
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/auth"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/recording"
	"github.com/Duang777/drove/internal/session"
)

// ServerOptions 配置 API server。
type ServerOptions struct {
	// Manager 处理会话逻辑。
	Manager *session.Manager
	// Hub 提供实时事件流。
	Hub *event.Hub
	// Auth owns control-plane credentials and their revocation lifecycle.
	Auth *auth.Controller
	// Web contains the embedded browser console production build.
	Web fs.FS
	// EventBuffer 是每个 WS 订阅的缓冲行数。
	EventBuffer int
	// AllowedOrigins contains the exact browser origins accepted by REST and WebSocket.
	AllowedOrigins []string
}

// Server 是 HTTP/WS 服务。
type Server struct {
	opts                   ServerOptions
	mux                    http.Handler
	web                    http.Handler
	allowedOrigins         map[string]struct{}
	webSocketV2QueueBudget int
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
type grantContextKey struct{}

const sessionCookieName = "drove_session"

// NewServer 创建 Server（路由已注册）。
func NewServer(opts ServerOptions) *Server {
	allowedOrigins := make(map[string]struct{}, len(opts.AllowedOrigins))
	for _, origin := range opts.AllowedOrigins {
		allowedOrigins[origin] = struct{}{}
	}
	s := &Server{
		opts:                   opts,
		allowedOrigins:         allowedOrigins,
		webSocketV2QueueBudget: webSocketV2QueueBytes,
	}
	if opts.Web != nil {
		s.web = http.FileServer(http.FS(opts.Web))
	}
	controlMux := http.NewServeMux()
	s.routes(controlMux)
	rootMux := http.NewServeMux()
	rootMux.HandleFunc("POST /api/v1/agents/{id}/signal", s.handleSignal)
	rootMux.HandleFunc("POST /api/v1/auth/login", s.handleExchangeLogin)
	rootMux.Handle("/api/", s.authenticate(controlMux))
	rootMux.Handle("/ws", s.authenticate(controlMux))
	rootMux.HandleFunc("/", s.handleWeb)
	s.mux = rootMux
	return s
}

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/agents", s.handleList)
	mux.HandleFunc("POST /api/v1/agents", s.handleCreate)
	mux.HandleFunc("POST /api/v1/worktrees", s.handleWorkspaceCreate)
	mux.HandleFunc("DELETE /api/v1/worktrees/{id}", s.handleWorkspaceDelete)
	mux.HandleFunc("GET /api/v1/agents/{id}", s.handleGet)
	mux.HandleFunc("DELETE /api/v1/agents/{id}", s.handleDelete)
	mux.HandleFunc("POST /api/v1/agents/{id}/resume", s.handleResume)
	mux.HandleFunc("GET /api/v1/agents/{id}/explain", s.handleExplain)
	mux.HandleFunc("POST /api/v1/agents/{id}/input", s.handleInput)
	mux.HandleFunc("GET /api/v1/agents/{id}/events", s.handleReplay)
	mux.HandleFunc("POST /api/v1/auth/login-code", s.handleIssueLoginCode)
	mux.HandleFunc("POST /api/v1/auth/token/rotate", s.handleRotateToken)
	mux.HandleFunc("GET /api/v1/agents/{id}/timeline", s.handleTimeline)
	mux.HandleFunc(
		"GET /api/v1/agents/{id}/timeline/blocked/{number}",
		s.handleBlockedOccurrence,
	)
	mux.HandleFunc("GET /api/v1/agents/{id}/frame", s.handleFrame)
	mux.HandleFunc("GET /ws", s.handleWS)
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("Authorization")
		var (
			grant auth.Grant
			ok    bool
		)
		switch {
		case len(values) == 1:
			grant, ok = s.opts.Auth.AuthorizeBearer(values[0])
		case len(values) == 0 && requestAccess(r) == BrowserAccess:
			cookie, err := r.Cookie(sessionCookieName)
			if err == nil {
				grant, ok = s.opts.Auth.AuthorizeCookie(cookie.Value)
			}
		}
		if !ok {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if grant.Kind() == auth.CookieAccess &&
			requiresOrigin(r) &&
			len(r.Header.Values("Origin")) == 0 {
			writeErr(w, http.StatusForbidden, "origin required")
			return
		}
		ctx := context.WithValue(r.Context(), grantContextKey{}, grant)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Handler returns the route tree bound to one accepted transport.
func (s *Server) Handler(access Access, allowedHosts ...string) http.Handler {
	hosts := make(map[string]struct{}, len(allowedHosts))
	for _, host := range allowedHosts {
		hosts[host] = struct{}{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := hosts[r.Host]; !ok {
			writeErr(w, http.StatusForbidden, "host not allowed")
			return
		}
		if access == BrowserAccess && !s.originAllowed(r) {
			writeErr(w, http.StatusForbidden, "origin not allowed")
			return
		}
		ctx := context.WithValue(r.Context(), accessContextKey{}, access)
		s.mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

// -- handlers --

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.opts.Manager.List())
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	s.handleStart(w, r, false)
}

func (s *Server) handleWorkspaceCreate(
	w http.ResponseWriter,
	r *http.Request,
) {
	s.handleStart(w, r, true)
}

func (s *Server) handleStart(
	w http.ResponseWriter,
	r *http.Request,
	requireWorktree bool,
) {
	var req session.StartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if requireWorktree && req.Worktree == nil {
		writeErr(w, http.StatusBadRequest, "worktree request is required")
		return
	}
	st, err := s.opts.Manager.Start(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, session.ErrInvalidMode),
			errors.Is(err, session.ErrInvalidHookPolicy),
			errors.Is(err, session.ErrHookUnsupported),
			errors.Is(err, session.ErrWorkspaceRequest):
			writeErr(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, session.ErrHookRequired),
			errors.Is(err, session.ErrManagerClosed),
			errors.Is(err, session.ErrEventCommitterUnavailable),
			errors.Is(err, session.ErrSignalOriginUnavailable),
			errors.Is(err, session.ErrWorkspaceUnavailable):
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

type workspaceCleanupResponse struct {
	AgentID string `json:"agent_id"`
	Branch  string `json:"branch"`
}

func (s *Server) handleWorkspaceDelete(w http.ResponseWriter, r *http.Request) {
	if requestAccess(r) != LocalAccess {
		writeErr(w, http.StatusForbidden, "worktree cleanup requires local access")
		return
	}
	force, err := parseWorkspaceForce(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	removed, err := s.opts.Manager.CleanupWorkspace(
		r.Context(),
		r.PathValue("id"),
		force,
	)
	if err != nil {
		switch {
		case errors.Is(err, session.ErrWorkspaceRequest):
			writeErr(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, session.ErrWorkspaceNotFound):
			writeErr(w, http.StatusNotFound, err.Error())
		case errors.Is(err, session.ErrWorkspaceDirty),
			errors.Is(err, session.ErrWorkspaceInUse):
			writeErr(w, http.StatusConflict, err.Error())
		case errors.Is(err, session.ErrWorkspaceUnavailable),
			errors.Is(err, session.ErrManagerClosed):
			writeErr(w, http.StatusServiceUnavailable, err.Error())
		default:
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, workspaceCleanupResponse{
		AgentID: removed.AgentID,
		Branch:  removed.Branch,
	})
}

func parseWorkspaceForce(r *http.Request) (bool, error) {
	query := r.URL.Query()
	for key := range query {
		if key != "force" {
			return false, fmt.Errorf("unsupported query parameter %q", key)
		}
	}
	values, exists := query["force"]
	if !exists {
		return false, nil
	}
	if len(values) != 1 ||
		(values[0] != "true" && values[0] != "false") {
		return false, errors.New("force must be one boolean")
	}
	return values[0] == "true", nil
}

func (s *Server) handleRotateToken(w http.ResponseWriter, r *http.Request) {
	if requestAccess(r) != LocalAccess {
		writeErr(w, http.StatusForbidden, "token rotation requires local access")
		return
	}
	if err := s.opts.Auth.Rotate(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleIssueLoginCode(w http.ResponseWriter, r *http.Request) {
	if requestAccess(r) != LocalAccess {
		writeErr(w, http.StatusForbidden, "login code requires local access")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	code, err := s.opts.Auth.IssueLoginCode()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, loginCodeResponse{Code: code})
}

func (s *Server) handleExchangeLogin(w http.ResponseWriter, r *http.Request) {
	if requestAccess(r) != BrowserAccess {
		writeErr(w, http.StatusForbidden, "browser login requires browser access")
		return
	}
	if !s.hasAllowedOrigin(r) {
		writeErr(w, http.StatusForbidden, "origin required")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, "content type must be application/json")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxLoginRequestBytes)
	var request loginExchangeRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeErr(w, http.StatusRequestEntityTooLarge, "request body exceeds maximum size")
			return
		}
		writeErr(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "request body must contain one JSON object")
		return
	}

	cookie, _, err := s.opts.Auth.ExchangeLoginCode(request.Code)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid login code")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    cookie,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleWeb(w http.ResponseWriter, r *http.Request) {
	if requestAccess(r) != BrowserAccess || s.web == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	w.Header().Set(
		"Content-Security-Policy",
		fmt.Sprintf("default-src 'self'; connect-src 'self' ws://%s wss://%s; "+
			"img-src 'self' data:; object-src 'none'; base-uri 'none'; "+
			"frame-ancestors 'none'; form-action 'none'", r.Host, r.Host),
	)
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")

	switch {
	case r.URL.Path == "/" || r.URL.Path == "/login":
		cloned := r.Clone(r.Context())
		urlCopy := *r.URL
		urlCopy.Path = "/"
		cloned.URL = &urlCopy
		w.Header().Set("Cache-Control", "no-store")
		s.web.ServeHTTP(w, cloned)
	case strings.HasPrefix(r.URL.Path, "/assets/"):
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		s.web.ServeHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	status, err := s.opts.Manager.Resume(
		r.Context(),
		agent.ID(r.PathValue("id")),
	)
	if err != nil {
		writeResumeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func writeResumeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, session.ErrUnknownAgent):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, session.ErrResumeConflict):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, session.ErrHookRequired),
		errors.Is(err, session.ErrSignalOriginUnavailable),
		errors.Is(err, session.ErrManagerClosed),
		errors.Is(err, session.ErrEventCommitterUnavailable):
		writeErr(w, http.StatusServiceUnavailable, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

const maxInputRequestBytes = 6*session.MaxInputBytes + 1024
const maxSignalRequestBytes = session.MaxSignalPayloadBytes + 4096
const maxLoginRequestBytes = 4096

type inputRequest struct {
	Data string `json:"data"`
}

type loginCodeResponse struct {
	Code string `json:"code"`
}

type loginExchangeRequest struct {
	Code string `json:"code"`
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
		case errors.Is(err, session.ErrInputBackpressure):
			writeErr(w, http.StatusServiceUnavailable, err.Error())
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

func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	timeline, err := s.opts.Manager.Timeline(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRecordingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, timeline)
}

func (s *Server) handleBlockedOccurrence(w http.ResponseWriter, r *http.Request) {
	number, err := strconv.Atoi(r.PathValue("number"))
	if err != nil || number <= 0 {
		writeErr(w, http.StatusBadRequest, "blocked occurrence must be one positive integer")
		return
	}
	occurrence, err := s.opts.Manager.BlockedOccurrence(
		r.Context(),
		r.PathValue("id"),
		number,
	)
	if err != nil {
		writeRecordingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, occurrence)
}

func (s *Server) handleFrame(w http.ResponseWriter, r *http.Request) {
	selector, err := parseFrameSelector(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	frame, err := s.opts.Manager.Frame(
		r.Context(),
		r.PathValue("id"),
		selector,
	)
	if err != nil {
		writeRecordingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, frame)
}

func parseFrameSelector(r *http.Request) (recording.Selector, error) {
	query := r.URL.Query()
	for key := range query {
		switch key {
		case "seq", "at", "offset":
		default:
			return recording.Selector{}, fmt.Errorf(
				"unsupported query parameter %q",
				key,
			)
		}
	}

	var input recording.SelectorInput
	if values, ok := query["seq"]; ok {
		if len(values) != 1 || values[0] == "" {
			return recording.Selector{}, errors.New(
				"seq must be one canonical unsigned decimal",
			)
		}
		sequence, err := recording.ParseSeq(values[0])
		if err != nil {
			return recording.Selector{}, err
		}
		input.Seq = &sequence
	}
	if values, ok := query["at"]; ok {
		if len(values) != 1 || values[0] == "" {
			return recording.Selector{}, errors.New(
				"at must be one RFC3339 timestamp",
			)
		}
		at, err := time.Parse(time.RFC3339Nano, values[0])
		if err != nil {
			return recording.Selector{}, fmt.Errorf(
				"at must be one RFC3339 timestamp: %w",
				err,
			)
		}
		input.At = &at
	}
	if values, ok := query["offset"]; ok {
		if len(values) != 1 || values[0] == "" {
			return recording.Selector{}, errors.New(
				"offset must be one canonical unsigned decimal",
			)
		}
		offset, err := recording.ParseOutputOffset(values[0])
		if err != nil {
			return recording.Selector{}, err
		}
		input.Offset = &offset
	}
	selector, err := recording.NewSelector(input)
	if err != nil {
		return recording.Selector{}, fmt.Errorf(
			"%w: %v",
			recording.ErrInvalidFrameSelector,
			err,
		)
	}
	return selector, nil
}

func writeRecordingError(w http.ResponseWriter, err error) {
	var expired *recording.OutputExpiredError
	switch {
	case errors.As(err, &expired):
		writeJSON(w, http.StatusGone, struct {
			Error     string                  `json:"error"`
			Code      string                  `json:"code"`
			SessionID string                  `json:"session_id"`
			Missing   []recording.OutputRange `json:"missing"`
		}{
			Error:     err.Error(),
			Code:      "output_expired",
			SessionID: expired.SessionID,
			Missing:   expired.Missing,
		})
	case errors.Is(err, recording.ErrUnknownSession),
		errors.Is(err, recording.ErrBlockedOccurrenceNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, recording.ErrInvalidCursor),
		errors.Is(err, recording.ErrInvalidFrameSelector),
		errors.Is(err, recording.ErrInvalidBlockedOccurrence):
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
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

func requestGrant(r *http.Request) auth.Grant {
	grant, _ := r.Context().Value(grantContextKey{}).(auth.Grant)
	return grant
}

func (s *Server) originAllowed(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 {
		return false
	}
	_, allowed := s.allowedOrigins[origins[0]]
	return allowed
}

func (s *Server) hasAllowedOrigin(r *http.Request) bool {
	return len(r.Header.Values("Origin")) == 1 && s.originAllowed(r)
}

func requiresOrigin(r *http.Request) bool {
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return true
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
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
