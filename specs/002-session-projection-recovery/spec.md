# Restart-safe session projection recovery

Status: Draft, awaiting owner approval

## Problem

Drove stores session history as an append-only SQLite event stream, but
`session.Manager` rebuilds empty `agents` and `sessions` maps whenever the
daemon starts.

The previous change made event sequence allocation restart-safe. It did not
restore the session projection. After a restart:

- `GET /api/v1/agents` returns an empty list.
- `GET /api/v1/agents/{id}` returns an unknown-agent error.
- Replay still works when the caller already knows the session ID.
- Historical events may claim that an agent is `working`, `blocked`, or
  `starting` even though the new daemon has no PTY or process handle for it.

The existing v1 event stream also lacks the `name` and `vendor` fields required
by `session.Status`. `PID` cannot be recovered safely, and `LastError` is not
consistently persisted.

## Goal

Rebuild the in-memory session projection from the append-only event log before
the daemon opens its API listener.

Every historical session must be queryable after restart. A session without a
PTY owned by the current daemon must be represented as `stopped`, with an
append-only reconciliation event recording why its previous state ended.

New sessions must persist the minimum metadata needed for exact projection
recovery.

## Non-goals

This change does not:

- Reconnect to, restart, signal, or kill a process created by an earlier daemon.
- Persist or trust historical PIDs.
- Persist commands, arguments, directories, environment variables, or terminal
  contents beyond the existing output events.
- Add an `interrupted` agent state.
- Add a mutable `sessions` table or any second state authority.
- Change REST routes, WebSocket fields, or `session.Status` JSON fields.
- Add projection checkpoints, pagination, retention, or event compaction.
- Make multiple daemon processes safe writers of one database.
- Redesign all runtime persistence failure handling.
- Repair, delete, or rewrite malformed historical events.

## Assumptions

One daemon process owns one Drove database at a time.

This plan also assumes the event log is small enough for one ordered streaming
scan during startup. If startup latency becomes unacceptable, a later change
may add a discardable checkpoint that is verified against the event sequence.
That checkpoint must remain a cache, not a second source of truth.

## Chosen approach

Add one startup boundary in `internal/session`:

```go
type BootstrapResult struct {
	Manager  *Manager
	Hub      *event.Hub
	Recovery RecoveryReport
}

func Bootstrap(
	ctx context.Context,
	reg *adapter.Registry,
	st *store.Store,
) (*BootstrapResult, error)
```

`session.Bootstrap` performs the complete recovery transaction:

1. Stream persisted events in global sequence order.
2. Fold lifecycle, state, and error facts into one draft per session.
3. Apply explicit compatibility rules for existing v1 events.
4. Plan reconciliation events for sessions that have no current PTY.
5. Validate every final snapshot through `internal/agent`.
6. Append all reconciliation events in one SQLite transaction.
7. Initialize the Hub after the committed maximum sequence.
8. Build a Manager containing restored Agent objects and no historical PTYs.
9. Return only after all steps succeed.

The daemon creates the API server and TCP listener after `Bootstrap` returns.

This keeps the public startup sequence small while preserving package
ownership:

```text
internal/daemon
    |
    +--> store.Open
    +--> session.Bootstrap
    |       +--> store.ScanEvents
    |       +--> session projector
    |       +--> agent.Restore
    |       +--> store.AppendEvents
    |       +--> event.NewHub
    |       +--> session.Manager
    |
    +--> api.NewServer
    +--> net.Listen
```

## Required behavior

### Startup ordering

The daemon startup order is:

```text
validate config
open and migrate store
bootstrap session projection
construct API server
open TCP listener
serve requests
```

No REST or WebSocket client can observe an empty or partially restored
projection.

If event scanning, projection, snapshot validation, or reconciliation commit
fails:

- `session.Bootstrap` returns a wrapped error.
- The daemon does not create the listener.
- No partially built Manager or Hub becomes reachable.
- A failed reconciliation transaction leaves no reconciliation rows behind.

