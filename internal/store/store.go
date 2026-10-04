// Package store 提供 SQLite 事件日志持久化（只追加，事件溯源）。
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/Duang777/drove/internal/event"

	_ "modernc.org/sqlite"
)

// EventRow 是 events 表的一行（与 event.Event 一一对应）。
type EventRow struct {
	Seq       uint64
	Timestamp time.Time
	Type      string
	SessionID string
	AgentID   string
	From      string
	To        string
	Reason    string
	Payload   string

	OutputAttachment        []byte `json:"-"`
	OutputAttachmentPresent bool   `json:"-"`
}

// Store 封装 SQLite 存储。
type Store struct {
	db                  *sql.DB
	readers             *sql.DB
	retentionGeneration atomic.Uint64
}

const schemaVersion = 2

// Open 打开（或创建）位于 path 的数据库，并执行迁移。
func Open(path string) (*Store, error) {
	if err := prepareDatabaseFile(path); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)",
		path,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %q: %w", path, err)
	}
	// 生产级配置：连接池（SQLite 单写者，池保持小）。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	s := &Store{db: db}
	if err := s.enableForeignKeys(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.enableSecureDelete(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	readers, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: open reader pool for %q: %w", path, err)
	}
	readers.SetMaxOpenConns(4)
	readers.SetMaxIdleConns(4)
	s.readers = readers
	return s, nil
}

func prepareDatabaseFile(path string) error {
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if !info.Mode().IsRegular() {
			return fmt.Errorf("store: database %q must be a regular file", path)
		}
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("store: inspect database %q: %w", path, err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrExist) {
		return prepareDatabaseFile(path)
	}
	if err != nil {
		return fmt.Errorf("store: create database %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("store: close new database %q: %w", path, err)
	}
	return nil
}

func (s *Store) enableForeignKeys() error {
	if _, err := s.db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("store: enable foreign keys: %w", err)
	}
	var enabled int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		return fmt.Errorf("store: verify foreign keys: %w", err)
	}
	if enabled != 1 {
		return errors.New("store: foreign keys remain disabled")
	}
	return nil
}

func (s *Store) enableSecureDelete() error {
	if _, err := s.db.Exec(`PRAGMA secure_delete = ON`); err != nil {
		return fmt.Errorf("store: enable secure delete: %w", err)
	}
	var enabled int
	if err := s.db.QueryRow(`PRAGMA secure_delete`).Scan(&enabled); err != nil {
		return fmt.Errorf("store: verify secure delete: %w", err)
	}
	if enabled != 1 {
		return errors.New("store: secure delete remains disabled")
	}
	return nil
}

// migrate 执行版本化迁移。
func (s *Store) migrate() error {
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return fmt.Errorf("store: read version: %w", err)
	}
	if v >= schemaVersion {
		return nil
	}

	if v < 1 {
		_, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS events (
				seq        INTEGER PRIMARY KEY,
				ts         TEXT    NOT NULL,
				type       TEXT    NOT NULL,
				session_id TEXT    NOT NULL,
				agent_id   TEXT    NOT NULL DEFAULT '',
				from_state TEXT    NOT NULL DEFAULT '',
				to_state   TEXT    NOT NULL DEFAULT '',
				reason     TEXT    NOT NULL DEFAULT '',
				payload    TEXT    NOT NULL DEFAULT ''
			);
			CREATE INDEX IF NOT EXISTS idx_events_session ON events(session_id, seq);
			CREATE INDEX IF NOT EXISTS idx_events_agent   ON events(agent_id, seq);
		`)
		if err != nil {
			return fmt.Errorf("store: migrate v1: %w", err)
		}
	}
	if v < 2 {
		_, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS output_chunks (
				event_seq INTEGER PRIMARY KEY,
				data      BLOB NOT NULL CHECK(length(data) BETWEEN 1 AND 32768),
				FOREIGN KEY(event_seq) REFERENCES events(seq) ON DELETE CASCADE
			);
		`)
		if err != nil {
			return fmt.Errorf("store: migrate v2: %w", err)
		}
	}
	_, err := s.db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion))
	if err != nil {
		return fmt.Errorf("store: set version: %w", err)
	}
	return nil
}

