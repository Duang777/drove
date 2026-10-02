// Package api 提供 REST 管理接口与 WebSocket 事件流。
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Duang777/drove/internal/agent"
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
}

// Server 是 HTTP/WS 服务。
type Server struct {
	opts ServerOptions
	http *http.Server
	mux  *http.ServeMux
}

// NewServer 创建 Server（路由已注册）。
func NewServer(opts ServerOptions) *Server {
	s := &Server{opts: opts, mux: http.NewServeMux()}
	s.routes()
	s.http = &http.Server{
		Handler:           s.mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

func (s *Server) routes() {
	mux := s.mux
	mux.HandleFunc("GET /api/v1/agents", s.handleList)
	mux.HandleFunc("POST /api/v1/agents", s.handleCreate)
	mux.HandleFunc("GET /api/v1/agents/{id}", s.handleGet)
	mux.HandleFunc("DELETE /api/v1/agents/{id}", s.handleDelete)
	mux.HandleFunc("GET /api/v1/agents/{id}/events", s.handleReplay)
	mux.HandleFunc("GET /ws", s.handleWS)
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
		CheckOrigin:     func(*http.Request) bool { return true },
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

	// 心跳：读侧持续读（丢弃消息），防止连接悬挂。
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"hello"}`))

	for ev := range sub.C() {
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
