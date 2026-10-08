package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Duang777/drove/internal/event"
	eventstore "github.com/Duang777/drove/internal/store"
)

// EventSource reads immutable event envelopes from the session event store.
type EventSource interface {
	ReadEventRange(context.Context, uint64, uint64, int) ([]eventstore.EventRow, error)
}

// ServiceOptions configures the notification planner and delivery workers.
type ServiceOptions struct {
	Store                *Store
	Events               EventSource
	Hub                  *event.Hub
	Resolver             MetadataResolver
	Policy               Policy
	Channels             []Channel
	BatchSize            int
	PollInterval         time.Duration
	DeliveryPollInterval time.Duration
	LeaseDuration        time.Duration
	MinRetry             time.Duration
	MaxRetry             time.Duration
	DisableWakeups       bool
	Now                  func() time.Time
	Logger               *slog.Logger
}

// Service plans durable notifications and drains the delivery outbox.
type Service struct {
	store                *Store
	events               EventSource
	hub                  *event.Hub
	resolver             MetadataResolver
	policy               Policy
	channels             map[ChannelKind]Channel
	enabled              map[ChannelKind]bool
	batchSize            int
	pollInterval         time.Duration
	deliveryPollInterval time.Duration
	leaseDuration        time.Duration
	minRetry             time.Duration
	maxRetry             time.Duration
	disableWakeups       bool
	now                  func() time.Time
	logger               *slog.Logger

	presenceUntil atomic.Int64
	started       atomic.Bool
	stopped       atomic.Bool
	cancel        context.CancelFunc
	subscription  *event.Subscription
	syncRequests  chan chan error
	done          chan struct{}
	workers       sync.WaitGroup
}

// NewService validates dependencies and constructs a stopped service.
func NewService(options ServiceOptions) (*Service, error) {
	if options.Store == nil {
		return nil, errors.New("notify: notification store is required")
	}
	if options.Events == nil {
		return nil, errors.New("notify: event source is required")
	}
	if options.Hub == nil {
		return nil, errors.New("notify: event Hub is required")
	}
	if options.Policy.PresenceTTL == 0 {
		options.Policy.PresenceTTL = 30 * time.Second
	}
	if err := options.Policy.validate(); err != nil {
		return nil, err
	}
	if options.BatchSize == 0 {
		options.BatchSize = 256
	}
	if options.BatchSize < 1 || options.BatchSize > 4096 {
		return nil, errors.New("notify: batch size must be between 1 and 4096")
	}
	if options.PollInterval == 0 {
		options.PollInterval = time.Second
	}
	if options.PollInterval < 0 {
		return nil, errors.New("notify: poll interval cannot be negative")
	}
	if options.DeliveryPollInterval == 0 {
		options.DeliveryPollInterval = time.Second
	}
	if options.DeliveryPollInterval < 0 {
		return nil, errors.New("notify: delivery poll interval cannot be negative")
	}
	if options.LeaseDuration == 0 {
		options.LeaseDuration = 30 * time.Second
	}
	if options.LeaseDuration < time.Second {
		return nil, errors.New("notify: lease duration must be at least one second")
	}
	if options.MinRetry == 0 {
		options.MinRetry = time.Second
	}
	if options.MaxRetry == 0 {
		options.MaxRetry = 5 * time.Minute
	}
	if options.MinRetry <= 0 || options.MaxRetry < options.MinRetry {
		return nil, errors.New("notify: retry bounds are invalid")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}

	channels := make(map[ChannelKind]Channel, len(options.Channels))
	enabled := make(map[ChannelKind]bool, len(options.Channels))
	for _, channel := range options.Channels {
		if channel == nil {
			return nil, errors.New("notify: channel cannot be nil")
		}
		kind := channel.Kind()
		if !validChannelKind(kind) {
			return nil, fmt.Errorf("notify: channel has invalid kind %q", kind)
		}
		if _, exists := channels[kind]; exists {
			return nil, fmt.Errorf("notify: duplicate channel %q", kind)
		}
		channels[kind] = channel
		enabled[kind] = true
	}

	return &Service{
		store:                options.Store,
		events:               options.Events,
		hub:                  options.Hub,
		resolver:             options.Resolver,
		policy:               options.Policy,
		channels:             channels,
		enabled:              enabled,
		batchSize:            options.BatchSize,
		pollInterval:         options.PollInterval,
		deliveryPollInterval: options.DeliveryPollInterval,
		leaseDuration:        options.LeaseDuration,
		minRetry:             options.MinRetry,
		maxRetry:             options.MaxRetry,
		disableWakeups:       options.DisableWakeups,
		now:                  options.Now,
		logger:               options.Logger,
		syncRequests:         make(chan chan error),
		done:                 make(chan struct{}),
	}, nil
}

