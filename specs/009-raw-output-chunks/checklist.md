# Spec review checklist

## Direction

- [ ] Issue #13 is the approval source.
- [ ] Issue #13 lands before Issue #14.
- [ ] Issue #14 retains terminal emulation, query replies, and screen rules.
- [ ] Issues #19 and #20 retain terminal streaming and UI work.
- [ ] No product code changes before this spec is approved.

## Scope

- [ ] Raw PTY output chunks are included.
- [ ] Per-session byte offsets are included.
- [ ] Cross-chunk signal-token redaction is included.
- [ ] A derived line view keeps current heuristics working.
- [ ] Output attachment retention is included.
- [ ] Raw input capture is excluded.
- [ ] Terminal screen emulation is excluded.
- [ ] Terminal query replies are excluded.
- [ ] Attach, resize, and playback timing are excluded.
- [ ] Legacy output-row deletion is excluded.
- [ ] Automatic `VACUUM` is excluded.

## Event contract

- [ ] The event type is exactly `output.chunk`.
- [ ] Payload version 1 is explicit.
- [ ] Offset is zero-based and per session.
- [ ] Length is decoded bytes, not Base64 bytes.
- [ ] Live and retained replay payloads include `data_b64`.
- [ ] Stored and expired envelopes omit `data_b64`.
- [ ] Missing `data_b64` means expired, not empty.
- [ ] Chunks are nonempty and at most 32 KiB.
- [ ] Draft constructors copy caller bytes.
- [ ] Unknown payload versions cannot change Agent projection.
- [ ] Legacy `output` remains readable.

## Reader-first rollout

- [ ] Recovery accepts `output.chunk` before the writer emits it.
- [ ] Store migration and hydration land before the writer.
- [ ] CLI replay reads legacy, retained, and expired output.
- [ ] TypeScript readers accept `output.chunk`.
- [ ] Web summaries never render Base64 text.
- [ ] The reader-first commit is the rollback floor after writer activation.
- [ ] Downgrade behavior is documented.

## PTY delivery

- [ ] `ReadString` is replaced with bounded `Read`.
- [ ] `OnOutput` receives copied bytes and a source offset.
- [ ] `OnOutputEnd` runs exactly once.
- [ ] No output callback starts after `OnOutputEnd`.
- [ ] `OnExit` and `OnOutputEnd` remain unordered.
- [ ] Close waits for output, output-end, and exit callbacks.
- [ ] No chunk exceeds 32 KiB.
- [ ] Normal chunks do not split valid UTF-8 code points.
- [ ] Invalid and incomplete final bytes remain byte-exact.
- [ ] Output is delivered without a coalescing timer.

## Session processing

- [ ] Each running session owns one output processor.
- [ ] The processor validates every source offset.
- [ ] Store commit precedes Hub publication and Detector observation.
- [ ] Final output remains durable after terminal state.
- [ ] Terminal sessions receive no later heuristic observation.
- [ ] The redactor carries token prefixes across reads.
- [ ] The token mask preserves byte length.
- [ ] Output end flushes a partial nonmatching token prefix.
- [ ] Persisted, published, replayed, and logged data contain no token.
- [ ] Derived line state consumes only redacted bytes.
- [ ] Derived lines handle split terminal control sequences.
- [ ] One unfinished line uses at most 64 KiB.
- [ ] Every committed PTY callback creates one output activity.
- [ ] Adapter heuristics remain vendor-owned.

## Storage

- [ ] Schema version 2 adds only `output_chunks`.
- [ ] Existing rows are not rewritten.
- [ ] Event envelopes remain in `events`.
- [ ] Raw bytes use SQLite BLOB storage.
- [ ] Envelope and attachment inserts share one transaction.
- [ ] Store failures expose neither half of an output event.
- [ ] Recovery scans immutable envelopes without loading raw BLOBs.
- [ ] Replay hydrates retained attachments with Base64.
- [ ] Replay keeps expired metadata without inventing empty output.
- [ ] Attachment deletion cannot reduce the global sequence.
- [ ] `foreign_keys=ON` is set and verified.
- [ ] Foreign-key and type checks reject orphan attachments.

## Retention

- [ ] The config path is `storage.output_retention_days`.
- [ ] The default is 30.
- [ ] `0` keeps output indefinitely.
- [ ] Negative values fail validation.
- [ ] Startup cleanup runs before projection recovery.
- [ ] Startup cleanup failure aborts startup.
- [ ] Scheduled cleanup runs every 24 hours.
- [ ] Scheduled cleanup failure logs a warning and retries later.
- [ ] The cleanup loop stops before Store close.
- [ ] Tests inject time and ticks.
- [ ] Cleanup deletes only `output_chunks`.
- [ ] State, lifecycle, signal, error, and input-audit events remain.
- [ ] `secure_delete=ON` is set and verified.
- [ ] Cleanup requests a truncating WAL checkpoint.
- [ ] No `VACUUM` runs automatically.

## Replay

- [ ] `drove log` writes only output bytes by default.
- [ ] Default replay adds no timestamps or lifecycle text.
- [ ] Legacy output rows receive one synthetic newline.
- [ ] Retained chunks preserve byte order.
- [ ] Expired bodies are skipped without corrupting stdout.
- [ ] `--plain` strips controls incrementally.
- [ ] `--plain` does not claim to render a screen.
- [ ] REST replay still returns every event envelope.
- [ ] Existing `EventRow` JSON field names remain unchanged.

## Permissions

- [ ] Newly created data directories use `0700`.
- [ ] Newly created SQLite files use `0600`.
- [ ] Existing broad modes produce structured warnings.
- [ ] Existing modes are not silently changed.
- [ ] Non-directory data paths are rejected.
- [ ] Non-regular database paths are rejected.
- [ ] `drove init`, auth, daemon log setup, and injection roots agree.

## Compatibility

- [ ] Schema version 1 migrates in place.
- [ ] Legacy line output remains replayable.
- [ ] Legacy line output is not pruned.
- [ ] Retention does not affect projection recovery.
- [ ] Retention does not affect the next global sequence.
- [ ] No third-party dependency is added.
- [ ] Go remains at version 1.23.
- [ ] Existing input-audit privacy remains unchanged.

## Verification

- [ ] No-newline delivery has a 50 ms acceptance check.
- [ ] Byte-exact PTY round trips are tested.
- [ ] UTF-8 read boundaries are tested.
- [ ] Every token split position is tested.
- [ ] Offset continuity is tested after redaction.
- [ ] Event and attachment atomicity is tested.
- [ ] Retained and expired replay are tested.
- [ ] Fake-clock retention is tested.
- [ ] WAL and secure-delete behavior is tested.
- [ ] Restart and sequence continuation are tested after pruning.
- [ ] New and existing permission modes are tested.
- [ ] Raw and plain CLI replay are tested.
- [ ] Web typecheck and output summaries are tested.
- [ ] Multi-session output passes repeated race tests.
- [ ] Full race, vet, Go build, Web typecheck, and Web build gates are listed.
- [ ] Every implementation commit is verified and pushed before the next.

## Approval

- [ ] The owner approves `spec.md`, `checklist.md`, and `tasks.md`.
