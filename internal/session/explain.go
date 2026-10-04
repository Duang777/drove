package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/store"
	"github.com/Duang777/drove/internal/term"
)

const (
	// DefaultExplainLimit is the number of durable decisions returned by default.
	DefaultExplainLimit = 50
	// MaxExplainLimit is the largest durable decision tail accepted by Manager.
	MaxExplainLimit = 200

	suppressedSignalReason = "detector authority rejected this signal"
)

// ErrInvalidExplainLimit reports an explain limit outside the supported range.
var ErrInvalidExplainLimit = errors.New("session: invalid explain limit")

// ExplainOptions controls the bounded durable history returned by Explain.
type ExplainOptions struct {
	Limit int
}

// ExplainEvent is a typed, bounded summary of one durable state decision.
type ExplainEvent struct {
	Seq                uint64               `json:"seq"`
	Timestamp          time.Time            `json:"timestamp"`
	Type               event.Type           `json:"type"`
	Source             agent.EvidenceSource `json:"source,omitempty"`
	Kind               detect.Kind          `json:"kind,omitempty"`
	Outcome            detect.Outcome       `json:"outcome,omitempty"`
	Rule               string               `json:"rule,omitempty"`
	Edge               agent.ScreenEdge     `json:"edge,omitempty"`
	Region             string               `json:"region,omitempty"`
	Evidence           string               `json:"evidence,omitempty"`
	SuppressionReason  string               `json:"suppression_reason,omitempty"`
	From               agent.State          `json:"from,omitempty"`
	To                 agent.State          `json:"to,omitempty"`
	Reason             string               `json:"reason,omitempty"`
	UnsupportedVersion *int                 `json:"unsupported_version,omitempty"`
}

// ExplainScreen is an ephemeral bounded view of the currently attached screen.
type ExplainScreen struct {
	CapturedAt time.Time `json:"captured_at"`
	Rows       []string  `json:"rows"`
	Truncated  bool      `json:"truncated"`
}

// Explanation combines current projections with a bounded durable decision tail.
type Explanation struct {
	AgentID    string            `json:"agent_id"`
	State      agent.State       `json:"state"`
	HookStatus detect.HookStatus `json:"hook_status"`
	Attached   bool              `json:"attached"`
	Events     []ExplainEvent    `json:"events"`
	Screen     *ExplainScreen    `json:"screen,omitempty"`
}

// Explain returns current state, recent durable decisions, and an optional
// ephemeral screen from the same currently attached terminal actor.
func (m *Manager) Explain(
	ctx context.Context,
	id agent.ID,
	options ExplainOptions,
) (Explanation, error) {
	limit, err := normalizeExplainLimit(options.Limit)
	if err != nil {
		return Explanation{}, err
	}
	a, ok := m.agent(id)
	if !ok {
		return Explanation{}, fmt.Errorf("%w: %q", ErrUnknownAgent, id)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rows, err := m.store.RecentEvents(
		ctx,
		string(id),
		[]event.Type{event.TypeAgentSignal, event.TypeStateChanged},
		limit,
	)
	if err != nil {
		return Explanation{}, fmt.Errorf("session: query explain history: %w", err)
	}
	events := make([]ExplainEvent, 0, len(rows))
	for _, row := range rows {
		summary, decodeErr := decodeExplainEvent(row)
		if decodeErr != nil {
			return Explanation{}, decodeErr
		}
		events = append(events, summary)
	}

	explanation := Explanation{
		AgentID:    string(id),
		State:      a.State(),
		HookStatus: detect.HookDetached,
		Events:     events,
	}

	m.mu.RLock()
	running, attached := m.sessions[id]
	if attached && !running.exitClaimed && running.terminal != nil {
		explanation.Attached = true
		if running.observer != nil {
			explanation.HookStatus = running.observer.Snapshot().HookStatus()
		}
		snapshot, capturedAt, available := running.terminal.snapshotWithCapturedAt()
		if available {
			screen, screenErr := explainScreen(snapshot, capturedAt)
			if screenErr != nil {
				m.mu.RUnlock()
				return Explanation{}, fmt.Errorf(
					"session: render explain screen for agent %q: %w",
					id,
					screenErr,
				)
			}
			explanation.Screen = &screen
		}
	}
	m.mu.RUnlock()
	return explanation, nil
}

func normalizeExplainLimit(limit int) (int, error) {
	if limit == 0 {
		return DefaultExplainLimit, nil
	}
	if limit < 0 || limit > MaxExplainLimit {
		return 0, fmt.Errorf(
			"%w: must be between 1 and %d",
			ErrInvalidExplainLimit,
			MaxExplainLimit,
		)
	}
	return limit, nil
}

func decodeExplainEvent(row store.EventRow) (ExplainEvent, error) {
	summary := ExplainEvent{
		Seq:       row.Seq,
		Timestamp: row.Timestamp,
		Type:      event.Type(row.Type),
	}
	switch summary.Type {
	case event.TypeAgentSignal:
		if err := decodeExplainSignal(row, &summary); err != nil {
			return ExplainEvent{}, err
		}
	case event.TypeStateChanged:
		summary.From = agent.State(row.From)
		summary.To = agent.State(row.To)
		summary.Reason = row.Reason
		if !agent.Valid(summary.From) || !agent.Valid(summary.To) {
			return ExplainEvent{}, fmt.Errorf(
				"session: explain state event at seq %d has invalid transition %q -> %q",
				row.Seq,
				row.From,
				row.To,
			)
		}
		if row.Payload != "" {
			if err := decodeExplainStateEvidence(row, &summary); err != nil {
				return ExplainEvent{}, err
			}
		}
	default:
		return ExplainEvent{}, fmt.Errorf(
			"session: explain event at seq %d has unexpected type %q",
			row.Seq,
			row.Type,
		)
	}
	return summary, nil
}

func decodeExplainSignal(row store.EventRow, summary *ExplainEvent) error {
	version, err := explainPayloadVersion(row, "signal")
	if err != nil {
		return err
	}
	switch version {
	case 1:
		var payload event.SignalPayloadV1
		if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
			return explainPayloadError(row, "decode signal", err)
		}
		if err := payload.Validate(); err != nil {
			return explainPayloadError(row, "validate signal", err)
		}
		applyExplainSignal(summary, payload)
	case 2:
		var payload event.SignalPayloadV2
		if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
			return explainPayloadError(row, "decode signal", err)
		}
		if err := payload.Validate(); err != nil {
			return explainPayloadError(row, "validate signal", err)
		}
		applyExplainSignal(summary, event.SignalPayloadV1(payload))
	case 3:
		var payload event.SignalPayloadV3
		if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
			return explainPayloadError(row, "decode signal", err)
		}
		if err := payload.Validate(); err != nil {
			return explainPayloadError(row, "validate signal", err)
		}
		applyExplainSignal(summary, payload.SignalPayloadV1)
		applyExplainScreen(summary, payload.Screen)
	default:
		summary.UnsupportedVersion = &version
	}
	return nil
}