### Ordered store scan

Add a streaming store operation:

```go
func (s *Store) ScanEvents(
	ctx context.Context,
	visit func(EventRow) error,
) (lastSeq uint64, err error)
```

It reads every row with `ORDER BY seq ASC`, parses the stored timestamp, and
calls `visit` once per row. It returns zero for an empty database.

Sequence rules:

- Sequence zero is invalid.
- Observed sequences must be strictly increasing.
- Gaps are allowed because the existing runtime can allocate a sequence before
  a persistence failure.
- A callback error stops the scan and is wrapped with the current sequence.

The callback keeps only per-session projection data. Output payloads are not
retained in memory.

### Atomic reconciliation append

Add a store operation:

```go
func (s *Store) AppendEvents(
	ctx context.Context,
	expectedLastSeq uint64,
	rows []EventRow,
) (newLastSeq uint64, err error)
```

The method uses one SQLite transaction and only performs inserts.

It must:

- Verify that the current database maximum equals `expectedLastSeq`.
- Require every input row to continue from `expectedLastSeq + 1`.
- Require input sequences to be consecutive within the batch.
- Roll back the entire batch if validation, an insert, or commit fails.
- Return `expectedLastSeq` for an empty batch.

The maximum check is a guard against an unsupported second writer appearing
between the startup scan and reconciliation commit.

### Session metadata event

New sessions persist one lifecycle event before the first state transition:

```text
Type:       session_lifecycle
Reason:     created
SessionID:  the agent UUID
AgentID:    the same agent UUID
Payload:    {"version":1,"name":"build-api","vendor":"generic"}
```

The session package owns this payload schema:

```go
type createdPayload struct {
	Version int    `json:"version"`
	Name    string `json:"name"`
	Vendor  string `json:"vendor"`
}
```

The event package remains unaware of payload fields. Its lifecycle constructor
accepts the encoded payload:

```go
func NewSessionLifecycle(
	seq uint64,
	sessionID string,
	agentID string,
	reason string,
	payload string,
) Event
```

The creation event contains no command, arguments, directory, environment, or
PID.

`Manager.Start` validates and resolves the request before writing this event.
An invalid generic request therefore creates no persistent session.

Once the creation event is committed, the session exists as an auditable
record. If PTY startup fails, Manager records a nonempty `error` event followed
by `starting -> stopped`, keeps the stopped Agent queryable, and returns the
startup error to the caller.

If the creation event cannot be persisted, Manager does not start a PTY.

### Projection ownership

The event-to-session reducer lives in `internal/session`, preferably in
`projection.go`.

The store package only returns `EventRow` values. It does not import the agent
state machine or interpret event payloads.

The event package defines the event envelope. It does not import session
metadata types.

The agent package remains the only state authority. It adds a validated restore
constructor:

```go
type RestoreSnapshot struct {
	ID        ID
	Name      string
	Vendor    string
	State     State
	LastError string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func Restore(snapshot RestoreSnapshot, opts ...Option) (*Agent, error)
```

`Restore` validates the snapshot and constructs an Agent without invoking its
state-change hook. Every state change after construction still uses
`Agent.Transition`.

The projector never exposes its mutable draft state to daemon, API, or client
packages.

### Projection rules

Events are folded in global sequence order.

Each nonempty session ID identifies one session. Current Drove behavior requires
the nonempty agent ID to match the session ID. A mismatch is a projection
error.

The reducer handles event types as follows:

| Event type | Projection effect |
| --- | --- |
| `session_lifecycle` with reason `created` | Decode metadata and initialize the session at `pending` |
| `state_changed` | Validate the states and update the current state |
| `error` | A nonempty payload becomes the latest persisted `LastError` |
| `output` | No status change; payload is not retained |

The first event for a session establishes `CreatedAt`.

