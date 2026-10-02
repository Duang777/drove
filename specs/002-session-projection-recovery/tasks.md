# Implementation tasks

Implementation starts only after the owner approves `spec.md`.

## Commit 1: recovery primitives

### T1: Add ordered event scanning

Files:

- `internal/store/store.go`
- `internal/store/store_test.go`

Work:

- Add context-aware `ScanEvents`.
- Stream all rows in ascending global sequence order.
- Return the observed maximum sequence.
- Wrap callback and row parsing failures with sequence context.

Verification:

```bash
go test ./internal/store -race
```

### T2: Add atomic event batches

Files:

- `internal/store/store.go`
- `internal/store/store_test.go`

Work:

- Add `AppendEvents` with an expected maximum check.
- Validate consecutive batch sequences.
- Insert the complete batch in one transaction.
- Prove that stale boundaries and duplicate rows leave no partial writes.

Verification:

```bash
go test ./internal/store -race
```

### T3: Add validated Agent restoration

Files:

- `internal/agent/agent.go`
- `internal/agent/agent_test.go`

Work:

- Add `RestoreSnapshot`.
- Add `Restore` without firing a state hook.
- Validate identity, metadata, state, and timestamps.
- Prove that normal transitions still apply after restoration.

Verification:

```bash
go test ./internal/agent -race
go test ./internal/store ./internal/agent -race
git diff --check
```

Commit and push:

```text
feat: add session recovery primitives
```

## Commit 2: self-describing sessions

### T4: Define the creation lifecycle payload

Files:

- `internal/event/event.go`
- `internal/event/event_test.go`
- `internal/session/session.go`
- `internal/session/session_test.go`

Work:

- Let `NewSessionLifecycle` accept an encoded payload.
- Define the private version 1 `created` payload in session.
- Validate and resolve Start requests before creating persistent history.
- Persist the creation event before the first state transition.
- Abort before PTY startup if the creation event cannot be stored.

Verification:

```bash
go test ./internal/event ./internal/session -race
```

### T5: Preserve failed startup history

Files:

- `internal/session/session.go`
- `internal/session/session_test.go`

Work:

- Record a nonempty error event when PTY startup fails after creation.
- Transition the Agent from `starting` to `stopped`.
- Keep the stopped Agent queryable while returning the startup error.
- Confirm invalid preflight requests still write no event.

Verification:

```bash
go test ./internal/session -race
go test ./internal/event ./internal/session -race
git diff --check
```

Commit and push:

```text
feat: persist recoverable session metadata
```

## Commit 3: boot-time projection recovery

### T6: Implement the pure projector

Files:

- `internal/session/projection.go`
- `internal/session/projection_test.go`

Work:

- Fold lifecycle, state, error, and output rows by session.
- Apply v1 metadata fallback.
- Apply state-chain anchoring and partial-history rules.
- Generate stable reconciliation rows and final restore snapshots.
- Return contextual errors for projection-critical corruption.

Verification:

```bash
go test ./internal/session -race
```

### T7: Implement session bootstrap

Files:

- `internal/session/session.go`
- `internal/session/session_test.go`

Work:

- Add `BootstrapResult` and `RecoveryReport`.
- Scan and project before constructing a public Manager.
- Validate restored Agents before writing reconciliation rows.
- Commit reconciliation rows atomically.
- Initialize the Hub from the committed maximum.
- Populate `Manager.agents` without creating historical PTYs.

Verification:

```bash
go test ./internal/session ./internal/store ./internal/agent ./internal/event -race
```

### T8: Define restored Manager operations

Files:

- `internal/session/session.go`
- `internal/session/session_test.go`

Work:

- Make Stop succeed for restored stopped Agents.
- Make Write return a not-attached error when no PTY exists.
- Remove nested read locking from List.
- Sort List by creation time and then Agent ID.

Verification:

```bash
go test ./internal/session -race
```

### T9: Wire daemon startup

Files:

- `internal/daemon/daemon.go`
- `internal/daemon/daemon_test.go`

Work:

- Replace separate Hub and Manager construction with session Bootstrap.
- Log the compact recovery report.
- Keep API server and listener construction after successful bootstrap.
- Cover empty, successful recovery, and recovery failure paths.

Verification:

```bash
go test ./internal/daemon ./internal/session -race
```

### T10: Run full and real restart verification

Files:

- `docs/technical-notes.md`

Work:

- Run the isolated three-start scenario from `spec.md`.
- Record recovered Status values and sequence boundaries.
- Mark session projection recovery complete.
- Preserve process reconnection as an explicit non-feature.

Verification:

```bash
gofmt -w internal/store internal/agent internal/event internal/session internal/daemon
go test ./... -race
go vet ./...
make build
git diff --check
```

Commit and push:

```text
feat: restore session projections after restart
```
