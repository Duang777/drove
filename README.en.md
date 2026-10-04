<p align="center"><a href="README.md">中文</a></p>

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/logo-dark.svg">
    <img alt="drove" src="docs/assets/logo.svg" width="420">
  </picture>
</p>

<p align="center">
  <strong>A flight recorder and control tower for AI coding agents.</strong><br>
  Record every local terminal as replayable bytes, and mark Working, Blocked, Done, and Idle.
</p>

<p align="center">
  <a href="https://github.com/Duang777/drove/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/Duang777/drove/actions/workflows/ci.yml/badge.svg"></a>
  <a href="https://go.dev/dl/"><img alt="Go 1.24.2+" src="https://img.shields.io/badge/Go-1.24.2%2B-00ADD8?logo=go&logoColor=white"></a>
  <a href="LICENSE"><img alt="Apache-2.0" src="https://img.shields.io/github/license/Duang777/drove"></a>
</p>

Drove is a local daemon and CLI. It starts Claude Code, Codex, or any executable in a real PTY, appends the raw terminal bytes to SQLite, and reports one shared set of states.

The recorder works today: `drove log` replays bytes, and `drove timeline` shows state spans and Blocked jump points. The tower grid, scrubbable playback UI, push notifications, and phone approval are tracked by [Epic #31](https://github.com/Duang777/drove/issues/31) and have no UI yet. There is no release build and no TUI.

## Status

| Marker | Meaning |
| --- | --- |
| Shipped | Usable from `main` with the commands below |
| In progress | Partially merged; the issue is still open |
| Planned | No command or UI yet |

| Capability | Status | Where |
| --- | --- | --- |
| One PTY per agent, owned by `droved` | Shipped | `internal/pty` |
| `init` `up` `resume` `ps` `log` `timeline` `explain` `stop` `send` `hook` `web` `token rotate` `version` | Shipped | `cmd/drove` |
| Per-session Claude / Codex signal injection | Shipped | [#15](https://github.com/Duang777/drove/issues/15) |
| Raw terminal bytes, retained 30 days by default | Shipped | [#13](https://github.com/Duang777/drove/issues/13) |
| `drove log` byte replay, `--plain` strips control sequences | Shipped | |
| Unix local control, Host / Origin checks, cookie login, and token rotation | Shipped | [#21](https://github.com/Duang777/drove/issues/21) |
| WebSocket v1 events and v2 per-session terminal streams, input, and resize | Shipped | [#19](https://github.com/Duang777/drove/issues/19) |
| Web skeleton: list, start, stop, live events | Shipped | `web/` |
| Terminal emulation, query replies, screen rules, and `drove explain` | Shipped | [#14](https://github.com/Duang777/drove/issues/14), [spec 010](specs/010-terminal-screen-detection/spec.md) |
| State timeline, Blocked jumps, and exact terminal frames | Shipped | [#25](https://github.com/Duang777/drove/issues/25) |
| Control-tower grid | Planned | [#26](https://github.com/Duang777/drove/issues/26) |
| Push notifications | Planned | [#27](https://github.com/Duang777/drove/issues/27) |
| Approve, deny, or reply from a phone | Planned | [#28](https://github.com/Duang777/drove/issues/28) |
| Native resume, and process-group SIGTERM with a grace period before SIGKILL | Shipped | [#16](https://github.com/Duang777/drove/issues/16) |
| Agent processes that survive daemon exit | Planned | [#17](https://github.com/Duang777/drove/issues/17), [#18](https://github.com/Duang777/drove/issues/18) |
| `drove attach`, a terminal UI, xterm.js | Planned | [#20](https://github.com/Duang777/drove/issues/20) |
| Away brief, cross-session search, git worktree | Planned | [#29](https://github.com/Duang777/drove/issues/29), [#30](https://github.com/Duang777/drove/issues/30), [#23](https://github.com/Duang777/drove/issues/23) |

## Architecture

<p>
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/architecture.en-dark.png">
    <img alt="Architecture. drove CLI connects to droved through a Unix socket, and the browser console connects through loopback HTTP. droved owns the session. The session connects to detect, the SQLite event log, and the PTY. The PTY runs claude, codex, or another executable. The agent reaches drove hook through DROVE_SIGNAL variables, and the hook calls back to droved with the session token." src="docs/assets/architecture.en.png" width="720">
  </picture>
</p>

The CLI is a short-lived client. Once started, `droved` owns the PTY. State and output are written to SQLite before they are published to WebSocket subscribers. Vendor-specific behavior stays in `internal/adapter`.

## Quick start

Go 1.24.2 or newer is required. PTYs use `github.com/creack/pty`, which builds on Linux and macOS. Windows is not supported. CI runs on Ubuntu with Go 1.24.2 and current stable. There are no release binaries; build from source. Keep `drove` and `droved` in the same directory: the CLI looks beside itself for the daemon, and the daemon looks beside itself for the hook relay.

```bash
git clone https://github.com/Duang777/drove.git
cd drove
make build
export PATH="$PWD/bin:$PATH"

drove init
drove up /bin/cat --name demo
drove ps
drove send <agent-id> 'hello'
drove log <agent-id>
drove log <agent-id> --plain
drove timeline <agent-id>
drove explain <agent-id>
drove stop <agent-id>
drove web
drove version
```

`drove init` writes the default config to `~/.drove/config.json`. An existing file is overwritten. `data_dir` is the absolute path of `~/.drove`, created with mode `0700`.

`drove up` starts `droved` when nothing is listening. The daemon log is `<data_dir>/drove.log`. The `/bin/cat` example does not need Claude or Codex. Those vendors need their own CLIs on `PATH`:

```bash
drove up claude --name api --dir "$PWD"
drove up codex --oneshot
drove up claude --hooks required
```

### Commands

| Command | Behavior |
| --- | --- |
| `drove init` | Write the default `config.json`. Overwrites an existing file |
| `drove up <vendor\|command>` | Start a session. Vendor names are `claude` and `codex`. Any other string is an executable name, with no extra arguments |
| `drove resume <agent-id>` | Run the vendor-native resume for an eligible Claude or Codex session while preserving its Agent ID |
| `drove ps` | Print AGENT ID, NAME, VENDOR, MODE, STATE, PID, RESUMABLE. Prints `no agents running` when empty |
| `drove log <agent-id>` | Write terminal bytes to stdout. Does not print state events |
| `drove log <agent-id> --plain` | Strip control sequences with a streaming filter |
| `drove timeline <agent-id>` | Print state spans, output retention, and one-based Blocked jump points. `--json` prints the full response |
| `drove explain <agent-id>` | Print recent state decisions and the ephemeral bounded screen for an attached session |
| `drove send <agent-id> <text>` | Send that line plus a newline. Prints the byte count |
| `drove send <agent-id> --stdin` | Read stdin as-is, with no added newline |
| `drove stop <agent-id>` | Stop that session |
| `drove hook --vendor claude\|codex` | Used by the injected agent child. Reads one JSON document from stdin and exits 0 even when delivery fails |
| `drove web` | Issue a one-time login code over the Unix socket and open the embedded browser console |
| `drove token rotate` | Atomically rotate the control token without printing it |
| `drove version` | Print the version. `make build` fills it from `git describe`. Commit and build time stay `unknown` unless those ldflags are set |

Flags on `drove up`: `--name`, `--dir`, `--oneshot`, `--hooks off|auto|required`.

`drove send` accepts valid UTF-8 only, at most 64 KiB. The audit event stores the byte count, not the text. One `drove hook` JSON document is limited to 1 MiB. The hook command does not read the control token and does not start the daemon.

## How it works

### State

The state machine lives in `internal/agent`. Running sessions show `working`, `blocked`, `done`, and `idle`, plus the lifecycle states `pending`, `starting`, and `stopped`. `done` and `stopped` are terminal.

A per-session detector computes one decision. That decision is stored in SQLite, applied in memory, then broadcast. Sources apply in this order:

1. **Process.** Startup failure and exit override other signals. An interactive session ends as `stopped`. A successful `--oneshot` exit is `done`.
2. **Claude command hooks.** After the first valid hook signal is committed, that session is hook-authoritative. The event kind selects Working, Blocked, or an Idle candidate. Idle has a 1 second confirmation window that later activity can cancel.
3. **Codex notify.** This does not become hook authority and does not satisfy `--hooks required`. In fallback it is only a cancellable Idle candidate.
4. **Screen rules.** Before hooks activate, fallback uses stable Claude and Codex screen rules. After hooks are active, only two screen signals can change state: an approval prompt disappearing (Blocked to Working), and a Claude interrupt (Working to Idle). Other screen edges are stored with a `suppressed` outcome.

`--hooks auto` is the default for Claude and Codex. It waits 5 seconds, then enters fallback if no valid native hook arrived. In fallback, 60 seconds of output silence can become an Idle candidate. `--hooks off` does not inject a reporter. `--hooks required` stops the session as `stopped` if no valid native hook arrives within 5 seconds. generic cannot select `required`.

### Terminal screen and streaming

Each attached session has one terminal actor. It receives output only after
SQLite commits it, owns the x/vt emulator, answers terminal queries directly
through the PTY, and keeps a bounded in-memory screen. Query replies do not
enter output replay or input audit. Use `drove explain <agent-id>` to inspect
recent decisions and the current screen while the process is attached.

A connection without a WebSocket subprotocol keeps the v1 global event
stream. `Sec-WebSocket-Protocol: drove.v2` enables per-agent raw, events, and
snapshot subscriptions, plus input and resize on a writable raw attachment.
Any other explicit subprotocol is rejected before upgrade.

A v2 cursor contains the last consumed global event sequence in `seq` and the
next unconsumed session output byte in `next_offset`. JSON carries both values
as decimal strings. A client advances its cursor only after it applies a
message, then sends the full cursor when it reconnects. History and live data
both come from bounded SQLite reads rather than the lossy event Hub.

The per-session recording actor orders output and effective resize. Drove
resizes the PTY and x/vt before it commits `agent.resized`. Each v2 connection
has an 8 MiB outbound budget. If the queue overflows, the server reports
`slow_consumer` with the last written cursor and closes that connection with
code 1013. Other connections and event commits continue.

A snapshot subscription is a live-only preview. Each attachment receives at
most one frame every 500 ms, and a newer frame replaces an unread frame.
Snapshots carry `restorable:false`, stay out of the Hub and SQLite, and cannot
start an exact replay.

### Per-session injection

By default Drove only changes the process it starts. It does not edit `~/.claude`, `~/.codex`, or project config, and it does not accept workspace trust or hook trust for you.

- Claude Code: a hooks-only file is written to `<data_dir>/sessions/<agent-id>/claude-settings.json` with mode `0600`, in a `0700` directory, and loaded with `--settings`. The directory is removed after the process exits.
- Codex: `-c notify=[...]` is appended. No config file is written. notify only means a turn finished.

Both inherit `DROVE_AGENT_ID`, `DROVE_SIGNAL_URL`, and `DROVE_SIGNAL_TOKEN`. The signal endpoint accepts only loopback plus that session token. The event log does not store the raw payload, prompt, tool input, transcript, or token.

If the caller already passed Claude `--bare` or `--settings`, or a Codex `notify` setting, Drove leaves it alone and records the injection as skipped. If the `drove` relay cannot be found, `auto` still starts the session. Manual setup is in the [hook guide](docs/hooks.md). A persistent installer is [#24](https://github.com/Duang777/drove/issues/24) and is not implemented. Codex OSC 9 notifications are not injected either.

### Recording

New sessions store PTY output as `output.chunk` events with a byte offset. Older `output` line events remain readable; replay adds a newline to each of them. `drove log` keeps ANSI sequences and invalid bytes. Expired attachments produce no placeholder text.

`GET /api/v1/agents/{id}/timeline` projects half-open state spans, output
retention ranges, and Blocked occurrences from event envelopes.
`GET /api/v1/agents/{id}/timeline/blocked/{number}` returns one Blocked span and a
jump cursor with a 30-second lead-in. `GET /api/v1/agents/{id}/frame` requires
exactly one of `seq`, `at`, or `offset`, then replays output and resize records
from the 40x120 origin. The CLI exposes the timeline but leaves terminal
playback to #20.

Raw output is kept for 30 days by default. `0` keeps it indefinitely. Cleanup deletes byte attachments only. Sequence numbers, timestamps, offsets, and lengths stay. Cleanup enables SQLite `secure_delete` and truncates the WAL. It does not run `VACUUM`, so allocated database space may not shrink.

After attachment expiry, the timeline remains available and reports the
missing ranges. A frame that needs missing bytes returns HTTP 410 with
`output_expired` and the exact ranges. Exact frames use a 64-entry in-memory
LRU that expires with the Store retention generation. A 50 MiB cold replay is
still far above the 300 ms target. See the
[technical notes](docs/technical-notes.md#11-terminal-stream-and-replay) and
[#35](https://github.com/Duang777/drove/issues/35).

Once a database contains `output.chunk`, the oldest safe reader is [`d11f6c3`](https://github.com/Duang777/drove/commit/d11f6c3). After a version 3 screen signal is written, the oldest safe reader is [`f361ab5`](https://github.com/Duang777/drove/commit/f361ab5).

### Stop and restart

`drove stop` and daemon shutdown on SIGINT or SIGTERM first send SIGTERM to
the PTY's entire process group. The default grace period is 5 seconds. If the
group remains, Drove sends SIGKILL. It closes the PTY master only after reaping
the direct child, then waits for trailing output and exit callbacks.

After `drove up` returns, the foreground command is gone and the session lives in `droved`. That daemon is not detached from the controlling terminal and does not handle SIGHUP. When the daemon exits, its agent processes do not remain.

On the next start the daemon rebuilds projections from the event log. Sessions
whose PTY cannot be reattached are first closed as `stopped` with
`session interrupted by daemon restart; previous PTY is not reconnectable`.
Bytes already stored can still be read with `drove log`.

A valid native Claude or Codex signal stores one private resume reference. When
`drove ps` shows `RESUMABLE=true` after a stop, `drove resume <agent-id>` runs
`claude --resume` or `codex resume` under the same Agent ID and append-only
event stream. The reference is absent from Status, public API events, CLI
output, logs, and public replay. The absolute working directory from creation
is also stored only in the private event payload. Native resume starts there
so a changed daemon working directory cannot hide the vendor session.

`session.auto_resume_on_start` is off by default. When enabled, the daemon
waits until the API is accepting connections, then resumes sessions that were
nonterminal before restart and have a reference, in creation order. A session
stopped by the user is not resumed automatically. Keeping the original process
alive across daemon restart remains [#17](https://github.com/Duang777/drove/issues/17)
and [#18](https://github.com/Duang777/drove/issues/18).

## Supported agents

| Launch | Interactive | `--oneshot` | State signal |
| --- | --- | --- | --- |
| `drove up claude` | `claude` | `claude --print` | Per-session command hooks |
| `drove up codex` | `codex` | `codex exec` | Process-level notify; Idle only while in fallback |
| `drove up <executable>` | Runs that file with no extra arguments | Successful exit is `done` | No hooks, empty screen classifier, `--hooks required` is rejected |

ACP is not registered. `drove up acp` tries to execute a program named `acp`. It does not start an ACP adapter.

## Configuration

The config file is `~/.drove/config.json`. `DROVE_DATA_DIR` overrides `data_dir`. The daemon rejects an `api_bind` host that is not loopback.

```json
{
  "data_dir": "/home/you/.drove",
  "api_bind": "127.0.0.1:7373",
  "disable_tcp": false,
  "event_buffer": 1024,
  "console_origins": [
    "http://localhost:5173",
    "http://127.0.0.1:5173"
  ],
  "storage": {
    "output_retention_days": 30
  },
  "session": {
    "auto_resume_on_start": false,
    "termination_grace_seconds": 5
  }
}
```

`disable_tcp=true` disables the browser listener while keeping the Unix socket
and CLI available. `drove web` returns an error in this mode.

`agents.<vendor>.signal_injection` accepts only `auto` or `off`. That is the vendor default for injection. `off|auto|required` is the per-invocation `drove up --hooks` policy and is not stored in this field.

```json
{
  "agents": {
    "claude": {"signal_injection": "off"},
    "codex": {"signal_injection": "off"}
  }
}
```

The control token is 256 random bits, stored as lowercase hex in `<data_dir>/control.token` with mode `0600`. It is created the first time the daemon starts. The config file itself is mode `0644`.

## Web console

The daemon embeds and serves the production frontend from the loopback browser
listener. This command issues a one-time login code over the Unix socket and
opens a local URL with the code in its fragment:

```bash
drove web
```

The page exchanges the code for an HttpOnly, SameSite=Strict cookie and clears
the fragment. Browser JavaScript never reads `control.token`.

The Vite development server still proxies `/api` and `/ws` to `api_bind` and
adds a Bearer token from `control.token`. Start the daemon first:

```bash
drove ps
cd web
npm install
npm run dev
```

Open `http://127.0.0.1:5173`. The page can list, start, and stop sessions, and
it shows the WebSocket event stream. The repository includes a strict
`drove.v2` browser client, but the page does not use it yet. An `output.chunk`
row shows its offset and length, not a terminal. Replay bytes with `drove log`.

`npm run build` updates `internal/webui/dist/`. Commit the generated assets
with the frontend source so a clean checkout builds with only the Go toolchain.

A live terminal, scrubbable replay, and the tower grid are [#20](https://github.com/Duang777/drove/issues/20) and [#26](https://github.com/Duang777/drove/issues/26). The approved direction for #20 is web first.

## Roadmap

The approved MVP is [Epic #31, flight recorder + control tower](https://github.com/Duang777/drove/issues/31).

The screen model, WebSocket terminal stream, replay timeline, and control-plane
hardening are complete.
The next items are:

1. [#20](https://github.com/Duang777/drove/issues/20) web live terminal and replay
2. [#26](https://github.com/Duang777/drove/issues/26) control-tower grid
3. [#27](https://github.com/Duang777/drove/issues/27) push and [#28](https://github.com/Duang777/drove/issues/28) approve, deny, or reply from a phone
4. [#35](https://github.com/Duang777/drove/issues/35) exact x/vt checkpoints for large recordings

After the MVP: the shim in [#17](https://github.com/Duang777/drove/issues/17) / [#18](https://github.com/Duang777/drove/issues/18), then the away brief [#29](https://github.com/Duang777/drove/issues/29), full-text search [#30](https://github.com/Duang777/drove/issues/30), and worktrees [#23](https://github.com/Duang777/drove/issues/23). Structured state sources [#22](https://github.com/Duang777/drove/issues/22) and the persistent hook installer [#24](https://github.com/Duang777/drove/issues/24) are deferred.

## How this differs

Drove runs the vendor's own CLI and does not link a vendor SDK. What it offers today is a local event log, byte replay, and one set of state commands for Claude and Codex. It does not offer git worktrees, diff review, or a PR workflow.

[herdr](https://herdr.dev/) is a TUI-centered tool whose public positioning keeps terminals in a background server after the client closes. Drove stores raw bytes and state events in local SQLite. Keeping the process alive after the daemon exits is not what Drove does today. Single-vendor background sessions, phone approval, and the official apps each cover that vendor's own agent.

## Security

This is a single-user control plane on the local machine.

- `api_bind` must be loopback. Any other host is rejected while validating config.
- The CLI uses `<data_dir>/run/droved.sock`. Its directory is mode `0700`, the socket is mode `0600`, and Darwin / Linux reject peers with another UID.
- Browsers use loopback TCP. Every request must have an exact allowed Host. Requests with an Origin must match the allowlist, and cookie-authenticated writes and WebSockets require an Origin.
- Control APIs accept a `control.token` bearer or an HttpOnly cookie. `drove token rotate` atomically replaces the token. The previous generation has a 30-second grace period, then existing WebSockets receive a policy close.
- Static frontend files and one-time login exchange do not require an existing cookie. Login exchange requires an Origin, and a code can be used once.
- `/signal` accepts only loopback and that session's token. It does not accept the control-plane token.
- Any process running as the same OS user can read the token file. The token does not defend against other local processes, or against the agent itself.
- Input audit does not store the text. Raw output may contain source and secrets; attachments are deleted after the default 30 days.
- Signal tokens in the output stream are redacted with an equal-length replacement.
- There is no auto-approve. Phone approval is [#28](https://github.com/Duang777/drove/issues/28), and the default there is still not automatic approval.

The design notes are in [RFC-001, security considerations](docs/rfc-001-agent-state-and-control.md). The repository does not have a separate threat-model document.

## Docs

| Doc | What it is |
| --- | --- |
| [Hook guide](docs/hooks.md) | Session injection and hand-written native hooks |
| [RFC-001](docs/rfc-001-agent-state-and-control.md) | State, input, and security design |
| [spec 006](specs/006-hook-backed-state-detection/spec.md) | Hook-backed state detection |
| [spec 008](specs/008-session-signal-injection/spec.md) | Per-session injection |
| [spec 009](specs/009-raw-output-chunks/spec.md) | Raw bytes and retention |
| [spec 010](specs/010-terminal-screen-detection/spec.md) | Screen detection, query replies, and explain |
| [spec 011](specs/011-terminal-stream-replay/spec.md) | Terminal streams, cursors, timeline, and exact frames |
| [Technical notes](docs/technical-notes.md) | A point-in-time reading. Its opening says the first six sections are not current `main` |
| [AGENTS.md](AGENTS.md) | Directory responsibilities and engineering constraints |

## Contributing

Every directory has an `AGENTS.md`. The nearest one wins. Commit messages use Conventional Commits: `feat:`, `fix:`, `refactor:`, `docs:`, `test:`.

```bash
make test   # go test ./... -race, then a coverage summary
make vet
make lint   # gofmt + vet
```

Frontend checks:

```bash
npm --prefix web run typecheck
npm --prefix web run build
```

## License

[Apache-2.0](LICENSE).
