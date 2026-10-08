package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/event"
	eventstore "github.com/Duang777/drove/internal/store"
	"github.com/google/uuid"
)

type projectionBatch struct {
	afterSeq uint64
	events   []eventstore.EventRow
	metadata map[string]AgentMetadata
	channels map[ChannelKind]bool
	policy   Policy
	active   bool
}

type agentProjection struct {
	state          string
	stateSeq       uint64
	blockedSeq     uint64
	lastNotifiedAt *time.Time
}

func (s *Store) project(
	ctx context.Context,
	batch projectionBatch,
) (int, error) {
	if err := batch.policy.validate(); err != nil {
		return 0, err
	}
	for kind, enabled := range batch.channels {
		if enabled && !validChannelKind(kind) {
			return 0, fmt.Errorf("notify: invalid projection channel %q", kind)
		}
	}
	if err := validateProjectionRange(batch.afterSeq, batch.events); err != nil {
		return 0, err
	}
	if len(batch.events) == 0 {
		return 0, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("notify: begin event projection: %w", err)
	}
	defer tx.Rollback()

	currentCursor, err := cursorInTx(ctx, tx)
	if err != nil {
		return 0, err
	}
	if currentCursor != batch.afterSeq {
		return 0, fmt.Errorf(
			"notify: projection cursor changed from %d to %d",
			batch.afterSeq,
			currentCursor,
		)
	}

	created := 0
	for _, row := range batch.events {
		if event.Type(row.Type) != event.TypeStateChanged {
			continue
		}
		count, err := s.projectStateChange(ctx, tx, row, batch)
		if err != nil {
			return 0, fmt.Errorf(
				"notify: project state change at seq %d: %w",
				row.Seq,
				err,
			)
		}
		created += count
	}

	lastSeq := batch.events[len(batch.events)-1].Seq
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE notify_cursor SET through_seq = ? WHERE singleton = 1`,
		lastSeq,
	); err != nil {
		return 0, fmt.Errorf("notify: advance cursor to %d: %w", lastSeq, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("notify: commit event projection: %w", err)
	}
	return created, nil
}

func validateProjectionRange(afterSeq uint64, rows []eventstore.EventRow) error {
	if afterSeq > math.MaxInt64 {
		return fmt.Errorf("notify: projection cursor %d exceeds SQLite range", afterSeq)
	}
	for index, row := range rows {
		expected := afterSeq + uint64(index) + 1
		if expected == 0 || expected > math.MaxInt64 {
			return errors.New("notify: projection sequence exceeds SQLite range")
		}
		if row.Seq != expected {
			return fmt.Errorf(
				"notify: projection event at index %d has seq %d, want %d",
				index,
				row.Seq,
				expected,
			)
		}
		if row.Timestamp.IsZero() {
			return fmt.Errorf(
				"notify: projection event at seq %d has no timestamp",
				row.Seq,
			)
		}
	}
	return nil
}

func cursorInTx(ctx context.Context, tx *sql.Tx) (uint64, error) {
	var raw int64
	if err := tx.QueryRowContext(
		ctx,
		`SELECT through_seq FROM notify_cursor WHERE singleton = 1`,
	).Scan(&raw); err != nil {
		return 0, fmt.Errorf("notify: read projection cursor: %w", err)
	}
	if raw < 0 {
		return 0, fmt.Errorf("notify: projection cursor is negative: %d", raw)
	}
	return uint64(raw), nil
}

func (s *Store) projectStateChange(
	ctx context.Context,
	tx *sql.Tx,
	row eventstore.EventRow,
	batch projectionBatch,
) (int, error) {
	if row.AgentID == "" {
		return 0, errors.New("state change has no Agent ID")
	}
	if !agent.Valid(agent.State(row.To)) {
		return 0, fmt.Errorf("state change has invalid target state %q", row.To)
	}

	previous, found, err := readAgentProjection(ctx, tx, row.AgentID)
	if err != nil {
		return 0, err
	}
	if found && row.Seq <= previous.stateSeq {
		return 0, fmt.Errorf(
			"Agent %q state seq %d does not advance %d",
			row.AgentID,
			row.Seq,
			previous.stateSeq,
		)
	}

	leftBlocked := row.To != string(agent.StateBlocked) &&
		(row.From == string(agent.StateBlocked) ||
			found && previous.state == string(agent.StateBlocked))
	if leftBlocked {
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE notify_deliveries
			 SET state = 'obsolete', lease_until = NULL
			 WHERE agent_id = ? AND state IN ('pending', 'leased')`,
			row.AgentID,
		); err != nil {
			return 0, fmt.Errorf("mark stale deliveries obsolete: %w", err)
		}
	}

	var lastNotifiedAt *time.Time
	if found {
		lastNotifiedAt = previous.lastNotifiedAt
	}
	created := 0
	if row.To == string(agent.StateBlocked) &&
		row.From != string(agent.StateBlocked) &&
		!debounced(row.Timestamp, lastNotifiedAt, batch.policy.Debounce) &&
		!(batch.policy.QuietWhenActive && batch.active) {
		metadata := batch.metadata[row.AgentID]
		if metadata.Name == "" {
			metadata.Name = row.AgentID
		}
		if metadata.Vendor == "" {
			metadata.Vendor = "unknown"
		}
		notification := Notification{
			ID:         "blocked-" + strconv.FormatUint(row.Seq, 10),
			AgentID:    row.AgentID,
			AgentName:  metadata.Name,
			Vendor:     metadata.Vendor,
			State:      string(agent.StateBlocked),
			BlockedSeq: row.Seq,
			OccurredAt: row.Timestamp.UTC(),
			DeepLink:   "/?agent=" + url.QueryEscape(row.AgentID),
		}
		payload, err := json.Marshal(notification)
		if err != nil {
			return 0, fmt.Errorf("encode notification payload: %w", err)
		}
		targets, err := deliveryTargets(
			ctx,
			tx,
			row.Timestamp,
			batch.channels,
		)
		if err != nil {
			return 0, err
		}
		for _, target := range targets {
			result, err := tx.ExecContext(
				ctx,
				`INSERT INTO notify_deliveries (
					id, source_seq, agent_id, channel, target_id, payload,
					state, attempts, not_before, lease_until,
					last_error_code, created_at, delivered_at
				 ) VALUES (?, ?, ?, ?, ?, ?, 'pending', 0, ?, NULL, '', ?, NULL)
				 ON CONFLICT(source_seq, channel, target_id) DO NOTHING`,
				uuid.NewString(),
				row.Seq,
				row.AgentID,
				target.channel,
				target.id,
				string(payload),
				formatTime(row.Timestamp),
				formatTime(row.Timestamp),
			)
			if err != nil {
				return 0, fmt.Errorf(
					"create %s delivery for target %q: %w",
					target.channel,
					target.id,
					err,
				)
			}
			inserted, err := result.RowsAffected()
			if err != nil {
				return 0, fmt.Errorf(
					"count %s delivery for target %q: %w",
					target.channel,
					target.id,
					err,
				)
			}
			created += int(inserted)
		}
		if len(targets) > 0 {
			notifiedAt := row.Timestamp.UTC()
			lastNotifiedAt = &notifiedAt
		}
	}

	blockedSeq := uint64(0)
	if row.To == string(agent.StateBlocked) {
		blockedSeq = row.Seq
	}
	var lastNotified any
	if lastNotifiedAt != nil {
		lastNotified = formatTime(*lastNotifiedAt)
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO notify_agents (
			agent_id, state, state_seq, blocked_seq, last_notified_at
		 ) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(agent_id) DO UPDATE SET
			state = excluded.state,
			state_seq = excluded.state_seq,
			blocked_seq = excluded.blocked_seq,
			last_notified_at = excluded.last_notified_at`,
		row.AgentID,
		row.To,
		row.Seq,
		blockedSeq,
		lastNotified,
	); err != nil {
		return 0, fmt.Errorf("update Agent projection: %w", err)
	}
	return created, nil
}

func readAgentProjection(
	ctx context.Context,
	tx *sql.Tx,
	agentID string,
) (agentProjection, bool, error) {
	var projection agentProjection
	var rawStateSeq int64
	var rawBlockedSeq int64
	var lastNotified sql.NullString
	err := tx.QueryRowContext(
		ctx,
		`SELECT state, state_seq, blocked_seq, last_notified_at
		 FROM notify_agents WHERE agent_id = ?`,
		agentID,
	).Scan(
		&projection.state,
		&rawStateSeq,
		&rawBlockedSeq,
		&lastNotified,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return agentProjection{}, false, nil
	}
	if err != nil {
		return agentProjection{}, false, fmt.Errorf("read Agent projection: %w", err)
	}
	if rawStateSeq <= 0 || rawBlockedSeq < 0 {
		return agentProjection{}, false, errors.New("Agent projection has invalid sequence")
	}
	projection.stateSeq = uint64(rawStateSeq)
	projection.blockedSeq = uint64(rawBlockedSeq)
	if lastNotified.Valid {
		parsed, err := parseTime(lastNotified.String)
		if err != nil {
			return agentProjection{}, false, fmt.Errorf(
				"parse Agent last notification time: %w",
				err,
			)
		}
		projection.lastNotifiedAt = &parsed
	}
	return projection, true, nil
}

func deliveryTargets(
	ctx context.Context,
	tx *sql.Tx,
	occurredAt time.Time,
	channels map[ChannelKind]bool,
) ([]deliveryTarget, error) {
	var targets []deliveryTarget
	if channels[ChannelWebPush] {
		rows, err := tx.QueryContext(
			ctx,
			`SELECT id, created_at
			 FROM push_subscriptions
			 WHERE revoked_at IS NULL
			 ORDER BY created_at, id`,
		)
		if err != nil {
			return nil, fmt.Errorf("read push delivery targets: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var createdAtRaw string
			if err := rows.Scan(&id, &createdAtRaw); err != nil {
				return nil, fmt.Errorf("scan push delivery target: %w", err)
			}
			createdAt, err := parseTime(createdAtRaw)
			if err != nil {
				return nil, fmt.Errorf(
					"parse push target %q creation time: %w",
					id,
					err,
				)
			}
			if !createdAt.After(occurredAt) {
				targets = append(targets, deliveryTarget{
					channel: ChannelWebPush,
					id:      id,
				})
			}
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate push delivery targets: %w", err)
		}
	}
	if channels[ChannelNtfy] {
		targets = append(targets, deliveryTarget{
			channel: ChannelNtfy,
			id:      "configured",
		})
	}
	return targets, nil
}

func debounced(
	occurredAt time.Time,
	lastNotifiedAt *time.Time,
	window time.Duration,
) bool {
	if lastNotifiedAt == nil || window <= 0 {
		return false
	}
	return occurredAt.Before(lastNotifiedAt.Add(window))
}

// CurrentBlockedSeq returns the active Blocked transition sequence.
func (s *Store) CurrentBlockedSeq(
	ctx context.Context,
	agentID string,
) (uint64, bool, error) {
	var state string
	var raw int64
	err := s.db.QueryRowContext(
		ctx,
		`SELECT state, blocked_seq FROM notify_agents WHERE agent_id = ?`,
		agentID,
	).Scan(&state, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("notify: read current Blocked seq: %w", err)
	}
	if state != string(agent.StateBlocked) || raw <= 0 {
		return 0, false, nil
	}
	return uint64(raw), true, nil
}
