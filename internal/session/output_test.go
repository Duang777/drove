package session

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Duang777/drove/internal/adapter"
	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/store"
)

func TestStreamingRedactorMasksTokenAtEverySplit(t *testing.T) {
	token := []byte(testSignalToken)
	wantMask := newStreamingRedactor(token).mask

	for split := 0; split <= len(token); split++ {
		t.Run(fmt.Sprintf("split-%d", split), func(t *testing.T) {
			redactor := newStreamingRedactor(token)
			var output []byte
			output = append(output, redactor.Feed(token[:split])...)
			output = append(output, redactor.Feed(token[split:])...)
			output = append(output, redactor.Feed([]byte(" tail"))...)
			output = append(output, redactor.Flush()...)

			want := append(append([]byte(nil), wantMask...), []byte(" tail")...)
			if !bytes.Equal(output, want) {
				t.Fatalf("redacted output = %q, want %q", output, want)
			}
			if len(output) != len(token)+len(" tail") {
				t.Fatalf("redacted length = %d, want %d", len(output), len(token)+len(" tail"))
			}
			if bytes.Contains(output, token) {
				t.Fatalf("redacted output contains token: %q", output)
			}
		})
	}
}

func TestOutputProcessorRedactsTokenAcrossAllOutputPaths(t *testing.T) {
	manager, st := newTestManager(t)
	id := agent.ID("redacted-output")
	running := attachOutputOnlyRuntime(manager, id, testSignalToken)
	subscription := manager.hub.Subscribe(4)
	defer manager.hub.Unsubscribe(subscription)

	split := len(testSignalToken) / 2
	first := []byte("prefix " + testSignalToken[:split])
	second := []byte(testSignalToken[split:] + " suffix\n")
	if err := running.output.Feed(first, 0); err != nil {
		t.Fatalf("feed first token half: %v", err)
	}
	if err := running.output.Feed(second, uint64(len(first))); err != nil {
		t.Fatalf("feed second token half: %v", err)
	}
	if err := running.output.End(uint64(len(first) + len(second))); err != nil {
		t.Fatalf("end output: %v", err)
	}

	var live []event.Event
	for {
		select {
		case streamed := <-subscription.C():
			live = append(live, streamed)
		default:
			goto liveDone
		}
	}
liveDone:
	if len(live) == 0 {
		t.Fatal("Hub received no output events")
	}

	stored, err := st.Replay(string(id))
	if err != nil {
		t.Fatalf("store replay: %v", err)
	}
	replayed, err := manager.Replay(string(id))
	if err != nil {
		t.Fatalf("manager replay: %v", err)
	}

	want := "prefix " +
		string(newStreamingRedactor([]byte(testSignalToken)).mask) +
		" suffix\n"
	if got := string(joinOutputRows(t, replayed)); got != want {
		t.Fatalf("replayed output = %q, want %q", got, want)
	}
	if len(joinOutputRows(t, replayed)) != len(first)+len(second) {
		t.Fatalf("redacted replay changed byte length")
	}

	assertTokenAbsentFromRows(t, testSignalToken, stored)
	assertTokenAbsentFromRows(t, testSignalToken, replayed)
	for _, streamed := range live {
		if strings.Contains(streamed.Payload, testSignalToken) {
			t.Fatalf("Hub payload contains token: %+v", streamed)
		}
		if streamed.Type == event.TypeOutputChunk {
			payload, decodeErr := event.DecodeOutputChunkPayload(streamed.Payload)
			if decodeErr != nil {
				t.Fatalf("decode Hub output: %v", decodeErr)
			}
			data, decodeErr := payload.DecodeData()
			if decodeErr != nil {
				t.Fatalf("decode Hub data: %v", decodeErr)
			}
			if bytes.Contains(data, []byte(testSignalToken)) {
				t.Fatalf("Hub output contains token: %q", data)
			}
		}
	}
}

