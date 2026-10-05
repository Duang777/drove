# Terminal attach and Web playback

Status: Approved

Approved: 2026-10-04

Owner direction: Issue #20 follows the Web-first scope in Epic #31. This
batch adds the Web terminal detail view and `drove attach`. A Bubble Tea fleet
list moves to a linked follow-up issue. `drove ps` remains the terminal list
for this release.

Related issues:

- [#20](https://github.com/Duang777/drove/issues/20), interactive attach and
  Web playback
- [#31](https://github.com/Duang777/drove/issues/31), MVP execution plan
- [#19](https://github.com/Duang777/drove/issues/19), WebSocket terminal stream
- [#25](https://github.com/Duang777/drove/issues/25), replay timeline and frames
- [#35](https://github.com/Duang777/drove/issues/35), exact replay checkpoints

## Problem

Drove can stream committed terminal bytes and reconstruct exact frames, but
users cannot operate a session through the CLI or inspect its live terminal in
the Web console. The Web console also lacks playback controls for the persisted
event history.

The existing recording APIs impose four constraints:

1. a continuing terminal replay must start at the recording origin;
2. `Frame` and `Snapshot` are visible previews with `restorable:false`;
3. raw terminal records and event timestamps arrive through separate ordered
   subscriptions;
4. a consumer advances its reconnect cursor only after it applies a message.

The new clients must preserve these constraints. React and Cobra must not own
cursor, resize, attachment, terminal parser, or recording rules.

## Goals

- Add `drove attach <agent-id> [--read-only]` over WebSocket v2.
- Add an xterm.js detail view for live output and writable terminal input.
- Add exact local playback with seek, pause, speed, and Blocked markers.
- Keep live xterm state intact while a separate replay xterm rebuilds.
- Resume raw and event subscriptions from separate locally applied cursors.
- Correlate terminal operations with event timestamps by sequence.
- Bound one browser recording to a 64 MiB origin-preserving prefix.
- Record user attachment and detachment without recording identity or content.
- Preserve existing input byte-count audit events.
- Support desktop and 320 px-wide viewports with visible focus and no page
  overflow.

## Non-goals

- Add a Bubble Tea fleet list in this batch.
- Treat a `Frame` or `Snapshot` as restorable emulator state.
- Add server-side full emulator checkpoints. Issue #35 owns that work.
- Claim sub-300 ms cold random seek for large recordings.
- Persist browser replay state, rendered screens, attachment IDs, client
  identity, or input text.
- Add a frontend router or state library.
- Change WebSocket v1 behavior.
- Add vendor-specific behavior outside `internal/adapter`.

## Architectural decision

The Web detail view uses one `TerminalSessionController`. The controller owns
both WebSocket subscriptions, both xterm instances, replay timing, reconnect,
input, resize, and teardown. React receives a render model, one viewport
binding, and playback actions.

The controller keeps the live xterm for the page lifetime. When the user
commits a seek, the controller creates a fresh replay xterm and applies the
recorded prefix from origin. Returning to live reveals the unchanged live
xterm.

`SessionTape` stores terminal operations and timestamps as browser domain
records. Transport parsers convert WebSocket and REST data before the tape
sees it. The tape never accepts wire DTOs or visible frame data.

The CLI command delegates to `internal/cliattach`. That package owns the local
TTY lease, input framing, signals, stream pumps, and cleanup. Cobra parses
arguments and constructs the client.

## User attachment intent

The existing optional `writable` field on a raw subscription has these
meanings:

- omitted: recording reader, with no user attachment audit;
- `false`: read-only user attachment;
- `true`: writable user attachment.

Selector presence does not express attachment intent. A reconnect can include
both `writable:false` and a cursor.

Go and TypeScript clients expose named access modes instead of an optional
boolean. Snapshot subscriptions remain preview infrastructure and do not
create attachment audit events.

## Attachment audit

Add immutable event type `agent.attachment` and payload version 1:

```go
type AttachmentAction string

const (
	AttachmentAttached AttachmentAction = "attached"
	AttachmentDetached AttachmentAction = "detached"
)

type AttachmentAuditPayloadV1 struct {
	Version int              `json:"version"`
	Action  AttachmentAction `json:"action"`
	Access  AttachmentAccess `json:"access"`
}
```

The envelope timestamp records when the action occurred. The payload contains
only the action and either `read_only` or `read_write` access. It does not
contain an attachment ID, input text, client identity, terminal size, control
token, or screen data.

Event draft validation, Store validation, recovery, package documentation, and
the browser event union must accept `agent.attachment` before a production
path writes it.

`session.Manager.AttachTerminal` receives an explicit purpose. A user raw
attachment commits one attached event after successful setup. Every removal
path commits one detached event after local cleanup:

- explicit unsubscribe or connection close;
- subscription setup failure after local attachment;
- process exit;
- `DetachAll`;
- manager shutdown.

The attachment actor removes each handle once. Duplicate close paths do not
write duplicate detach events. Audit commit failure follows the existing
fail-stop policy after local cleanup.

Existing successful input writes continue to emit `agent.input` with a byte
count. Attachment events do not aggregate input because a crash could lose
that aggregate.

## CLI attach

Add:

```text
drove attach <agent-id> [--read-only]
```

Writable attach sends input and viewport changes. Read-only attach sends
neither.

`internal/cliattach` owns this sequence:

1. require a terminal on stdin;
2. enter raw mode and install idempotent cleanup;
3. open WebSocket v2 and subscribe with explicit access;
4. write only raw terminal output bytes to stdout;
5. read stdin in chunks no larger than 32 KiB;
6. retain an incomplete UTF-8 suffix between reads;
7. forward Ctrl-C to the remote session and consume Ctrl-Q as local detach;
8. send the initial viewport and `SIGWINCH` updates only for writable access;
9. unblock a pending stdin read when the stream ends or the context cancels;
10. close pumps, signals, stream, reader, and raw mode exactly once.

Use `github.com/muesli/cancelreader` to cancel stdin reads. Use
`golang.org/x/term` for terminal state and size. One input request must contain
valid UTF-8 and no more than 64 KiB.

The command leaves the remote agent running on local detach, stream EOF, and
context cancellation.

## Browser domain model

Transport parsers convert canonical decimal strings to `bigint` values and
decode raw bytes before constructing a recorded operation:

```ts
export type RecordedOperation =
  | {
      readonly kind: 'output'
      readonly seq: bigint
      readonly offset: bigint
      readonly data: Uint8Array
      readonly cursor: Cursor
    }
  | {
      readonly kind: 'resize'
      readonly seq: bigint
      readonly rows: number
      readonly columns: number
      readonly cursor: Cursor
    }

export interface TimedOperation {
  readonly operation: RecordedOperation
  readonly atMillis: number
}

export type ReplayPlan =
  | {
      readonly kind: 'exact'
      readonly target: Cursor
      readonly operations: ReadonlyArray<TimedOperation>
    }
  | { readonly kind: 'not_ready' }
  | { readonly kind: 'expired'; readonly missing: ReadonlyArray<OutputRange> }
  | { readonly kind: 'local_limit'; readonly byteLimit: number }
```

`SessionTape` owns the ephemeral origin prefix. It keeps raw operations in
sequence order and timestamps by sequence. It advances the exact frontier only
when both records exist.

The tape accepts an identical duplicate after reconnect. It rejects a
conflicting duplicate, an output gap, a noncanonical cursor, or time that
regresses in sequence order.

The tape stops recording before it would exceed 64 MiB. It does not evict the
origin. Live rendering continues, but local exact replay reports
`local_limit`.

Timeline state spans and Blocked markers remain authoritative REST data. The
tape does not derive a second state projection.

## Strict REST boundary

`web/src/api/client.ts` parses response JSON as `unknown`. Timeline and frame
parsers validate object shape, known variants, dimensions, timestamps, and
canonical decimal strings.

HTTP 410 becomes:

```ts
export class OutputExpiredError extends Error {
  readonly kind = 'output_expired'

  constructor(
    readonly sessionID: string,
    readonly missing: ReadonlyArray<OutputRange>,
  ) {
    super('Recorded terminal output has expired')
  }
}
```

The UI may display a frame as a labeled preview while an exact rebuild runs.
No API passes a `Frame` or `Snapshot` into `SessionTape` or xterm as replay
state.

## Terminal controller

React uses this capability:

```ts
export interface AgentTerminalHandle {
  readonly view: AgentTerminalView
  readonly bindViewport: (element: HTMLDivElement | null) => void
  readonly dispatch: (action: PlaybackAction) => void
}
```

`PlaybackAction` covers go-live, preview time, committed seek, seek
cancellation, Blocked jump, play, pause, speed, and retry. React never receives
an xterm input callback, resize callback, transport cursor, or xterm instance.

`TerminalSessionController` owns these flows.

### Live and reconnect

The controller loads the timeline, creates a live xterm, and subscribes to raw
and event modes from origin. The shared stream callback resolves only after
the controller applies the output or durable resize. Writable input remains
disabled until raw `caught_up`.

The controller tracks a local applied cursor per mode. On reconnect, it keeps
the live xterm, replaces the one-shot stream, and resumes each mode from its
own cursor. It ignores server-written cursors that are ahead of local
application.

Reconnect uses a generation token and bounded backoff. A newer generation
invalidates every callback from an older stream.

### Record and seek

Raw records and event timestamps enter `SessionTape` through separate typed
methods. A slider move updates only the preview time and labels. It does not
send frame requests.

A committed seek, keyboard idle commit, or Blocked jump may start one frame
request. A newer commit aborts the previous request.

For exact seek, the controller disposes the old replay xterm, creates a fresh
40x120 xterm, and applies the origin plan in bounded batches. A newer seek
cancels the old rebuild. The controller never resets and reuses a replay
xterm.

### Playback

The controller waits for each xterm output write or recorded resize before it
schedules the next timestamp. Playback preserves recorded idle gaps. Speed
supports `0.5`, `1`, `2`, and `4`.

Pause and speed changes cancel the current timer and resume from the current
replay cursor. Bounded batches yield to the browser so replay does not hold the
main thread indefinitely.

### Input and resize

The controller owns xterm `onData`. It splits a paste into valid UTF-8
requests no larger than 64 KiB and serializes acknowledgements.

`FitAddon.proposeDimensions()` or an equivalent measurement proposes a
viewport without mutating xterm. The controller sends one pending proposal.
It resizes the live xterm only when the corresponding durable resize record
arrives in stream order. Replay applies only recorded sizes.

### Teardown

Disposal invalidates generations, aborts fetches and replay, clears timers,
rejects queued input, disconnects observers, closes the stream, and disposes
both xterms exactly once. A callback after disposal cannot mutate the view or
write to a terminal.

## Web composition

The embedded server already serves the application at `/`. Use
`/?agent=<id>` and `popstate` for detail routing. Do not add a router or change
the Go SPA fallback.

The fleet page keeps its current list and event feed. Selecting a row opens the
detail page. Browser Back returns to the fleet without a full reload.

The detail page contains:

- a compact header with Back, agent identity, state, working directory, and
  live connection status;
- the terminal as the primary visual area;
- a state-colored playback rail with time, Blocked markers, live or replay
  mode, play or pause, speed, and go-live controls;
- explicit loading, reconnecting, preview, expired, local-limit, and error
  states.

The visual system remains a cool light canvas with an emerald operational
accent and a dark terminal. Use the existing global stylesheet and radius
scale of 4, 6, and 8 px. Controls use Lucide icons when an icon exists and
provide tooltips for unfamiliar actions.

At 320 px and wider:

- the page has no horizontal overflow;
- controls remain at least 40 px;
- text does not overlap or leave its container;
- the fixed-format replay terminal may scroll horizontally inside its own
  viewport;
- focus remains visible;
- reduced-motion preferences disable nonessential transitions.

## Dependencies and modules

Add Web dependencies `@xterm/xterm`, `@xterm/addon-fit`, and `lucide-react`.
Add Vitest for pure playback, navigation, and controller tests.

Add direct Go dependencies only where the standard library has no equivalent:
`golang.org/x/term` and `github.com/muesli/cancelreader`.

| Module | Responsibility |
|---|---|
| `web/src/navigation.ts` | Query route and browser history. |
| `web/src/api/client.ts`, `web/src/api/types.ts` | Strict REST parsing and typed expiry. |
| `web/src/terminal/sessionTape.ts` | Origin prefix, timestamp correlation, coverage, and replay plans. |
| `web/src/terminal/sessionController.ts` | Streams, xterm, reconnect, input, resize, seek, playback, and disposal. |
| `web/src/hooks/useAgentTerminal.ts` | React lifetime adapter over an external store. |
| `web/src/components/AgentDetailPage.tsx` | Detail-page composition. |
| `web/src/components/TerminalViewport.tsx` | Terminal host and labeled overlays. |
| `web/src/components/PlaybackRail.tsx` | Accessible timeline and playback actions. |
| `internal/event` | Versioned attachment audit event. |
| `internal/store`, `internal/session/projection.go` | Reader-first validation and restart compatibility. |
| `internal/session` | User attachment audit and forced cleanup. |
| `internal/api/websocket_v2.go` | Optional writable presence to access intent. |
| `internal/client/terminal.go` | Typed raw access and v2 terminal client. |
| `internal/cliattach` | Local TTY lifecycle and stream pumps. |
| `cmd/drove` | Cobra registration and flags. |

## Compatibility, privacy, and security

- Existing event readers accept `agent.attachment` before writers emit it.
- Recording readers omit `writable` and produce no user attachment audit.
- WebSocket v1 and existing REST response shapes remain unchanged.
- Raw output still contains only committed, token-redacted attachment bytes.
- Input audit contains successful byte counts and no input content.
- Attachment audit contains no client or terminal content.
- The production Web client continues to use the HttpOnly, SameSite=Strict
  session cookie. Frontend code never reads the control token.
- The Vite development proxy continues to inject authentication for HTTP and
  WebSocket requests.
- Browser recording, xterm state, frame previews, and reconnect state remain
  memory-only and disappear when the page closes.

## Acceptance

- Writable CLI attach handles arrows, Escape, paste, Ctrl-C, Ctrl-Q, initial
  size, `SIGWINCH`, remote EOF, connection failure, and terminal restoration.
- Read-only CLI attach renders output but never sends input or resize.
- Every audited user attachment writes one attached and one detached event
  across explicit close, failed setup, process exit, `DetachAll`, and shutdown.
- Snapshot and recording-reader subscriptions write no attachment audit.
- Existing input byte-count events remain unchanged and contain no input text.
- Live Web output matches committed raw output and applies durable resize in
  sequence order.
- Reconnect from each locally applied cursor produces no missing or duplicate
  terminal effect.
- Exact seek and playback match origin replay under fragmented UTF-8, split
  control sequences, alternate screen, and resize.
- Slider movement sends no repeated frame requests. A committed seek has at
  most one current frame request and one current replay build.
- Output expiry and the 64 MiB local limit disable only unavailable exact
  playback. Timeline and Blocked navigation remain usable.
- Returning to live reveals terminal state accumulated while replay was open.
- Desktop, 375 px, and 320 px browser checks show no overlap, page overflow,
  blank terminal, or broken focus order.
- A real daemon and PTY verify input, resize, reconnect, seek, playback, and
  cleanup.
- Focused 20-round race tests, full race, vet, Go builds, Web tests, Web
  typecheck, Web build, `git diff --check`, and both CI lanes pass.
- A Bubble Tea fleet-list follow-up is linked from Issue #20 before Issue #20
  closes.
