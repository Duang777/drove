# Terminal overview TUI checklist

Related issue: [#41](https://github.com/Duang777/drove/issues/41)

## Architecture

- [ ] `cmd/drove` only registers `tui`, creates the client, and calls
  `clitui.Run`.
- [ ] `internal/clitui` has an `AGENTS.md`.
- [ ] `internal/clitui` does not import API, store, PTY, terminal emulator, or
  adapter implementations.
- [ ] `session.Status` remains the only fleet state projection.
- [ ] The TUI does not parse screen text or event payloads to infer state.
- [ ] The TUI does not add a daemon route or protocol message.

## Fleet

- [ ] `client.List` runs immediately at startup.
- [ ] The poll interval is 500 milliseconds.
- [ ] Only one list request runs at a time.
- [ ] One trailing refresh is retained while a list request runs.
- [ ] A successful response replaces the complete local fleet.
- [ ] A failed response keeps the last successful fleet and shows the error.
- [ ] Blocked rows form a stable prefix.
- [ ] Filters cover all, pending, starting, working, blocked, idle, done, and
  stopped.
- [ ] Selection uses Agent ID and a clamped fallback index.
- [ ] Selection survives refresh reorder.
- [ ] An empty visible list clears selection and disables row actions.

## Preview

- [ ] Only the selected Agent has a snapshot subscription.
- [ ] The subscription sets only Agent ID and snapshot mode.
- [ ] The preview actor owns the stream and its only `Next` caller.
- [ ] A replacement cancels and joins the old generation before opening the
  new stream.
- [ ] Target requests and preview events coalesce to their latest value.
- [ ] Every preview event carries Agent ID and generation.
- [ ] The model rejects stale preview events.
- [ ] Reconnect waits are 250 milliseconds, 500 milliseconds, 1 second, and
  then at most 2 seconds.
- [ ] A valid snapshot resets reconnect delay.
- [ ] Retry stops when the generation or TUI context ends.
- [ ] `Close` is idempotent and waits for the active worker.
- [ ] The preview renders daemon lines without terminal emulation.
- [ ] The preview shows dimensions, capture time, and truncation.
- [ ] A stopped or unavailable session has a neutral empty state.
- [ ] Overview resize sends no terminal resize request.

## Interaction

- [ ] The selected row has a non-color focus marker.
- [ ] The filter has a visible focused state.
- [ ] `up`, `down`, `j`, and `k` move selection.
- [ ] `f` focuses the filter; arrows change it.
- [ ] `a` starts writable attach.
- [ ] `r` starts read-only attach.
- [ ] Attach uses `tea.Exec` and `cliattach.Run`.
- [ ] Attach return keeps the selected Agent ID.
- [ ] `s` opens a focused one-line editor.
- [ ] Send adds exactly one newline.
- [ ] An empty editor sends one newline.
- [ ] Send enforces `session.MaxInputBytes` after adding the newline.
- [ ] `x` opens stop confirmation for the selected Agent ID.
- [ ] Only `y` confirms stop.
- [ ] `n` and `Esc` cancel stop.
- [ ] `e` loads a typed explanation.
- [ ] Long explanations scroll in a viewport.
- [ ] Action results cannot update a different selected Agent's detail pane.
- [ ] The footer shows keys for the current focus.
- [ ] Narrow terminals use a stacked layout.

## Cleanup

- [ ] Bubble Tea owns overview resize.
- [ ] `cliattach` owns attach resize and raw terminal state.
- [ ] Repeated attach returns restore the Bubble Tea terminal.
- [ ] Parent context cancellation stops polls, actions, retries, and preview
  work.
- [ ] TUI exit closes the active snapshot stream.
- [ ] TUI exit waits for the preview actor.
- [ ] TUI exit never calls `client.Stop`.
- [ ] Remote agents remain running after TUI exit.

## Tests

- [ ] Pure tests cover row copying, filters, stable sorting, and selection.
- [ ] Model tests cover every key path and visible focus.
- [ ] Model tests cover polling without overlap and one trailing refresh.
- [ ] Preview tests fail if two `Next` calls overlap.
- [ ] Preview tests cover replacement, stale frames, reconnect, and close.
- [ ] Ten-session state refresh completes before a one-second deadline.
- [ ] Writable and read-only attach have separate acceptance cases.
- [ ] Resize and exit cleanup use completion channels under `-race`.
- [ ] A pseudo-terminal test covers repeated `tea.Exec` handoff and restoration.
- [ ] `cmd/drove` tests command registration, argument errors, and delegation.

## Documentation

- [ ] `README.md` and `README.en.md` list `drove tui`.
- [ ] `cmd/drove/AGENTS.md` describes the thin command boundary.
- [ ] Package documentation names the polling and preview ownership rules.
- [ ] Keyboard help in the UI matches implemented keys.

## Verification

- [ ] `gofmt -w .`
- [ ] `go test ./internal/clitui ./cmd/drove -race -count=1`
- [ ] `go test ./... -race -count=1`
- [ ] `go vet ./...`
- [ ] `make build`
- [ ] `npm --prefix web run test --if-present -- --run`
- [ ] `npm --prefix web run typecheck`
- [ ] `npm --prefix web run build`
- [ ] `git diff --check`
- [ ] Real PTY smoke test shows the fleet, attach return, and clean quit.
