package notify

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/event"
	eventstore "github.com/Duang777/drove/internal/store"
)

func TestProjectionCreatesMetadataOnlyDeliveryAndAdvancesCursor(t *testing.T) {
	ctx := context.Background()
	store := openInitializedStore(t, 0)
	base := time.Date(2026, time.October, 8, 13, 0, 0, 0, time.UTC)
	saveTestSubscription(t, store, "phone", base.Add(-time.Minute))

	created, err := store.project(ctx, projectionBatch{
		afterSeq: 0,
		events: []eventstore.EventRow{
			{
				Seq:              1,
				Timestamp:        base.Add(-time.Second),
				Type:             string(event.TypeOutputChunk),
				SessionID:        "agent-1",
				AgentID:          "agent-1",
				Payload:          `{"version":1,"offset":0,"len":20,"prompt":"SECRET-PROMPT"}`,
				OutputAttachment: []byte("SECRET-TERMINAL-DATA"),
			},
			{
				Seq:       2,
				Timestamp: base,
				Type:      string(event.TypeStateChanged),
				SessionID: "agent-1",
				AgentID:   "agent-1",
				From:      "working",
				To:        "blocked",
				Reason:    "SECRET-REASON",
				Payload:   `{"token":"SECRET-TOKEN"}`,
			},
		},
		metadata: map[string]AgentMetadata{
			"agent-1": {Name: "Build agent", Vendor: "generic"},
		},
		channels: map[ChannelKind]bool{ChannelWebPush: true},
		policy: Policy{
			Debounce:    30 * time.Second,
			PresenceTTL: 30 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("project events: %v", err)
	}
	if created != 1 {
		t.Fatalf("created deliveries = %d, want 1", created)
	}
	cursor, err := store.Cursor(ctx)
	if err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if cursor != 2 {
		t.Fatalf("cursor = %d, want 2", cursor)
	}

	var payload string
	var state string
	if err := store.db.QueryRow(
		`SELECT payload, state FROM notify_deliveries`,
	).Scan(&payload, &state); err != nil {
		t.Fatalf("read delivery: %v", err)
	}
	if state != deliveryPending {
		t.Fatalf("delivery state = %q, want pending", state)
	}
	for _, forbidden := range []string{
		"SECRET-PROMPT",
		"SECRET-TERMINAL-DATA",
		"SECRET-REASON",
		"SECRET-TOKEN",
		"payload",
		"screen",
		"output",
		"prompt",
		"token",
	} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("delivery payload contains %q: %s", forbidden, payload)
		}
	}

	var keys map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &keys); err != nil {
		t.Fatalf("decode delivery payload keys: %v", err)
	}
	gotKeys := make([]string, 0, len(keys))
	for key := range keys {
		gotKeys = append(gotKeys, key)
	}
	slices.Sort(gotKeys)
	wantKeys := []string{
		"agent_id",
		"agent_name",
		"blocked_seq",
		"deep_link",
		"id",
		"occurred_at",
		"state",
		"vendor",
	}
	if !slices.Equal(gotKeys, wantKeys) {
		t.Fatalf("notification keys = %v, want %v", gotKeys, wantKeys)
	}

	var notification Notification
	if err := json.Unmarshal([]byte(payload), &notification); err != nil {
		t.Fatalf("decode notification: %v", err)
	}
	if notification.AgentID != "agent-1" ||
		notification.AgentName != "Build agent" ||
		notification.Vendor != "generic" ||
		notification.BlockedSeq != 2 ||
		notification.DeepLink != "/?agent=agent-1" ||
		!notification.OccurredAt.Equal(base) {
		t.Fatalf("notification = %+v", notification)
	}
}

