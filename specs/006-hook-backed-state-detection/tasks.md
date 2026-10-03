# Implementation tasks

Implement each commit in order. Run its focused checks, commit, and push before
starting the next commit.

## Approval writeback

Before implementation:

- change `spec.md` status to `Approved for implementation`;
- check the approval item in `checklist.md`;
- record the approval date.

Commit and push:

```text
docs: approve hook-backed state detection
```

## Commit 1: reader-first event compatibility

### T1: Define the new wire contracts

Files:

- `internal/event/event.go`
- `internal/event/event_test.go`
- `internal/event/AGENTS.md`

Work:

- Add `TypeAgentSignal` with wire value `agent.signal`.
- Add payload types for signal version 1 and state evidence version 1.
- Add validation for sources, kinds, outcomes, confidence, IDs, and timestamps.
- Keep the writer disabled.

### T2: Read creation metadata version 2

Files:

- `internal/agent/agent.go`
- `internal/agent/agent_test.go`
- `internal/session/projection.go`
- `internal/session/projection_test.go`

Work:

- Add `agent.HookPolicy` and validation.
- Add the exact `agent.Evidence` fields, source allowlist, bounds, and
  validation.
- Define creation payload version 2 with required mode and hook policy.
- Continue reading version 1.
- Restore version 1 sessions with policy off.
- Do not emit version 2 yet.

### T3: Read signal and state evidence

Files:

- `internal/session/projection.go`
- `internal/session/projection_test.go`

Work:

- Accept `agent.signal` as projection-neutral.
- Validate matching nonempty session and Agent IDs.
- Parse known signal and state evidence versions.
- Count and skip unknown audit metadata versions.
- Reject malformed known versions.
- Keep unknown event types fatal.
- Preserve `done -> stopped` reconciliation without a new error.
- Separate live transition checks from recovery compatibility checks.

### T4: Update frontend readers

Files:

- `web/src/api/types.ts`
- `web/src/components/EventLog.tsx`

Work:

- Add `agent.signal` to the event union.
- Add additive state evidence and hook status fields.
- Render signal metadata without assuming a Web control exists.

Verification:

```bash
gofmt -w internal/agent internal/event internal/session
go test ./internal/agent ./internal/event ./internal/session -race
npm --prefix web run typecheck
npm --prefix web run build
git diff --check
```

Commit and push:

```text
feat: read agent signal events
```

This commit is the rollback floor after any later commit emits
`agent.signal` or creation metadata version 2.

## Commit 2: typed commit primitives

### T5: Add unsequenced event drafts

Files:

- `internal/event/event.go`
- `internal/event/event_test.go`
- `internal/event/AGENTS.md`

Work:

- Add immutable `event.Draft`.
- Add draft constructors for every current event type.
- Add sealing from a copied draft to a committed Event.
- Expose only the validated `event.Commit(seq, at, draft)` sealing operation.
- Add `Hub.PublishBatch` with nonzero, consecutive sequence validation.
- Keep current runtime calls working until Commit 3.

### T6: Add prepared Agent changes

Files:

- `internal/agent/agent.go`
- `internal/agent/agent_test.go`
- `internal/agent/AGENTS.md`

Work:

- Add Agent revision and immutable snapshots.
- Add typed state, error, and state-plus-error change intents.
- Add `Prepare` without mutation.
- Add `ApplyCommitted` with revision and Agent identity checks.
- Add read-only accessors for transition, error, evidence, and timestamp data
  in `PreparedChange`.
- Derive canonical state evidence from the prepared change.
- Keep legacy mutation methods until Commit 3.

### T7: Add the private committer

Files:

- `internal/session/committer.go`
- `internal/session/committer_test.go`
- `internal/session/AGENTS.md`

Work:

- Add the sealed event and Agent operation variants.
- Reserve the Detector operation variant for Commit 4, after
  `internal/detect` exists.
- Copy all event drafts and payload bytes at construction.
- Add the 256-operation queue and one writer goroutine.
- Keep `lastSeq` private to the writer.
- Implement admission-only caller cancellation.
- Use one `Store.AppendEvents` call per operation.
- Advance the durable boundary immediately after SQLite succeeds.
- Apply typed projections before `Hub.PublishBatch`.
- Add poison, failure notification, queue draining, and idempotent close.
- Do not route production writers to it yet.

Verification:

```bash
gofmt -w internal/agent internal/event internal/session
go test ./internal/agent ./internal/event ./internal/session ./internal/store -race -count=20
git diff --check
```

