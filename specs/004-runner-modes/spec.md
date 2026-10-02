# Interactive and oneshot runner modes

Status: Approved

## Problem

Drove currently launches Claude with `claude --print` and Codex with
`codex exec`. Both commands run one task and exit. This conflicts with the
resident PTY model required by RFC-001, where an agent remains available for
later input and can move between `working`, `blocked`, and `idle`.

The current API does not record how a process was launched. `StartRequest`,
`Agent`, `Status`, and the persisted creation event have no runner mode.
`Manager.onExit` also sends every process exit to `stopped`, so a successful
one-shot task cannot finish at `done`.

Changing the command alone is not enough. Session shutdown uses the same
process exit callback as natural completion. Without an explicit stop cause,
a successful one-shot process that races with `Stop` or daemon shutdown can
be classified as `done` even though Drove requested termination.

Natural exits also leave the PTY in `Manager.sessions`. Status can therefore
report a PID after the process has exited.

## Goal

Add a durable runner mode with two values:

- `interactive`: launch the vendor's resident CLI and map every natural
  process exit to `stopped`.
- `oneshot`: launch the vendor's one-shot CLI and map a successful natural
  process exit to `done`.

New requests default to `interactive`. `drove up --oneshot` preserves the
current Claude and Codex commands.

The completed behavior must guarantee:

- Claude interactive runs `claude`.
- Claude oneshot runs `claude --print`.
- Codex interactive runs `codex`.
- Codex oneshot runs `codex exec`.
- Generic and custom commands keep their command and arguments unchanged.
- The mode is visible in status and survives event replay recovery.
- Old creation events without a mode recover as `oneshot`.
- Invalid request modes return HTTP 400 before any event or process is
  created.
- A user stop or daemon shutdown that records its stop cause before the exit
  claim finishes at `stopped`.
- Successful natural oneshot exit finishes at `done`.
- Natural interactive exit and failed natural exit finish at `stopped`.
- Natural exit removes the PTY from the live-session index.

## Non-goals

This change does not:

- Add REST, CLI, or Web input injection.
- Add terminal resize endpoints or interactive terminal rendering.
- Install Claude or Codex hooks.
- Add the RFC-001 signal hierarchy or a new detector package.
- Fix Blocked recovery or add idle timeouts.
- Add an explicit interactive `done` command.
- Reconnect PTYs or processes after daemon restart.
- Change the approved rule that every recovered non-stopped state, including
  historical `done`, becomes `stopped`.
- Change PTY line buffering or trailing-output ordering.
- Add graceful signal escalation or process-group cleanup.
- Replace `adapter.Registry.For` with a new launch-resolution service.
- Change custom command precedence or argument handling.
- Redesign general runtime persistence failure handling.
- Change the SQLite schema or add an event type.

## Assumptions

Interactive mode is supported on the Unix platforms already supported by
`creack/pty`.

The CLI and daemon normally come from the same build. A new CLI can send a
mode that an old daemon does not understand because the old `StartRequest`
ignores unknown JSON fields. Mixed-version behavior is not a release
guarantee.

One daemon owns each live PTY. A recovered session has no PTY and remains
subject to the existing startup reconciliation rule.

## Chosen approach

Add `agent.RunMode` as immutable Agent metadata. Session validates and
defaults the request, adapter maps the validated mode to vendor arguments,
and PTY remains unaware of both vendor and mode.

Keep the version 1 creation payload and add an optional `mode` field. The new
projector treats an absent field as legacy oneshot metadata. It rejects a
present but invalid field as corrupt history.

Replace the bare PTY value in `Manager.sessions` with a private runtime entry.
The entry records stop intent and whether the exit callback has claimed the
process exit. This gives natural exit, user stop, and daemon shutdown one
lock-protected ordering point.

```text
CLI or REST request
    |
    +--> session validates/defaults RunMode
    |
    +--> adapter.Runner.Command(mode)
    |       +--> Claude or Codex arguments
    |
    +--> session persists version 1 creation metadata with mode
    |
    +--> PTY runs the resolved command
            |
            +--> raw ExitInfo
                    |
                    +--> session decides done or stopped
```

## Required behavior

### Runner mode domain type

Define the mode in `internal/agent`:

