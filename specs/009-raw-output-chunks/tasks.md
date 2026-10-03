# Implementation tasks

Implement each commit in order. Run its focused checks, commit, and push before
starting the next commit.

## Spec commit

Commit this approved specification:

```text
docs: specify raw output chunk storage
```

## Commit 1: reader-first event and storage support

This commit must be deployable before any process emits `output.chunk`.

### T1: Add the output chunk event contract

Files:

- `internal/event/event.go`
- `internal/event/event_test.go`
- `internal/event/AGENTS.md`

Work:

- Add `TypeOutputChunk` with wire value `output.chunk`.
- Add `OutputChunkPayloadV1`.
- Separate immutable stored metadata from hydrated `data_b64`.
- Add metadata and hydrated validators.
- Add a draft constructor that copies raw bytes.
- Keep output attachment bytes private inside drafts and committed events.
- Expose copied attachment data and stored metadata to the Committer.
- Reject empty, oversized, malformed, and length-mismatched chunks.
- Keep legacy `TypeOutput` unchanged.

### T2: Add schema version 2 and atomic attachments

Files:

- `internal/store/store.go`
- `internal/store/store_test.go`
- `internal/store/AGENTS.md`

Work:

- Add `output_chunks(event_seq, data)` in schema version 2.
- Keep `events` as the global sequence and immutable envelope table.
- Enable and verify `foreign_keys=ON`.
- Extend `EventRow` with a JSON-hidden output attachment field.
- Write the envelope and attachment in one transaction.
- Reject output attachments on non-output event types.
- Require an attachment for every newly appended `output.chunk`.
- Keep `ScanEvents` attachment-free.
- Left-join attachments in `Replay`.
- Prove that migration does not rewrite schema version 1 rows.
- Prove rollback on either envelope or attachment failure.

### T3: Add all output chunk readers

Files:

- `internal/session/committer.go`
- `internal/session/committer_test.go`
- `internal/session/projection.go`
- `internal/session/projection_test.go`
- `internal/session/session.go`
- `internal/session/session_test.go`
- `cmd/drove/main.go`
- `cmd/drove/main_test.go`
- `cmd/drove/AGENTS.md`
- `web/src/api/types.ts`
- `web/src/components/EventLog.tsx`
- relevant `AGENTS.md` files

Work:

- Map committed output attachments into Store rows.
- Recognize `output.chunk` as projection-neutral.
- Hydrate retained replay rows in `Manager.Replay`.
- Leave expired replay rows as metadata without `data_b64`.
- Change `drove log` default output to raw terminal bytes only.
- Replay legacy `output` rows with one synthetic newline.
- Skip expired bodies without writing marker text to stdout.
- Add `output.chunk` to the TypeScript event union.
- Format Web output chunks as bounded offset and length summaries.
- Never render Base64 data in the Web event log.
- Seed retained and expired rows to prove reader behavior.

Verification:

```bash
gofmt -w cmd/drove internal/event internal/session internal/store
go test ./internal/event ./internal/store ./internal/session ./internal/api ./internal/client ./cmd/drove -race -count=20
go test ./... -race -count=1
go vet ./...
make build
npm --prefix web run typecheck
npm --prefix web run build
git diff --check
```

Commit and push:

```text
feat: read raw output chunk events
```

This commit is the rollback floor before Commit 2 enables the writer.

## Commit 2: raw PTY writer and derived text

### T4: Replace the PTY line reader

Files:

- `internal/pty/pty.go`
- `internal/pty/pty_test.go`
- `internal/pty/AGENTS.md`

Work:

- Remove `bufio.Reader.ReadString`.
- Read with a 32 KiB byte buffer.
- Add `OnOutput(chunk []byte, offset uint64)`.
- Add `OnOutputEnd(offset uint64)`.
- Copy callback bytes before delivery.
- Deliver available output immediately without a timer.
- Keep valid UTF-8 code points intact between normal chunks.
- Preserve invalid and incomplete final bytes exactly.
- Call output end once after the final output callback.
- Keep output end unordered with process exit.
- Keep `Close` idempotent and waiting for all callbacks.
- Add forced short-read, UTF-8 boundary, EOF, Close, and no-newline tests.

### T5: Add streaming terminal text helpers

Files:

