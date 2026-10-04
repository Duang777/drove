# Terminal attach and Web playback checklist

## Approval and scope

- [x] Issue #20 and the Web-first Epic #31 comment define the delivery.
- [x] The batch includes the Web detail view and `drove attach`.
- [x] The existing `drove ps` remains the terminal list.
- [x] A Bubble Tea fleet list moves to a linked follow-up issue.
- [x] Issue #35 owns server-side exact replay checkpoints.
- [x] `Frame` and `Snapshot` remain non-restorable previews.

## Ownership

- [x] `internal/session` remains the owner of live attachments and resize
  arbitration.
- [x] `internal/recording` remains the owner of persisted replay facts.
- [x] `internal/term` remains the only Go terminal-emulator owner.
- [x] `internal/cliattach` owns the local TTY lease and attach pumps.
- [x] `SessionTape` owns the browser origin prefix and timestamp correlation.
- [x] `TerminalSessionController` owns xterm, streams, reconnect, seek,
  playback, input, resize, and teardown.
- [x] React receives no xterm instance, terminal input callback, resize
  callback, or transport cursor.
- [x] Cobra contains no terminal lifecycle or stream-pump logic.

## Attachment intent and audit

- [x] Omitted `writable` means a recording reader.
- [x] `writable:false` means a read-only user attachment.
- [x] `writable:true` means a writable user attachment.
- [x] Selector presence never expresses attachment intent.
- [x] Event draft validation accepts `agent.attachment` before a writer emits
  it.
- [x] Store event-type filters accept `agent.attachment` envelopes.
- [x] Recovery treats `agent.attachment` as a known non-state event.
- [x] The browser event union accepts `agent.attachment`.
- [x] The audit payload contains only version, action, and access.
- [x] Successful user setup writes one attached event.
- [x] Every attachment removal path writes one detached event.
- [x] Duplicate close paths do not write duplicate detach events.
- [x] Recording-reader and snapshot subscriptions write no attachment audit.
- [x] Existing `agent.input` byte-count audit remains unchanged.

## CLI attach

- [x] `drove attach <agent-id>` opens a writable raw subscription.
- [x] `--read-only` sends no input or viewport.
- [x] Attach requires a terminal on stdin and enters raw mode.
- [x] Stdin reads are at most 32 KiB.
- [x] An incomplete UTF-8 suffix survives across reads.
- [x] Every input request is valid UTF-8 and at most 64 KiB.
- [x] Ctrl-C reaches the remote agent.
- [x] Ctrl-Q detaches locally and never reaches the remote agent.
- [x] Writable attach sends the initial viewport and `SIGWINCH` changes.
- [x] Read-only attach does not install resize handling.
- [x] Stream EOF and cancellation unblock a pending stdin read.
- [x] Every exit path closes resources and restores the TTY exactly once.
- [x] Local detach leaves the remote agent running.

## Browser recording

- [x] REST and WebSocket boundaries parse untrusted JSON from `unknown`.
- [x] Canonical decimal strings become `bigint` domain values.
- [x] `SessionTape` accepts domain operations instead of transport messages.
- [x] Raw operations and event timestamps correlate by sequence.
- [x] The exact frontier advances only when both records exist.
- [x] Identical reconnect duplicates are accepted.
- [x] Conflicting duplicates and output gaps are rejected.
- [x] Recorded time cannot regress in sequence order.
- [x] Timeline spans and Blocked markers remain authoritative REST data.
- [x] The tape freezes before it exceeds the 64 MiB byte limit.
- [x] The tape never evicts the origin prefix.
- [ ] Live rendering continues after the local replay limit.
- [x] HTTP 410 becomes a typed `OutputExpiredError`.
- [x] Neither a `Frame` nor a `Snapshot` can seed exact replay.

## Live terminal and reconnect

- [ ] The live xterm persists for the detail-page lifetime.
- [ ] Raw and event modes track separate locally applied cursors.
- [ ] A stream callback advances its cursor only after terminal application.
- [ ] Writable input stays disabled until raw `caught_up`.
- [ ] Reconnect keeps the live xterm and resumes from local cursors.
- [ ] Server-written cursors do not override local application state.
- [ ] Reconnect backoff is bounded and cancelable.
- [ ] Generation checks reject stale callbacks.
- [ ] Terminal output writes and durable resizes do not overlap.