`UpdatedAt` is the timestamp of the latest lifecycle, state, or nonempty error
fact. Output events do not change it.

Event sequence, not timestamp, determines ordering. A timestamp earlier than
`CreatedAt` is accepted, but the exposed `UpdatedAt` is clamped to
`CreatedAt`.

### State-chain compatibility

Every state event must have:

- A valid `from` state.
- A valid `to` state.
- A transition accepted by `agent.CanTransition(from, to)`.

Existing v1 data may have missing persisted events because the current runtime
changes in-memory state before its write-only hook attempts persistence.

Compatibility rules:

- The first valid state event for a session may establish its state from that
  event's `from` value.
- A later event whose `from` differs from the projected state normally fails
  recovery.
- It may re-anchor from that event only when the global scan observed at least
  one missing sequence between the previous state fact and the mismatching
  event.
- Every re-anchor increments `RecoveryReport.PartialHistory`.
- A sequence gap by itself is not an error and does not change any state.

This rule accepts known persistence gaps without silently accepting an
internally contradictory complete log.

### Legacy metadata compatibility

Existing databases have no creation lifecycle event.

When a session has state history but no `created` event:

- `Name` is the complete agent ID.
- `Vendor` is `unknown`.
- `CreatedAt` is the first event timestamp for that session.
- `RecoveryReport.LegacyMetadata` is incremented.

The reducer does not infer vendor from output, name, command text, or reason.

A session that contains only output or error rows and no creation or state event
does not create a Status entry. Its rows remain available through Replay.

### Historical state reconciliation

The new daemon owns no historical PTY. Every restored state except `stopped`
must therefore become `stopped` before the API listener opens.

Reconciliation uses these rules:

| Last persisted state | Appended events | Restored state |
| --- | --- | --- |
| `pending` | restart error, then `pending -> stopped` | `stopped` |
| `starting` | restart error, then `starting -> stopped` | `stopped` |
| `working` | restart error, then `working -> stopped` | `stopped` |
| `blocked` | restart error, then `blocked -> stopped` | `stopped` |
| `idle` | restart error, then `idle -> stopped` | `stopped` |
| `done` | `done -> stopped` | `stopped` |
| `stopped` | none | `stopped` |

The error payload for non-done active states is:

```text
session interrupted by daemon restart; previous PTY is not reconnectable
```

The state-change reason is:

```text
recovered after daemon restart without PTY
```

`done` receives no new error because task completion was already observed. Its
historical `done` event remains in Replay, while current control state becomes
`stopped`.

Reconciliation order is stable:

1. Sort sessions by first event sequence.
2. Break ties by agent ID.
3. Append each session's reconciliation rows in that order.

All rows use one recovery timestamp. Already stopped sessions append nothing,
so a second restart is idempotent.

The Hub starts after the committed reconciliation maximum. Reconciliation
events are not published as live WebSocket events because no listener or
subscriber exists during bootstrap. They remain available through Replay.

### Bad-event policy

Recovery fails closed for projection-critical corruption.

The error must include the event sequence and session ID when available.

Fatal cases:

- SQL query, row scan, or timestamp parsing fails.
- Sequence is zero, repeated, or decreases.
- Event type is unknown.
- A state or lifecycle event has an empty session ID.
- Nonempty agent ID differs from session ID.
- Lifecycle reason is unknown.
- Creation metadata JSON is malformed, duplicated, unsupported, or has an empty
  name or vendor.
- State names or transitions are invalid.
- A state mismatch cannot use the explicit partial-history rule.

An empty error payload remains in Replay but does not replace `LastError`.

Daemon-level output or error events with an empty session ID do not create an
Agent. State and lifecycle events with an empty session ID are fatal.

Recovery never updates, deletes, or rewrites the offending row.

### Restored Manager behavior

Restored Agent objects are inserted into `Manager.agents`.