- `internal/term/stream.go`
- `internal/term/stream_test.go`
- `internal/term/AGENTS.md`
- `internal/adapter/terminal.go`
- `internal/adapter/terminal_test.go`
- `internal/adapter/adapter.go`
- `internal/adapter/AGENTS.md`

Work:

- Add a stateful control-sequence stripper.
- Carry incomplete CSI, OSC, DCS, SOS, PM, APC, and escape sequences across
  feeds.
- Preserve ordinary UTF-8 and invalid non-control bytes.
- Add a one-shot helper for existing adapter callers.
- Move the existing adapter scanner tests to the shared implementation.
- Keep `Entry.Classify` compatible with complete ANSI-decorated lines.
- Do not add a terminal emulator or query replies.

### T6: Add the per-session output processor

Files:

- `internal/session/output.go`
- `internal/session/output_test.go`
- `internal/session/session.go`
- `internal/session/session_test.go`
- `internal/session/signal_test.go`
- `internal/session/committer_test.go`
- `internal/session/AGENTS.md`
- `cmd/drove/main.go`
- `cmd/drove/main_test.go`

Work:

- Give every `runningSession` one output processor.
- Validate contiguous PTY source offsets.
- Replace signal tokens across callback boundaries.
- Retain only a suffix that can still match the token prefix.
- Use a length-preserving `[REDACTED]` mask.
- Split redacted event data at 32 KiB without splitting valid UTF-8.
- Commit output drafts before Hub publication and Detector observations.
- Deliver one output-activity observation per committed source callback.
- Derive sanitized LF lines with one trailing CR removed.
- Bound one unfinished line at 64 KiB and retain its newest bytes.
- Flush token, UTF-8, terminal-stripper, and final-line state at output end.
- Persist final output after terminal state without reopening Detector state.
- Add `drove log --plain` with the shared streaming stripper.
- Test every signal-token split position and search all output paths.
- Test Store failure before observation and publication.
- Test concurrent sessions under the race detector.

Verification:

```bash
gofmt -w cmd/drove internal/adapter internal/pty internal/session internal/term
go test ./internal/term ./internal/pty ./internal/event ./internal/store ./internal/adapter ./internal/detect ./internal/session ./cmd/drove -race -count=20
go test ./... -race -count=1
go vet ./...
make build
npm --prefix web run typecheck
npm --prefix web run build
git diff --check
```

Commit and push:

```text
feat: record raw PTY output chunks
```

## Commit 3: retention and private storage

### T7: Add output retention config

Files:

- `internal/config/config.go`
- `internal/config/config_test.go`
- `internal/config/AGENTS.md`
- `cmd/drove/main.go`
- `cmd/drove/main_test.go`

Work:

- Add `StorageConfig.OutputRetentionDays`.
- Default the value to 30 when omitted.
- Accept `0` as permanent retention.
- Reject negative values.
- Include the default in `drove init` output.
- Keep unknown future storage fields forward compatible.

### T8: Add attachment pruning

Files:

- `internal/store/store.go`
- `internal/store/store_test.go`
- `internal/store/AGENTS.md`

Work:

- Add a cutoff-based output attachment prune operation.
- Delete only attachments joined to `output.chunk` envelopes.
- Keep every `events` row unchanged.
- Enable and verify `secure_delete=ON`.
- Run `wal_checkpoint(TRUNCATE)` after cleanup, including zero-row cleanup.
- Return the deleted attachment count.
- Prove that old output lines and every non-output event remain.
- Prove that `LastSeq` and projection scans remain unchanged.
- Do not run `VACUUM`.

### T9: Schedule startup and daily cleanup

Files:

- `internal/daemon/daemon.go`
- `internal/daemon/retention.go`
- `internal/daemon/retention_test.go`
- `internal/daemon/daemon_test.go`
- `internal/daemon/AGENTS.md`

Work:

- Run cleanup after Store open and before session bootstrap.
- Abort startup when the first cleanup fails.
- Start one 24-hour loop after successful startup.
- Inject current time and ticks in focused tests.
- Log scheduled failures and continue.
- Cancel and join the loop before Store close.
- Prove projection recovery and next-sequence allocation after cleanup.

### T10: Create private storage paths and warn on old modes

Files:

- `internal/config/config.go`
- `internal/config/config_test.go`
- `internal/auth/auth.go`
- `internal/auth/auth_test.go`
- `internal/store/store.go`
- `internal/store/store_test.go`
- `internal/daemon/storage_permissions.go`
- `internal/daemon/storage_permissions_test.go`
- `internal/daemon/daemon.go`
- `internal/client/client.go`
- `internal/client/client_test.go`
- `cmd/drove/main.go`
- `cmd/drove/main_test.go`
- relevant `AGENTS.md` files

Work:

- Create new Drove data directories with mode `0700`.
- Pre-create a missing SQLite file with mode `0600`.
- Reject non-directory data paths and non-regular database paths.
- Inspect existing modes with `Lstat`.
- Log structured warnings for group or other permission bits.
- Do not change existing path modes automatically.
- Align `drove init`, auth, daemon logs, and session injection roots.
- Test new paths, existing secure paths, broad paths, and invalid file types.

Verification:

```bash
gofmt -w cmd/drove internal/auth internal/client internal/config internal/daemon internal/store
go test ./internal/auth ./internal/client ./internal/config ./internal/store ./internal/daemon ./cmd/drove -race -count=20
go test ./... -race -count=1
go vet ./...
make build
npm --prefix web run typecheck
npm --prefix web run build
git diff --check
```

Commit and push:

```text
feat: expire retained output attachments
```

## Commit 4: documentation and end-to-end proof

### T11: Update project documentation

Files:

- `README.md`
- `docs/rfc-001-agent-state-and-control.md`
- `docs/next-phase-research.md`
- `docs/technical-notes.md`
- relevant `AGENTS.md` files

Work:

- Document `output.chunk` and legacy `output`.
- Document raw and `--plain` replay.
- Document the 30-day default and permanent-retention setting.
- Explain that retention removes bytes but keeps event metadata.
- Explain that the SQLite file can retain its allocated size.
- Document the reader-first rollback floor.
- Keep terminal emulation and query replies linked to Issue #14.
- Update directory ownership for `internal/term`.

### T12: Run automated verification

Run:

```bash
gofmt -w cmd internal
go test ./internal/term ./internal/pty ./internal/event ./internal/store ./internal/session ./internal/config ./internal/daemon ./internal/api ./internal/client ./cmd/drove -race -count=20
go test ./... -race -count=1
go vet ./...
make build
npm --prefix web run typecheck
npm --prefix web run build
git diff --check
```

### T13: Run isolated output verification

Use a temporary Drove data directory and a private config:

- verify new directory and database modes;
- start a no-newline `/bin/sh` prompt and record local delivery latency;
- capture output containing ANSI, UTF-8, invalid bytes, and a final partial
  code point;
- compare decoded replay chunks with direct PTY output;
- confirm raw and plain CLI modes;
- confirm the signal token is absent from the database, WAL, daemon log, Hub
  capture, and replay response;
- stop and restart the daemon;
- confirm recovery and sequence continuation.

### T14: Run real-vendor recording checks

With isolated Claude Code and Codex homes:

- record one Claude TUI startup and one Codex TUI startup;
- compare Drove's decoded chunk stream with a direct PTY capture;
- confirm terminal queries arrive before any newline;
- do not add replies or expect the Codex TUI to pass its query handshake in
  this issue;
- record the exact CLI versions in `docs/technical-notes.md`;
- keep every persistent vendor config hash unchanged.

### T15: Verify retention

With an injected or fixture clock:

- create retained and expired output attachments;
- keep state, signal, lifecycle, error, and input-audit events around the
  cutoff;
- run cleanup and confirm only expired attachments disappear;
- confirm expired metadata remains;
- confirm `secure_delete` is enabled and the WAL checkpoint completes;
- restart and confirm `drove ps`;
- append another event and confirm the global sequence continues.

### T16: Synchronize the Issue

- Comment on Issue #13 with commit and verification evidence.
- Close Issue #13 only after all acceptance checks pass.
- Leave Issue #14 open and note that raw chunk delivery is now available.
- Push the documentation commit before changing Issue state.

Commit and push:

```text
docs: document raw output retention
```

## Stop conditions

Stop implementation and return to the specification if any of these occur:

- output retention requires deleting or updating an `events` row;
- redaction changes output byte length;
- a raw byte reaches Detector evidence, an error event, or a daemon
  diagnostic log;
- terminal query replies are required to complete a test;
- a new third-party terminal dependency is needed;
- an existing vendor config must change;
- a commit cannot pass its focused race tests.