Commit and push:

```text
feat: add ordered event committer
```

## Commit 3: switch every runtime writer

### T8: Start the committer after recovery

Files:

- `internal/session/session.go`
- `internal/session/session_test.go`
- `internal/daemon/daemon.go`
- `internal/daemon/daemon_test.go`

Work:

- Start the committer after bootstrap scan and reconciliation.
- Initialize it from the committed reconciliation sequence.
- Expose one Manager failure channel to daemon.
- Make daemon begin ordered shutdown after a fatal commit error.
- Keep bootstrap reconciliation as the only direct pre-runtime batch write.
- Emit recovery state evidence with source `recovery`, event
  `daemon_restart`, and confidence 1 after the writer switch.

### T9: Migrate lifecycle and Agent mutation

Files:

- `internal/session/session.go`
- `internal/session/session_test.go`
- `internal/agent/agent.go`
- `internal/agent/agent_test.go`

Work:

- Commit creation and `Pending -> Starting` before registration.
- Migrate process-start success and failure.
- Migrate process exit, LastError, and terminal state batches.
- Derive state and error events from prepared Agent changes.
- Preserve run-mode exit semantics.
- Preserve `requestStop` and `claimExit` as the terminal single-winner
  protocol.

### T10: Migrate event-only writers

Files:

- `internal/session/session.go`
- `internal/session/session_test.go`

Work:

- Route output, input audit, and non-state errors through the committer.
- Preserve `inputMu` across PTY write and audit commit.
- Preserve the delivered-but-unaudited error.
- Preserve final output after a terminal event.
- Do not make PTY output wait for process-exit ordering.

### T11: Remove the old writers

Files:

- `internal/agent/agent.go`
- `internal/event/event.go`
- `internal/session/session.go`
- package tests and AGENTS files

Work:

- Delete `Agent.Transition`, `Agent.SetError`, and `WithStateChangeHook`.
- Delete `session.onStateChange` and `persistAndPublish`.
- Delete `Hub.NextSeq` and zero-sequence publication.
- Stop runtime use of `Store.AppendEvent`.
- Search the repository for every deleted entry point.

Verification:

```bash
gofmt -w internal/agent internal/event internal/session internal/daemon
go test ./internal/agent ./internal/event ./internal/session ./internal/daemon -race -count=20
go test ./... -race -count=1
go vet ./...
sh -c 'out=$(rg -n -F -e NextSeq -e persistAndPublish -e WithStateChangeHook -e ".Transition(" -e ".SetError(" --glob "*.go" .); code=$?; [ "$code" -eq 1 ] && [ -z "$out" ]'
git diff --check
```

The fixed-string search must exit 1 with no matches. Exit 0 means a deleted API
remains, and exit 2 means the search failed.

Commit and push:

```text
refactor: serialize runtime event commits
```

## Commit 4: Detector and fallback state recovery

### T12: Add the pure Detector

Files:

- `internal/detect/detect.go`
- `internal/detect/detect_test.go`
- `internal/detect/clock.go`
- `internal/detect/AGENTS.md`
- `internal/AGENTS.md`

Work:

- Add normalized source, kind, scope, signal, observation, state, and decision
  types.
- Add HookStatus, Outcome, and TimerPlan enum-backed values.
- Add State snapshot and Decision read-only accessors for session.
- Add source-specific constructors and bounds.
- Implement the live transition decision table.
- Implement process priority.
- Implement timer generations.
- Implement the fixed timing and confidence constants.
- Add a fake clock for deterministic tests.
- Update the internal package dependency map for
  `session -> adapter -> detect -> agent`.

### T13: Add the per-session observation actor

Files:

- `internal/session/detector.go`
- `internal/session/detector_test.go`
- `internal/session/committer.go`
- `internal/session/committer_test.go`
- `internal/session/session.go`
- `internal/session/session_test.go`

Work:

- Add one bounded actor inbox per attached session.
- Add the sealed Detector commit operation reserved in Commit 2.
- Process one decision and commit at a time.
- Apply Detector state only after SQLite succeeds.
- Add the bounded delivery ID cache.
- Drain admitted observations and cancel timers on close.
- Reject observation admission after process exit claims the session.

### T14: Route process and heuristic observations

Files:

- `internal/adapter/adapter.go`
- `internal/adapter/vendors.go`
- `internal/adapter/vendors_test.go`
- `internal/session/session.go`
- `internal/session/session_test.go`

Work:

- Replace state-bearing `StateHint` with normalized redacted hints.
- Commit output before submitting its redacted observation.
- Implement confidence filtering, Blocked confirmation, Blocked recovery, and
  silence-to-Idle.
- Keep Done text as audit-only evidence.
- Route process start, start failure, and exit facts through the actor.
- Preserve trailing output after terminal state.

Verification:

```bash
gofmt -w internal/adapter internal/detect internal/session
go test ./internal/detect ./internal/adapter ./internal/session -race -count=20
go test ./... -race -count=1
go vet ./...
git diff --check
```

Commit and push:

```text
feat: add ordered agent state detector
```

## Commit 5: vendor hooks and session policy

### T15: Add Claude and Codex normalizers

Files:

- `internal/adapter/adapter.go`
- `internal/adapter/claude_hooks.go`
- `internal/adapter/claude_hooks_test.go`
- `internal/adapter/codex_hooks.go`
- `internal/adapter/codex_hooks_test.go`
- `internal/adapter/testdata/`
- `internal/adapter/AGENTS.md`

Work:

- Add exact `Registry.Lookup`.
- Add `HookNormalizer` to hook-capable entries.
- Implement every Claude and Codex event mapping in `spec.md`.
- Map root and subagent scope.
- Map only allowlisted Claude Notifications to human wait.
- Map unknown Notifications to neutral `other`.
- Reject unknown vendors and events.
- Use capability-based activation instead of parsing a CLI version.
- Confirm that unknown events do not activate hooks and cause auto fallback or
  required failure at the normal deadline.
- Use redacted official-schema fixtures.
- Prove that sensitive and extra vendor fields do not leave the adapter.

### T16: Add policy and token lifecycle

Files:

- `internal/session/session.go`
- `internal/session/session_test.go`
- `internal/session/projection.go`
- `internal/session/projection_test.go`
- `internal/daemon/daemon.go`
- `internal/daemon/daemon_test.go`

Work:

- Normalize the default policy per adapter capability.
- Reject required policy for an unsupported adapter before creation.
- Emit creation metadata version 2.
- Generate and hash a 32-byte token.
- Register a pending session and readiness gate before `pty.Start`.
- Inject Agent ID, absolute signal URL, and token through `pty.Config.Env`.
- Make an early authenticated delivery wait for readiness.
- Invalidate the token on startup failure and exit claim.
- Keep Bootstrap before listener creation.
- After `net.Listen`, call `Manager.ConfigureSignalOrigin` once with the actual
  loopback origin and before Serve.
- Reject hook-enabled Start before origin configuration.
- Support an ephemeral listener address in tests.

### T17: Implement policy state

Files:

- `internal/detect/detect.go`
- `internal/detect/detect_test.go`
- `internal/session/detector.go`
- `internal/session/detector_test.go`

Work:

- Implement off, awaiting, fallback, active, failed, and detached states.
- Suppress auto heuristics during the five-second activation interval.
- Enable fallback after the auto interval.
- Let a late valid auto signal select hook authority permanently.
- Make required wait for a committed signal.
- Stop and return `ErrHookRequired` after required timeout.
- Make a short required oneshot without activation become Stopped.

### T18: Add the session delivery operation and status

Files:

- `internal/session/session.go`
- `internal/session/session_test.go`
- `internal/agent/agent.go`
- `internal/agent/agent_test.go`

Work:

- Add the transport-neutral `HookDelivery`.
- Add every documented session sentinel error and stable wrapping.
- Authenticate token, attachment, policy, and vendor before normalization.
- Bound hook admission at 250 milliseconds.
- Add policy, observed hook status, and last transition evidence to Status.
- Restore detached status and last understood evidence after restart.

Verification:

```bash
gofmt -w internal/agent internal/adapter internal/detect internal/session
go test ./internal/agent ./internal/adapter ./internal/detect ./internal/session ./internal/daemon -race -count=20
go test ./... -race -count=1
go vet ./...
git diff --check
```

Commit and push:

```text
feat: normalize vendor hook signals
```

## Commit 6: signal API and relay

### T19: Add the loopback signal endpoint

Files:

- `internal/api/server.go`
- `internal/api/server_test.go`
- `internal/api/AGENTS.md`

Work:

- Register `POST /api/v1/agents/{id}/signal`.
- Reject non-loopback peers.
- Require `application/json` and bearer authentication.
- Enforce the raw and encoded body limits.
- Strictly decode one versioned envelope.
- Pass nested vendor JSON to `Manager.DeliverHook`.
- Implement every approved HTTP status mapping.
- Map invalid hook policy and unsupported required hooks to 400 on agent
  creation.