```go
// RunMode controls command selection and natural-exit interpretation.
type RunMode string

const (
	RunModeInteractive RunMode = "interactive"
	RunModeOneshot     RunMode = "oneshot"
)

// ValidRunMode reports whether mode is a normalized supported value.
func ValidRunMode(mode RunMode) bool
```

`Agent` stores one `RunMode`. Add:

```go
func WithRunMode(mode RunMode) Option
func (a *Agent) RunMode() RunMode
```

`agent.New` defaults to `RunModeInteractive`. `agent.Restore` requires a valid
nonempty mode. The projector must supply the legacy fallback before calling
`Restore`.

The mode does not change while an Agent exists. The Agent remains the only
authority for both state and durable session metadata.

### Request validation and status

Add JSON tags to every `StartRequest` field and add `Mode`:

```go
type StartRequest struct {
	Vendor  string        `json:"vendor"`
	Name    string        `json:"name,omitempty"`
	Command string        `json:"command,omitempty"`
	Args    []string      `json:"args,omitempty"`
	Dir     string        `json:"dir,omitempty"`
	Mode    agent.RunMode `json:"mode,omitempty"`
}
```

`Manager.Start` normalizes the mode before command selection, event creation,
or PTY startup:

- Empty mode becomes `interactive`.
- Exact `interactive` and `oneshot` values are accepted.
- Every other value returns an error that wraps `session.ErrInvalidMode`.
- Validation does not trim whitespace or change letter case.

An invalid mode creates no Agent, event, or process.

Add the normalized mode to `Status`:

```go
type Status struct {
	AgentID   string        `json:"agent_id"`
	Name      string        `json:"name"`
	Vendor    string        `json:"vendor"`
	Mode      agent.RunMode `json:"mode"`
	State     agent.State   `json:"state"`
	PID       int           `json:"pid,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	LastError string        `json:"last_error,omitempty"`
}
```

Status reads the mode from `Agent.RunMode()`. Manager must not keep a second
mode map.

### Adapter command mapping

Change the existing Runner method:

```go
type Runner interface {
	Vendor() string
	Command(mode agent.RunMode) (name string, args []string)
}
```

The built-in mapping is:

| Vendor | Interactive | Oneshot |
| --- | --- | --- |
| Claude | `claude` | `claude --print` |
| Codex | `codex` | `codex exec` |
| Generic | no default command | no default command |

Session passes only a validated mode to `Runner.Command`.

`StartRequest.Command` remains a complete override after adapter command
selection. `StartRequest.Args` remains its argument list. Neither mode adds,
removes, reorders, or rewrites custom command arguments.

An empty or unknown vendor keeps the current generic fallback behavior.
Generic still requires an explicit command. This validation remains in
session.

The adapter does not receive `StartRequest`, a directory, a stop cause, or an
exit policy.

### Creation metadata

Keep the creation metadata at version 1:

```go
type createdPayload struct {
	Version int            `json:"version"`
	Name    string         `json:"name"`
	Vendor  string         `json:"vendor"`
	Mode    *agent.RunMode `json:"mode,omitempty"`
}
```

New writers always set `Mode` to the normalized value. The pointer is used
only by the persistence boundary:

- `nil` means that an old version 1 event omitted the field.
- A nonnil valid value is restored exactly.
- A nonnil empty or unknown value is corrupt history.

Recovery applies different defaults at two boundaries:

```text
new request with no mode          -> interactive
old creation event with no mode   -> oneshot
history with no creation event    -> oneshot
```

The historical fallback is `oneshot` because the previous built-in Claude and
Codex commands were one-shot commands. Old generic commands cannot be
classified exactly, but recovery never reconnects their processes. The
fallback affects metadata only.

Add the mode to `sessionDraft` and `agent.RestoreSnapshot`. A projection error
for invalid persisted mode must include the event sequence and session ID.

The existing recovery behavior remains unchanged after projection:

- Every historical state except `stopped` transitions to `stopped`.
- Historical `done` receives no restart-interruption error.
- No historical PID is restored.

### Live process ownership

Replace the PTY map value with a private entry:

```go
type stopCause uint8

const (
	stopCauseNone stopCause = iota
	stopCauseUser
	stopCauseShutdown
)

type runningSession struct {
	process     *pty.Session
	stopCause   stopCause
	exitClaimed bool
}

