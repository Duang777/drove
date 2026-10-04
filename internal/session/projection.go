package session

import (
	"encoding/json"
	"fmt"
	"path/filepath"
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
	ScannedEvents                int
	Sessions                     int
	Interrupted                  int
	LegacyMetadata               int
	PartialHistory               int
	UnknownSignalPayloadVersions int
	UnknownStateEvidenceVersions int
	LastSeq                      uint64
}

type recoveryProjector struct {
	sessions      map[string]*sessionDraft
	lastSeq       uint64
	gapGeneration uint64
	lastResumeSeq uint64
	lastResumeID  string
	report        RecoveryReport
}

type sessionDraft struct {
	id                     string
	name                   string
	vendor                 string
	runMode                agent.RunMode
	hookPolicy             agent.HookPolicy
	signalInjection        agent.SignalInjectionMode
	injectionStatus        agent.SignalInjectionStatus
	injectionReason        agent.SignalInjectionReason
	state                  agent.State
	lastError              string
	lastTransition         *agent.Evidence
	createdAt              time.Time
	updatedAt              time.Time
	firstSeq               uint64
	lastStateGapGeneration uint64
	vendorSessionRef       string
	workingDir             string
	workspaceRemoved       bool
	hasCreated             bool
	hasState               bool
}

type recoveryPlan struct {
	Snapshots         []agent.RestoreSnapshot
	VendorSessionRefs map[string]string
	WorkingDirs       map[string]string
	ResumeOnStart     map[string]bool
	WorkspaceRemoved  map[string]bool
	Reconciliation    []store.EventRow
	Report            RecoveryReport
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
			return projectionError(row, "%s event has empty session ID", row.Type)
		}
		return validateAgentID(row)
	case event.TypeAgentSignal:
		return p.applySignal(row)
	case event.TypeAgentResized:
		return p.applyResize(row)
	case event.TypeAgentAttachment:
		return p.applyAttachment(row)
	case event.TypeAgentResumed:
		return p.applyResume(row)
	case event.TypeOutput, event.TypeOutputChunk:
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

func (p *recoveryProjector) applyResize(row store.EventRow) error {
	if row.SessionID == "" {
		return projectionError(row, "resize event has empty session ID")
	}
	if err := validateAgentID(row); err != nil {
		return err
	}
	if _, err := event.DecodeAgentResizedPayload(row.Payload); err != nil {
		return projectionWrapError(row, "validate resize payload", err)
	}
	p.draft(row)
	return nil
}

func (p *recoveryProjector) applyAttachment(row store.EventRow) error {
	if row.SessionID == "" {
		return projectionError(row, "attachment event has empty session ID")
	}
	if err := validateAgentID(row); err != nil {
		return err
	}
	if _, err := event.DecodeAttachmentAuditPayload(row.Payload); err != nil {
		return projectionWrapError(row, "validate attachment payload", err)
	}
	return nil
}

func (p *recoveryProjector) applyLifecycle(row store.EventRow) error {
	if row.SessionID == "" {
		return projectionError(row, "lifecycle event has empty session ID")
	}
	if err := validateAgentID(row); err != nil {
		return err
	}
	switch row.Reason {
	case "created":
		return p.applyCreated(row)
	case workspaceRemovedReason:
		return p.applyWorkspaceRemoved(row)
	default:
		return projectionError(row, "unknown lifecycle reason %q", row.Reason)
	}
}