// Start initializes the durable cursor and starts planner and worker loops.
func (s *Service) Start(parent context.Context) error {
	if !s.started.CompareAndSwap(false, true) {
		return errors.New("notify: service already started")
	}
	if _, err := s.store.InitializeCursor(
		parent,
		s.hub.LastSeq(),
		s.now().UTC(),
	); err != nil {
		s.started.Store(false)
		return fmt.Errorf("notify: start service: %w", err)
	}

	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	if !s.disableWakeups {
		s.subscription = s.hub.Subscribe(1)
	}
	s.workers.Add(1)
	go s.runPlanner(ctx)
	for _, channel := range s.channels {
		s.workers.Add(1)
		go s.runWorker(ctx, channel)
	}
	return nil
}

func (s *Service) runPlanner(ctx context.Context) {
	defer s.workers.Done()
	defer close(s.done)

	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()
	var wakes <-chan event.Event
	if s.subscription != nil {
		wakes = s.subscription.C()
	}

	for {
		select {
		case _, open := <-wakes:
			if !open {
				wakes = nil
				continue
			}
			s.syncAndLog(ctx)
		case <-ticker.C:
			s.syncAndLog(ctx)
		case result := <-s.syncRequests:
			result <- s.syncPublished(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (s *Service) syncAndLog(ctx context.Context) {
	if err := s.syncPublished(ctx); err != nil &&
		!errors.Is(err, context.Canceled) {
		s.logger.Error("notification planner failed")
	}
}

func (s *Service) syncPublished(ctx context.Context) error {
	for {
		cursor, err := s.store.Cursor(ctx)
		if err != nil {
			return err
		}
		published := s.hub.LastSeq()
		if cursor >= published {
			return nil
		}
		rows, err := s.events.ReadEventRange(
			ctx,
			cursor,
			published,
			s.batchSize,
		)
		if err != nil {
			return fmt.Errorf(
				"notify: read published events after %d through %d: %w",
				cursor,
				published,
				err,
			)
		}
		if len(rows) == 0 {
			return fmt.Errorf(
				"notify: published event range after %d through %d is empty",
				cursor,
				published,
			)
		}
		metadata := s.resolveMetadata(ctx, rows)
		active := s.now().Before(
			time.Unix(0, s.presenceUntil.Load()),
		)
		if _, err := s.store.project(ctx, projectionBatch{
			afterSeq: cursor,
			events:   rows,
			metadata: metadata,
			channels: s.enabled,
			policy:   s.policy,
			active:   active,
		}); err != nil {
			return err
		}
	}
}

func (s *Service) resolveMetadata(
	ctx context.Context,
	rows []eventstore.EventRow,
) map[string]AgentMetadata {
	metadata := make(map[string]AgentMetadata)
	for _, row := range rows {
		if event.Type(row.Type) != event.TypeStateChanged ||
			row.To != "blocked" {
			continue
		}
		if _, exists := metadata[row.AgentID]; exists {
			continue
		}
		if s.resolver == nil {
			metadata[row.AgentID] = AgentMetadata{
				Name:   row.AgentID,
				Vendor: "unknown",
			}
			continue
		}
		resolved, err := s.resolver.ResolveNotificationAgent(ctx, row.AgentID)
		if err != nil {
			s.logger.Warn(
				"notification Agent metadata lookup failed",
				"agent_id",
				row.AgentID,
				"error_type",
				fmt.Sprintf("%T", err),
			)
			resolved = AgentMetadata{
				Name:   row.AgentID,
				Vendor: "unknown",
			}
		}
		metadata[row.AgentID] = resolved
	}
	return metadata
}

func (s *Service) runWorker(ctx context.Context, channel Channel) {
	defer s.workers.Done()

	ticker := time.NewTicker(s.deliveryPollInterval)
	defer ticker.Stop()
	for {
		if sent := s.deliverReady(ctx, channel); sent {
			continue
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

func (s *Service) deliverReady(ctx context.Context, channel Channel) bool {
	delivery, found, err := s.store.claimDelivery(
		ctx,
		channel.Kind(),
		s.now().UTC(),
		s.leaseDuration,
	)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			s.logger.Error(
				"notification delivery claim failed",
				"channel",
				channel.Kind(),
			)
		}
		return false
	}
	if !found {
		return false
	}

	result, sendErr := channel.Send(ctx, delivery)
	if sendErr != nil {
		result = SendResult{
			Outcome: SendRetry,
			Code:    "send_error",
		}
		s.logger.Warn(
			"notification delivery attempt failed",
			"delivery_id",
			delivery.ID,
			"agent_id",
			delivery.AgentID,
			"channel",
			delivery.Channel,
			"error_type",
			fmt.Sprintf("%T", sendErr),
		)
	} else if err := result.validate(); err != nil {
		result = SendResult{
			Outcome: SendPermanent,
			Code:    "invalid_result",
		}
		s.logger.Error(
			"notification channel returned an invalid result",
			"delivery_id",
			delivery.ID,
			"channel",
			delivery.Channel,
		)
	}

	now := s.now().UTC()
	switch result.Outcome {
	case SendDelivered:
		_, err = s.store.markDelivered(ctx, delivery.ID, now)
	case SendRetry:
		delay := result.RetryAfter
		if delay == 0 {
			delay = s.retryDelay(delivery.Attempts)
		}
		if delay > s.maxRetry {
			delay = s.maxRetry
		}
		_, err = s.store.markRetry(
			ctx,
			delivery.ID,
			now.Add(delay),
			result.Code,
		)
	case SendPermanent:
		_, err = s.store.markPermanent(ctx, delivery.ID, result.Code)
	case SendRevokeTarget:
		if delivery.Channel == ChannelWebPush {
			_, err = s.store.RevokePushSubscription(
				ctx,
				delivery.TargetID,
				now,
			)
		} else {
			_, err = s.store.markPermanent(ctx, delivery.ID, result.Code)
		}
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		s.logger.Error(
			"notification delivery result could not be recorded",
			"delivery_id",
			delivery.ID,
			"channel",
			delivery.Channel,
		)
	}
	return true
}

func (s *Service) retryDelay(attempt int) time.Duration {
	exponent := attempt - 1
	if exponent < 0 {
		exponent = 0
	}
	if exponent > 30 {
		exponent = 30
	}
	multiplier := time.Duration(1 << exponent)
	delay := s.minRetry * multiplier
	if delay < s.minRetry || delay > s.maxRetry {
		return s.maxRetry
	}
	return delay
}

// Drain waits until the notification cursor reaches the current published head.
func (s *Service) Drain(ctx context.Context) error {
	if !s.started.Load() || s.stopped.Load() {
		return errors.New("notify: service is not running")
	}
	result := make(chan error, 1)
	select {
	case s.syncRequests <- result:
	case <-ctx.Done():
		return fmt.Errorf("notify: request planner drain: %w", ctx.Err())
	case <-s.done:
		return errors.New("notify: planner stopped before drain")
	}
	select {
	case err := <-result:
		if err != nil {
			return fmt.Errorf("notify: drain planner: %w", err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("notify: wait for planner drain: %w", ctx.Err())
	case <-s.done:
		return errors.New("notify: planner stopped during drain")
	}
}

// RecordPresence suppresses new notifications while a visible page is active.
func (s *Service) RecordPresence() {
	until := s.now().Add(s.policy.PresenceTTL).UnixNano()
	for {
		current := s.presenceUntil.Load()
		if current >= until || s.presenceUntil.CompareAndSwap(current, until) {
			return
		}
	}
}

// SavePushSubscription persists one browser target.
func (s *Service) SavePushSubscription(
	ctx context.Context,
	subscription PushSubscription,
) (PushSubscription, error) {
	return s.store.SavePushSubscription(ctx, subscription)
}

// PushSubscriptions returns all durable browser targets.
func (s *Service) PushSubscriptions(
	ctx context.Context,
) ([]PushSubscription, error) {
	return s.store.PushSubscriptions(ctx)
}

// RevokePushSubscription revokes one browser target.
func (s *Service) RevokePushSubscription(
	ctx context.Context,
	id string,
) (bool, error) {
	return s.store.RevokePushSubscription(ctx, id, s.now().UTC())
}

// CurrentBlockedSeq returns the current Blocked transition for an Agent.
func (s *Service) CurrentBlockedSeq(
	ctx context.Context,
	agentID string,
) (uint64, bool, error) {
	return s.store.CurrentBlockedSeq(ctx, agentID)
}

// Close stops all loops and returns outstanding leases to the queue.
func (s *Service) Close(ctx context.Context) error {
	if !s.started.Load() {
		return nil
	}
	if !s.stopped.CompareAndSwap(false, true) {
		select {
		case <-s.done:
			return nil
		case <-ctx.Done():
			return fmt.Errorf("notify: wait for service close: %w", ctx.Err())
		}
	}
	if s.subscription != nil {
		s.hub.Unsubscribe(s.subscription)
	}
	s.cancel()

	waited := make(chan struct{})
	go func() {
		s.workers.Wait()
		close(waited)
	}()
	select {
	case <-waited:
	case <-ctx.Done():
		return fmt.Errorf("notify: wait for workers: %w", ctx.Err())
	}
	if err := s.store.releaseLeases(ctx); err != nil {
		return fmt.Errorf("notify: close service: %w", err)
	}
	return nil
}
