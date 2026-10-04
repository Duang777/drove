package session

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Duang777/drove/internal/adapter"
	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/pty"
	"github.com/Duang777/drove/internal/store"
)

const terminalAcceptanceDeadline = time.Second

func TestTerminalAcceptanceApprovalVisibleOffAndFallback(t *testing.T) {
	for _, vendor := range []string{"claude", "codex"} {
		for _, policy := range []agent.HookPolicy{agent.HooksOff, agent.HooksAuto} {
			for _, mode := range terminalAcceptanceFeedModes() {
				name := fmt.Sprintf("%s/%s/%s", vendor, policy, mode.name)
				t.Run(name, func(t *testing.T) {
					harness := newTerminalAcceptanceHarness(t, vendor, policy)
					if policy == agent.HooksAuto {
						harness.enterFallback(t)
					}

					queries := loadTerminalAcceptanceQueries(t)
					stream := append(
						terminalAcceptanceQueryInput(queries),
						loadTerminalAcceptanceFixture(t, vendor, "approval")...,
					)
					startedAt := harness.detectorClock.Now()
					harness.feed(t, stream, mode)
					harness.awaitReplies(t, queries)

					before := harness.screenSignalCount(t)
					harness.sample(t)
					harness.waitForScreenSignals(t, before+1)
					harness.fireDetectorTimer(t, 750*time.Millisecond)
					harness.waitForState(t, agent.StateBlocked)
					if elapsed := harness.detectorClock.Now().Sub(startedAt); elapsed > time.Second {
						t.Fatalf("approval confirmation took %s, want at most 1s", elapsed)
					}

					harness.assertReplyIsolation(t, queries)
					harness.exitAndFeedTrailing(t, vendor, mode)
				})
			}
		}
	}
}

func TestTerminalAcceptanceActiveHookClearanceAndClaudeInterrupt(t *testing.T) {
	for _, vendor := range []string{"claude", "codex"} {
		for _, mode := range terminalAcceptanceFeedModes() {
			t.Run(vendor+"/"+mode.name, func(t *testing.T) {
				harness := newTerminalAcceptanceHarness(t, vendor, agent.HooksAuto)
				harness.activateBlockedHook(t)

				queries := loadTerminalAcceptanceQueries(t)
				approval := append(
					terminalAcceptanceQueryInput(queries),
					loadTerminalAcceptanceFixture(t, vendor, "approval")...,
				)
				harness.feed(t, approval, mode)
				harness.awaitReplies(t, queries)
				before := harness.screenSignalCount(t)
				harness.sample(t)
				harness.waitForScreenSignals(t, before+1)

				clearanceStartedAt := harness.detectorClock.Now()
				harness.feed(
					t,
					loadTerminalAcceptanceFixture(t, vendor, "idle_after_approval"),
					mode,
				)
				before = harness.screenSignalCount(t)
				harness.sample(t)
				harness.waitForScreenSignals(t, before+2)
				harness.fireDetectorTimer(t, 500*time.Millisecond)
				harness.waitForState(t, agent.StateWorking)
				if elapsed := harness.detectorClock.Now().Sub(clearanceStartedAt); elapsed > time.Second {
					t.Fatalf("approval clearance took %s, want at most 1s", elapsed)
				}

				if vendor == "claude" {
					interruptStartedAt := harness.detectorClock.Now()
					harness.feed(
						t,
						loadTerminalAcceptanceFixture(t, vendor, "interrupted"),
						mode,
					)
					before = harness.screenSignalCount(t)
					harness.sample(t)
					harness.waitForScreenSignals(t, before+1)
					harness.fireDetectorTimer(t, time.Second)
					harness.waitForState(t, agent.StateIdle)
					if elapsed := harness.detectorClock.Now().Sub(interruptStartedAt); elapsed > 2*time.Second {
						t.Fatalf("interrupt confirmation took %s, want at most 2s", elapsed)
					}
				}

				harness.assertReplyIsolation(t, queries)
				harness.endOutput(t)
			})
		}
	}
}

func TestTerminalAcceptanceEchoedReplyIsCommittedOutput(t *testing.T) {
	harness := newTerminalAcceptanceHarness(t, "claude", agent.HooksOff)
	query := loadTerminalAcceptanceQueries(t)[0]

	harness.feed(t, []byte(query.Input), terminalAcceptanceFeedMode{
		name:  "query",
		sizes: []int{len(query.Input)},
	})
	harness.awaitReplies(t, []terminalAcceptanceQuery{query})

	harness.feed(t, []byte(query.Reply), terminalAcceptanceFeedMode{
		name:  "echo",
		sizes: []int{len(query.Reply)},
	})
	harness.endOutput(t)

	rows := harness.replay(t)
	if got, want := joinOutputRows(t, rows), []byte(query.Input+query.Reply); !bytes.Equal(got, want) {
		t.Fatalf("echoed output = %q, want %q", got, want)
	}
	if got := countEventType(rows, event.TypeOutputChunk); got != 2 {
		t.Fatalf("output events = %d, want one query and one explicit echo", got)
	}
}