func (p *recoveryProjector) applyCreated(row store.EventRow) error {
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
	if strings.TrimSpace(metadata.Name) == "" {
		return projectionError(row, "creation metadata name is empty")
	}
	if strings.TrimSpace(metadata.Vendor) == "" {
		return projectionError(row, "creation metadata vendor is empty")
	}
	switch metadata.Version {
	case 1:
		draft.hookPolicy = agent.HooksOff
		draft.signalInjection = agent.SignalInjectionOff
		draft.injectionStatus = agent.InjectionDetached
		draft.injectionReason = agent.InjectionReasonRecovered
		if metadata.Mode == nil {
			draft.runMode = agent.RunModeOneshot
		} else if !agent.ValidRunMode(*metadata.Mode) {
			return projectionError(row, "creation metadata mode %q is invalid", *metadata.Mode)
		} else {
			draft.runMode = *metadata.Mode
		}
	case 2:
		if metadata.Mode == nil || !agent.ValidRunMode(*metadata.Mode) {
			return projectionError(row, "creation metadata version 2 requires a valid mode")
		}
		if metadata.HookPolicy == nil || !agent.ValidHookPolicy(*metadata.HookPolicy) {
			return projectionError(row, "creation metadata version 2 requires a valid hook policy")
		}
		draft.runMode = *metadata.Mode
		draft.hookPolicy = *metadata.HookPolicy
		hasInjectionMetadata := metadata.SignalInjection != nil ||
			metadata.SignalInjectionStatus != nil ||
			metadata.SignalInjectionReason != nil
		if !hasInjectionMetadata {
			draft.signalInjection = agent.SignalInjectionOff
			draft.injectionStatus = agent.InjectionDetached
			draft.injectionReason = agent.InjectionReasonRecovered
			break
		}
		if metadata.SignalInjection == nil ||
			metadata.SignalInjectionStatus == nil ||
			metadata.SignalInjectionReason == nil ||
			!agent.ValidSignalInjectionMode(*metadata.SignalInjection) {
			return projectionError(
				row,
				"creation metadata version 2 has an invalid signal injection mode",
			)
		}
		if !agent.ValidSignalInjectionResult(
			*metadata.SignalInjectionStatus,
			*metadata.SignalInjectionReason,
		) {
			return projectionError(
				row,
				"creation metadata version 2 has an invalid signal injection result",
			)
		}
		draft.signalInjection = *metadata.SignalInjection
		draft.injectionStatus = agent.InjectionDetached
		draft.injectionReason = agent.InjectionReasonRecovered
	default:
		return projectionError(row, "unsupported creation metadata version %d", metadata.Version)
	}
	workingDir := metadata.WorkingDir
	if workingDir == "" {
		workingDir = metadata.Dir
	} else if metadata.Dir != "" && metadata.Dir != workingDir {
		return projectionError(row, "creation metadata has conflicting working directories")
	}
	if workingDir != "" {
		if !filepath.IsAbs(workingDir) ||
			filepath.Clean(workingDir) != workingDir {
			return projectionError(
				row,
				"creation metadata working directory is not a clean absolute path",
			)
		}
		draft.workingDir = workingDir
	}
	if err := validateWorkspaceMetadata(
		metadata.Workspace,
		metadata.WorkingDir,
	); err != nil {
		return projectionWrapError(row, "validate workspace metadata", err)
	}

	draft.name = metadata.Name
	draft.vendor = metadata.Vendor
	draft.state = agent.StatePending
	draft.updatedAt = row.Timestamp
	draft.hasCreated = true
	return nil
}

func (p *recoveryProjector) applyWorkspaceRemoved(row store.EventRow) error {
	draft := p.draft(row)
	if !draft.hasCreated && !draft.hasState {
		return projectionError(row, "workspace removal has no session history")
	}
	if draft.workspaceRemoved {
		return projectionError(row, "duplicate workspace removal")
	}
	var payload workspaceRemovedPayload
	if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
		return projectionWrapError(row, "decode workspace removal", err)
	}
	if payload.Version != 1 {
		return projectionError(
			row,
			"unsupported workspace removal version %d",
			payload.Version,
		)
	}
	draft.workspaceRemoved = true
	draft.updatedAt = row.Timestamp
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
	isResume := from == agent.StateStopped && to == agent.StateStarting
	if isResume &&
		(p.lastResumeSeq+1 != row.Seq || p.lastResumeID != row.SessionID) {
		return projectionError(
			row,
			"stopped -> starting requires an immediately preceding same-Agent agent.resumed event",
		)
	}
	if !isResume && !agent.CanRecoverTransition(from, to) {
		return projectionError(row, "invalid state transition %s -> %s", from, to)
	}
	evidence, known, err := parseStateEvidence(row)
	if err != nil {
		return err
	}
	if !known && row.Payload != "" {
		p.report.UnknownStateEvidenceVersions++
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
	if known {
		draft.lastTransition = evidence
	}
	draft.updatedAt = row.Timestamp
	draft.lastStateGapGeneration = p.gapGeneration
	draft.hasState = true
	return nil
}

func (p *recoveryProjector) applyResume(row store.EventRow) error {
	if row.SessionID == "" {
		return projectionError(row, "agent.resumed event has empty session ID")
	}
	if err := validateAgentID(row); err != nil {
		return err
	}
	if row.Reason != "requested" {
		return projectionError(row, "agent.resumed event has invalid reason %q", row.Reason)
	}
	payload, err := event.DecodeAgentResumedPayload(row.Payload)
	if err != nil {
		return projectionWrapError(row, "validate agent.resumed payload", err)
	}
	p.lastResumeSeq = row.Seq
	p.lastResumeID = row.SessionID
	p.draft(row).vendorSessionRef = payload.VendorSessionRef
	return nil
}