type Manager struct {
	// Existing fields omitted.
	sessions map[agent.ID]*runningSession
}
```

`stopCause` is current-daemon coordination state. It is not Agent metadata and
is not persisted.

The Manager mutex protects `stopCause`, `exitClaimed`, and membership in
`sessions`.

The PTY exit callback captures the exact `runningSession` pointer created for
that start. An exit claim succeeds only when the map still contains that
pointer and no previous callback has claimed it.

`onExit` runs inside the PTY wait loop, so it cannot call synchronous
`Session.Close` without waiting on itself. After `onExit` and the read loop
return, the PTY wait loop closes the master file and marks the Session closed.
A later `Session.Close` remains idempotent.

### Stop ordering

`Manager.Stop` records `stopCauseUser` under the Manager mutex before it calls
`pty.Session.Close`.

The exit claim defines the concurrent ordering:

- If `Stop` records its cause first, the exit is intentional and ends at
  `stopped`.
- If `onExit` claims the exit first, natural-exit policy wins.

The entry stays in `Manager.sessions` until `onExit` records the terminal
state. `onExit` then removes the entry only if the map still contains the same
pointer.

`Stop` returns success when the session is already detached and its Agent is
`done` or `stopped`. This keeps repeated stop calls idempotent.

Manager shutdown must mark every unclaimed attached entry with
`stopCauseShutdown` while holding one lock. It must mark all entries before it
closes the first PTY. The existing stable close order and callback drainage
remain unchanged.

### Exit decision

Implement the decision table as a private pure session function:

```go
type exitDecision struct {
	target       agent.State
	reason       string
	errorMessage string
}

func decideExit(
	mode agent.RunMode,
	cause stopCause,
	info pty.ExitInfo,
) exitDecision
```

The complete table is:

| Exit source | Mode | Process result | Final state | Persist error |
| --- | --- | --- | --- | --- |
| User stop that wins the exit claim | Either | Any | `stopped` | No |
| Daemon shutdown that wins the exit claim | Either | Any | `stopped` | No |
| Natural | Oneshot | Code 0 and nil error | `done` | No |
| Natural | Oneshot | Nonzero or wait error | `stopped` | Yes |
| Natural | Interactive | Code 0 and nil error | `stopped` | No |
| Natural | Interactive | Nonzero or wait error | `stopped` | Yes |
| PTY startup failure | Either | Start error | `stopped` | Existing error path |

For a failed natural exit, session sets `Agent.LastError` and persists an
error event before the state transition. The error event uses the process
wait error. A requested stop does not persist the expected kill error.

`onExit` applies the decision, waits for the synchronous state-change hook,
and then detaches the matching runtime entry. This order prevents Status from
observing a nonterminal Agent without a PTY.

Add `idle -> done` to the Agent transition table. A successful oneshot can be
idle when a future detector is present. Existing `working -> done` and
`blocked -> done` transitions remain valid.

### Heuristic Done behavior

The current Claude heuristic can emit `StateDone` while the process is still
alive.

For interactive Agents, `Manager.onOutput` must ignore `StateDone` hints.
Interactive Done requires a future explicit signal and remains outside this
phase.

For oneshot Agents, keep the existing Done hint behavior. Natural process exit
is still final:

- A successful exit leaves an existing `done` state unchanged.
- A failed exit corrects `done` to `stopped` and persists the error.
- User stop or daemon shutdown changes `done` to `stopped`.

Blocked hints remain unchanged in both modes.

### CLI behavior

Add `--oneshot` to `drove up`.

```bash
drove up claude
# launches: claude

drove up --oneshot claude
# launches: claude --print

drove up codex
# launches: codex

drove up --oneshot codex
# launches: codex exec
```

The flag maps directly to a mode value:

```text
flag absent  -> interactive
flag present -> oneshot
```

The CLI must not reproduce vendor argument rules.

`drove up` includes `mode` in its started message. `drove ps` adds a MODE
column so operators can interpret the state and expected exit behavior.

For a custom command, `--oneshot` changes only the mode:

```bash
drove up /usr/local/bin/my-agent
drove up --oneshot /usr/local/bin/my-agent
```

Both commands execute `/usr/local/bin/my-agent` with the same arguments.

### REST and Web contracts

The REST request accepts:

```json
{
  "vendor": "codex",
  "name": "review",
  "mode": "oneshot"
}
```

Omitting `mode` starts an interactive session.

`api.Server.handleCreate` maps `session.ErrInvalidMode` to HTTP 400. Other
start failures keep their current status mapping in this phase.

Adding JSON tags makes the Go client send lowercase keys that match the Web
contract. Go's JSON decoder continues to accept the old capitalized field
names case-insensitively.

Update the TypeScript contract:

```ts
export type RunMode = 'interactive' | 'oneshot'

