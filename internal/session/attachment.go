package session

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/pty"
	"github.com/Duang777/drove/internal/term"
)

// AttachmentID is an opaque identifier for one live terminal attachment.
type AttachmentID string

// AttachmentMode controls whether an attachment may write input and own size.
type AttachmentMode string

const (
	// AttachmentReadOnly receives terminal data without input or size ownership.
	AttachmentReadOnly AttachmentMode = "read_only"
	// AttachmentWritable may write input and participate in size ownership.
	AttachmentWritable AttachmentMode = "writable"
)

// AttachmentPurpose distinguishes internal recording readers from users.
type AttachmentPurpose string

const (
	// AttachmentPurposeRecording is an unaudited internal recording reader.
	AttachmentPurposeRecording AttachmentPurpose = "recording"
	// AttachmentPurposeUser is an audited user-visible terminal attachment.
	AttachmentPurposeUser AttachmentPurpose = "user"
)

var (
	// ErrAttachmentClosed reports an operation on a detached terminal handle.
	ErrAttachmentClosed = errors.New("session: terminal attachment closed")
	// ErrAttachmentReadOnly reports a write through a read-only attachment.
	ErrAttachmentReadOnly = errors.New("session: terminal attachment is read-only")
	// ErrSnapshotWatchExists reports a duplicate watch on one attachment.
	ErrSnapshotWatchExists = errors.New("session: snapshot watch already exists")
)

// AttachmentOptions configures a live terminal attachment.
type AttachmentOptions struct {
	Purpose AttachmentPurpose
	Mode    AttachmentMode
	Rows    int
	Columns int
}

type attachmentConfig struct {
	purpose  AttachmentPurpose
	mode     AttachmentMode
	viewport term.Size
}

func (o AttachmentOptions) validate() (attachmentConfig, error) {
	switch o.Purpose {
	case AttachmentPurposeRecording:
		if o.Mode != AttachmentReadOnly {
			return attachmentConfig{}, errors.New(
				"session: recording attachment must be read-only",
			)
		}
	case AttachmentPurposeUser:
	default:
		return attachmentConfig{}, fmt.Errorf(
			"session: invalid attachment purpose %q",
			o.Purpose,
		)
	}

	switch o.Mode {
	case AttachmentReadOnly:
		if o.Rows != 0 || o.Columns != 0 {
			return attachmentConfig{}, errors.New(
				"session: read-only attachment cannot propose a terminal size",
			)
		}
		return attachmentConfig{purpose: o.Purpose, mode: o.Mode}, nil
	case AttachmentWritable:
	default:
		return attachmentConfig{}, fmt.Errorf(
			"session: invalid attachment mode %q",
			o.Mode,
		)
	}

	if o.Rows == 0 && o.Columns == 0 {
		return attachmentConfig{purpose: o.Purpose, mode: o.Mode}, nil
	}
	viewport, err := term.NewSize(o.Rows, o.Columns)
	if err != nil {
		return attachmentConfig{}, fmt.Errorf(
			"session: invalid attachment viewport: %w",
			err,
		)
	}
	return attachmentConfig{
		purpose:  o.Purpose,
		mode:     o.Mode,
		viewport: viewport,
	}, nil
}

type attachmentState struct {
	purpose   AttachmentPurpose
	mode      AttachmentMode
	proposed  term.Size
	ticket    uint64
	snapshots chan LiveSnapshot
}

// TerminalAttachment is a live handle to one attached terminal session.
type TerminalAttachment struct {
	id     AttachmentID
	mode   AttachmentMode
	output *outputProcessor

	closeOnce sync.Once
	closeErr  error
}

// AttachTerminal registers a live terminal attachment.
func (m *Manager) AttachTerminal(
	ctx context.Context,
	id agent.ID,
	options AttachmentOptions,
) (*TerminalAttachment, error) {
	config, err := options.validate()
	if err != nil {
		return nil, err
	}

	m.mu.RLock()
	closed := m.closed
	_, known := m.agents[id]
	running, attached := m.sessions[id]
	m.mu.RUnlock()
	if closed {
		return nil, ErrManagerClosed
	}
	if !known {
		return nil, fmt.Errorf("%w: %q", ErrUnknownAgent, id)
	}
	if !attached || running.output == nil {
		return nil, fmt.Errorf("%w: %q", ErrNotAttached, id)
	}

	attachmentID := AttachmentID(uuid.NewString())
	if err := running.output.Attach(ctx, attachmentID, config); err != nil {
		return nil, err
	}
	return &TerminalAttachment{
		id:     attachmentID,
		mode:   config.mode,
		output: running.output,
	}, nil
}

// ID returns the opaque live attachment identifier.
func (a *TerminalAttachment) ID() AttachmentID {
	if a == nil {
		return ""
	}
	return a.id
}

// Mode returns the attachment's access mode.
func (a *TerminalAttachment) Mode() AttachmentMode {
	if a == nil {
		return ""
	}
	return a.mode
}