func (p *recoveryProjector) applySignal(row store.EventRow) error {
	if row.SessionID == "" {
		return projectionError(row, "signal event has empty session ID")
	}
	if err := validateAgentID(row); err != nil {
		return err
	}
	var version struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal([]byte(row.Payload), &version); err != nil {
		return projectionWrapError(row, "decode signal payload version", err)
	}
	if version.Version == 0 {
		return projectionError(row, "signal payload version is required")
	}
	var payload event.SignalPayloadV1
	switch version.Version {
	case 1:
		if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
			return projectionWrapError(row, "decode signal payload", err)
		}
		if err := payload.Validate(); err != nil {
			return projectionWrapError(row, "validate signal payload", err)
		}
	case 2:
		var versioned event.SignalPayloadV2
		if err := json.Unmarshal([]byte(row.Payload), &versioned); err != nil {
			return projectionWrapError(row, "decode signal payload", err)
		}
		if err := versioned.Validate(); err != nil {
			return projectionWrapError(row, "validate signal payload", err)
		}
		payload = event.SignalPayloadV1(versioned)
	case 3:
		var versioned event.SignalPayloadV3
		if err := json.Unmarshal([]byte(row.Payload), &versioned); err != nil {
			return projectionWrapError(row, "decode signal payload", err)
		}
		if err := versioned.Validate(); err != nil {
			return projectionWrapError(row, "validate signal payload", err)
		}
		payload = versioned.SignalPayloadV1
	case 4:
		var versioned event.SignalPayloadV4
		if err := json.Unmarshal([]byte(row.Payload), &versioned); err != nil {
			return projectionWrapError(row, "decode signal payload", err)
		}
		if err := versioned.Validate(); err != nil {
			return projectionWrapError(row, "validate signal payload", err)
		}
		payload = versioned.SignalPayloadV1
	default:
		p.report.UnknownSignalPayloadVersions++
		return nil
	}
	if ref := payload.VendorSessionReference(); ref != "" {
		p.draft(row).vendorSessionRef = ref
	}
	return nil
}

func parseStateEvidence(row store.EventRow) (*agent.Evidence, bool, error) {
	if row.Payload == "" {
		return nil, false, nil
	}
	var version struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal([]byte(row.Payload), &version); err != nil {
		return nil, false, projectionWrapError(row, "decode state evidence version", err)
	}
	if version.Version == 0 {
		return nil, false, projectionError(row, "state evidence version is required")
	}
	switch version.Version {
	case 1:
		var payload event.StateEvidencePayloadV1
		if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
			return nil, false, projectionWrapError(row, "decode state evidence", err)
		}
		if err := payload.Validate(); err != nil {
			return nil, false, projectionWrapError(row, "validate state evidence", err)
		}
		return agentEvidence(
			payload.Source,
			payload.Event,
			payload.Confidence,
			payload.DeliveryID,
			row,
		)
	case 2:
		var payload event.StateEvidencePayloadV2
		if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
			return nil, false, projectionWrapError(row, "decode state evidence", err)
		}
		if err := payload.Validate(); err != nil {
			return nil, false, projectionWrapError(row, "validate state evidence", err)
		}
		return agentEvidence(
			payload.Source,
			payload.Event,
			payload.Confidence,
			payload.DeliveryID,
			row,
		)
	case 3:
		var payload event.StateEvidencePayloadV3
		if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
			return nil, false, projectionWrapError(row, "decode state evidence", err)
		}
		if err := payload.Validate(); err != nil {
			return nil, false, projectionWrapError(row, "validate state evidence", err)
		}
		if payload.Source != string(agent.EvidenceScreen) {
			return agentEvidence(
				payload.Source,
				payload.Event,
				payload.Confidence,
				payload.DeliveryID,
				row,
			)
		}
		return screenAgentEvidence(payload, row)
	case 4:
		var payload event.StateEvidencePayloadV4
		if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
			return nil, false, projectionWrapError(row, "decode state evidence", err)
		}
		if err := payload.Validate(); err != nil {
			return nil, false, projectionWrapError(row, "validate state evidence", err)
		}
		if payload.Terminal != nil {
			return terminalAgentEvidence(payload, row)
		}
		if payload.Source == string(agent.EvidenceScreen) {
			return screenAgentEvidence(event.StateEvidencePayloadV3{
				StateEvidencePayloadV1: payload.StateEvidencePayloadV1,
				Screen:                 payload.Screen,
			}, row)
		}
		return agentEvidence(
			payload.Source,
			payload.Event,
			payload.Confidence,
			payload.DeliveryID,
			row,
		)
	default:
		return nil, false, nil
	}
}

func terminalAgentEvidence(
	payload event.StateEvidencePayloadV4,
	row store.EventRow,
) (*agent.Evidence, bool, error) {
	terminal, err := agent.NewTerminalAttribution(
		payload.Terminal.Protocol,
		payload.Terminal.OutputOffset,
		payload.Terminal.LastOutputSeq,
	)
	if err != nil {
		return nil, false, projectionWrapError(row, "validate terminal attribution", err)
	}
	evidence := &agent.Evidence{
		Source:     agent.EvidenceSource(payload.Source),
		Event:      payload.Event,
		Confidence: payload.Confidence,
		DeliveryID: payload.DeliveryID,
		Terminal:   &terminal,
	}
	if err := evidence.Validate(); err != nil {
		return nil, false, projectionWrapError(row, "validate agent evidence", err)
	}
	return evidence, true, nil
}

