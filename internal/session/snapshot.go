package session

import (
	"time"

	"github.com/Duang777/drove/internal/recording"
	"github.com/Duang777/drove/internal/term"
)

const liveSnapshotInterval = 500 * time.Millisecond

// LiveSnapshot is a bounded, live-only terminal preview.
type LiveSnapshot struct {
	Cursor     recording.Cursor `json:"cursor"`
	Rows       uint16           `json:"rows"`
	Columns    uint16           `json:"columns"`
	Lines      []string         `json:"lines"`
	Truncated  bool             `json:"truncated"`
	Restorable bool             `json:"restorable"`
	CapturedAt time.Time        `json:"captured_at"`
}

func (s LiveSnapshot) clone() LiveSnapshot {
	s.Lines = append([]string(nil), s.Lines...)
	return s
}

func (p *outputProcessor) watchSnapshots(
	state *outputProcessorState,
	id AttachmentID,
) (<-chan LiveSnapshot, error) {
	attachment, ok := state.attachments[id]
	if !ok || state.ended {
		return nil, ErrAttachmentClosed
	}
	if attachment.snapshots != nil {
		return nil, ErrSnapshotWatchExists
	}
	snapshots := make(chan LiveSnapshot, 1)
	attachment.snapshots = snapshots
	if snapshot, available := p.captureSnapshot(state); available {
		snapshots <- snapshot
	}
	p.armSnapshotTimer(state)
	return snapshots, nil
}

func (p *outputProcessor) captureSnapshot(
	state *outputProcessorState,
) (LiveSnapshot, bool) {
	if p.running.terminal == nil {
		return LiveSnapshot{}, false
	}
	snapshot, capturedAt, outputOffset, available :=
		p.running.terminal.snapshotState()
	if !available ||
		outputOffset != state.nextOutputOffset ||
		!sameTerminalSize(snapshot.Size(), state.effectiveSize) {
		return LiveSnapshot{}, false
	}
	view, err := snapshot.View(term.DefaultViewOptions())
	if err != nil {
		return LiveSnapshot{}, false
	}
	cursor, err := recording.NewCursor(
		recording.Seq(state.lastSeq),
		recording.OutputOffset(state.nextOutputOffset),
	)
	if err != nil {
		return LiveSnapshot{}, false
	}
	return LiveSnapshot{
		Cursor:     cursor,
		Rows:       uint16(snapshot.Size().Rows()),
		Columns:    uint16(snapshot.Size().Columns()),
		Lines:      view.Rows(),
		Truncated:  view.Truncated(),
		Restorable: false,
		CapturedAt: capturedAt,
	}, true
}

func (p *outputProcessor) publishSnapshots(state *outputProcessorState) {
	snapshot, available := p.captureSnapshot(state)
	if !available {
		return
	}
	for _, attachment := range state.attachments {
		if attachment.snapshots == nil {
			continue
		}
		latest := snapshot.clone()
		select {
		case attachment.snapshots <- latest:
		default:
			select {
			case <-attachment.snapshots:
			default:
			}
			attachment.snapshots <- latest
		}
	}
}

func (p *outputProcessor) armSnapshotTimer(state *outputProcessorState) {
	if state.ended || !p.hasSnapshotWatches(state) {
		return
	}
	if state.snapshotTimer == nil {
		state.snapshotTimer = p.manager.clock.NewTimer(liveSnapshotInterval)
	} else {
		stopAndDrainTimer(state.snapshotTimer)
		state.snapshotTimer.Reset(liveSnapshotInterval)
	}
	state.snapshotTimerC = state.snapshotTimer.C()
}

func (p *outputProcessor) stopSnapshotTimer(state *outputProcessorState) {
	if state.snapshotTimer != nil {
		stopAndDrainTimer(state.snapshotTimer)
	}
	state.snapshotTimerC = nil
}

func (p *outputProcessor) hasSnapshotWatches(state *outputProcessorState) bool {
	for _, attachment := range state.attachments {
		if attachment.snapshots != nil {
			return true
		}
	}
	return false
}

func (p *outputProcessor) closeSnapshotWatches(state *outputProcessorState) {
	for _, attachment := range state.attachments {
		if attachment.snapshots != nil {
			close(attachment.snapshots)
			attachment.snapshots = nil
		}
	}
}