// AppendEvent 追加一条事件。
func (s *Store) AppendEvent(ev EventRow) error {
	if err := validateOutputAttachment(ev); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("store: begin append event: %w", err)
	}
	defer tx.Rollback()
	if err := appendEventRow(context.Background(), tx, ev); err != nil {
		return fmt.Errorf("store: append event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit append event: %w", err)
	}
	return nil
}

// AppendEvents 在一个事务内追加连续事件，并防止基于过期最大序号写入。
func (s *Store) AppendEvents(ctx context.Context, expectedLastSeq uint64, events []EventRow) (uint64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return expectedLastSeq, fmt.Errorf("store: begin event batch: %w", err)
	}
	defer tx.Rollback()

	var current sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(seq) FROM events`).Scan(&current); err != nil {
		return expectedLastSeq, fmt.Errorf("store: read event batch boundary: %w", err)
	}
	if current.Valid && current.Int64 < 0 {
		return expectedLastSeq, fmt.Errorf("store: invalid negative event seq %d", current.Int64)
	}
	currentLastSeq := uint64(current.Int64)
	if currentLastSeq != expectedLastSeq {
		return expectedLastSeq, fmt.Errorf(
			"store: stale event batch boundary: current seq %d, expected %d",
			currentLastSeq,
			expectedLastSeq,
		)
	}

	for i, event := range events {
		expectedSeq := expectedLastSeq + uint64(i) + 1
		if event.Seq != expectedSeq {
			return expectedLastSeq, fmt.Errorf(
				"store: event batch seq %d at index %d, want %d",
				event.Seq,
				i,
				expectedSeq,
			)
		}
		if err := validateOutputAttachment(event); err != nil {
			return expectedLastSeq, fmt.Errorf(
				"store: validate event batch at seq %d: %w",
				event.Seq,
				err,
			)
		}
	}
	for _, event := range events {
		if err := appendEventRow(ctx, tx, event); err != nil {
			return expectedLastSeq, fmt.Errorf("store: append event batch at seq %d: %w", event.Seq, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return expectedLastSeq, fmt.Errorf("store: commit event batch: %w", err)
	}
	return expectedLastSeq + uint64(len(events)), nil
}

func validateOutputAttachment(row EventRow) error {
	if row.Type == string(event.TypeOutputChunk) {
		if len(row.OutputAttachment) == 0 {
			return errors.New("store: output chunk requires an attachment")
		}
		return nil
	}
	if row.OutputAttachment != nil {
		return fmt.Errorf("store: event type %q cannot have an output attachment", row.Type)
	}
	return nil
}

func appendEventRow(ctx context.Context, tx *sql.Tx, row EventRow) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO events (seq, ts, type, session_id, agent_id, from_state, to_state, reason, payload)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.Seq, row.Timestamp.UTC().Format(time.RFC3339Nano), row.Type, row.SessionID,
		row.AgentID, row.From, row.To, row.Reason, row.Payload,
	); err != nil {
		return fmt.Errorf("append envelope: %w", err)
	}
	if row.Type != string(event.TypeOutputChunk) {
		return nil
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO output_chunks (event_seq, data) VALUES (?, ?)`,
		row.Seq,
		row.OutputAttachment,
	); err != nil {
		return fmt.Errorf("append output attachment: %w", err)
	}
	return nil
}

