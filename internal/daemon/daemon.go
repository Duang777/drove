// Package daemon 组装并驱动 Drove 常驻服务。
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Duang777/drove/internal/adapter"
	"github.com/Duang777/drove/internal/api"
	"github.com/Duang777/drove/internal/auth"
	"github.com/Duang777/drove/internal/config"
	"github.com/Duang777/drove/internal/localipc"
	"github.com/Duang777/drove/internal/session"
	"github.com/Duang777/drove/internal/store"
	"github.com/Duang777/drove/internal/version"
)

// Daemon 是常驻服务。
type Daemon struct {
	cfg            *config.Config
	now            func() time.Time
	retentionTicks <-chan time.Time
	pruneOutput    outputRetentionPruner
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
	if err := inspectStoragePaths(log, d.cfg.DataDir, d.cfg.DBPath); err != nil {
		return fmt.Errorf("daemon: storage: %w", err)
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

	deleted, err := d.pruneRetainedOutput(ctx, st, d.retentionNow())
	if err != nil {
		return fmt.Errorf("daemon: startup output retention: %w", err)
	}
	log.Info("startup output retention completed", "deleted", deleted)

	// 2. 在 API 对外可见前恢复会话投影。
	socketPath, err := filepath.Abs(localipc.SocketPath(d.cfg.DataDir))
	if err != nil {
		return fmt.Errorf("daemon: resolve local socket: %w", err)
	}
	recovered, err := bootstrapSessions(
		ctx,
		st,
		signalInjectionOption(d.cfg, socketPath),
	)
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
		Manager:        mgr,
		Hub:            hub,
		EventBuffer:    d.cfg.EventBuffer,
		ControlToken:   controlToken,
		AllowedOrigins: d.cfg.ConsoleOrigins,
	})

	localListener, err := localipc.Listen(d.cfg.DataDir)
	if err != nil {
		closeErr := mgr.Close()
		hub.Close()
		return errors.Join(
			fmt.Errorf("daemon: listen local socket: %w", err),
			closeErr,
		)
	}
	listeners := []net.Listener{localListener}
	var tcpListener net.Listener
	if !d.cfg.DisableTCP {
		tcpListener, err = net.Listen("tcp", d.cfg.APIBind)
		if err != nil {
			closeErr := mgr.Close()
			hub.Close()
			return errors.Join(
				fmt.Errorf("daemon: listen %s: %w", d.cfg.APIBind, err),
				localListener.Close(),
				closeErr,
			)
		}
		listeners = append(listeners, tcpListener)
	}
	if err := session.CleanupStaleSignalInjections(d.cfg.DataDir); err != nil {
		closeErr := mgr.Close()
		hub.Close()
		return errors.Join(
			fmt.Errorf("daemon: clean stale signal injection: %w", err),
			closeListeners(listeners),
			closeErr,
		)
	}
	signalHost := d.cfg.APIBind
	if tcpListener != nil {
		signalHost = tcpListener.Addr().String()
	}
	if err := mgr.ConfigureSignalOrigin(&url.URL{
		Scheme: "http",
		Host:   signalHost,
	}); err != nil {
		closeErr := mgr.Close()
		hub.Close()
		return errors.Join(
			fmt.Errorf("daemon: configure signal origin: %w", err),
			closeListeners(listeners),
			closeErr,
		)
	}

	stopRetention, retentionDone := d.startRetentionLoop(st, log)

	// 4. 启动 + 优雅关闭。
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	endpoints := []httpEndpoint{{
		listener: localListener,
		server: &http.Server{
			Handler:           srv.Handler(api.LocalAccess),
			ReadHeaderTimeout: 5 * time.Second,
		},
	}}
	if tcpListener != nil {
		endpoints = append(endpoints, httpEndpoint{
			listener: tcpListener,
			server: &http.Server{
				Handler:           srv.Handler(api.BrowserAccess),
				ReadHeaderTimeout: 5 * time.Second,
			},
		})
	}

	errCh := make(chan error, len(endpoints))
	for _, endpoint := range endpoints {
		endpoint := endpoint
		go func() {
			log.Info(
				"drove daemon listening",
				"network", endpoint.listener.Addr().Network(),
				"addr", endpoint.listener.Addr().String(),
				"version", version.Version,
			)
			if err := endpoint.server.Serve(endpoint.listener); err != nil &&
				!errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()
	}

	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
	case err := <-errCh:
		runErr = fmt.Errorf("daemon: serve: %w", err)
	case err := <-mgr.Fatal():
		runErr = fmt.Errorf("daemon: session event commit: %w", err)
	}

	stopRetention()
	<-retentionDone

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdownHTTPServers(shutdownCtx, endpoints); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("daemon: shutdown api: %w", err))
	}
	if err := mgr.Close(); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("daemon: shutdown sessions: %w", err))
	}
	hub.Close()
	log.Info("drove daemon stopped")
	return runErr
}

type httpEndpoint struct {
	listener net.Listener
	server   *http.Server
}

func closeListeners(listeners []net.Listener) error {
	var err error
	for _, listener := range listeners {
		err = errors.Join(err, listener.Close())
	}
	return err
}

func shutdownHTTPServers(
	ctx context.Context,
	endpoints []httpEndpoint,
) error {
	var err error
	for _, endpoint := range endpoints {
		err = errors.Join(err, endpoint.server.Shutdown(ctx))
	}
	return err
}

func bootstrapSessions(
	ctx context.Context,
	st *store.Store,
	options ...session.ManagerOption,
) (*session.BootstrapResult, error) {
	recovered, err := session.Bootstrap(ctx, adapter.NewRegistry(), st, options...)
	if err != nil {
		return nil, fmt.Errorf("daemon: bootstrap sessions: %w", err)
	}
	return recovered, nil
}
