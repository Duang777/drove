# Spec review checklist

## Scope

- [x] Phase 1A delivers hook-backed detection and Blocked recovery.
- [x] Every existing runtime event producer moves to one global committer.
- [x] Hook installation and removal remain in Phase 1B.
- [x] WebSocket input remains outside this phase.
- [x] The phase does not add ACP, PTY reconnection, or remote access.
- [x] No database schema migration is required.

## Existing contracts

- [x] `internal/agent` remains the state-rule authority.
- [x] Vendor-specific parsing remains in `internal/adapter`.
- [x] `inputMu` still orders complete input audit against process exit.
- [x] `requestStop` and `claimExit` retain one terminal winner.
- [x] `callbacksReady` still gates PTY output and exit during startup.
- [x] Early hook delivery waits on the same startup readiness boundary.
- [x] PTY output may follow a terminal state event.
- [x] PTY completion drains all callbacks before teardown.
- [x] Bootstrap still reconciles `done -> stopped` without a new error.
- [x] Input delivered before an audit failure remains a do-not-retry result.

## Event ordering

- [x] Event drafts have no sequence number.
- [x] Hub no longer allocates sequence numbers.
- [x] One Manager-private goroutine owns the runtime sequence.
- [x] Every runtime writer uses `Store.AppendEvents`.
- [x] A decision's signal, optional error, and state event share one batch.
- [x] The order is SQLite, durable boundary, Agent, Detector, then Hub.
- [x] A failed append changes neither projection nor Hub.
- [x] A post-commit invariant failure poisons the committer.
- [x] No unpersisted error event is published after a store failure.
- [x] Bootstrap reconciliation remains the only pre-runtime direct batch write.

## Agent mutation

- [x] `Transition`, `SetError`, and the state callback are removed together.
- [x] State evidence fields, sources, bounds, and validation are explicit.
- [x] Agent changes are planned without mutation.
- [x] Prepared Agent changes expose only read-only data needed by session.
- [x] The committer derives state and error events from the prepared change.
- [x] Only the committer applies prepared changes.
- [x] Live and recovery transition checks are separate.
- [x] Live Done and Stopped are terminal.
- [x] Recovery can still move Done to Stopped.

## Detector

- [x] Each attached session has one observation actor.
- [x] `internal/detect` is pure and vendor-neutral.
- [x] Detector state and decisions expose typed snapshots, outcomes, changes,
  and timer plans.
- [x] Process facts outrank hooks, heuristics, and timers.
- [x] An observed active hook permanently suppresses heuristic state changes.
- [x] Config presence and trust guesses do not activate hooks.
- [x] Stop, Interrupt, TurnFailed, and idle prompt use a one-second window.
- [x] PermissionRequest uses a 750-millisecond confirmation.
- [x] Heuristic Blocked requires 0.85 confidence and 750 milliseconds.
- [x] Two activity observations within one second recover fallback Blocked.
- [x] Sixty seconds of fallback silence can produce Idle, never Done.
- [x] Timer generations prevent stale callbacks from changing state.
- [x] Duplicate delivery IDs write no second event.
- [x] The delivery cache is bounded at 1024 entries.
- [x] Semantic duplicates with new delivery IDs remain auditable.
- [x] Subagent completion and task-completion hints cannot produce Done.
- [x] Only a successful natural oneshot exit can produce live Done.

## Hook policy

- [x] Empty policy defaults to auto for hook-capable adapters.
- [x] Empty policy defaults to off for adapters without hook support.
- [x] Off injects no callback environment and enables fallback immediately.
- [x] Auto waits five seconds before enabling fallback.
- [x] A late valid auto signal can still select hook authority.
- [x] Required rejects an unsupported adapter before creation.
- [x] Required waits for a committed valid signal.
- [x] Required timeout stops the process and returns a typed error.
- [x] Required never uses heuristic state changes.
- [x] A short required oneshot without activation becomes Stopped, not Done.