// ScanEvents 按全局 seq 升序访问全部事件，并返回最后一个序号。
func (s *Store) ScanEvents(ctx context.Context, visit func(EventRow) error) (uint64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, ts, type, session_id, agent_id, from_state, to_state, reason, payload
		 FROM events ORDER BY seq ASC`,
	)
	if err != nil {
		return 0, fmt.Errorf("store: scan events: %w", err)
	}
	defer rows.Close()

	var lastSeq uint64
	for rows.Next() {
		var row EventRow
		var rawSeq int64
		var timestamp string
		if err := rows.Scan(&rawSeq, &timestamp, &row.Type, &row.SessionID, &row.AgentID,
			&row.From, &row.To, &row.Reason, &row.Payload); err != nil {
			return lastSeq, fmt.Errorf("store: scan event row at seq %d: %w", rawSeq, err)
		}
		if rawSeq <= 0 {
			return lastSeq, fmt.Errorf("store: invalid event seq %d", rawSeq)
		}
		row.Seq = uint64(rawSeq)
		if lastSeq != 0 && row.Seq <= lastSeq {
			return lastSeq, fmt.Errorf("store: event seq %d is not greater than %d", row.Seq, lastSeq)
		}
		parsedTimestamp, err := time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			return lastSeq, fmt.Errorf("store: parse event timestamp at seq %d: %w", row.Seq, err)
		}
		row.Timestamp = parsedTimestamp
		if err := visit(row); err != nil {
			return lastSeq, fmt.Errorf("store: visit event at seq %d: %w", row.Seq, err)
		}
		lastSeq = row.Seq
	}
	if err := rows.Err(); err != nil {
		return lastSeq, fmt.Errorf("store: iterate events: %w", err)
	}
	return lastSeq, nil
}

// Replay 按 seq 升序回放某会话的全部事件。
func (s *Store) Replay(sessionID string) ([]EventRow, error) {
	rows, err := s.db.Query(
		`SELECT e.seq, e.ts, e.type, e.session_id, e.agent_id, e.from_state,
		        e.to_state, e.reason, e.payload, o.data,
		        CASE WHEN o.event_seq IS NULL THEN 0 ELSE 1 END
		 FROM events AS e
		 LEFT JOIN output_chunks AS o ON o.event_seq = e.seq
		 WHERE e.session_id = ?
		 ORDER BY e.seq ASC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("store: replay: %w", err)
	}
	defer rows.Close()

	var out []EventRow
	for rows.Next() {
		var r EventRow
		var ts string
		var attachmentPresent int
		if err := rows.Scan(&r.Seq, &ts, &r.Type, &r.SessionID, &r.AgentID,
			&r.From, &r.To, &r.Reason, &r.Payload, &r.OutputAttachment,
			&attachmentPresent); err != nil {
			return nil, fmt.Errorf("store: scan: %w", err)
		}
		r.OutputAttachmentPresent = attachmentPresent == 1
		t, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return nil, fmt.Errorf("store: parse ts: %w", err)
		}
		r.Timestamp = t
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate: %w", err)
	}
	return out, nil
}

