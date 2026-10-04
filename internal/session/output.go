package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/term"
)

const outputInboxSize = 64

var (
	errOutputProcessorClosed = errors.New("session: output processor closed")
	errOutputStreamEnded     = errors.New("session: output stream ended")
)

type outputOperation uint8

const (
	outputFeed outputOperation = iota + 1
	outputEnd
	outputResize
	outputAttach
	outputDetach
	outputDetachAll
	outputAttachedInput
	outputWatchSnapshots
	outputClose
)

type outputRequest struct {
	operation    outputOperation
	chunk        []byte
	offset       uint64
	size         term.Size
	attachmentID AttachmentID
	attachment   attachmentConfig
	inputPayload string
	result       chan outputResult
}

type outputResult struct {
	input     InputResult
	snapshots <-chan LiveSnapshot
	err       error
}

type outputProcessor struct {
	manager *Manager
	id      agent.ID
	running *runningSession

	redactor            streamingRedactor
	initialOutputOffset uint64
	requests            chan outputRequest
	done                chan struct{}

	admissionMu sync.RWMutex
	closing     bool
	closeOnce   sync.Once
	closeErr    error
}

type outputProcessorState struct {
	redactor streamingRedactor

	nextSourceOffset uint64
	nextOutputOffset uint64
	lastSeq          uint64
	pendingActivity  int
	ended            bool
	failure          error

	effectiveSize        term.Size
	attachments          map[AttachmentID]*attachmentState
	owner                AttachmentID
	nextActivityTicket   uint64
	acceptingAttachments bool
	snapshotTimer        observationTimer
	snapshotTimerC       <-chan time.Time
}

func newOutputProcessor(
	manager *Manager,
	id agent.ID,
	running *runningSession,
	signalToken string,
) *outputProcessor {
	return newOutputProcessorAtOffset(manager, id, running, signalToken, 0)
}

func newOutputProcessorAtOffset(
	manager *Manager,
	id agent.ID,
	running *runningSession,
	signalToken string,
	initialOutputOffset uint64,
) *outputProcessor {
	processor := &outputProcessor{
		manager:             manager,
		id:                  id,
		running:             running,
		redactor:            newStreamingRedactor([]byte(signalToken)),
		initialOutputOffset: initialOutputOffset,
		requests:            make(chan outputRequest, outputInboxSize),
		done:                make(chan struct{}),
	}
	go processor.run()
	return processor
}

func (p *outputProcessor) Feed(chunk []byte, offset uint64) error {
	if len(chunk) == 0 {
		return fmt.Errorf("session: empty PTY output callback")
	}
	result, err := p.submit(context.Background(), outputRequest{
		operation: outputFeed,
		chunk:     append([]byte(nil), chunk...),
		offset:    offset,
	})
	if err != nil {
		return err
	}
	return result.err
}

func (p *outputProcessor) End(offset uint64) error {
	result, err := p.submit(context.Background(), outputRequest{
		operation: outputEnd,
		offset:    offset,
	})
	if err != nil {
		return err
	}
	return result.err
}

func (p *outputProcessor) Resize(ctx context.Context, size term.Size) error {
	result, err := p.submit(ctx, outputRequest{
		operation: outputResize,
		size:      size,
	})
	if err != nil {
		return err
	}
	return result.err
}

func (p *outputProcessor) Attach(
	ctx context.Context,
	id AttachmentID,
	config attachmentConfig,
) error {
	result, err := p.submit(ctx, outputRequest{
		operation:    outputAttach,
		attachmentID: id,
		attachment:   config,
	})
	if err != nil {
		return err
	}
	return result.err
}

func (p *outputProcessor) ResizeAttachment(
	ctx context.Context,
	id AttachmentID,
	size term.Size,
) error {
	result, err := p.submit(ctx, outputRequest{
		operation:    outputResize,
		attachmentID: id,
		size:         size,
	})
	if err != nil {
		return err
	}
	return result.err
}

func (p *outputProcessor) SendAttachedInput(
	ctx context.Context,
	id AttachmentID,
	data []byte,
	payload string,
) (InputResult, error) {
	if !p.running.inputMu.TryLock() {
		return InputResult{}, ErrInputBackpressure
	}
	defer p.running.inputMu.Unlock()

	result, err := p.submit(ctx, outputRequest{
		operation:    outputAttachedInput,
		attachmentID: id,
		chunk:        append([]byte(nil), data...),
		inputPayload: payload,
	})
	if err != nil {
		return InputResult{}, err
	}
	return result.input, result.err
}

