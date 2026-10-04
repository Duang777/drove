# Terminal screen detection and explainability

Status: Approved for implementation

Approved: 2026-10-04

Owner direction: complete an approval-gated design for Issue #14 before
changing product code.

Related issues:

- [#14](https://github.com/Duang777/drove/issues/14), terminal emulation,
  terminal query replies, screen rules, and state explanations;
- [#19](https://github.com/Duang777/drove/issues/19), terminal stream protocol
  and backpressure;
- [#20](https://github.com/Duang777/drove/issues/20), attach, live resize, and
  terminal UI;
- [#24](https://github.com/Duang777/drove/issues/24), optional persistent hook
  installation.

This specification revises the active-hook and heuristic rules in
[Spec 006](../006-hook-backed-state-detection/spec.md). It builds on the raw
PTY byte stream and retention contract in
[Spec 009](../009-raw-output-chunks/spec.md).

Implementation baseline: `e0d4326`.

## Problem

Drove now persists redacted PTY bytes without waiting for newlines, but it
still interprets agent state from derived text lines. That is not a reliable
model for a full-screen terminal application.

The current design has four concrete failures:

1. Codex emits terminal queries during TUI startup. Drove does not answer
   them, so Codex can stop before rendering its first usable screen.
2. Line heuristics cannot observe cursor movement, overwritten prompts, or a
   prompt that disappears without producing another complete line.
3. Claude and Codex treat ordinary output containing `Error` as possible
   Blocked evidence. This is a known false positive.
4. Once hooks become active, Spec 006 suppresses every heuristic state change.
   A visible approval prompt can disappear without a matching post-tool hook,
   and a Claude interrupt can leave the session in Working.

Drove also has no bounded command that explains why a state changed, which
candidate was suppressed, or what an attached agent currently displays.

## Goal

This change makes rendered terminal state a first-class, auditable signal:

1. Emulate one terminal screen for each attached PTY session from committed,
   token-redacted output bytes.
2. Start the PTY at the same fixed dimensions as the emulator.
3. Answer the terminal queries required by recorded Claude and Codex startup
   streams without recording replies as user input.
4. Replace adapter line heuristics with adapter-owned declarative screen
   rules.
5. Preserve process facts as the highest state authority.
6. Permit exactly two screen-driven transitions while hooks are active:
   approval resolution from Blocked to Working, and Claude interruption from
   Working to Idle.
7. Record all other active-hook screen candidates as suppressed durable
   evidence.
8. Use screen rules for Blocked, Working recovery, and visible Idle evidence
   when hooks are off or in fallback.
9. Add `GET /api/v1/agents/{id}/explain` and `drove explain <id>`.
10. Replay real redacted Claude and Codex byte fixtures and record a
    32-session, 1 MiB/s-per-session benchmark.

## Non-goals

This change does not:

- add a terminal attach protocol, Web terminal, or multi-client flow control;
- expose a public live-resize endpoint or CLI command;
- implement Issue #19 or Issue #20;
- reconnect a PTY or restore an emulator after daemon restart;
- install, edit, or remove persistent vendor hook configuration;
- change oneshot completion or process-exit semantics;
- infer Done from screen text, hooks, interrupts, or silence;
- persist terminal rows, screen excerpts, screen titles, styles, links,
  clipboard content, scrollback, or screen hashes;
- publish a screen snapshot through the event Hub or WebSocket stream;
- capture terminal query replies as output or `agent.input`;
- add general-purpose secret detection;
- expose x/vt, x/ansi, or ultraviolet types outside `internal/term`;
- promise support for every terminal protocol extension.

Issue #20 owns the public resize transport. This phase adds an internal resize
operation only so PTY and emulator ordering can be tested.

## Revision to Spec 006

This specification replaces only the following Spec 006 rules.

Spec 006 states that an active hook permanently suppresses every heuristic
state change. After this change, `SourceScreen` is not treated as the old
line-based `SourceHeuristic`. Under `hook_active`, Detector permits only:

- `KindHumanInputResolved` from a cleared approval screen rule to confirm
  `Blocked -> Working` after 500 milliseconds;
- `KindInterrupted` from the Claude screen classifier to confirm
  `Working -> Idle` after one second.

Every other screen signal under `hook_active` remains durable with outcome
`suppressed`. The adapter cannot grant authority. Detector derives authority
from source, kind, current Agent state, and hook status.

The old adapter `Heuristic.Classify(line)` path and Blocked recovery based on
two non-Blocked lines are removed. When hook policy is `off`, or `auto` has
entered `fallback`, screen rule edges provide visible Blocked, Working
recovery, and Idle evidence. Existing output activity and fallback silence
remain liveness evidence. They do not inspect line text.

All other Spec 006 contracts remain:

- process facts outrank every hook, screen, and timer candidate;
- a successful natural oneshot process exit is the only live source of Done;
- the observation actor remains the only Detector owner;
- the global Committer remains the only runtime event writer;
- state changes remain durable before Agent and Detector projections change;
- terminal output after process exit remains durable but cannot reopen state.

## Evidence and dependency decision

Issue #14 contains a recorded Codex startup stream with DSR, OSC color,
keyboard-mode, and device-attribute queries. A local dependency probe confirmed
that the selected x/vt revision:

- answers DSR, OSC 10/11, and primary device attributes;
- exposes a handler for the missing Kitty keyboard query;
- uses a reply pipe that must be drained before the first emulator write;
- fixes a DSR cursor-coordinate defect present in the tested Go-1.23-compatible
  historical revision.

Drove will pin:

```text
github.com/charmbracelet/x/vt v0.0.0-20261004011457-ad85c59fdf4e
```

That revision requires Go 1.24.2. The implementation therefore:

- changes `go.mod` to `go 1.24.2`;
- replaces the Go 1.23 CI lane with a Go 1.24.2 minimum-version lane;
- keeps a second CI lane on the project's current stable Go release;
- updates documented development prerequisites.

The historical x/vt revision is rejected. Avoiding the toolchain change would
make Drove own broad DSR, OSC, keyboard, and device-attribute compatibility on
top of an older API.

## Chosen architecture

Each attached session gains a dedicated terminal actor. The existing
observation actor and global Committer keep their current ownership.

```text
PTY output bytes
    |
    v
session output processor
    |  validate source offset
    |  redact signal token
    |  commit output.chunk
    v
global Committer
    |  SQLite -> projection -> Hub
    v
typed committed chunk
    |
    v
per-session terminal actor
    |  x/vt controller
    |  immediate query replies -> pty.Session.Write
    |  fixed-window screen sampling
    |  adapter screen classifier
    v
screen rule edges
    |
    v
existing observation actor -> Detector -> global Committer

Explain:
Store tail of signal/state events + attached actor's ephemeral snapshot
```

The terminal actor exists because a final short screen update needs a real
timer even while the interactive process remains open. A synchronous parser
cannot both cap classification near 10 Hz and guarantee that final update.

The design does not add a PTY writer actor. `pty.Session.Write` already holds a
mutex across one complete `writeFull` operation. User input and terminal
replies therefore cannot interleave bytes within one frame.

## Ownership

| Mutable resource | Sole owner |
| --- | --- |
| x/vt emulator mutation | per-session terminal actor |
| adapter rule-edge memory | actor-owned classifier instance |
| screen dirty state and 100 ms sample timer | terminal actor |
| current immutable terminal snapshot | terminal actor |
| x/vt reply pipe reads | controller reply pump |
| complete PTY write frames | `pty.Session.Write` mutex |
| Detector state and confirmation candidates | observation actor |
| real Detector timer | observation actor |
| Agent and Detector committed projections | global Committer |
| global event sequence and SQLite appends | global Committer |
| explain history | immutable event log, with no second projection |

`internal/term` does not import `adapter`, `detect`, `event`, or `session`.
`internal/adapter` does not own state authority. `internal/detect` does not
import x/vt or vendor packages. `internal/api` and `internal/client` remain
transport layers over `session.Manager`.

## Caller view

The output processor performs one terminal operation only after its output
batch is durable:

```go
receipt, err := p.commitOutput(redacted)
if err != nil {
	return err
}
return p.terminal.FeedCommitted(ctx, term.CommittedChunk{
	Bytes:        redacted,
	OutputOffset: receipt.OutputOffset,
	LastSeq:      receipt.LastSeq,
	CommittedAt:  receipt.Timestamp,
})
```

The terminal actor exposes one data operation, one read operation, and
independent lifecycle facts:

```go
FeedCommitted(context.Context, term.CommittedChunk) error
Snapshot() (term.Snapshot, bool)
MarkProcessExited()
EndOutput(context.Context, finalOffset uint64) error
Resize(context.Context, term.Size) error
Close() error
```

`FeedCommitted` owns feed, reply generation, dirty tracking, sampling,
classification, and observation delivery. Callers do not orchestrate those
stages.

`Manager` adds one domain operation:

```go
Explain(context.Context, agent.ID, ExplainOptions) (Explanation, error)
```

## Terminal controller

`internal/term.Controller` is a deep wrapper around the pinned x/vt revision.
It owns:

- emulator construction and mutation;
- a mandatory reply pipe and reply pump;
- the reply-pump readiness barrier;
- a supplemental `CSI ? u` handler that returns `CSI ? 0 u`;
- screen-cell normalization;
- immutable snapshot construction;
- emulator resize ordering;
- close and reply-drain behavior.

The reply pump starts and reports ready before the controller accepts its first
write. This is mandatory because x/vt's reply pipe is unbuffered. Starting the
pump after `Write` can deadlock on a query.

The pump reads only x/vt reply bytes. Its sink calls `pty.Session.Write`
directly. It never calls `Manager.SendInput`, so a reply cannot create an
`agent.input` event. The reply path does not feed those bytes into the
emulator or create output, Store, Hub, log, or explain records. If a child
process explicitly echoes input, those child-emitted bytes remain ordinary PTY
output and follow the committed-output path.

The pinned x/vt behavior is verified with exact byte tests for:

- DSR cursor-position query `CSI 6 n`;
- OSC 10 and OSC 11 color queries;
- primary device attributes `CSI c`;
- Kitty keyboard-mode query `CSI ? u`.

After process exit, the pump continues draining and discarding replies until
controller close. A PTY sink error such as `ErrClosed` does not stop the
drain. Otherwise a trailing query can block x/vt while output is draining.

## Terminal size and startup

The initial PTY and emulator size is 40 rows by 120 columns.

`internal/pty` uses `creack/pty.StartWithSize`; it does not start the process
and resize afterward. A child process must observe 40x120 on its first
terminal-size query.

Startup uses this order:

1. Validate the request and register the Agent, observation actor, pending
   session, and readiness gates.
2. Start the already-sized PTY with fixed callbacks.
3. Attach the process and construct the terminal actor and controller.
4. Durably commit `process_started` and `Starting -> Working`.
5. Open `signalReady`.
6. Open `callbacksReady`.
7. Wait for required-hook activation when configured.

Opening `callbacksReady` before the required-hook wait lets startup output
reach x/vt and lets Codex receive query replies. Screen state changes remain
subject to the configured hook status in Detector.

An internal resize applies the PTY size and emulator size in actor order. A
failure reports an error and does not claim both sides are synchronized. This
operation is tested but is not connected to an API in this phase.

## Committed output and sampling

`term.CommittedChunk` contains copied redacted bytes, the output stream offset,
the final durable output sequence for the committed batch, and commit time.
No unredacted or uncommitted byte may enter the controller.

The terminal actor uses a fixed 100 millisecond sampling window:

1. The first dirty committed chunk arms `now + 100ms`.
2. Later chunks mark the screen dirty but do not move that deadline.
3. At the deadline, the actor captures one normalized immutable snapshot.
4. It publishes that snapshot for attached explain calls.
5. It runs the adapter classifier and emits only rule edges.
6. If output became dirty after capture, it arms the next fixed window.

Terminal query replies are generated during the controller write. They do not
wait for screen sampling.

The actor inbox is bounded. `FeedCommitted` applies backpressure to the PTY
output callback rather than dropping committed bytes, query processing, rule
edges, or durable suppression evidence.

`EndOutput` immediately samples a final dirty screen instead of waiting for
the timer. It then closes the controller after all reply work drains.

Process exit calls `MarkProcessExited` before the process fact terminates
Detector. The mark fences new screen observations. Trailing output remains
durable and still updates the emulator while the PTY read loop drains, but it
cannot produce a new state decision. An exited session is detached for explain
purposes even while that private drain is finishing.

## Adapter screen classifiers

Each `adapter.Entry` constructs one stateful classifier:

```go
func (e Entry) NewScreenClassifier() (*ScreenClassifier, error)
func (c *ScreenClassifier) Observe(term.Snapshot) []ScreenHint
func (c *ScreenClassifier) Rebaseline(term.Snapshot)
```

`ScreenClassifier` owns private declarative rule definitions, compiled
matchers, region selection, and prior match state. `Observe` emits only an
edge when a rule changes from absent to present or present to absent.

`ScreenHint` contains:

- a vendor-neutral `detect.Kind`;
- a stable rule name;
- edge `present` or `cleared`;
- a stable bounded region identifier;
- static adapter-authored evidence;
- confidence;
- confirmation duration.

Regexes, literals, captures, and matching stages remain private to
`internal/adapter`. Session code never branches on a vendor name or screen
pattern.

The duration is declarative stability metadata, not authority. Detector
accepts only the fixed duration for the matching row in its authority table;
an adapter cannot shorten, skip, or add a confirmation window.

The initial stable rules are:

| Adapter | Stable rule | Meaning |
| --- | --- | --- |
| Claude | `claude.approval_prompt` | approval present or cleared |
| Claude | `claude.idle_prompt` | visible idle prompt |
| Claude | `claude.interrupted` | visible interrupt result |
| Codex | `codex.approval_prompt` | approval present or cleared |
| Codex | `codex.idle_prompt` | visible idle prompt |

Only the Claude adapter emits `KindInterrupted`. The generic adapter has an
empty classifier.

Recorded, redacted byte fixtures are the authority for the initial patterns.
Tests cover true matches, near misses, cursor rewrites, fragmented writes, and
ordinary output containing `Error`.

The existing `Heuristic`, `OutputHint`, `Entry.Classify`, vendor line
classifiers, and session derived-line classification are deleted in the writer
activation commit. `term.Stripper` remains for `drove log --plain`.

## Detector authority and timers

`internal/detect` adds `SourceScreen`. A screen signal carries one validated
screen attribution value with stable rule, edge, region, output offset, final
output sequence, and static evidence.

Detector candidate state becomes keyed by bounded purpose and rule identity.
This prevents an approval-clear candidate, hook permission candidate, idle
candidate, activation timeout, and fallback-silence candidate from replacing
one another accidentally.

The observation actor still owns one physical timer. Detector returns the
earliest deadline plus a timer reference containing purpose, rule identity,
and generation. A stale firing is durably recorded and cannot consume a newer
or unrelated candidate.

The authority table is:

| Hook status | Screen signal | Decision |
| --- | --- | --- |
| any | after terminal process fact | `terminal` |
| `hook_active` | approval `cleared`, current Blocked | 500 ms Working candidate |
| `hook_active` | Claude interrupted, current Working | 1 s Idle candidate |
| `hook_active` | every other screen edge | `suppressed` |
| `awaiting_hook` or `required_failed` | every screen edge | `suppressed` or `terminal` |
| `off` or `fallback` | approval `present` | 750 ms Blocked candidate |
| `off` or `fallback` | approval `cleared`, current Blocked | 500 ms Working candidate |
| `off` or `fallback` | idle or interrupt `present` | 1 s Idle candidate |

An opposing edge, authoritative hook activity, process fact, or incompatible
state cancels the relevant candidate. Cancellation does not cancel an
unrelated candidate.

Screen rules never produce Done. Screen confidence does not bypass the table.

## Durable event contract

Existing payload versions remain readable. New screen writers use signal
payload version 3 and state evidence version 3.

### Signal payload version 3

A screen candidate or suppression uses:

```json
{
  "version": 3,
  "source": "screen",
  "kind": "human_input_resolved",
  "vendor": "claude",
  "vendor_event": "screen_rule",
  "scope": "root",
  "confidence": 1,
  "received_at": "2026-10-04T10:00:00Z",
  "outcome": "candidate",
  "screen": {
    "rule": "claude.approval_prompt",
    "edge": "cleared",
    "region": "viewport.bottom",
    "output_offset": 4312,
    "last_output_seq": 918,
    "evidence": "approval prompt"
  }
}
```

`output_offset` is the exclusive redacted output offset represented by the
sample. `last_output_seq` is the final durable `output.chunk` sequence applied
before capture.

Screen payloads contain no matched text, row content, regex, capture, title,
hash, or reversible fingerprint.

### State evidence version 3

A transition derived from a confirmed screen candidate copies the same typed
screen attribution:

```json
{
  "version": 3,
  "source": "screen",
  "event": "claude.approval_prompt",
  "confidence": 1,
  "screen": {
    "rule": "claude.approval_prompt",
    "edge": "cleared",
    "region": "viewport.bottom",
    "output_offset": 4312,
    "last_output_seq": 918,
    "evidence": "approval prompt"
  }
}
```

The attribution is created once in the normalized screen signal and copied
into signal audit and transition evidence. Session code does not reconstruct
it from strings.

Signal version 3 validation requires screen attribution for `source=screen`
and rejects it for unrelated sources. State evidence version 3 applies the
same bounded validation.

Recovery remains tolerant of unknown supplemental evidence:

- an unknown `agent.signal` payload version is counted and skipped because the
  event is projection-neutral;
- an unknown state evidence version is counted and the authoritative state
  columns still apply;
- malformed known versions remain errors.

Once the first version 3 signal is written, the reader-first event commit is
the database rollback floor.

## Explain contract

`Manager.Explain` reads a bounded durable tail containing only
`agent.signal` and `state_changed` events. It does not maintain another
projection.

The default limit is 50 relevant events. The API accepts a positive `limit`
up to 200. Store access filters and limits in SQLite so explain does not load
an entire output history or hydrate output attachments.

The response contains:

```json
{
  "agent_id": "agent-id",
  "state": "blocked",
  "hook_status": "hook_active",
  "attached": true,
  "events": [
    {
      "seq": 918,
      "timestamp": "2026-10-04T10:00:00Z",
      "type": "agent.signal",
      "source": "screen",
      "kind": "human_input_resolved",
      "outcome": "candidate",
      "rule": "claude.approval_prompt",
      "edge": "cleared",
      "region": "viewport.bottom",
      "evidence": "approval prompt"
    }
  ],
  "screen": {
    "captured_at": "2026-10-04T10:00:00Z",
    "rows": ["..."],
    "truncated": false
  }
}
```

The exact response uses typed Go fields rather than forwarding raw payload
JSON. Unknown historical evidence remains visible as a bounded event with its
sequence, type, and unsupported-version marker.

`GET /api/v1/agents/{id}/explain` is loopback-protected by the existing API
server contract. `internal/api` parses the limit and maps errors, while
`internal/client` remains transport-only.

`drove explain <id>` prints newest-relevant durable decisions in chronological
order, including source, kind, outcome, stable rule, suppression reason, and
state transition. It prints the current screen only when attached and labels
it `ephemeral redacted current screen`. `--json` returns the typed response.

## Snapshot privacy

The explain snapshot is an ephemeral view of the current attached screen:

- at most the bottom 12 visible rows;
- at most 160 terminal cells per row;
- at most 4 KiB after UTF-8 encoding;
- no title, scrollback, styles, links, clipboard data, or reply bytes;
- no persistence, daemon logging, Hub publication, or WebSocket publication;
- removed from availability as soon as the session detaches.

The emulator consumes the existing signal-token-redacted stream. That
redaction is not a general secret scanner. The API and CLI label the snapshot
accordingly instead of claiming that arbitrary terminal secrets are removed.

Durable explain evidence uses stable adapter-authored metadata only. Drove does
not store a screen excerpt or hash to explain historical decisions.

## Concurrency and ordering

The committed-output path is:

1. PTY callback delivers a copied byte chunk and source offset.
2. The output processor validates offset and replaces the signal token.
3. The global Committer appends `output.chunk` envelopes and attachments.
4. The Committer publishes the hydrated output events.
5. The output processor submits one typed committed chunk to the terminal
   actor.
6. The controller applies bytes and writes any reply through the PTY write
   lock.
7. A sample deadline captures and classifies the current screen.
8. Rule edges enter the existing observation actor.
9. Detector produces signal audit and an optional Agent change.
10. The global Committer persists, applies, and publishes that decision.

This preserves raw-output-first evidence. A screen event can reference only an
output offset and event sequence that are already durable.

The terminal actor never reads mutable Detector state. The observation actor
never reads x/vt state. Hook and process observations do not wait behind x/vt
emulation or terminal reply writes.

## Failure handling

### Controller creation fails

Session startup records the existing process-start failure path, invalidates
the signal token, closes both readiness gates, stops the PTY, and detaches.
Working is not committed.

### A query reply write fails

The controller reports the bounded error to session orchestration and keeps
draining x/vt replies. Process state remains authoritative. The error path
must not retry through `SendInput` or invent an input audit.

### Screen classification fails

Static rule compilation fails before the PTY is started. Runtime classifier
methods do not return partially interpreted hints. An invariant failure
enters the existing fail-stop path rather than silently falling back to line
heuristics.

### Output commits but terminal feed fails

The output remains durable. Session records a bounded internal error and
enters the existing fatal path because later screen decisions would no longer
be attributable to a complete committed stream.

### Process exit races with a sample

`MarkProcessExited` fences emission before Detector termination. A screen
candidate already committed before the process fact may exist, but the process
fact cancels or supersedes it. No screen decision follows the terminal process
decision.

### Explain races with detach

Durable events still return. The snapshot is included only if the same
attached terminal actor can return a current immutable snapshot. Explain does
not retain a snapshot after that check.

## Compatibility and rollout

Implementation is split into independently deployable commits:

1. Raise the Go floor, pin x/vt, and add the isolated terminal controller.
2. Start PTYs at their declared initial size.
3. Add reader-first event version 3, `SourceScreen`, and keyed candidate
   support without enabling a production screen writer.
4. Add adapter screen classifiers and fixtures without replacing line
   heuristics yet.
5. Enable the terminal actor, screen writer, startup reply path, and remove
   line heuristics in one commit.
6. Add the explain Manager, Store query, API, client, and CLI.
7. Add acceptance, race, and performance proof.
8. Update architecture documentation and synchronize Issue #14.

Before step 5, database rollback is unaffected. After step 5 writes its first
version 3 signal, step 3 is the oldest safe database reader.

Raising the Go floor is a source-build compatibility change, not a database
change. It is explicit in step 1 and must not be hidden in the writer commit.

Legacy `agent.signal` versions 1 and 2 and state evidence versions 1 and 2
remain readable. Existing raw output, retention, replay, and `drove log`
contracts do not change.

## Verification

Focused tests cover:

- exact query replies for DSR, OSC 10/11, DA1, and Kitty keyboard mode;
- reply-pump readiness before the first controller write;
- reply drain after a closed PTY sink;
- no `agent.input`, output event, log, or snapshot for reply bytes;
- child-observed 40x120 size from process start;
- ordered internal PTY and emulator resize;
- committed-only controller input and copied chunk ownership;
- fixed, non-sliding 100 ms sampling;
- final dirty-screen flush at output end;
- no screen observation after process exit;
- stateful rule edges, rebaseline, near misses, and stable names;
- Claude and Codex redacted byte fixtures under fragmented feeds;
- ordinary `Error` output producing no Blocked candidate;
- active-hook approval clearance and Claude interrupt exceptions;
- durable suppression for every other active-hook screen edge;
- independent keyed candidates and stale timer references;
- process facts canceling every screen candidate;
- version 3 event validation and legacy/unknown reader behavior;
- explain event limits, ordering, unsupported versions, and detached behavior;
- snapshot row, cell, byte, and metadata bounds;
- terminal actor close, startup failure, output-end, and exit races;
- multiple sessions under repeated race tests.

Acceptance scenarios are:

1. With hooks off or in fallback, a rendered approval prompt reaches Blocked
   within one second.
2. With active hooks and Blocked already established by hook evidence, clearing
   the approval prompt reaches Working within one second even without a
   post-tool hook.
3. A rendered Claude interrupt reaches Idle within two seconds.
4. Recorded Claude and Codex startup fixtures answer every required query and
   do not hang.
5. Trailing output after process exit remains replayable and produces no later
   screen signal.
6. Explain for an attached session shows bounded durable evidence and an
   ephemeral bounded snapshot; explain after detach shows durable evidence
   only.

Performance verification runs 32 terminal actors with 1 MiB/s of committed
input per actor. It records toolchain, hardware, aggregate throughput,
allocations, goroutine count, and whether bounded inbox backpressure engages.
The run must complete without byte loss, unbounded queue growth, deadlock, or
race. The first recorded result is a baseline, not an invented CI latency
threshold.

The full automated gate is:

```bash
gofmt -w cmd internal
go test ./internal/term ./internal/pty ./internal/event ./internal/agent ./internal/adapter ./internal/detect ./internal/session ./internal/store ./internal/api ./internal/client ./cmd/drove -race -count=20
go test ./... -race -count=1
go vet ./...
make build
npm --prefix web run typecheck
npm --prefix web run build
git diff --check
```

Manual verification uses isolated Drove, Claude, and Codex homes. It must not
modify persistent vendor configuration or trust settings.

## Risks and controls

### x/vt blocks while producing a reply

The reply pump starts behind a readiness barrier before any controller write.
Tests feed each known query under a deadline and exercise a closed reply sink.

### Screen rules drift across vendor releases

Patterns remain adapter-private and fixture-driven. Durable evidence stores a
stable rule name, not the private pattern. New vendor behavior changes one
classifier and its fixtures.

### Screen sampling consumes too much CPU

The first dirty chunk arms one fixed 100 ms window. Later chunks do not slide
or multiply timers. The 32-session benchmark records throughput and allocation
cost before release.

### Screen evidence weakens hook authority

Detector, not adapter or session, enforces the two explicit active-hook
exceptions. Every other screen edge is durably suppressed and testable.

### Explain leaks terminal content

Historical evidence contains no screen text or hash. The live snapshot is
attached-only, bottom-bounded, byte-bounded, token-redacted, and explicitly
described as not generally secret-safe.

### Terminal work delays process or hook decisions

The terminal actor is separate from the observation actor. Process and hook
observations do not execute x/vt writes or wait for the screen sample timer.

## Rejected alternatives

### Historical x/vt plus custom query protocol

The tested old revision has a DSR coordinate defect and older API. Replacing
all query handlers would make Drove own a broad terminal compatibility layer.

### A second PTY writer actor

`pty.Session.Write` already serializes complete frames. Another queue would add
shutdown and sealing rules without adding policy or safety.

### One actor for terminal and Detector work

x/vt feed, reply backpressure, and screen classification could delay hook and
process facts. The existing observation actor already owns Detector correctly.

### Public rule manifests evaluated by session

This would leak patterns and split capture, match, edge detection, and delivery
into caller-orchestrated stages. The classifier keeps that operation deep.

### Persisted excerpts or screen hashes

They increase sensitive-data lifetime and are not required to identify a
stable rule, edge, region, output position, or decision.

### Keep line heuristics beside screen rules

Two authorities would classify the same terminal output differently and would
preserve the bare `Error` false positive.

## Fragile assumption

This design assumes the pinned x/vt revision continues to produce terminal
replies through the behavior verified by exact fixture tests. The dependency
is intentionally hidden behind `internal/term`. If a future revision changes
reply semantics, only the controller and its compatibility tests may change;
session, adapter, Detector, event, and API contracts must remain stable.

## Stop conditions

Stop implementation and return to this specification if any of these occur:

- unredacted or uncommitted output must enter x/vt;
- x/vt or rule pattern types must escape their owning packages;
- the terminal actor must inspect Detector state;
- the observation actor must inspect terminal cells;
- terminal replies must pass through `Manager.SendInput`;
- screen text or a screen hash must become durable;
- a screen transition outside the authority table is required;
- an exited session must retain a snapshot for explain;
- output after process exit must create a state observation;
- a focused commit cannot pass its race tests.

## Approval

The owner approved this specification, `checklist.md`, and `tasks.md` for
implementation on 2026-10-04.
