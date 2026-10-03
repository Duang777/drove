# Session signal injection

Status: Approved for implementation

Approved: 2026-10-03

Approval source: the owner decisions and acceptance criteria recorded in
[Issue #15](https://github.com/Duang777/drove/issues/15).

Related issue:

- [#15](https://github.com/Duang777/drove/issues/15), inject hook and notify
  reporting per Drove session

Related work:

- [#14](https://github.com/Duang777/drove/issues/14), terminal screen model,
  OSC parsing, and transition-specific authority
- [Phase 1A hook-backed detection](../006-hook-backed-state-detection/spec.md)
- [Optional persistent hook manager](../007-managed-hook-configuration/spec.md)

## Problem

Phase 1A injects a session Agent ID, signal URL, and capability token into each
vendor process. The vendor process does not call `drove hook` unless the user
also edits Claude Code or Codex configuration.

The default `auto` hook policy therefore enters fallback after five seconds on
a clean machine. This makes the hook path opt-in even though Drove owns the
vendor process and can supply session-only configuration without changing user
files.

Claude Code and Codex require different injection mechanisms:

- Claude Code accepts a JSON settings file through `--settings`. Hooks from
  that file merge with hooks from user, project, local, and managed settings.
- Codex accepts a process-local `notify` command through `-c`. It appends one
  JSON payload argument after each completed turn.
- Codex OSC notifications are visible in PTY output, but Drove cannot consume
  them correctly until Issue #14 replaces line-based output handling with a
  terminal screen model.
- Codex native hook injection can work with session-scoped trust hashes, but
  that trust path is not part of the public configuration contract.

The injected Claude hooks are authoritative after Drove observes one valid
signal. Codex legacy notify is narrower. It proves only that one turn ended.
Treating the first notify as full hook activation would suppress the fallback
signals that still identify Working and Blocked.

## Goal

Make the default clean-machine path work without persistent vendor
configuration:

```text
drove up
    -> session Manager selects the vendor injection capability
    -> adapter builds process arguments and optional temporary files
    -> session writes private files and starts the vendor in the PTY
    -> vendor invokes drove hook with inherited session credentials
    -> existing signal endpoint authenticates and normalizes the delivery
    -> Detector applies source-specific authority
    -> session removes temporary files after the PTY and callbacks finish
```

Claude reaches `hook_active` through the injected `SessionStart` hook. Codex
notify produces durable, non-authoritative turn-stop evidence while fallback
continues to identify the other states.

## Scope

This phase includes:

- a vendor-neutral `SignalInjector` capability in `internal/adapter`;
- process-local injection configuration with `auto` and `off` modes;
- default `auto` injection for adapters that support it;
- a private Claude settings file containing only Drove hooks;
- `--settings <file>` injection for Claude interactive and oneshot runs;
- Codex `-c notify=[...]` injection for interactive and oneshot runs;
- an argv payload mode for `drove hook`;
- a no-behavior `--managed-by drove/v1` marker;
- Codex legacy notify normalization with sensitive-field removal;
- a non-authoritative `notify` signal source;
- reader-first event compatibility for notify signal and transition evidence;
- explicit filtering of Codex's internal task-title notification;
- startup rollback, normal cleanup, shutdown cleanup, and stale-file cleanup;
- additive session metadata and status for requested injection mode and result;
- isolated real-CLI verification that user and project config hashes do not
  change.

## Non-goals

This phase does not:

- edit user, project, local, managed, or system vendor configuration;
- implement the persistent commands in spec 007;
- inject Codex native hooks or a session trust hash;
- use `--dangerously-bypass-hook-trust`;
- treat Codex notify as full hook authority;
- parse OSC 9, OSC 777, terminal titles, or a rendered terminal screen;
- implement the transition-specific screen authority approved in Issue #14;
- add `drove explain`;
- add Gemini or another vendor;
- write a Drove signal token into a settings file or command argument;
- add a daemon API endpoint;
- change the five-second hook activation interval.

## Chosen architecture

The adapter plans injection. The session layer owns side effects.

`internal/adapter` knows the vendor flags, event lists, JSON shape, notify
format, and argument conflicts. Its `SignalInjector` receives immutable launch
facts and returns a complete argument list plus optional temporary-file
descriptions. It does not read or write files.

`internal/session` resolves the Drove relay executable, creates one
session-private directory, writes planned files, starts the PTY, and removes
the directory after all process callbacks finish. It records the requested
injection mode and the result in creation metadata.

`internal/config` parses the vendor-keyed injection mode. It does not contain
Claude or Codex branches.

`internal/detect` distinguishes authoritative command hooks from Codex legacy
notify. A notify can arm the existing turn-stop confirmation only while the
session has not activated native hooks. It never satisfies `required` and
never disables fallback.

The dependency direction remains:

```text
daemon -> config
daemon -> session -> adapter -> detect -> agent
session -> pty
```

No new package or service is required.

## Configuration

The Drove config accepts:

```json
{
  "agents": {
    "claude": {
      "signal_injection": "auto"
    },
    "codex": {
      "signal_injection": "off"
    }
  }
}
```

`signal_injection` values are:

- `auto`: inject when the exact adapter supports a session-only mechanism;
- `off`: add no injection arguments or files.

An omitted vendor entry or omitted field resolves to `auto` for an adapter
with `SignalInjector` and `off` for an adapter without it. `generic` therefore
defaults to `off`. An unknown nonempty value fails config validation before
the daemon opens the store.

The existing session hook policy remains separate:

| Hook policy | Injection mode | Result |
| --- | --- | --- |
| `off` | any | Do not inject and do not create a signal token. |
| `auto` | `auto` | Inject the supported session mechanism. |
| `auto` | `off` | Keep the current manual-config and fallback behavior. |
| `required` | `auto` | Inject, but only an authoritative hook can satisfy the five-second requirement. |
| `required` | `off` | Require a manually configured authoritative hook. |

Codex notify does not satisfy `required`.

## Session metadata and status

Creation metadata becomes version 3. It adds:

```json
{
  "version": 3,
  "name": "claude-12345678",
  "vendor": "claude",
  "mode": "interactive",
  "hook_policy": "auto",
  "signal_injection": "auto",
  "signal_injection_status": "injected",
  "signal_injection_reason": "session_config"
}
```

Stable injection status values are:

- `off`;
- `injected`;
- `skipped`;
- `detached`.

Stable reasons are:

- `hook_policy_off`;
- `configured_off`;
- `unsupported`;
- `relay_unavailable`;
- `argument_conflict`;
- `session_config`;
- `recovered`.

`session.Status` adds:

```text
signal_injection
signal_injection_status
signal_injection_reason
```

Readers continue to accept creation versions 1 and 2. They restore both as
injection `off`, status `detached`, and reason `recovered`. A recovered version
3 session preserves the requested mode but reports status `detached` because
the original PTY and temporary configuration no longer exist.

The status describes launch configuration. `hook_status` remains the runtime
authority status. `injected` never implies `hook_active`.

## Adapter contract

`adapter.Entry` gains an optional `SignalInjector`.

The request contains:

- vendor and run mode;
- the normalized base arguments from `Runner.Command`;
- copied request arguments;
- the absolute relay executable path;
- the Agent ID;
- the relative session configuration directory.

The result contains:

- the complete final argument list;
- zero or more temporary files with relative names, copied bytes, and
  permission mode;
- a bounded result reason;
- the signal channel, either `hook` or `notify`.

Every result is immutable from the caller's perspective. The adapter rejects
an absolute temporary-file name, `..`, an empty path segment, and duplicate
relative names. The session layer repeats those checks before writing.

The adapter owns argument order. This matters because Codex global `-c`
options must precede the `exec` subcommand in oneshot mode.

The existing `StartRequest.Args` contract is corrected in the same change.
For a known vendor, request arguments are appended to the adapter's base
arguments before injection. Current code ignores them unless `Command` is
also set.

## Relay executable

The daemon resolves the command that injected vendor processes call:

1. Resolve the running `droved` executable with `os.Executable` and
   `filepath.EvalSymlinks`.
2. Check for an executable regular file named `drove` in the same directory.
3. If no sibling exists, use `exec.LookPath("drove")` and resolve that path.
4. If neither path works, report relay unavailable to the session Manager.

Tests inject the relay path through a `ManagerOption`; they do not depend on
the test binary's directory.

The adapter shell-quotes the absolute path for Claude's command string. Codex
receives the executable and each argument as separate array values.

A missing relay does not fail an `auto` start. The session records injection
`skipped` with reason `relay_unavailable` and continues under the selected hook
policy. A `required` session still waits for a manually configured native hook
and fails through the existing timeout when none arrives.

## Managed relay command

Generated commands include:

```text
drove hook --vendor <vendor> --managed-by drove/v1
```

`drove hook` accepts `--managed-by` only when the value is empty or
`drove/v1`. The flag does not change relay transport, redaction, retries, or
exit status.

Codex notify uses argv payload mode:

```text
drove hook --vendor codex --managed-by drove/v1 --payload-argv <json>
```

The configured Codex command array omits `<json>`. Codex appends that one
argument when it invokes notify.

Without `--payload-argv`, `drove hook` accepts no positional arguments and
reads the payload from stdin as before. With `--payload-argv`, it accepts
exactly one positional argument and does not read stdin. Both modes apply the
existing 1 MiB limit, UTF-8 check, single-object check, redacted diagnostics,
and exit-zero delivery behavior.

The payload never appears in an error message.

## Claude injection

For each hook-enabled Claude session with injection `auto`, the adapter
generates one JSON document containing only a `hooks` key.

It installs the 17 event groups already accepted by the Claude normalizer:

```text
SessionStart
UserPromptSubmit
PreToolUse
PostToolUse
PostToolUseFailure
PostToolBatch
PermissionRequest
PermissionDenied
Elicitation
ElicitationResult
Notification
Stop
StopFailure
SubagentStart
SubagentStop
TaskCompleted
SessionEnd
```

The Notification matcher is:

```text
permission_prompt|elicitation_dialog|elicitation_url_dialog|agent_needs_input|idle_prompt|quota_auto_resume_stale
```

Every handler is synchronous, uses the managed relay command, and has timeout
5. The file contains no `disableAllHooks`, `allowManagedHooksOnly`, trust,
token, Agent ID, or signal URL.

The session writes:

```text
<data_dir>/sessions/<agent-id>/claude-settings.json
```

The session directory uses mode `0700`. The file uses mode `0600`. The write
uses a same-directory temporary file, file sync, rename, and directory sync.

The adapter inserts:

```text
--settings <absolute-settings-path>
```

after Claude's base mode arguments and before caller-supplied arguments.

The adapter reports `argument_conflict` and skips injection when caller
arguments contain:

- `--bare`;
- `--settings`;
- `--settings=<value>`.

Drove does not override these explicit caller choices. A skipped `required`
session can still activate through the caller's settings. Otherwise the
existing required timeout stops it.

Claude merges the injected settings with other settings levels. Drove does not
write `disableAllHooks = false`, because that could reactivate unrelated user
hooks. A user or managed policy can therefore suppress the injected hooks.

## Codex notify injection

For each hook-enabled Codex session with injection `auto`, the adapter inserts
this global option before the `exec` subcommand or other caller arguments:

```text
-c notify=["/absolute/path/to/drove","hook","--vendor","codex","--managed-by","drove/v1","--payload-argv"]
```

The adapter serializes the value as valid TOML. It does not use shell quoting
inside the array.

If caller arguments already set top-level `notify` through `-c` or
`--config`, the adapter reports `argument_conflict` and skips injection. It
does not replace, merge, or execute the caller's notify command.

The injection does not add:

- native `[hooks]` values;
- `hooks.state` trust hashes;
- `--dangerously-bypass-hook-trust`;
- TUI notification settings.

Issue #14 may later add
`tui.notification_method=osc9` and
`tui.notification_condition=always` together with terminal parsing. Adding
those flags before a consumer exists would only place response text in the PTY
stream.

## Codex notify normalization

The Codex normalizer accepts either:

- the existing native hook object with `hook_event_name` or `event_name`; or
- a legacy notify object whose `type` is `agent-turn-complete`.

For a normal notify object, the adapter emits:

```text
source: notify
kind: turn_stopped
vendor: codex
vendor_event: agent-turn-complete
scope: root
vendor_session_id: thread-id
vendor_turn_id: turn-id
confidence: 1
```

The adapter reads but never returns or persists:

- `cwd`;
- `input-messages`;
- `last-assistant-message`;
- unknown payload fields.

Codex TUI also emits a notify for its internal task-title turn. The adapter
returns the private `ErrIgnoredHookPayload` sentinel when `input-messages`
contains exactly one string that starts with
`Generate a concise, single-line task title`. `Manager.DeliverHook` treats
that sentinel as a successful no-op. The endpoint returns 204, writes no
event, and does not change hook authority.

Other unknown notify types return `ErrUnknownHookEvent` and keep the existing
422 response.

## Notify event compatibility

The current signal payload version 1 accepts only process, hook, heuristic,
and timer sources. Adding notify to that version would make an older reader
reject a known payload version.

The reader-first change adds:

- `event.SignalPayloadV2`, which adds source `notify`;
- `event.StateEvidencePayloadV2`, which adds source `notify`;
- support for reading versions 1 and 2;
- the existing skip-and-count behavior for an unknown signal or state evidence
  version.

Existing producers continue to write version 1. A notify decision writes
signal payload version 2. If notify later causes an Idle transition, that
state event uses evidence payload version 2 and source `notify`.

An older Phase 1A binary skips the unknown signal payload version. It still
applies the state columns while skipping unknown state evidence, which keeps
restart projection compatible.

## Detector authority

`detect.SourceNotify` is below an active native hook and above fallback
silence. It is not hook activation.

The Detector applies these rules:

- native `SourceHook` behavior remains unchanged;
- a notify never changes `HookStatus`;
- a notify never satisfies `required`;
- a notify received while `HookStatus` is `hook_active` is persisted with
  outcome `suppressed`;
- a notify received while policy is `auto` and status is `fallback` arms the
  existing one-second turn-stop confirmation;
- a notify received while status is `awaiting_hook` is persisted as
  `suppressed`, so it cannot replace the five-second activation timer;
- hook, output activity, or a newer notify cancels or replaces the candidate
  under the existing timer-generation rules;
- a confirmed notify candidate can move Working or Blocked to Idle;
- a notify cannot move Idle to Working or any state to Blocked or Done;
- a notify received after terminal process state is persisted with outcome
  `terminal` when it was admitted before exit, or rejected as detached after
  the exit claim.

Fallback output and screen rules remain able to identify Working and Blocked.
This prevents a turn-complete-only channel from suppressing evidence it cannot
replace.

## Startup ordering

Session startup uses this order:

1. Validate vendor, run mode, hook policy, and request arguments.
2. Resolve the configured signal-injection mode.
3. Resolve the relay executable.
4. Create the Agent ID and prepare the session signal credential.
5. Ask the adapter for an immutable injection plan.
6. Materialize any temporary files under the session-private directory.
7. Commit creation metadata version 3 and `Pending -> Starting`.
8. Register the pending runtime session.
9. Start the PTY with the final arguments and existing signal environment.
10. Continue the approved Phase 1A readiness and required-hook sequence.

If planning is unsupported, the relay is unavailable, or arguments conflict,
`auto` records `skipped` and continues without files or added arguments.

If temporary-file creation, sync, close, or rename fails, startup removes any
partial session directory and returns before it commits session creation.

If event commit or PTY startup fails after file materialization, startup
removes the session directory and joins a cleanup error with the primary
error.

## Cleanup and restart

The runtime session owns its temporary injection directory.

Normal process exit and explicit stop remove the directory only after the PTY
has finished its output and exit callbacks. This allows `SessionEnd` to invoke
the relay until the vendor process exits.

Cleanup is idempotent. It rejects symbolic links and removes only the exact
directory rooted at:

```text
<data_dir>/sessions/<canonical-agent-id>
```

A cleanup failure appends one redacted non-state error event while the
committer is available. The error includes the Agent ID and bounded operation
name, not file content or a token.

During daemon startup, before accepting new sessions, Drove removes stale
session-injection directories because the current architecture cannot retain
a PTY across daemon restart. This rule must be revisited before Issue #17 adds
a persistent runtime shim.

The startup cleanup scans only direct child names that are canonical Agent
UUIDs. It refuses symbolic links and leaves unknown files or directories
unchanged.

## Security and privacy

- Temporary files contain only hook definitions and the relay executable
  path.
- The session token remains in the inherited environment.
- The token never appears in arguments, temporary files, events, or logs.
- Temporary directories use `0700`; files use `0600`.
- The session layer validates every adapter-supplied relative path.
- Claude command arguments use POSIX shell quoting inside the hook string.
- Codex notify uses an argv array and no shell.
- Relay argv payloads use the existing size and UTF-8 bounds.
- The adapter removes prompt, message, path, and response text before a signal
  leaves `internal/adapter`.
- Internal Codex title-generation notifications write no event.
- Injection never changes trust or enables disabled hooks.

## Compatibility and rollout

The release order is reader first:

1. Add event payload version 2 readers and `SourceNotify` without emitting
   notify events.
2. Add relay argv mode and Codex notify normalization.
3. Add configuration, adapter injection plans, and session file lifecycle.
4. Enable injection by default only after old logs and seeded version 2 logs
   pass restart tests.

Once notify events exist, the reader-first commit is the rollback floor for
evidence details. Older Phase 1A binaries still recover state because unknown
signal metadata is projection-neutral and state columns remain authoritative.

No dependency is added.

## Module ownership

| Module | Owns | Does not own |
| --- | --- | --- |
| `internal/config` | vendor-keyed `signal_injection` values and validation | vendor defaults or argument syntax |
| `internal/adapter` | injection capability, vendor arguments, temp-file content, notify normalization | filesystem writes, session credentials, process lifecycle |
| `internal/session` | resolved injection policy, relay path, temp files, cleanup, startup ordering | vendor flags or JSON shape |
| `internal/detect` | notify authority and state decisions | vendor payload fields or process arguments |
| `internal/event` | version 2 signal and evidence wire validation | injection policy |
| `cmd/drove` | stdin or argv payload selection and marker validation | injection planning |
| `internal/daemon` | config and relay-path dependency injection | vendor decisions |

## Alternatives considered

### Persistent installation first

Persistent installation requires structured merge, ownership manifests,
backups, locks, drift handling, and exact uninstall. It also affects vendor
sessions started outside Drove.

Session injection changes only the child process that Drove owns and leaves
user files unchanged. It is the default path. Spec 007 remains an optional
follow-up for users who explicitly want persistent integration.

### Codex native hooks with injected trust

Tests show that session flags can inject both native hooks and their
`trusted_hash`. The public Codex documentation does not define that workflow.
Its identity keys also depend on a synthetic session-flags path.

The default path uses the documented `notify` option. Native Codex hooks remain
available through manual configuration, and Drove never approves trust.

### Treat notify as hook activation

Notify emits only turn completion. If it activated hook authority, Drove would
suppress the fallback evidence needed for Working and Blocked. The selected
source-specific rule gives notify only the transition it can prove.

### Inject OSC settings before Issue #14

Current PTY reading splits by lines and sanitizes OSC only for heuristics. It
cannot reliably turn OSC messages into structured observations. Injection and
parsing must land together under Issue #14.

## Verification

Automated tests must cover:

- default, explicit auto, explicit off, and invalid config modes;
- generic and unknown vendors defaulting to off;
- user config round-trip with unrelated agent settings;
- relay sibling resolution, PATH fallback, missing relay, symlink, regular
  file, and executable permission checks;
- legacy stdin relay behavior;
- argv payload relay behavior;
- marker omitted, valid, and invalid;
- payload limits, UTF-8, one-object validation, redaction, retry ID reuse, and
  exit-zero behavior in both relay modes;
- every Claude injected event and the exact Notification matcher;
- Claude interactive and oneshot argument order;
- Claude `--bare`, `--settings`, and `--settings=<value>` conflicts;
- Codex interactive and oneshot global `-c` ordering;
- Codex `-c notify=...` and `--config notify=...` conflicts;
- TOML encoding of relay paths and arguments with spaces and quotes;
- native Codex hook payload compatibility;
- normal Codex notify, unknown notify type, malformed notify, and internal
  title notify;
- removal of `cwd`, input messages, assistant text, and unknown fields;
- signal and state evidence versions 1 and 2;
- old readers' skip behavior for version 2 metadata;
- notify before hook activation, during fallback, after hook activation,
  during required wait, and after process terminal state;
- notify confirmation cancellation and stale timer generation;
- Blocked to Idle from notify without claiming Working or hook activation;
- temporary-file path validation and permission modes;
- failures at temporary create, write, sync, close, rename, and cleanup;
- startup failure after materialization;
- normal exit, user stop, daemon shutdown, and repeated cleanup;
- stale startup cleanup with valid Agent IDs, unknown names, and symlinks;
- creation metadata versions 1, 2, and 3;
- status separation between injection result and runtime hook status;
- concurrent session isolation under the race detector;
- no change to isolated Claude or Codex user and project config hashes.

Manual verification must use an isolated HOME and data directory:

1. Start Claude 2.1.288 or newer with no persistent hook config.
2. Confirm that the temporary settings file has mode `0600`.
3. Confirm that Claude reaches `hook_active` within five seconds.
4. Confirm that existing user and project hooks still run.
5. Confirm that `disableAllHooks = true` suppresses injected hooks and auto
   enters fallback.
6. Stop the session and confirm that the temporary directory is removed.
7. Start Codex 0.160.0 or newer with no persistent notify config.
8. Complete one turn and confirm one redacted notify signal event and a
   confirmed Idle transition.
9. Confirm that the internal task-title turn writes no signal event.
10. Confirm that Codex remains fallback rather than `hook_active`.
11. Set signal injection off for each vendor and confirm that no argument or
    temporary file is added.
12. Hash all user and project vendor config files before and after the run and
    confirm that they are unchanged.

Repository gates are:

```bash
gofmt -w cmd internal
go test ./internal/event ./internal/agent ./internal/detect ./internal/adapter ./internal/session ./internal/config ./internal/daemon ./internal/client ./cmd/drove -race -count=20
go test ./... -race -count=1
go vet ./...
make build
npm --prefix web run typecheck
npm --prefix web run build
git diff --check
```

## Acceptance criteria

- `drove up claude` can reach `hook_active` without persistent Claude config.
- Claude's injected settings contain only hooks and no credential.
- Claude preserves existing settings-layer behavior.
- `drove up codex` injects one process-local notify command.
- One real Codex turn emits one redacted notify signal.
- The internal Codex title turn emits no signal.
- Codex notify can confirm Idle but cannot activate hooks, enter Working,
  Blocked, or Done, or satisfy `required`.
- `signal_injection = off` restores the Phase 1A launch behavior.
- Explicit conflicting vendor arguments are never overwritten.
- User and project Claude and Codex config files remain byte-identical.
- Temporary files use the specified permissions and are removed on every
  completed lifecycle path.
- Restart removes only stale Drove injection directories.
- Existing stdin hook relay commands remain compatible.
- Tokens, prompts, input messages, assistant messages, and raw vendor payloads
  do not enter events or logs.
- The full automated and isolated vendor verification suites pass.
- Each independently verifiable implementation commit is pushed before the
  next starts.

## Risks and controls

### Codex notify is incomplete

Notify reports only turn completion. The Detector keeps fallback active and
limits notify authority to a confirmed Idle transition.

### Claude policy blocks session settings

Drove does not override `disableAllHooks`, `allowManagedHooksOnly`, or
workspace trust. Auto mode enters fallback. Required mode returns its existing
activation error.

### The relay executable moves

The daemon resolves the sibling CLI and PATH on each startup. Existing daemon
processes keep the resolved path for new sessions. Restarting the daemon picks
up an installation move.

### Cleanup fails

Drove reports a redacted event and retries stale cleanup on the next daemon
start. It never follows a symlink while cleaning.

### Codex changes notify payloads

The normalizer rejects unknown types and malformed fields. Auto sessions keep
fallback state detection. No raw payload is persisted.

## Fragile assumption

This plan assumes Claude keeps merging hooks supplied through `--settings`
with the normal settings layers. If that behavior changes, no valid
`SessionStart` arrives. Auto falls back after five seconds, and required fails
without claiming that hooks are active.
