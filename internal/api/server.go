// Package api 提供 REST 管理接口与 WebSocket 事件流。
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/auth"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/session"
)

// ServerOptions 配置 API server。
type ServerOptions struct {
	// Bind 是监听地址（如 127.0.0.1:7373）。
	Bind string
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
	http           *http.Server
	mux            http.Handler
	allowedOrigins map[string]struct{}
}

// NewServer 创建 Server（路由已注册）。
func NewServer(opts ServerOptions) *Server {
	allowedOrigins := make(map[string]struct{}, len(opts.AllowedOrigins))
	for _, origin := range opts.AllowedOrigins {
		allowedOrigins[origin] = struct{}{}
	}
	s := &Server{opts: opts, allowedOrigins: allowedOrigins}
	mux := http.NewServeMux()
	s.routes(mux)
	s.mux = s.authenticate(mux)
	s.http = &http.Server{
		Handler:           s.mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/agents", s.handleList)
	mux.HandleFunc("POST /api/v1/agents", s.handleCreate)
	mux.HandleFunc("GET /api/v1/agents/{id}", s.handleGet)
	mux.HandleFunc("DELETE /api/v1/agents/{id}", s.handleDelete)
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

// Serve 开始监听并服务。
func (s *Server) Serve(ln net.Listener) error {
	return s.http.Serve(ln)
}

// Shutdown 优雅关闭。
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
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
		if errors.Is(err, session.ErrInvalidMode) {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
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

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.opts.Manager.Stop(agent.ID(id)); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

const maxInputRequestBytes = 6*session.MaxInputBytes + 128

type inputRequest struct {
	Data string `json:"data"`
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

func (s *Server) handleReplay(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rows, err := s.opts.Manager.Replay(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

// handleWS 提供实时事件流。
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	up := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 4096,
		CheckOrigin:     s.checkWebSocketOrigin,
	}
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("ws upgrade failed", "err", err)
		return
	}
	defer conn.Close()

	buf := s.opts.EventBuffer
	if buf <= 0 {
		buf = 1024
	}
	sub := s.opts.Hub.Subscribe(buf)
	defer s.opts.Hub.Unsubscribe(sub)

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"hello"}`))

	for {
		select {
		case <-readDone:
			return
		case ev, ok := <-sub.C():
			if !ok {
				return
			}
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			payload, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		}
	}
}

func (s *Server) checkWebSocketOrigin(r *http.Request) bool {
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
