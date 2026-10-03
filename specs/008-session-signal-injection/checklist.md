# Spec review checklist

## Direction

- [x] Issue #15 is the approval source.
- [x] Per-session injection is the default path.
- [x] Persistent installation is deferred to spec 007.
- [x] OSC parsing and screen authority remain in Issue #14.
- [x] User and project vendor files remain unchanged.

## Scope

- [x] Claude `--settings` injection is included.
- [x] Codex process-local notify injection is included.
- [x] Relay argv payload support is included.
- [x] Codex native hook trust injection is excluded.
- [x] Trust bypass is excluded.
- [x] OSC injection is excluded until a parser exists.
- [x] Gemini and persistent installers are excluded.

## Architecture

- [x] Adapter owns vendor arguments and temporary-file content.
- [x] Session owns temporary-file side effects and cleanup.
- [x] Config remains vendor-agnostic.
- [x] Detector owns source-specific authority.
- [x] Event owns wire compatibility.
- [x] Daemon remains the composition root.
- [x] No new package or service is required.

## Configuration

- [x] The config path is `agents.<vendor>.signal_injection`.
- [x] Supported values are `auto` and `off`.
- [x] Hook-capable injectors default to auto.
- [x] Unsupported adapters default to off.
- [x] Invalid values fail before daemon startup.
- [x] Hook policy and injection mode remain separate.
- [x] Codex notify cannot satisfy required hooks.

## Metadata

- [x] Creation metadata version 3 is defined.
- [x] Injection mode, status, and reason enums are fixed.
- [x] Versions 1 and 2 retain explicit fallback values.
- [x] Recovered sessions report detached injection.
- [x] Injection status does not claim runtime hook activation.

## Adapter

- [x] `SignalInjector` is optional per exact adapter.
- [x] Requests and results are immutable.
- [x] Adapter paths are relative and revalidated by session.
- [x] Adapter owns final vendor argument order.
- [x] Known-vendor request arguments are no longer ignored.
- [x] Generic remains unsupported.

## Relay

- [x] Sibling and PATH relay resolution order is explicit.
- [x] Missing relay skips auto injection without changing hook semantics.
- [x] `--managed-by drove/v1` is backward compatible.
- [x] Invalid marker values are rejected.
- [x] Stdin mode accepts no positional argument.
- [x] Argv mode accepts exactly one argument.
- [x] Both modes share validation, retries, redaction, and payload bounds.
- [x] Payload text never appears in diagnostics.

## Claude

- [x] The temporary file contains only a hooks key.
- [x] All 17 supported events are listed.
- [x] The Notification matcher is exact.
- [x] Handler timeout and synchronous behavior are fixed.
- [x] Token, Agent ID, and URL remain in the environment.
- [x] File and directory modes are fixed.
- [x] Argument order is explicit.
- [x] `--bare` and settings conflicts are explicit.
- [x] Drove does not override hook-disabling policy.

## Codex

- [x] Notify uses the documented top-level `notify` option.
- [x] Global `-c` placement before `exec` is explicit.
- [x] The command is a TOML argv array.
- [x] Existing notify config is not overwritten.
- [x] Native hook and trust settings are not injected.
- [x] TUI notification settings are not injected before Issue #14.
- [x] Notify fields retained in durable evidence are bounded.
- [x] Prompt and assistant content are removed.
- [x] The internal title turn is ignored without an event.

## Authority

- [x] Notify is a separate source.
- [x] Notify never changes hook status.
- [x] Notify never satisfies required.
- [x] Active native hooks suppress notify decisions.
- [x] Awaiting sessions suppress notify without replacing activation timeout.
- [x] Fallback sessions can use notify for turn stop.
- [x] Notify can only confirm Idle.
- [x] Notify cannot infer Working, Blocked, Done, or process terminal state.
- [x] Existing confirmation and timer-generation rules are reused.

## Compatibility

- [x] Signal payload version 2 is reader first.
- [x] State evidence payload version 2 is reader first.
- [x] Existing producers remain on version 1.
- [x] Old readers skip version 2 metadata safely.
- [x] Existing native hook payloads remain valid.
- [x] Existing stdin relay commands remain valid.
- [x] No dependency is added.

## Lifecycle

- [x] Temporary files exist before PTY start.
- [x] Partial writes are removed before creation commit.
- [x] Event and PTY startup failures clean up.
- [x] Normal exit, stop, and shutdown clean up after callbacks.
- [x] Cleanup is idempotent.
- [x] Cleanup rejects symlinks.
- [x] Startup removes only canonical stale session directories.
- [x] Unknown names and files remain unchanged.
- [x] Cleanup failures are redacted and observable.
- [x] Persistent runtime shim work must revisit startup cleanup.

## Security

- [x] Token remains only in process environment and memory.
- [x] Temporary files contain no credential.
- [x] Codex notify uses argv without a shell.
- [x] Claude hook command uses POSIX quoting.
- [x] Argv payload uses the current 1 MiB limit.
- [x] Sensitive notify fields never leave the adapter.
- [x] Trust state and bypass flags are never written.

## Verification

- [x] Config and relay-resolution tests are listed.
- [x] Claude and Codex argument tests are listed.
- [x] Relay compatibility and redaction tests are listed.
- [x] Event reader-first tests are listed.
- [x] Detector source-authority tests are listed.
- [x] File fault-injection and cleanup tests are listed.
- [x] Restart and concurrency tests are listed.
- [x] Isolated real-vendor checks are listed.
- [x] User and project config hashes are checked.
- [x] Full race, vet, Go, and Web gates are listed.
- [x] Every implementation part is committed and pushed separately.

## Approval

- [x] The owner approved the direction and acceptance criteria in Issue #15.
