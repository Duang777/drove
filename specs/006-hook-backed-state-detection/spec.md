# Hook-backed agent state detection

Status: Approved for implementation

Approved: 2026-10-03

Owner direction: continue RFC-001 Phase 1 after reviewing the open issues and
the current vendor hook contracts.

Related issues:

- [#2](https://github.com/Duang777/drove/issues/2), hook-backed state authority
- [#3](https://github.com/Duang777/drove/issues/3), Blocked recovery
- [#4](https://github.com/Duang777/drove/issues/4), input injection

Research:

- [RFC-001](../../docs/rfc-001-agent-state-and-control.md)
- [Phase 1 hooks and Detector research](../../docs/next-phase-research.md)

## Problem

Drove currently has two ordering defects that block hook-backed state
detection.

First, `agent.Agent.Transition` changes memory before its callback attempts to
persist the state event. The callback cannot return an error. A failed SQLite
write can therefore leave the live Agent ahead of the event log.

Second, each event producer obtains a sequence from `event.Hub`, writes SQLite,
and publishes independently. Concurrent sessions can allocate sequence N and
N+1, then persist or publish them in the opposite order. Adding one Detector
goroutine per session does not solve this global ordering problem.

The current output heuristic also changes state immediately. It does not use
`Confidence`, cannot recover Blocked sessions reliably, and competes with the
future hook path.

Phase 1A must add hook signals without weakening these existing contracts:

- every Agent runs in one goroutine and one PTY;
- `internal/agent` owns the state transition rules;
- vendor JSON and event names stay in `internal/adapter`;
- events are durable before they change an in-memory projection or reach Hub;
- input delivery and process exit remain ordered by the per-session input lock;
- `requestStop` and `claimExit` retain one terminal winner;
- PTY output may arrive after the terminal state event;
- bootstrap still reconciles every restored state, including `done`, to
  `stopped`.

## Goal

Deliver one complete signal path:

```text
Claude or Codex command hook
    -> drove hook relay
    -> POST /api/v1/agents/{id}/signal
    -> vendor adapter
    -> per-session observation actor
    -> Detector decision
    -> global event committer
    -> SQLite
    -> Agent and Detector projections
    -> Hub
```

The phase also moves every existing runtime event producer through the same
global committer. SQLite, Agent memory, and Hub must observe one committed
order.

## Scope

This phase includes:

- reader-first compatibility for `agent.signal`, creation metadata version 2,
  and state evidence version 1;
- one private global event committer owned by `session.Manager`;
- commit-before-apply Agent mutations;
- one observation actor and one Detector state per attached session;
- process, hook, heuristic, and timer signal arbitration;
- Blocked recovery and confirmed Idle transitions;
- `off`, `auto`, and `required` hook policies;
- Claude Code and Codex hook adapters;
- a loopback signal endpoint with a per-session capability token;
- `drove hook --vendor <vendor>` as an observation-only relay;
- manual hook configuration examples;
- API status fields for policy, observed hook status, and the last state
  evidence;
- migration of lifecycle, output, error, input, and state events to the global
  committer.

## Non-goals

This phase does not:

- install, edit, or remove Claude or Codex configuration;
- accept workspace, project, or hook trust on the user's behalf;
- use a trust bypass flag;
- inspect config files to claim that hooks are active;
- add WebSocket input;
- add terminal attach, resize, or process-group control;
- reconnect a PTY after daemon restart;
- infer Done from hook events, output text, or silence;
- add a database table or schema migration;
- batch unrelated requests for throughput;
- expose timing or queue constants as user configuration;
- add ACP as a signal source.

Issue #4 remains open because WebSocket input is outside this phase.

## Chosen architecture

The design uses two serial owners.

Each attached session has one observation actor. Hook deliveries, heuristic
hints, process facts, and timer firings enter that actor. The actor calls a
pure `detect.Detector.Decide` function and submits the resulting immutable
decision. No callback or input producer changes Agent state.

`session.Manager` owns one global committer goroutine. The committer assigns
sequence numbers, appends one SQLite batch, applies typed in-memory changes,
and publishes the committed events. No other runtime path writes the event
store or allocates event sequence numbers.

This separates two ordering domains:

- the observation actor orders decisions for one session;
- the committer orders durable events across all sessions.

The committer remains private to `internal/session`. A public commit service
would expose projection and shutdown rules to its callers even though those
rules belong to session orchestration.

## Caller view

### Start a session

The public request gains a hook policy:

```go
type StartRequest struct {
	Vendor  string           `json:"vendor"`
	Name    string           `json:"name,omitempty"`
	Command string           `json:"command,omitempty"`
	Args    []string         `json:"args,omitempty"`
	Dir     string           `json:"dir,omitempty"`
	Mode    agent.RunMode    `json:"mode,omitempty"`
	Hooks   agent.HookPolicy `json:"hooks,omitempty"`
}
```

An empty policy selects `auto` for an adapter with hook support and `off` for
an adapter without hook support.

The CLI exposes the same choice:

```bash
drove up claude
drove up claude --hooks off
drove up claude --hooks required
```

### Deliver a hook

The API performs transport checks, then makes one domain call:

```go
err := s.opts.Manager.DeliverHook(
	r.Context(),
	agent.ID(r.PathValue("id")),
	delivery,
)
```

The call returns success only after the accepted signal and any derived state
event are durable, applied, and published. A duplicate `delivery_id` returns
success without writing another event.

### Observe PTY output

The output callback first commits the output event. It then submits only
redacted activity and heuristic metadata to the session actor:

```go
if err := m.commitOutput(id, line); err != nil {
	m.fail(err)
	return
}
if observation, ok := normalizeOutput(entry, line, m.clock.Now()); ok {
	_ = running.observer.Deliver(m.lifecycle, observation)
}
```

This two-step path is deliberate. The existing PTY contract allows trailing
output after the process-exit callback. A terminal Detector can stop while the
output callback continues to preserve the final output in the event log.
Output always precedes a state signal derived from that output.

### Audit input

`SendInput` keeps the current external-side-effect contract. It holds the
per-session input lock across the complete PTY write and the event commit.

```go
written, err := running.process.Write(data)
if err != nil {
	return result, err
}
err = m.commitInputAudit(id, written)
```

If the PTY accepts the bytes and the audit commit fails, `SendInput` returns
`ErrInputAudit` and tells the caller not to retry.

## Domain types

### Hook policy

Hook policy is creation metadata, so it belongs beside `RunMode`:

```go
package agent

type HookPolicy string

const (
	HooksOff      HookPolicy = "off"
	HooksAuto     HookPolicy = "auto"
	HooksRequired HookPolicy = "required"
)

func ValidHookPolicy(HookPolicy) bool
```

`createdPayload` version 2 requires both the normalized run mode and hook
policy. The recovery reader keeps version 1 support.

### Normalized signal

`internal/detect` owns vendor-neutral signal values:

```go
package detect

type Source string

const (
	SourceProcess   Source = "process"
	SourceHook      Source = "hook"
	SourceHeuristic Source = "heuristic"
	SourceTimer     Source = "timer"
)

type HookStatus string

const (
	HookOff            HookStatus = "off"
	HookAwaiting       HookStatus = "awaiting_hook"
	HookFallback       HookStatus = "fallback"
	HookActive         HookStatus = "hook_active"
	HookRequiredFailed HookStatus = "required_failed"
	HookDetached       HookStatus = "detached"
)

type Kind string

const (
	KindSessionStarted        Kind = "session_started"
	KindObserved              Kind = "observed"
	KindTurnStarted           Kind = "turn_started"
	KindToolActivity          Kind = "tool_activity"
	KindHumanInputRequired    Kind = "human_input_required"
	KindHumanInputResolved    Kind = "human_input_resolved"
	KindPermissionRequested   Kind = "permission_requested"
	KindPermissionResolved    Kind = "permission_resolved"
	KindTurnStopped           Kind = "turn_stopped"
	KindTurnFailed            Kind = "turn_failed"
	KindInterrupted           Kind = "interrupted"
	KindIdlePrompt            Kind = "idle_prompt"
	KindSessionEnded          Kind = "session_ended"
	KindSubagentStarted       Kind = "subagent_started"
	KindSubagentStopped       Kind = "subagent_stopped"
	KindTaskCompleted         Kind = "task_completed"
	KindOutputActivity        Kind = "output_activity"
	KindHeuristicBlocked      Kind = "heuristic_blocked"
	KindProcessStarted        Kind = "process_started"
	KindProcessStartFailed    Kind = "process_start_failed"
	KindProcessExited         Kind = "process_exited"
	KindHookActivationExpired Kind = "hook_activation_expired"
	KindTimerFired            Kind = "timer_fired"
)

type Scope string

const (
	ScopeRoot     Scope = "root"
	ScopeSubagent Scope = "subagent"
)

type Signal struct {
	Version         int
	Source          Source
	Kind            Kind
	Vendor          string
	VendorEvent     string
	Scope           Scope
	VendorSessionID string
	VendorTurnID    string
	Notification    string
	Confidence      float64
	OccurredAt      time.Time
	ReceivedAt      time.Time
	DeliveryID      string
	TimerGeneration uint64
	Process         *ProcessFact
}
```

Source-specific constructors validate valid combinations. They copy strings
and reject values outside the documented bounds. Callers cannot construct a
signal that claims hook authority without a delivery ID and vendor event.

`ReceivedAt` determines observation order. `OccurredAt` is audit evidence only.

### Agent change

`Agent.Transition`, `Agent.SetError`, `WithStateChangeHook`, and
`session.onStateChange` are removed in one writer-switch commit.

State changes carry one validated evidence value:

```go
package agent

type EvidenceSource string

const (
	EvidenceSession   EvidenceSource = "session"
	EvidenceProcess   EvidenceSource = "process"
	EvidenceHook      EvidenceSource = "hook"
	EvidenceHeuristic EvidenceSource = "heuristic"
	EvidenceTimer     EvidenceSource = "timer"
	EvidenceRecovery  EvidenceSource = "recovery"
)

type Evidence struct {
	Source     EvidenceSource `json:"source"`
	Event      string         `json:"event"`
	Confidence float64        `json:"confidence"`
	DeliveryID string         `json:"delivery_id,omitempty"`
}
```

`Source` and `Event` are required. `Source` must be one of the constants.
`Event` has a 64-byte ASCII limit. `Confidence` must be finite and between
zero and one. `DeliveryID` is empty or a canonical UUID. Only hook evidence
sets it. Constructors copy the strings.

Session startup uses source `session`. Process start, exit, and failure use
source `process`. Bootstrap reconciliation uses source `recovery`, event
`daemon_restart`, and confidence 1.

The replacement separates planning from application:

```go
package agent

type Change struct {
	// private target state, reason, error update, and transition evidence
}

type PreparedChange struct {
	// private Agent ID, expected revision, old values, and new values
}

type Snapshot struct {
	ID       ID
	State    State
	RunMode  RunMode
	Revision uint64
}

func MoveTo(to State, reason string, evidence Evidence) Change
func FailTo(to State, reason, message string, evidence Evidence) Change
func RecordError(message string, evidence Evidence) Change

func (a *Agent) Prepare(change Change) (PreparedChange, error)
func (a *Agent) ApplyCommitted(prepared PreparedChange) error
func (a *Agent) Snapshot() Snapshot
func (p PreparedChange) Transition() (
	from, to State,
	reason string,
	evidence Evidence,
	ok bool,
)
func (p PreparedChange) ErrorMessage() (string, bool)
func (p PreparedChange) Timestamp() time.Time
```

`Prepare` reads but does not mutate the Agent. It checks the current revision,
the live transition table, run-mode restrictions, and the expected Agent ID.

Only the global committer calls `ApplyCommitted`. It calls `Prepare`
immediately before the SQLite transaction, then applies the same
`PreparedChange` after the transaction succeeds. Since the committer is the
only runtime mutator, the revision cannot change between these steps.

The committer derives the canonical error and `state_changed` drafts from
`PreparedChange`. A caller cannot persist one transition and apply another.

### Event draft

An uncommitted event must not have a sequence number:

```go
package event

type Draft struct {
	// private event fields without Seq
}

func NewAgentSignalDraft(sessionID, agentID, payload string) Draft
func NewStateChangedDraft(
	sessionID, agentID, from, to, reason, payload string,
) Draft
func NewOutputDraft(sessionID, agentID, line string) Draft
func NewErrorDraft(sessionID, agentID, message string) Draft
func NewSessionLifecycleDraft(
	sessionID, agentID, reason, payload string,
) Draft
func NewAgentInputDraft(sessionID, agentID, payload string) Draft
func Commit(seq uint64, at time.Time, draft Draft) (Event, error)
```

The committer seals copied drafts as `event.Event` values. Runtime constructors
do not accept a sequence number.

`Hub.NextSeq` and zero-sequence allocation in `Hub.Publish` are removed.

```go
func NewHub(initialPublishedSeq uint64) *Hub
func (h *Hub) PublishBatch(events []Event) error
```

`PublishBatch` requires nonzero, consecutive sequence numbers beginning at the
Hub's next expected sequence. Subscriber overflow remains lossy per
subscription and increments `Dropped`. The method validates the complete
batch and closed state before it delivers any event.

## Detector and observation actor

`internal/detect` is a pure decision package. It has no goroutine, channel,
HTTP type, adapter type, store, Hub, or mutable Agent pointer.

```go
package detect

type Config struct {
	HookActivation       time.Duration
	StopConfirmation     time.Duration
	PermissionConfirmation time.Duration
	HeuristicConfirmation time.Duration
	HeuristicRecoveryWindow time.Duration
	HeuristicRecoveryLines int
	FallbackIdleAfter    time.Duration
	HeuristicConfidence  float64
	DeliveryRememberCount int
}

type Detector struct {
	// immutable validated configuration
}

type State struct {
	// hook latch, candidates, timer generation, recent output, delivery IDs
}

type Snapshot struct {
	// immutable copy of State
}

type Observation struct {
	// sealed hook, process, heuristic, activity, or timer variant
}

type Decision struct {
	// immutable outcome, optional transition, next timer, and State delta
}

type Outcome string

const (
	OutcomeObserved   Outcome = "observed"
	OutcomeCandidate  Outcome = "candidate"
	OutcomeTransition Outcome = "transitioned"
	OutcomeSuppressed Outcome = "suppressed"
	OutcomeStale      Outcome = "stale"
	OutcomeTerminal   Outcome = "terminal"
)

type TimerAction string

const (
	TimerKeep   TimerAction = "keep"
	TimerCancel TimerAction = "cancel"
	TimerArm    TimerAction = "arm"
)

type TimerPlan struct {
	Action     TimerAction
	Deadline   time.Time
	Generation uint64
}

func New(Config) (*Detector, error)
func NewState(policy agent.HookPolicy) State
func (s *State) Snapshot() Snapshot
func (s Snapshot) HookStatus() HookStatus
func (d *Detector) Decide(
	state Snapshot,
	current agent.Snapshot,
	observation Observation,
) (Decision, error)
func (d Decision) Duplicate() bool
func (d Decision) Signal() (Signal, Outcome, bool)
func (d Decision) Change() (agent.Change, bool)
func (d Decision) Timer() TimerPlan
func (s *State) ApplyCommitted(Decision) error
```

`Outcome` and `TimerPlan` are immutable values. `TimerPlan` contains a
deadline and generation, not a live timer. These accessors let
`internal/session` build canonical event payloads and reset its timer without
accessing private Detector fields.

`State` protects `Snapshot` and `ApplyCommitted` with a local read/write lock.
The observation actor remains its only decision producer, while concurrent
Status calls can read an immutable snapshot without a data race.

`runningSession` owns the mutable `detect.State`, a bounded observation inbox,
and one resettable timer. Its actor performs this loop:

1. Receive one observation.
2. Snapshot the current Agent and Detector state.
3. Call `Detector.Decide`.
4. Return immediately for a previously committed duplicate delivery.
5. Build one sealed decision commit operation.
6. Wait for the global committer.
7. Reset the timer only after the commit succeeds.
8. Reply to the producer.

The actor processes at most one uncommitted decision. Two decisions for one
Agent cannot use the same revision.

Every timer carries a generation. Canceling or replacing a timer increments
the generation. A queued callback with an old generation produces a persisted
`stale` timer signal and no state change.

The Detector remembers the last 1024 successfully committed hook delivery IDs
in a ring plus a set. It adds an ID only after commit success. A repeated ID
returns the first successful outcome and writes no second event. A repeated
semantic event with a new delivery ID is persisted and may have no projection
effect.

The cache is not restored. Daemon restart invalidates the token and detaches
the old PTY, so the old hook cannot deliver to the recovered session.

## Global committer

The committer and its operation types are private to `internal/session`:

```go
type commitOperation interface {
	isCommitOperation()
}

type eventsOperation struct {
	drafts []event.Draft
}

type agentOperation struct {
	agent  *agent.Agent
	change agent.Change
	drafts []event.Draft
}

type decisionOperation struct {
	agent    *agent.Agent
	state    *detect.State
	decision detect.Decision
	drafts   []event.Draft
}

type commitReceipt struct {
	FirstSeq uint64
	LastSeq  uint64
}
```

The private marker method seals the operation set to `internal/session`.
Constructors copy all slices and payload bytes. No operation contains a
function, an open-ended apply interface, a channel, a PTY, or a caller-owned
context.

One goroutine owns `lastSeq` and processes operations:

1. Validate the sealed operation.
2. Prepare any Agent change and derive its canonical event drafts.
3. Assign consecutive sequence numbers from `lastSeq + 1`.
4. Call `Store.AppendEvents` once.
5. Immediately advance the committer's durable `lastSeq`.
6. Apply the prepared Agent change.
7. Apply the Detector decision state.
8. Publish the committed batch through `Hub.PublishBatch`.
9. Return the receipt.

SQLite is the first visible write. Hub subscribers cannot observe the event
before the Agent projection changes.

A concurrent status read may briefly observe the old in-memory state after
SQLite commits and before the committer applies the change. The operation
does not return and Hub does not publish during this interval.

### Admission and backpressure

The global queue holds 256 operations. Each session inbox holds 64
observations.

Durable events are not dropped. Producers block when the global queue is full.
PTY output eventually applies operating-system backpressure to the child
process.

The signal endpoint waits at most 250 milliseconds for observation admission.
If the inbox remains full, it returns `429 Too Many Requests` with
`Retry-After: 1`.

The caller context controls only queue admission. After an operation enters
the global queue, the committer owns it and uses the Manager lifecycle context.
The caller waits for a definite result even if its request context is
canceled. This avoids an ambiguous "canceled but possibly committed" result.

The committer does not merge operations from different callers. A Detector
decision may contain one to three events in one transaction.

### Failure

Any `Store.AppendEvents` error poisons the committer. It applies no projection,
publishes no event, fails queued and future work with the same root cause, and
notifies `Manager.Failures()`.

The committer does not retry a failed transaction. A failed `tx.Commit` can be
ambiguous. Restart recovery is the only safe retry boundary.

If SQLite commits and Agent apply, Detector apply, or Hub publication reports
an invariant error, the committer keeps the advanced durable sequence, stops
publication, and poisons itself. The daemon begins ordered shutdown. Restart
rebuilds memory from SQLite.

The committer never publishes an unpersisted "persist failed" event.

## State rules

### Live transitions

The live state table is:

| From | Allowed targets |
| --- | --- |
| Pending | Starting, Stopped |
| Starting | Working, Idle, Stopped |
| Working | Blocked, Idle, Done, Stopped |
| Blocked | Working, Idle, Done, Stopped |
| Idle | Working, Blocked, Done, Stopped |
| Done | none |
| Stopped | none |

Only a successful natural oneshot process exit may produce Done. Interactive
sessions never enter Done from a hook, output heuristic, or timer.

`Blocked -> Idle` handles a confirmed turn stop while the last visible state
was Blocked. `Idle -> Blocked` handles a new explicit human-wait signal before
other activity.

### Recovery compatibility

Recovery uses a separate historical transition check. It accepts all
previously valid transitions and keeps the existing bootstrap reconciliation:

| Restored state | Bootstrap result |
| --- | --- |
| Pending, Starting, Working, Blocked, or Idle | append restart error, then move to Stopped |
| Done | move to Stopped without a new error |
| Stopped | append nothing |

The existing `done -> stopped` reconciliation remains valid. Done is terminal
for live process decisions, not for bootstrap's loss-of-PTY reconciliation.

### Signal priority

The priority is:

1. process facts;
2. an observed active hook;
3. fallback heuristics;
4. fallback silence.

A current-session hook becomes active only after token authentication, adapter
normalization, and durable signal commit. Config presence and vendor trust do
not activate it.

Once active, a session never falls back to heuristic state changes. Matching
heuristic hints may still be stored with outcome `suppressed`.

### Hook decisions

The adapters map vendor events into these decisions:

| Normalized signal | State effect |
| --- | --- |
| SessionStarted | activate hooks; no state change |
| Root TurnStarted or ToolActivity | Blocked or Idle to Working |
| HumanInputResolved | Blocked or Idle to Working |
| HumanInputRequired from Elicitation or an allowlisted waiting Notification | Working or Idle to Blocked |
| PermissionRequested | arm a cancelable Blocked candidate |
| PermissionResolved | cancel a matching Blocked candidate; no direct state change |
| Root TurnStopped, TurnFailed, Interrupted, or idle prompt | arm a cancelable Idle candidate |
| SubagentStarted | Idle or Blocked to Working |
| SubagentStopped | no direct state change |
| TaskCompleted or `agent_completed` | no direct state change |
| SessionEnded | no direct state change |

Activity or a new human-wait signal cancels an Idle candidate. Tool activity,
permission denial, or human-input resolution cancels a permission candidate.

Subagent events never mark the root session Done. `TaskCompleted` and
`agent_completed` are turn or task evidence, not process completion.

### Process decisions

Process facts outrank every candidate and timer:

| Process fact | Result |
| --- | --- |
| Successful natural oneshot exit with hook requirement satisfied | Done |
| Successful natural interactive exit | Stopped |
| Nonzero or failed exit | Stopped and update LastError |
| User stop | Stopped |
| Daemon shutdown | Stopped |
| Hook-required timeout | Stopped and update LastError |
| PTY start failure | Stopped and update LastError |

A successful oneshot exit before required hook activation becomes Stopped with
the required-hook error. The requested authority was not observed.

### Heuristic decisions

Heuristics can change state only when policy is `off`, or when `auto` has
entered fallback.

- A Blocked hint needs confidence at least 0.85.
- The hint must remain unopposed for 750 milliseconds.
- A later non-Blocked output observation cancels the candidate.
- Two nonempty, non-Blocked output observations within one second recover a
  fallback session from Blocked to Working.
- Sixty seconds without output or accepted state-bearing signal moves a
  fallback Working session to low-confidence Idle.
- Any output resets the silence timer.
- A Done text hint is audit evidence only.
- Silence never produces Done.

## Timing constants

Phase 1A uses fixed internal values:

| Setting | Value |
| --- | ---: |
| Hook activation interval | 5 seconds |
| Stop, Interrupt, TurnFailed, or idle prompt confirmation | 1 second |
| PermissionRequest confirmation | 750 milliseconds |
| Heuristic Blocked confirmation | 750 milliseconds |
| Heuristic recovery window | 1 second |
| Heuristic recovery observations | 2 |
| Fallback Working-to-Idle silence | 60 seconds |
| Minimum heuristic confidence | 0.85 |
| Delivery ID memory | 1024 |
| Per-session observation inbox | 64 |
| Global commit queue | 256 |
| Signal admission wait | 250 milliseconds |

Tests inject a fake clock. This phase does not expose these values in config.

## Hook policy

### Off

`off` does not create a token or inject signal environment variables.
Heuristic fallback starts immediately. The endpoint rejects a signal for that
session.

### Auto

`auto` creates a token and starts in `awaiting_hook`.

During the five-second activation interval, output is durable but heuristics
do not change state. A valid signal changes the status to `hook_active` and
permanently selects hook authority.

If no valid signal arrives, a timer decision records
`hook_activation_expired`, changes the status to `fallback`, and enables
heuristics. A later valid signal still changes the status to `hook_active` and
disables heuristic state changes permanently.

The absence of a hook does not itself make the Agent Idle.

### Required

`required` is valid only when the selected adapter has a hook normalizer. The
request fails before creation for an unsupported adapter.

Explicit `auto` on an adapter without a hook normalizer keeps policy `auto`,
injects no callback variables, and enters fallback immediately. The empty
default for the same adapter normalizes to `off`.

After the process starts, `Manager.Start` waits up to five seconds for the
first valid signal to commit. The signal may be state-neutral. Success changes
the status to `hook_active`.

On timeout, the Detector commits `hook_activation_expired` and an error update.
`Start` records the hook-required stop cause, closes the PTY, waits for the
terminal process decision, and returns `ErrHookRequired`. Heuristics never
drive state in this policy.

## Startup ordering

An agent can invoke `drove hook` before `pty.Start` returns. The session must
not reject this as detached.

Startup uses this order:

1. Validate the request, adapter, run mode, hook policy, and command.
2. Commit creation metadata and `Pending -> Starting`.
3. Register the Agent in the runtime projection.
4. Create the Detector, token digest, readiness gate, and pending
   `runningSession`.
5. Register the pending session before calling `pty.Start`.
6. Pass fixed output and exit callbacks plus the signal environment to
   `pty.Start`.
7. Store the returned process handle.
8. Commit the ProcessStarted observation and `Starting -> Working`.
9. Close the readiness gate.
10. For `required`, wait for hook activation or timeout.

PTY output and exit callbacks keep the existing `callbacksReady` wait.

An authenticated hook that reaches the pending session also waits on the
readiness gate. After startup succeeds, the delivery enters the observation
actor. If startup fails, the gate returns the startup error, the token becomes
invalid, and the delivery fails without activating hooks.

This rule avoids adding `Starting -> Blocked` only to handle a startup race.

## Stop, exit, input, and trailing output

The current `inputMu` contract remains:

- `SendInput` holds it through the PTY write and audit commit;
- process exit takes it before `claimExit`;
- input that wins is audited before the terminal decision;
- exit that wins causes later input to return detached.

Hook delivery uses the same exit claim without holding the lock while waiting
for the durable result. Admission before the claim is ordered before the
process observation. Admission after the claim is rejected as expired.

`requestStop` records the first stop cause. `claimExit` records one terminal
winner. Detector logic does not replace these functions.

The PTY callback order does not change. `OnExit` may run before `readLoop`
finishes. The process decision can therefore precede a final output event.
`pty.Session.Close` still waits for both callbacks before Manager closes the
committer. A trailing output is durable but does not reopen a terminal
Detector.

## Event contract

### Agent signal

Add:

```go
const TypeAgentSignal Type = "agent.signal"
```

The payload is version 1:

```json
{
  "version": 1,
  "source": "hook",
  "kind": "tool_activity",
  "vendor": "claude",
  "vendor_event": "PostToolUse",
  "scope": "root",
  "vendor_session_id": "vendor-session-id",
  "vendor_turn_id": "vendor-turn-id",
  "notification": "",
  "confidence": 1,
  "occurred_at": "2026-10-03T10:00:00Z",
  "received_at": "2026-10-03T10:00:00.100Z",
  "delivery_id": "550e8400-e29b-41d4-a716-446655440000",
  "outcome": "observed"
}
```

Optional process summaries may add an integer exit code and an allowlisted exit
kind. The payload never contains prompt text, tool input, transcript paths,
assistant messages, arbitrary error text, the token, or raw vendor JSON.

Allowed outcomes are `observed`, `candidate`, `transitioned`, `suppressed`,
`stale`, and `terminal`.

The signal event is projection-neutral. Its derived `state_changed` event is
the only state projection input.

### State evidence

New state events use payload version 1:

```json
{
  "version": 1,
  "source": "hook",
  "event": "PostToolUse",
  "confidence": 1,
  "delivery_id": "550e8400-e29b-41d4-a716-446655440000"
}
```

Legacy state events have an empty payload. The state columns and reason remain
authoritative. Bootstrap reconciliation written after the runtime writer
switch uses source `recovery`, event `daemon_restart`, and confidence 1.

### Atomic decision batch

A decision writes events in this order:

1. `agent.signal`;
2. an optional `error`;
3. an optional `state_changed`.

All events use consecutive sequence numbers in one `AppendEvents` transaction.
A signal with no state effect still writes `agent.signal`.

### Reader behavior

The reader-first version:

- accepts creation payload versions 1 and 2;
- maps creation version 1 to hook policy `off`;
- accepts an empty legacy state payload;
- parses state evidence version 1;
- recognizes `agent.signal` as projection-neutral;
- validates matching nonempty session and Agent IDs;
- rejects malformed known payload versions;
- counts and skips an unknown `agent.signal` payload version;
- counts and skips unknown state evidence while still applying the state
  columns;
- continues to reject an unknown event type.

`RecoveryReport` adds counters for unknown signal payload and state evidence
versions. Unknown audit metadata does not prevent recovery because it does not
drive the projection.

## Adapter boundary

`adapter.Entry` gains an exact hook normalizer:

```go
type HookInput struct {
	DeliveryID string
	ReceivedAt time.Time
	Payload    json.RawMessage
}

type HookNormalizer interface {
	NormalizeHook(HookInput) (detect.Signal, error)
}

type Entry struct {
	Runner         Runner
	Heuristic      Heuristic
	HookNormalizer HookNormalizer
}

func (r *Registry) Lookup(vendor string) (Entry, bool)
```

Existing command lookup may retain its generic fallback. Signal lookup must
use `Lookup` and reject an unknown vendor. A hook payload can never fall back
to generic.

Claude and Codex normalizers own:

- vendor event allowlists;
- required field checks;
- root and subagent scope mapping;
- Claude Notification allowlists;
- confidence and normalized kind selection;
- redaction and string bounds.

The normalizer ignores unrelated extra fields inside a known vendor event.
Vendor schemas add fields over time. The normalizer returns only allowlisted
scalars and enums.

An unknown vendor event returns an adapter error. An unknown Claude
Notification subtype becomes a neutral observed signal with notification
`other`; it does not enter Blocked.

Phase 1A uses this Claude event mapping:

| Claude event | Normalized kind | Scope and effect |
| --- | --- | --- |
| `SessionStart` | `session_started` | root; activates hooks |
| `UserPromptSubmit` | `turn_started` | root; Working |
| `PreToolUse` | `tool_activity` | payload scope; root activity is Working |
| `PostToolUse` | `tool_activity` | payload scope; root activity is Working |
| `PostToolUseFailure` | `tool_activity` | payload scope; records failed activity without persisting raw error text |
| `PostToolBatch` | `tool_activity` | root; Working |
| `PermissionRequest` | `permission_requested` | root; 750-millisecond Blocked candidate |
| `PermissionDenied` | `permission_resolved` | root; cancels a permission candidate without a direct transition |
| `Elicitation` | `human_input_required` | root; Blocked |
| `ElicitationResult` | `human_input_resolved` | root; Working from Blocked or Idle |
| `Notification` with a waiting subtype | `human_input_required` | root; Blocked |
| `Notification` with `idle_prompt` | `idle_prompt` | root; one-second Idle candidate |
| `Notification` with a recovery subtype | `human_input_resolved` | root; Working from Blocked or Idle |
| `Notification` with `agent_completed` | `task_completed` | root; audit only |
| Other `Notification` subtype | `observed` | root; audit only, subtype stored as `other` |
| `Stop` | `turn_stopped` | root; one-second Idle candidate |
| `StopFailure` | `turn_failed` | root; one-second Idle candidate |
| `SubagentStart` | `subagent_started` | subagent; root remains or becomes Working |
| `SubagentStop` | `subagent_stopped` | subagent; audit only |
| `TaskCompleted` | `task_completed` | root; audit only |
| `SessionEnd` | `session_ended` | root; audit only |

Waiting Notification subtypes are `permission_prompt`,
`elicitation_dialog`, `elicitation_url_dialog`, `agent_needs_input`, and
`quota_auto_resume_stale`. Recovery subtypes are `auth_success`,
`elicitation_complete`, and `elicitation_response`.
`quota_auto_resume_fired` and `quota_auto_resume_disabled` are neutral
observations.

Phase 1A uses all 12 Codex hook events:

| Codex event | Normalized kind | Scope and effect |
| --- | --- | --- |
| `SessionStart` | `session_started` | root; activates hooks |
| `UserPromptSubmit` | `turn_started` | root; Working |
| `PreToolUse` | `tool_activity` | payload scope; root activity is Working |
| `PermissionRequest` | `permission_requested` | root; 750-millisecond Blocked candidate |
| `PostToolUse` | `tool_activity` | payload scope; root activity is Working |
| `PreCompact` | `observed` | root; audit only |
| `PostCompact` | `observed` | root; audit only |
| `SubagentStart` | `subagent_started` | subagent; root remains or becomes Working |
| `SubagentStop` | `subagent_stopped` | subagent; audit only |
| `Stop` | `turn_stopped` | root; one-second Idle candidate |
| `Interrupt` | `interrupted` | root; one-second Idle candidate |
| `SessionEnd` | `session_ended` | root; audit only |

Phase 1A does not declare a minimum CLI version. Claude's public event list is
dynamic, and Codex hooks are still changing. Support is capability-based:

- a session is hook-active only after one event in the table commits;
- an unsupported or unknown delivered event returns 422 and does not activate
  hooks;
- `auto` enters fallback when no supported event commits within five seconds;
- `required` fails when no supported event commits within five seconds;
- a vendor executable without command-hook support follows the same timeout
  behavior;
- manual verification records the exact tested vendor versions.

This rule closes version-detection failure without parsing CLI version output
or maintaining a version range that can become stale.

Tests use fixed, redacted fixtures taken from the documented Claude and Codex
schemas.

## Session delivery contract

The API converts the wire envelope into this transport-neutral value:

```go
package session

type HookDelivery struct {
	Token      string
	Vendor     string
	DeliveryID string
	Payload    json.RawMessage
}

func (m *Manager) DeliverHook(
	ctx context.Context,
	id agent.ID,
	delivery HookDelivery,
) error
```

`Manager.DeliverHook` stamps `ReceivedAt` from the Manager clock. The HTTP
client cannot choose the ordering timestamp.

Session exposes sentinel errors for stable API mapping:

```go
var (
	ErrHookUnauthorized         = errors.New("session: hook unauthorized")
	ErrHookDisabled             = errors.New("session: hooks disabled")
	ErrHookVendorMismatch       = errors.New("session: hook vendor mismatch")
	ErrHookDetached             = errors.New("session: hook session detached")
	ErrHookInvalid              = errors.New("session: invalid hook signal")
	ErrHookUnsupported          = errors.New("session: hook unsupported")
	ErrHookBackpressure         = errors.New("session: hook backpressure")
	ErrHookRequired             = errors.New("session: required hook not observed")
	ErrInvalidHookPolicy        = errors.New("session: invalid hook policy")
	ErrEventCommitterUnavailable = errors.New("session: event committer unavailable")
	ErrSignalOriginUnavailable  = errors.New("session: signal origin unavailable")
)
```

The Manager checks conditions in this order:

1. unknown Agent;
2. detached session;
3. policy;
4. token;
5. vendor match and exact adapter support;
6. adapter payload normalization;
7. observation admission;
8. durable commit.

Checking policy before the token lets a known `off` session return the
documented 409 even though it has no token.

`ErrUnknownAgent` maps to 404. The hook sentinels map to the endpoint statuses
below. For `POST /api/v1/agents`, `ErrInvalidHookPolicy` and
`ErrHookUnsupported` map to 400. `ErrHookRequired`,
`ErrManagerClosed`, `ErrEventCommitterUnavailable`, and
`ErrSignalOriginUnavailable` map to 503. Internal errors wrap one of these
sentinels with `%w`; the API never parses an error string or imports adapter or
Detector error types.

## Signal endpoint

Register:

```text
POST /api/v1/agents/{id}/signal
```

The request requires `application/json`, a loopback peer, and:

```text
Authorization: Bearer <DROVE_SIGNAL_TOKEN>
```

The strict envelope is:

```json
{
  "version": 1,
  "vendor": "claude",
  "delivery_id": "550e8400-e29b-41d4-a716-446655440000",
  "payload": {}
}
```

The API rejects unknown envelope fields and trailing JSON values. The nested
vendor payload is passed to the exact adapter.

The raw payload limit is 1 MiB. The encoded envelope limit is 1 MiB plus
4 KiB. Both the relay and API reject invalid UTF-8 and more than one JSON
value.

Each session token contains 32 random bytes encoded with base64url. The Manager
stores only its SHA-256 digest and compares digests with
`subtle.ConstantTimeCompare`. The token is never persisted or logged.

Status mapping is:

| Condition | HTTP |
| --- | ---: |
| Committed signal or committed duplicate | 204 |
| Invalid envelope, UTF-8, version, or delivery ID | 400 |
| Missing or incorrect token | 401 |
| Non-loopback peer | 403 |
| Unknown Agent | 404 |
| Hooks off, unsupported adapter, or vendor mismatch | 409 |
| Detached session or expired token | 410 |
| Body over the limit | 413 |
| Invalid or unsupported vendor event | 422 |
| Observation inbox remains full for 250 ms | 429 |
| Manager closing or committer poisoned | 503 |

No response includes the token or raw vendor payload.

## Hook relay

Add:

```bash
drove hook --vendor <vendor>
```

`cmd/drove` reads stdin and environment values, then calls this transport API:

```go
package client

const MaxHookPayloadBytes = 1 << 20

type HookRelayConfig struct {
	AgentID  string
	SignalURL string
	Token    string
}

func NewHookRelay(config HookRelayConfig) (*HookRelay, error)
func (r *HookRelay) Forward(
	ctx context.Context,
	vendor string,
	payload []byte,
) error
```

The command reads at most `MaxHookPayloadBytes + 1` bytes from stdin. It reads
the three environment variables and passes their values to
`NewHookRelay`. It does not parse vendor semantics.

The client:

1. validates a single UTF-8 JSON object up to 1 MiB;
2. validates an absolute HTTP URL with a loopback host;
3. rejects user information, a query, a fragment, or an Agent ID path
   mismatch;
4. generates one UUID delivery ID;
5. sends the strict envelope with the bearer token;
6. retries one network, `429`, or `503` failure after 100 milliseconds;
7. reuses the delivery ID for the retry.

Each attempt has a 750 millisecond timeout. The relay never calls
`EnsureDaemon` and never reads Drove config.

The relay is observational. After Cobra accepts the command shape, malformed
input, missing environment, authentication failure, adapter rejection, or
exhausted delivery writes one redacted diagnostic to stderr and exits 0. It
must not block or alter the vendor action. `required` policy reports failure
through the daemon activation deadline.

## Process environment

For `auto` and `required`, Manager injects:

```text
DROVE_AGENT_ID=<agent UUID>
DROVE_SIGNAL_URL=http://127.0.0.1:<port>/api/v1/agents/<escaped-id>/signal
DROVE_SIGNAL_TOKEN=<base64url capability>
```

Bootstrap keeps the approved recovery order and finishes before
`net.Listen`. After the listener opens, the daemon configures its actual
address exactly once:

```go
func (m *Manager) ConfigureSignalOrigin(origin *url.URL) error
```

The daemon may construct the API server before it opens the listener, as the
approved recovery specification requires. It calls this method after
`net.Listen` and before `Serve`. The method accepts only an absolute loopback
HTTP origin with no user information, query, fragment, or path. It rejects a
second call.

This one-time step supports an ephemeral test port without moving the listener
ahead of recovery. A hook-enabled `Start` returns
`ErrSignalOriginUnavailable` before creation if the daemon has not completed
the step. An `off` session does not need the origin.

`off` injects none of these variables.

## Status contract

`session.Status` adds:

```go
HookPolicy     agent.HookPolicy `json:"hook_policy"`
HookStatus     detect.HookStatus `json:"hook_status"`
LastTransition *agent.Evidence  `json:"last_transition,omitempty"`
```

Hook status values are:

- `off`;
- `awaiting_hook`;
- `fallback`;
- `hook_active`;
- `required_failed`;
- `detached`.

The values report observed runtime state. They do not claim that a vendor
configuration exists or that trust was granted.

Recovered sessions report `detached`. Their policy comes from creation
metadata, and their last transition evidence comes from the newest understood
state payload.

The TypeScript Status and event unions receive the same additive fields. This
phase does not add Web controls.

## Shutdown

Normal shutdown keeps this dependency order:

1. Stop the API server and wait for accepted handlers.
2. Mark Manager closed and reject new operations.
3. Wait for in-progress starts.
4. Mark stop causes and close PTYs while Detectors and the committer still run.
5. Let exit callbacks claim and commit one terminal decision.
6. Let PTY close drain all output and callback work.
7. Close Detector actors and cancel their timers.
8. Close and drain the global committer.
9. Close Hub subscriptions.
10. Close SQLite.

Manager, Detector, committer, and Hub close operations are idempotent.

If the committer is poisoned, Manager cannot promise terminal events. It
unblocks accepted work, closes PTYs, returns the root cause, and leaves the
last durable batch as the recovery boundary.

## Compatibility and rollout

The release is reader-first:

1. Land readers for creation version 2, `agent.signal`, and state evidence
   version 1. Keep every new writer disabled.
2. Prove the new reader restores old logs and seeded new-format logs.
3. Add and test the private committer without routing runtime events to it.
4. Switch every runtime writer to the committer in one commit.
5. Enable Detector-produced signals only after the writer switch.
6. Add the endpoint and relay last.

Once a writer emits `agent.signal` or creation version 2, the reader-first
commit is the rollback floor. A binary older than that commit cannot recover
the database.

Bootstrap reconciliation may call `Store.AppendEvents` directly before the
committer starts. No runtime code may call `AppendEvent`, allocate a sequence
through Hub, or publish a zero-sequence event.

## Module ownership

| Module | Owns | Does not own |
| --- | --- | --- |
| `cmd/drove` | stdin and environment access, command output | vendor semantics, daemon startup for relay |
| `internal/client` | bounded relay transport, retry, URL validation | vendor parsing, state decisions |
| `internal/api` | loopback, bearer shape, body limit, strict envelope, HTTP mapping | token ownership, vendor mapping, state policy |
| `internal/adapter` | vendor payload parsing, event allowlists, redaction, normalized signals | persistence, timers, Agent mutation |
| `internal/detect` | authority, candidates, debounce, dedupe, state decisions | goroutines, HTTP, vendor JSON, SQLite |
| `internal/session` | actor lifetime, token association, global commit, projection apply, PTY ordering | vendor-specific branches |
| `internal/agent` | metadata, live and historical transition rules, prepared changes | persistence, Hub, HTTP |
| `internal/event` | drafts, committed events, payload schemas, Hub broadcast | sequence allocation, vendor policy |
| `internal/store` | append-only SQLite transaction and replay | state or hook policy |

## Alternatives considered

### Public commit package

A public `internal/commit` package would need to expose Agent changes, apply
rules, failure state, and shutdown ordering. Session callers would still need
to understand the transaction protocol. Keeping the committer private hides
more policy behind a smaller interface.

### Detector-owned commit callback

A `DecisionSink` or `apply func()` makes Detector correctness depend on an
arbitrary side effect. The callback can capture mutable state or violate
commit-before-apply. The chosen design keeps `Decide` pure and lets the
session actor submit a sealed operation.

### One global Detector

One actor could order all signals and events. It would also share every
session timer, hook latch, and delivery cache. Per-session actors isolate that
state, while the global committer merges only immutable operations.

### Reverse PTY exit and output callbacks

Waiting for the output reader before `OnExit` would order final output before
the terminal event. It would also delay process state indefinitely when a
descendant retains the PTY file descriptor. The existing completion contract
already drains both callbacks before resource teardown, so Phase 1A keeps it.

### Remove `done -> stopped` reconciliation

Done is terminal for live decisions, but a recovered process has no PTY.
Changing this rule would break the approved session-recovery contract and
existing logs. Phase 1A separates live and historical transition checks
instead.

## Verification

Tests must cover:

- old creation and state events under the new reader;
- seeded creation version 2, signal version 1, and state evidence version 1;
- unknown audit payload versions and malformed known versions;
- live versus recovery transition tables;
- `done -> stopped` reconciliation without a new error;
- event draft sequencing and Hub rejection of gaps or zero sequences;
- concurrent sessions producing one SQLite and Hub order;
- append failure before apply;
- apply or publish invariant failure after a durable commit;
- committer poisoning, queued waiter release, and idempotent close;
- every legacy runtime writer using the committer;
- hook authority activation and one-way heuristic suppression;
- Stop, permission, heuristic, silence, and activation timers with a fake clock;
- timer generation races and stale timer audit;
- duplicate delivery IDs and bounded cache eviction;
- semantic duplicates with new delivery IDs;
- root and subagent decisions;
- Blocked recovery;
- interactive and oneshot process exits;
- required-hook timeout and short oneshot exit;
- early hook delivery waiting on the readiness gate;
- input versus exit ordering;
- stop versus natural exit single-winner behavior;
- terminal state followed by trailing PTY output;
- Claude and Codex redacted fixtures;
- unknown vendor, unknown event, and unknown Notification subtype;
- loopback, token, body, envelope, status, and backpressure behavior;
- relay URL checks, retry ID reuse, timeout, stdout, and exit behavior;
- old-log restart and new signal-log restart;
- full race, vet, Go build, Web typecheck, and Web build checks.

Manual verification must cover:

- Claude with a trusted manual project hook;
- Claude with an untrusted or disallowed hook under `auto`;
- Claude under `required` without an active hook;
- Codex with a trusted manual project hook;
- Codex with an untrusted hook under `auto`;
- Blocked followed by `drove send` and hook-confirmed Working;
- concurrent Claude and Codex sessions;
- daemon restart after signal and state batches;
- confirmation that no prompt, tool input, transcript path, assistant message,
  or token appears in SQLite.

## Acceptance criteria

- One global goroutine assigns every runtime event sequence.
- Every runtime event uses `Store.AppendEvents`.
- SQLite commits before Agent or Detector memory changes.
- Agent and Detector memory change before Hub publishes the same batch.
- A failed append changes neither memory nor Hub.
- A failed durable pipeline stops further commits and triggers daemon
  shutdown.
- The old callback mutation path and Hub sequence allocator no longer exist.
- One observation actor proposes all live state changes for each session.
- A valid current-session hook permanently outranks heuristics.
- Hook absence alone never changes Agent state.
- Blocked can recover to Working.
- Stop and Interrupt use a cancelable confirmation window.
- Interactive sessions never infer Done.
- A successful oneshot exit is the only live source of Done.
- Bootstrap still moves Done to Stopped without adding an interruption error.
- Input delivery keeps its delivered-but-unaudited error contract.
- Final PTY output may follow the terminal state event and remains durable.
- The endpoint accepts only loopback, authenticated, bounded deliveries.
- Raw vendor JSON and sensitive content never enter the event log.
- `drove hook` cannot block a vendor action through its exit status.
- Phase 1A does not modify vendor configuration or trust state.
- Issue #4 remains open for WebSocket input.

## Risks and controls

### One slow SQLite write blocks every session

The store already has one SQLite connection and one durable global order.
Bounded queues make this limit visible instead of allowing unbounded memory.
The existing five-second SQLite busy timeout sends persistent stalls to the
fatal shutdown path.

### A hook arrives before attachment completes

The pending session and token are registered before `pty.Start`. The delivery
waits on the readiness gate and cannot skip lifecycle transitions.

### A hook disappears after activation

The session does not switch back to heuristics. Switching authorities after
silence would let two imperfect sources alternate control. Status remains
`hook_active`; later health reporting can show last signal time.

### The relay receives a large sensitive payload

Both sides cap the payload at 1 MiB. The adapter extracts only allowlisted
metadata. The raw bytes are released after normalization and never reach
event constructors or logs.

### SQLite commits but projection application fails

The event log remains authoritative. The committer advances its durable
boundary, publishes nothing, and stops the daemon. Restart reconstructs the
projection from the committed batch.

### A final output line follows Stopped or Done

This is an existing PTY behavior, not a state reversal. The output remains
durable and receives the next global sequence. Detector state stays terminal.

## Synthesis decision

The design arena compared three shapes:

- a Manager-private committer with a pure Detector;
- a public transaction executor with typed Agent changes;
- a Detector actor that emits immutable commit plans through a sink.

The first shape is the base because it matches `internal/session` ownership and
keeps Detector free of persistence. The design adopts typed Agent changes from
the second shape and sealed projection operations from the third.

It rejects a public committer, arbitrary apply callbacks, Detector-owned
persistence, duplicate delivery audit rows, PTY callback reordering, and
removal of bootstrap `done -> stopped`.

## Approval gate

Implementation starts only after the owner approves this specification,
`checklist.md`, and `tasks.md`.
