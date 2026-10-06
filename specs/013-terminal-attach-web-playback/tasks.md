# Implementation tasks

Complete commits in order. For every product-code commit:

1. implement only that commit's tasks;
2. run the focused checks;
3. run the common full gate;
4. inspect the complete diff;
5. commit with the listed message;
6. push immediately;
7. verify that the remote branch contains the commit.

Do not mix unfinished work from two commits.

## Common full gate

```bash
gofmt -w .
go test ./... -race -count=1
go vet ./...
make build
npm --prefix web run test --if-present -- --run
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
docs: specify terminal attach and web playback
```

## Commit 1: reader-first attachment audit

### T1: Add the attachment event contract

Files:

- `internal/event/event.go`
- `internal/event/event_test.go`
- `internal/event/AGENTS.md`

Work:

- Add event type `agent.attachment`.
- Add action values `attached` and `detached`.
- Add access values `read_only` and `read_write`.
- Validate payload version 1 and reject unknown fields or values.
- Keep the payload free of attachment IDs, client identity, input, terminal
  dimensions, and screen data.
- Do not add a production writer in this task.

### T2: Add persistence and recovery readers

Files:

- `internal/store`
- `internal/session/projection.go`
- matching tests and package `AGENTS.md` files
- `web/src/api/types.ts`

Work:

- Accept and replay the new immutable event.
- Treat it as a known non-state event during recovery.
- Keep current state projection unchanged.
- Add it to the browser event union.
- Prove that old recordings and all existing event types still load.

Focused verification:

```bash
go test ./internal/event ./internal/store ./internal/session -race -count=20
go test ./internal/event ./internal/store ./internal/session -run 'Test.*(Event|Draft|Replay|Recover|Projection)' -count=100
npm --prefix web run typecheck
```

Commit and push:

```text
feat: recognize terminal attachment events
```

No attach path may write the event before this commit lands.

## Commit 2: audited client and CLI attach

### T3: Add explicit raw access

Files:

- `internal/session/attachment.go`
- `internal/session/session.go`
- `internal/session` tests
- `internal/api/websocket_v2.go`
- `internal/api/websocket_v2_test.go`
- `internal/client/terminal.go`
- `internal/client/terminal_test.go`
- relevant package `AGENTS.md` files

Work:

- Give `AttachTerminal` an explicit recording or user purpose.
- Preserve the optional `writable` field's presence at the API boundary.
- Map omitted, false, and true to recording, read-only, and writable access.
- Expose named access modes through the Go client.
- Commit attached only after successful user setup.
- Retain audit purpose and access until actor removal.
- Commit detached after local cleanup on every removal path.
- Make explicit close, failed setup, process exit, `DetachAll`, and shutdown
  converge on one exactly-once removal operation.
- Keep the Committer available until forced detach drafts finish.
- Keep snapshot and recording-reader paths unaudited.
- Preserve one `agent.input` event for each successful input write.

### T4: Add the CLI runner

Files:

- `internal/cliattach/AGENTS.md`
- `internal/cliattach/*.go`
- `internal/cliattach/*_test.go`
- `cmd/drove/main.go`
- `cmd/drove/main_test.go`
- `go.mod`
- `go.sum`

Work:

- Add `drove attach <agent-id> [--read-only]`.
- Keep Cobra limited to flags, client construction, and `cliattach.Run`.
- Enter local raw mode and install idempotent cleanup.
- Read stdin in 32 KiB chunks.
- Keep an incomplete UTF-8 suffix between reads.
- Consume Ctrl-Q locally and forward Ctrl-C.
- Send initial size and `SIGWINCH` only for writable access.
- Serialize input requests and cap each request at 64 KiB.
- Use a cancelable reader so remote EOF and context cancellation unblock
  stdin.
- Restore the TTY and release signals, pumps, reader, and stream exactly once.
- Leave the remote agent running.

### T5: Add attach acceptance

- Unit-test UTF-8 fragmentation, Ctrl-Q filtering, read-only mode, pump
  failures, cancellation, and cleanup races.
- Add a pseudo-terminal plus fake-v2 test for arrows, Escape, paste, Ctrl-C,
  Ctrl-Q, resize, remote EOF, and restoration.
- Prove that attachment audit contains no input or client identity.

