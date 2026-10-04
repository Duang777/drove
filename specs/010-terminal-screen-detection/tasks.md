# Implementation tasks

Implementation starts only after approval. Complete commits in order. For each
commit:

1. implement only that commit's tasks;
2. run its focused checks;
3. run the common full gate;
4. inspect the diff;
5. commit with the listed message;
6. push immediately;
7. verify the remote contains the commit before starting the next one.

Do not accumulate two completed commits locally.

## Common full gate

Run this gate for every product-code commit:

```bash
gofmt -w cmd internal
go test ./... -race -count=1
go vet ./...
make build
npm --prefix web run typecheck
npm --prefix web run build
git diff --check
```

The focused checks below run high-risk packages 20 times under the race
detector. A focused failure stops the commit.

## Spec commit

After approval, update the status and approval date in `spec.md`, check the
approved boxes in `checklist.md`, then run:

```bash
git diff --check
git status --short
```

Commit and push:

```text
docs: specify terminal screen detection
```

## Commit 1: toolchain and terminal controller

This commit raises the build floor and adds an isolated terminal controller.
It does not connect x/vt to a running session.

### T1: Raise the Go floor and pin x/vt

Files:

- `go.mod`
- `go.sum`
- `.github/workflows/ci.yml`
- `README.md`
- `AGENTS.md`

Work:

- Change the module directive to `go 1.24.2`.
- Add x/vt at
  `v0.0.0-20261004011457-ad85c59fdf4e`.
- Run `go mod tidy`.
- Replace the Go 1.23 CI lane with exact Go 1.24.2.
- Keep a second explicit current-stable Go lane.
- Update repository prerequisites and workspace instructions.
- Do not add a `toolchain` directive that silently selects a newer compiler.
- Record the dependency pin and minimum-version rationale.

### T2: Add normalized terminal types

Files:

- `internal/term/controller.go`
- `internal/term/controller_test.go`
- `internal/term/AGENTS.md`

Work:

- Add validated `Size`, copied `CommittedChunk`, normalized immutable
  `Snapshot`, and bounded snapshot options.
- Keep x/vt, x/ansi, and ultraviolet types private.
- Normalize visible cells without title, scrollback, style, link, clipboard,
  or reply data.
- Bound explain extraction by bottom rows, terminal cells per row, and encoded
  bytes.
- Preserve `Stripper`, `Strip`, and `StripString`.

### T3: Add the controller and reply pump

Files:

- `internal/term/controller.go`
- `internal/term/controller_test.go`
- `internal/term/testdata/*`

Work:

- Construct the pinned x/vt terminal at a validated size.
- Create the x/vt reply pipe and start one reply pump.
- Wait for a pump-ready barrier before accepting the first write.
- Route replies to an injected complete-frame sink.
- Add the supplemental Kitty `CSI ? u -> CSI ? 0 u` handler.
- Keep draining after sink errors until controller close.
- Make close idempotent and join the pump.
- Add exact deadline-bounded tests for DSR, OSC 10/11, DA1, and Kitty query
  replies.
- Prove reply bytes never enter snapshots.
- Add resize tests without exposing x/vt types.

Focused verification:

```bash
go test ./internal/term -race -count=20
go test ./internal/term -run 'TestController.*(DSR|OSC|Device|Kitty|Drain|Close|Resize)' -count=100
```

Then run the common full gate.

Commit and push:

```text
feat: add terminal screen controller
```

## Commit 2: initial PTY size

This commit makes terminal dimensions true at process start. It does not enable
screen classification.

### T4: Start the PTY with its declared size

Files:

- `internal/pty/pty.go`
- `internal/pty/pty_test.go`
- `internal/pty/AGENTS.md`

Work:

- Add a validated PTY size to `pty.Config`.
- Replace `pty.Start` followed by `Setsize` with `pty.StartWithSize`.
- Keep the existing read, output-end, exit, write-lock, and Close contracts.
- Reject zero or overflowing dimensions before process start.
- Add a child-process test that reads terminal size as its first operation.
- Prove the observed initial size is 40 rows by 120 columns.

### T5: Declare one initial session size

Files:

- `internal/session/session.go`
- `internal/session/session_test.go`
- `internal/session/AGENTS.md`

Work:

- Pass the same 40x120 size to PTY startup.
- Keep the future emulator size conversion explicit and checked.
- Preserve startup failure cleanup and readiness-gate behavior.
- Do not expose a resize API.

Focused verification:

```bash
go test ./internal/pty ./internal/session -race -count=20
go test ./internal/pty -run TestSessionChildObservesInitialSize -count=100
```

Then run the common full gate.

Commit and push:

```text
fix: size PTY before agent startup
```

## Commit 3: reader-first screen evidence and keyed candidates

This commit must accept every new durable payload before a production screen
writer exists. It becomes the rollback floor after Commit 5.

### T6: Add typed screen evidence

Files:

- `internal/agent/agent.go`
- `internal/agent/agent_test.go`
- `internal/agent/AGENTS.md`
- `internal/detect/detect.go`
- `internal/detect/detect_test.go`
- `internal/detect/AGENTS.md`

Work:

- Add `EvidenceScreen` and a bounded copied screen attribution type.
- Add `SourceScreen`.
- Add validated screen edge values `present` and `cleared`.
- Validate stable rule, region, output offset, final output sequence, and
  static evidence.
- Add a constructor that prevents adapter or session code from claiming an
  invalid source/kind/attribution combination.
- Preserve process, hook, notify, heuristic, and timer compatibility.

### T7: Replace the single candidate slot

Files:

- `internal/detect/detect.go`
- `internal/detect/detect_test.go`
- `internal/session/detector.go`
- `internal/session/detector_test.go`

Work:

- Key candidate state by bounded purpose and rule identity.
- Give each key an independent generation and deadline.
- Return only the earliest deadline to the observation actor.
- Include purpose, rule identity, and generation in a timer reference.
- Re-arm the one physical timer after each committed Decision.
- Persist stale timer firings without consuming another key.
- Keep delivery-ID dedupe and hook activation behavior unchanged.
- Add the exact screen authority table from `spec.md`.
- Confirm active-hook approval clearance after 500 ms.
- Confirm active-hook Claude interrupt after one second without branching on a
  vendor inside Detector.
- Suppress every other active-hook screen edge durably.
- Keep process facts supreme and screen signals unable to produce Done.
- Keep legacy line-heuristic behavior active until Commit 5.

### T8: Add event payload version 3 readers

Files:

- `internal/event/event.go`
- `internal/event/event_test.go`
- `internal/event/AGENTS.md`
- `internal/session/committer.go`
- `internal/session/committer_test.go`
- `internal/session/detector.go`
- `internal/session/projection.go`
- `internal/session/projection_test.go`
- `internal/session/AGENTS.md`

Work:

- Add `SignalPayloadV3` with optional typed screen attribution.
- Add `StateEvidencePayloadV3` with the same attribution shape.
- Encode version 3 only for screen source/evidence.
- Continue writing existing versions for existing sources.
- Decode versions 1, 2, and 3 during recovery.
- Skip and count unknown signal versions.
- Apply state columns while counting unknown state evidence versions.
- Reject malformed known version 3 payloads.
- Seed reader-first rows for candidate, suppressed, transitioned, stale, and
  terminal outcomes.
- Prove that seeded screen events do not require output attachments to recover
  Agent state.

Focused verification:

```bash
go test ./internal/agent ./internal/event ./internal/detect ./internal/session -race -count=20
go test ./internal/detect -run 'Test.*(Screen|Candidate|Timer|Process)' -count=100
```

Then run the common full gate.

Commit and push:

```text
feat: read screen detection evidence
```

## Commit 4: adapter screen classifiers

This commit adds adapter-private rule engines and fixtures. Existing line
heuristics remain active until Commit 5.

### T9: Add the deep classifier boundary

Files:

- `internal/adapter/adapter.go`
- `internal/adapter/screen.go`
- `internal/adapter/screen_test.go`
- `internal/adapter/AGENTS.md`

Work:

- Add `Entry.NewScreenClassifier`.
- Add one stateful classifier instance per session.
- Keep declarative definitions, regexes, literals, compiled matchers, regions,
  and previous match state private.
- Return only copied normalized `ScreenHint` values.
- Emit an edge only when match presence changes.
- Implement `Rebaseline` without emitting an edge.
- Validate stable names, static evidence, confidence, and confirmation.
- Require each rule duration to match Detector's fixed authority policy.
- Return an empty classifier for generic.
- Do not let rules return Agent states or authority.