// SendInput writes one complete UTF-8 input through a writable attachment.
func (a *TerminalAttachment) SendInput(
	ctx context.Context,
	data []byte,
) (InputResult, error) {
	if a == nil || a.output == nil {
		return InputResult{}, ErrAttachmentClosed
	}
	if a.mode != AttachmentWritable {
		return InputResult{}, ErrAttachmentReadOnly
	}
	payload, err := validateInput(data)
	if err != nil {
		return InputResult{}, err
	}
	return a.output.SendAttachedInput(ctx, a.id, data, payload)
}

// Resize updates the attachment's proposed terminal size.
func (a *TerminalAttachment) Resize(
	ctx context.Context,
	rows int,
	columns int,
) error {
	if a == nil || a.output == nil {
		return ErrAttachmentClosed
	}
	if a.mode != AttachmentWritable {
		return ErrAttachmentReadOnly
	}
	size, err := term.NewSize(rows, columns)
	if err != nil {
		return fmt.Errorf("session: invalid attachment resize: %w", err)
	}
	return a.output.ResizeAttachment(ctx, a.id, size)
}

// WatchSnapshots starts one capacity-one live snapshot stream.
func (a *TerminalAttachment) WatchSnapshots(
	ctx context.Context,
) (<-chan LiveSnapshot, error) {
	if a == nil || a.output == nil {
		return nil, ErrAttachmentClosed
	}
	return a.output.WatchSnapshots(ctx, a.id)
}

// Close detaches the handle and closes its snapshot stream.
func (a *TerminalAttachment) Close() error {
	if a == nil || a.output == nil {
		return nil
	}
	a.closeOnce.Do(func() {
		a.closeErr = a.output.Detach(context.Background(), a.id)
		if errors.Is(a.closeErr, errOutputProcessorClosed) {
			a.closeErr = nil
		}
	})
	return a.closeErr
}

func (p *outputProcessor) attach(
	state *outputProcessorState,
	id AttachmentID,
	config attachmentConfig,
) error {
	if !state.acceptingAttachments || state.ended {
		return ErrAttachmentClosed
	}
	if id == "" {
		return errors.New("session: attachment ID is required")
	}
	if _, exists := state.attachments[id]; exists {
		return fmt.Errorf("session: duplicate attachment %q", id)
	}

	proposed := config.viewport
	if proposed.Rows() == 0 {
		proposed = state.effectiveSize
	}
	attachment := &attachmentState{
		purpose:  config.purpose,
		mode:     config.mode,
		proposed: proposed,
		ticket:   p.nextTicket(state),
	}
	if config.mode == AttachmentWritable && state.owner == "" {
		if err := p.applyResize(state, proposed); err != nil {
			return err
		}
		state.owner = id
	}
	state.attachments[id] = attachment
	if config.purpose == AttachmentPurposeUser {
		if err := p.commitAttachmentAudit(
			state,
			event.AttachmentAttached,
			config.mode,
		); err != nil {
			cleanupErr := p.removeAttachment(state, id)
			auditErr := fmt.Errorf(
				"session: commit attached audit for agent %q: %w",
				p.id,
				err,
			)
			p.manager.committer.Fail(auditErr)
			return errors.Join(auditErr, cleanupErr)
		}
	}
	return nil
}

func (p *outputProcessor) resizeAttachment(
	state *outputProcessorState,
	id AttachmentID,
	size term.Size,
) error {
	attachment, ok := state.attachments[id]
	if !ok {
		return ErrAttachmentClosed
	}
	if attachment.mode != AttachmentWritable {
		return ErrAttachmentReadOnly
	}
	if state.owner == id {
		if err := p.applyResize(state, size); err != nil {
			return err
		}
	}
	attachment.proposed = size
	attachment.ticket = p.nextTicket(state)
	return nil
}

func (p *outputProcessor) detach(
	state *outputProcessorState,
	id AttachmentID,
) error {
	attachment, ok := state.attachments[id]
	if !ok {
		return nil
	}
	localErr := p.removeAttachment(state, id)
	if attachment.purpose != AttachmentPurposeUser {
		return localErr
	}
	auditErr := p.commitAttachmentAudit(
		state,
		event.AttachmentDetached,
		attachment.mode,
	)
	if auditErr != nil {
		auditErr = fmt.Errorf(
			"session: commit detached audit for agent %q: %w",
			p.id,
			auditErr,
		)
		p.manager.committer.Fail(auditErr)
	}
	return errors.Join(localErr, auditErr)
}

func (p *outputProcessor) removeAttachment(
	state *outputProcessorState,
	id AttachmentID,
) error {
	attachment, ok := state.attachments[id]
	if !ok {
		return nil
	}
	delete(state.attachments, id)
	if attachment.snapshots != nil {
		close(attachment.snapshots)
	}
	defer func() {
		if !p.hasSnapshotWatches(state) {
			p.stopSnapshotTimer(state)
		}
	}()
	if state.owner != id {
		return nil
	}

	state.owner = ""
	var fallbackID AttachmentID
	var fallback *attachmentState
	for candidateID, candidate := range state.attachments {
		if candidate.mode != AttachmentWritable {
			continue
		}
		if fallback == nil || candidate.ticket > fallback.ticket {
			fallbackID = candidateID
			fallback = candidate
		}
	}
	if fallback != nil {
		state.owner = fallbackID
		if err := p.applyResize(state, fallback.proposed); err != nil {
			return err
		}
	}
	return nil
}

