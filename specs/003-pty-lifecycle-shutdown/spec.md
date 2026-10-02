# Race-free PTY lifecycle and safe daemon shutdown

Status: Approved

## Problem

Drove starts each agent in a PTY and uses callbacks to turn process output and
exit facts into persisted events. The current lifecycle has two races.

First, `pty.Start` launches `readLoop` and `waitLoop` before
`session.Manager.Start` assigns `Session.OnOutput` and `Session.OnExit`. A
short process can write output or exit before either callback exists. The race
detector also reports unsynchronized callback reads and writes.

Moving the assignments into `pty.Start` is necessary but not sufficient. A
short process can still invoke `OnExit` before `Manager.Start` attaches the PTY
and records `starting -> working`. That creates an invalid or incomplete state
sequence.

Second, `daemon.Run` shuts down the HTTP server and then closes the store
through a deferred call. It does not close live PTYs or Hub subscriptions.
This can leave child processes running after the daemon exits. A late PTY
callback can also try to append an event after SQLite has closed.

`internal/daemon/AGENTS.md` currently documents API, Hub, store, then session
shutdown. That order conflicts with the runtime dependency: session callbacks
need both the Hub and the store until every PTY has finished.

## Goal

Make PTY callback registration and daemon shutdown race-free.

The completed behavior must guarantee:

- PTY callbacks are fixed before any read or wait goroutine starts.
- A successful start persists `starting -> working` before output or exit
  callbacks can affect the session.
- Closing a PTY waits until its output and exit callbacks finish.
- Closing a Manager stops every attached PTY and waits for its callbacks.
- Closing the Hub terminates every active subscription without a panic.
- The daemon closes resources in dependency order: API, sessions, Hub, store.
- A normal daemon shutdown persists each live session's transition to
  `stopped` before SQLite closes.
- Repeated close calls are safe.

## Non-goals

This change does not:

- Add input or resize REST, CLI, or WebSocket operations.
- Implement RFC-001 runner modes, hooks, detectors, or Blocked recovery.
- Reconnect PTYs after restart.
- Add graceful process signals, escalation delays, or process-group cleanup.
- Change the current direct `Process.Kill` behavior.
- Redesign runtime event persistence or make state transitions transactional.
- Guarantee that trailing output appears before the process-exit state event.
- Add daemon authentication, origin checks, or WebSocket ping messages.
- Change the SQLite schema or event JSON schema.
- Add a new service, command, config key, flag, or dependency.

## Assumptions

One daemon owns a Manager, Hub, and store.

API shutdown normally drains in-flight REST handlers before Manager shutdown.
The Manager still tracks in-flight `Start` calls so a timed-out API shutdown
cannot make PTY startup race with Manager closure.

PTY callbacks must remain bounded. They may write to the local store and Hub,
but they must not wait for user input or external services.

This plan assumes `Process.Kill` eventually lets `exec.Cmd.Wait` return. If
that assumption fails because of an operating-system defect, `Manager.Close`
can block. Returning early would be less safe because callbacks could still
access the store.

## Chosen approach

Use three lifecycle barriers without adding a new service or package:

1. `pty.Config` carries immutable callbacks into `pty.Start`.
2. `Manager.Start` holds those callbacks behind a per-start ready channel
   until the PTY is attached and the Agent reaches `working`.
3. `Manager.Close` marks the Manager closed, waits for in-flight starts, then
   closes every attached PTY before the daemon closes the Hub and store.

```text
Manager.Start
    |
    +--> persist created and pending -> starting
    +--> create callback ready channel
    +--> pty.Start(Config{OnOutput, OnExit})
    |       +--> copy callbacks into Session
    |       +--> launch readLoop and waitLoop
    |       +--> callbacks wait for ready channel
    +--> attach Session
    +--> persist starting -> working
    +--> close ready channel
            |
            +--> output and exit callbacks may run

daemon shutdown
    |
    +--> API Shutdown
    +--> Manager.Close
    |       +--> reject new starts
    |       +--> wait for in-flight starts
    |       +--> close every PTY
    |               +--> kill process
    |               +--> wait for output and exit callbacks
    +--> Hub.Close
    +--> Store.Close
```