### T10: Add Claude and Codex rules

Files:

- `internal/adapter/claude_screen.go`
- `internal/adapter/claude_screen_test.go`
- `internal/adapter/codex_screen.go`
- `internal/adapter/codex_screen_test.go`
- `internal/adapter/testdata/claude/*.bin`
- `internal/adapter/testdata/codex/*.bin`

Work:

- Add the stable rules listed in `spec.md`.
- Map approval presence and clearance to normalized human-input kinds.
- Map visible idle prompts to normalized Idle evidence.
- Map only Claude interrupt output to `KindInterrupted`.
- Replay redacted real terminal byte fixtures through `term.Controller`.
- Feed fixtures at single-byte, boundary, and realistic chunk splits.
- Cover cursor movement, overwritten prompts, wide cells, and trailing partial
  frames.
- Add near-miss fixtures and ordinary output containing `Error`.
- Keep all fixture content free of user paths, tokens, prompts, and private
  source text.
- Record fixture vendor versions in a testdata README.

Focused verification:

```bash
go test ./internal/term ./internal/adapter -race -count=20
go test ./internal/adapter -run 'Test.*Screen' -count=100
```

Then run the common full gate.

Commit and push:

```text
feat: classify rendered agent screens
```

## Commit 5: terminal actor and screen writer activation

This commit connects committed output to x/vt, enables version 3 screen
events, changes startup reply ordering, and removes line heuristics together.

### T11: Add the per-session terminal actor

Files:

- `internal/session/terminal.go`
- `internal/session/terminal_test.go`
- `internal/session/session.go`
- `internal/session/session_test.go`
- `internal/session/AGENTS.md`

Work:

- Give each attached session one terminal actor and classifier.
- Bound the actor inbox.
- Construct the controller with a reply sink that calls
  `pty.Session.Write` directly.
- Keep user input and reply frames serialized by the existing PTY write lock.
- Implement `FeedCommitted`, `Snapshot`, `MarkProcessExited`, `EndOutput`,
  internal `Resize`, and idempotent `Close`.
- Own the first-dirty fixed 100 ms timer inside the actor.
- Capture immutable snapshots and emit only classifier edges.
- Deliver screen observations to the existing observation actor.
- Apply backpressure instead of dropping committed bytes or edges.
- Fence screen observation before Detector process termination.
- Keep the actor alive through output drain.
- Drop snapshot availability at detach.

### T12: Feed only committed output

Files:

- `internal/session/output.go`
- `internal/session/output_test.go`
- `internal/session/committer.go`
- `internal/session/committer_test.go`

Work:

- Retain the Committer receipt for each output batch.
- Build a copied `term.CommittedChunk` only after commit success.
- Carry exclusive output offset, final sequence, and commit time.
- Feed the exact token-redacted bytes represented by the committed batch.
- Keep one output-activity observation per committed PTY callback.
- Stop before terminal feed when persistence fails.
- Enter the existing fatal path when a committed stream cannot be fed.
- Add ordering assertions from Store append through screen signal commit.

### T13: Release callbacks before required-hook wait

Files:

- `internal/session/session.go`
- `internal/session/session_test.go`
- `internal/session/signal_test.go`

Work:

- Construct the actor after the sized PTY starts and before callbacks run.
- Commit ProcessStarted and Working before opening gates.
- Open `signalReady`, then `callbacksReady`.
- Wait for required-hook activation only after callbacks can process terminal
  queries.
- Preserve early-hook waiting, startup cleanup, and short-process behavior.
- Add a required-hook Codex query fixture that proves startup does not
  deadlock.

### T14: Remove line classification

Files:

- `internal/adapter/adapter.go`
- `internal/adapter/vendors.go`
- `internal/adapter/vendors_test.go`
- `internal/adapter/terminal_test.go`
- `internal/adapter/AGENTS.md`
- `internal/session/output.go`
- `internal/session/output_test.go`
- `internal/session/signal_test.go`
- `internal/term/AGENTS.md`

Work:

- Remove `Heuristic`, `OutputHint`, and `Entry.Classify`.
- Remove Claude and Codex line classifiers.
- Remove session derived-line buffering and classification.
- Remove Blocked recovery based on two non-Blocked lines.
- Keep output activity for text-free liveness and fallback silence.
- Keep `term.Stripper` solely for plain replay and compatible one-shot callers.
- Replace all `Error` false-positive tests with a no-screen-edge assertion.
- Update package ownership documents in the same commit.

Focused verification:

```bash
go test ./internal/pty ./internal/term ./internal/adapter ./internal/detect ./internal/session -race -count=20
go test ./internal/session -run 'Test.*(Terminal|Screen|Startup|Output|Exit|Required)' -count=100
```

Then run the common full gate.

Commit and push:

```text
feat: detect agent state from terminal screens
```

This commit activates the writer. Commit 3 is now the oldest safe database
reader.

## Commit 6: explain API and CLI

### T15: Add a bounded event-tail query

Files:

- `internal/store/store.go`
- `internal/store/store_test.go`
- `internal/store/AGENTS.md`

Work:

- Add an envelope-only recent-event query for one session and an allowlisted
  set of event types.
- Filter and limit in SQLite.
- Return chronological order after selecting the newest matching rows.
- Never join or hydrate output attachments.
- Validate positive limits and cap Manager calls at 200.
- Prove unrelated output volume cannot displace the requested signal/state
  tail.

### T16: Add `Manager.Explain`

Files:

- `internal/session/explain.go`
- `internal/session/explain_test.go`
- `internal/session/session.go`
- `internal/session/AGENTS.md`

Work:

- Add `ExplainOptions`, typed durable event summaries, screen view, and
  `Explanation`.
- Default to 50 and reject limits above 200.
- Decode signal/evidence versions 1, 2, and 3 into bounded typed summaries.
- Represent unknown versions without forwarding arbitrary payload text.
- Read a live snapshot only from the same currently attached terminal actor.
- Return durable events after detach with no screen.
- Return current Agent state and Detector hook status without creating another
  projection.
- Handle detach races without retaining a snapshot.

### T17: Add the HTTP and client contract

Files:

- `internal/api/server.go`
- `internal/api/server_test.go`
- `internal/api/AGENTS.md`
- `internal/client/client.go`
- `internal/client/client_test.go`
- `internal/client/AGENTS.md`

Work:

- Register `GET /api/v1/agents/{id}/explain`.
- Parse only the optional positive `limit`.
- Keep the existing loopback and control-token protection.
- Map unknown Agent, invalid limit, and internal errors consistently.
- Return the typed JSON response.
- Add one transport-only client method.
- Test attached, detached, empty-history, unknown-version, auth, and limit
  cases.

### T18: Add `drove explain`

Files:

- `cmd/drove/main.go`
- `cmd/drove/main_test.go`
- `cmd/drove/AGENTS.md`

Work:

- Add `drove explain <id>`.
- Add `--limit` and `--json`.
- Print durable decisions in chronological order.
- Show source, kind, outcome, rule, suppression reason, and transition.
- Print an attached snapshot under the exact label
  `ephemeral redacted current screen`.
- Do not print a detached placeholder that looks like retained screen state.
- Keep stdout machine-clean in JSON mode and send errors to stderr.

Focused verification:

```bash
go test ./internal/store ./internal/session ./internal/api ./internal/client ./cmd/drove -race -count=20
go test ./internal/session ./internal/api ./cmd/drove -run 'Test.*Explain' -count=100
```

Then run the common full gate.

Commit and push:

```text
feat: explain agent state decisions
```

## Commit 7: acceptance and performance proof

### T19: Add end-to-end fixture acceptance

Files:

- `internal/session/terminal_acceptance_test.go`
- `internal/session/testdata/terminal/*`
- relevant existing test files

Work:

- Replay the approved redacted Claude and Codex fixture streams through the
  real output processor, Store, terminal actor, classifier, Detector, and
  Committer.
- Assert approval visible reaches Blocked within one second in off/fallback.
- Assert active-hook approval clearance reaches Working within one second
  without a post-tool hook.
- Assert Claude interrupt reaches Idle within two seconds.
- Assert required terminal queries receive replies under a deadline.
- Assert the reply path creates no input audit, output event, explain history,
  or snapshot content.
- Treat any bytes explicitly echoed by the child as ordinary committed output.
- Assert trailing output after process exit persists without a later screen
  signal.