func screenAgentEvidence(
	payload event.StateEvidencePayloadV3,
	row store.EventRow,
) (*agent.Evidence, bool, error) {
	screen, err := agent.NewScreenAttribution(
		payload.Screen.Rule,
		agent.ScreenEdge(payload.Screen.Edge),
		payload.Screen.Region,
		payload.Screen.OutputOffset,
		payload.Screen.LastOutputSeq,
		payload.Screen.Evidence,
	)
	if err != nil {
		return nil, false, projectionWrapError(row, "validate screen attribution", err)
	}
	evidence := &agent.Evidence{
		Source:     agent.EvidenceSource(payload.Source),
		Event:      payload.Event,
		Confidence: payload.Confidence,
		DeliveryID: payload.DeliveryID,
		Screen:     &screen,
	}
	if err := evidence.Validate(); err != nil {
		return nil, false, projectionWrapError(row, "validate agent evidence", err)
	}
	return evidence, true, nil
}

func agentEvidence(
	source string,
	eventName string,
	confidence float64,
	deliveryID string,
	row store.EventRow,
) (*agent.Evidence, bool, error) {
	evidence := &agent.Evidence{
		Source:     agent.EvidenceSource(source),
		Event:      eventName,
		Confidence: confidence,
		DeliveryID: deliveryID,
	}
	if err := evidence.Validate(); err != nil {
		return nil, false, projectionWrapError(row, "validate agent evidence", err)
	}
	return evidence, true, nil
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
		Snapshots:         make([]agent.RestoreSnapshot, 0, len(drafts)),
		VendorSessionRefs: make(map[string]string),
		WorkingDirs:       make(map[string]string),
		ResumeOnStart:     make(map[string]bool),
		WorkspaceRemoved:  make(map[string]bool),
		Report:            p.report,
	}
	nextSeq := p.lastSeq
	for _, draft := range drafts {
		if !draft.hasCreated {
			draft.name = draft.id
			draft.vendor = "unknown"
			draft.runMode = agent.RunModeOneshot
			draft.hookPolicy = agent.HooksOff
			draft.signalInjection = agent.SignalInjectionOff
			draft.injectionStatus = agent.InjectionDetached
			draft.injectionReason = agent.InjectionReasonRecovered
			plan.Report.LegacyMetadata++
		}

		state := draft.state
		if draft.vendorSessionRef != "" {
			plan.VendorSessionRefs[draft.id] = draft.vendorSessionRef
			plan.ResumeOnStart[draft.id] =
				!draft.workspaceRemoved &&
					state != agent.StateDone &&
					state != agent.StateStopped
		}
		workingDir := draft.workingDir
		if draft.workspaceRemoved {
			workingDir = ""
			plan.WorkspaceRemoved[draft.id] = true
		}
		if workingDir != "" {
			plan.WorkingDirs[draft.id] = workingDir
		}
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
				Payload:   recoveryEvidencePayload(),
			})
			draft.lastTransition = &agent.Evidence{
				Source:     agent.EvidenceRecovery,
				Event:      "daemon_restart",
				Confidence: 1,
			}
			state = agent.StateStopped
			updatedAt = recoveryTime
		}
		if updatedAt.Before(draft.createdAt) {
			updatedAt = draft.createdAt
		}
		plan.Snapshots = append(plan.Snapshots, agent.RestoreSnapshot{
			ID:              agent.ID(draft.id),
			Name:            draft.name,
			Vendor:          draft.vendor,
			WorkingDir:      workingDir,
			RunMode:         draft.runMode,
			HookPolicy:      draft.hookPolicy,
			SignalInjection: draft.signalInjection,
			InjectionStatus: draft.injectionStatus,
			InjectionReason: draft.injectionReason,
			State:           state,
			LastError:       lastError,
			LastTransition:  draft.lastTransition,
			CreatedAt:       draft.createdAt,
			UpdatedAt:       updatedAt,
		})
	}
	plan.Report.Sessions = len(plan.Snapshots)
	plan.Report.LastSeq = nextSeq
	return plan, nil
}

func recoveryEvidencePayload() string {
	payload, err := json.Marshal(event.StateEvidencePayloadV1{
		Version:    1,
		Source:     string(agent.EvidenceRecovery),
		Event:      "daemon_restart",
		Confidence: 1,
	})
	if err != nil {
		panic(fmt.Sprintf("session: encode fixed recovery evidence: %v", err))
	}
	return string(payload)
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