- Map required activation and signal-origin failures to 503 on agent creation.
- Return 204 only for committed or duplicate delivery.

### T20: Add the relay client

Files:

- `internal/client/hook.go`
- `internal/client/hook_test.go`
- `internal/client/AGENTS.md`

Work:

- Accept Agent ID, signal URL, token, vendor, and payload as supplied values.
- Validate an absolute loopback HTTP URL and exact escaped Agent path.
- Validate one UTF-8 JSON object up to 1 MiB.
- Generate one UUID delivery ID per invocation.
- Add one retry for network, 429, or 503 failures.
- Reuse the delivery ID.
- Use a 750-millisecond timeout per attempt.
- Do not call `EnsureDaemon`.

### T21: Add `drove hook`

Files:

- `cmd/drove/main.go`
- `cmd/drove/main_test.go`
- `cmd/drove/AGENTS.md`

Work:

- Register `drove hook --vendor <vendor>`.
- Add `drove up --hooks off|auto|required`.
- Pass the selected policy through `StartRequest`.
- Reject an invalid policy before contacting the daemon.
- Read vendor JSON from stdin only, up to the client limit plus one byte.
- Read the three callback environment variables and pass their values to the
  relay client.
- Write no stdout.
- Write one redacted diagnostic to stderr on failure.
- Exit zero after valid command dispatch, including delivery failure.
- Keep ordinary CLI usage errors under the existing Cobra behavior.
- Test both the startup flag and relay command.

Verification:

```bash
gofmt -w internal/api internal/client internal/daemon cmd/drove
go test ./internal/api ./internal/client ./internal/daemon ./cmd/drove -race -count=20
go test ./... -race -count=1
go vet ./...
make build
npm --prefix web run typecheck
npm --prefix web run build
git diff --check
```

Commit and push:

```text
feat: add agent hook signal relay
```

## Commit 7: documentation and end-to-end verification

### T22: Document manual hook configuration

Files:

- `docs/hooks.md`
- `README.md`
- `docs/rfc-001-agent-state-and-control.md`
- `docs/next-phase-research.md`
- relevant `AGENTS.md` files

Work:

- Add current Claude and Codex command-hook examples.
- State that users must approve vendor trust themselves.
- Document `off`, `auto`, and `required`.
- Document that the relay is observation-only and exits zero on delivery
  failure.
- Document the 1 MiB boundary and redaction rules.
- Mark RFC-001 Phase 1A implemented without claiming Phase 1B or WebSocket
  input.

### T23: Run the full automated suite

Run:

```bash
gofmt -w cmd internal
go test ./internal/agent ./internal/event ./internal/store ./internal/pty ./internal/adapter ./internal/detect ./internal/session ./internal/api ./internal/client ./internal/daemon ./cmd/drove -race -count=20
go test ./... -race -count=1
go vet ./...
make build
npm --prefix web run typecheck
npm --prefix web run build
git diff --check
```

### T24: Run isolated daemon regressions

Use a temporary data directory and loopback port. Verify:

- two concurrent sessions produce the same consecutive order in SQLite and
  Hub;
- a forced append failure changes no Agent or Detector projection;
- Manager failure stops the daemon without a Hub-only error;
- input that wins the exit race is audited before terminal state;
- exit that wins rejects later input and hook delivery;
- final PTY output can follow terminal state and remains replayable;
- restart restores signal and state batches and reconciles Done to Stopped.

### T25: Run vendor hook regressions

With isolated manual configuration, verify:

- trusted Claude hooks activate and drive Working, Blocked, and Idle;
- untrusted or disallowed Claude hooks enter auto fallback;
- Claude required mode fails explicitly without activation;
- trusted Codex hooks activate and drive the shared state model;
- untrusted Codex hooks enter auto fallback;
- repeated relay delivery creates one signal event;
- no sensitive vendor field or token appears in SQLite.

Record the tested vendor versions and the sanitized event names in
`docs/hooks.md`.

### T26: Update issue state

- Close #2 only after both vendor regressions pass.
- Close #3 only after Blocked recovery passes for hook and fallback modes.
- Keep #4 open and note that REST and CLI input are complete while WebSocket
  input remains.
- Do not claim Phase 1B hook installation is complete.

Commit and push:

```text
docs: document hook-backed state detection
```
