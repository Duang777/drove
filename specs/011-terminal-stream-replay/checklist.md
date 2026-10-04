# Terminal stream and replay checklist

## Approval and scope

- [x] Issues #19 and #25 are the approval source.
- [x] agent-2 owns both issues as one delivery batch.
- [x] Issue #14 remains an upstream dependency and is not duplicated here.
- [x] Issue #21 security middleware remains outside protocol-specific code.
- [x] Attach UI, xterm rendering, and the tower grid remain in #20 and #26.
- [x] Rendered screens remain ephemeral.

## Architecture

- [x] SQLite events and output attachments remain the only recording source.
- [x] A collapsed commit notification is only a wakeup, never a data source.
- [x] Raw streams do not use the lossy event Hub.
- [x] Store reads are bounded by rows, bytes, and a captured sequence.
- [x] Output and effective resize share one per-session actor order.
- [x] The existing terminal actor remains the sole x/vt owner.
- [x] No Manager lock is held across actor, PTY, Store, or network waits.
- [x] The new recording package has a narrow read-only responsibility.

## Cursor and persistence

- [x] Cursor means last consumed sequence plus next output byte.
- [x] Sequence is global and may be sparse per session.
- [x] Offset is session-local and uses the redacted persisted byte stream.
- [x] Offset subscriptions may begin inside a retained chunk.
- [x] Full cursors are validated against immutable history.
- [x] JSON sequence and offset fields use decimal strings.
- [x] Resize payload includes rows, columns, and current output offset.
- [x] Resize readers land before the first production writer.
- [x] Old sessions default to 40 rows by 120 columns.

## WebSocket v2

- [x] No subprotocol preserves the existing v1 path.
- [x] `drove.v2` is explicitly negotiated and echoed.
- [x] Unsupported explicit subprotocols are rejected.
- [x] Text requests reject unknown fields and duplicate request IDs.
- [x] Binary raw framing fixes magic, kind, flags, byte order, ID length,
  sequence, and start offset.
- [x] One connection may subscribe to multiple agents.
- [x] Duplicate `agent_id + mode` subscriptions are rejected.
- [x] One writer goroutine owns every frame and control write.
- [x] The connection queue is byte-bounded.
- [x] Slow consumers receive a best-effort last-written cursor and close 1013.
- [x] Other clients, the Committer, and PTY callbacks cannot be blocked by a
  slow client.
- [x] Raw history and live delivery use the same Store range reader.

## Attachments and resize

- [x] The first writable attachment owns the initial viewport.
- [x] A non-owner resize only updates its proposal.
- [x] Successful attached input promotes the sender before writing input.
- [x] Owner detach selects the remaining attachment with greatest activity.
- [x] Read-only and snapshot subscriptions never own size.
- [x] Duplicate effective size writes no resize event.
- [x] PTY and emulator resize complete before event commit.
- [x] A post-apply commit failure enters fail-stop.

## Snapshot channel

- [x] Snapshot delivery is independent of the event Hub.
- [x] Sampling is at most 2 Hz per subscription.
- [x] An unread sample is replaced by the latest sample.
- [x] Snapshot messages include cursor and dimensions.
- [x] Snapshot messages state `restorable:false`.
- [x] Snapshot data is unavailable after detach.

## Timeline and frame

- [x] State spans derive only from `state_changed` envelopes.
- [x] Spans are half-open and ordered by sequence.
- [x] Blocked occurrences use one-based ordinals.
- [x] Evidence source and screen rule decode from known payload versions.
- [x] Unknown additive evidence versions do not destroy the state band.
- [x] Blocked lookup includes a 30-second lead-in cursor.
- [x] Timeline reports output coverage and retention holes.
- [x] Frame accepts exactly one sequence, time, or offset selector.
- [x] Time resolution is deterministic by timestamp and sequence.
- [x] Frame replay applies output and resize in sequence order.
- [x] Replay-generated terminal replies are discarded.
- [x] Frames reuse the existing bounded snapshot view.
- [x] Missing required output returns `output_expired`.
- [x] Timeline remains available after output expiry.
- [x] Exact frame cache entries cannot seed later replay.
- [x] No visible snapshot is described as a restorable keyframe.

## Privacy and compatibility

- [x] Raw v2 bytes come only from persisted, token-redacted attachments.
- [x] Input text is not persisted.
- [x] Terminal reply bytes are not streamed or audited.
- [x] Snapshot and frame caches are memory-only.
- [x] Existing `/events` JSON remains unchanged.
- [x] Existing v1 WebSocket hello, events, input, ack, error, ping, and close
  behavior remains unchanged.

## Verification

- [x] Reader-first event and Store tests pass.
- [x] Range-tail concurrency and reconnect property tests pass.
- [x] Binary codec tests pass in Go and TypeScript.
- [x] Multi-client resize ordering tests pass.
- [x] Slow-consumer isolation tests pass.
- [x] Snapshot coalescing and 2 Hz tests pass.
- [x] Timeline and Blocked fixture tests pass.
- [x] Frame golden and expiry tests pass.
- [x] 50 MiB cold and warm benchmark results are recorded.
- [x] Focused packages pass 20 race-enabled repetitions.
- [x] `go test ./... -race -count=1` passes.
- [x] `go vet ./...` passes.
- [x] `make build` passes.
- [x] Web typecheck and build pass.
- [x] Every implementation commit is pushed before the next begins.
- [x] Issues #19 and #25 contain final evidence and are closed.