Focused verification:

```bash
go test ./internal/session ./internal/api ./internal/client ./internal/cliattach ./cmd/drove -race -count=20
go test ./internal/session ./internal/cliattach ./cmd/drove -run 'Test.*(Attach|Detach|UTF8|TTY|Signal|ReadOnly|Cleanup)' -count=100
```

Commit and push:

```text
feat: attach to terminal sessions
```

## Commit 3: typed browser replay model

### T6: Add strict transport parsing

Files:

- `web/src/api/client.ts`
- `web/src/api/types.ts`
- matching tests

Work:

- Parse timeline and frame JSON from `unknown`.
- Validate canonical decimal strings before conversion to `bigint`.
- Validate known variants, ranges, dimensions, and timestamps.
- Convert HTTP 410 to `OutputExpiredError` with missing ranges.
- Keep visible frames out of exact replay types.

### T7: Add `SessionTape`

Files:

- `web/src/terminal/sessionTape.ts`
- `web/src/terminal/sessionTape.test.ts`

Work:

- Define domain-only output and resize operations.
- Correlate raw operations and event timestamps by sequence.
- Advance the exact frontier only when both records exist.
- Accept identical reconnect duplicates.
- Reject conflicting duplicates, gaps, and time regressions.
- Return typed exact, not-ready, expired, and local-limit plans.
- Freeze before 64 MiB without evicting origin data.
- Keep timeline spans and Blocked markers outside the tape.

### T8: Add the Web test runner

Files:

- `web/package.json`
- `web/package-lock.json`
- `web/vite.config.ts` or a focused Vitest configuration

Work:

- Add Vitest with the smallest environment needed by pure model tests.
- Add deterministic clock and transport fixtures for later controller tests.
- Keep the existing typecheck and build commands unchanged.

Focused verification:

```bash
npm --prefix web run test -- --run
npm --prefix web run typecheck
npm --prefix web run build
```

Commit and push:

```text
feat: model browser terminal replay
```

## Commit 4: live terminal playback controller

### T9: Add the terminal dependencies and adapter

Files:

- `web/package.json`
- `web/package-lock.json`
- `web/src/terminal`

Work:

- Add `@xterm/xterm` and `@xterm/addon-fit`.
- Keep xterm construction and disposal behind a controller-owned adapter.
- Support awaited writes, recorded resize, input subscription, dimension
  proposal, focus, and disposal.
- Do not expose xterm through the React interface.

### T10: Add `TerminalSessionController`

Files:

- `web/src/terminal/sessionController.ts`
- `web/src/terminal/sessionController.test.ts`
- `web/src/ws/terminalStream.ts`
- matching transport tests

Work:

- Keep a persistent live xterm and a replaceable replay xterm.
- Subscribe to raw and events with separate locally applied cursors.
- Apply each message before the stream callback resolves.
- Enable writable input only after raw `caught_up`.
- Reconnect with generation checks and bounded backoff.
- Keep server-written cursors separate from local application cursors.
- Serialize UTF-8 input requests of at most 64 KiB.
- Send proposed dimensions without mutating live xterm.
- Apply live resize only from the durable stream record.
- Record typed operations and timestamps in `SessionTape`.
- Rebuild a fresh 40x120 replay xterm from origin for each seek.
- Await every output write and resize during playback.
- Support pause and `0.5`, `1`, `2`, and `4` speeds.
- Bound replay batches and yield to the browser.
- Abort stale fetches, replay, reconnect, timers, and callbacks.
- Dispose all resources exactly once.

### T11: Add the React lifetime adapter

Files:

- `web/src/hooks/useAgentTerminal.ts`
- matching tests

Work:

- Expose only `view`, `bindViewport`, and `dispatch`.
- Subscribe through an external-store contract.
- Recreate the controller when the agent ID or access changes.
- Dispose the old controller before a new one becomes active.

Focused verification:

```bash
npm --prefix web run test -- --run
npm --prefix web run typecheck
npm --prefix web run build
```

Commit and push:

```text
feat: control live terminal playback
```

## Commit 5: Web terminal detail view

### T12: Add query navigation

Files:

- `web/src/navigation.ts`
- `web/src/navigation.test.ts`
- `web/src/App.tsx`

