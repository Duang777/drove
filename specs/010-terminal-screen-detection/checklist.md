# Spec review checklist

## Direction

- [x] Issue #14 is the approval source.
- [x] Spec 009 remains the committed-output foundation.
- [x] The revision to Spec 006 is explicit and narrow.
- [x] No product code changes before this spec is approved.
- [x] Every completed implementation commit is verified and pushed before the
      next commit starts.

## Scope

- [x] Per-session terminal screen emulation is included.
- [x] Required terminal query replies are included.
- [x] Initial PTY sizing before process start is included.
- [x] Adapter-owned screen rules are included.
- [x] Screen-driven state evidence is included.
- [x] Explain API and CLI are included.
- [x] A bounded attached-screen snapshot is included.
- [x] Real Claude and Codex fixtures are included.
- [x] A 32-session throughput benchmark is included.
- [x] Terminal attach and Web rendering remain in Issues #19 and #20.
- [x] Public live resize remains in Issue #20.
- [x] Persistent hook installation remains in Issue #24.
- [x] PTY reconnection and emulator recovery are excluded.
- [x] General secret scanning is excluded.

## Dependency and toolchain

- [x] x/vt is pinned to
      `v0.0.0-20261004011457-ad85c59fdf4e`.
- [x] `go.mod` moves to Go 1.24.2.
- [x] CI tests the exact minimum Go 1.24.2.
- [x] CI retains a current-stable Go lane.
- [x] Documented prerequisites match `go.mod`.
- [x] No x/vt, x/ansi, or ultraviolet type escapes `internal/term`.
- [x] The historical Go-1.23-compatible x/vt revision remains rejected.

## Ownership

- [x] One terminal actor owns emulator mutation.
- [x] The actor owns screen dirtiness and the 100 ms sample timer.
- [x] The actor owns one adapter classifier instance.
- [x] The controller reply pump is the only x/vt reply reader.
- [x] `pty.Session.Write` remains the complete-frame write serializer.
- [x] No PTY writer actor is added.
- [x] The observation actor remains the only Detector owner.
- [x] The global Committer remains the only runtime event writer.
- [x] Explain adds no mutable projection.
- [x] Session code contains no vendor screen patterns.
- [x] Adapter code cannot grant state-transition authority.
- [x] Detector code contains no x/vt or vendor-specific branch.

## Terminal controller

- [x] The reply pump is ready before the first controller write.
- [x] DSR receives an exact pinned-revision reply.
- [x] OSC 10 and OSC 11 receive exact pinned-revision replies.
- [x] Primary device attributes receive an exact pinned-revision reply.
- [x] `CSI ? u` receives `CSI ? 0 u`.
- [x] Reply bytes call `pty.Session.Write` directly.
- [x] Reply bytes never call `Manager.SendInput`.
- [x] Reply bytes never create `agent.input`.
- [x] The reply path creates no output, Store, Hub, log, or snapshot record.
- [x] Child-emitted echo, if any, remains ordinary committed PTY output.
- [x] Reply draining continues after a closed PTY sink.
- [x] Controller close cannot deadlock on a trailing query.
- [x] Screen snapshots contain normalized immutable data.
- [x] Controller resize preserves emulator ordering.

## PTY sizing and startup

- [x] The initial size is 40 rows by 120 columns.
- [x] `creack/pty.StartWithSize` replaces start-then-resize.
- [x] A child sees the declared size on its first query.
- [x] Pending session and readiness gates exist before PTY start.
- [x] The terminal actor is attached before callbacks are released.
- [x] `Starting -> Working` commits before output and exit callbacks proceed.
- [x] `signalReady` opens before `callbacksReady`.
- [x] `callbacksReady` opens before a required-hook wait.
- [x] Codex can receive query replies during required-hook activation.
- [x] Startup failure invalidates the token, closes gates, and detaches.
- [x] Internal resize is not exposed as a new public API.

## Committed output

