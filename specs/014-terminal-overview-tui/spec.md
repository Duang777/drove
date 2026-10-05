# Terminal overview TUI

Status: Approved

Approved: 2026-10-05

Related issues:

- [#41](https://github.com/Duang777/drove/issues/41), Bubble Tea fleet overview
- [#20](https://github.com/Duang777/drove/issues/20), CLI attach and Web playback
- [#19](https://github.com/Duang777/drove/issues/19), WebSocket v2 terminal stream
- [#14](https://github.com/Duang777/drove/issues/14), bounded screen snapshots

## Problem

`drove ps` shows one static fleet view. Users must run separate commands to
inspect or operate a session, and they cannot keep the fleet state visible
while agents work.

The TUI must preserve four existing ownership rules:

1. `session.Status` is the only fleet state projection.
2. The daemon renders bounded screen snapshots.
3. `internal/cliattach` owns interactive terminal attachment.
4. `internal/client` owns REST and WebSocket protocol details.

The TUI coordinates these capabilities. It does not infer state from terminal
text, emulate a terminal, or add vendor-specific behavior.

## Goals

- Add `drove tui`.
- Show state, vendor, hook status, update age, and the latest transition source.
- Keep Blocked sessions at the top of the visible list.
- Filter by all seven Agent states.
- Show one daemon-rendered snapshot for the selected session.
- Run writable and read-only attach from the selected row.
- Send one line, stop with confirmation, and show typed explain output.
- Keep the selected Agent ID across refreshes, sorting, filters, and attach.
- Show ten concurrent state changes within one second.
- Close every local stream and worker when the TUI exits.
- Leave all remote agents running when the TUI exits.

## Non-goals

- Add a new daemon endpoint or WebSocket protocol.
- Reconstruct `session.Status` from events.
- Subscribe to every session's terminal output.
- Run an x/vt emulator in the TUI.
- Persist TUI filters, selection, snapshots, or notices.
- Change `drove attach`, `drove send`, `drove stop`, or `drove explain`
  semantics.
- Add mouse input.
- Add vendor-specific keys or rendering.

## Package boundary

Add `internal/clitui`. Its public API is:

```go
// Run opens the fleet overview until the user quits or ctx ends.
func Run(ctx context.Context, daemon *client.Client) error
```

`cmd/drove` registers the command, creates the existing authenticated client,
and calls `clitui.Run`. It does not own models, key handling, rendering,
polling, WebSocket state, or attach cleanup.

`internal/clitui` may import:

- `internal/client`
- `internal/cliattach`
- `internal/session`
- public domain types exposed by those packages
- Bubble Tea, Bubbles, and Lip Gloss

It must not import `internal/api`, `internal/store`, `internal/pty`,
`internal/term`, or `internal/adapter`.

## Dependencies

Pin these direct dependencies:

```text
github.com/charmbracelet/bubbletea v1.3.10
github.com/charmbracelet/bubbles   v1.0.0
github.com/charmbracelet/lipgloss  v1.1.0
```

These versions support Go 1.24.2. Bubble Tea 1.3.10 provides `tea.Exec`, which
releases and restores the terminal around an interactive command.

## Fleet projection

Poll `client.List` every 500 milliseconds. Each successful response replaces
the complete local collection. A failed response keeps the last successful
collection and shows the error.

Only one list request may run at a time. If a poll or completed action requests
a refresh while another request runs, record one trailing refresh and start it
after the current request returns.

Do not use per-Agent event subscriptions for fleet refresh. A selector-free
event subscription starts at the recording origin, includes output events,
requires a cursor for reconnect, and cannot discover new sessions. An event
can also arrive before the in-memory status projection becomes visible.

The visible row is a copy of display fields:

```go
type fleetRow struct {
	AgentID          string
	Name             string
	Vendor           string
	State            agent.State
	HookStatus       detect.HookStatus
	TransitionEvent  string
	TransitionSource agent.EvidenceSource
	UpdatedAt        time.Time
}
```

Filtering uses exact `agent.State` values. The filters are:

```text
all
pending
starting
working
blocked
idle
done
stopped
```

After filtering, a stable partition places Blocked rows first. The partition
preserves the daemon's creation order within the Blocked and non-Blocked
groups.

Selection stores an Agent ID and a fallback index:

```go
type selection struct {
	AgentID       string
	FallbackIndex int
}
```

If the current Agent ID remains visible, keep it. Otherwise, clamp the fallback
index to the new visible list and select that row. An empty list clears the
selection.

## Snapshot preview

The TUI opens one WebSocket v2 stream for the selected Agent. It subscribes
with:

```go
client.TerminalSubscription{
	AgentID: selectedID,
	Mode:    client.TerminalModeSnapshot,
}
```

The subscription must not set `Access`, `Rows`, `Columns`, `Cursor`,
`Sequence`, or `Offset`.

The preview renders `TerminalSnapshot.Lines` directly. It may clip lines to the
local pane, but it must not parse terminal control sequences or rebuild screen
state. The preview also shows the source terminal dimensions, capture time,
and truncation state.

One private preview actor owns all snapshot resources:

```go
type previewTarget struct {
	AgentID    string
	Generation uint64
}

type previewEvent struct {
	Target   previewTarget
	Snapshot *client.TerminalSnapshot
	Err      error
	RetryIn  time.Duration
}

type previewController interface {
	Replace(previewTarget)
	Events() <-chan previewEvent
	Close() error
}
```

The actor supervises at most one generation worker. The worker owns one
WebSocket and is the only caller of `TerminalStream.Next`.

When the target changes, the actor performs these steps:

1. Cancel the current generation.
2. Wait for its worker to close the stream and return.
3. Coalesce pending targets to the latest value.
4. Open and subscribe the new generation.

The worker reconnects after 250 milliseconds, 500 milliseconds, 1 second, and
then 2 seconds. A valid snapshot resets the delay. Retry waits and stream calls
must stop when the generation context ends.

Both target and event channels keep only the latest value. Each event carries
the Agent ID and generation. The model rejects events that do not match the
current target.

`Close` is idempotent. It cancels the actor, waits for the active worker, and
returns any stream close error. Tests use completion channels to prove worker
termination instead of comparing process-wide goroutine counts.

## Interaction model

The list row is the default focus. It uses a visible marker and a distinct
style that does not rely on color alone.

The key map is:

| Key | Action |
|---|---|
| `up`, `k` | Select the previous visible row |
| `down`, `j` | Select the next visible row |
| `f` | Focus the state filter |
| `left`, `right` | Change the focused filter |
| `enter`, `esc` | Leave filter focus |
| `a` | Start writable attach |
| `r` | Start read-only attach |
| `s` | Open the send editor |
| `x` | Open stop confirmation |
| `e` | Load and show typed explanation |
| `q`, `ctrl+c` | Quit from the fleet view |

The footer shows only keys valid for the current focus.

### Send

The send editor uses a one-line Bubbles text input. `Enter` sends the entered
text plus one newline. An empty editor sends one newline, matching
`drove send <agent-id> ""`.

The TUI rejects a payload only when the text plus newline exceeds
`session.MaxInputBytes`. `Esc` cancels without sending.

### Stop

`x` opens a focused confirmation for the selected Agent ID. `y` calls
`client.Stop`; `n` and `Esc` cancel. A completed stop requests an immediate
fleet refresh.

### Explain

`e` calls `client.Explain` for the selected Agent ID. The overlay renders the
typed state, hook status, attachment status, and bounded event fields. It does
not decode raw event payloads.

The explanation uses a Bubbles viewport when the content exceeds the available
height. `Esc` returns to the fleet.

## Attach handoff

`a` and `r` return `tea.Exec` with a small `tea.ExecCommand` adapter. Bubble Tea
releases its input reader, renderer, and terminal state before the adapter
calls `cliattach.Run`.

Writable attach passes `cliattach.Options{ReadOnly: false}`. Read-only attach
passes `cliattach.Options{ReadOnly: true}`.

`cliattach.Run` continues to own raw mode, stdin and stdout pumps, `SIGWINCH`,
Ctrl-Q, and attachment cleanup. The TUI must not reproduce those operations.

After attach returns, the model:

1. Keeps the previous selected Agent ID.
2. Requests an immediate fleet refresh.
3. Replaces the preview generation for that Agent ID.
4. Shows a non-cancellation attach error in the status line.

Production `drove tui` uses Bubble Tea's default `os.Stdin` and `os.Stdout`.
This matches the fixed streams used by `cliattach.Run`.

## Rendering

Wide terminals show the fleet and preview side by side. Narrow terminals stack
the preview below the fleet. The layout derives stable pane dimensions from
`tea.WindowSizeMsg`.

The fleet header shows the active filter, visible count, total count, and
Blocked count. Each row shows:

- selection marker;
- state;
- name, with Agent ID as fallback;
- vendor;
- hook status;
- latest transition event and source;
- age since `UpdatedAt`.

The preview pane distinguishes loading, unavailable, reconnecting, empty, and
ready states. A stopped session uses neutral wording such as `no live
snapshot`.

Resize messages change only local dimensions. The TUI does not register an OS
resize signal or send a daemon resize request.

## Error handling

Fleet refresh errors keep the last good rows visible.

Preview errors stay in the preview pane while reconnect runs. `unknown_agent`
and `not_attached` remain non-fatal because a later fleet transition can make
the same Agent available.

Action errors stay visible until the next action or explicit dismissal. A late
result includes its target Agent ID and cannot replace the current selected
Agent's preview or explanation.

Parent context cancellation stops all local work. `Run` returns the context
error unless the user quit normally.

## Cleanup invariants

- The Bubble Tea update loop is the only writer of model state.
- The preview actor is the only owner of snapshot streams.
- One stream has one `Next` caller.
- Replacement joins the old generation before opening the next.
- Every established stream closes on replacement, retry, or exit.
- Overview resize creates no signal subscription or goroutine.
- `tea.Exec` gives terminal ownership to `cliattach` only after Bubble Tea
  releases it.
- TUI quit never calls `client.Stop`.

## Acceptance

The implementation is complete when these tests pass under `-race`:

- Ten changed sessions appear after the next 500 millisecond poll and before a
  one-second deadline.
- A slow list request never overlaps another request.
- Blocked pinning is stable and every state filter works.
- Selection survives refresh reorder and both attach modes.
- Send appends one newline, including for an empty line.
- Stop requires `y`; `n` and `Esc` do not call the daemon.
- Explain renders typed fields and has visible focus.
- Rapid selection changes reject stale snapshots.
- The preview actor never overlaps `Next` calls and joins each old worker.
- Repeated terminal resize messages do not change preview subscriptions or
  create resize work.
- Quit closes local resources and calls no stop operation.
- A pseudo-terminal test crosses Bubble Tea's `tea.Exec` handoff and verifies
  terminal restoration after repeated attach returns.
