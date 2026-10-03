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

	"github.com/Duang777/drove/internal/adapter"
	"github.com/Duang777/drove/internal/api"
	"github.com/Duang777/drove/internal/auth"
	"github.com/Duang777/drove/internal/config"
	"github.com/Duang777/drove/internal/session"
	"github.com/Duang777/drove/internal/store"
	"github.com/Duang777/drove/internal/version"
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
func (d *Daemon) Run(ctx context.Context) (runErr error) {
	log := slog.Default()

	if err := d.cfg.Validate(); err != nil {
		return fmt.Errorf("daemon: config: %w", err)
	}
	controlToken, err := auth.Ensure(d.cfg.DataDir)
	if err != nil {
		return fmt.Errorf("daemon: control token: %w", err)
	}

	// 1. 存储。
	st, err := store.Open(d.cfg.DBPath)
	if err != nil {
		return fmt.Errorf("daemon: store: %w", err)
	}
	defer func() {
		if closeErr := st.Close(); closeErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("daemon: close store: %w", closeErr))
		}
	}()

	// 2. 在 API 对外可见前恢复会话投影。
	recovered, err := bootstrapSessions(ctx, st)
	if err != nil {
		return err
	}
	hub := recovered.Hub
	mgr := recovered.Manager
	report := recovered.Recovery
	log.Info(
		"session projection recovered",
		"scanned_events", report.ScannedEvents,
		"sessions", report.Sessions,
		"interrupted", report.Interrupted,
		"legacy_metadata", report.LegacyMetadata,
		"partial_history", report.PartialHistory,
		"last_seq", report.LastSeq,
	)

	// 3. API server。
	srv := api.NewServer(api.ServerOptions{
		Bind:           d.cfg.APIBind,
		Manager:        mgr,
		Hub:            hub,
		EventBuffer:    d.cfg.EventBuffer,
		ControlToken:   controlToken,
		AllowedOrigins: d.cfg.ConsoleOrigins,
	})

	ln, err := net.Listen("tcp", d.cfg.APIBind)
	if err != nil {
		closeErr := mgr.Close()
		hub.Close()
		return errors.Join(
			fmt.Errorf("daemon: listen %s: %w", d.cfg.APIBind, err),
			closeErr,
		)
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
		runErr = fmt.Errorf("daemon: serve: %w", err)
	case err := <-mgr.Fatal():
		runErr = fmt.Errorf("daemon: session event commit: %w", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("daemon: shutdown api: %w", err))
	}
	if err := mgr.Close(); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("daemon: shutdown sessions: %w", err))
	}
	hub.Close()
	log.Info("drove daemon stopped")
	return runErr
}

func bootstrapSessions(ctx context.Context, st *store.Store) (*session.BootstrapResult, error) {
	recovered, err := session.Bootstrap(ctx, adapter.NewRegistry(), st)
	if err != nil {
		return nil, fmt.Errorf("daemon: bootstrap sessions: %w", err)
	}
	return recovered, nil
}