The minimal alternative is to move the two callback assignments into
`pty.Config` and leave shutdown unchanged. It removes the callback data race
but still allows exit-before-working state corruption and callbacks after
store closure. The chosen approach covers the complete lifecycle with a small
amount of synchronization in existing packages.

A channel-owned lifecycle runner was also considered. It would serialize every
session operation through one goroutine, but it would replace the current
Manager API and broaden this fix into an orchestration rewrite. The existing
mutexes, one start wait group, and per-session channels are sufficient here.

## Required behavior

### Callback registration

Move callback registration into `pty.Config`:

```go
type Config struct {
	Command  string
	Args     []string
	Env      []string
	Dir      string
	OnOutput func(line string)
	OnExit   func(info ExitInfo)
}
```

`pty.Start` copies these functions into private `Session` fields before it
launches `readLoop` or `waitLoop`.

Remove the exported mutable callback fields from `Session`. No code may assign
or replace callbacks after `pty.Start` returns.

No callback runs when PTY creation or `exec.Cmd.Start` fails.

### PTY callback and completion semantics

`OnOutput` is optional. For each nonempty line or final partial line read from
the PTY, `readLoop` calls it once. A read that returns both data and an error
must not emit the data twice.

`OnExit` is optional and runs exactly once after `exec.Cmd.Wait` returns. It
receives the existing `ExitInfo`.

The process-exit callback does not need to wait for the output reader. This
keeps process state responsive when a descendant retains the PTY file
descriptor. Trailing output may therefore follow the stopped state event.

The Session owns a private completion channel. Completion means all of these
conditions hold:

- `exec.Cmd.Wait` returned.
- `OnExit` returned.
- `readLoop` returned.
- Every in-progress `OnOutput` call returned.

`WaitCh` remains available and receives one `ExitInfo`. Receiving from
`WaitCh` reports process exit; only `Session.Close` guarantees complete
callback drainage.

### PTY close semantics

`Session.Close` is synchronous and idempotent.

The first call:

1. Marks the Session closed under its local mutex.
2. Closes the PTY master to interrupt reads.
3. Kills the process if it is still running.
4. Waits for the private completion channel.
5. Returns any non-benign close or kill errors with PTY context.

A concurrent or later call waits for the same completion channel and returns
the first close result. It does not close the file or kill the process again.

After close begins:

- `Write` returns `pty.ErrClosed`.
- `Resize` returns `pty.ErrClosed`.
- `PID` remains a read-only record of the launched process.

`os.ErrClosed` and an already-finished process are benign close conditions.

### Session startup barrier

`Manager.Start` creates a ready channel before calling `pty.Start`.

Both configured callbacks wait for this ready channel before calling
`Manager.onOutput` or `Manager.onExit`. `Manager.Start` closes the channel only
after:

1. The PTY Session is stored in `Manager.sessions`.
2. The Agent successfully transitions from `starting` to `working`.
3. The state-change callback finishes its synchronous persist-and-publish
   attempt.

This gives every successful process the following state prefix:

```text
pending -> starting -> working
```

Output and exit facts may follow only after that prefix.

If the working transition fails after PTY startup, `Manager.Start` must release
the ready channel before closing the Session. Otherwise `Session.Close` would
wait on callbacks that cannot pass the startup barrier.

The existing failed-PTY-start path remains unchanged because no callback
goroutine exists when `pty.Start` returns an error.

### Manager lifecycle barrier

Add:

```go
var ErrManagerClosed = errors.New("session: manager closed")

func (m *Manager) Close() error
```

`Manager` tracks whether closure has started and how many `Start` calls are in
flight.