func TestTerminalAcceptanceNoGoroutineLeak(t *testing.T) {
	beforeProfile, before := terminalAcceptanceGoroutines(t)
	mode := terminalAcceptanceFeedMode{
		name:  "lifecycle",
		sizes: []int{31, 64, 7, 128},
	}

	for iteration := range 12 {
		harness := newTerminalAcceptanceHarness(t, "claude", agent.HooksOff)
		queries := loadTerminalAcceptanceQueries(t)
		harness.feed(t, terminalAcceptanceQueryInput(queries), mode)
		harness.awaitReplies(t, queries)

		switch iteration % 3 {
		case 0:
			harness.endOutput(t)
		case 1:
			harness.exitAndFeedTrailing(t, "claude", mode)
		case 2:
			if err := harness.manager.Close(); err != nil {
				t.Fatalf("iteration %d manager close: %v", iteration, err)
			}
		}
		harness.Close()
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		runtime.GC()
		afterProfile, after := terminalAcceptanceGoroutines(t)
		if after.terminalActors <= before.terminalActors &&
			after.replyForwarders <= before.replyForwarders &&
			after.replyPumps <= before.replyPumps &&
			after.observationActors <= before.observationActors &&
			after.committers <= before.committers {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"terminal goroutines leaked: before=%+v after=%+v\n"+
					"before profile:\n%s\nafter profile:\n%s",
				before,
				after,
				beforeProfile,
				afterProfile,
			)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type terminalAcceptanceFeedMode struct {
	name  string
	sizes []int
}

func terminalAcceptanceFeedModes() []terminalAcceptanceFeedMode {
	return []terminalAcceptanceFeedMode{
		{name: "byte-boundaries", sizes: []int{1, 31, 2, 47}},
		{name: "control-boundaries", sizes: []int{2, 5, 1, 8, 3, 13}},
		{name: "realistic-chunks", sizes: []int{31, 64, 7, 128}},
	}
}

type terminalAcceptanceQuery struct {
	Name  string `json:"name"`
	Input string `json:"input"`
	Reply string `json:"reply"`
}

func loadTerminalAcceptanceQueries(t *testing.T) []terminalAcceptanceQuery {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(
		"testdata",
		"terminal",
		"query_replies.json",
	))
	if err != nil {
		t.Fatalf("read query fixture: %v", err)
	}
	var queries []terminalAcceptanceQuery
	if err := json.Unmarshal(data, &queries); err != nil {
		t.Fatalf("decode query fixture: %v", err)
	}
	if len(queries) == 0 {
		t.Fatal("query fixture is empty")
	}
	return queries
}

func terminalAcceptanceQueryInput(queries []terminalAcceptanceQuery) []byte {
	var input []byte
	for _, query := range queries {
		input = append(input, query.Input...)
	}
	return input
}

func loadTerminalAcceptanceFixture(t *testing.T, vendor, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(
		"testdata",
		"terminal",
		vendor+"_"+name+".bin",
	))
	if err != nil {
		t.Fatalf("read %s %s fixture: %v", vendor, name, err)
	}
	return data
}

type terminalAcceptanceHarness struct {
	closeOnce sync.Once

	manager       *Manager
	store         *store.Store
	target        *agent.Agent
	running       *runningSession
	process       *terminalAcceptanceProcess
	detectorClock *actorFakeClock
	terminalClock *terminalTestClock

	sourceOffset uint64
	sourceBytes  []byte
	sourceFeeds  int
}