## Seek and playback

- [ ] Slider movement updates labels without sending frame requests.
- [ ] A committed seek owns at most one current frame request.
- [ ] A newer seek aborts the previous frame request and replay build.
- [ ] Every exact seek creates a fresh replay xterm at 40x120.
- [ ] Exact replay applies output and resize from origin in sequence order.
- [ ] Replay work yields between bounded batches.
- [ ] Playback awaits each output write or resize before the next timer.
- [ ] Playback supports pause and `0.5`, `1`, `2`, and `4` speeds.
- [ ] Pause and speed changes resume at the current replay cursor.
- [ ] Recorded idle gaps remain part of playback time.
- [ ] Returning to live reveals output received during replay.
- [ ] Frame previews remain labeled and separate from xterm state.

## Input, resize, and teardown

- [ ] The controller owns xterm `onData`.
- [ ] Browser input splits on UTF-8 boundaries and stays within 64 KiB.
- [ ] Browser input acknowledgements are serialized.
- [ ] Fit measurement proposes dimensions without mutating xterm.
- [ ] The controller keeps at most one pending resize proposal.
- [ ] Live xterm resizes only after the durable resize record arrives.
- [ ] Replay applies only recorded sizes.
- [ ] Disposal invalidates stream and replay generations.
- [ ] Disposal aborts fetches, timers, replay, and queued input.
- [ ] Disposal disconnects observers and closes the stream.
- [ ] Disposal releases both xterms exactly once.
- [ ] Stale callbacks cannot mutate a disposed controller.

## Web experience

- [ ] `/?agent=<id>` opens the detail view without a new router.
- [ ] Browser Back returns to the fleet without a full reload.
- [ ] Selecting a fleet row opens its detail view.
- [ ] The detail header shows identity, state, working directory, and
  connection status.
- [ ] The terminal is the primary visual area.
- [ ] The playback rail shows state spans and Blocked markers.
- [ ] Controls cover live, replay, seek, play, pause, speed, retry, and
  go-live actions.
- [ ] Loading, reconnecting, preview, expired, local-limit, and error states
  are visible.
- [ ] Icon buttons use Lucide icons and have accessible names.
- [ ] Focus is visible and controls remain at least 40 px.
- [ ] Reduced-motion preferences disable nonessential transitions.
- [ ] Desktop, 375 px, and 320 px layouts contain all text and controls.
- [ ] The page has no horizontal overflow.
- [ ] A wide replay terminal scrolls inside its own viewport.

## Privacy and compatibility

- [ ] WebSocket v1 behavior remains unchanged.
- [ ] Existing REST response shapes remain unchanged.
- [ ] Raw bytes come only from committed, token-redacted output.
- [ ] No event stores input text, terminal screen data, or a control token.
- [x] Attachment events store no attachment ID or client identity.
- [ ] Browser terminal and replay state remain memory-only.
- [ ] Production frontend code never reads the control token.
- [ ] No vendor-specific behavior appears outside `internal/adapter`.

## Verification

- [x] Reader-first attachment event tests pass.
- [x] Attachment teardown and exactly-once audit tests pass.
- [x] CLI UTF-8 framing and cleanup unit tests pass.
- [x] A pseudo-terminal attach acceptance test passes.
- [ ] `SessionTape` property and limit tests pass.
- [ ] Controller fake-clock, reconnect, seek, and disposal tests pass.
- [ ] Browser tests pass for desktop, 375 px, and 320 px viewports.
- [ ] A real daemon and PTY pass live, input, resize, reconnect, and replay
  acceptance.
- [x] Focused packages pass 20 race-enabled repetitions.
- [x] `go test ./... -race -count=1` passes.
- [x] `go vet ./...` passes.
- [x] `make build` passes.
- [ ] Web tests, typecheck, and build pass.
- [x] `git diff --check` passes.
- [ ] Both remote CI lanes pass.
- [ ] Every implementation commit is pushed before the next begins.
- [ ] A Bubble Tea follow-up issue is linked from Issue #20.
- [ ] Issue #20 contains final evidence and is closed.