func (p *outputProcessor) WatchSnapshots(
	ctx context.Context,
	id AttachmentID,
) (<-chan LiveSnapshot, error) {
	result, err := p.submit(ctx, outputRequest{
		operation:    outputWatchSnapshots,
		attachmentID: id,
	})
	if err != nil {
		return nil, err
	}
	return result.snapshots, result.err
}

func (p *outputProcessor) Detach(ctx context.Context, id AttachmentID) error {
	result, err := p.submit(ctx, outputRequest{
		operation:    outputDetach,
		attachmentID: id,
	})
	if err != nil {
		return err
	}
	return result.err
}

func (p *outputProcessor) DetachAll() error {
	result, err := p.submit(context.Background(), outputRequest{
		operation: outputDetachAll,
	})
	if err != nil {
		return err
	}
	return result.err
}

func (p *outputProcessor) Close() error {
	p.closeOnce.Do(func() {
		request := outputRequest{
			operation: outputClose,
			result:    make(chan outputResult, 1),
		}
		p.admissionMu.Lock()
		p.closing = true
		p.requests <- request
		p.admissionMu.Unlock()
		p.closeErr = (<-request.result).err
		<-p.done
	})
	return p.closeErr
}

func (p *outputProcessor) submit(
	ctx context.Context,
	request outputRequest,
) (outputResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	request.result = make(chan outputResult, 1)

	p.admissionMu.RLock()
	if p.closing {
		p.admissionMu.RUnlock()
		return outputResult{}, errOutputProcessorClosed
	}
	select {
	case p.requests <- request:
		p.admissionMu.RUnlock()
	case <-ctx.Done():
		p.admissionMu.RUnlock()
		return outputResult{}, fmt.Errorf(
			"session: admit recording operation: %w",
			ctx.Err(),
		)
	}
	return <-request.result, nil
}

func (p *outputProcessor) run() {
	defer close(p.done)
	initialSize, err := initialTerminalSize()
	if err != nil {
		panic(err)
	}
	state := outputProcessorState{
		redactor:             p.redactor,
		nextOutputOffset:     p.initialOutputOffset,
		effectiveSize:        initialSize,
		attachments:          make(map[AttachmentID]*attachmentState),
		acceptingAttachments: true,
	}

	for {
		select {
		case request := <-p.requests:
			if request.operation == outputClose {
				request.result <- outputResult{err: p.detachAll(&state)}
				return
			}
			request.result <- p.handle(&state, request)
		case <-state.snapshotTimerC:
			state.snapshotTimerC = nil
			p.publishSnapshots(&state)
			if p.hasSnapshotWatches(&state) && !state.ended {
				p.armSnapshotTimer(&state)
			}
		}
	}
}

func (p *outputProcessor) handle(
	state *outputProcessorState,
	request outputRequest,
) outputResult {
	if state.failure != nil &&
		request.operation != outputDetach &&
		request.operation != outputDetachAll {
		return outputResult{err: state.failure}
	}

	switch request.operation {
	case outputFeed:
		return outputResult{err: p.feed(state, request.chunk, request.offset)}
	case outputEnd:
		return outputResult{err: p.end(state, request.offset)}
	case outputResize:
		if request.attachmentID == "" {
			return outputResult{err: p.applyResize(state, request.size)}
		}
		return outputResult{
			err: p.resizeAttachment(state, request.attachmentID, request.size),
		}
	case outputAttach:
		return outputResult{
			err: p.attach(state, request.attachmentID, request.attachment),
		}
	case outputDetach:
		return outputResult{err: p.detach(state, request.attachmentID)}
	case outputDetachAll:
		return outputResult{err: p.detachAll(state)}
	case outputAttachedInput:
		input, err := p.sendAttachedInput(
			state,
			request.attachmentID,
			request.chunk,
			request.inputPayload,
		)
		return outputResult{input: input, err: err}
	case outputWatchSnapshots:
		snapshots, err := p.watchSnapshots(state, request.attachmentID)
		return outputResult{snapshots: snapshots, err: err}
	default:
		return outputResult{err: fmt.Errorf(
			"session: unknown recording operation %d",
			request.operation,
		)}
	}
}

