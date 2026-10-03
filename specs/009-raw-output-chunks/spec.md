# Raw PTY output chunks and retention

Status: Draft for approval

Owner direction: implement Issue #13 before Issue #14.

Related issues:

- [#13](https://github.com/Duang777/drove/issues/13), raw PTY output chunks and retention;
- [#14](https://github.com/Duang777/drove/issues/14), terminal emulation and screen rules;
- [#19](https://github.com/Duang777/drove/issues/19), terminal stream protocol and backpressure;
- [#20](https://github.com/Duang777/drove/issues/20), attach and terminal UI.

Implementation baseline: `9e1745d`.

## Problem

`internal/pty.Session.readLoop` calls `ReadString('\n')`. This makes a newline
part of the transport contract even though a PTY is a byte stream.

The line reader causes four failures:

- a prompt without a newline does not reach the session observer promptly;
- line boundaries do not preserve terminal control sequences or cursor state;
- replay cannot reproduce the bytes that a terminal received;
- Issue #14 cannot feed a terminal emulator or answer startup queries from
  Codex and other full-screen programs.

The current `output` event also stores terminal text directly in the
append-only `events` table. A retention job cannot delete those rows safely.
Deleting a tail output row would make `MAX(seq)` move backwards, which could
reuse a global event sequence. Deleting an interior row would also make the
event log mutable.

Raw output can contain credentials or private source text. Drove must bound how
long it keeps new raw output without deleting lifecycle, state, signal, error,
or input-audit history.

## Goal

This change establishes a lossless output transport for future terminal
features while preserving event-sourcing rules:

1. PTY reads deliver bounded byte chunks without waiting for a newline.
2. Each session records zero-based byte offsets and can detect a missing or
   reordered chunk.
3. New `output.chunk` events carry a versioned Base64 wire payload.
4. SQLite stores raw bytes as a retention-managed attachment to an immutable
   event envelope.
5. Signal tokens remain absent even when one token spans multiple PTY reads.
6. Detector heuristics keep a bounded derived line view.
7. `drove log` replays terminal bytes by default and offers a plain text view.
8. New raw output expires after 30 days by default. A value of `0` keeps it.
9. A restart after retention continues from the previous global event
   sequence and reconstructs every Agent projection.

## Non-goals

This change does not:

- emulate a terminal screen;
- answer DSR, OSC color, device-attribute, or keyboard-mode queries;
- add screen-based state rules or change Detector authority;
- add terminal attach, resize APIs, timing playback, or multi-client flow
  control;
- capture raw user input;
- change the existing redacted `agent.input` audit contract;
- expire legacy `output` rows already stored in the append-only event table;
- run `VACUUM` or promise that the SQLite file shrinks after cleanup;
- add a new third-party dependency or raise the Go version.

Issue #14 owns terminal emulation, query replies, and screen authority. Issues
#19 and #20 own live terminal transport and rendering.

## Evidence

The design follows three current facts:

- asciicast v3 records terminal output as ordered output events instead of
  lines:
  <https://docs.asciinema.org/manual/asciicast/v3/>;
- asciinema does not capture input by default because input can contain
  passwords:
  <https://docs.asciinema.org/faq/#does-asciinema-record-the-passwords-i-type-during-recording-sessions>;
- SQLite normally leaves deleted bytes in reusable pages. `secure_delete`
  overwrites deleted content, and a WAL checkpoint can truncate the WAL:
  <https://www.sqlite.org/pragma.html#pragma_secure_delete> and
  <https://www.sqlite.org/pragma.html#pragma_wal_checkpoint>.

Issue #14 also records a real Codex 0.160.0 startup sequence that stops before
the first screen. Codex writes terminal queries without a newline, so the
current reader never passes them to an upper layer.

## Chosen design

Drove keeps an immutable event envelope and stores raw output in a separate
attachment table. The event sequence, timestamp, session identity, offset, and
length stay in `events`. Only the attached bytes expire.

```text
PTY read
   |
   v
bounded UTF-8-safe chunks
   |
   v
session output processor
   |-- validate source offset
   |-- redact the signal token across reads
   |-- create output.chunk draft
   |-- derive bounded plain lines
   |
   v
global Committer
   |
   +--> events: immutable envelope and output metadata
   |
   +--> output_chunks: raw BLOB attachment
   |
   +--> Hub: hydrated output.chunk event
             |
             +--> WebSocket readers

Replay: events LEFT JOIN output_chunks -> hydrated retained chunks
Prune:  DELETE output_chunks only      -> event envelope stays
```

This adds a schema migration and changes more than eight files across PTY,
event, store, session, daemon, config, CLI, API readers, and Web readers. The
change is split into reader-first commits so each commit is deployable and has
a clear rollback point.

### Rejected direct deletion

The minimal implementation would put Base64 bytes in `events.payload` and
delete old `output.chunk` rows. That implementation is rejected because it
breaks both global sequence allocation and the append-only event log.

Adding a separate high-water table would prevent sequence reuse, but the event
history would still become mutable. The attachment table is a smaller
exception: retention deletes only non-projection bytes.

### Rejected permanent Base64 storage

Keeping `data_b64` in `events.payload` forever preserves append-only storage
but makes `storage.output_retention_days` ineffective. Base64 also adds about
one third to the stored byte count. SQLite therefore stores raw bytes as a
BLOB and Base64-encodes them only for event transport.

## Event contract

### Event type

`internal/event` adds:

```go
const TypeOutputChunk Type = "output.chunk"
```

The legacy `output` type remains readable. New writers stop emitting it after
the reader-first commit is available.

### Payload version 1

A live event or a retained replay row has this payload:

```json
{
  "version": 1,
  "offset": 0,
  "len": 12,
  "data_b64": "SGVsbG8gd29ybGQ="
}
```

The fields have these meanings:

- `version` is exactly `1`;
- `offset` is the zero-based byte offset in this session's redacted output
  stream;
- `len` is the decoded byte count and is in the range `1..32768`;
- `data_b64` is standard padded Base64 and decodes to exactly `len` bytes.

The stored event envelope omits `data_b64`:

```json
{"version":1,"offset":0,"len":12}
```

`events.payload` stores that metadata. `output_chunks.data` stores the bytes.
The Committer writes both in one SQLite transaction before it updates memory
or publishes the hydrated event.

After retention removes the attachment, replay returns the immutable metadata
without `data_b64`. Consumers treat that row as an expired output span. They
must not treat an absent body as an empty output chunk.

Offsets are per session. Global `seq` values can interleave chunks from
different sessions. For one session, each new offset equals the previous
offset plus the previous length.

### Event validation

`event.OutputChunkPayloadV1` provides explicit metadata and hydrated
validation:

- metadata validation accepts an omitted `data_b64`;
- hydrated validation requires valid Base64 and an exact decoded length;
- producers cannot create a zero-length or oversized chunk;
- unknown payload versions fail output decoding but do not change Agent
  projection recovery.

The event draft owns a copied byte slice. Callers cannot mutate bytes after
they create the draft.

### Reader-first compatibility

The first implementation commit adds all readers before any writer emits
`output.chunk`:

- `internal/session` recovery recognizes `output.chunk` as
  projection-neutral;
- `internal/store` migrates and hydrates output attachments;
- REST replay and the Go client preserve the existing `EventRow` JSON shape;
- `drove log` reads legacy lines, retained chunks, and expired chunk metadata;
- Web event types accept `output.chunk` and summarize offset and length
  without rendering Base64.

Once the writer commit emits `output.chunk`, the reader-first commit is the
lowest safe rollback version. Binaries before that commit reject the unknown
event type during bootstrap.

## PTY byte delivery

### Callback contract

`internal/pty.Config` changes its output callbacks to:

```go
OnOutput    func(chunk []byte, offset uint64)
OnOutputEnd func(offset uint64)
```

`OnOutput` receives a nonempty copied slice. `offset` identifies the first byte
in that slice. The first callback uses offset `0`.

`OnOutputEnd` runs exactly once after no further output callback can start. Its
offset is the total number of bytes delivered. It lets the session processor
flush an incomplete token prefix, an incomplete UTF-8 suffix at EOF, and the
final derived line.

`OnExit` and `OnOutputEnd` have no ordering guarantee. This preserves the
existing rule that final PTY output can follow a terminal process event.
`Session.Close` still waits for the read loop, the process wait, and every
callback.

### Chunk boundaries

The reader calls `Read` with a 32 KiB buffer and delivers available output
immediately. This version does not add a 16 ms coalescing timer. Immediate
delivery is smaller and satisfies the no-newline latency requirement.

The chunker applies these rules:

- no chunk exceeds 32 KiB;
- a valid multi-byte UTF-8 code point is not split between normal chunks;
- at most three bytes of an incomplete UTF-8 suffix wait for the next read;
- invalid UTF-8 bytes remain byte-exact and do not block later output;
- EOF flushes every remaining byte, including an incomplete final code point;
- concatenating all callback bytes reproduces the PTY byte stream exactly.

The callback offset counts raw PTY bytes. The session redactor preserves byte
length, so persisted offsets use the same positions.

### Read errors

EOF and the platform PTY close error end the stream normally. An unexpected
read error ends output delivery and still calls `OnOutputEnd`. Existing
process-exit handling remains the source of terminal Agent state.

## Session output processor

Each `runningSession` owns one output processor. Only the PTY read goroutine
feeds it, so the processor needs no broad mutex.

For each callback, the processor:

1. checks that the source offset matches the next expected offset;
2. redacts the signal token with state retained across callbacks;
3. splits redacted output into chunks no larger than 32 KiB;
4. commits the output chunk drafts as one Committer operation;
5. sends one output activity to the existing observation actor;
6. feeds the same redacted bytes to the derived line view;
7. sends any heuristic hints through the existing Detector path.

An offset mismatch is an internal stream error. The processor commits one
bounded error message without output bytes and triggers the existing fatal
Committer path if that error cannot be persisted.

When process exit wins first, later output still commits. The processor does
not send output activity or heuristic hints after `exitClaimed`.

### Cross-chunk token redaction

The old `strings.ReplaceAll` call cannot find a signal token split between
reads. The new redactor retains only the longest suffix that can still be a
prefix of the token. It emits every other byte immediately.

When the redactor finds the token, it writes a mask with the same byte length.
For normal generated tokens, the mask begins with `[REDACTED]` and uses `*`
for the remaining bytes. Length preservation keeps all later offsets stable.

`OnOutputEnd` flushes a partial, nonmatching prefix. Tests inspect persisted
events, Hub events, replay responses, errors, and logs to prove that the token
never appears.

### Derived line view

Raw chunks do not change the adapter heuristic interface in this phase.
`internal/term` adds a small stateful control-sequence stripper. It carries
incomplete CSI, OSC, DCS, SOS, PM, APC, and single-character escape sequences
across chunk boundaries.

The session line view:

- receives only redacted bytes;
- strips terminal control sequences incrementally;
- emits a line on LF and removes one trailing CR;
- emits the final partial line at output end;
- keeps at most 64 KiB for one unfinished line and retains the newest bytes
  when the limit is exceeded;
- calls the existing adapter heuristic with plain lines;
- reports output activity once for every committed PTY callback, including a
  callback without a complete line.

`adapter.Entry.Classify` keeps one-shot sanitization for callers that supply a
complete ANSI-decorated line. It reuses `internal/term` instead of maintaining
a second scanner.

This line view is compatibility behavior. It is not a screen model and cannot
reliably classify a full-screen TUI. Issue #14 replaces it as fallback evidence
with rendered screen rules.

## Storage model

### Schema version 2

The migration adds one attachment table:

```sql
CREATE TABLE output_chunks (
    event_seq INTEGER PRIMARY KEY
        REFERENCES events(seq) ON DELETE RESTRICT,
    data BLOB NOT NULL
);
```

The migration does not rewrite existing rows. Legacy `output` events remain in
`events`.

Store opening enables `foreign_keys=ON` and verifies the returned value. The
foreign key prevents an attachment from surviving without its event envelope.

`Store.AppendEvent` and `Store.AppendEvents` write an output envelope and its
attachment in one transaction. A partial insert cannot expose metadata without
the bytes or bytes without metadata.

`Store.ScanEvents` reads immutable event envelopes only. The recovery
projector ignores both `output` and `output.chunk` for Agent state. It returns
the greatest sequence from `events`, so attachment deletion cannot move the
Committer boundary.

`Store.Replay` left-joins `output_chunks` and returns rows in global sequence
order:

- retained chunks include hydrated `data_b64`;
- expired chunks keep version, offset, and length only;
- legacy output rows remain unchanged.

### Retention

Config adds:

```json
{
  "storage": {
    "output_retention_days": 30
  }
}
```

The value is a nonnegative integer:

- an omitted field defaults to `30`;
- `0` disables deletion and keeps output attachments indefinitely;
- a negative value fails config validation before daemon startup.

When retention is enabled, the daemon:

1. removes attachments older than the UTC cutoff after opening the Store and
   before projection recovery;
2. starts one retention loop after startup;
3. repeats cleanup every 24 hours;
4. stops and joins the loop before closing the Store.

The startup cleanup is mandatory. A startup cleanup error aborts startup. A
scheduled cleanup error writes a structured warning and retries on the next
interval. Tests inject the current time and the tick channel. They do not wait
on wall time.

Cleanup deletes only rows from `output_chunks`. It never deletes or updates an
`events` row. Agent state, signal evidence, lifecycle history, input audits,
error history, global sequence allocation, and recovery remain unchanged.

The Store enables `secure_delete=ON` and verifies the returned setting. After a
retention transaction, it runs `wal_checkpoint(TRUNCATE)`. This removes deleted
attachment content from reusable database cells and truncates committed WAL
frames. Drove does not run `VACUUM`; SQLite can reuse free pages, but the main
database file may keep its existing size.

### File permissions

New paths use private modes:

- Drove data directories use `0700`;
- a newly created SQLite database uses `0600`;
- the existing control token and session injection files remain `0600`.

At startup, the daemon inspects the data directory and database with `Lstat`.
It rejects non-directory and non-regular targets. If an existing path grants
group or other permissions, the daemon logs a structured warning with the path
and actual mode. It does not silently change an existing user's permissions.

`drove init`, `auth.Ensure`, CLI daemon-log setup, and session injection root
creation use `0700` for newly created data directories. Store opening creates a
missing database as a regular `0600` file before SQLite opens it.

## Replay behavior

`drove log <agent-id>` changes from an event timeline to output replay.

The default mode:

- writes retained output bytes to stdout in sequence order;
- writes no timestamps or lifecycle lines into the terminal byte stream;
- writes one newline after each legacy `output` row because legacy writers
  removed line endings;
- skips expired chunk bodies while preserving later retained bytes;
- applies no playback delay.

`drove log --plain <agent-id>` passes the same byte stream through the
stateful `internal/term` stripper. It removes control sequences but does not
apply cursor movement or render a screen. Issue #14 owns rendered plain text.

The REST replay endpoint continues returning every event envelope. Web readers
display `output.chunk offset=<n> len=<n>` and never place `data_b64` in the
visible event text. A future terminal component can decode the bytes.

## Concurrency and ordering

The existing global Committer remains the only sequence allocator.

For an output operation:

1. session builds copied chunk drafts;
2. Committer assigns consecutive global sequences;
3. Store inserts event envelopes and BLOB attachments in one transaction;
4. no Agent projection mutation is needed;
5. Hub publishes hydrated events in sequence order;
6. session sends derived Detector observations.

Store failure publishes no output and sends no derived observation. The
Committer enters its existing fatal state.

Retention uses the same single SQLite connection as writes. `database/sql`
serializes it with append transactions. Event envelopes are never part of the
retention delete, so a concurrent append cannot lose a sequence or output
metadata.

## Compatibility

### Existing databases

Schema version 1 migrates in place. Existing `output` rows remain readable and
are not subject to the new retention policy.

### Existing API readers

The event object shape does not change. Readers that use an open string for
`type` can ignore `output.chunk`. The checked TypeScript union receives the new
value in the reader-first commit.

### Rollback

Before the writer commit, rollback to `9e1745d` remains safe because the schema
migration only adds a table.

After the writer emits the first `output.chunk`, rollback cannot go below the
reader-first commit. Older recovery code rejects the new event type.

Retention deletion is intentionally irreversible for raw attachments. Event
metadata and every projection fact remain. Set
`storage.output_retention_days` to `0` before deployment when indefinite raw
replay is required.

## Failure handling

### An output attachment insert fails

The transaction rolls back both the event envelope and the attachment. The
Committer keeps the prior sequence and enters the existing fatal path.

### A token spans several reads

The streaming redactor keeps only a possible token prefix. It replaces a full
match before any matching byte enters an event draft.

### A process exits before the read loop drains

`OnExit` can record the terminal state first. The read loop still commits final
output and calls `OnOutputEnd`. Detector state remains terminal.

### Retention finds an attachment without a valid envelope

Foreign-key and append validation prevent new orphan rows. The retention query
joins `events` and deletes only attachments whose event type is
`output.chunk`. An inconsistent row causes an integrity error during replay or
maintenance and is not silently reclassified.

### A replay crosses the retention cutoff

Metadata exposes each missing offset and length. Replay continues with later
retained chunks. Raw `drove log` does not inject warning text into stdout.

### Scheduled cleanup fails

The daemon logs the error without stopping live Agents. The next scheduled
pass retries. The startup pass remains strict so a daemon never starts while
its configured cleanup is already known to fail.

## Verification

Focused tests cover:

- immediate delivery of `printf 'Allow? [y/n] '` without a newline;
- byte-exact round trips across short reads, 32 KiB boundaries, ANSI
  sequences, invalid bytes, and EOF;
- UTF-8 code points split across underlying reads but not output callbacks;
- one `OnOutputEnd` call and no callback after it;
- Close waiting for output and end callbacks;
- signal tokens split at every byte position across two or more chunks;
- length-preserving redaction and stable offsets;
- bounded derived lines and split control sequences;
- atomic event-envelope and BLOB attachment writes;
- Store failure before Hub publication or Detector observation;
- reader-first projection of seeded `output.chunk` rows;
- retained and expired replay rows;
- legacy `output` replay;
- config defaults, `0`, and negative rejection;
- startup and scheduled retention with an injected clock and tick channel;
- `secure_delete` verification and WAL truncation;
- projection recovery and sequence continuation after attachment deletion;
- private modes for newly created data directories and databases;
- warnings for existing broad modes;
- raw and `--plain` CLI output;
- Web reader summaries that omit Base64 text;
- concurrent output from multiple sessions under the race detector.

Manual acceptance uses an isolated data directory:

1. Start `/bin/sh -c "printf 'Allow? [y/n] '; sleep 1"` and confirm the first
   output event arrives within 50 ms on a local unloaded machine.
2. Capture a Claude and a Codex TUI stream, concatenate decoded chunks, and
   compare the result byte for byte with a direct PTY capture.
3. Split a multi-byte UTF-8 character across forced reads and confirm replay
   output contains the original bytes.
4. Split the session signal token at every boundary and search the SQLite
   database, WAL, Hub capture, replay response, and daemon log for the token.
5. Seed old and new output with a fake clock, prune one-day-old attachments,
   restart the daemon, and confirm `drove ps` and the next global sequence.
6. Confirm `drove log` can still replay a schema version 1 database that
   contains only legacy `output` rows.

The full gate is:

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

## Risks and controls

### Raw output increases storage and WebSocket volume

The writer caps chunks at 32 KiB and stores bytes as BLOBs instead of Base64.
The existing Hub can drop slow subscribers. Issue #19 owns per-session stream
subscriptions and stronger backpressure.

### Retention weakens full historical replay

The default is explicit and configurable. Immutable event metadata preserves
offsets, ordering, and projection history after the bytes expire. A value of
`0` keeps all output.

### Stream redaction could shift offsets

The mask has exactly the token's byte length. Tests split the token at every
position and assert offset continuity.

### The derived line buffer could grow without a newline

It keeps only the newest 64 KiB. Output activity still reaches the observer for
every committed PTY callback. Issue #14 removes reliance on line boundaries
for TUI state detection.

### Cleanup can block writes

The Store already uses one SQLite connection. Cleanup is one bounded delete
transaction followed by a WAL checkpoint. It runs at startup and once per day,
not per append.

## Fragile assumption

This design assumes Drove can redact the current per-session signal token
without changing byte length. If a future secret requires variable-length
replacement, PTY source offsets and persisted replay offsets must become
separate fields. The current design prevents that hidden change by testing that
every redacted output chunk preserves its source span length.

## Approval gate

Implementation starts only after the owner approves this specification,
`checklist.md`, and `tasks.md`.