Historical sessions are not inserted into `Manager.sessions`. Their Status
therefore has `PID == 0`, and the existing `omitempty` tag omits `pid` from
JSON.

`Manager.Stop` remains idempotent:

- A restored stopped Agent with no PTY returns nil.
- An unknown Agent still returns an error.
- An Agent that claims a live state but has no PTY is an internal invariant
  error.

`Manager.Write` continues to require a live PTY and returns an explicit
not-attached error for a restored session.

`Manager.List` must:

- Copy Agent references while holding the Manager read lock.
- Release the lock before calling `Status`.
- Sort by `CreatedAt`, then by `AgentID`.

This removes the current nested read-lock risk and makes restored list order
deterministic.

### API compatibility

No REST or WebSocket schema changes are required.

- `GET /api/v1/agents` includes restored sessions.
- `GET /api/v1/agents/{id}` returns a restored session.
- `GET /api/v1/agents/{id}/events` includes reconciliation events.
- Restored sessions omit `pid`.
- Legacy sessions return `vendor: "unknown"`.
- Existing event fields remain unchanged.
- No frontend type change is required.

## Recovery report

`BootstrapResult.Recovery` uses a compact startup-only report:

```go
type RecoveryReport struct {
	ScannedEvents  int
	Sessions       int
	Interrupted    int
	LegacyMetadata int
	PartialHistory int
	LastSeq        uint64
}
```

The daemon logs these counts with `slog` after bootstrap. The report is not
exposed through REST or WebSocket.

## Expected files

The implementation is expected to touch 13 files:

- `internal/store/store.go`
- `internal/store/store_test.go`
- `internal/agent/agent.go`
- `internal/agent/agent_test.go`
- `internal/event/event.go`
- `internal/event/event_test.go`
- `internal/session/session.go`
- `internal/session/session_test.go`
- `internal/session/projection.go`
- `internal/session/projection_test.go`
- `internal/daemon/daemon.go`
- `internal/daemon/daemon_test.go`
- `docs/technical-notes.md`

This exceeds eight files because the change crosses four existing ownership
boundaries and adds tests at each boundary. It does not add a new service,
database table, API route, CLI command, config key, or frontend dependency.

## Test plan

### Store tests

- Empty scan returns last sequence zero.
- Scan visits rows in global sequence order.
- Sequence gaps are preserved.
- Callback errors include the current sequence.
- Atomic append continues from the expected maximum.
- A stale expected maximum fails without inserting rows.
- A duplicate sequence inside a batch rolls back the full batch.

### Agent tests

- A valid restored snapshot preserves ID, metadata, state, error, and times.
- Invalid state, empty ID, empty metadata, or inverted timestamps fail.
- A restored Agent still enforces normal transition rules.
- Restore does not call the state-change hook.

### Projector tests

- Empty input produces no sessions.
- Version 1 creation metadata restores exact name and vendor.
- A legacy session uses full ID and `unknown`.
- Every historical state produces the required reconciliation plan.
- `done` becomes `stopped` without a new error.
- Latest nonempty error wins.
- Output does not affect Status timestamps.
- A first state event can establish a partial history.
- A later mismatch re-anchors only when a global sequence gap permits it.
- Every fatal bad-event case returns sequence and session context.
- Multiple sessions produce stable reconciliation and list order.

### Bootstrap tests

Use a temporary SQLite database:

- Empty database returns an empty Manager and a Hub whose next sequence is one.
- Existing stopped sessions restore without new events.
- Existing active sessions commit reconciliation rows and restore as stopped.
- A second bootstrap appends no duplicate reconciliation rows.
- Reconciliation transaction failure returns no usable result.
- The returned Hub continues after the committed reconciliation maximum.

### Session tests

- New sessions write `created` before their first state event.
- Invalid requests write no lifecycle event and start no PTY.
- PTY startup failure records error and stopped facts.
- A restored stopped Agent can be stopped again.
- A restored Agent rejects input because it has no live PTY.
- List ordering is stable and race-safe.