func (p *outputProcessor) feed(
	state *outputProcessorState,
	chunk []byte,
	offset uint64,
) error {
	if state.ended {
		return errOutputStreamEnded
	}
	if offset != state.nextSourceOffset {
		return p.failOffset(state, "chunk", offset)
	}

	state.nextSourceOffset += uint64(len(chunk))
	state.pendingActivity++
	output := state.redactor.Feed(chunk)
	if len(output) == 0 {
		return nil
	}
	return p.commitAndObserve(state, output)
}

func (p *outputProcessor) end(
	state *outputProcessorState,
	offset uint64,
) error {
	if state.ended {
		return errOutputStreamEnded
	}
	if offset != state.nextSourceOffset {
		return p.failOffset(state, "end", offset)
	}
	state.ended = true
	defer func() {
		p.stopSnapshotTimer(state)
		p.closeSnapshotWatches(state)
	}()

	output := state.redactor.Flush()
	if len(output) > 0 {
		if err := p.commitAndObserve(state, output); err != nil {
			return err
		}
	}
	if p.running.terminal != nil {
		if err := p.running.terminal.EndOutput(
			context.Background(),
			state.nextOutputOffset,
		); err != nil {
			return p.failTerminal(state, "end output", err, false)
		}
	}
	return nil
}

func (p *outputProcessor) failOffset(
	state *outputProcessorState,
	callback string,
	got uint64,
) error {
	streamErr := fmt.Errorf(
		"session: PTY output %s offset mismatch: got %d, want %d",
		callback,
		got,
		state.nextSourceOffset,
	)
	state.failure = streamErr
	receipt, commitErr := p.manager.committer.CommitEvents(
		context.Background(),
		[]event.Draft{event.NewErrorDraft(
			string(p.id),
			string(p.id),
			streamErr.Error(),
		)},
	)
	if commitErr != nil {
		return fmt.Errorf("%v; persist output stream error: %w", streamErr, commitErr)
	}
	state.lastSeq = receipt.LastSeq
	return streamErr
}

func (p *outputProcessor) commitAndObserve(
	state *outputProcessorState,
	output []byte,
) error {
	drafts := make([]event.Draft, 0, len(output)/event.MaxOutputChunkBytes+1)
	offset := state.nextOutputOffset
	for _, chunk := range splitOutputChunks(output) {
		draft, err := event.NewOutputChunkDraft(
			string(p.id),
			string(p.id),
			offset,
			chunk,
		)
		if err != nil {
			state.failure = err
			return fmt.Errorf("session: create output chunk at offset %d: %w", offset, err)
		}
		drafts = append(drafts, draft)
		offset += uint64(len(chunk))
	}
	receipt, err := p.manager.committer.CommitEvents(context.Background(), drafts)
	if err != nil {
		state.failure = err
		return fmt.Errorf(
			"session: commit output at offset %d: %w",
			state.nextOutputOffset,
			err,
		)
	}

	if p.running.terminal != nil {
		chunk, err := term.NewCommittedChunk(
			output,
			offset,
			receipt.LastSeq,
			receipt.Timestamp,
		)
		if err != nil {
			return p.failTerminal(state, "create committed chunk", err, true)
		}
		if err := p.running.terminal.FeedCommitted(
			context.Background(),
			chunk,
		); err != nil {
			return p.failTerminal(state, "feed committed output", err, true)
		}
	}
	state.nextOutputOffset = offset
	state.lastSeq = receipt.LastSeq
	for range state.pendingActivity {
		if !p.canObserve() {
			break
		}
		observation, err := detect.ObserveOutput(p.running.vendor, p.manager.clock.Now())
		if err != nil {
			break
		}
		if err := p.running.observer.Deliver(context.Background(), observation); err != nil {
			break
		}
	}
	state.pendingActivity = 0
	return nil
}

func (p *outputProcessor) failTerminal(
	state *outputProcessorState,
	operation string,
	cause error,
	fatal bool,
) error {
	terminalErr := fmt.Errorf("session: terminal %s: %w", operation, cause)
	state.failure = terminalErr
	receipt, commitErr := p.manager.committer.CommitEvents(
		context.Background(),
		[]event.Draft{event.NewErrorDraft(
			string(p.id),
			string(p.id),
			terminalErr.Error(),
		)},
	)
	if commitErr != nil {
		return errors.Join(
			terminalErr,
			fmt.Errorf("session: persist terminal error: %w", commitErr),
		)
	}
	state.lastSeq = receipt.LastSeq
	if fatal {
		p.manager.committer.Fail(terminalErr)
	}
	return terminalErr
}