Work:

- Parse and write `/?agent=<id>`.
- Handle `popstate` without a full reload.
- Open a detail page from each fleet row.
- Keep the existing fleet list and event feed.

### T13: Add the detail components

Files:

- `web/src/components/AgentDetailPage.tsx`
- `web/src/components/TerminalViewport.tsx`
- `web/src/components/PlaybackRail.tsx`
- matching tests

Work:

- Add the compact header and connection state.
- Make the terminal the primary visual area.
- Render authoritative state spans and Blocked markers.
- Add seek, play, pause, speed, retry, and go-live controls.
- Keep slider movement local until commit.
- Allow at most one current frame preview request.
- Label frame previews and never load them into xterm.
- Show loading, reconnecting, expired, local-limit, and error states.
- Add `lucide-react` and accessible names for icon controls.

### T14: Add responsive styles

Files:

- `web/src/styles.css`
- xterm CSS import entry

Work:

- Keep the existing light canvas and emerald operational accent.
- Use the dark terminal as the visual anchor.
- Keep radii at 4, 6, and 8 px.
- Keep controls at least 40 px and focus visible.
- Support reduced motion and safe-area insets.
- Prevent horizontal page overflow at 320 px.
- Contain a wide fixed-format replay terminal in an internal horizontal
  scroller.

Focused verification:

```bash
npm --prefix web run test -- --run
npm --prefix web run typecheck
npm --prefix web run build
```

Commit and push:

```text
feat: add web terminal playback
```

## Commit 6: acceptance and documentation

### T15: Run real CLI and browser acceptance

- Start a real daemon and sessions for both Claude and Codex when available.
- Verify writable and read-only CLI attach against a real PTY.
- Verify live Web output, input, durable resize, reconnect, exact seek,
  playback, Blocked jumps, go-live, and teardown.
- Verify that returning to live includes output received during replay.
- Verify desktop, 375 px, and 320 px layouts with browser screenshots.
- Check canvas and terminal pixels for nonblank rendering.
- Check focus order, keyboard controls, tooltips, text containment, and page
  overflow.
- Check browser console and network failures.

### T16: Run privacy and lifecycle acceptance

- Inspect attachment and input events from the Store.
- Confirm that input text, client identity, attachment IDs, screen data,
  cookies, and control tokens are absent.
- Exercise explicit unsubscribe, failed setup, process exit, `DetachAll`, and
  manager shutdown.
- Confirm exactly one detached event per audited attachment.
- Confirm recording readers and snapshot previews produce no attachment event.

### T17: Update documentation

Files:

- `README.md`
- `docs/technical-notes.md`
- relevant package `AGENTS.md` files
- this checklist

Work:

- Document `drove attach`, Ctrl-Q, read-only behavior, and TTY cleanup.
- Document Web live and replay modes.
- Document origin replay, the 64 MiB local limit, output expiry, and Issue
  #35's checkpoint limitation.
- Document attachment audit fields and privacy exclusions.
- Record the verified desktop and mobile behavior.

### T18: Integrate and close Issue #20

- Open a focused Bubble Tea fleet-list follow-up.
- Link the follow-up from Issue #20.
- Run focused race tests 20 times.
- Run the common full gate.
- Re-read the complete branch diff.
- Wait for both remote CI lanes.
- Comment on Issue #20 with commits, test commands, screenshots, and known
  replay limits.
- Close Issue #20 only after every functional acceptance item passes.
- Merge the pull request and synchronize local `main` with `origin/main`.

Commit and push:

```text
docs: document terminal attach and playback
```

## Stop conditions

Stop and revise this specification if:

- exact replay must continue from a visible `Frame` or `Snapshot`;
- React must own an xterm instance, cursor, input callback, or resize callback;
- Cobra must own raw mode, signals, UTF-8 framing, or stream pumps;
- reconnect must trust a server-written cursor ahead of browser application;
- slider movement must issue repeated cold frame requests;
- live xterm must resize before a durable resize record arrives;
- origin data must be evicted to stay under the browser byte limit;
- attachment audit must store input, client identity, or terminal content;
- a snapshot or recording reader must look like a user attachment;
- WebSocket v1 must change;
- an implementation commit cannot be pushed before the next begins.