func newTerminalAcceptanceHarness(
	t *testing.T,
	vendor string,
	policy agent.HookPolicy,
) *terminalAcceptanceHarness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "drove.db"))
	if err != nil {
		t.Fatalf("open acceptance store: %v", err)
	}
	manager := NewManager(adapter.NewRegistry(), event.NewHub(0), st, 0)
	manager.newCredential = func() (string, signalTokenDigest, error) {
		token := strings.Repeat("Z", 43)
		return token, signalTokenDigest(sha256.Sum256([]byte(token))), nil
	}
	origin, err := url.Parse("http://127.0.0.1:43129")
	if err != nil {
		t.Fatalf("parse signal origin: %v", err)
	}
	if err := manager.ConfigureSignalOrigin(origin); err != nil {
		t.Fatalf("configure signal origin: %v", err)
	}

	base := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	detectorClock := newActorFakeClock(base)
	terminalClock := newTerminalTestClock(base)
	manager.clock = detectorClock

	id := agent.ID(fmt.Sprintf("acceptance-%s-%s-%s", vendor, policy, uuid.NewString()))
	target := agent.New(
		id,
		agent.WithName(string(id)),
		agent.WithVendor(vendor),
		agent.WithRunMode(agent.RunModeInteractive),
		agent.WithHookPolicy(policy),
	)
	managed := newManagedAgent(target)
	manager.mu.Lock()
	manager.agents[id] = managed
	manager.mu.Unlock()
	commitTestState(t, manager, target, agent.StateStarting, "acceptance start")

	running, _, _, err := manager.prepareManagedRuntime(managed, manager.reg.For(vendor))
	if err != nil {
		t.Fatalf("prepare acceptance runtime: %v", err)
	}
	process := &terminalAcceptanceProcess{}
	running.process = process
	close(running.signalReady)
	close(running.callbacksReady)
	manager.mu.Lock()
	manager.sessions[id] = running
	manager.mu.Unlock()

	terminalActor, err := newTerminalActor(
		mustInitialTerminalSize(t),
		process,
		running.classifier,
		running.observer,
		vendor,
		terminalClock,
		func(actorErr error) {
			manager.failTerminalActor(id, actorErr)
		},
	)
	if err != nil {
		t.Fatalf("create acceptance terminal actor: %v", err)
	}
	running.terminal = terminalActor

	started, err := processObservation(
		detect.KindProcessStarted,
		detectorClock.Now(),
		&detect.ProcessFact{HookAvailable: policy != agent.HooksOff},
	)
	if err != nil {
		t.Fatalf("create process-start observation: %v", err)
	}
	if err := running.observer.Deliver(context.Background(), started); err != nil {
		t.Fatalf("deliver process-start observation: %v", err)
	}
	if got := target.State(); got != agent.StateWorking {
		t.Fatalf("startup state = %s, want working", got)
	}

	harness := &terminalAcceptanceHarness{
		manager:       manager,
		store:         st,
		target:        target,
		running:       running,
		process:       process,
		detectorClock: detectorClock,
		terminalClock: terminalClock,
	}
	t.Cleanup(harness.Close)
	return harness
}

func (h *terminalAcceptanceHarness) Close() {
	h.closeOnce.Do(func() {
		if h.running.terminal != nil {
			_ = h.running.terminal.Close()
		}
		if h.running.observer != nil {
			h.running.observer.Close()
		}
		h.manager.mu.Lock()
		if h.manager.sessions[h.target.ID()] == h.running {
			delete(h.manager.sessions, h.target.ID())
		}
		h.manager.mu.Unlock()
		_ = h.manager.Close()
		_ = h.store.Close()
	})
}

func (h *terminalAcceptanceHarness) feed(
	t *testing.T,
	data []byte,
	mode terminalAcceptanceFeedMode,
) {
	t.Helper()
	for offset, sizeIndex := 0, 0; offset < len(data); sizeIndex++ {
		size := mode.sizes[sizeIndex%len(mode.sizes)]
		end := min(len(data), offset+size)
		chunk := data[offset:end]
		if err := h.running.output.Feed(chunk, h.sourceOffset); err != nil {
			t.Fatalf("feed %s at source offset %d: %v", mode.name, h.sourceOffset, err)
		}
		h.sourceOffset += uint64(len(chunk))
		h.sourceBytes = append(h.sourceBytes, chunk...)
		h.sourceFeeds++
		offset = end
	}
}

func (h *terminalAcceptanceHarness) sample(t *testing.T) {
	t.Helper()
	h.advance(terminalSampleWait)
	timer := h.terminalClock.onlyTimer(t)
	if !timer.Active() {
		t.Fatal("terminal sample timer is inactive")
	}
	timer.fire(h.terminalClock.Now())
}

func (h *terminalAcceptanceHarness) advance(delta time.Duration) {
	h.detectorClock.advance(delta)
	h.terminalClock.advance(delta)
}

func (h *terminalAcceptanceHarness) fireDetectorTimer(
	t *testing.T,
	wantDelay time.Duration,
) {
	t.Helper()
	timer := h.detectorClock.lastTimer(t)
	delay := timer.currentDelay()
	if delay != wantDelay {
		t.Fatalf("detector timer = %s, want %s", delay, wantDelay)
	}
	h.advance(delay)
	timer.fire(h.detectorClock.Now())
}