func TestProjectionDebouncesFlapsAndObsoletesPendingDelivery(t *testing.T) {
	ctx := context.Background()
	store := openInitializedStore(t, 0)
	base := time.Date(2026, time.October, 8, 14, 0, 0, 0, time.UTC)
	saveTestSubscription(t, store, "phone", base.Add(-time.Minute))

	rows := []eventstore.EventRow{
		stateRow(1, base, "working", "blocked"),
		stateRow(2, base.Add(5*time.Second), "blocked", "working"),
		stateRow(3, base.Add(20*time.Second), "working", "blocked"),
		stateRow(4, base.Add(25*time.Second), "blocked", "working"),
		stateRow(5, base.Add(31*time.Second), "working", "blocked"),
	}
	created, err := store.project(ctx, projectionBatch{
		afterSeq: 0,
		events:   rows,
		metadata: map[string]AgentMetadata{
			"agent-1": {Name: "Agent one", Vendor: "generic"},
		},
		channels: map[ChannelKind]bool{ChannelWebPush: true},
		policy: Policy{
			Debounce:    30 * time.Second,
			PresenceTTL: 30 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("project flap sequence: %v", err)
	}
	if created != 2 {
		t.Fatalf("created deliveries = %d, want 2", created)
	}

	rowsOut, err := store.db.Query(
		`SELECT source_seq, state FROM notify_deliveries ORDER BY source_seq`,
	)
	if err != nil {
		t.Fatalf("query deliveries: %v", err)
	}
	defer rowsOut.Close()
	var got []struct {
		seq   uint64
		state string
	}
	for rowsOut.Next() {
		var row struct {
			seq   uint64
			state string
		}
		if err := rowsOut.Scan(&row.seq, &row.state); err != nil {
			t.Fatalf("scan delivery: %v", err)
		}
		got = append(got, row)
	}
	if err := rowsOut.Err(); err != nil {
		t.Fatalf("iterate deliveries: %v", err)
	}
	if len(got) != 2 ||
		got[0].seq != 1 || got[0].state != deliveryObsolete ||
		got[1].seq != 5 || got[1].state != deliveryPending {
		t.Fatalf("deliveries = %+v", got)
	}
	blockedSeq, blocked, err := store.CurrentBlockedSeq(ctx, "agent-1")
	if err != nil {
		t.Fatalf("read current Blocked seq: %v", err)
	}
	if !blocked || blockedSeq != 5 {
		t.Fatalf("current Blocked seq = %d, %t; want 5, true", blockedSeq, blocked)
	}
}

func TestProjectionSuppressesBlockedWhileBrowserIsActive(t *testing.T) {
	ctx := context.Background()
	store := openInitializedStore(t, 0)
	base := time.Date(2026, time.October, 8, 15, 0, 0, 0, time.UTC)
	saveTestSubscription(t, store, "phone", base.Add(-time.Minute))

	created, err := store.project(ctx, projectionBatch{
		afterSeq: 0,
		events:   []eventstore.EventRow{stateRow(1, base, "working", "blocked")},
		metadata: map[string]AgentMetadata{},
		channels: map[ChannelKind]bool{ChannelWebPush: true},
		policy: Policy{
			Debounce:        30 * time.Second,
			QuietWhenActive: true,
			PresenceTTL:     30 * time.Second,
		},
		active: true,
	})
	if err != nil {
		t.Fatalf("project active Blocked event: %v", err)
	}
	if created != 0 {
		t.Fatalf("created deliveries = %d, want 0", created)
	}
	counts, err := store.deliveryCounts(ctx)
	if err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	if len(counts) != 0 {
		t.Fatalf("delivery counts = %v, want none", counts)
	}
	if cursor, err := store.Cursor(ctx); err != nil || cursor != 1 {
		t.Fatalf("cursor = %d, %v; want 1", cursor, err)
	}
}

func TestProjectionHonorsSubscriptionCreationCutoff(t *testing.T) {
	ctx := context.Background()
	store := openInitializedStore(t, 0)
	base := time.Date(2026, time.October, 8, 16, 0, 0, 0, time.UTC)
	saveTestSubscription(t, store, "before", base.Add(-time.Nanosecond))
	saveTestSubscription(t, store, "after", base.Add(time.Nanosecond))

	created, err := store.project(ctx, projectionBatch{
		afterSeq: 0,
		events:   []eventstore.EventRow{stateRow(1, base, "working", "blocked")},
		metadata: map[string]AgentMetadata{},
		channels: map[ChannelKind]bool{ChannelWebPush: true},
		policy: Policy{
			Debounce:    30 * time.Second,
			PresenceTTL: 30 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("project Blocked event: %v", err)
	}
	if created != 1 {
		t.Fatalf("created deliveries = %d, want 1", created)
	}
	var targetID string
	if err := store.db.QueryRow(
		`SELECT target_id FROM notify_deliveries`,
	).Scan(&targetID); err != nil {
		t.Fatalf("read target: %v", err)
	}
	if targetID != "before" {
		t.Fatalf("delivery target = %q, want before", targetID)
	}
}

func TestProjectionRollsBackCursorAndOutboxTogether(t *testing.T) {
	ctx := context.Background()
	store := openInitializedStore(t, 0)
	base := time.Date(2026, time.October, 8, 17, 0, 0, 0, time.UTC)
	saveTestSubscription(t, store, "phone", base.Add(-time.Minute))
	if _, err := store.db.Exec(`
		CREATE TRIGGER fail_delivery
		BEFORE INSERT ON notify_deliveries
		BEGIN
			SELECT RAISE(ABORT, 'injected delivery failure');
		END;
	`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}

	_, err := store.project(ctx, projectionBatch{
		afterSeq: 0,
		events:   []eventstore.EventRow{stateRow(1, base, "working", "blocked")},
		metadata: map[string]AgentMetadata{},
		channels: map[ChannelKind]bool{ChannelWebPush: true},
		policy: Policy{
			Debounce:    30 * time.Second,
			PresenceTTL: 30 * time.Second,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "injected delivery failure") {
		t.Fatalf("projection error = %v, want injected failure", err)
	}
	if cursor, err := store.Cursor(ctx); err != nil || cursor != 0 {
		t.Fatalf("cursor after rollback = %d, %v; want 0", cursor, err)
	}
	for _, table := range []string{"notify_agents", "notify_deliveries"} {
		var count int
		if err := store.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s rows after rollback = %d, want 0", table, count)
		}
	}
}

func TestDeliveryLeaseRecoversAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "notify.db")
	base := time.Date(2026, time.October, 8, 18, 0, 0, 0, time.UTC)
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open notification database: %v", err)
	}
	if _, err := store.InitializeCursor(ctx, 0, base); err != nil {
		t.Fatalf("initialize cursor: %v", err)
	}
	saveTestSubscription(t, store, "phone", base.Add(-time.Minute))
	if _, err := store.project(ctx, projectionBatch{
		afterSeq: 0,
		events:   []eventstore.EventRow{stateRow(1, base, "working", "blocked")},
		metadata: map[string]AgentMetadata{},
		channels: map[ChannelKind]bool{ChannelWebPush: true},
		policy: Policy{
			Debounce:    30 * time.Second,
			PresenceTTL: 30 * time.Second,
		},
	}); err != nil {
		t.Fatalf("project delivery: %v", err)
	}

	first, found, err := store.claimDelivery(
		ctx,
		ChannelWebPush,
		base,
		2*time.Second,
	)
	if err != nil || !found {
		t.Fatalf("first claim = %+v, %t, %v", first, found, err)
	}
	if first.Attempts != 1 || first.Subscription == nil {
		t.Fatalf("first claim = %+v", first)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close leased database: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen notification database: %v", err)
	}
	defer reopened.Close()
	if _, found, err := reopened.claimDelivery(
		ctx,
		ChannelWebPush,
		base.Add(time.Second),
		2*time.Second,
	); err != nil || found {
		t.Fatalf("claim before lease expiry = %t, %v; want false", found, err)
	}
	second, found, err := reopened.claimDelivery(
		ctx,
		ChannelWebPush,
		base.Add(3*time.Second),
		2*time.Second,
	)
	if err != nil || !found {
		t.Fatalf("claim after lease expiry = %+v, %t, %v", second, found, err)
	}
	if second.ID != first.ID || second.Attempts != 2 {
		t.Fatalf("recovered claim = %+v, want ID %q attempts 2", second, first.ID)
	}
}

func TestRevokingSubscriptionCancelsPendingDelivery(t *testing.T) {
	ctx := context.Background()
	store := openInitializedStore(t, 0)
	base := time.Date(2026, time.October, 8, 19, 0, 0, 0, time.UTC)
	saveTestSubscription(t, store, "phone", base.Add(-time.Minute))
	if _, err := store.project(ctx, projectionBatch{
		afterSeq: 0,
		events:   []eventstore.EventRow{stateRow(1, base, "working", "blocked")},
		metadata: map[string]AgentMetadata{},
		channels: map[ChannelKind]bool{ChannelWebPush: true},
		policy: Policy{
			Debounce:    30 * time.Second,
			PresenceTTL: 30 * time.Second,
		},
	}); err != nil {
		t.Fatalf("project delivery: %v", err)
	}
	revoked, err := store.RevokePushSubscription(
		ctx,
		"phone",
		base.Add(time.Second),
	)
	if err != nil || !revoked {
		t.Fatalf("revoke subscription = %t, %v", revoked, err)
	}
	counts, err := store.deliveryCounts(ctx)
	if err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	if counts[deliveryObsolete] != 1 || counts[deliveryPending] != 0 {
		t.Fatalf("delivery counts after revocation = %v", counts)
	}
}

func openInitializedStore(t *testing.T, cursor uint64) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "notify.db"))
	if err != nil {
		t.Fatalf("open notification database: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close notification database: %v", err)
		}
	})
	if _, err := store.InitializeCursor(
		context.Background(),
		cursor,
		time.Date(2026, time.October, 8, 12, 0, 0, 0, time.UTC),
	); err != nil {
		t.Fatalf("initialize notification cursor: %v", err)
	}
	return store
}

func saveTestSubscription(
	t *testing.T,
	store *Store,
	id string,
	createdAt time.Time,
) {
	t.Helper()
	if _, err := store.SavePushSubscription(
		context.Background(),
		PushSubscription{
			ID:         id,
			Endpoint:   "https://push.example.test/" + id,
			P256DH:     "public-" + id,
			Auth:       "auth-" + id,
			DeviceName: id,
			CreatedAt:  createdAt,
		},
	); err != nil {
		t.Fatalf("save test subscription %q: %v", id, err)
	}
}

func stateRow(
	seq uint64,
	at time.Time,
	from string,
	to string,
) eventstore.EventRow {
	return eventstore.EventRow{
		Seq:       seq,
		Timestamp: at,
		Type:      string(event.TypeStateChanged),
		SessionID: "agent-1",
		AgentID:   "agent-1",
		From:      from,
		To:        to,
	}
}
