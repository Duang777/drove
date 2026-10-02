# Restart-safe event sequence

Status: Draft, awaiting owner approval

## Problem

Drove stores every event in SQLite with `events.seq` as the primary key. The in-memory `event.Hub` owns sequence allocation and starts at zero whenever `droved` starts.

After a daemon restart, the database can already contain sequence values while the new Hub allocates from one again. `Store.AppendEvent` then fails with a primary-key conflict. `Manager.persistAndPublish` emits an in-memory error event and returns, so the agent continues to run but its new events are absent from replay.

This behavior was reproduced against commit `50214ba`:

1. Start the daemon with an empty isolated data directory.
2. Run two generic commands and persist events 1 through 7.
3. Stop and restart the daemon with the same data directory.
4. Start another generic command.
5. Observe that the command reaches `stopped`, while its replay response is `null` and the database still contains only events 1 through 7.

## Goal

Continue event sequence allocation from the largest persisted sequence whenever the daemon opens an existing database.

After this change, a daemon restart must not cause sequence reuse or stop new events from being persisted.

## Non-goals

This change does not:

- Rebuild the in-memory agent list from historical events.
- Reconnect to agent processes created by an earlier daemon process.
- Change the event table schema or existing event JSON.
- Add multi-daemon writes to one SQLite database.
- Add API, CLI, or Web features.
- Redesign persistence failure handling outside this reproduced sequence collision.

## Assumption

One daemon process owns one Drove database at a time.

Reading `MAX(seq)` during startup is sufficient under this invariant. If multiple daemon processes must write the same database later, sequence allocation must move into a database transaction.

## Required behavior

### Empty database

When the event table is empty:

- `Store.LastSeq()` returns zero.
- The Hub starts from zero.
- The first allocated sequence is one.

### Existing database

When the largest persisted sequence is `N`:

- Daemon startup reads `N` before accepting API requests.
- The Hub starts with `N` as its current sequence.
- The first newly allocated sequence is `N + 1`.
- Every later allocation is strictly increasing within that daemon process.

### Startup failure

If the daemon cannot read the largest persisted sequence:

- Startup returns an error.
- The API listener does not start.
- The error wraps the storage error with daemon startup context.

The daemon must not fall back to zero because that can silently disable persistence.

### Compatibility

- Existing SQLite databases remain valid.
- No migration or backfill runs.
- Existing event rows remain unchanged.
- REST and WebSocket payloads remain unchanged.
- The event ordering contract remains a monotonically increasing unsigned sequence.

## Design

### Event Hub constructor

Change the constructor to make the starting sequence explicit:

```go
func NewHub(initialSeq uint64) *Hub
```

The constructor stores `initialSeq` in the Hub's atomic sequence field. `NextSeq()` keeps its current behavior and returns `initialSeq + 1` on the first call.

All callers must pass an explicit value:

- Tests that need a new stream pass `0`.
- Daemon startup passes the value returned by `Store.LastSeq()`.

Do not add a variadic argument or a second constructor. A required argument makes sequence ownership visible at every construction site.

### Daemon startup

After `store.Open` succeeds and before `event.NewHub` runs:

```text
open store
read last persisted sequence
construct Hub from that sequence
construct registry and Manager
start API listener
```

Extract a small unexported daemon helper only if it gives the restart behavior a direct test seam. Do not introduce a public runtime-builder abstraction for this change.

### Preset sequence behavior

`Hub.Publish` currently preserves a nonzero sequence supplied by its caller. This change does not alter that behavior.

Production session events continue to use:

```go
ev.Seq = hub.NextSeq()
store.AppendEvent(ev)
hub.Publish(ev)
```

Changing preset-sequence semantics belongs in a separate change because it affects callers beyond daemon startup.

## Files

Expected implementation files:

- `internal/event/event.go`
- `internal/event/event_test.go`
- `internal/daemon/daemon.go`
- `internal/daemon/daemon_test.go`

Update `docs/technical-notes.md` after implementation so the reproduced P0 is marked resolved.

The expected implementation touches five files. Do not change API handlers, the database schema, session projection, or frontend code.

## Test plan

### Unit tests

Add event Hub tests:

- A Hub initialized with zero allocates sequence one.
- A Hub initialized with 41 allocates sequence 42.
- Two allocations after initialization are consecutive.
- Existing publish and slow-subscriber behavior still passes.

### Daemon startup test

Use a temporary SQLite database:

1. Open the store.
2. Append an event with a known nonzero sequence.
3. Build the daemon event Hub through the startup helper.
4. Assert that the next sequence is one greater than the stored value.

Also cover an empty database.

### Regression verification

Run:

```bash
gofmt -w internal/event/event.go internal/event/event_test.go internal/daemon/daemon.go internal/daemon/daemon_test.go
go test ./internal/event ./internal/daemon -race
go test ./... -race
go vet ./...
make build
```

Then repeat the isolated restart scenario:

1. Start `droved` with a temporary data directory.
2. Start a short generic command.
3. Record the largest persisted sequence.
4. Restart `droved` with the same data directory.
5. Start another short generic command.
6. Confirm that the new session has replayable events and every new sequence exceeds the previous maximum.

## Acceptance criteria

- Existing databases open without migration.
- Empty databases still allocate their first event as sequence one.
- Restarting the daemon continues from the persisted maximum sequence.
- New events after restart are present in SQLite and available from replay.
- No duplicate primary-key error occurs in the restart test.
- The API payload format does not change.
- `go test ./... -race`, `go vet ./...`, and `make build` pass.
- The implementation remains within the files listed in this spec unless a failing test proves that another file is required.

## Risks and controls

### Two daemons use one database

The proposed fix does not coordinate sequence allocation across processes. The project currently assumes one daemon per database, and the default bind address prevents a second daemon on the same endpoint.

Control: document the single-writer assumption in the test and implementation. Do not claim multi-writer safety.

### Future event import

A caller can publish an event with a preset sequence that does not update the Hub counter.

Control: keep this behavior unchanged in the current patch. Add a separate issue if event import or replay into a live Hub becomes a supported operation.

### Scope expansion into recovery

Restoring the sequence does not restore session metadata or process handles.

Control: keep session projection recovery and process reconnection out of this patch. The next spec must decide their product semantics first.

## Rollback

The change does not alter stored data. Reverting the code restores the old startup behavior, although the old behavior can collide with existing rows after another restart.

No database rollback is required.
