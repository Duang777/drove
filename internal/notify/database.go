package notify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

const notificationSchemaVersion = 1

// Store owns mutable notification delivery state.
type Store struct {
	db *sql.DB
}

// Open opens or creates a private notification database.
func Open(path string) (*Store, error) {
	if err := prepareDatabaseFile(path); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=secure_delete(ON)",
		path,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("notify: open database %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &Store{db: db}
	if err := store.verifyPragmas(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func prepareDatabaseFile(path string) error {
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if !info.Mode().IsRegular() {
			return fmt.Errorf("notify: database %q must be a regular file", path)
		}
		if info.Mode().Perm() != 0o600 {
			return fmt.Errorf(
				"notify: database %q permissions are %04o, want 0600",
				path,
				info.Mode().Perm(),
			)
		}
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("notify: inspect database %q: %w", path, err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrExist) {
		return prepareDatabaseFile(path)
	}
	if err != nil {
		return fmt.Errorf("notify: create database %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("notify: close new database %q: %w", path, err)
	}
	return nil
}

func (s *Store) verifyPragmas() error {
	for _, pragma := range []struct {
		name string
		want int
	}{
		{name: "foreign_keys", want: 1},
		{name: "secure_delete", want: 1},
	} {
		var got int
		if err := s.db.QueryRow("PRAGMA " + pragma.name).Scan(&got); err != nil {
			return fmt.Errorf("notify: verify %s: %w", pragma.name, err)
		}
		if got != pragma.want {
			return fmt.Errorf(
				"notify: %s is %d, want %d",
				pragma.name,
				got,
				pragma.want,
			)
		}
	}
	return nil
}

func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("notify: read schema version: %w", err)
	}
	if version > notificationSchemaVersion {
		return fmt.Errorf(
			"notify: database schema version %d is newer than supported version %d",
			version,
			notificationSchemaVersion,
		)
	}
	if version == notificationSchemaVersion {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("notify: begin migration: %w", err)
	}
	defer tx.Rollback()

	if version < 1 {
		if _, err := tx.Exec(`
			CREATE TABLE notify_cursor (
				singleton      INTEGER PRIMARY KEY CHECK(singleton = 1),
				through_seq    INTEGER NOT NULL CHECK(through_seq >= 0),
				initialized_at TEXT NOT NULL
			);

			CREATE TABLE push_subscriptions (
				id          TEXT PRIMARY KEY,
				endpoint    TEXT NOT NULL UNIQUE,
				p256dh      TEXT NOT NULL,
				auth        TEXT NOT NULL,
				device_name TEXT NOT NULL,
				created_at  TEXT NOT NULL,
				revoked_at  TEXT
			);

			CREATE TABLE notify_agents (
				agent_id         TEXT PRIMARY KEY,
				state            TEXT NOT NULL,
				state_seq        INTEGER NOT NULL CHECK(state_seq > 0),
				blocked_seq      INTEGER NOT NULL CHECK(blocked_seq >= 0),
				last_notified_at TEXT
			);

			CREATE TABLE notify_deliveries (
				id              TEXT PRIMARY KEY,
				source_seq      INTEGER NOT NULL CHECK(source_seq > 0),
				agent_id        TEXT NOT NULL,
				channel         TEXT NOT NULL,
				target_id       TEXT NOT NULL,
				payload         TEXT NOT NULL,
				state           TEXT NOT NULL,
				attempts        INTEGER NOT NULL DEFAULT 0 CHECK(attempts >= 0),
				not_before      TEXT NOT NULL,
				lease_until     TEXT,
				last_error_code TEXT NOT NULL DEFAULT '',
				created_at      TEXT NOT NULL,
				delivered_at    TEXT,
				UNIQUE(source_seq, channel, target_id)
			);

			CREATE INDEX idx_notify_deliveries_ready
				ON notify_deliveries(state, not_before);
			CREATE INDEX idx_notify_deliveries_target
				ON notify_deliveries(channel, target_id, state);
		`); err != nil {
			return fmt.Errorf("notify: migrate schema version 1: %w", err)
		}
	}
	if _, err := tx.Exec(
		fmt.Sprintf(`PRAGMA user_version = %d`, notificationSchemaVersion),
	); err != nil {
		return fmt.Errorf("notify: set schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("notify: commit migration: %w", err)
	}
	return nil
}

// InitializeCursor creates the first notification cursor at publishedSeq.
// Existing cursors are returned unchanged.
func (s *Store) InitializeCursor(
	ctx context.Context,
	publishedSeq uint64,
	now time.Time,
) (uint64, error) {
	if now.IsZero() {
		return 0, errors.New("notify: cursor initialization time is required")
	}
	if _, err := s.db.ExecContext(
		ctx,
		`INSERT INTO notify_cursor (singleton, through_seq, initialized_at)
		 VALUES (1, ?, ?)
		 ON CONFLICT(singleton) DO NOTHING`,
		publishedSeq,
		formatTime(now),
	); err != nil {
		return 0, fmt.Errorf("notify: initialize cursor: %w", err)
	}
	return s.Cursor(ctx)
}

// Cursor returns the last event sequence projected into notification state.
func (s *Store) Cursor(ctx context.Context) (uint64, error) {
	var raw int64
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT through_seq FROM notify_cursor WHERE singleton = 1`,
	).Scan(&raw); err != nil {
		return 0, fmt.Errorf("notify: read cursor: %w", err)
	}
	if raw < 0 {
		return 0, fmt.Errorf("notify: cursor contains negative sequence %d", raw)
	}
	return uint64(raw), nil
}

// SavePushSubscription creates or refreshes a browser subscription.
func (s *Store) SavePushSubscription(
	ctx context.Context,
	subscription PushSubscription,
) (PushSubscription, error) {
	if err := subscription.validate(); err != nil {
		return PushSubscription{}, err
	}
	subscription.CreatedAt = subscription.CreatedAt.UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PushSubscription{}, fmt.Errorf("notify: begin subscription save: %w", err)
	}
	defer tx.Rollback()

	var existingID string
	err = tx.QueryRowContext(
		ctx,
		`SELECT id FROM push_subscriptions WHERE endpoint = ?`,
		subscription.Endpoint,
	).Scan(&existingID)
	switch {
	case err == nil:
		subscription.ID = existingID
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE push_subscriptions
			 SET p256dh = ?, auth = ?, device_name = ?, created_at = ?, revoked_at = NULL
			 WHERE id = ?`,
			subscription.P256DH,
			subscription.Auth,
			subscription.DeviceName,
			formatTime(subscription.CreatedAt),
			subscription.ID,
		); err != nil {
			return PushSubscription{}, fmt.Errorf("notify: refresh subscription: %w", err)
		}
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO push_subscriptions (
				id, endpoint, p256dh, auth, device_name, created_at, revoked_at
			 ) VALUES (?, ?, ?, ?, ?, ?, NULL)`,
			subscription.ID,
			subscription.Endpoint,
			subscription.P256DH,
			subscription.Auth,
			subscription.DeviceName,
			formatTime(subscription.CreatedAt),
		); err != nil {
			return PushSubscription{}, fmt.Errorf("notify: insert subscription: %w", err)
		}
	default:
		return PushSubscription{}, fmt.Errorf("notify: find subscription endpoint: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return PushSubscription{}, fmt.Errorf("notify: commit subscription save: %w", err)
	}
	subscription.RevokedAt = nil
	return subscription, nil
}

// PushSubscriptions returns all subscriptions, including revoked records.
func (s *Store) PushSubscriptions(
	ctx context.Context,
) ([]PushSubscription, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, endpoint, p256dh, auth, device_name, created_at, revoked_at
		 FROM push_subscriptions
		 ORDER BY created_at, id`,
	)
	if err != nil {
		return nil, fmt.Errorf("notify: list subscriptions: %w", err)
	}
	defer rows.Close()

	var subscriptions []PushSubscription
	for rows.Next() {
		subscription, err := scanPushSubscription(rows)
		if err != nil {
			return nil, err
		}
		subscriptions = append(subscriptions, subscription)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("notify: iterate subscriptions: %w", err)
	}
	return subscriptions, nil
}

// RevokePushSubscription revokes a browser target and cancels its unsent work.
func (s *Store) RevokePushSubscription(
	ctx context.Context,
	id string,
	now time.Time,
) (bool, error) {
	if id == "" {
		return false, errors.New("notify: subscription ID is required")
	}
	if now.IsZero() {
		return false, errors.New("notify: subscription revocation time is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("notify: begin subscription revocation: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(
		ctx,
		`UPDATE push_subscriptions
		 SET revoked_at = ?
		 WHERE id = ? AND revoked_at IS NULL`,
		formatTime(now),
		id,
	)
	if err != nil {
		return false, fmt.Errorf("notify: revoke subscription: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("notify: count revoked subscriptions: %w", err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE notify_deliveries
		 SET state = 'obsolete', lease_until = NULL
		 WHERE channel = ? AND target_id = ? AND state IN ('pending', 'leased')`,
		ChannelWebPush,
		id,
	); err != nil {
		return false, fmt.Errorf("notify: cancel revoked subscription deliveries: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("notify: commit subscription revocation: %w", err)
	}
	return changed > 0, nil
}

func scanPushSubscription(scanner interface{ Scan(...any) error }) (PushSubscription, error) {
	var subscription PushSubscription
	var createdAt string
	var revokedAt sql.NullString
	if err := scanner.Scan(
		&subscription.ID,
		&subscription.Endpoint,
		&subscription.P256DH,
		&subscription.Auth,
		&subscription.DeviceName,
		&createdAt,
		&revokedAt,
	); err != nil {
		return PushSubscription{}, fmt.Errorf("notify: scan subscription: %w", err)
	}
	parsedCreatedAt, err := parseTime(createdAt)
	if err != nil {
		return PushSubscription{}, fmt.Errorf(
			"notify: parse subscription %q creation time: %w",
			subscription.ID,
			err,
		)
	}
	subscription.CreatedAt = parsedCreatedAt
	if revokedAt.Valid {
		parsedRevokedAt, err := parseTime(revokedAt.String)
		if err != nil {
			return PushSubscription{}, fmt.Errorf(
				"notify: parse subscription %q revocation time: %w",
				subscription.ID,
				err,
			)
		}
		subscription.RevokedAt = &parsedRevokedAt
	}
	return subscription, nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse timestamp %q: %w", value, err)
	}
	return parsed, nil
}

// Close closes the notification database.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("notify: close database: %w", err)
	}
	return nil
}