- Run each stream under varied input chunk boundaries.

### T20: Add the 32-session benchmark

Files:

- `internal/session/terminal_benchmark_test.go`
- `docs/technical-notes.md`

Work:

- Run 32 actors with 1 MiB/s of committed bytes per actor.
- Use representative control sequences and screen churn.
- Record Go version, OS, CPU, x/vt pin, duration, aggregate throughput,
  allocations, peak goroutines, and whether inbox backpressure engaged.
- Check byte accounting and final screen capture.
- Run with the race detector once for correctness.
- Run without the race detector for the recorded performance baseline.
- Do not introduce an arbitrary CI latency threshold.

### T21: Run race and leak verification

Run:

```bash
go test ./internal/term ./internal/pty ./internal/event ./internal/agent ./internal/adapter ./internal/detect ./internal/session ./internal/store ./internal/api ./internal/client ./cmd/drove -race -count=20
go test ./internal/session -run TestTerminalAcceptance -race -count=20
go test ./internal/session -run '^$' -bench BenchmarkTerminalActor32 -benchmem -count=5
go test ./... -race -count=1
go vet ./...
make build
npm --prefix web run typecheck
npm --prefix web run build
git diff --check
```

Inspect goroutine profiles before and after repeated startup, output-end,
process-exit, and Manager close. No terminal actor or reply pump may remain.

Commit and push:

```text
test: prove terminal screen detection
```

## Commit 8: documentation and issue closure

### T22: Update project documentation

Files:

- `README.md`
- `docs/rfc-001-agent-state-and-control.md`
- `docs/next-phase-research.md`
- `docs/technical-notes.md`
- relevant `AGENTS.md` files

Work:

- Document terminal actor ownership and committed-output ordering.
- Document the query reply path and why replies are not user input.
- Document the Spec 006 active-hook exceptions.
- Document stable screen evidence and suppression outcomes.
- Document explain API and CLI examples.
- Document snapshot limits and the lack of general secret scanning.
- Document the x/vt pin, Go 1.24.2 floor, and CI lanes.
- Document the reader-first rollback floor.
- Keep attach and public resize linked to Issues #19 and #20.
- Keep persistent hook installation linked to Issue #24.
- Include measured benchmark results, not estimates.

### T23: Run isolated manual verification

Use private temporary Drove, Claude, and Codex homes:

- confirm all persistent vendor config hashes before and after;
- start Claude and Codex interactive sessions;
- verify startup query replies and first-screen rendering;
- exercise approval visible and approval cleared behavior;
- exercise Claude interrupt behavior;
- compare `drove explain` and `drove explain --json`;
- detach and confirm the screen disappears from explain;
- restart the daemon and confirm durable explanation history;
- inspect SQLite, WAL, logs, replay, and Hub capture for screen rows, reply
  bytes, signal tokens, fixture-private text, and screen hashes;
- confirm trailing output replay after exit.

Record exact CLI versions and commands in `docs/technical-notes.md`.

### T24: Synchronize Issue #14

- Comment on Issue #14 with all implementation commit IDs.
- Include automated gates, 20-round race results, fixture versions, manual
  acceptance, and benchmark evidence.
- State the reader-first rollback floor.
- Link follow-up scope to Issues #19, #20, and #24.
- Close Issue #14 only after the final documentation commit is pushed and all
  acceptance checks pass.

Verification:

```bash
git diff --check
go test ./... -race -count=1
go vet ./...
make build
npm --prefix web run typecheck
npm --prefix web run build
```

Commit and push:

```text
docs: document terminal screen detection
```

## Stop conditions

Stop the current commit and return to `spec.md` if:

- unredacted or uncommitted bytes must enter the controller;
- x/vt types escape `internal/term`;
- private rule patterns escape `internal/adapter`;
- adapter or session code must decide state authority;
- the terminal actor must read Detector internals;
- the observation actor must read terminal cells;
- terminal replies must use `Manager.SendInput`;
- screen text or a screen hash must become durable;
- a third active-hook screen transition is required;
- line and screen state classifiers must coexist after writer activation;
- a detached snapshot must be retained;
- output after process exit must create a screen decision;
- a focused 20-round race gate fails;
- a completed commit cannot be pushed before the next begins.