func (p *outputProcessor) detachAll(state *outputProcessorState) error {
	state.acceptingAttachments = false
	state.owner = ""
	p.stopSnapshotTimer(state)
	p.closeSnapshotWatches(state)

	drafts := make([]event.Draft, 0, len(state.attachments))
	var draftErrors []error
	for _, attachment := range state.attachments {
		if attachment.purpose != AttachmentPurposeUser {
			continue
		}
		draft, err := attachmentAuditDraft(
			p.id,
			event.AttachmentDetached,
			attachment.mode,
		)
		if err != nil {
			draftErrors = append(draftErrors, err)
			continue
		}
		drafts = append(drafts, draft)
	}
	state.attachments = make(map[AttachmentID]*attachmentState)
	if err := errors.Join(draftErrors...); err != nil {
		p.manager.committer.Fail(err)
		return err
	}
	if len(drafts) == 0 {
		return nil
	}
	receipt, err := p.manager.committer.CommitEvents(
		context.Background(),
		drafts,
	)
	if err != nil {
		auditErr := fmt.Errorf(
			"session: commit forced detach audits for agent %q: %w",
			p.id,
			err,
		)
		p.manager.committer.Fail(auditErr)
		return auditErr
	}
	state.lastSeq = receipt.LastSeq
	return nil
}

func (p *outputProcessor) sendAttachedInput(
	state *outputProcessorState,
	id AttachmentID,
	data []byte,
	payload string,
) (InputResult, error) {
	attachment, ok := state.attachments[id]
	if !ok {
		return InputResult{}, ErrAttachmentClosed
	}
	if attachment.mode != AttachmentWritable {
		return InputResult{}, ErrAttachmentReadOnly
	}
	if state.ended {
		return InputResult{}, ErrAttachmentClosed
	}

	p.running.inputMu.Lock()
	defer p.running.inputMu.Unlock()
	if err := p.attachedInputAvailable(); err != nil {
		return InputResult{}, err
	}
	if !sameTerminalSize(attachment.proposed, state.effectiveSize) {
		if err := p.applyResize(state, attachment.proposed); err != nil {
			return InputResult{}, err
		}
	}

	written, err := p.running.process.Write(data)
	result := InputResult{BytesWritten: written}
	if err != nil {
		if errors.Is(err, pty.ErrClosed) {
			return result, fmt.Errorf("%w: %q", ErrNotAttached, p.id)
		}
		return result, fmt.Errorf(
			"%w: agent %q wrote %d/%d bytes: %w",
			ErrInputWrite,
			p.id,
			written,
			len(data),
			err,
		)
	}
	if written != len(data) {
		return result, fmt.Errorf(
			"%w: agent %q wrote %d/%d bytes",
			ErrInputWrite,
			p.id,
			written,
			len(data),
		)
	}

	attachment.ticket = p.nextTicket(state)
	state.owner = id
	receipt, err := p.manager.committer.CommitEvents(
		context.Background(),
		[]event.Draft{event.NewAgentInputDraft(
			string(p.id),
			string(p.id),
			payload,
		)},
	)
	if err != nil {
		return result, fmt.Errorf(
			"%w: agent %q received %d bytes; do not retry: %w",
			ErrInputAudit,
			p.id,
			written,
			err,
		)
	}
	state.lastSeq = receipt.LastSeq
	return result, nil
}

func (p *outputProcessor) attachedInputAvailable() error {
	p.manager.mu.RLock()
	defer p.manager.mu.RUnlock()
	if p.manager.closed {
		return ErrManagerClosed
	}
	current, attached := p.manager.sessions[p.id]
	if !attached || current != p.running || p.running.exitClaimed ||
		p.running.process == nil {
		return fmt.Errorf("%w: %q", ErrNotAttached, p.id)
	}
	return nil
}

func (p *outputProcessor) nextTicket(state *outputProcessorState) uint64 {
	state.nextActivityTicket++
	return state.nextActivityTicket
}

func (p *outputProcessor) commitAttachmentAudit(
	state *outputProcessorState,
	action event.AttachmentAction,
	mode AttachmentMode,
) error {
	draft, err := attachmentAuditDraft(p.id, action, mode)
	if err != nil {
		return err
	}
	receipt, err := p.manager.committer.CommitEvents(
		context.Background(),
		[]event.Draft{draft},
	)
	if err != nil {
		return err
	}
	state.lastSeq = receipt.LastSeq
	return nil
}

func attachmentAuditDraft(
	id agent.ID,
	action event.AttachmentAction,
	mode AttachmentMode,
) (event.Draft, error) {
	var access event.AttachmentAccess
	switch mode {
	case AttachmentReadOnly:
		access = event.AttachmentReadOnly
	case AttachmentWritable:
		access = event.AttachmentReadWrite
	default:
		return event.Draft{}, fmt.Errorf(
			"session: invalid attachment audit mode %q",
			mode,
		)
	}
	return event.NewAgentAttachmentDraft(
		string(id),
		string(id),
		action,
		access,
	)
}
