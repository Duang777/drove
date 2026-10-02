# Implementation tasks

Implementation starts only after the owner approves `spec.md`.

## Approval writeback

After owner approval and before T1:

- Change `spec.md` status from `Draft` to `Approved`.
- Check the approval item in `checklist.md`.
- Record approval in LoopX.
- Commit and push the approval as its own documentation commit.

Suggested commit:

```text
docs: approve runner modes spec
```

## Commit 1: typed runner mode and durable metadata

### T1: Add the Agent runner mode

Files:

- `internal/agent/agent.go`
- `internal/agent/agent_test.go`
- `internal/agent/AGENTS.md`

Work:

- Add `RunMode`, its two constants, and `ValidRunMode`.
- Store mode as immutable Agent metadata.
- Default new Agents to interactive.
- Add `WithRunMode` and `Agent.RunMode`.
- Add mode to `RestoreSnapshot`.
- Reject invalid restored mode values.
- Add focused domain and restoration tests.

Verification:

```bash
go test ./internal/agent -race
```

### T2: Make adapter commands mode-aware

Files:

- `internal/adapter/adapter.go`
- `internal/adapter/vendors.go`
- `internal/adapter/vendors_test.go`
- `internal/adapter/AGENTS.md`

Work:

- Change `Runner.Command` to accept a validated `agent.RunMode`.
- Map Claude interactive to `claude`.
- Map Claude oneshot to `claude --print`.
- Map Codex interactive to `codex`.
- Map Codex oneshot to `codex exec`.
- Keep generic command output empty in both modes.
- Table-test the complete vendor and mode matrix.

Verification:

```bash
go test ./internal/adapter -race
```

### T3: Validate, persist, and recover mode

Files:

- `internal/session/session.go`
- `internal/session/session_test.go`
- `internal/session/projection.go`
- `internal/session/projection_test.go`
- `internal/session/AGENTS.md`

Work:

- Add lowercase JSON tags and mode to `StartRequest`.
- Add `ErrInvalidMode` and normalize mode at the start boundary.
- Reject invalid mode before event creation.
- Pass the normalized mode to adapter and Agent.
- Add mode to Status.
- Add optional mode to version 1 creation metadata.
- Make new writers always persist a valid mode.
- Recover old creation metadata and state-only history as oneshot.
- Reject present empty or unknown persisted values with projection context.
- Keep custom command precedence and argv unchanged.
- Keep restart reconciliation unchanged.

Verification:

```bash
go test ./internal/agent ./internal/adapter ./internal/session -race
```

### T4: Expose mode through API, CLI, and Web types

Files:

- `internal/api/server.go`
- `internal/api/server_test.go`
- `cmd/drove/main.go`
- `cmd/drove/main_test.go`
- `cmd/drove/AGENTS.md`
- `web/src/api/types.ts`

Work:

- Map `ErrInvalidMode` to HTTP 400.
- Keep process-start failures at HTTP 500.
- Add `--oneshot` to `drove up`.
- Map the boolean flag to the two domain values.
- Include mode in the start result and `drove ps`.
- Add mode to the TypeScript request and status contracts.
- Test lowercase request JSON, invalid mode, CLI construction, and flag exposure.

Verification:

```bash
go test ./internal/api ./cmd/drove -race
npm --prefix web run typecheck
npm --prefix web run build
```

### T5: Verify and deliver Commit 1

Run:

```bash
gofmt -w internal/agent internal/adapter internal/session internal/api cmd/drove
go test ./internal/agent ./internal/adapter ./internal/session ./internal/api ./cmd/drove -race
go test ./... -race -count=1
go vet ./...
npm --prefix web run typecheck
npm --prefix web run build
make build
git diff --check
```

Commit and push:

```text
feat: add interactive and oneshot runner modes
```

## Commit 2: mode-aware exit semantics

### T6: Track process stop intent

Files:

- `internal/session/session.go`
- `internal/session/session_test.go`

Work:

- Replace each bare PTY map value with a private running-session entry.
- Track stop cause and exit claim under the Manager mutex.
- Capture the exact runtime entry in the exit callback.
- Record user stop before closing the PTY.
- Mark every daemon-shutdown stop cause before closing any PTY.
- Keep the existing stable close order and synchronous callback drainage.
- Keep Stop idempotent after done or stopped.

Verification:

```bash
go test ./internal/session -race -count=20
```

### T7: Apply the exit decision table

Files:

- `internal/agent/agent.go`
- `internal/agent/agent_test.go`
- `internal/session/session.go`
- `internal/session/session_test.go`

Work:

- Add `idle -> done`.
- Implement the private pure `decideExit` function.
- Map successful natural oneshot exit to done.
- Map natural interactive exit to stopped.
- Map failed natural exit to stopped and persist its process error.
- Suppress expected kill errors for requested stops.
- Ignore heuristic Done hints for interactive Agents.
- Persist the terminal state before detaching the matching PTY.
- Remove stale PIDs after natural exit.
- Test the full decision matrix and real process behavior.

Verification:

```bash
go test ./internal/agent ./internal/session -race -count=20
```

### T8: Update technical documentation

Files:

- `docs/technical-notes.md`

Work:

- Record the new request default and vendor mappings.
- Record event compatibility and the legacy oneshot fallback.
- Record the natural and requested exit matrix.
- Keep hooks, input, and detector work listed as later phases.

Verification:

```bash
rg -n "interactive|oneshot|RunMode|exit|退出" docs/technical-notes.md
```

### T9: Run full and manual verification

Work:

- Run the repeated focused race tests.
- Run the full race suite, vet, Go builds, and Web checks.
- Run the isolated interactive, oneshot, stop, and restart scenario.
- Confirm that the worktree contains only approved files.

Verification:

```bash
gofmt -w internal/agent internal/session
go test ./internal/agent ./internal/session -race -count=20
go test ./... -race -count=1
go vet ./...
npm --prefix web run typecheck
npm --prefix web run build
make build
git diff --check
git status --short
```

Commit and push:

```text
fix: apply runner mode exit semantics
```