func decodeExplainStateEvidence(
	row store.EventRow,
	summary *ExplainEvent,
) error {
	version, err := explainPayloadVersion(row, "state evidence")
	if err != nil {
		return err
	}
	switch version {
	case 1:
		var payload event.StateEvidencePayloadV1
		if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
			return explainPayloadError(row, "decode state evidence", err)
		}
		if err := payload.Validate(); err != nil {
			return explainPayloadError(row, "validate state evidence", err)
		}
		applyExplainStateEvidence(summary, payload)
	case 2:
		var payload event.StateEvidencePayloadV2
		if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
			return explainPayloadError(row, "decode state evidence", err)
		}
		if err := payload.Validate(); err != nil {
			return explainPayloadError(row, "validate state evidence", err)
		}
		applyExplainStateEvidence(
			summary,
			event.StateEvidencePayloadV1(payload),
		)
	case 3:
		var payload event.StateEvidencePayloadV3
		if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
			return explainPayloadError(row, "decode state evidence", err)
		}
		if err := payload.Validate(); err != nil {
			return explainPayloadError(row, "validate state evidence", err)
		}
		applyExplainStateEvidence(summary, payload.StateEvidencePayloadV1)
		applyExplainScreen(summary, payload.Screen)
	default:
		summary.UnsupportedVersion = &version
	}
	return nil
}

func explainPayloadVersion(row store.EventRow, name string) (int, error) {
	var version struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal([]byte(row.Payload), &version); err != nil {
		return 0, explainPayloadError(row, "decode "+name+" version", err)
	}
	if version.Version == 0 {
		return 0, fmt.Errorf(
			"session: explain %s at seq %d requires a payload version",
			name,
			row.Seq,
		)
	}
	return version.Version, nil
}

func explainPayloadError(row store.EventRow, action string, err error) error {
	return fmt.Errorf(
		"session: explain %s at seq %d: %w",
		action,
		row.Seq,
		err,
	)
}

func applyExplainSignal(
	summary *ExplainEvent,
	payload event.SignalPayloadV1,
) {
	summary.Source = agent.EvidenceSource(payload.Source)
	summary.Kind = detect.Kind(payload.Kind)
	summary.Outcome = detect.Outcome(payload.Outcome)
	summary.Evidence = payload.Evidence
	if summary.Outcome == detect.OutcomeSuppressed {
		summary.SuppressionReason = suppressedSignalReason
	}
}

func applyExplainStateEvidence(
	summary *ExplainEvent,
	payload event.StateEvidencePayloadV1,
) {
	summary.Source = agent.EvidenceSource(payload.Source)
	summary.Evidence = payload.Event
}

func applyExplainScreen(
	summary *ExplainEvent,
	screen *event.ScreenAttributionPayload,
) {
	if screen == nil {
		return
	}
	summary.Rule = screen.Rule
	summary.Edge = agent.ScreenEdge(screen.Edge)
	summary.Region = screen.Region
	summary.Evidence = screen.Evidence
}

func explainScreen(
	snapshot term.Snapshot,
	capturedAt time.Time,
) (ExplainScreen, error) {
	view, err := snapshot.View(term.DefaultViewOptions())
	if err != nil {
		return ExplainScreen{}, err
	}
	rows := view.Rows()
	if rows == nil {
		rows = make([]string, 0)
	}
	return ExplainScreen{
		CapturedAt: capturedAt,
		Rows:       rows,
		Truncated:  view.Truncated(),
	}, nil
}
