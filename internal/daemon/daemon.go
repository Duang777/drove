// Package daemon 组装并驱动 Drove 常驻服务。
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/drovehq/drove/internal/adapter"
	"github.com/drovehq/drove/internal/api"
	"github.com/drovehq/drove/internal/config"
	"github.com/drovehq/drove/internal/event"
	"github.com/drovehq/drove/internal/session"
	"github.com/drovehq/drove/internal/store"
	"github.com/drovehq/drove/internal/version"
)

// Daemon 是常驻服务。
type Daemon struct {
	cfg *config.Config
}

// New 创建 Daemon。
func New(cfg *config.Config) *Daemon {
	return &Daemon{cfg: cfg}
}

// Run 启动 daemon 并阻塞，直到 ctx 取消或收到终止信号。
func (d *Daemon) Run(ctx context.Context) error {
	log := slog.Default()

	if err := d.cfg.Validate(); err != nil {
		return fmt.Errorf("daemon: config: %w", err)
	}

	// 1. 存储。
	st, err := store.Open(d.cfg.DBPath)
	if err != nil {
		return fmt.Errorf("daemon: store: %w", err)
	}
	defer st.Close()

	// 2. 事件 Hub + 适配器注册表 + 会话管理器。
	hub := event.NewHub()
	reg := adapter.NewRegistry()
	mgr := session.NewManager(reg, hub, st)

	// 3. API server。
	srv := api.NewServer(api.ServerOptions{
		Bind:        d.cfg.APIBind,
		Manager:     mgr,
		Hub:         hub,
		EventBuffer: d.cfg.EventBuffer,
	})

	ln, err := net.Listen("tcp", d.cfg.APIBind)
	if err != nil {
		return fmt.Errorf("daemon: listen %s: %w", d.cfg.APIBind, err)
	}

	// 4. 启动 + 优雅关闭。
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Info("drove daemon listening", "addr", ln.Addr().String(), "version", version.Version)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("daemon: shutdown: %w", err)
	}
	log.Info("drove daemon stopped")
	return nil
}
