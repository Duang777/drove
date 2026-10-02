# Implementation tasks

Implementation starts only after the owner approves `spec.md`.

## Commit 1: race-free PTY startup

### T1: Move callbacks into PTY configuration

Files:

- `internal/pty/pty.go`
- `internal/pty/pty_test.go`

Work:

- Add `OnOutput` and `OnExit` to `pty.Config`.
- Copy callbacks into private Session fields before starting goroutines.
- Remove mutable callback fields from Session.
- Keep start failures callback-free.
- Test immediate output and exit under the race detector.

Verification:

```bash
go test ./internal/pty -race -count=20
```

### T2: Define synchronous idempotent PTY close

Files:

- `internal/pty/pty.go`
- `internal/pty/pty_test.go`

Work:

- Track reader completion and total Session completion.
- Deliver each nonempty output fragment once.
- Make `OnExit` run exactly once.
- Make Close interrupt reads, kill the process, and wait for callbacks.
- Make concurrent and repeated Close calls return the same result.
- Preserve `ErrClosed` for Write and Resize after close begins.
- Wrap non-benign file and process errors.

Verification:

```bash
go test ./internal/pty -race -count=20
```

### T3: Gate callbacks until session startup commits

Files:

- `internal/session/session.go`
- `internal/session/session_test.go`

Work:

- Create a ready channel before `pty.Start`.
- Pass gated callbacks through `pty.Config`.
- Attach the PTY and persist `starting -> working` before opening the gate.
- Release the gate before cleanup on every post-start error path.
- Prove that immediate output and exit cannot precede the working event.
- Prove that a short process reaches `stopped`.

Verification:

```bash
go test ./internal/pty ./internal/session -race -count=20
go test ./... -race -count=1
git diff --check
```

### T4: Update PTY and session package rules

Files:

- `internal/pty/AGENTS.md`
- `internal/session/AGENTS.md`

Work:

- Document immutable callback registration through `pty.Config`.
- Document synchronous PTY completion.
- Document the Manager startup barrier.
- Keep the existing package ownership boundaries.

Commit and push:

```text
fix: make PTY callbacks race-free
```

## Commit 2: dependency-ordered shutdown

### T5: Add Hub closure

Files:

- `internal/event/event.go`
- `internal/event/event_test.go`

Work:

- Track whether the Hub is closed.
- Add idempotent `Hub.Close`.
- Close and remove all subscriptions under the write lock.
- Return an already-closed subscription after Hub closure.
- Keep Unsubscribe and Publish safe after closure.
- Test Publish, Unsubscribe, and Close races.

Verification:

```bash
go test ./internal/event -race -count=20
```

### T6: Add the Manager lifecycle barrier

Files:

- `internal/session/session.go`
- `internal/session/session_test.go`

Work:

- Add `ErrManagerClosed`.
- Register each Start as in flight before validation or persistence.
- Mark the Manager closed before waiting for in-flight starts.
- Close attached sessions in stable Agent ID order.
- Continue after individual close errors and join them with Agent context.
- Clear attached sessions after callback drainage.
- Make repeated Manager closure return the same result.
- Test close, start-after-close, and concurrent lifecycle behavior.

Verification:

```bash
go test ./internal/pty ./internal/session ./internal/event -race -count=20
```

### T7: Unify daemon cleanup

Files:

- `internal/daemon/daemon.go`
- `internal/daemon/daemon_test.go`

Work:

- Route cancellation and Serve failure through one cleanup path.
- Shut down the API with the existing five-second deadline.
- Close the Manager before the Hub.
- Close the store last through a named-return defer.
- Attempt every cleanup step and join wrapped errors.
- Test a real long-running PTY through the local REST API.
- Verify that daemon return implies process exit and persisted `stopped`.
- Restart with the same database and verify that no interruption reconciliation
  is needed.

Verification:

```bash
go test ./internal/event ./internal/session ./internal/daemon -race -count=10
```

### T8: Correct lifecycle documentation

Files:

- `internal/daemon/AGENTS.md`
- `docs/technical-notes.md`

Work:

- Replace the unsafe documented shutdown order.
- Record the callback and shutdown fixes with verification evidence.
- Keep input injection, process groups, and persistence failure propagation as
  separate future work.

Verification:

```bash
rg -n "关闭顺序|回调|孤儿|store|Hub" internal/daemon/AGENTS.md docs/technical-notes.md
```

### T9: Run full and manual verification

Work:

- Run focused repeated race tests.
- Run the full race suite, vet, and build.
- Run the isolated live-process shutdown and restart scenario.
- Confirm the working tree contains only the approved files.

Verification:

```bash
gofmt -w internal/pty internal/session internal/event internal/daemon
go test ./internal/pty ./internal/session -race -count=20
go test ./internal/event ./internal/daemon -race -count=10
go test ./... -race -count=1
go vet ./...
make build
git diff --check
git status --short
```

Commit and push:

```text
fix: close daemon resources in dependency order
```

## Approval writeback

After owner approval and before T1:

- Change `spec.md` status from `Draft` to `Approved`.
- Check the approval item in `checklist.md`.
- Record approval in LoopX.
- Commit and push the approval as its own documentation commit.

Suggested commit:

```text
docs: approve PTY lifecycle shutdown spec
```
