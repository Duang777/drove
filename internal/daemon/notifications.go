package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/api"
	"github.com/Duang777/drove/internal/config"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/notify"
	"github.com/Duang777/drove/internal/session"
	"github.com/Duang777/drove/internal/store"
)

const notificationPresenceTTL = 30 * time.Second

type notificationRuntime struct {
	store   *notify.Store
	service *notify.Service
	webPush *notify.WebPushChannel
	ntfy    bool
}

func startNotifications(
	cfg *config.Config,
	events *store.Store,
	hub *event.Hub,
	manager *session.Manager,
	logger *slog.Logger,
) (*notificationRuntime, error) {
	if !cfg.Notify.Enabled() {
		return nil, nil
	}

	notificationStore, err := notify.Open(
		filepath.Join(cfg.DataDir, "notify.db"),
	)
	if err != nil {
		return nil, fmt.Errorf("daemon: notification store: %w", err)
	}
	fail := func(startErr error) (*notificationRuntime, error) {
		return nil, errors.Join(startErr, notificationStore.Close())
	}

	var channels []notify.Channel
	var webPushChannel *notify.WebPushChannel
	if cfg.Notify.WebPush.Enabled {
		credentials, err := notify.LoadOrCreateVAPID(cfg.DataDir)
		if err != nil {
			return fail(fmt.Errorf("daemon: Web Push credentials: %w", err))
		}
		webPushChannel, err = notify.NewWebPushChannel(notify.WebPushOptions{
			Subject:     cfg.Notify.WebPush.VAPIDSubject,
			Credentials: credentials,
		})
		if err != nil {
			return fail(fmt.Errorf("daemon: Web Push channel: %w", err))
		}
		channels = append(channels, webPushChannel)
	}
	if cfg.Notify.Ntfy.Enabled {
		token, err := notify.LoadNtfyToken(cfg.Notify.Ntfy.TokenFile)
		if err != nil {
			return fail(fmt.Errorf("daemon: ntfy credentials: %w", err))
		}
		channel, err := notify.NewNtfyChannel(notify.NtfyOptions{
			BaseURL: cfg.Notify.Ntfy.BaseURL,
			Topic:   cfg.Notify.Ntfy.Topic,
			Token:   token,
		})
		if err != nil {
			return fail(fmt.Errorf("daemon: ntfy channel: %w", err))
		}
		channels = append(channels, channel)
	}

	service, err := notify.NewService(notify.ServiceOptions{
		Store:    notificationStore,
		Events:   events,
		Hub:      hub,
		Resolver: managerNotificationResolver{manager: manager},
		Policy: notify.Policy{
			Debounce:        time.Duration(cfg.Notify.DebounceSeconds) * time.Second,
			QuietWhenActive: cfg.Notify.QuietWhenActive,
			PresenceTTL:     notificationPresenceTTL,
		},
		Channels: channels,
		Logger:   logger,
	})
	if err != nil {
		return fail(fmt.Errorf("daemon: notification service: %w", err))
	}
	// The daemon owns service shutdown so the planner can drain after Run's
	// caller or signal context is canceled.
	if err := service.Start(context.Background()); err != nil {
		return fail(fmt.Errorf("daemon: start notification service: %w", err))
	}
	logger.Info(
		"notification service started",
		"web_push", webPushChannel != nil,
		"ntfy", cfg.Notify.Ntfy.Enabled,
	)
	return &notificationRuntime{
		store:   notificationStore,
		service: service,
		webPush: webPushChannel,
		ntfy:    cfg.Notify.Ntfy.Enabled,
	}, nil
}

func (r *notificationRuntime) drain(ctx context.Context) error {
	if r == nil {
		return nil
	}
	return r.service.Drain(ctx)
}

func (r *notificationRuntime) closeService(ctx context.Context) error {
	if r == nil {
		return nil
	}
	return r.service.Close(ctx)
}

func (r *notificationRuntime) closeStore() error {
	if r == nil {
		return nil
	}
	return r.store.Close()
}

func notificationAPIOptions(
	runtime *notificationRuntime,
	cfg config.NotifyConfig,
) api.NotificationOptions {
	options := api.NotificationOptions{
		Debounce:        time.Duration(cfg.DebounceSeconds) * time.Second,
		QuietWhenActive: cfg.QuietWhenActive,
	}
	if runtime == nil {
		return options
	}
	options.Service = runtime.service
	options.NtfyEnabled = runtime.ntfy
	if runtime.webPush != nil {
		options.WebPushPublicKey = runtime.webPush.PublicKey()
	}
	return options
}

type managerNotificationResolver struct {
	manager *session.Manager
}

func (r managerNotificationResolver) ResolveNotificationAgent(
	_ context.Context,
	agentID string,
) (notify.AgentMetadata, error) {
	status, err := r.manager.Status(agent.ID(agentID))
	if err != nil {
		return notify.AgentMetadata{}, fmt.Errorf(
			"daemon: resolve notification Agent %q: %w",
			agentID,
			err,
		)
	}
	return notify.AgentMetadata{
		Name:   status.Name,
		Vendor: status.Vendor,
	}, nil
}