func TestOutputProcessorSplitsUTF8ChunksWithContiguousOffsets(t *testing.T) {
	manager, _ := newTestManager(t)
	id := agent.ID("chunked-output")
	running := attachOutputOnlyRuntime(manager, id, "")
	data := bytes.Repeat([]byte("界"), event.MaxOutputChunkBytes)

	if err := running.output.Feed(data, 0); err != nil {
		t.Fatalf("feed output: %v", err)
	}
	if err := running.output.End(uint64(len(data))); err != nil {
		t.Fatalf("end output: %v", err)
	}

	rows, err := manager.Replay(string(id))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	var offset uint64
	var chunks int
	for _, row := range rows {
		if row.Type != string(event.TypeOutputChunk) {
			continue
		}
		payload, decodeErr := event.DecodeOutputChunkPayload(row.Payload)
		if decodeErr != nil {
			t.Fatalf("decode output metadata: %v", decodeErr)
		}
		chunk, decodeErr := payload.DecodeData()
		if decodeErr != nil {
			t.Fatalf("decode output data: %v", decodeErr)
		}
		if payload.Offset != offset {
			t.Fatalf("chunk offset = %d, want %d", payload.Offset, offset)
		}
		if len(chunk) > event.MaxOutputChunkBytes {
			t.Fatalf("chunk length = %d, max %d", len(chunk), event.MaxOutputChunkBytes)
		}
		if !utf8.Valid(chunk) {
			t.Fatalf("chunk at offset %d split a UTF-8 code point", offset)
		}
		offset += uint64(len(chunk))
		chunks++
	}
	if chunks < 2 {
		t.Fatalf("output chunk count = %d, want multiple chunks", chunks)
	}
	if offset != uint64(len(data)) {
		t.Fatalf("final output offset = %d, want %d", offset, len(data))
	}
	if got := joinOutputRows(t, rows); !bytes.Equal(got, data) {
		t.Fatalf("joined output length = %d, want %d", len(got), len(data))
	}
}

func TestOutputProcessorDerivesBoundedPlainLinesAtEnd(t *testing.T) {
	manager, _ := newTestManager(t)
	heuristic := &recordingHeuristic{}
	entry := adapter.Entry{Heuristic: heuristic}
	id := agent.ID("derived-lines")
	a := agent.New(
		id,
		agent.WithName("derived-lines"),
		agent.WithVendor("test"),
		agent.WithRunMode(agent.RunModeInteractive),
		agent.WithHookPolicy(agent.HooksOff),
	)
	manager.mu.Lock()
	manager.agents[id] = a
	manager.mu.Unlock()
	commitTestState(t, manager, a, agent.StateStarting, "test start")
	commitTestState(t, manager, a, agent.StateWorking, "test working")
	running := attachTestRuntime(t, manager, a, entry)

	first := []byte("old\x1b]0;hidden\x1b")
	second := append([]byte{'\\'}, bytes.Repeat([]byte("n"), maxDerivedLineBytes+17)...)
	second = append(second, '\n')
	second = append(second, []byte("final\r")...)
	if err := running.output.Feed(first, 0); err != nil {
		t.Fatalf("feed first control fragment: %v", err)
	}
	if err := running.output.Feed(second, uint64(len(first))); err != nil {
		t.Fatalf("feed second control fragment: %v", err)
	}
	if err := running.output.End(uint64(len(first) + len(second))); err != nil {
		t.Fatalf("end output: %v", err)
	}

	lines := heuristic.Lines()
	if len(lines) != 2 {
		t.Fatalf("derived lines = %d, want 2: %q", len(lines), lines)
	}
	if lines[0] != strings.Repeat("n", maxDerivedLineBytes) {
		t.Fatalf("bounded line length = %d, want %d", len(lines[0]), maxDerivedLineBytes)
	}
	if lines[1] != "final" {
		t.Fatalf("final line = %q, want final", lines[1])
	}
	for _, line := range lines {
		if strings.Contains(line, "hidden") || strings.ContainsRune(line, '\x1b') {
			t.Fatalf("derived line retained terminal control text: %q", line)
		}
	}
}

func TestOutputProcessorDefersActivityUntilBufferedBytesCommit(t *testing.T) {
	manager, _ := newTestManager(t)
	id := agent.ID("pending-activity")
	a := agent.New(
		id,
		agent.WithName("pending-activity"),
		agent.WithVendor("generic"),
		agent.WithRunMode(agent.RunModeInteractive),
		agent.WithHookPolicy(agent.HooksOff),
	)
	manager.mu.Lock()
	manager.agents[id] = a
	manager.mu.Unlock()
	commitTestState(t, manager, a, agent.StateStarting, "test start")
	commitTestState(t, manager, a, agent.StateWorking, "test working")
	running := attachTestRuntime(t, manager, a, manager.reg.For("generic"))
	running.output = newOutputProcessor(
		manager,
		id,
		running,
		manager.reg.For("generic"),
		testSignalToken,
	)

	first := []byte(testSignalToken[:8])
	if err := running.output.Feed(first, 0); err != nil {
		t.Fatalf("feed token prefix: %v", err)
	}
	rows, err := manager.Replay(string(id))
	if err != nil {
		t.Fatalf("replay buffered prefix: %v", err)
	}
	if countOutputActivity(rows) != 0 || len(joinOutputRows(t, rows)) != 0 {
		t.Fatalf("buffered prefix produced output or activity: %+v", rows)
	}

	second := []byte(testSignalToken[8:] + "\n")
	if err := running.output.Feed(second, uint64(len(first))); err != nil {
		t.Fatalf("feed token suffix: %v", err)
	}
	rows, err = manager.Replay(string(id))
	if err != nil {
		t.Fatalf("replay committed token: %v", err)
	}
	if got := countOutputActivity(rows); got != 2 {
		t.Fatalf("output activity count = %d, want 2", got)
	}
}

