package session

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/store"
)

const (
	restartInterruptionError = "session interrupted by daemon restart; previous PTY is not reconnectable"
	restartStopReason        = "recovered after daemon restart without PTY"
)

// RecoveryReport 汇总一次启动投影恢复的结果。
type RecoveryReport struct {
	ScannedEvents  int
	Sessions       int
	Interrupted    int
	LegacyMetadata int
	PartialHistory int
	LastSeq        uint64
}

type recoveryProjector struct {
	sessions      map[string]*sessionDraft
	lastSeq       uint64
	gapGeneration uint64
	report        RecoveryReport
}

type sessionDraft struct {
	id                     string
	name                   string
	vendor                 string
	runMode                agent.RunMode
	state                  agent.State
	lastError              string
	createdAt              time.Time
	updatedAt              time.Time
	firstSeq               uint64
	lastStateGapGeneration uint64
	hasCreated             bool
	hasState               bool
}

type recoveryPlan struct {
	Snapshots      []agent.RestoreSnapshot
	Reconciliation []store.EventRow
	Report         RecoveryReport
}

func newRecoveryProjector() *recoveryProjector {
	return &recoveryProjector{sessions: make(map[string]*sessionDraft)}
}

func (p *recoveryProjector) Apply(row store.EventRow) error {
	if row.Seq == 0 {
		return projectionError(row, "sequence is zero")
	}
	if p.lastSeq != 0 {
		if row.Seq <= p.lastSeq {
			return projectionError(row, "sequence is not greater than %d", p.lastSeq)
		}
		if row.Seq-p.lastSeq > 1 {
			p.gapGeneration++
		}
	}
	p.lastSeq = row.Seq
	p.report.ScannedEvents++

	switch event.Type(row.Type) {
	case event.TypeSessionLifecycle:
		return p.applyLifecycle(row)
	case event.TypeStateChanged:
		return p.applyState(row)
	case event.TypeError:
		return p.applyError(row)
	case event.TypeAgentInput:
		if row.SessionID == "" {
			return projectionError(row, "input event has empty session ID")
		}
		return validateAgentID(row)
	case event.TypeOutput:
		if row.SessionID == "" {
			return nil
		}
		if err := validateAgentID(row); err != nil {
			return err
		}
		p.draft(row)
		return nil
	default:
		return projectionError(row, "unknown event type %q", row.Type)
	}
}

func (p *recoveryProjector) applyLifecycle(row store.EventRow) error {
	if row.SessionID == "" {
		return projectionError(row, "lifecycle event has empty session ID")
	}
	if err := validateAgentID(row); err != nil {
		return err
	}
	if row.Reason != "created" {
		return projectionError(row, "unknown lifecycle reason %q", row.Reason)
	}

	draft := p.draft(row)
	if draft.hasCreated {
		return projectionError(row, "duplicate creation metadata")
	}
	if draft.hasState {
		return projectionError(row, "creation metadata follows state history")
	}

	var metadata createdPayload
	if err := json.Unmarshal([]byte(row.Payload), &metadata); err != nil {
		return projectionWrapError(row, "decode creation metadata", err)
	}
	if metadata.Version != 1 {
		return projectionError(row, "unsupported creation metadata version %d", metadata.Version)
	}
	if strings.TrimSpace(metadata.Name) == "" {
		return projectionError(row, "creation metadata name is empty")
	}
	if strings.TrimSpace(metadata.Vendor) == "" {
		return projectionError(row, "creation metadata vendor is empty")
	}
	if metadata.Mode == nil {
		draft.runMode = agent.RunModeOneshot
	} else if !agent.ValidRunMode(*metadata.Mode) {
		return projectionError(row, "creation metadata mode %q is invalid", *metadata.Mode)
	} else {
		draft.runMode = *metadata.Mode
	}

	draft.name = metadata.Name
	draft.vendor = metadata.Vendor
	draft.state = agent.StatePending
	draft.updatedAt = row.Timestamp
	draft.hasCreated = true
	return nil
}

func (p *recoveryProjector) applyState(row store.EventRow) error {
	if row.SessionID == "" {
		return projectionError(row, "state event has empty session ID")
	}
	if err := validateAgentID(row); err != nil {
		return err
	}

	from := agent.State(row.From)
	to := agent.State(row.To)
	if !agent.Valid(from) {
		return projectionError(row, "invalid from state %q", row.From)
	}
	if !agent.Valid(to) {
		return projectionError(row, "invalid to state %q", row.To)
	}
	if !agent.CanTransition(from, to) {
		return projectionError(row, "invalid state transition %s -> %s", from, to)
	}

	draft := p.draft(row)
	if !draft.hasState {
		draft.state = from
	} else if draft.state != from {
		if p.gapGeneration == draft.lastStateGapGeneration {
			return projectionError(
				row,
				"state chain mismatch: projected %s, event starts from %s",
				draft.state,
				from,
			)
		}
		draft.state = from
		p.report.PartialHistory++
	}
	draft.state = to
	draft.updatedAt = row.Timestamp
	draft.lastStateGapGeneration = p.gapGeneration
	draft.hasState = true
	return nil
}