- [x] The terminal actor accepts only typed committed chunks.
- [x] Chunks contain copied token-redacted bytes.
- [x] Chunks carry an output offset and final durable output sequence.
- [x] Output durability precedes controller feed.
- [x] A controller feed failure preserves committed output and fails closed.
- [x] No second raw-output writer or event sequence allocator is added.

## Sampling and lifecycle

- [x] The first dirty chunk arms `now + 100ms`.
- [x] Later dirty chunks do not slide the current deadline.
- [x] Query replies do not wait for sampling.
- [x] One sample publishes one immutable snapshot.
- [x] Classification emits only rule edges.
- [x] Dirtiness after capture arms the next window.
- [x] `EndOutput` immediately samples a final dirty screen.
- [x] `MarkProcessExited` fences new screen observations.
- [x] Process exit is marked before Detector termination.
- [x] Trailing output remains durable and can update the private emulator.
- [x] Trailing output creates no screen signal.
- [x] An exited session has no explain snapshot.
- [x] Actor input is bounded and applies backpressure without dropping bytes.
- [x] Actor and controller close are idempotent and drain all goroutines.

## Adapter classifiers

- [x] Each adapter constructs a stateful classifier.
- [x] Rule definitions and compiled matchers remain private.
- [x] Region selection remains private.
- [x] Session receives only normalized `ScreenHint` values.
- [x] Hints contain kind, stable rule, edge, region, static evidence,
      confidence, and confirmation duration.
- [x] Detector rejects a rule duration that does not match its fixed policy.
- [x] Claude defines approval, idle, and interrupt rules.
- [x] Codex defines approval and idle rules.
- [x] Generic defines no screen rules.
- [x] Only Claude can emit the screen interrupt kind.
- [x] Rule names are stable and bounded ASCII identifiers.
- [x] Initial and changed matches emit edges exactly once.
- [x] `Rebaseline` changes classifier memory without inventing an edge.
- [x] Real redacted fixtures cover cursor rewrites and fragmented feeds.
- [x] Near misses are covered.
- [x] Ordinary output containing `Error` does not emit Blocked evidence.
- [x] `Heuristic`, `OutputHint`, `Entry.Classify`, and vendor line classifiers
      are removed when the screen writer activates.
- [x] `term.Stripper` remains available to `drove log --plain`.

## Detector authority

- [x] `SourceScreen` is first-class.
- [x] Process facts remain supreme.
- [x] Screen signals never produce Done.
- [x] Active hooks permit only approval clearance and Claude interrupt.
- [x] Active-hook approval clearance requires current Blocked.
- [x] Active-hook Claude interrupt requires current Working.
- [x] Approval clearance confirms after 500 milliseconds.
- [x] Claude interrupt confirms after one second.
- [x] Every other active-hook screen edge is durably suppressed.
- [x] Awaiting and failed required-hook states do not accept screen authority.
- [x] Off and fallback accept visible approval, clearance, idle, and interrupt
      rules as specified.
- [x] Approval presence confirms Blocked after 750 milliseconds.
- [x] Visible idle confirms Idle after one second.
- [x] Existing output activity and fallback silence remain text-free liveness
      evidence.
- [x] Candidate state is keyed by bounded purpose and rule identity.
- [x] One candidate cannot replace an unrelated candidate.
- [x] The observation actor owns one physical earliest-deadline timer.
- [x] Timer firings carry purpose, rule identity, and generation.
- [x] Stale timer firings are durable and cannot consume another candidate.
- [x] Opposing edges and process facts cancel the relevant candidate.

## Event contract

- [x] Signal payload version 3 is explicit.
- [x] State evidence version 3 is explicit.
- [x] Versions 1 and 2 remain readable.
- [x] Screen attribution is created once in `detect.Signal`.
- [x] The same typed attribution reaches signal and transition evidence.
- [x] Rule, edge, region, output offset, final output sequence, and static
      evidence are bounded and validated.