`Start` must register itself as in flight before request validation or event
creation. If closure has started, `Start` returns an error that wraps
`ErrManagerClosed`. It writes no event and launches no process.

`Manager.Close` performs these steps once:

1. Mark the Manager closed so no new start can register.
2. Wait for every already-registered start to return.
3. Copy attached sessions while holding the Manager lock.
4. Release the lock.
5. Close sessions in Agent ID order.
6. Continue after an individual close error.
7. Remove all closed sessions from `Manager.sessions`.
8. Return all close errors with session context by using `errors.Join`.

Sorting gives tests and logs a stable close order. Sequential close avoids
creating one shutdown goroutine per agent.

A second `Manager.Close` call waits for the first call and returns the same
result.

Manager shutdown relies on each PTY's `OnExit` path to transition a live Agent
to `stopped`. It does not add a second direct state transition.

After Manager closure:

- `Start` returns `ErrManagerClosed`.
- `Status`, `List`, and `Replay` remain readable while the store is open.
- `Stop` remains idempotent for stopped Agents.
- `Write` finds no attached PTY and returns `ErrNotAttached`.
- Status omits `pid` because `Manager.sessions` is empty.

### Hub close semantics

Add:

```go
func (h *Hub) Close()
```

`Hub.Close` is idempotent. It marks the Hub closed, removes all subscriptions,
and closes every subscription channel while holding the Hub write lock.

The existing publish read lock prevents `Close` from closing a channel while a
publisher sends to it.

After Hub closure:

- Existing subscribers observe a closed channel.
- `Unsubscribe` is a no-op for a subscription already removed by `Close`.
- `Subscribe` returns a Subscription whose channel is already closed.
- `Publish` does not deliver an event and does not panic.
- Sequence allocation behavior stays unchanged.

The API WebSocket loop already ranges over the subscription channel. Closing
the Hub therefore ends the writer loop and runs the handler's deferred
WebSocket close. No API route or WebSocket message change is required.

### Daemon shutdown order

After startup reaches the serving state, signal cancellation and unexpected
`Serve` failure use the same cleanup path:

```text
1. Server.Shutdown with the existing five-second deadline
2. Manager.Close
3. Hub.Close
4. Store.Close
```

The daemon attempts every cleanup step even when an earlier step returns an
error. It returns joined, wrapped errors after cleanup.

`Store.Close` remains deferred through a named return so startup failures also
close SQLite. The deferred close runs after Manager and Hub cleanup on the
normal serving path, and its error joins the return value.

The order has these effects:

- API shutdown prevents new REST starts.
- Existing WebSocket subscribers can receive shutdown state events while
  Manager closes sessions.
- Manager closure waits until no PTY callback can use the Hub or store.
- Hub closure ends remaining WebSocket subscriptions.
- Store closure happens after every event producer has stopped.

Update `internal/daemon/AGENTS.md` to replace its current unsafe order with
this dependency order. Also update the PTY and session package guides so their
callback and close descriptions match the implementation.

### Error handling

Keep cleanup errors instead of returning the first one.

Required error context:

- PTY close errors identify the failed file or process operation.
- Manager close errors identify the Agent ID.
- API shutdown errors use `daemon: shutdown api`.
- Manager errors use `daemon: shutdown sessions`.
- Store errors use `daemon: close store`.

Hub close has no expected error return.

The existing `persistAndPublish` failure behavior remains out of scope.
Manager closure waits for the attempt to finish, but it cannot make that
attempt transactional.

## Ownership

Package responsibilities remain unchanged:

- `internal/pty` owns callback execution, process wait, PTY close, and callback
  completion.
- `internal/session` owns startup ordering, Agent state transitions, attached
  sessions, and closing all sessions.
- `internal/event` owns subscription closure.
- `internal/api` keeps thin REST and WebSocket handlers and requires no code
  change.
- `internal/daemon` owns cross-component shutdown order and error aggregation.
- `internal/store` remains unaware of PTY and daemon lifecycles.