### Daemon and restart verification

Run:

```bash
gofmt -w internal/store internal/agent internal/event internal/session internal/daemon
go test ./internal/store ./internal/agent ./internal/event ./internal/session ./internal/daemon -race
go test ./... -race
go vet ./...
make build
```

Then run an isolated real restart:

1. Start `droved` with a temporary data directory.
2. Start one short generic command and one command that remains active.
3. Record both IDs and the largest persisted sequence.
4. Stop the daemon without reconnecting either PTY.
5. Restart `droved` with the same database.
6. Confirm both sessions appear in `GET /api/v1/agents`.
7. Confirm both have `state: "stopped"` and no `pid`.
8. Confirm Replay includes the restart reconciliation events where required.
9. Confirm a new session starts and uses a sequence greater than every recovery
   event.
10. Restart once more and confirm no duplicate reconciliation rows appear.

## Commit boundaries

Implementation must be delivered as three independently buildable and testable
commits. Push each commit after its verification passes.

### Commit 1: recovery primitives

- Add ordered store scanning and atomic event batches.
- Add validated Agent snapshot restoration.
- Add focused store and agent tests.

This commit changes no daemon or API behavior.

### Commit 2: self-describing session creation

- Add the versioned `created` lifecycle payload.
- Persist it before a new session enters the state machine.
- Record PTY startup failures as error plus stopped.
- Add event and session tests.

Old databases remain valid, and new databases begin collecting exact metadata
for future restarts.

### Commit 3: boot-time projection recovery

- Add the pure session projector and bootstrap boundary.
- Reconcile historical non-stopped sessions.
- Wire daemon startup through bootstrap.
- Fix restored Stop behavior and deterministic List locking and ordering.
- Add bootstrap, daemon, and real restart coverage.
- Update the technical note.

After this commit, the full feature is active.

## Acceptance criteria

- Existing schema version 1 databases open without migration.
- Existing sessions appear in List and Status after daemon restart.
- New sessions recover their exact name and vendor.
- Legacy sessions use the documented metadata fallback.
- Every recovered session without a current PTY is `stopped`.
- Reconciliation facts are appended, never used to rewrite history.
- Reconciliation is atomic and idempotent across repeated restarts.
- The Hub continues after the committed recovery maximum.
- Recovery failure prevents the API listener from opening.
- Restored sessions never expose a historical PID.
- REST and WebSocket JSON fields do not change.
- `go test ./... -race`, `go vet ./...`, and `make build` pass.
- The isolated three-start restart scenario passes.
- Each implementation commit is pushed after its own verification.

## Risks and controls

### Full-log startup scan

Startup time grows with total event count, including output rows.

Control: scan rows as a stream and retain only per-session drafts. Record the
scanned count. Add checkpoints only after measurement proves they are needed.

### Partial historical state chains

The current runtime can lose a persisted state event after changing in-memory
state.

Control: allow a bounded re-anchor only when a global sequence gap provides
evidence of a missing event. Count partial histories in the startup report.

### Unknown future events

An older daemon will reject an event type it cannot interpret.

Control: fail before exposing an incorrect projection. Any future event type
must define its minimum readable version and projection behavior.

### Orphaned old processes

The old OS process may still exist after daemon restart.

Control: never trust a persisted PID and never signal or attach to the process.
`stopped` means the current Drove daemon does not control it.

### Runtime persistence failures remain

Live state hooks can still change memory before an event append fails.

Control: this spec handles historical evidence from that failure mode but does
not redesign the runtime callback contract. A separate change must add fatal
error propagation or transactional state transitions.

## Rollback

The change adds event rows but no database schema.

An older binary can still open the database and replay the new lifecycle,
error, and state events as ordinary rows. It will not restore session
projections because that behavior does not exist in the older code.

Reverting the code requires no database rollback. Reconciliation events remain
valid history and must not be deleted.
