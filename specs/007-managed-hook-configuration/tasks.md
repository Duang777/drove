# Implementation tasks

Implement each commit in order. Run its focused checks, commit, and push before
starting the next commit.

## Approval writeback

Before implementation:

- confirm that the per-session injection work in Issue #15 is complete;
- change `spec.md` status to `Approved for implementation`;
- add the approval date;
- check the approval item in `checklist.md`;
- link the separate issue that tracks this optional persistent installer.

Commit and push:

```text
docs: approve managed hook configuration
```

## Commit 1: managed node definitions and pure planners

### T1: Accept the ownership marker

Files:

- `cmd/drove/main.go`
- `cmd/drove/main_test.go`
- `cmd/drove/AGENTS.md`

Work:

- Add optional `drove hook --managed-by`.
- Accept only an empty value or `drove/v1`.
- Keep relay input, output, retry, redaction, and exit behavior unchanged.
- Verify that existing hook commands remain compatible.

### T2: Add hook-configuration adapter contracts

Files:

- `internal/adapter/adapter.go`
- `internal/adapter/hook_config.go`
- `internal/adapter/hook_config_test.go`
- `internal/adapter/AGENTS.md`

Work:

- Add immutable scope, representation, target, node, plan, and inspection
  values.
- Add a hook-configuration capability to exact Claude and Codex registry
  entries.
- Keep `generic` unsupported.
- Keep all vendor names, paths, environment variables, event lists, matchers,
  and configuration fields inside `internal/adapter`.
- Define stable node IDs and canonical node hashes.
- Implement user and project target resolution.
- Implement user absolute and project portable managed command generation.
- Add POSIX shell-argument quoting and PATH warnings.

### T3: Add the structured JSON planner

Files:

- `internal/adapter/json_document.go`
- `internal/adapter/json_document_test.go`
- `internal/adapter/claude_hook_config.go`
- `internal/adapter/claude_hook_config_test.go`
- `internal/adapter/testdata/hook-config/claude/`

Work:

- Build a private ordered JSON tree with `encoding/json.Decoder`.
- Reject invalid UTF-8, byte-order marks, duplicate keys, trailing values, and
  non-object roots.
- Preserve unknown values and every array order.
- Implement Claude inspection, install, update, and uninstall plans.
- Add all 17 Claude nodes and the exact Notification matcher.
- Preserve exact foreign nodes without adopting them.
- Treat unknown fields on an owned node as drift.
- Render deterministic two-space JSON with one trailing newline.
- Add golden files for missing, foreign, mixed, owned, drifted, and cleanup
  cases.

### T4: Add the syntax-preserving TOML planner

Files:

- `go.mod`
- `go.sum`
- `internal/adapter/toml_document.go`
- `internal/adapter/toml_document_test.go`
- `internal/adapter/codex_hook_config.go`
- `internal/adapter/codex_hook_config_test.go`
- `internal/adapter/testdata/hook-config/codex/`

Work:

- Pin `github.com/pelletier/go-toml/v2` to `v2.4.3`.
- Use the public decoder for semantic validation.
- Isolate all unstable parser use in `toml_document.go`.
- Locate edit ranges from the parser rather than text matching.
- Support one ordinary `[hooks]` table with array-of-inline-table event values.
- Reject every unsupported equivalent encoding listed in `spec.md`.
- Preserve all untouched TOML byte ranges, comments, ordering, quoting, and
  line endings.
- Implement all 12 Codex nodes.
- Implement deterministic representation selection.
- Reject `hooks.json` with unknown top-level fields.
- Reparse and verify complete proposed bytes.
- Add golden and byte-range tests for JSON and TOML forms.

Verification:

```bash
gofmt -w cmd/drove internal/adapter
go mod tidy
go test ./internal/adapter ./cmd/drove -race -count=20
git diff --check
```

Commit and push:

```text
feat: plan managed hook configuration
```

## Commit 2: ownership and crash-safe transactions

### T5: Create the hook configuration package

Files:

- `internal/hookconfig/AGENTS.md`
- `internal/hookconfig/types.go`
- `internal/hookconfig/manager.go`
- `internal/hookconfig/manager_test.go`
- `internal/AGENTS.md`

Work:

- Document the package responsibility before adding implementation.
- Add operation, result, warning, conflict, and status types.
- Add immutable plans that copy target bytes, node records, and manifest data.
- Add the 4 MiB target bound.
- Depend on exact adapter hook-configuration capabilities.
- Keep the package independent from daemon, API, session, and client.

### T6: Implement committed ownership manifests

Files:

- `internal/hookconfig/manifest.go`
- `internal/hookconfig/manifest_test.go`
- `internal/hookconfig/testdata/manifests/`

Work:

- Implement the exact version 1 committed manifest schema.
- Derive the target ID from vendor, scope, and canonical target path.
- Validate paths, enums, digests, node IDs, timestamps, and writer metadata.
- Serialize manifests deterministically.
- Reject unknown versions without rewriting them.
- Classify missing, malformed, unknown, exact, mixed, and drifted ownership.
- Store only owned nodes.

### T7: Implement pending transaction recovery

Files:

- `internal/hookconfig/pending.go`
- `internal/hookconfig/pending_test.go`

Work:

- Implement the exact version 1 pending schema.
- Validate embedded previous and next manifests.
- Implement `absent`, before-digest, after-digest, and third-digest cases.
- Keep status recovery inspection read-only.
- Make mutating operations complete a resolvable pending record under lock.
- Leave a third-digest conflict untouched.

### T8: Implement the filesystem transaction

Files:

- `internal/hookconfig/fs.go`
- `internal/hookconfig/fs_unix.go`
- `internal/hookconfig/fs_test.go`
- `internal/hookconfig/manager_test.go`

Work:

- Create ownership directories and files with the specified modes.
- Add one target-ID advisory lock with a five-second bound.
- Reject target and managed-subdirectory symbolic links.
- Read and digest the target under lock.
- Create and sync a restricted backup.
- Atomically write and sync the pending record.
- Write the target through a same-directory temporary file.
- Preserve an existing target's permission bits.
- Recheck the before digest immediately before rename.
- Sync the target directory after rename or deletion.
- Re-read and verify the target before manifest commit.
- Atomically commit or remove the ownership manifest.
- Remove and sync the pending record last.
- Retain five committed backups and every pending-referenced backup.
- Add an injected filesystem interface for every failure point.

### T9: Prove transaction behavior

Files:

- `internal/hookconfig/manager_test.go`
- `internal/hookconfig/fs_test.go`

Work:

- Test install, update, uninstall, and repair against real temporary
  directories.
- Test no-op operations create no backup and change no timestamp.
- Inject create, write, sync, close, rename, delete, and directory-sync
  failures.
- Simulate interruption before and after target publication.
- Simulate a non-cooperating writer before and after rename.
- Test same-target lock contention and different-target concurrency.
- Run all concurrency cases under the race detector.

Verification:

```bash
gofmt -w internal/hookconfig internal/adapter
go test ./internal/hookconfig ./internal/adapter -race -count=20
go vet ./internal/hookconfig ./internal/adapter
git diff --check
```

Commit and push:

```text
feat: add crash-safe hook ownership
```

This commit is the reader and recovery floor before any CLI command writes a
vendor target.

## Commit 3: local hook management CLI

### T10: Add the hooks command group

Files:

- `cmd/drove/main.go`
- `cmd/drove/hooks.go`
- `cmd/drove/hooks_test.go`
- `cmd/drove/AGENTS.md`

Work:

- Register `hooks install`, `update`, `uninstall`, `status`, and `repair`.
- Add the exact common flags and command-specific flags from `spec.md`.
- Validate vendor, scope, project, agent, and repair combinations before file
  access.
- Load Drove config only to resolve the ownership root.
- Do not call `EnsureDaemon` for a mutation.
- Route every plan and mutation through `internal/hookconfig`.
- Keep command functions thin.

### T11: Add deterministic output and exit codes

Files:

- `cmd/drove/main.go`
- `cmd/drove/hooks.go`
- `cmd/drove/hooks_test.go`

Work:

- Render one deterministic human-readable target section.
- Emit the version 1 JSON result as one document with no extra text.
- Redact config content, environment values, probe output, and foreign
  commands.
- Map a typed failed check to exit code 1.
- Keep runtime and filesystem failures at exit code 2.
- Prove that dry run creates no directories or files.

### T12: Wire static status and repair

Files:

- `internal/hookconfig/status.go`
- `internal/hookconfig/status_test.go`
- `cmd/drove/hooks_test.go`

Work:

- Implement all configuration, ownership, policy, trust, runtime, and
  capability enum values.
- Implement aggregate ownership rules and per-node evidence.
- Report pending before, after, and conflict states without recovery writes.
- Implement ownership discard without target mutation.
- Make unknown trust a visible warning.
- Add static visible-policy detection without claiming that higher-precedence
  policy is absent.

Verification:

```bash
gofmt -w cmd/drove internal/hookconfig
go test ./internal/hookconfig ./cmd/drove -race -count=20
go test ./... -race -count=1
go vet ./...
make build
git diff --check
```

Commit and push:

```text
feat: manage vendor hook configuration
```

## Commit 4: bounded status probes

### T13: Add vendor capability and policy probes

Files:

- `internal/adapter/hook_probe.go`
- `internal/adapter/claude_hook_probe.go`
- `internal/adapter/codex_hook_probe.go`
- corresponding adapter tests and fixtures