No new package dependency is introduced.

## API compatibility

REST routes, JSON payloads, WebSocket event fields, CLI commands, and database
rows do not change.

The Go-only `internal/pty` construction API changes:

- `OnOutput` and `OnExit` move from mutable `Session` fields to `Config`.
- `Session.Close` changes from returning `ErrClosed` on repeat calls to
  returning the original close result.

The Go-only `internal/session` API adds `Manager.Close` and
`ErrManagerClosed`.

The Go-only `internal/event` API adds `Hub.Close`.

All affected packages are under `internal`, so these changes do not create an
external compatibility commitment.

## Expected files

Implementation is expected to touch 12 files:

- `internal/pty/pty.go`
- `internal/pty/pty_test.go`
- `internal/pty/AGENTS.md`
- `internal/session/session.go`
- `internal/session/session_test.go`
- `internal/session/AGENTS.md`
- `internal/event/event.go`
- `internal/event/event_test.go`
- `internal/daemon/daemon.go`
- `internal/daemon/daemon_test.go`
- `internal/daemon/AGENTS.md`
- `docs/technical-notes.md`

This exceeds eight files because the change crosses PTY, session, event, and
daemon ownership boundaries and updates tests and package rules at those
boundaries. It adds one test file and no production package.

## Test plan

### PTY tests

- A short command that writes one newline-terminated line delivers it once.
- A short command that writes a final partial line delivers it once.
- A command that exits immediately invokes `OnExit` once.
- `Close` interrupts a blocked PTY read and waits for both callbacks.
- A deliberately blocked output callback keeps `Close` blocked until the test
  releases the callback.
- Concurrent and repeated close calls return the same result.
- `Write` and `Resize` return `ErrClosed` after close begins.
- A missing executable returns an error and invokes no callback.
- Focused tests pass under the race detector.

### Session tests

- An immediate-output process always records `starting -> working` before any
  output or stopped event.
- Immediate process exit cannot leave the Agent in `starting` or `working`.
- Manager closure stops a long-running process and records `stopped`.
- Manager closure removes the live PID from Status.
- Repeated Manager closure is safe.
- Start after Manager closure returns `ErrManagerClosed`, creates no event, and
  starts no process.
- A Start already in flight completes before Manager closure snapshots and
  closes sessions.
- Manager closure remains race-free with concurrent Status and Stop reads.

### Hub tests

- Hub closure closes every existing subscription channel.
- Repeated Hub closure is safe.
- Unsubscribe after Hub closure is safe.
- Subscribe after Hub closure returns a closed channel.
- Publish racing with Close never sends on a closed channel.

### Daemon tests

Use a temporary database and a local API port:

- Start a long-running `/bin/cat` generic session through REST.
- Cancel the daemon context.
- Confirm `Daemon.Run` returns after cleanup.
- Confirm the child PID no longer exists.
- Reopen the database and confirm the session contains a transition to
  `stopped`.
- Restart against the same database and confirm bootstrap reports no
  interrupted session because the prior shutdown persisted `stopped`.
- Cover the unexpected Serve-error path with the same cleanup helper where
  practical.

### Full verification

Run:

```bash
gofmt -w internal/pty internal/session internal/event internal/daemon
go test ./internal/pty ./internal/session -race -count=20
go test ./internal/event ./internal/daemon -race -count=10
go test ./... -race -count=1
go vet ./...
make build
git diff --check
```

The repeated focused runs increase the chance of exposing short-process
ordering bugs. They do not replace the full race run.

### Manual shutdown verification

Use an isolated data directory:

1. Start `droved`.
2. Create a generic session that runs `/bin/cat`.
3. Record the session ID, PID, and largest event sequence.
4. Send SIGTERM to `droved`.
5. Confirm the daemon exits within the test timeout.
6. Confirm the agent PID no longer exists.
7. Restart `droved` with the same database.
8. Confirm the session is `stopped` and has no PID.
9. Confirm Replay includes the shutdown state event.
10. Confirm restart adds no interruption error or reconciliation transition.

