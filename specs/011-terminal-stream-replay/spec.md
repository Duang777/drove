# Terminal stream and replay timeline

Status: Approved

Approved: 2026-10-04

Owner direction: agent-2 owns Issues #19 and #25 as one delivery batch. The
terminal stream lands first, followed by timeline and frame reads over the same
cursor and resize contract.

Related issues:

- [#19](https://github.com/Duang777/drove/issues/19), WebSocket terminal stream
- [#25](https://github.com/Duang777/drove/issues/25), replay timeline and frames
- [#14](https://github.com/Duang777/drove/issues/14), terminal screen model
- [#20](https://github.com/Duang777/drove/issues/20), attach and Web playback
- [#21](https://github.com/Duang777/drove/issues/21), control-plane security
- [#26](https://github.com/Duang777/drove/issues/26), tower grid snapshots

## Problem

Drove persists redacted PTY output as immutable `output.chunk` events, but its
current WebSocket sends a lossy global event stream and its replay endpoint
loads an entire session. That is insufficient for terminal attach, reconnect,
resize replay, or random frame reads.

The new paths must preserve four existing invariants:

1. the global Committer remains the only runtime sequence writer;
2. bytes become visible only after SQLite commits them;
3. terminal queries and their replies never enter output or input audit;
4. screen text remains ephemeral and is never persisted.

## Goals

- Add an opt-in WebSocket v2 protocol for per-agent raw output and event
  subscriptions.
- Resume from a canonical sequence and byte-offset cursor without gaps or
  duplicates.
- Persist successful terminal resizes in the same per-session order as output.
- Isolate slow clients with a byte-bounded connection queue and a precise
  reconnect cursor.
- Provide a coalescing, at-most-2-Hz live screen channel for the tower grid.
- Derive state timelines and Blocked occurrences from immutable events.
- Return exact bounded terminal frames selected by sequence, time, or output
  offset.
- Keep timeline data available when output attachments expire and return a
  typed `output_expired` error when a frame needs missing bytes.

## Non-goals

- Implement `drove attach`, the Bubble Tea UI, xterm rendering, or the tower
  grid.
- Persist rendered screens or a second timeline projection.
- Implement Issue #17 `output.gap`.
- Add automatic approval or any vendor-specific behavior outside
  `internal/adapter`.
- Claim that a visible `term.Snapshot` can restore an x/vt emulator.

## Architectural decision

Three designs were compared:

1. a per-session committed-output broker registered inside the Committer;
2. replacement of output and terminal actors with one large runtime actor;
3. SQLite range-and-tail reads awakened by a discardable commit clock.

The third design is the base. Both historical and live delivery read the same
immutable rows, so correctness does not depend on an in-memory byte broker or
an atomic Store-to-Hub handoff. The selected design adds these constraints
from the other candidates:

- `latest` size ownership changes only after successful attached input; the
  first attachment is the initial owner and owner detach selects the most
  recently active remaining attachment;
- live snapshots use a capacity-one latest-value path outside the event Hub;
- frames cache only exact completed answers;
- no-subprotocol WebSocket behavior remains unchanged.

SQLite reads must be short and bounded. A stream never holds a database cursor
while waiting for a network writer.

## Canonical cursor

All new stream and replay APIs use:

```go
type Cursor struct {
    Seq        uint64
    NextOffset uint64
}
```

`Seq` is the last session event consumed. `NextOffset` is the first persisted
output byte not consumed. Sequence is global and can be sparse within a
session. Offset is local to one session.

Rules:

- `{0, 0}` is before the recording.
- `output.chunk` stores an inclusive start offset and advances `NextOffset` by
  its decoded length.
- non-output records advance `Seq` without changing `NextOffset`.
- an offset selector may start inside a retained output chunk; the first
  binary frame contains only the requested suffix.
- a supplied full cursor must match the immutable event log. The server never
  repairs a mixed sequence/offset pair.
- v2 JSON encodes sequence and offset values as canonical unsigned decimal
  strings so browsers do not lose values above `2^53-1`.

## Durable resize event

Add event type `agent.resized` and version 1 payload:

```json
{
  "version": 1,
  "rows": 50,
  "columns": 160,
  "output_offset": 98304
}
```

The event records only an effective size that the PTY and emulator both
accepted. Attachment IDs are ephemeral and never persisted. Old recordings
without resize events start at 40 rows by 120 columns.

The event reader lands before the first writer. Recovery treats resize as a
known non-state event.

## Per-session ordering

The existing output processor becomes a channel-owned recording actor. It
serializes:

- source-offset validation and token redaction;
- output event commit followed by terminal feed;
- effective PTY and emulator resize followed by resize event commit;
- attachment registration, input-driven owner promotion, and detach fallback;
- output end.

The terminal actor remains the only x/vt owner. The recording actor calls it
for committed feed, resize, and snapshot reads. The Committer never calls back
into either actor.

Output ordering:

1. validate and redact one PTY callback;
2. commit one or more contiguous `output.chunk` events;
3. feed exactly those committed bytes to the terminal actor;
4. advance the session cursor;
5. submit output activity to the detector.

Resize ordering:

1. select the effective size under the configured policy;
2. resize the PTY, then x/vt through the terminal actor;
3. commit `agent.resized` at the current output offset;
4. advance the session cursor;
5. acknowledge the client.

If a post-apply resize commit fails, or committed output cannot reach x/vt,
the existing fail-stop path terminates the daemon. Continuing would make live
and replay state disagree.

Manager registry locks are never held while waiting for an actor, SQLite,
the PTY, a subscriber, or a WebSocket.

## Size arbitration

Version 2 terminal subscriptions may register a viewport. The default policy
is `latest`.

- The first writable attachment is the initial owner.
- A non-owner resize updates only its proposed size.
- Successful input promotes that attachment and applies its proposed size
  before writing the input.
- Detaching the owner selects the remaining attachment with the greatest
  actor-issued activity ticket.
- Read-only and snapshot-only subscriptions do not own size.
- Reapplying the current effective size writes no event.

The internal policy type may reserve `smallest`, but this phase exposes only
the default `latest` behavior unless configuration already has a stable owner.

## Store range reads and commit clock

`internal/store` adds bounded queries that:

- capture the latest durable global sequence;
- resolve session cursors by sequence, time, or output offset;
- read session event pages within `(after_seq, through_seq]`;
- optionally hydrate output attachments;
- report attachment presence explicitly;
- cap both row count and attachment bytes.

The Store keeps the existing append-only schema. Offset lookup may use
validated `output.chunk` JSON metadata because a 50 MiB session has roughly
1,600 maximum-sized chunks; a new mutable projection is not justified.

The Committer advances a process-local commit clock after SQLite success. The
clock contains only a global high watermark and a collapsed wakeup channel.
It is not a data source. A tail always re-reads SQLite through the observed
watermark, so collapsed or delayed notifications cannot lose records.

The tail loop is:

1. capture global durable high watermark `H`;
2. read bounded session pages after the private scan sequence through `H`;
3. emit matching records in sequence order;
4. advance the private scan sequence to `H`, including when no row matched;
5. re-read the high watermark;
6. repeat immediately if it advanced, otherwise wait for the next change.

## Recording package

Add `internal/recording` with its own `AGENTS.md`.

The package owns:

- cursor parsing, validation, and selector resolution;
- raw and event range-and-tail iterators;
- output retention holes;
- state span and Blocked occurrence projection;
- exact terminal frame reconstruction;
- bounded exact-result frame caching.

It depends on `agent`, `event`, `store`, and `term`. It does not depend on
`session`, `pty`, `adapter`, `detect`, `api`, or the event Hub.

`session.Manager` owns live attachments and the commit clock. API composition
uses a small façade that combines the recording archive with current
attachment state; SQL rows and transport DTOs do not escape their packages.

## WebSocket protocol

### Negotiation

- No subprotocol selects the existing v1 handler unchanged.
- `Sec-WebSocket-Protocol: drove.v2` selects v2 and is echoed by the server.
- An unsupported explicit subprotocol is rejected before upgrade.
- Authentication, Host checks, Origin checks, and future cookie/CSRF handling
  run before protocol dispatch.

The v1 path retains its exact hello, global event stream, input validation,
ack/error objects, ping/pong, and close behavior. New `agent.resized` events
may appear as ordinary event envelopes.

### Version 2 text messages

Client commands are strict JSON objects with `version`, `type`, and a
connection-unique `request_id`.

Supported commands:

- `subscribe` with one agent, one mode (`raw`, `events`, or `snapshot`), and
  an optional cursor/sequence/offset selector;
- `unsubscribe`;
- `input`, tied to a writable raw subscription;
- `resize`, tied to a writable raw subscription.

Server messages:

- `hello`, `subscribed`, `unsubscribed`, `ack`, `error`;
- `event`, `resized`, `snapshot`, `caught_up`.

One connection may subscribe to multiple agents but only once per
`agent_id + mode`. Snapshot messages carry a cursor and
`restorable:false`.

### Binary raw frame

Each binary message contains one retained `output.chunk`, or its requested
suffix. Integers use network byte order.

```text
offset  size  field
0       4     ASCII "DRV2"
4       1     kind = 1 (output)
5       1     flags; bit 0 means historical
6       2     agent_id byte length N
8       8     event sequence
16      8     inclusive start output offset
24      N     ASCII agent_id
24+N    rest  raw terminal bytes, 1..32768 bytes
```

The cursor after the frame is `{seq, start_offset + payload_length}`.

### Backpressure

Each v2 connection owns one byte-accounted outbound queue with an 8 MiB data
budget and a reserved terminal-control slot. Producers never wait for socket
I/O.

On overflow:

1. cancel all subscriptions on that connection;
2. discard queued but unwritten data;
3. best-effort send `slow_consumer` with each subscription's last
   successfully written cursor;
4. close with WebSocket code 1013.

The writer, not the producer, advances resume cursors after a successful
`WriteMessage`.

## Snapshot stream

Snapshot mode is live-only. It reads the terminal actor's immutable bounded
snapshot at most twice per second. A capacity-one channel replaces an unread
snapshot with the newer value. This intentional coalescing is allowed because
snapshots are display previews, not replay records.

Every snapshot includes the current cursor, terminal size, bounded rows,
truncation flag, and `restorable:false`. It is not published to the event Hub
and is unavailable after detach.

## Timeline

`Timeline` is a pure projection over immutable event envelopes captured
through one sequence boundary.

Each half-open state span contains state, start and end positions, timestamps,
source, and optional screen rule. Each entry into Blocked receives a one-based
ordinal. The final live span has no end. A terminal span ends at the last
session event timestamp.

The timeline also reports:

- captured head cursor;
- full output offset range;
- retained range and coalesced missing attachment ranges;
- all Blocked entries.

The Blocked lookup returns its span and a jump cursor at or before 30 seconds
before entry. It remains available when output expired, while marking whether
the jump frame is reconstructable.

## Frame resolution and rendering

The frame endpoint requires exactly one selector:

- `seq=X`: apply terminal-affecting events with sequence at most X;
- `at=T`: choose the greatest event in `(timestamp, seq)` order at or before T,
  then apply its sequence prefix;
- `offset=O`: apply output bytes in `[0,O)` and resize events recorded at
  output offsets at most O.

Rendering creates a replay-only `term.Controller` at 40x120, discards terminal
query replies, applies output and resize records in sequence order, and
returns the existing bounded `Snapshot.View`.

The pinned x/vt exposes no clone, marshal, or restore operation. Its private
state includes parser state, modes, dual screens, saved cursors, character
sets, tab stops, and partial graphemes. Therefore a visible `term.Snapshot`
cannot be an exact replay keyframe.

This phase uses:

- an LRU of completed frames keyed by session, resolved cursor, view bounds,
  and retention generation;
- from-origin replay for an uncached target.

It does not label snapshots as keyframes. The 50 MiB random-frame benchmark
records cold and warm p95 separately. If cold p95 exceeds 300 ms, the issue
report must state that a full x/vt checkpoint capability is required; the
implementation must not trade correctness for that target.

## REST and CLI

Add authenticated endpoints:

```text
GET /api/v1/agents/{id}/timeline
GET /api/v1/agents/{id}/timeline/blocked/{number}
GET /api/v1/agents/{id}/frame?seq=...|at=...|offset=...
```

Unknown sessions return 404. Invalid selectors return 400. Missing required
output returns HTTP 410 with code `output_expired` and missing byte ranges.
Corrupt known event payloads return 500; no partial frame is returned.

Add matching `internal/client` methods and `drove timeline <id> [--json]`.
The text form prints state spans and numbered Blocked entries. The CLI does
not implement terminal playback in this phase.

## Compatibility and privacy

- Existing event and replay readers accept `agent.resized` before writers emit
  it.
- v1 WebSocket and `GET /events` response shapes remain unchanged.
- `output.chunk` remains token-redacted before persistence and streaming.
- Raw v2 output contains only bytes already stored in `output_chunks`.
- Frames and snapshots contain only the existing bounded visible view.
- Frame caches and live snapshot channels are memory-only and close on daemon
  shutdown.
- Retention invalidates cached frames before deleted bytes can be returned.
- Input audit records only byte counts.

## Acceptance

- History followed by live raw output equals retained output exactly for every
  tested chunk split and reconnect cursor.
- Starting inside a chunk returns the correct suffix.
- A slow connection closes with code 1013 and a last-written resume cursor;
  another connection and the Committer continue.
- v1 tests remain byte-shape compatible.
- Two clients demonstrate first-owner, non-owner proposal, input promotion,
  and owner-detach fallback. PTY size, x/vt size, and replayed size agree.
- Snapshot subscriptions never exceed 2 Hz, coalesce unread values, and never
  enter the Hub or Store.
- A fixture with at least three Blocked intervals projects correct ordinals,
  times, durations, evidence, and jump cursors.
- Frames selected by sequence, time, and offset match from-origin golden
  replay under fragmented UTF-8, control sequences, alternate screen, and
  resize.
- Missing attachments return `output_expired`; timeline still succeeds.
- The 50 MiB benchmark records cold and warm random-frame p95 without a false
  performance claim.
- Focused 20-round race tests, full race, vet, Go builds, Web typecheck, Web
  build, and `git diff --check` pass.
