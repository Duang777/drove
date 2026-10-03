# Implementation tasks

Implement each commit in order. Run its focused checks, commit, and push before
starting the next commit.

## Spec commit

Commit the Issue #15 priority correction and this approved specification:

```text
docs: prioritize session signal injection
```

## Commit 1: reader-first notify and relay support

### T1: Add notify wire readers

Files:

- `internal/event/event.go`
- `internal/event/event_test.go`
- `internal/session/projection.go`
- `internal/session/projection_test.go`
- `internal/session/detector.go`
- `internal/session/detector_test.go`

Work:

- Add signal payload version 2 with source `notify`.
- Add state evidence payload version 2 with source `notify`.
- Keep all existing producers on version 1.
- Read versions 1 and 2 explicitly.
- Keep unknown-version skip behavior.
- Prove restart projection with seeded version 2 signal and state evidence.

### T2: Add non-authoritative notify decisions

Files:

- `internal/detect/detect.go`
- `internal/detect/detect_test.go`
- `internal/agent/agent.go`
- `internal/agent/agent_test.go`

Work:

- Add `SourceNotify` and `NewNotifySignal`.
- Permit only root `turn_stopped` with a canonical delivery ID.
- Never change hook status from a notify.
- Suppress notify while native hooks are active.
- Suppress notify while awaiting so the activation timer remains authoritative.
- Allow notify to arm the existing stop confirmation only in fallback.
- Keep required-hook activation unchanged.
- Add state evidence source `notify`.
- Test every authority and timer-generation branch.

### T3: Add relay marker and argv payload mode

Files:

- `cmd/drove/main.go`
- `cmd/drove/main_test.go`
- `cmd/drove/AGENTS.md`

Work:

- Add optional `--managed-by` validation.
- Add optional `--payload-argv`.
- Require zero positional arguments in stdin mode.
- Require exactly one positional argument in argv mode.
- Reuse payload parsing, delivery ID, retry, redaction, and exit-zero behavior.
- Prove that raw argv payload text is absent from errors and stderr.

### T4: Normalize Codex notify

Files:

- `internal/adapter/hooks.go`
- `internal/adapter/codex_hooks.go`
- `internal/adapter/codex_hooks_test.go`
- `internal/session/signal.go`
- `internal/session/signal_test.go`

Work:

- Accept `agent-turn-complete`.
- Return a notify source with only IDs and bounded enum evidence.
- Strip cwd, input messages, assistant message, and unknown fields.
- Add `ErrIgnoredHookPayload`.
- Ignore the exact internal title-prompt prefix as a successful no-op.
- Keep native Codex hook behavior unchanged.

Verification:

```bash
gofmt -w cmd/drove internal/agent internal/adapter internal/detect internal/event internal/session
go test ./internal/event ./internal/agent ./internal/detect ./internal/adapter ./internal/session ./cmd/drove -race -count=20
go test ./... -race -count=1
go vet ./...
make build
git diff --check
```

Commit and push:

```text
feat: accept codex notify signals
```

This commit is the rollback floor before any process receives an injected
notify command.

## Commit 2: injection configuration and adapter plans

### T5: Add signal-injection config

Files:

- `internal/config/config.go`
- `internal/config/config_test.go`
- `internal/config/AGENTS.md`

Work:

- Add vendor-keyed agent settings.
- Parse `signal_injection` as `auto` or `off`.
- Keep adapter capability defaults outside config.
- Reject unknown values during validation.
- Preserve unrelated vendor settings during JSON loading.

### T6: Add adapter injection contracts

Files:

- `internal/adapter/adapter.go`
- `internal/adapter/signal_injection.go`
- `internal/adapter/signal_injection_test.go`
- `internal/adapter/AGENTS.md`

Work:

- Add immutable injection requests, plans, and temporary-file descriptions.
- Validate copied arguments and relative paths.
- Add optional injectors to Claude and Codex entries.
- Keep generic unsupported.
- Add POSIX command quoting and TOML argv-array encoding.

### T7: Add Claude injection planning

Files:

- `internal/adapter/claude_signal_injection.go`
- `internal/adapter/claude_signal_injection_test.go`
- `internal/adapter/testdata/session-injection/claude/`

Work:

- Generate one settings document containing only hooks.
- Add all 17 events and the exact Notification matcher.
- Use the managed relay command and timeout 5.
- Place `--settings` in the specified argument order.
- Detect `--bare` and settings conflicts.
- Add deterministic golden files.

### T8: Add Codex notify planning

Files:

- `internal/adapter/codex_signal_injection.go`
- `internal/adapter/codex_signal_injection_test.go`

Work:

- Generate one valid TOML notify array.
- Put global `-c` before `exec`.
- Preserve copied caller argument order.
- Detect existing top-level notify overrides.
- Add no native hook, trust, bypass, or TUI notification setting.
- Test paths and arguments containing spaces, quotes, and shell characters.

Verification:

```bash
gofmt -w internal/config internal/adapter
go test ./internal/config ./internal/adapter -race -count=20
go vet ./internal/config ./internal/adapter
git diff --check
```

Commit and push:

```text
feat: plan per-session signal injection
```

## Commit 3: session materialization and cleanup

### T9: Add injection metadata

Files:

- `internal/agent/agent.go`
- `internal/agent/agent_test.go`
- `internal/session/projection.go`
- `internal/session/projection_test.go`
- `internal/session/session.go`
- `internal/session/session_test.go`

Work:

- Add requested injection mode, status, and reason to Agent snapshots.
- Write creation metadata version 3.
- Read versions 1, 2, and 3.
- Restore old metadata with explicit detached fallback values.
- Add fields to `session.Status`.
- Keep injection status separate from runtime hook status.

### T10: Resolve and inject the relay executable

Files:

- `internal/daemon/daemon.go`
- `internal/daemon/daemon_test.go`
- `internal/session/signal_injection.go`
- `internal/session/signal_injection_test.go`
- relevant `AGENTS.md` files

Work:

- Resolve sibling `drove`, then PATH.
- Reject symlink, non-regular, and non-executable candidates.
- Add Manager options for data directory, vendor settings, relay path, and
  filesystem fault injection.
- Resolve adapter-capability defaults.
- Skip auto injection with bounded status when unsupported, unavailable, or
  conflicting.

### T11: Materialize private session files

Files:

- `internal/session/signal_injection.go`
- `internal/session/signal_injection_test.go`
- `internal/session/session.go`
- `internal/session/session_test.go`

Work:

- Validate every relative file name again.
- Create the canonical session directory with mode `0700`.
- Write files atomically with mode `0600`, file sync, rename, and directory
  sync.
- Materialize before creation commit and PTY start.
- Remove partial files on every pre-commit failure.
- Join cleanup errors with post-materialization startup failures.
- Correct known-vendor `StartRequest.Args` handling.

### T12: Clean normal, failed, and stale sessions

Files:

- `internal/session/signal_injection.go`
- `internal/session/signal_injection_test.go`
- `internal/session/session.go`
- `internal/session/session_test.go`
- `internal/daemon/daemon_test.go`

Work:

- Remove the runtime directory after PTY callbacks finish.
- Cover natural exit, startup failure, explicit stop, and daemon shutdown.
- Keep cleanup idempotent.
- Reject symlinks.
- Emit one redacted error event when runtime cleanup fails.
- Remove only canonical Agent-ID directories at daemon startup.
- Leave unknown names and files unchanged.
- Run concurrent starts and cleanup under the race detector.

Verification:

```bash
gofmt -w internal/agent internal/daemon internal/session
go test ./internal/agent ./internal/adapter ./internal/config ./internal/session ./internal/daemon -race -count=20
go test ./... -race -count=1
go vet ./...
make build
git diff --check
```

Commit and push:

```text
feat: inject session signal configuration
```

## Commit 4: documentation and end-to-end verification

### T13: Update documentation

Files:

- `README.md`
- `docs/hooks.md`
- `docs/rfc-001-agent-state-and-control.md`
- `docs/next-phase-research.md`
- relevant `AGENTS.md` files

Work:

- Document default per-session injection and explicit off configuration.
- Distinguish Claude native hook authority from Codex notify evidence.
- Document settings conflicts and missing relay behavior.
- State that no user or project vendor file changes.
- Keep manual native hook configuration as a supported authority upgrade.
- Mark persistent installation as optional and deferred.
- Link OSC behavior to Issue #14.

### T14: Run the full automated suite

Run:

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

### T15: Run isolated Claude verification

Use Claude 2.1.288 or newer, an isolated HOME, and a temporary Drove data
directory:

- hash user, project, and local settings before startup;
- start without persistent Drove hooks;
- inspect the private settings file and permissions while the session runs;
- observe `SessionStart` and `hook_active`;
- verify an existing identical user hook is not duplicated by Claude;
- verify `disableAllHooks = true` causes fallback;
- stop and verify cleanup;
- confirm every persistent settings hash is unchanged.

### T16: Run isolated Codex verification

Use Codex 0.160.0 or newer and the same isolation rules:

- hash user and project Codex config before startup;
- inspect the launched arguments;
- complete one TUI turn against a mock provider;
- observe one redacted notify signal and Idle confirmation;
- prove the internal title turn is absent from events;
- prove hook status remains fallback;
- stop and verify no temporary file remains;
- confirm every persistent config hash is unchanged.

### T17: Verify explicit off

- Set Claude and Codex signal injection to off.
- Prove no extra argument or temporary file is created.
- Prove manual native hooks still activate normally.

### T18: Update issues

- Comment on Issue #15 with commits, versions, config hashes, and test results.
- Close Issue #15 only when all acceptance criteria pass.
- Comment on spec 007's tracking issue that persistent installation remains
  deferred.
- Leave Issue #14 open for OSC and screen authority.

Commit and push:

```text
docs: document session signal injection
```
