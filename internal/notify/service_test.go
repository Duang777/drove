package notify

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/event"
	eventstore "github.com/Duang777/drove/internal/store"
)

func TestServiceStartsAtPublishedHeadAndPollingRepairsMissingWakeup(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	events, err := eventstore.Open(filepath.Join(dir, "drove.db"))
	if err != nil {
		t.Fatalf("open event store: %v", err)
	}
	t.Cleanup(func() {
		if err := events.Close(); err != nil {
			t.Errorf("close event store: %v", err)
		}
	})
	notifications, err := Open(filepath.Join(dir, "notify.db"))
	if err != nil {
		t.Fatalf("open notification store: %v", err)
	}
	t.Cleanup(func() {
		if err := notifications.Close(); err != nil {
			t.Errorf("close notification store: %v", err)
		}
	})

	base := time.Now().UTC().Add(-time.Second)
	historical := committedState(
		t,
		1,
		base,
		"working",
		"blocked",
	)
	appendCommitted(t, events, 0, historical)
	hub := event.NewHub(1)
	defer hub.Close()
	saveTestSubscription(t, notifications, "phone", base.Add(-time.Minute))

	channel := &recordingChannel{
		kind:       ChannelWebPush,
		deliveries: make(chan Delivery, 2),
		result:     SendResult{Outcome: SendDelivered},
	}
	service, err := NewService(ServiceOptions{
		Store:          notifications,
		Events:         events,
		Hub:            hub,
		Resolver:       fixedMetadataResolver(),
		Policy:         testPolicy(),
		Channels:       []Channel{channel},
		PollInterval:   10 * time.Millisecond,
		DisableWakeups: true,
		Logger:         slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("construct service: %v", err)
	}
	if err := service.Start(ctx); err != nil {
		t.Fatalf("start service: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := service.Close(closeCtx); err != nil {
			t.Errorf("close service: %v", err)
		}
	})

	select {
	case delivery := <-channel.deliveries:
		t.Fatalf("historical Blocked event produced delivery: %+v", delivery)
	case <-time.After(40 * time.Millisecond):
	}
	cursor, err := notifications.Cursor(ctx)
	if err != nil {
		t.Fatalf("read initialized cursor: %v", err)
	}
	if cursor != 1 {
		t.Fatalf("initialized cursor = %d, want published head 1", cursor)
	}

	leftBlocked := committedState(
		t,
		2,
		base.Add(2*time.Second),
		"blocked",
		"working",
	)
	blockedAgain := committedState(
		t,
		3,
		base.Add(3*time.Second),
		"working",
		"blocked",
	)
	appendCommitted(t, events, 1, leftBlocked, blockedAgain)
	if err := hub.PublishBatch([]event.Event{leftBlocked, blockedAgain}); err != nil {
		t.Fatalf("publish state changes: %v", err)
	}

	select {
	case delivery := <-channel.deliveries:
		if delivery.SourceSeq != 3 ||
			delivery.Notification.AgentName != "Resolved agent" ||
			delivery.Notification.Vendor != "generic" {
			t.Fatalf("delivery = %+v", delivery)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("polling did not repair the missing Hub wakeup")
	}
	if err := service.Drain(ctx); err != nil {
		t.Fatalf("drain planner: %v", err)
	}
	cursor, err = notifications.Cursor(ctx)
	if err != nil {
		t.Fatalf("read final cursor: %v", err)
	}
	if cursor != 3 {
		t.Fatalf("final cursor = %d, want 3", cursor)
	}
}

func TestServiceMetadataFailureUsesSafeFallback(t *testing.T) {
	var logs bytes.Buffer
	service := &Service{
		resolver: MetadataResolverFunc(func(
			context.Context,
			string,
		) (AgentMetadata, error) {
			return AgentMetadata{}, errors.New(
				"SECRET-ENDPOINT-and-private-metadata",
			)
		}),
		logger: slog.New(slog.NewTextHandler(&logs, nil)),
	}
	metadata := service.resolveMetadata(
		context.Background(),
		[]eventstore.EventRow{{
			Seq:       1,
			Timestamp: time.Now().UTC(),
			Type:      string(event.TypeStateChanged),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			From:      "working",
			To:        "blocked",
		}},
	)
	if got := metadata["agent-1"]; got.Name != "agent-1" ||
		got.Vendor != "unknown" {
		t.Fatalf("fallback metadata = %+v", got)
	}
	if strings.Contains(logs.String(), "SECRET-ENDPOINT") {
		t.Fatalf("metadata error leaked into logs: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "agent_id=agent-1") ||
		!strings.Contains(logs.String(), "error_type=") {
		t.Fatalf("metadata failure log lacks safe context: %s", logs.String())
	}
}

func TestServiceChannelFailureLogDoesNotExposeProviderSecrets(t *testing.T) {
	ctx := context.Background()
	notifications := openInitializedStore(t, 0)
	base := time.Date(2026, time.October, 8, 19, 0, 0, 0, time.UTC)
	saveTestSubscription(t, notifications, "phone", base.Add(-time.Minute))
	if _, err := notifications.project(ctx, projectionBatch{
		afterSeq: 0,
		events:   []eventstore.EventRow{stateRow(1, base, "working", "blocked")},
		metadata: map[string]AgentMetadata{
			"agent-1": {Name: "Agent one", Vendor: "generic"},
		},
		channels: map[ChannelKind]bool{ChannelWebPush: true},
		policy:   testPolicy(),
	}); err != nil {
		t.Fatalf("project delivery: %v", err)
	}

	var logs bytes.Buffer
	service := &Service{
		store:         notifications,
		leaseDuration: 30 * time.Second,
		minRetry:      time.Second,
		maxRetry:      time.Minute,
		now:           func() time.Time { return base.Add(time.Second) },
		logger:        slog.New(slog.NewJSONHandler(&logs, nil)),
	}
	channel := &failingChannel{
		kind: ChannelWebPush,
		err:  errors.New("SECRET-ENDPOINT SECRET-TOKEN"),
	}
	if !service.deliverReady(ctx, channel) {
		t.Fatal("delivery was not claimed")
	}
	if strings.Contains(logs.String(), "SECRET-") {
		t.Fatalf("channel failure log leaked provider data: %s", logs.String())
	}
	if !strings.Contains(logs.String(), `"error_type"`) ||
		!strings.Contains(logs.String(), `"delivery_id"`) {
		t.Fatalf("channel failure log lacks safe context: %s", logs.String())
	}
}

func TestServicePresenceExpiresBeforeLaterBlockedTransition(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	events, err := eventstore.Open(filepath.Join(dir, "drove.db"))
	if err != nil {
		t.Fatalf("open event store: %v", err)
	}
	t.Cleanup(func() {
		if err := events.Close(); err != nil {
			t.Errorf("close event store: %v", err)
		}
	})
	notifications, err := Open(filepath.Join(dir, "notify.db"))
	if err != nil {
		t.Fatalf("open notification store: %v", err)
	}
	t.Cleanup(func() {
		if err := notifications.Close(); err != nil {
			t.Errorf("close notification store: %v", err)
		}
	})

	base := time.Date(2026, time.October, 8, 20, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(base.UnixNano())
	now := func() time.Time {
		return time.Unix(0, clock.Load()).UTC()
	}
	hub := event.NewHub(0)
	defer hub.Close()
	saveTestSubscription(t, notifications, "phone", base.Add(-time.Minute))
	channel := &recordingChannel{
		kind:       ChannelWebPush,
		deliveries: make(chan Delivery, 1),
		result:     SendResult{Outcome: SendDelivered},
	}
	service, err := NewService(ServiceOptions{
		Store:    notifications,
		Events:   events,
		Hub:      hub,
		Resolver: fixedMetadataResolver(),
		Policy: Policy{
			Debounce:        30 * time.Second,
			QuietWhenActive: true,
			PresenceTTL:     10 * time.Second,
		},
		Channels:             []Channel{channel},
		PollInterval:         time.Hour,
		DeliveryPollInterval: 5 * time.Millisecond,
		Now:                  now,
		Logger:               slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("construct service: %v", err)
	}
	if err := service.Start(ctx); err != nil {
		t.Fatalf("start service: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := service.Close(closeCtx); err != nil {
			t.Errorf("close service: %v", err)
		}
	})

	service.RecordPresence()
	firstBlocked := committedState(t, 1, base, "working", "blocked")
	appendCommitted(t, events, 0, firstBlocked)
	if err := hub.Publish(firstBlocked); err != nil {
		t.Fatalf("publish first Blocked event: %v", err)
	}
	if err := service.Drain(ctx); err != nil {
		t.Fatalf("drain active-page event: %v", err)
	}
	select {
	case delivery := <-channel.deliveries:
		t.Fatalf("active page received delivery: %+v", delivery)
	case <-time.After(30 * time.Millisecond):
	}

	clock.Store(base.Add(31 * time.Second).UnixNano())
	working := committedState(
		t,
		2,
		base.Add(30*time.Second),
		"blocked",
		"working",
	)
	secondBlocked := committedState(
		t,
		3,
		base.Add(31*time.Second),
		"working",
		"blocked",
	)
	appendCommitted(t, events, 1, working, secondBlocked)
	if err := hub.PublishBatch([]event.Event{working, secondBlocked}); err != nil {
		t.Fatalf("publish second Blocked transition: %v", err)
	}
	if err := service.Drain(ctx); err != nil {
		t.Fatalf("drain expired presence event: %v", err)
	}
	select {
	case delivery := <-channel.deliveries:
		if delivery.SourceSeq != 3 {
			t.Fatalf("delivery source seq = %d, want 3", delivery.SourceSeq)
		}
	case <-time.After(time.Second):
		t.Fatal("expired presence still suppressed Blocked delivery")
	}
}

func TestServiceSendsPushTestAndRevokesRejectedTarget(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, time.October, 8, 21, 0, 0, 0, time.UTC)
	notifications := openInitializedStore(t, 0)
	saved, err := notifications.SavePushSubscription(ctx, PushSubscription{
		ID:         "subscription-1",
		Endpoint:   "https://push.example.test/device",
		P256DH:     "public-key",
		Auth:       "auth-secret",
		DeviceName: "Test phone",
		CreatedAt:  base,
	})
	if err != nil {
		t.Fatalf("save push subscription: %v", err)
	}
	channel := &recordingChannel{
		kind:       ChannelWebPush,
		deliveries: make(chan Delivery, 2),
		result:     SendResult{Outcome: SendDelivered},
	}
	service := &Service{
		store:    notifications,
		channels: map[ChannelKind]Channel{ChannelWebPush: channel},
		now:      func() time.Time { return base.Add(time.Minute) },
	}

	if err := service.SendPushTest(ctx, saved.ID); err != nil {
		t.Fatalf("send push test: %v", err)
	}
	delivery := <-channel.deliveries
	if delivery.TargetID != saved.ID ||
		delivery.Notification.State != "test" ||
		delivery.Notification.AgentName != "Drove" ||
		delivery.Notification.DeepLink != "/" {
		t.Fatalf("test delivery = %+v", delivery)
	}

	channel.result = SendResult{Outcome: SendRevokeTarget, Code: "gone"}
	if err := service.SendPushTest(ctx, saved.ID); !errors.Is(
		err,
		ErrPushSubscriptionRevoked,
	) {
		t.Fatalf("rejected push test error = %v, want revoked", err)
	}
	subscription, found, err := notifications.PushSubscription(ctx, saved.ID)
	if err != nil {
		t.Fatalf("load rejected subscription: %v", err)
	}
	if !found || subscription.RevokedAt == nil {
		t.Fatalf("rejected subscription = %+v, found=%t", subscription, found)
	}
	if err := service.SendPushTest(ctx, saved.ID); !errors.Is(
		err,
		ErrPushSubscriptionRevoked,
	) {
		t.Fatalf("revoked push test error = %v, want revoked", err)
	}
	if err := service.SendPushTest(ctx, "missing"); !errors.Is(
		err,
		ErrPushSubscriptionNotFound,
	) {
		t.Fatalf("missing push test error = %v, want not found", err)
	}
}

func TestServiceCloseReleasesInterruptedLease(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	events, err := eventstore.Open(filepath.Join(dir, "drove.db"))
	if err != nil {
		t.Fatalf("open event store: %v", err)
	}
	t.Cleanup(func() {
		if err := events.Close(); err != nil {
			t.Errorf("close event store: %v", err)
		}
	})
	notifications, err := Open(filepath.Join(dir, "notify.db"))
	if err != nil {
		t.Fatalf("open notification store: %v", err)
	}
	t.Cleanup(func() {
		if err := notifications.Close(); err != nil {
			t.Errorf("close notification store: %v", err)
		}
	})

	base := time.Now().UTC()
	hub := event.NewHub(0)
	defer hub.Close()
	saveTestSubscription(t, notifications, "phone", base.Add(-time.Minute))
	channel := &blockingChannel{
		started: make(chan struct{}),
	}
	service, err := NewService(ServiceOptions{
		Store:                notifications,
		Events:               events,
		Hub:                  hub,
		Policy:               testPolicy(),
		Channels:             []Channel{channel},
		PollInterval:         time.Hour,
		DeliveryPollInterval: 5 * time.Millisecond,
		Logger:               slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("construct service: %v", err)
	}
	if err := service.Start(ctx); err != nil {
		t.Fatalf("start service: %v", err)
	}

	blocked := committedState(t, 1, base, "working", "blocked")
	appendCommitted(t, events, 0, blocked)
	if err := hub.Publish(blocked); err != nil {
		t.Fatalf("publish Blocked event: %v", err)
	}
	if err := service.Drain(ctx); err != nil {
		t.Fatalf("drain planner: %v", err)
	}
	select {
	case <-channel.started:
	case <-time.After(time.Second):
		t.Fatal("delivery worker did not claim the outbox row")
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Close(closeCtx); err != nil {
		t.Fatalf("close service: %v", err)
	}
	counts, err := notifications.deliveryCounts(ctx)
	if err != nil {
		t.Fatalf("count deliveries after close: %v", err)
	}
	if counts[deliveryPending] != 1 || counts[deliveryLeased] != 0 {
		t.Fatalf("delivery counts after close = %v, want one pending", counts)
	}
}

type recordingChannel struct {
	kind       ChannelKind
	deliveries chan Delivery
	result     SendResult
}

func (c *recordingChannel) Kind() ChannelKind {
	return c.kind
}

func (c *recordingChannel) Send(
	_ context.Context,
	delivery Delivery,
) (SendResult, error) {
	c.deliveries <- delivery
	return c.result, nil
}

type blockingChannel struct {
	started chan struct{}
}

func (c *blockingChannel) Kind() ChannelKind {
	return ChannelWebPush
}

func (c *blockingChannel) Send(
	ctx context.Context,
	_ Delivery,
) (SendResult, error) {
	select {
	case <-c.started:
	default:
		close(c.started)
	}
	<-ctx.Done()
	return SendResult{}, ctx.Err()
}

type failingChannel struct {
	kind ChannelKind
	err  error
}

func (c *failingChannel) Kind() ChannelKind {
	return c.kind
}

func (c *failingChannel) Send(
	context.Context,
	Delivery,
) (SendResult, error) {
	return SendResult{}, c.err
}

func fixedMetadataResolver() MetadataResolver {
	return MetadataResolverFunc(func(
		context.Context,
		string,
	) (AgentMetadata, error) {
		return AgentMetadata{Name: "Resolved agent", Vendor: "generic"}, nil
	})
}

func testPolicy() Policy {
	return Policy{
		Debounce:    30 * time.Second,
		PresenceTTL: 30 * time.Second,
	}
}

func committedState(
	t *testing.T,
	seq uint64,
	at time.Time,
	from string,
	to string,
) event.Event {
	t.Helper()
	committed, err := event.Commit(
		seq,
		at,
		event.NewStateChangedDraft(
			"agent-1",
			"agent-1",
			from,
			to,
			"test",
			"",
		),
	)
	if err != nil {
		t.Fatalf("commit state event: %v", err)
	}
	return committed
}

func appendCommitted(
	t *testing.T,
	store *eventstore.Store,
	afterSeq uint64,
	events ...event.Event,
) {
	t.Helper()
	rows := make([]eventstore.EventRow, len(events))
	for index, committed := range events {
		rows[index] = eventstore.EventRow{
			Seq:       committed.Seq,
			Timestamp: committed.Timestamp,
			Type:      string(committed.Type),
			SessionID: committed.SessionID,
			AgentID:   committed.AgentID,
			From:      committed.From,
			To:        committed.To,
			Reason:    committed.Reason,
			Payload:   committed.StoredPayload(),
		}
	}
	if _, err := store.AppendEvents(
		context.Background(),
		afterSeq,
		rows,
	); err != nil {
		t.Fatalf("append committed events: %v", err)
	}
}