## Security and privacy

- [x] The endpoint accepts only loopback peers.
- [x] Each attached session has a 32-byte random capability token.
- [x] Manager stores only the token digest.
- [x] Token comparison uses constant time.
- [x] The strict relay envelope is versioned.
- [x] Unknown envelope fields and trailing JSON are rejected.
- [x] Raw payload and envelope limits are explicit.
- [x] Signal lookup never uses the generic adapter fallback.
- [x] Claude and Codex event mappings are complete for the supported set.
- [x] Capability observation replaces a static minimum-version claim.
- [x] Unsupported events cannot activate hooks.
- [x] Unknown Notification subtypes cannot produce Blocked.
- [x] Prompt, tool input, transcript, assistant text, token, and raw JSON are
  excluded from durable payloads.
- [x] The relay validates an absolute loopback URL.
- [x] CLI owns stdin and environment access; client owns validation, delivery
  ID generation, retry, and HTTP transport.
- [x] The relay does not call `EnsureDaemon`.
- [x] Relay retries reuse the delivery ID.
- [x] The observation relay cannot block vendor actions through its exit code.

## API and status

- [x] The endpoint path, envelope, auth header, and body limits are explicit.
- [x] The session delivery value and stable error classes are explicit.
- [x] Agent creation errors for invalid, unsupported, required, and
  unconfigured-origin cases have HTTP mappings.
- [x] HTTP mappings cover malformed, unauthorized, non-loopback, unknown,
  detached, oversized, invalid vendor, backpressure, and shutdown cases.
- [x] A 204 response means durable success or an already committed duplicate.
- [x] Status distinguishes policy from observed hook status.
- [x] Status does not claim that config or trust exists.
- [x] Recovered sessions report detached hook status.
- [x] The CLI exposes `drove up --hooks off|auto|required`.
- [x] TypeScript receives additive status and event fields.
- [x] No Web control is added in this phase.

## Compatibility

- [x] Creation metadata version 1 remains readable.
- [x] Historical version 1 sessions recover with hooks off.
- [x] Creation metadata version 2 stores mode and hook policy.
- [x] Empty legacy state payloads remain readable.
- [x] Known signal and state evidence payload versions are validated.
- [x] Unknown audit metadata versions are counted and skipped.
- [x] Unknown event types remain fatal.
- [x] Reader compatibility lands before any new writer.
- [x] The reader-first commit is the rollback floor after writer activation.

## Backpressure and shutdown

- [x] The per-session inbox is bounded at 64 observations.
- [x] The global queue is bounded at 256 operations.
- [x] Durable events are not silently dropped.
- [x] Hook admission has a 250-millisecond bound and retryable response.
- [x] Accepted commit operations return a definite result after cancellation.
- [x] Committer failure releases current, queued, and future callers.
- [x] Daemon observes Manager failure and begins shutdown.
- [x] Bootstrap finishes before listener creation.
- [x] Manager accepts the actual loopback signal origin exactly once before
  API serving.
- [x] API, Manager, PTY, Detector, committer, Hub, and Store close in dependency
  order.
- [x] Close operations are idempotent.
- [x] The internal dependency map includes `adapter -> detect -> agent`.

## Verification

- [x] Reader, transition, committer, Detector, adapter, API, client, CLI, PTY,
  recovery, and daemon tests are specified.
- [x] Fake-clock tests cover every timer and race.
- [x] Fault injection covers append, apply, and publish failures.
- [x] Concurrent sessions prove one SQLite and Hub order.
- [x] Manual trusted and untrusted Claude and Codex cases are required.
- [x] Manual database inspection checks signal redaction.
- [x] Full race, vet, Go build, Web typecheck, and Web build checks are required.
- [x] Each independently verifiable boundary is committed and pushed before
  the next boundary starts.

## Approval

- [ ] The owner approved this specification for implementation.