func TestOutputProcessorOffsetMismatchPersistsBoundedErrorOnly(t *testing.T) {
	tests := []struct {
		name string
		run  func(*outputProcessor) error
	}{
		{
			name: "chunk",
			run: func(processor *outputProcessor) error {
				return processor.Feed([]byte("never persist"), 99)
			},
		},
		{
			name: "end",
			run: func(processor *outputProcessor) error {
				return processor.End(99)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, _ := newTestManager(t)
			id := agent.ID("offset-" + test.name)
			running := attachOutputOnlyRuntime(manager, id, "")

			err := test.run(running.output)
			if err == nil || !strings.Contains(err.Error(), "offset mismatch") {
				t.Fatalf("offset error = %v", err)
			}
			rows, replayErr := manager.Replay(string(id))
			if replayErr != nil {
				t.Fatalf("replay: %v", replayErr)
			}
			if len(rows) != 1 || rows[0].Type != string(event.TypeError) {
				t.Fatalf("rows after mismatch = %+v, want one error", rows)
			}
			if len(rows[0].Payload) > 160 {
				t.Fatalf("offset error length = %d, want bounded", len(rows[0].Payload))
			}
			if strings.Contains(rows[0].Payload, "never persist") {
				t.Fatalf("offset error retained output bytes: %q", rows[0].Payload)
			}
		})
	}
}