## Commit boundaries

Implementation must be delivered in two independently useful commits. Push
each commit after its verification passes.

### Commit 1: race-free PTY startup

- Move callbacks into `pty.Config`.
- Make callback ownership immutable.
- Add PTY callback completion and idempotent close.
- Add the Manager startup barrier.
- Add PTY and short-process session tests.
- Update PTY and session package rules.

This commit removes the callback race and preserves the complete state prefix
for short processes. Daemon-wide shutdown remains unchanged.

Commit:

```text
fix: make PTY callbacks race-free
```

### Commit 2: dependency-ordered shutdown

- Add idempotent Hub closure.
- Add the Manager lifecycle barrier and `Manager.Close`.
- Use one daemon cleanup path for cancellation and Serve failure.
- Close API, sessions, Hub, and store in dependency order.
- Add Hub, Manager, daemon, and real shutdown coverage.
- Correct the daemon package rule.
- Update the technical note.

After this commit, daemon shutdown stops live agents and drains callbacks
before SQLite closes.

Commit:

```text
fix: close daemon resources in dependency order
```

## Acceptance criteria

- No code mutates PTY callbacks after `pty.Start` returns.
- The race detector reports no callback read/write race.
- Every successful short process has `pending -> starting -> working` before
  callback-driven events.
- Each PTY output fragment is delivered at most once.
- `Session.Close` waits for all PTY callbacks and is idempotent.
- `Manager.Close` rejects new starts, drains in-flight starts, stops every
  attached process, and is idempotent.
- `Hub.Close` closes subscriptions and is safe with concurrent Publish and
  Unsubscribe.
- Daemon cancellation and Serve failure both run dependency-ordered cleanup.
- The store remains open until every Manager callback completes.
- A normal shutdown persists `stopped` for each live session.
- No live session PID remains after daemon shutdown.
- A restart after normal shutdown performs no interruption reconciliation for
  those sessions.
- REST, WebSocket, CLI, event, and database schemas do not change.
- Package guides describe the implemented shutdown order.
- Focused repeated race tests, the full race suite, vet, build, and the manual
  shutdown scenario pass.
- Each implementation commit is pushed after its own verification.

## Risks and controls

### Shutdown can wait on a callback

`Session.Close` waits until callbacks return. A callback that blocks forever
can block daemon shutdown.

Control: callbacks remain local operations with no user interaction or
external service call. Tests prove that Close waits rather than closing the
store underneath a callback. A timeout that abandons callbacks is not added
because it would restore the original store race.

### Immediate SIGKILL remains abrupt

The current PTY close path kills the process immediately.

Control: preserve existing behavior in this fix. Signal escalation and
process-group cleanup need separate process-lifecycle requirements and tests.

### Trailing output can follow stopped

The process wait and PTY reader are independent. `OnExit` can run before the
reader drains buffered output.

Control: Session completion waits for both callbacks before resource teardown,
so no output reaches a closed store. Event sequence remains the authoritative
order. A later terminal-stream design can define stronger ordering if the
product requires it.

### Shutdown errors can contain several causes

API, session, and store cleanup can fail in one shutdown.

Control: attempt every step and use `errors.Join` with component context.
Tests assert meaningful substrings instead of depending on one flat string.

## Rollback

The change has no data migration and adds no event type.

Reverting both commits restores the old in-memory lifecycle. Existing SQLite
files remain readable.

Reverting only Commit 2 keeps race-free PTY startup while restoring the old
daemon shutdown behavior. Reverting only Commit 1 is not supported while
Commit 2 remains because Manager shutdown depends on synchronous PTY callback
drainage.

## Approval gate

Implementation starts only after the owner approves this spec. Approval also
authorizes correcting the shutdown order in `internal/daemon/AGENTS.md`.
