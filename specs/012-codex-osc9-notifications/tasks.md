# Implementation tasks

Complete each commit in order. Run the focused checks, commit, and push before
starting the next commit.

## Commit 1: committed OSC 9 scanner

Files:

- `internal/term/osc9.go`
- `internal/term/osc9_test.go`
- `internal/term/AGENTS.md`

Work:

- Add a bounded stateful scanner for direct OSC 9 and one Codex tmux wrapper.
- Retain committed output offset, sequence, and time for each frame.
- Cover BEL, ST, every split point, byte-at-a-time input, malformed data,
  overflow recovery, multiple frames, and reset.
- Keep vendor text matching out of `internal/term`.

Verification:

```bash
gofmt -w internal/term
go test -race ./internal/term -count=20
go vet ./internal/term
git diff --check
```

Commit:

```text
feat: scan committed osc9 notifications
```

## Commit 2: Codex planning and typed terminal evidence

Files:

- `internal/adapter/adapter.go`
- `internal/adapter/signal_injection.go`
- `internal/adapter/codex_signal_injection.go`
- `internal/adapter/codex_signal_injection_test.go`
- `internal/adapter/codex_notifications.go`
- `internal/adapter/codex_notifications_test.go`
- `internal/agent/agent.go`
- `internal/agent/agent_test.go`
- `internal/detect/detect.go`
- `internal/detect/detect_test.go`
- `internal/event/event.go`
- `internal/event/event_test.go`
- `internal/session/detector.go`
- `internal/session/projection.go`
- `internal/session/explain.go`
- related tests and `AGENTS.md` files

Work:

- Atomically inject legacy notify and approval-only OSC settings.
- Reject any caller-owned managed key in base or request arguments.
- Add the per-session Codex output decoder.
- Add typed terminal attribution to Signal, Evidence, and v4 wire payloads.
- Land v4 recovery and explain readers before enabling the writer.
- Add terminal notify validation and fallback permission-candidate behavior.
- Cancel the permission candidate on approval-cleared screen edges.

Verification:

```bash
gofmt -w internal/adapter internal/agent internal/detect internal/event internal/session
go test -race ./internal/adapter ./internal/agent ./internal/detect ./internal/event ./internal/session -count=20
go vet ./internal/adapter ./internal/agent ./internal/detect ./internal/event ./internal/session
git diff --check
```

Commit:

```text
feat: normalize codex osc9 approvals
```

## Commit 3: session ordering and lifecycle

Files:

- `internal/session/signal_injection.go`
- `internal/session/session.go`
- `internal/session/terminal.go`
- `internal/session/output.go`
- related tests and fixtures

Work:

- Enable the decoder only for a complete injected Codex plan.
- Let the terminal actor return normalized observations from committed bytes.
- Deliver output activity before terminal observations from the same batch.
- Fence notifications after process exit and reset incomplete input at end.
- Prove Store failure prevents scanner input.
- Prove OSC body absence across persisted and public read paths.

Verification:

```bash
gofmt -w internal/session
go test -race ./internal/session -count=20
go vet ./internal/session
git diff --check
```

Commit:

```text
feat: observe committed codex approvals
```

## Commit 4: documentation and acceptance

Files:

- `README.md`
- `README.en.md`
- `docs/hooks.md`
- `docs/next-phase-research.md`
- fixture provenance documents

Work:

- Remove statements that OSC 9 is unimplemented.
- Point OSC 9 tracking at Issue #40.
- Document approval-only behavior and conflict ownership.
- Record direct and tmux fixture provenance.

Verification:

```bash
go test -race ./...
go vet ./...
make build
git diff --check
```

Commit:

```text
docs: document codex osc9 approvals
```

## Delivery

- Push every commit.
- Open one PR for Issues #38 and #40 after all checks pass.
- Wait for both CI lanes.
- Record commit and CI evidence on both issues.
- Close the issues only through the merged PR.
