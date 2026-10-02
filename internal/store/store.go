// Package store 提供 SQLite 事件日志持久化（只追加，事件溯源）。
package store

import (
	"database/sql"
	"fmt"
	"time"

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
}

// Store 封装 SQLite 存储。
type Store struct {
	db *sql.DB
}

const schemaVersion = 1

// Open 打开（或创建）位于 path 的数据库，并执行迁移。
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %q: %w", path, err)
	}
	// 生产级配置：连接池（SQLite 单写者，池保持小）。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
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
	_, err := s.db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion))
	if err != nil {
		return fmt.Errorf("store: set version: %w", err)
	}
	return nil
}

// AppendEvent 追加一条事件。
func (s *Store) AppendEvent(ev EventRow) error {
	_, err := s.db.Exec(
		`INSERT INTO events (seq, ts, type, session_id, agent_id, from_state, to_state, reason, payload)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.Seq, ev.Timestamp.UTC().Format(time.RFC3339Nano), ev.Type, ev.SessionID, ev.AgentID,
		ev.From, ev.To, ev.Reason, ev.Payload,
	)
	if err != nil {
		return fmt.Errorf("store: append event: %w", err)
	}
	return nil
}

// Replay 按 seq 升序回放某会话的全部事件。
func (s *Store) Replay(sessionID string) ([]EventRow, error) {
	rows, err := s.db.Query(
		`SELECT seq, ts, type, session_id, agent_id, from_state, to_state, reason, payload
		 FROM events WHERE session_id = ? ORDER BY seq ASC`,
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
		if err := rows.Scan(&r.Seq, &ts, &r.Type, &r.SessionID, &r.AgentID,
			&r.From, &r.To, &r.Reason, &r.Payload); err != nil {
			return nil, fmt.Errorf("store: scan: %w", err)
		}
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

// LastSeq 返回当前最大事件序号（回放续接用）。
func (s *Store) LastSeq() (uint64, error) {
	var v sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(seq) FROM events`).Scan(&v); err != nil {
		return 0, fmt.Errorf("store: last seq: %w", err)
	}
	return uint64(v.Int64), nil
}

// Close 关闭数据库。
func (s *Store) Close() error {
	return s.db.Close()
}