func (p *outputProcessor) applyResize(
	state *outputProcessorState,
	size term.Size,
) error {
	if state.ended {
		return errOutputStreamEnded
	}
	if sameTerminalSize(size, state.effectiveSize) {
		return nil
	}
	if p.running.terminal == nil {
		return errors.New("session: terminal is unavailable for resize")
	}
	rows := size.Rows()
	columns := size.Columns()
	if rows <= 0 || columns <= 0 || rows > 1<<16-1 || columns > 1<<16-1 {
		return errors.New("session: terminal resize requires a validated size")
	}
	draft, err := event.NewAgentResizedDraft(
		string(p.id),
		string(p.id),
		uint16(rows),
		uint16(columns),
		state.nextOutputOffset,
	)
	if err != nil {
		return fmt.Errorf("session: create terminal resize: %w", err)
	}
	if err := p.running.terminal.Resize(context.Background(), size); err != nil {
		if terminalResizeWasApplied(err) {
			return p.failTerminal(state, "apply resize", err, true)
		}
		return err
	}
	receipt, err := p.manager.committer.CommitEvents(
		context.Background(),
		[]event.Draft{draft},
	)
	if err != nil {
		resizeErr := fmt.Errorf(
			"session: commit applied terminal resize: %w",
			err,
		)
		state.failure = resizeErr
		p.manager.committer.Fail(resizeErr)
		return resizeErr
	}
	state.effectiveSize = size
	state.lastSeq = receipt.LastSeq
	return nil
}

func sameTerminalSize(left, right term.Size) bool {
	return left.Rows() == right.Rows() && left.Columns() == right.Columns()
}

func (p *outputProcessor) canObserve() bool {
	p.manager.mu.RLock()
	defer p.manager.mu.RUnlock()
	current, attached := p.manager.sessions[p.id]
	return attached &&
		current == p.running &&
		!p.running.exitClaimed &&
		p.running.observer != nil
}

type streamingRedactor struct {
	token   []byte
	mask    []byte
	pending []byte
}

func newStreamingRedactor(token []byte) streamingRedactor {
	copiedToken := append([]byte(nil), token...)
	mask := make([]byte, len(copiedToken))
	copy(mask, []byte("[REDACTED]"))
	for index := len("[REDACTED]"); index < len(mask); index++ {
		mask[index] = '*'
	}
	return streamingRedactor{
		token: copiedToken,
		mask:  mask,
	}
}

func (r *streamingRedactor) Feed(input []byte) []byte {
	return r.process(input, false)
}

func (r *streamingRedactor) Flush() []byte {
	output := r.process(nil, true)
	r.pending = nil
	return output
}

func (r *streamingRedactor) process(input []byte, final bool) []byte {
	if len(r.token) == 0 {
		return append([]byte(nil), input...)
	}

	data := make([]byte, 0, len(r.pending)+len(input))
	data = append(data, r.pending...)
	data = append(data, input...)
	r.pending = nil

	output := make([]byte, 0, len(data))
	for len(data) > 0 {
		index := bytes.Index(data, r.token)
		if index >= 0 {
			output = append(output, data[:index]...)
			output = append(output, r.mask...)
			data = data[index+len(r.token):]
			continue
		}
		if final {
			output = append(output, data...)
			break
		}
		prefix := longestTokenPrefixSuffix(data, r.token)
		output = append(output, data[:len(data)-prefix]...)
		r.pending = append(r.pending[:0], data[len(data)-prefix:]...)
		break
	}
	return output
}

func longestTokenPrefixSuffix(data, token []byte) int {
	limit := min(len(data), len(token)-1)
	for length := limit; length > 0; length-- {
		if bytes.Equal(data[len(data)-length:], token[:length]) {
			return length
		}
	}
	return 0
}

func splitOutputChunks(data []byte) [][]byte {
	chunks := make([][]byte, 0, len(data)/event.MaxOutputChunkBytes+1)
	for len(data) > event.MaxOutputChunkBytes {
		size := completeOutputPrefix(data, event.MaxOutputChunkBytes)
		chunks = append(chunks, data[:size])
		data = data[size:]
	}
	if len(data) > 0 {
		chunks = append(chunks, data)
	}
	return chunks
}

func completeOutputPrefix(data []byte, limit int) int {
	lastComplete := 0
	for index := 0; index < limit; {
		r, size := utf8.DecodeRune(data[index:])
		if r != utf8.RuneError || size != 1 {
			if index+size > limit {
				return lastComplete
			}
			index += size
			lastComplete = index
			continue
		}
		index++
		lastComplete = index
	}
	return lastComplete
}
