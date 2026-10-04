# Implementation tasks

Complete commits in order. For every product-code commit:

1. implement only that commit's tasks;
2. run the focused checks;
3. run the common full gate;
4. inspect the complete diff;
5. commit with the listed message;
6. push immediately;
7. verify the remote branch contains the commit.

Do not mix unfinished work from two commits.

## Common full gate

```bash
gofmt -w cmd internal
go test ./... -race -count=1
go vet ./...
make build
npm --prefix web run typecheck
npm --prefix web run build
git diff --check
```

## Spec commit

Add this approved specification, checklist, and task plan.

Verification:

```bash
git diff --check
git status --short --branch -uall
```

Commit and push:

```text
docs: specify terminal streaming and replay
```

## Commit 1: reader-first terminal recording

### T1: Add canonical cursors

Files:

- `internal/recording/AGENTS.md`
- `internal/recording/cursor.go`
- `internal/recording/cursor_test.go`

Work:

- Add validated sequence, output offset, cursor, and selector types.
- Define `{0,0}` origin and exclusive next-offset semantics.
- Parse and format canonical unsigned decimal strings.
- Reject mixed or impossible cursor forms at the boundary.

### T2: Add resize event readers

Files:

- `internal/event/event.go`
- `internal/event/event_test.go`
- `internal/event/AGENTS.md`
- `internal/session/projection.go`
- `internal/session/projection_test.go`

Work:

- Add `agent.resized` and validated payload version 1.
- Include rows, columns, and output offset.
- Add the type to event validation.
- Treat it as a known non-state event during recovery.
- Do not add a production writer in this commit.

### T3: Add bounded Store ranges

Files:

- `internal/store/store.go`
- `internal/store/store_test.go`
- `internal/store/AGENTS.md`

Work:

- Add a session boundary query through an optional global sequence.
- Add bounded event pages filtered by session and sequence range.
- Cap row count and hydrated attachment bytes.
- Report output attachment presence explicitly.
- Resolve sequence, `(timestamp, seq)`, and output-offset selectors.
- Return short-lived page results; never hold rows across network waits.
- Keep `Replay` behavior unchanged.

### T4: Add a commit clock

Files:

- `internal/session/committer.go`
- `internal/session/committer_test.go`

Work:

- Track the latest durable global sequence.
- Add a race-free wait for a sequence greater than a supplied watermark.
- Advance only after Store commit.
- Collapse wakeups without losing data because readers always query SQLite.
- Close all waiters when the Committer closes or fails.

Focused verification:

```bash
go test ./internal/event ./internal/store ./internal/recording ./internal/session -race -count=20
go test ./internal/store ./internal/recording -run 'Test.*(Range|Cursor|Offset|Boundary|Retention)' -count=100
```

Commit and push:

```text
feat: add terminal recording readers
```

This is the reader-first rollback floor for resize events and v2 cursors.

## Commit 2: ordered resize and live snapshots

### T5: Serialize terminal-affecting operations

Files:

- `internal/session/output.go`
- `internal/session/output_test.go`
- `internal/session/session.go`
- `internal/session/session_test.go`

Work:

- Move output processor state behind one bounded actor inbox.
- Preserve source-offset validation, equal-length redaction, chunk splitting,
  Store-before-terminal ordering, and one output activity per PTY callback.
- Add effective resize operations to the same actor order.
- Apply PTY and emulator resize before committing `agent.resized`.
- Enter fail-stop after any post-commit or post-apply invariant failure.
- Make close in-band and return a result for every admitted request.

### T6: Add attachment size ownership

Files:

- `internal/session/attachment.go`
- `internal/session/attachment_test.go`
- `internal/session/session.go`
- `internal/session/AGENTS.md`

Work:

- Add opaque attachment IDs and writable/read-only attachment modes.
- Implement the input-driven `latest` policy.
- Make attached input promote and resize before writing.
- Recompute ownership on detach.
- Keep v1 and REST input behavior unchanged.
- Never persist attachment IDs.

### T7: Add live snapshot watches

Files:

- `internal/session/terminal.go`
- `internal/session/terminal_test.go`
- `internal/session/snapshot.go`
- `internal/session/snapshot_test.go`

Work:

- Return terminal snapshot metadata with sequence and next offset.
- Add a live-only snapshot watch at no more than 2 Hz.
- Use capacity-one latest-value delivery.
- Mark views non-restorable.
- Close watches at output end and detach.
- Do not publish through Hub or Store.

Focused verification:

```bash
go test ./internal/pty ./internal/term ./internal/event ./internal/session -race -count=20
go test ./internal/session -run 'Test.*(Output|Resize|Attachment|Snapshot|Exit|Close)' -count=100
```

Commit and push:

```text
feat: record terminal resizes
```

## Commit 3: WebSocket v2 terminal streams

### T8: Add recording tails

Files:

- `internal/recording/tail.go`
- `internal/recording/tail_test.go`
- `internal/recording/AGENTS.md`
- `internal/session/session.go`

Work:

- Add raw and event range-and-tail iterators.
- Capture an initial durable head and read bounded history through it.
- Continue from the commit clock without a Store-to-live handoff.
- Slice an initial chunk for offset selectors.
- Surface expired attachments before emitting later corrupting bytes.
- Keep each tail independently cancelable.

### T9: Add the v2 wire codec and connection queue

Files:

- `internal/api/websocket_v2.go`
- `internal/api/websocket_v2_test.go`
- `internal/api/AGENTS.md`

Work:

- Negotiate `drove.v2` without changing no-subprotocol v1.
- Add strict subscribe, unsubscribe, input, and resize requests.
- Add hello, subscribed, ack, error, event, resized, snapshot, and caught-up
  responses.