func (p *recoveryProjector) applyError(row store.EventRow) error {
	if row.SessionID == "" {
		return nil
	}
	if err := validateAgentID(row); err != nil {
		return err
	}

	draft := p.draft(row)
	if row.Payload != "" {
		draft.lastError = row.Payload
		draft.updatedAt = row.Timestamp
	}
	return nil
}

func (p *recoveryProjector) draft(row store.EventRow) *sessionDraft {
	if draft, ok := p.sessions[row.SessionID]; ok {
		return draft
	}
	draft := &sessionDraft{
		id:        row.SessionID,
		createdAt: row.Timestamp,
		firstSeq:  row.Seq,
	}
	p.sessions[row.SessionID] = draft
	return draft
}

func (p *recoveryProjector) Finish(recoveryTime time.Time) (recoveryPlan, error) {
	drafts := make([]*sessionDraft, 0, len(p.sessions))
	for _, draft := range p.sessions {
		if draft.hasCreated || draft.hasState {
			drafts = append(drafts, draft)
		}
	}
	sort.Slice(drafts, func(i, j int) bool {
		if drafts[i].firstSeq == drafts[j].firstSeq {
			return drafts[i].id < drafts[j].id
		}
		return drafts[i].firstSeq < drafts[j].firstSeq
	})

	plan := recoveryPlan{
		Snapshots: make([]agent.RestoreSnapshot, 0, len(drafts)),
		Report:    p.report,
	}
	nextSeq := p.lastSeq
	for _, draft := range drafts {
		if !draft.hasCreated {
			draft.name = draft.id
			draft.vendor = "unknown"
			draft.runMode = agent.RunModeOneshot
			plan.Report.LegacyMetadata++
		}

		state := draft.state
		lastError := draft.lastError
		updatedAt := draft.updatedAt
		if state != agent.StateStopped {
			if state != agent.StateDone {
				nextSeq++
				plan.Reconciliation = append(plan.Reconciliation, store.EventRow{
					Seq:       nextSeq,
					Timestamp: recoveryTime,
					Type:      string(event.TypeError),
					SessionID: draft.id,
					AgentID:   draft.id,
					Payload:   restartInterruptionError,
				})
				lastError = restartInterruptionError
				plan.Report.Interrupted++
			}
			nextSeq++
			plan.Reconciliation = append(plan.Reconciliation, store.EventRow{
				Seq:       nextSeq,
				Timestamp: recoveryTime,
				Type:      string(event.TypeStateChanged),
				SessionID: draft.id,
				AgentID:   draft.id,
				From:      string(state),
				To:        string(agent.StateStopped),
				Reason:    restartStopReason,
			})
			state = agent.StateStopped
			updatedAt = recoveryTime
		}
		if updatedAt.Before(draft.createdAt) {
			updatedAt = draft.createdAt
		}
		plan.Snapshots = append(plan.Snapshots, agent.RestoreSnapshot{
			ID:        agent.ID(draft.id),
			Name:      draft.name,
			Vendor:    draft.vendor,
			RunMode:   draft.runMode,
			State:     state,
			LastError: lastError,
			CreatedAt: draft.createdAt,
			UpdatedAt: updatedAt,
		})
	}
	plan.Report.Sessions = len(plan.Snapshots)
	plan.Report.LastSeq = nextSeq
	return plan, nil
}

func validateAgentID(row store.EventRow) error {
	if row.AgentID != "" && row.AgentID != row.SessionID {
		return projectionError(
			row,
			"agent ID %q does not match session ID",
			row.AgentID,
		)
	}
	return nil
}

func projectionError(row store.EventRow, format string, args ...any) error {
	message := fmt.Sprintf(format, args...)
	if row.SessionID == "" {
		return fmt.Errorf("session: project event seq %d: %s", row.Seq, message)
	}
	return fmt.Errorf(
		"session: project event seq %d session %q: %s",
		row.Seq,
		row.SessionID,
		message,
	)
}

func projectionWrapError(row store.EventRow, message string, err error) error {
	if row.SessionID == "" {
		return fmt.Errorf("session: project event seq %d: %s: %w", row.Seq, message, err)
	}
	return fmt.Errorf(
		"session: project event seq %d session %q: %s: %w",
		row.Seq,
		row.SessionID,
		message,
		err,
	)
}
