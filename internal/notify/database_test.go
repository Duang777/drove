package notify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenCreatesPrivateDatabaseAndMigratesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notify.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open notification database: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close notification database: %v", err)
	}

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("inspect notification database: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("notification database mode = %v, want regular 0600", info.Mode())
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen notification database: %v", err)
	}
	defer reopened.Close()
	var version int
	if err := reopened.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != notificationSchemaVersion {
		t.Fatalf(
			"schema version = %d, want %d",
			version,
			notificationSchemaVersion,
		)
	}
}

func TestOpenMigratesVersionOneDatabaseToActionTickets(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "notify.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open notification database: %v", err)
	}
	createdAt := time.Date(2026, time.October, 8, 10, 0, 0, 0, time.UTC)
	saveTestSubscription(t, store, "phone", createdAt)
	if _, err := store.db.Exec(`
		DROP TABLE action_tickets;
		PRAGMA user_version = 1;
	`); err != nil {
		t.Fatalf("downgrade database fixture to version 1: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close version 1 database fixture: %v", err)
	}

	migrated, err := Open(path)
	if err != nil {
		t.Fatalf("migrate version 1 notification database: %v", err)
	}
	defer migrated.Close()

	var version int
	if err := migrated.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read migrated schema version: %v", err)
	}
	if version != 2 {
		t.Fatalf("migrated schema version = %d, want 2", version)
	}
	var tableName string
	if err := migrated.db.QueryRow(
		`SELECT name FROM sqlite_schema
		 WHERE type = 'table' AND name = 'action_tickets'`,
	).Scan(&tableName); err != nil {
		t.Fatalf("find migrated action ticket table: %v", err)
	}
	subscription, found, err := migrated.PushSubscription(ctx, "phone")
	if err != nil {
		t.Fatalf("read subscription after migration: %v", err)
	}
	if !found ||
		subscription.ID != "phone" ||
		!subscription.CreatedAt.Equal(createdAt) {
		t.Fatalf("subscription after migration = %+v, found=%t", subscription, found)
	}
}

func TestOpenRejectsUnsafeDatabasePaths(t *testing.T) {
	dir := t.TempDir()
	directoryPath := filepath.Join(dir, "directory")
	if err := os.Mkdir(directoryPath, 0o700); err != nil {
		t.Fatalf("create directory fixture: %v", err)
	}
	if _, err := Open(directoryPath); err == nil ||
		!strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory error = %v, want regular file rejection", err)
	}

	permissivePath := filepath.Join(dir, "permissive.db")
	if err := os.WriteFile(permissivePath, nil, 0o600); err != nil {
		t.Fatalf("create permissive fixture: %v", err)
	}
	if err := os.Chmod(permissivePath, 0o644); err != nil {
		t.Fatalf("set permissive mode: %v", err)
	}
	if _, err := Open(permissivePath); err == nil ||
		!strings.Contains(err.Error(), "want 0600") {
		t.Fatalf("permissive file error = %v, want mode rejection", err)
	}

	target := filepath.Join(dir, "target.db")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatalf("create symlink target: %v", err)
	}
	link := filepath.Join(dir, "link.db")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("create symlink fixture: %v", err)
	}
	if _, err := Open(link); err == nil ||
		!strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink error = %v, want regular file rejection", err)
	}
}

func TestCursorAndSubscriptionSurviveRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "notify.db")
	base := time.Date(2026, time.October, 8, 11, 0, 0, 0, time.UTC)

	store, err := Open(path)
	if err != nil {
		t.Fatalf("open notification database: %v", err)
	}
	cursor, err := store.InitializeCursor(ctx, 41, base)
	if err != nil {
		t.Fatalf("initialize cursor: %v", err)
	}
	if cursor != 41 {
		t.Fatalf("initial cursor = %d, want 41", cursor)
	}
	saved, err := store.SavePushSubscription(ctx, PushSubscription{
		ID:         "subscription-1",
		Endpoint:   "https://push.example.test/device",
		P256DH:     "public-key",
		Auth:       "auth-secret",
		DeviceName: "Phone",
		CreatedAt:  base,
	})
	if err != nil {
		t.Fatalf("save subscription: %v", err)
	}
	if saved.ID != "subscription-1" {
		t.Fatalf("saved subscription ID = %q", saved.ID)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close first database: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen notification database: %v", err)
	}
	defer reopened.Close()
	cursor, err = reopened.InitializeCursor(ctx, 99, base.Add(time.Hour))
	if err != nil {
		t.Fatalf("resume cursor: %v", err)
	}
	if cursor != 41 {
		t.Fatalf("resumed cursor = %d, want stored value 41", cursor)
	}
	subscriptions, err := reopened.PushSubscriptions(ctx)
	if err != nil {
		t.Fatalf("list subscriptions: %v", err)
	}
	if len(subscriptions) != 1 ||
		subscriptions[0].Endpoint != "https://push.example.test/device" ||
		subscriptions[0].P256DH != "public-key" ||
		subscriptions[0].Auth != "auth-secret" ||
		subscriptions[0].DeviceName != "Phone" ||
		!subscriptions[0].CreatedAt.Equal(base) ||
		subscriptions[0].RevokedAt != nil {
		t.Fatalf("reopened subscription = %+v", subscriptions)
	}
}

func TestSavePushSubscriptionRefreshesStableTarget(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "notify.db"))
	if err != nil {
		t.Fatalf("open notification database: %v", err)
	}
	defer store.Close()

	base := time.Date(2026, time.October, 8, 12, 0, 0, 0, time.UTC)
	first, err := store.SavePushSubscription(ctx, PushSubscription{
		ID:         "subscription-1",
		Endpoint:   "https://push.example.test/device",
		P256DH:     "old-key",
		Auth:       "old-auth",
		DeviceName: "Old phone",
		CreatedAt:  base,
	})
	if err != nil {
		t.Fatalf("save first subscription: %v", err)
	}
	if revoked, err := store.RevokePushSubscription(
		ctx,
		first.ID,
		base.Add(time.Minute),
	); err != nil || !revoked {
		t.Fatalf("revoke subscription = %t, %v", revoked, err)
	}

	refreshed, err := store.SavePushSubscription(ctx, PushSubscription{
		ID:         "new-random-id",
		Endpoint:   first.Endpoint,
		P256DH:     "new-key",
		Auth:       "new-auth",
		DeviceName: "New phone",
		CreatedAt:  base.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("refresh subscription: %v", err)
	}
	if refreshed.ID != first.ID {
		t.Fatalf(
			"refreshed ID = %q, want stable ID %q",
			refreshed.ID,
			first.ID,
		)
	}
	subscriptions, err := store.PushSubscriptions(ctx)
	if err != nil {
		t.Fatalf("list subscriptions: %v", err)
	}
	if len(subscriptions) != 1 ||
		subscriptions[0].P256DH != "new-key" ||
		subscriptions[0].Auth != "new-auth" ||
		subscriptions[0].DeviceName != "New phone" ||
		subscriptions[0].RevokedAt != nil {
		t.Fatalf("refreshed subscription = %+v", subscriptions)
	}
}
