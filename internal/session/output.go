package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/term"
)

type outputProcessor struct {
	manager *Manager
	id      agent.ID
	running *runningSession

	redactor streamingRedactor

	nextSourceOffset uint64
	nextOutputOffset uint64
	pendingActivity  int
	ended            bool
	failed           bool
}

func newOutputProcessor(
	manager *Manager,
	id agent.ID,
	running *runningSession,
	signalToken string,
) *outputProcessor {
	return &outputProcessor{
		manager:  manager,
		id:       id,
		running:  running,
		redactor: newStreamingRedactor([]byte(signalToken)),
	}
}

func (p *outputProcessor) Feed(chunk []byte, offset uint64) error {
	if p.ended {
		return fmt.Errorf("session: output received after stream end")
	}
	if p.failed {
		return fmt.Errorf("session: output processor has failed")
	}
	if len(chunk) == 0 {
		return fmt.Errorf("session: empty PTY output callback")
	}
	if offset != p.nextSourceOffset {
		return p.failOffset("chunk", offset)
	}

	p.nextSourceOffset += uint64(len(chunk))
	p.pendingActivity++
	output := p.redactor.Feed(chunk)
	if len(output) == 0 {
		return nil
	}
	return p.commitAndObserve(output)
}

func (p *outputProcessor) End(offset uint64) error {
	if p.ended {
		return fmt.Errorf("session: output stream ended more than once")
	}
	if p.failed {
		return fmt.Errorf("session: output processor has failed")
	}
	if offset != p.nextSourceOffset {
		return p.failOffset("end", offset)
	}
	p.ended = true

	output := p.redactor.Flush()
	if len(output) > 0 {
		if err := p.commitAndObserve(output); err != nil {
			return err
		}
	}
	if p.running.terminal != nil {
		if err := p.running.terminal.EndOutput(
			context.Background(),
			p.nextOutputOffset,
		); err != nil {
			return p.failTerminal("end output", err, false)
		}
	}
	return nil
}

func (p *outputProcessor) failOffset(callback string, got uint64) error {
	p.failed = true
	streamErr := fmt.Errorf(
		"session: PTY output %s offset mismatch: got %d, want %d",
		callback,
		got,
		p.nextSourceOffset,
	)
	_, commitErr := p.manager.committer.CommitEvents(
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
	return streamErr
}

func (p *outputProcessor) commitAndObserve(output []byte) error {
	drafts := make([]event.Draft, 0, len(output)/event.MaxOutputChunkBytes+1)
	offset := p.nextOutputOffset
	for _, chunk := range splitOutputChunks(output) {
		draft, err := event.NewOutputChunkDraft(
			string(p.id),
			string(p.id),
			offset,
			chunk,
		)
		if err != nil {
			p.failed = true
			return fmt.Errorf("session: create output chunk at offset %d: %w", offset, err)
		}
		drafts = append(drafts, draft)
		offset += uint64(len(chunk))
	}
	receipt, err := p.manager.committer.CommitEvents(context.Background(), drafts)
	if err != nil {
		p.failed = true
		return fmt.Errorf("session: commit output at offset %d: %w", p.nextOutputOffset, err)
	}

	p.nextOutputOffset = offset
	if p.running.terminal != nil {
		chunk, err := term.NewCommittedChunk(
			output,
			p.nextOutputOffset,
			receipt.LastSeq,
			receipt.Timestamp,
		)
		if err != nil {
			return p.failTerminal("create committed chunk", err, true)
		}
		if err := p.running.terminal.FeedCommitted(
			context.Background(),
			chunk,
		); err != nil {
			return p.failTerminal("feed committed output", err, true)
		}
	}
	for range p.pendingActivity {
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
	p.pendingActivity = 0
	return nil
}

func (p *outputProcessor) failTerminal(
	operation string,
	cause error,
	fatal bool,
) error {
	p.failed = true
	terminalErr := fmt.Errorf("session: terminal %s: %w", operation, cause)
	_, commitErr := p.manager.committer.CommitEvents(
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
	if fatal {
		p.manager.committer.Fail(terminalErr)
	}
	return terminalErr
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
