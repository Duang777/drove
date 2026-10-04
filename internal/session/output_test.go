package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/store"
	"github.com/Duang777/drove/internal/term"
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
	manager.agents[id] = newManagedAgent(a)
	manager.mu.Unlock()
	commitTestState(t, manager, a, agent.StateStarting, "test start")
	commitTestState(t, manager, a, agent.StateWorking, "test working")
	running := attachTestRuntime(t, manager, a, manager.reg.For("generic"))
	running.output = newOutputProcessor(
		manager,
		id,
		running,
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
		agents:    make(map[agent.ID]*managedAgent),
		sessions:  make(map[agent.ID]*runningSession),
	}
	a := agent.New(
		"store-failure",
		agent.WithVendor("test"),
		agent.WithHookPolicy(agent.HooksOff),
	)
	observer, err := newManagedObservationActor(
		newManagedAgent(a),
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
	running := &runningSession{
		process:  &fakeProcessSession{},
		observer: observer,
		vendor:   "test",
	}
	running.output = newOutputProcessor(
		manager,
		a.ID(),
		running,
		"",
	)
	terminalClock := newTerminalTestClock(time.Unix(1, 0).UTC())
	running.terminal = newTerminalTestActor(
		t,
		"generic",
		terminalClock,
		&terminalTestProcess{},
		observer,
	)
	defer running.terminal.Close()
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
	terminalClock.mu.Lock()
	timerCount := len(terminalClock.timers)
	terminalClock.mu.Unlock()
	if timerCount != 0 {
		t.Fatalf("terminal timers after failed store = %d, want 0", timerCount)
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

func TestOutputProcessorScreenEvidenceReferencesCommittedOutput(t *testing.T) {
	manager, _ := newTestManager(t)
	clock := newTerminalTestClock(time.Unix(10, 0).UTC())
	manager.clock = clock
	id := agent.ID("screen-ordering")
	a := agent.New(
		id,
		agent.WithName("screen-ordering"),
		agent.WithVendor("claude"),
		agent.WithRunMode(agent.RunModeInteractive),
		agent.WithHookPolicy(agent.HooksOff),
	)
	manager.mu.Lock()
	manager.agents[id] = newManagedAgent(a)
	manager.mu.Unlock()
	commitTestState(t, manager, a, agent.StateStarting, "test start")
	commitTestState(t, manager, a, agent.StateWorking, "test working")
	running := attachTestRuntime(t, manager, a, manager.reg.For("claude"))
	terminalProcess := &terminalTestProcess{}
	terminalActor, err := newTerminalActor(
		mustInitialTerminalSize(t),
		terminalProcess,
		running.classifier,
		running.observer,
		running.vendor,
		clock,
		func(actorErr error) {
			manager.failTerminalActor(id, actorErr)
		},
	)
	if err != nil {
		t.Fatalf("new terminal actor: %v", err)
	}
	running.terminal = terminalActor

	output := []byte("\x1b[2J\x1b[30;1HDo you want to proceed?\r\nEsc to cancel")
	if err := running.output.Feed(output, 0); err != nil {
		t.Fatalf("feed output: %v", err)
	}

	rows, err := manager.Replay(string(id))
	if err != nil {
		t.Fatalf("replay before sample: %v", err)
	}
	var outputSeq uint64
	for _, row := range rows {
		if row.Type == string(event.TypeOutputChunk) {
			outputSeq = row.Seq
		}
		if signalPayloadVersion(row.Payload) == 3 {
			t.Fatalf("screen signal committed before sample: %+v", row)
		}
	}
	if outputSeq == 0 {
		t.Fatal("output did not commit before terminal sampling")
	}

	clock.advance(terminalSampleWait)
	clock.timerAt(t, 0).fire(clock.Now())
	screenRow, payload := waitForScreenSignal(t, manager, id)
	if screenRow.Seq <= outputSeq {
		t.Fatalf("screen seq = %d, output seq = %d", screenRow.Seq, outputSeq)
	}
	if payload.Screen == nil ||
		payload.Screen.OutputOffset != uint64(len(output)) ||
		payload.Screen.LastOutputSeq != outputSeq {
		t.Fatalf("screen attribution = %+v, output seq = %d", payload.Screen, outputSeq)
	}
	if got := a.State(); got != agent.StateWorking {
		t.Fatalf("state before screen confirmation = %s, want working", got)
	}

	screenTimer := clock.timerAt(t, 1)
	if delay, _ := screenTimer.state(); delay != 750*time.Millisecond {
		t.Fatalf("screen confirmation delay = %s, want 750ms", delay)
	}
	clock.advance(750 * time.Millisecond)
	screenTimer.fire(clock.Now())
	waitForState(t, manager, id, agent.StateBlocked)
}

func TestOutputProcessorOrdinaryErrorCreatesNoScreenEdge(t *testing.T) {
	manager, _ := newTestManager(t)
	clock := newTerminalTestClock(time.Unix(20, 0).UTC())
	manager.clock = clock
	id := agent.ID("ordinary-error")
	a := agent.New(
		id,
		agent.WithName("ordinary-error"),
		agent.WithVendor("claude"),
		agent.WithRunMode(agent.RunModeInteractive),
		agent.WithHookPolicy(agent.HooksOff),
	)
	manager.mu.Lock()
	manager.agents[id] = newManagedAgent(a)
	manager.mu.Unlock()
	commitTestState(t, manager, a, agent.StateStarting, "test start")
	commitTestState(t, manager, a, agent.StateWorking, "test working")
	running := attachTestRuntime(t, manager, a, manager.reg.For("claude"))
	terminalActor, err := newTerminalActor(
		mustInitialTerminalSize(t),
		&terminalTestProcess{},
		running.classifier,
		running.observer,
		running.vendor,
		clock,
		nil,
	)
	if err != nil {
		t.Fatalf("new terminal actor: %v", err)
	}
	running.terminal = terminalActor

	output := []byte("\x1b[2J\x1b[30;1HError: compilation failed")
	if err := running.output.Feed(output, 0); err != nil {
		t.Fatalf("feed ordinary error: %v", err)
	}
	clock.advance(terminalSampleWait)
	clock.timerAt(t, 0).fire(clock.Now())
	waitForTerminalSnapshot(t, terminalActor)

	rows, err := manager.Replay(string(id))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	for _, row := range rows {
		if signalPayloadVersion(row.Payload) == 3 {
			t.Fatalf("ordinary error created screen edge: %+v", row)
		}
	}
	if got := a.State(); got != agent.StateWorking {
		t.Fatalf("ordinary error changed state to %s", got)
	}
}

func TestOutputProcessorCommittedFeedFailurePreservesOutputAndFailsClosed(t *testing.T) {
	manager, _ := newTestManager(t)
	id := agent.ID("terminal-feed-failure")
	running := attachOutputOnlyRuntime(manager, id, "")
	terminalActor := newTerminalTestActor(
		t,
		"generic",
		newTerminalTestClock(time.Unix(30, 0).UTC()),
		&terminalTestProcess{},
		&recordingTerminalObserver{},
	)
	if err := terminalActor.Close(); err != nil {
		t.Fatalf("close terminal actor: %v", err)
	}
	running.terminal = terminalActor

	output := []byte("durable before terminal failure")
	err := running.output.Feed(output, 0)
	if !errors.Is(err, errTerminalActorClosed) {
		t.Fatalf("feed error = %v, want terminal actor closed", err)
	}

	rows, replayErr := manager.Replay(string(id))
	if replayErr != nil {
		t.Fatalf("replay: %v", replayErr)
	}
	if got := joinOutputRows(t, rows); !bytes.Equal(got, output) {
		t.Fatalf("durable output = %q, want %q", got, output)
	}
	errorRows := 0
	for _, row := range rows {
		if row.Type == string(event.TypeError) {
			errorRows++
		}
	}
	if errorRows != 1 {
		t.Fatalf("error rows = %d, want 1", errorRows)
	}
	select {
	case fatal := <-manager.Fatal():
		if !errors.Is(fatal, errTerminalActorClosed) {
			t.Fatalf("fatal error = %v, want terminal actor closed", fatal)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for terminal fail-stop")
	}
}

func TestOutputProcessorPersistsQueryReplyFailureWithoutInputAudit(t *testing.T) {
	manager, _ := newTestManager(t)
	id := agent.ID("terminal-reply-failure")
	running := attachOutputOnlyRuntime(manager, id, "")
	terminalActor := newTerminalTestActor(
		t,
		"generic",
		newTerminalTestClock(time.Unix(40, 0).UTC()),
		&terminalTestProcess{writeLimit: 1},
		&recordingTerminalObserver{},
	)
	running.terminal = terminalActor

	query := []byte("\x1b[6n")
	if err := running.output.Feed(query, 0); err != nil {
		t.Fatalf("feed query: %v", err)
	}
	err := running.output.End(uint64(len(query)))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("end output error = %v, want short write", err)
	}

	rows, replayErr := manager.Replay(string(id))
	if replayErr != nil {
		t.Fatalf("replay: %v", replayErr)
	}
	var outputRows, errorRows, inputRows int
	for _, row := range rows {
		switch row.Type {
		case string(event.TypeOutputChunk):
			outputRows++
		case string(event.TypeError):
			errorRows++
		case string(event.TypeAgentInput):
			inputRows++
		}
	}
	if outputRows != 1 || errorRows != 1 || inputRows != 0 {
		t.Fatalf(
			"rows output=%d error=%d input=%d, want 1/1/0",
			outputRows,
			errorRows,
			inputRows,
		)
	}
	select {
	case fatal := <-manager.Fatal():
		t.Fatalf("query reply failure triggered fail-stop: %v", fatal)
	default:
	}
}

func TestOutputProcessorSerializesOutputAndEffectiveResize(t *testing.T) {
	manager, _ := newTestManager(t)
	id := agent.ID("ordered-resize")
	running := attachOutputOnlyRuntime(manager, id, "")
	process := &terminalTestProcess{}
	running.process = process
	running.terminal = newTerminalTestActor(
		t,
		"generic",
		newTerminalTestClock(time.Unix(50, 0).UTC()),
		process,
		&recordingTerminalObserver{},
	)

	first := []byte("before")
	second := []byte("after")
	if err := running.output.Feed(first, 0); err != nil {
		t.Fatalf("feed before resize: %v", err)
	}
	size, err := term.NewSize(24, 80)
	if err != nil {
		t.Fatalf("new resize: %v", err)
	}
	if err := running.output.Resize(context.Background(), size); err != nil {
		t.Fatalf("resize: %v", err)
	}
	if err := running.output.Feed(second, uint64(len(first))); err != nil {
		t.Fatalf("feed after resize: %v", err)
	}
	if err := running.output.End(uint64(len(first) + len(second))); err != nil {
		t.Fatalf("end output: %v", err)
	}

	rows, err := manager.Replay(string(id))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	var recordingTypes []string
	for _, row := range rows {
		switch event.Type(row.Type) {
		case event.TypeOutputChunk:
			recordingTypes = append(recordingTypes, row.Type)
		case event.TypeAgentResized:
			recordingTypes = append(recordingTypes, row.Type)
			payload, decodeErr := event.DecodeAgentResizedPayload(row.Payload)
			if decodeErr != nil {
				t.Fatalf("decode resize: %v", decodeErr)
			}
			if payload.Rows != 24 ||
				payload.Columns != 80 ||
				payload.OutputOffset != uint64(len(first)) {
				t.Fatalf("resize payload = %+v", payload)
			}
		}
	}
	wantTypes := []string{
		string(event.TypeOutputChunk),
		string(event.TypeAgentResized),
		string(event.TypeOutputChunk),
	}
	if !slices.Equal(recordingTypes, wantTypes) {
		t.Fatalf("recording types = %v, want %v", recordingTypes, wantTypes)
	}
	if got := process.Resizes(); !slices.Equal(
		got,
		[][2]uint16{{24, 80}},
	) {
		t.Fatalf("PTY resizes = %v", got)
	}
	snapshot, available := running.terminal.Snapshot()
	if !available || !sameTerminalSize(snapshot.Size(), size) {
		t.Fatalf("terminal size = %+v, available=%t", snapshot.Size(), available)
	}
}

func TestOutputProcessorCloseWaitsForAdmittedRequests(t *testing.T) {
	manager, _ := newTestManager(t)
	id := agent.ID("ordered-close")
	running := attachOutputOnlyRuntime(manager, id, "")
	resizeStarted := make(chan struct{})
	resizeRelease := make(chan struct{})
	process := &terminalTestProcess{
		resizeStarted: resizeStarted,
		resizeRelease: resizeRelease,
	}
	running.process = process
	running.terminal = newTerminalTestActor(
		t,
		"generic",
		newTerminalTestClock(time.Unix(55, 0).UTC()),
		process,
		&recordingTerminalObserver{},
	)
	size, err := term.NewSize(24, 80)
	if err != nil {
		t.Fatalf("new resize: %v", err)
	}

	resizeDone := make(chan error, 1)
	go func() {
		resizeDone <- running.output.Resize(context.Background(), size)
	}()
	select {
	case <-resizeStarted:
	case <-time.After(time.Second):
		t.Fatal("resize did not reach the PTY")
	}

	feedDone := make(chan error, 1)
	go func() {
		feedDone <- running.output.Feed([]byte("queued"), 0)
	}()
	deadline := time.Now().Add(time.Second)
	for len(running.output.requests) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("feed was not admitted behind resize")
		}
		time.Sleep(time.Millisecond)
	}

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- running.output.Close()
	}()
	select {
	case err := <-closeDone:
		t.Fatalf("close returned before admitted requests: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	close(resizeRelease)

	for name, result := range map[string]<-chan error{
		"resize": resizeDone,
		"feed":   feedDone,
		"close":  closeDone,
	} {
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("%s result: %v", name, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s request did not receive a result", name)
		}
	}
	if err := running.output.Feed([]byte("late"), 6); !errors.Is(
		err,
		errOutputProcessorClosed,
	) {
		t.Fatalf("feed after close error = %v", err)
	}
}

func TestOutputProcessorResizeFailureBeforeApplyDoesNotFailStop(t *testing.T) {
	manager, _ := newTestManager(t)
	id := agent.ID("resize-retry")
	running := attachOutputOnlyRuntime(manager, id, "")
	resizeErr := errors.New("resize rejected")
	process := &terminalTestProcess{resizeErr: resizeErr}
	running.process = process
	running.terminal = newTerminalTestActor(
		t,
		"generic",
		newTerminalTestClock(time.Unix(60, 0).UTC()),
		process,
		&recordingTerminalObserver{},
	)
	size, err := term.NewSize(24, 80)
	if err != nil {
		t.Fatalf("new resize: %v", err)
	}
	if err := running.output.Resize(context.Background(), size); !errors.Is(err, resizeErr) {
		t.Fatalf("resize error = %v, want %v", err, resizeErr)
	}
	select {
	case fatal := <-manager.Fatal():
		t.Fatalf("pre-apply resize triggered fail-stop: %v", fatal)
	default:
	}

	output := []byte("still usable")
	if err := running.output.Feed(output, 0); err != nil {
		t.Fatalf("feed after rejected resize: %v", err)
	}
	if err := running.output.End(uint64(len(output))); err != nil {
		t.Fatalf("end output: %v", err)
	}
	rows, err := manager.Replay(string(id))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	for _, row := range rows {
		if row.Type == string(event.TypeAgentResized) {
			t.Fatalf("rejected resize was persisted: %+v", row)
		}
	}
}

func TestOutputProcessorPostApplyResizeCommitFailureFailsStop(t *testing.T) {
	storageErr := errors.New("resize store unavailable")
	st := &memoryCommitStore{appendErr: storageErr}
	hub := event.NewHub(0)
	committer := newCommitter(0, st, hub)
	manager := &Manager{
		hub:       hub,
		committer: committer,
		clock:     systemObservationClock{},
		agents:    make(map[agent.ID]*managedAgent),
		sessions:  make(map[agent.ID]*runningSession),
	}
	id := agent.ID("resize-commit-failure")
	process := &terminalTestProcess{}
	running := &runningSession{process: process, vendor: "generic"}
	running.output = newOutputProcessor(manager, id, running, "")
	running.terminal = newTerminalTestActor(
		t,
		"generic",
		newTerminalTestClock(time.Unix(70, 0).UTC()),
		process,
		&recordingTerminalObserver{},
	)
	manager.sessions[id] = running
	t.Cleanup(func() {
		_ = running.output.Close()
		_ = running.terminal.Close()
		committer.Close()
	})

	size, err := term.NewSize(24, 80)
	if err != nil {
		t.Fatalf("new resize: %v", err)
	}
	err = running.output.Resize(context.Background(), size)
	if !errors.Is(err, storageErr) {
		t.Fatalf("resize error = %v, want %v", err, storageErr)
	}
	if got := process.Resizes(); !slices.Equal(
		got,
		[][2]uint16{{24, 80}},
	) {
		t.Fatalf("PTY resizes = %v", got)
	}
	select {
	case fatal := <-manager.Fatal():
		if !errors.Is(fatal, storageErr) {
			t.Fatalf("fatal error = %v, want %v", fatal, storageErr)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for resize fail-stop")
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

func waitForScreenSignal(
	t *testing.T,
	manager *Manager,
	id agent.ID,
) (store.EventRow, event.SignalPayloadV3) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		rows, err := manager.Replay(string(id))
		if err != nil {
			t.Fatalf("replay screen signal: %v", err)
		}
		for _, row := range rows {
			if row.Type != string(event.TypeAgentSignal) ||
				signalPayloadVersion(row.Payload) != 3 {
				continue
			}
			var payload event.SignalPayloadV3
			if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
				t.Fatalf("decode screen signal: %v", err)
			}
			if err := payload.Validate(); err != nil {
				t.Fatalf("validate screen signal: %v", err)
			}
			return row, payload
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for screen signal")
		}
		time.Sleep(time.Millisecond)
	}
}

func signalPayloadVersion(payload string) int {
	var version struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal([]byte(payload), &version); err != nil {
		return 0
	}
	return version.Version
}

func waitForTerminalSnapshot(t *testing.T, actor *terminalActor) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		if _, available := actor.Snapshot(); available {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for terminal snapshot")
		}
		time.Sleep(time.Millisecond)
	}
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