- [x] `output_offset` is the exclusive redacted output offset.
- [x] `last_output_seq` names already-durable output.
- [x] Screen text is absent.
- [x] Private patterns and captures are absent.
- [x] Screen hashes and reversible fingerprints are absent.
- [x] Source-screen payloads require screen attribution.
- [x] Unrelated sources reject screen attribution.
- [x] Unknown signal versions remain projection-neutral.
- [x] Unknown state evidence versions do not block state-column recovery.
- [x] Malformed known versions still fail recovery.
- [x] The reader-first event commit is the rollback floor after writer
      activation.

## Explain

- [x] `Manager.Explain` is the only domain entry point.
- [x] Explain reads only a bounded durable event tail.
- [x] SQLite filters to `agent.signal` and `state_changed`.
- [x] Output attachments are not hydrated for explain.
- [x] The default event limit is 50.
- [x] The maximum event limit is 200.
- [x] Events return in chronological order.
- [x] Unknown historical evidence is represented safely.
- [x] The API route is exactly `GET /api/v1/agents/{id}/explain`.
- [x] API code only validates transport input and maps errors.
- [x] Client code remains transport-only.
- [x] The CLI route is exactly `drove explain <id>`.
- [x] The CLI reports source, kind, outcome, rule, suppression, and transition.
- [x] `--json` returns the typed response.
- [x] Explain maintains no second source of state truth.

## Snapshot privacy

- [x] A snapshot is returned only for the currently attached actor.
- [x] At most the bottom 12 rows are returned.
- [x] At most 160 terminal cells are returned per row.
- [x] The encoded snapshot is at most 4 KiB.
- [x] Title and scrollback are excluded.
- [x] Styles, links, and clipboard data are excluded.
- [x] Reply bytes are excluded.
- [x] Snapshots are never persisted or logged.
- [x] Snapshots are never published to Hub or WebSocket.
- [x] Snapshot availability ends at detach.
- [x] The CLI labels it `ephemeral redacted current screen`.
- [x] Documentation says token redaction is not general secret detection.

## Failure and compatibility

- [x] Controller creation failure never commits Working.
- [x] Reply write failure does not invent user input or retry through
      `SendInput`.
- [x] Rule compilation fails before PTY start.
- [x] Runtime classification cannot silently fall back to line heuristics.
- [x] Committed output remains durable if terminal feed fails.
- [x] Process exit wins every screen race.
- [x] Explain racing detach returns durable history without retaining a screen.
- [x] Legacy signal and evidence payloads remain readable.
- [x] Existing replay, retention, and `drove log` behavior remains unchanged.
- [x] The Go-floor change is isolated before writer activation.
- [x] Rollback boundaries are documented.

## Verification

- [x] Exact query reply tests use deadlines.
- [x] Reply-pump startup and post-exit drain are tested.
- [x] Initial-size and internal-resize ordering are tested.
- [x] Fixed-window and final-flush sampling are tested with a fake clock.
- [x] Claude and Codex fixtures replay at varied chunk boundaries.
- [x] Active-hook exceptions and suppressions are tested.
- [x] Keyed candidates and stale timer references are tested.
- [x] Version 3 and legacy recovery are tested.
- [x] Explain bounds and detached behavior are tested.
- [x] Snapshot privacy limits are tested.
- [x] Startup, exit, output-end, and close races are tested.
- [x] Approval visible reaches Blocked within one second in off/fallback.
- [x] Approval clearance reaches Working within one second under active hooks.
- [x] Claude interrupt reaches Idle within two seconds.
- [x] Recorded startup queries do not hang.
- [x] Thirty-two actors at 1 MiB/s each complete without byte loss, deadlock,
      race, or unbounded queue growth.
- [x] Benchmark toolchain, hardware, throughput, allocations, and goroutines
      are recorded.
- [x] Focused race tests run 20 times for every implementation commit.
- [x] Full race, vet, Go build, Web typecheck, and Web build gates are listed.
- [x] Isolated manual verification does not change persistent vendor config.
- [x] Issue #14 is updated only after verification evidence is pushed.

## Approval

- [x] The owner approved `spec.md`, `checklist.md`, and `tasks.md` for
      implementation on 2026-10-04.