func TestOutputProcessorStoreFailurePrecedesPublicationAndObservation(t *testing.T) {
	storageErr := errors.New("disk unavailable")
	st := &memoryCommitStore{appendErr: storageErr}
	hub := event.NewHub(0)
	committer := newCommitter(0, st, hub)
	clock := &countingOutputClock{}
	manager := &Manager{
		hub:       hub,
		committer: committer,
		clock:     clock,
		agents:    make(map[agent.ID]*agent.Agent),
		sessions:  make(map[agent.ID]*runningSession),
	}
	a := agent.New(
		"store-failure",
		agent.WithVendor("test"),
		agent.WithHookPolicy(agent.HooksOff),
	)
	observer, err := newObservationActor(
		a,
		committer,
		agent.HooksOff,
		detect.DefaultConfig(),
		clock,
	)
	if err != nil {
		t.Fatalf("new observation actor: %v", err)
	}
	t.Cleanup(func() {
		observer.Close()
		committer.Close()
	})
	heuristic := &recordingHeuristic{}
	running := &runningSession{
		process:  &fakeProcessSession{},
		observer: observer,
		vendor:   "test",
	}
	running.output = newOutputProcessor(
		manager,
		a.ID(),
		running,
		adapter.Entry{Heuristic: heuristic},
		"",
	)
	manager.sessions[a.ID()] = running
	subscription := hub.Subscribe(1)
	defer hub.Unsubscribe(subscription)

	err = running.output.Feed([]byte("line\n"), 0)
	if !errors.Is(err, storageErr) {
		t.Fatalf("feed error = %v, want storage error", err)
	}
	if got := clock.Calls(); got != 0 {
		t.Fatalf("observation clock calls = %d, want 0", got)
	}
	if lines := heuristic.Lines(); len(lines) != 0 {
		t.Fatalf("heuristic lines after failed store = %q", lines)
	}
	select {
	case published := <-subscription.C():
		t.Fatalf("published event after failed store: %+v", published)
	default:
	}
	select {
	case fatal := <-committer.Fatal():
		if !errors.Is(fatal, storageErr) {
			t.Fatalf("fatal error = %v, want storage error", fatal)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for fatal store error")
	}
}

func TestOutputProcessorConcurrentSessions(t *testing.T) {
	manager, _ := newTestManager(t)
	const (
		sessionCount = 8
		chunkCount   = 20
	)

	var wait sync.WaitGroup
	errs := make(chan error, sessionCount)
	for sessionIndex := range sessionCount {
		id := agent.ID(fmt.Sprintf("concurrent-%d", sessionIndex))
		running := attachOutputOnlyRuntime(manager, id, "")
		wait.Add(1)
		go func() {
			defer wait.Done()
			var offset uint64
			for chunkIndex := range chunkCount {
				chunk := []byte(fmt.Sprintf("%s:%02d\n", id, chunkIndex))
				if err := running.output.Feed(chunk, offset); err != nil {
					errs <- err
					return
				}
				offset += uint64(len(chunk))
			}
			if err := running.output.End(offset); err != nil {
				errs <- err
			}
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent output: %v", err)
	}

	for sessionIndex := range sessionCount {
		id := agent.ID(fmt.Sprintf("concurrent-%d", sessionIndex))
		rows, err := manager.Replay(string(id))
		if err != nil {
			t.Fatalf("replay %s: %v", id, err)
		}
		var want strings.Builder
		for chunkIndex := range chunkCount {
			fmt.Fprintf(&want, "%s:%02d\n", id, chunkIndex)
		}
		if got := string(joinOutputRows(t, rows)); got != want.String() {
			t.Fatalf("output for %s = %q, want %q", id, got, want.String())
		}
	}
}

func attachOutputOnlyRuntime(
	manager *Manager,
	id agent.ID,
	token string,
) *runningSession {
	running := &runningSession{
		process: &fakeProcessSession{},
		vendor:  "generic",
	}
	running.output = newOutputProcessor(
		manager,
		id,
		running,
		manager.reg.For("generic"),
		token,
	)
	manager.mu.Lock()
	manager.sessions[id] = running
	manager.mu.Unlock()
	return running
}

func joinOutputRows(t *testing.T, rows []store.EventRow) []byte {
	t.Helper()

	var output []byte
	for _, row := range rows {
		if row.Type == string(event.TypeOutputChunk) {
			output = append(output, outputChunkData(t, row)...)
		}
	}
	return output
}

func assertTokenAbsentFromRows(t *testing.T, token string, rows []store.EventRow) {
	t.Helper()

	for _, row := range rows {
		for _, value := range []string{
			row.Type,
			row.SessionID,
			row.AgentID,
			row.From,
			row.To,
			row.Reason,
			row.Payload,
		} {
			if strings.Contains(value, token) {
				t.Fatalf("event row at seq %d contains token", row.Seq)
			}
		}
		if bytes.Contains(row.OutputAttachment, []byte(token)) {
			t.Fatalf("event attachment at seq %d contains token", row.Seq)
		}
		if row.Type == string(event.TypeOutputChunk) {
			payload, err := event.DecodeOutputChunkPayload(row.Payload)
			if err != nil {
				t.Fatalf("decode output chunk at seq %d: %v", row.Seq, err)
			}
			if payload.DataB64 != "" {
				data, decodeErr := payload.DecodeData()
				if decodeErr != nil {
					t.Fatalf("decode output data at seq %d: %v", row.Seq, decodeErr)
				}
				if bytes.Contains(data, []byte(token)) {
					t.Fatalf("output chunk at seq %d contains token", row.Seq)
				}
			}
		}
	}
}

func countOutputActivity(rows []store.EventRow) int {
	count := 0
	for _, row := range rows {
		if row.Type == string(event.TypeAgentSignal) &&
			strings.Contains(row.Payload, `"vendor_event":"output_activity"`) {
			count++
		}
	}
	return count
}

type recordingHeuristic struct {
	mu    sync.Mutex
	lines []string
}

func (h *recordingHeuristic) Classify(line string) (adapter.OutputHint, bool) {
	h.mu.Lock()
	h.lines = append(h.lines, line)
	h.mu.Unlock()
	return adapter.OutputHint{}, false
}

func (h *recordingHeuristic) Lines() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.lines...)
}

type countingOutputClock struct {
	calls atomic.Int64
}

func (c *countingOutputClock) Now() time.Time {
	c.calls.Add(1)
	return time.Now().UTC()
}

func (c *countingOutputClock) NewTimer(delay time.Duration) observationTimer {
	return systemObservationClock{}.NewTimer(delay)
}

func (c *countingOutputClock) Calls() int64 {
	return c.calls.Load()
}