func (h *terminalAcceptanceHarness) enterFallback(t *testing.T) {
	t.Helper()
	h.fireDetectorTimer(t, detect.DefaultConfig().HookActivation)
	h.waitForHookStatus(t, detect.HookFallback)
}

func (h *terminalAcceptanceHarness) activateBlockedHook(t *testing.T) {
	t.Helper()
	signal, err := detect.NewHookSignal(detect.Signal{
		Kind:             detect.KindHumanInputRequired,
		Vendor:           h.target.Vendor(),
		VendorEvent:      "approval_required",
		Scope:            detect.ScopeRoot,
		VendorSessionRef: "redacted-session",
		Confidence:       1,
		ReceivedAt:       h.detectorClock.Now(),
		DeliveryID:       uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("create hook observation: %v", err)
	}
	observation, err := detect.ObserveSignal(signal)
	if err != nil {
		t.Fatalf("normalize hook observation: %v", err)
	}
	if err := h.running.observer.Deliver(context.Background(), observation); err != nil {
		t.Fatalf("activate hook: %v", err)
	}
	h.waitForHookStatus(t, detect.HookActive)
	h.waitForState(t, agent.StateBlocked)
}

func (h *terminalAcceptanceHarness) awaitReplies(
	t *testing.T,
	queries []terminalAcceptanceQuery,
) {
	t.Helper()
	deadline := time.Now().Add(terminalAcceptanceDeadline)
	for {
		written := h.process.Written()
		complete := true
		for _, query := range queries {
			if !bytes.Contains(written, []byte(query.Reply)) {
				complete = false
				break
			}
		}
		if complete {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminal replies = %q, want all query replies", written)
		}
		time.Sleep(time.Millisecond)
	}
}

func (h *terminalAcceptanceHarness) waitForState(t *testing.T, want agent.State) {
	t.Helper()
	deadline := time.Now().Add(terminalAcceptanceDeadline)
	for h.target.State() != want {
		if time.Now().After(deadline) {
			t.Fatalf("state = %s, want %s", h.target.State(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func (h *terminalAcceptanceHarness) waitForHookStatus(
	t *testing.T,
	want detect.HookStatus,
) {
	t.Helper()
	deadline := time.Now().Add(terminalAcceptanceDeadline)
	for h.running.observer.Snapshot().HookStatus() != want {
		if time.Now().After(deadline) {
			t.Fatalf(
				"hook status = %s, want %s",
				h.running.observer.Snapshot().HookStatus(),
				want,
			)
		}
		time.Sleep(time.Millisecond)
	}
}

func (h *terminalAcceptanceHarness) screenSignalCount(t *testing.T) int {
	t.Helper()
	count := 0
	for _, row := range h.replay(t) {
		if row.Type == string(event.TypeAgentSignal) &&
			signalPayloadVersion(row.Payload) == 3 {
			count++
		}
	}
	return count
}

func (h *terminalAcceptanceHarness) waitForScreenSignals(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(terminalAcceptanceDeadline)
	for {
		if got := h.screenSignalCount(t); got >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("screen signals = %d, want at least %d", h.screenSignalCount(t), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func (h *terminalAcceptanceHarness) assertReplyIsolation(
	t *testing.T,
	queries []terminalAcceptanceQuery,
) {
	t.Helper()
	rows := h.replay(t)
	if got := joinOutputRows(t, rows); !bytes.Equal(got, h.sourceBytes) {
		t.Fatalf("durable output differs from child output: got %d bytes, want %d", len(got), len(h.sourceBytes))
	}
	if got := countEventType(rows, event.TypeOutputChunk); got != h.sourceFeeds {
		t.Fatalf("output events = %d, want %d child callbacks", got, h.sourceFeeds)
	}
	if got := countEventType(rows, event.TypeAgentInput); got != 0 {
		t.Fatalf("query replies created %d input audit events", got)
	}

	explanation, err := h.manager.Explain(
		context.Background(),
		h.target.ID(),
		ExplainOptions{Limit: MaxExplainLimit},
	)
	if err != nil {
		t.Fatalf("explain acceptance session: %v", err)
	}
	encoded, err := json.Marshal(explanation)
	if err != nil {
		t.Fatalf("encode explanation: %v", err)
	}
	for _, query := range queries {
		if marker := terminalReplyMarker(query.Reply); marker != "" &&
			bytes.Contains(encoded, []byte(marker)) {
			t.Fatalf("reply marker %q entered explain output", marker)
		}
	}
	if explanation.Screen == nil {
		t.Fatal("attached acceptance session has no sampled screen")
	}
}

func (h *terminalAcceptanceHarness) exitAndFeedTrailing(
	t *testing.T,
	vendor string,
	mode terminalAcceptanceFeedMode,
) {
	t.Helper()
	before := h.screenSignalCount(t)
	h.manager.onExit(h.target.ID(), h.running, pty.ExitInfo{Code: 0})
	if got := h.target.State(); got != agent.StateStopped {
		t.Fatalf("post-exit state = %s, want stopped", got)
	}
	h.feed(t, loadTerminalAcceptanceFixture(t, vendor, "trailing"), mode)
	h.endOutput(t)

	if got := h.screenSignalCount(t); got != before {
		t.Fatalf("trailing output created screen signals: before=%d after=%d", before, got)
	}
	rows := h.replay(t)
	terminalSeq := uint64(0)
	trailingSeq := uint64(0)
	for _, row := range rows {
		if row.Type == string(event.TypeStateChanged) &&
			row.To == string(agent.StateStopped) {
			terminalSeq = row.Seq
		}
		if row.Type == string(event.TypeOutputChunk) && row.Seq > terminalSeq {
			trailingSeq = row.Seq
		}
	}
	if terminalSeq == 0 || trailingSeq <= terminalSeq {
		t.Fatalf("terminal seq = %d, trailing output seq = %d", terminalSeq, trailingSeq)
	}
	explanation, err := h.manager.Explain(
		context.Background(),
		h.target.ID(),
		ExplainOptions{},
	)
	if err != nil {
		t.Fatalf("explain detached session: %v", err)
	}
	if explanation.Attached || explanation.Screen != nil {
		t.Fatalf("detached explanation retained screen: %+v", explanation.Screen)
	}
}

func (h *terminalAcceptanceHarness) endOutput(t *testing.T) {
	t.Helper()
	if err := h.running.output.End(h.sourceOffset); err != nil {
		t.Fatalf("end acceptance output: %v", err)
	}
	if err := h.running.terminal.Close(); err != nil {
		t.Fatalf("close acceptance terminal: %v", err)
	}
}

func (h *terminalAcceptanceHarness) replay(t *testing.T) []store.EventRow {
	t.Helper()
	rows, err := h.manager.Replay(string(h.target.ID()))
	if err != nil {
		t.Fatalf("replay acceptance session: %v", err)
	}
	return rows
}

func terminalReplyMarker(reply string) string {
	switch {
	case strings.Contains(reply, "rgb:"):
		return "rgb:"
	case strings.Contains(reply, "?62;"):
		return "?62;"
	case strings.Contains(reply, "?0u"):
		return "?0u"
	case strings.Contains(reply, "[4;7R"):
		return "[4;7R"
	default:
		return ""
	}
}

func countEventType(rows []store.EventRow, eventType event.Type) int {
	count := 0
	for _, row := range rows {
		if row.Type == string(eventType) {
			count++
		}
	}
	return count
}

type terminalAcceptanceGoroutineCount struct {
	terminalActors    int
	replyForwarders   int
	replyPumps        int
	observationActors int
	committers        int
}

func terminalAcceptanceGoroutines(
	t *testing.T,
) (string, terminalAcceptanceGoroutineCount) {
	t.Helper()
	var profile bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&profile, 2); err != nil {
		t.Fatalf("write goroutine profile: %v", err)
	}
	text := profile.String()
	return text, terminalAcceptanceGoroutineCount{
		terminalActors: strings.Count(
			text,
			"internal/session.(*terminalActor).run",
		),
		replyForwarders: strings.Count(
			text,
			"internal/session.(*terminalActor).forwardReplies",
		),
		replyPumps: strings.Count(
			text,
			"internal/term.(*Controller).pumpReplies",
		),
		observationActors: strings.Count(
			text,
			"internal/session.(*observationActor).run",
		),
		committers: strings.Count(
			text,
			"internal/session.(*committer).run",
		),
	}
}

type terminalAcceptanceProcess struct {
	mu      sync.Mutex
	written []byte
}

func (p *terminalAcceptanceProcess) Write(frame []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.written = append(p.written, frame...)
	return len(frame), nil
}

func (p *terminalAcceptanceProcess) Resize(uint16, uint16) error {
	return nil
}

func (p *terminalAcceptanceProcess) Close() error {
	return nil
}

func (p *terminalAcceptanceProcess) PID() int {
	return 1
}

func (p *terminalAcceptanceProcess) Written() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]byte(nil), p.written...)
}