export interface StartRequest {
  vendor: string
  name?: string
  command?: string
  args?: string[]
  dir?: string
  mode?: RunMode
}

export interface AgentStatus {
  agent_id: string
  name: string
  vendor: string
  mode: RunMode
  state: AgentState
  pid?: number
  created_at: string
  updated_at: string
  last_error?: string
}
```

The Web UI does not add a runner-mode control in this phase. The type change
keeps the shared JSON contract accurate.

## Ownership

Package responsibilities remain:

- `internal/agent` owns valid mode values and immutable Agent metadata.
- `internal/adapter` owns vendor command names and mode-specific arguments.
- `internal/session` owns request defaults, request validation, custom command
  precedence, creation metadata, recovery fallback, stop intent, and exit
  policy.
- `internal/pty` owns command execution and raw `ExitInfo`.
- `internal/api` owns JSON decoding and HTTP error mapping.
- `internal/client` remains a typed JSON transport.
- `cmd/drove` owns the `--oneshot` flag and CLI display.
- `web/src/api` mirrors the Go JSON contract.
- `internal/event` and `internal/store` keep the creation payload opaque.

No new package or dependency edge is required. Adapter already imports agent
for state hints.

## Alternatives considered

### Add a registry-wide launch resolver

A resolver could combine vendor lookup, mode selection, custom command
precedence, generic validation, and heuristic lookup in one adapter call.

Rejected for Phase 0. It adds `Command`, `ResolveRequest`, and `ResolvedRun`
contracts and moves general request validation into adapter. The existing
`Runner.Command(mode)` method can hide the required vendor differences with a
smaller change.

### Define mode in session or adapter

A session-owned type would reverse the current adapter dependency. An
adapter-owned type would make durable Agent metadata and recovery depend on a
vendor integration package.

Rejected. `internal/agent` already owns durable runtime metadata and is an
existing dependency of adapter and session.

### Version the creation payload as version 2

A version bump would make the previous projector reject events written by the
new binary.

Rejected. Mode is additive, and an optional version 1 field preserves both
forward reading and binary rollback.

### Infer stop intent from ExitInfo

A killed process normally returns a nonzero result, but a naturally failed
process can return the same result.

Rejected. `ExitInfo` describes the process result, not why Drove initiated
the exit.

## Compatibility

### Event history

- The creation payload remains version 1.
- Old version 1 payloads without mode recover as oneshot.
- New version 1 payloads include mode.
- Old binaries ignore the added JSON field.
- No SQLite migration or backfill runs.

### REST and WebSocket

- Old request bodies without mode remain valid and now default to interactive.
- Status gains one additive field.
- Existing routes and event envelope fields do not change.
- Old clients that ignore unknown response fields continue to work.

### Runtime

- `--oneshot` preserves the old built-in Claude and Codex commands.
- Generic and custom commands retain their current argv behavior.
- PTY receives the same resolved command shape as before.
- Daemon restart still stops every recovered historical session.

### Rollback

A previous binary can read events created by the new binary because the
payload version remains 1 and Go ignores unknown JSON fields.

After rollback, the old binary does not expose mode and interprets all future
starts with its old one-shot command behavior. Sessions from the newer binary
are already stopped during daemon shutdown, so rollback does not reconnect an
interactive process.

## Expected files

Implementation is expected to touch these existing files:

- `internal/agent/agent.go`
- `internal/agent/agent_test.go`
- `internal/agent/AGENTS.md`
- `internal/adapter/adapter.go`
- `internal/adapter/vendors.go`
- `internal/adapter/vendors_test.go`
- `internal/adapter/AGENTS.md`
- `internal/session/session.go`
- `internal/session/session_test.go`
- `internal/session/projection.go`
- `internal/session/projection_test.go`
- `internal/session/AGENTS.md`
- `internal/pty/pty.go`
- `internal/pty/pty_test.go`
- `internal/pty/AGENTS.md`
- `internal/api/server.go`
- `cmd/drove/main.go`
- `cmd/drove/AGENTS.md`
- `web/src/api/types.ts`
- `docs/technical-notes.md`

Implementation may add focused API or CLI test files because those packages
currently have none. It must not add a production package or dependency.

## Test plan

### Agent tests

- `ValidRunMode` accepts only the two defined values.
- `agent.New` defaults to interactive.
- `WithRunMode` sets oneshot.
- `agent.Restore` accepts both modes.
- Restore rejects an empty or unknown mode after projection.
- `idle -> done` is legal.
- Unrelated state transitions remain unchanged.

### Adapter tests

Table-test every built-in mapping:

```text
claude interactive -> claude []
claude oneshot     -> claude [--print]
codex interactive  -> codex []
codex oneshot      -> codex [exec]
generic interactive -> "" []
generic oneshot     -> "" []
```

The generic cases prove that adapter does not add mode-specific arguments to
a custom command.

### Session request and persistence tests

- Omitted request mode normalizes to interactive before command selection.
- Invalid mode returns `ErrInvalidMode`.
- Invalid mode writes no event and starts no process.
- Custom command and arguments reach PTY unchanged in both modes.
- New creation metadata remains version 1 and contains the normalized mode.
- Status reads mode from Agent.

### Recovery tests

- Old version 1 metadata without mode restores as oneshot.
- New version 1 metadata restores both valid modes.
- Legacy history without creation metadata restores as oneshot.
- Present empty or unknown persisted mode blocks bootstrap.
- Projection errors contain event sequence and session ID.
- Historical done still becomes stopped without an interruption error.
- A new payload can be decoded by the old version 1 struct.

### Exit and concurrency tests

Use real short commands through PTY:

- Interactive `/bin/sh -c 'exit 0'` ends at stopped and has no PID.
- Oneshot `/bin/sh -c 'exit 0'` ends at done and has no PID.
- Natural `exit 7` ends at stopped and persists an error.
- User stop of a live oneshot `/bin/cat` ends at stopped.
- User stop does not persist `signal: killed` as LastError.
- Manager shutdown of a live oneshot process ends at stopped.
- Manager shutdown marks all sessions before closing the first PTY.
- A natural exit racing Stop records one terminal outcome according to the
  exit claim.
- Repeated Stop after done or stopped succeeds.
- Interactive output that matches the Claude Done heuristic does not mark a
  live process done.
- Natural PTY completion closes the master and makes later writes and resizes
  return `pty.ErrClosed`.
- Focused tests pass repeatedly under the race detector.

### API, CLI, and Web tests

- Lowercase JSON without mode starts interactive.
- Lowercase JSON with oneshot is accepted.
- Invalid mode returns HTTP 400.
- PTY startup failure remains HTTP 500.
- `sessionStartRequest` maps the flag to the expected mode.
- The custom command path does not rewrite the command.
- Cobra exposes `--oneshot`.
- `drove up` and `drove ps` display mode.
- TypeScript type checking and build pass.

### Full verification

Run:

```bash
gofmt -w internal/agent internal/adapter internal/session internal/api cmd/drove
go test ./internal/agent ./internal/adapter ./internal/session -race -count=20
go test ./internal/api ./cmd/drove -race
go test ./... -race -count=1
go vet ./...
make build
npm --prefix web run typecheck
npm --prefix web run build
git diff --check
```

### Manual verification

Use an isolated data directory:

1. Start `droved`.
2. Start Claude without `--oneshot`.
3. Confirm the process command is `claude` and the status mode is
   `interactive`.
4. Stop the session and confirm the final state is `stopped`.
5. Start a generic command equivalent to `/bin/sh -c 'exit 0'` in oneshot
   mode.
6. Confirm the final state is `done` and Status has no PID.
7. Restart `droved` with the same database.
8. Confirm both sessions recover with their persisted modes.
9. Confirm the historical `done` session is reconciled to `stopped` under the
   existing restart rule.

Codex command mapping is covered by adapter tests when Codex is not installed
on the development host.

## Commit boundaries

Implementation must be delivered in two independently verified commits. Push
each commit after its verification passes.

### Commit 1: typed runner mode and durable metadata

- Add `agent.RunMode` and immutable Agent metadata.
- Add mode and lowercase JSON tags to request and status.
- Add `Runner.Command(mode)` and vendor command mappings.
- Add the CLI flag and mode display.
- Add the TypeScript contract fields.
- Persist mode in version 1 creation metadata.
- Recover old metadata as oneshot.
- Map invalid request mode to HTTP 400.
- Update package guides.
- Add domain, adapter, request, projection, API, and CLI tests.

Suggested commit:

```text
feat: add interactive and oneshot runner modes
```

Verification:

```bash
go test ./internal/agent ./internal/adapter ./internal/session ./internal/api ./cmd/drove -race
go test ./... -race -count=1
go vet ./...
npm --prefix web run typecheck
npm --prefix web run build
make build
git diff --check
```

### Commit 2: mode-aware exit semantics

- Add the private running-session entry and stop causes.
- Claim concurrent exits under the Manager mutex.
- Mark all shutdown causes before closing PTYs.
- Apply the pure exit decision table.
- Persist unexpected natural process errors.
- Remove naturally exited PTYs from the live index.
- Keep Stop idempotent after done or stopped.
- Ignore interactive Done hints.
- Add `idle -> done`.
- Update the technical note.
- Add focused process and race tests.

Suggested commit:

```text
fix: apply runner mode exit semantics
```

Verification:

```bash
go test ./internal/agent ./internal/session -race -count=20
go test ./... -race -count=1
go vet ./...
make build
git diff --check
```

## Acceptance criteria

- `drove up claude` resolves to `claude`.
- `drove up --oneshot claude` resolves to `claude --print`.
- `drove up codex` resolves to `codex`.
- `drove up --oneshot codex` resolves to `codex exec`.
- Generic and custom argv values are unchanged by mode.
- Omitted new-request mode becomes interactive.
- Invalid request mode returns HTTP 400 with no persisted event.
- Status and CLI output expose the normalized mode.
- New version 1 creation events contain mode.
- Old creation events and state-only histories recover as oneshot.
- Old binaries can decode new version 1 creation payloads.
- Successful natural oneshot exit reaches done.
- Natural interactive exit reaches stopped.
- Failed natural exit reaches stopped and persists the process error.
- A user stop or daemon shutdown that wins the exit claim reaches stopped in
  either mode.
- Concurrent Stop and natural exit have one lock-defined winner.
- Natural exit removes the PID from Status.
- Natural exit releases the PTY master without waiting for garbage collection.
- Interactive Done hints do not mark a live process done.
- Restart still reconciles historical done to stopped.
- PTY does not import or inspect mode.
- No new production package or dependency is added.
- Focused repeated race tests, full race tests, vet, Go builds, Web checks, and
  manual verification pass.
- Each implementation commit is pushed after its own verification.

## Risks and controls

### Interactive sessions cannot receive input through the public API yet

Phase 0 makes the vendor process resident before Phase 2 exposes input.

Control: PTY already supports `Write`, and Phase 0 keeps the session attached.
Input endpoints remain a separate reviewed change.

### Legacy generic mode is not knowable

Old generic commands may have been resident or short-lived.

Control: recover them as oneshot metadata because the old built-in behavior
was one-shot. Recovery does not reconnect them, so the fallback cannot change
process behavior.

### Done hints can precede oneshot process exit

The existing Claude heuristic can mark a oneshot Agent done before the process
has exited.

Control: the exit decision remains final. A failed exit corrects done to
stopped. Removing oneshot Done hints belongs with the Phase 1 signal work.

### Stop and natural exit can happen together

A process can exit while a user or daemon requests stop.

Control: `stopCause` and `exitClaimed` share the Manager mutex. The first
operation recorded under that lock determines the lifecycle outcome.

### Mode is additive inside a version 1 payload

A strict version bump would make rollback fail even though the new field is
optional for old readers.

Control: keep version 1, require new writers to include mode, and use a pointer
only while decoding so missing and invalid values remain distinct.

## Rollback

The change has no database migration and no new event type.

Reverting both implementation commits restores the old one-shot command and
exit behavior. Existing creation events remain readable because their version
is still 1.

Reverting only Commit 2 keeps mode selection and persistence but restores the
old all-exits-to-stopped behavior. Reverting only Commit 1 is not supported
while Commit 2 remains because the exit policy depends on `agent.RunMode`.

## Approval gate

Implementation starts only after the owner approves this spec.