Work:

- Probe executable versions only during `status --check`.
- Bound each process to two seconds and bound captured output.
- Treat version output as evidence, not the sole support decision.
- Detect known visible disabling policy.
- Return unknown when a higher-precedence or cloud policy cannot be proven.
- Keep all vendor process and protocol details inside `internal/adapter`.

### T14: Add optional Codex trust probing

Files:

- `internal/adapter/codex_hook_probe.go`
- `internal/adapter/codex_hook_probe_test.go`
- `internal/adapter/testdata/hook-probe/codex/`

Work:

- Start `codex app-server` only for `status --check`.
- Use the exact target project directory and environment.
- Negotiate the protocol before calling `hooks/list`.
- Send no mutation request.
- Match nodes by source and canonical hash.
- Map trusted, untrusted, modified, and managed states.
- Return unknown on an absent method, timeout, malformed response, unknown
  version, extra output, or unmatched node.
- Kill and wait for the subprocess on every return path.

### T15: Attach optional runtime evidence

Files:

- `internal/client/client.go`
- `internal/client/client_test.go`
- `cmd/drove/hooks.go`
- `cmd/drove/hooks_test.go`

Work:

- Reuse the existing read-only session status endpoint.
- Query it only when `--agent` is present.
- Do not auto-start the daemon.
- Reject a vendor mismatch.
- Map hook-active, attached non-active, detached, terminal, and unavailable
  states exactly as specified.
- Keep runtime evidence separate from static target status.

### T16: Enforce `status --check`

Files:

- `internal/hookconfig/status.go`
- `internal/hookconfig/status_test.go`
- `cmd/drove/hooks.go`
- `cmd/drove/hooks_test.go`

Work:

- Evaluate every documented check-failure condition.
- Do not fail only because trust is unknown.
- Fail stale runtime only when `--agent` was supplied.
- Include all warnings and evidence in JSON before returning the typed
  check-failure result.

Verification:

```bash
gofmt -w cmd/drove internal/adapter internal/client internal/hookconfig
go test ./internal/adapter ./internal/client ./internal/hookconfig ./cmd/drove -race -count=20
go test ./... -race -count=1
go vet ./...
make build
git diff --check
```

Commit and push:

```text
feat: report hook trust and runtime status
```

## Commit 5: documentation and end-to-end verification

### T17: Update user and architecture documentation

Files:

- `README.md`
- `docs/hooks.md`
- `docs/rfc-001-agent-state-and-control.md`
- `docs/phase-1b-hook-management-research.md`
- relevant `AGENTS.md` files

Work:

- Document each hook-management command and scope.
- Document command identity, PATH behavior, trust steps, backups, repair, and
  exact uninstall.
- Keep manual configuration as a supported fallback.
- State that configuration presence does not prove runtime activation.
- Mark RFC-001 Phase 1B implemented only after all checks pass.
- Record that per-session injection remains separate Issue #15 work.

### T18: Run the full automated suite

Run:

```bash
gofmt -w cmd internal
go test ./internal/adapter ./internal/hookconfig ./internal/client ./cmd/drove -race -count=20
go test ./... -race -count=1
go vet ./...
make build
npm --prefix web run typecheck
npm --prefix web run build
git diff --check
```

### T19: Run isolated transaction regressions

Use a temporary Drove data directory, HOME, and project directory. Verify:

- Claude user install, no-op install, update, drift rejection, and exact
  uninstall;
- Claude project install uses the portable command;
- Codex creates `hooks.json` when no representation exists;
- Codex preserves an existing JSON representation;
- Codex preserves an existing TOML representation and every untouched byte
  range;
- both Codex representations cause a refusal;
- a user edit after install survives uninstall;
- each interruption point recovers to the before or after manifest;
- a third target digest remains an explicit conflict;
- repair discards ownership without changing target bytes;
- backup permissions and retention are correct.

### T20: Run vendor end-to-end checks

With isolated configuration:

- install Claude hooks and complete vendor workspace trust manually;
- start Drove with hooks required and observe `SessionStart`;
- confirm that visible policy blocking remains separate from installation;
- uninstall and prove that foreign settings remain;
- install Codex JSON hooks and complete project and hook trust manually;
- observe one Codex signal through Drove;
- update one owned Codex node and confirm that vendor trust must be reviewed
  again;
- run Codex TOML install and uninstall against a commented fixture;
- confirm that neither installer writes trust state or a Drove session token.

Record exact Claude, Codex, Go, and operating-system versions in
`docs/hooks.md`.

### T21: Update issue state

- Update the separate persistent-installer issue with the delivered behavior.
- Do not close or change the scope of Issue #15 from this specification.
- Do not claim that Drove can approve vendor trust.

Commit and push:

```text
docs: document managed hook configuration
```