// RecentEvents returns the newest matching event envelopes in chronological
// order. It never loads output attachments.
func (s *Store) RecentEvents(
	ctx context.Context,
	sessionID string,
	types []event.Type,
	limit int,
) ([]EventRow, error) {
	if sessionID == "" {
		return nil, errors.New("store: recent events require a session ID")
	}
	if len(types) == 0 {
		return nil, errors.New("store: recent events require at least one event type")
	}
	if limit <= 0 {
		return nil, errors.New("store: recent events limit must be positive")
	}

	typeNames := make([]string, len(types))
	seen := make(map[event.Type]struct{}, len(types))
	for index, typ := range types {
		if !validEventType(typ) {
			return nil, fmt.Errorf("store: recent events contain invalid type %q", typ)
		}
		if _, exists := seen[typ]; exists {
			return nil, fmt.Errorf("store: recent events contain duplicate type %q", typ)
		}
		seen[typ] = struct{}{}
		typeNames[index] = string(typ)
	}
	encodedTypes, err := json.Marshal(typeNames)
	if err != nil {
		return nil, fmt.Errorf("store: encode recent event types: %w", err)
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT seq, ts, type, session_id, agent_id, from_state, to_state, reason, payload
		 FROM (
			SELECT seq, ts, type, session_id, agent_id, from_state, to_state, reason, payload
			FROM events
			WHERE session_id = ?
			  AND type IN (SELECT value FROM json_each(?))
			ORDER BY seq DESC
			LIMIT ?
		 )
		 ORDER BY seq ASC`,
		sessionID,
		string(encodedTypes),
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("store: query recent events: %w", err)
	}
	defer rows.Close()

	out := make([]EventRow, 0, limit)
	for rows.Next() {
		var row EventRow
		var rawSeq int64
		var timestamp string
		if err := rows.Scan(
			&rawSeq,
			&timestamp,
			&row.Type,
			&row.SessionID,
			&row.AgentID,
			&row.From,
			&row.To,
			&row.Reason,
			&row.Payload,
		); err != nil {
			return nil, fmt.Errorf("store: scan recent event: %w", err)
		}
		if rawSeq <= 0 {
			return nil, fmt.Errorf("store: invalid recent event seq %d", rawSeq)
		}
		row.Seq = uint64(rawSeq)
		row.Timestamp, err = time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			return nil, fmt.Errorf(
				"store: parse recent event timestamp at seq %d: %w",
				row.Seq,
				err,
			)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate recent events: %w", err)
	}
	return out, nil
}

func validEventType(typ event.Type) bool {
	switch typ {
	case event.TypeStateChanged,
		event.TypeOutput,
		event.TypeOutputChunk,
		event.TypeError,
		event.TypeSessionLifecycle,
		event.TypeAgentInput,
		event.TypeAgentSignal,
		event.TypeAgentResized:
		return true
	default:
		return false
	}
}

// LastSeq 返回当前最大事件序号（回放续接用）。
func (s *Store) LastSeq() (uint64, error) {
	var v sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(seq) FROM events`).Scan(&v); err != nil {
		return 0, fmt.Errorf("store: last seq: %w", err)
	}
	return uint64(v.Int64), nil
}

// PruneOutputAttachments removes retained output bodies older than cutoff.
// Event envelopes and their global sequences remain unchanged.
func (s *Store) PruneOutputAttachments(
	ctx context.Context,
	cutoff time.Time,
) (int64, error) {
	result, err := s.db.ExecContext(
		ctx,
		`DELETE FROM output_chunks
		 WHERE event_seq IN (
			SELECT o.event_seq
			FROM output_chunks AS o
			JOIN events AS e ON e.seq = o.event_seq
			WHERE e.type = ? AND julianday(e.ts) < julianday(?)
		 )`,
		string(event.TypeOutputChunk),
		cutoff.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, fmt.Errorf("store: prune output attachments: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: count pruned output attachments: %w", err)
	}
	if deleted > 0 {
		s.retentionGeneration.Add(1)
	}

	var busy, logFrames, checkpointedFrames int
	if err := s.db.QueryRowContext(
		ctx,
		`PRAGMA wal_checkpoint(TRUNCATE)`,
	).Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		return deleted, fmt.Errorf("store: checkpoint output retention: %w", err)
	}
	if busy != 0 {
		return deleted, fmt.Errorf(
			"store: checkpoint output retention remained busy with %d WAL frames",
			logFrames,
		)
	}
	return deleted, nil
}

// OutputRetentionGeneration changes after output attachments are deleted.
func (s *Store) OutputRetentionGeneration() uint64 {
	return s.retentionGeneration.Load()
}

// Close 关闭数据库。
func (s *Store) Close() error {
	var readerErr error
	if s.readers != nil {
		readerErr = s.readers.Close()
	}
	writerErr := s.db.Close()
	if readerErr != nil || writerErr != nil {
		return fmt.Errorf(
			"store: close: %w",
			errors.Join(readerErr, writerErr),
		)
	}
	return nil
}