- Encode raw bytes with the specified `DRV2` binary header.
- Add one byte-accounted 8 MiB outbound queue and reserved control slot.
- Track last successfully written cursors.
- On overflow, cancel subscriptions, report `slow_consumer`, and close 1013.
- Keep one reader and one writer goroutine per connection.

### T10: Add Go and browser protocol clients

Files:

- `internal/client/terminal.go`
- `internal/client/terminal_test.go`
- `internal/client/AGENTS.md`
- `web/src/api/types.ts`
- `web/src/ws/terminalStream.ts`
- relevant TypeScript tests or build fixtures

Work:

- Add v2 negotiation and strict text/binary decoding.
- Use decimal strings for wire sequence and offset values.
- Track cursors only after the consumer applies a frame.
- Add subscribe, unsubscribe, input, and resize methods.
- Keep the existing v1 browser `EventStream` unchanged.

Focused verification:

```bash
go test ./internal/recording ./internal/session ./internal/api ./internal/client -race -count=20
go test ./internal/api -run 'TestWebSocketV(1|2).*' -count=100
npm --prefix web run typecheck
npm --prefix web run build
```

Commit and push:

```text
feat: stream terminal sessions over websocket
```

After this commit, synchronize Issue #19 with evidence. Close it only after the
final integration gate.

## Commit 4: replay timeline and frames

### T11: Add timeline projection

Files:

- `internal/recording/timeline.go`
- `internal/recording/timeline_test.go`

Work:

- Derive half-open spans from state-change envelopes.
- Decode source and screen rule from known evidence versions.
- Preserve spans with empty attribution for unknown additive versions.
- Number Blocked entries in sequence order.
- Resolve a 30-second lead-in cursor and retention availability.
- Report output head and coalesced retention holes.

### T12: Add exact frame rendering

Files:

- `internal/recording/frame.go`
- `internal/recording/frame_test.go`
- `internal/recording/cache.go`
- `internal/recording/cache_test.go`

Work:

- Resolve exactly one sequence, time, or output-offset selector.
- Replay from 40x120 origin through the captured sequence.
- Apply output and resize records in sequence order.
- Discard emulator replies.
- Return the bounded snapshot view and `exact_origin_replay` fidelity.
- Return typed missing ranges when required attachments expired.
- Add a bounded LRU of exact completed frame responses.
- Never use a visible snapshot as a later replay start.

### T13: Add REST, client, and CLI contracts

Files:

- `internal/api/server.go`
- `internal/api/server_test.go`
- `internal/client/client.go`
- `internal/client/client_test.go`
- `cmd/drove/main.go`
- `cmd/drove/main_test.go`
- relevant `AGENTS.md` files

Work:

- Add timeline, Blocked occurrence, and frame routes.
- Parse exactly one frame selector.
- Map unknown session to 404, invalid selector to 400, output expiry to 410,
  and internal corruption to 500.
- Add matching client methods.
- Add `drove timeline <id> [--json]`.
- Keep `GET /events` unchanged.

Focused verification:

```bash
go test ./internal/recording ./internal/api ./internal/client ./cmd/drove -race -count=20
go test ./internal/recording ./internal/api ./cmd/drove -run 'Test.*(Timeline|Blocked|Frame)' -count=100
```

Commit and push:

```text
feat: replay terminal timelines
```

## Commit 5: acceptance, performance, and documentation

### T14: Add end-to-end protocol acceptance

- Race subscription setup against output commits and resize.
- Verify history plus live output equals Store output exactly.
- Reconnect from every emitted cursor.
- Verify one slow client cannot delay another client or the Committer.
- Verify v1 behavior with a golden transcript.
- Verify snapshot coalescing and frequency.

### T15: Add replay acceptance and benchmarks

- Use a fixture with at least three Blocked intervals.
- Include fragmented UTF-8, split CSI/OSC, alternate screen, and resize.
- Compare sequence, time, and offset frames with direct from-origin replay.
- Prune required attachments and verify timeline succeeds while frame fails.
- Benchmark 50 MiB cold random and warm exact-cache frame requests.
- Record hardware, Go version, x/vt pin, p50, p95, bytes replayed, allocations,
  and any unmet performance target.

### T16: Update documentation

Files:

- `README.md`
- `docs/technical-notes.md`
- relevant package `AGENTS.md` files
- this checklist

Work:

- Document v2 negotiation, cursor semantics, resize ordering, reconnect, and
  slow-consumer behavior.
- Document timeline and frame endpoints and CLI.
- Document snapshot limits and non-restorable status.
- Document output expiry behavior.
- Record benchmark results without claiming an unmet target.

### T17: Integrate and close issues

- Fetch and integrate the completed #14 work from `main`.
- Resolve conflicts without dropping concurrent work.
- Run focused race tests 20 times.
- Run the common full gate.
- Re-read the complete branch diff and remote CI.
- Comment on Issues #19 and #25 with commits and verification.
- Close both issues only when their functional acceptance criteria pass.
- If cold 50 MiB p95 remains above 300 ms, open a focused x/vt checkpoint
  follow-up and state the measured limitation in #25.

Commit and push:

```text
docs: document terminal streaming and replay
```

## Stop conditions

Stop and revise this specification if:

- raw bytes must come from an uncommitted source;
- stream correctness depends on Hub delivery;
- a network writer can block the Committer or terminal actor;
- output and resize cannot share one per-session order;
- attachment IDs or screen contents must be persisted;
- terminal replies must pass through `SendInput`;
- a visible snapshot must be treated as a restorable emulator checkpoint;
- v1 must change to support v2;
- a range reader must hold SQLite rows while waiting for network I/O;
- a completed implementation commit cannot be pushed before the next begins.
