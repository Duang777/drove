# Spec review checklist

## Direction

- [x] Phase 1B uses explicit local hook-management commands.
- [x] `drove up` does not mutate vendor configuration.
- [x] Per-session settings, notify, and OSC injection remain separate work.
- [x] The spec records the difference from the current Issue #15 body.
- [x] The design follows RFC-001 and the Phase 1B research.
- [x] The implementation starts only after owner approval.

## Scope

- [x] Claude Code and Codex are the only supported vendors.
- [x] User and project scopes are supported.
- [x] Install, update, uninstall, status, and ownership repair are included.
- [x] Dry-run and versioned JSON output are included.
- [x] Managed and system configuration is read-only.
- [x] Trust acceptance and bypass flags are excluded.
- [x] Daemon API, Web UI, Gemini, OSC, and legacy notify changes are excluded.
- [x] No event or database schema change is required.
- [x] Windows support is excluded from the first implementation.

## Architecture

- [x] `cmd/drove` owns flags, rendering, and exit-code mapping.
- [x] `internal/hookconfig` owns transactions and ownership records.
- [x] `internal/adapter` owns every vendor-specific path and document rule.
- [x] Hook-management commands do not start the daemon.
- [x] Runtime evidence uses an optional read-only client lookup.
- [x] The new dependency direction cannot create an internal package cycle.
- [x] The new package requires its own `AGENTS.md`.
- [x] The change acknowledges that it touches more than eight files.

## CLI

- [x] Every mutating command requires `--vendor` and `--scope`.
- [x] `--project` validation and default behavior are explicit.
- [x] Status defaults for vendor and scope are explicit.
- [x] `--agent` runtime lookup does not auto-start the daemon.
- [x] `--check` failure conditions are explicit.
- [x] Unknown trust is a warning, not an implicit success claim.
- [x] Dry run creates no local state.
- [x] Repair requires `--discard-ownership`.
- [x] Repair never edits the vendor target.
- [x] Exit codes follow the repository convention.
- [x] The check-failure exit code uses a typed result.

## Target resolution

- [x] Claude user and project paths are exact.
- [x] `CLAUDE_CONFIG_DIR` applies only to user scope.
- [x] Codex user and project candidate paths are exact.
- [x] `CODEX_HOME` applies only to user scope.
- [x] Relative environment overrides have a deterministic base.
- [x] The project directory must exist.
- [x] The target ID derivation is exact.
- [x] Target and managed-subdirectory symbolic links are rejected.
- [x] Non-regular and oversized targets are rejected.
- [x] Codex representation selection handles neither, either, and both files.
- [x] An existing Codex config without hooks causes creation of `hooks.json`.

## Managed command

- [x] The marker is exactly `--managed-by drove/v1`.
- [x] Legacy relay calls without the marker remain valid.
- [x] Other nonempty marker values are rejected.
- [x] User scope uses the resolved current executable path.
- [x] Project scope uses the portable literal command `drove`.
- [x] POSIX shell quoting is defined.
- [x] PATH failure for project scope produces a warning.
- [x] An executable move is handled by update, not install.
- [x] Tokens and session identifiers are absent from generated files.

## Vendor definitions

- [x] The 17 Claude event nodes are listed.
- [x] The Claude Notification matcher is exact.
- [x] The 12 Codex event nodes are listed.
- [x] Every generated handler is synchronous.
- [x] The timeout is fixed at five seconds.
- [x] Generated handlers contain no trust or decision field.
- [x] Stable logical node IDs are defined.
- [x] Canonical node hashing rules are defined.
- [x] A marker without a manifest does not prove ownership.

## JSON

- [x] The reader accepts one UTF-8 object and trailing whitespace.
- [x] Byte-order marks, trailing values, and duplicate keys are rejected.
- [x] Unknown values and array order are preserved.
- [x] Vendor structure is validated before planning.
- [x] Unknown fields on owned and foreign nodes have different safe handling.
- [x] The implementation uses a structured ordered tree.
- [x] Formatting changes are disclosed.
- [x] The writer format is deterministic.

## TOML

- [x] The dependency and exact version are specified.
- [x] The dependency supports the repository Go baseline.
- [x] Public semantic validation rejects duplicate and conflicting keys.
- [x] Byte-range parsing is isolated behind one private wrapper.
- [x] Supported `[hooks]` syntax is explicit.
- [x] Unsupported equivalent encodings are explicit.
- [x] Untouched byte ranges remain identical.
- [x] New event and table placement is deterministic.
- [x] The proposed complete file is reparsed before execution.
- [x] Dependency upgrades are guarded by golden tests.

## Ownership

- [x] The ownership root follows Drove `data_dir`.
- [x] Manifest, pending, lock, and backup paths are deterministic.
- [x] Directory and file permission modes are explicit.
- [x] The committed manifest schema is versioned.
- [x] The pending transaction schema is versioned.
- [x] Manifest records contain no vendor file bytes or secrets.
- [x] Unknown manifest versions block mutation.
- [x] Malformed manifests block mutation.
- [x] The manifest records only owned nodes.
- [x] File and container ownership survive updates.
- [x] The literal digest for a missing file is defined.

## Mutation semantics

- [x] Install adds missing nodes without adopting exact foreign nodes.
- [x] Install does not silently update an old owned definition.
- [x] Update requires a committed manifest.
- [x] Update can add new and remove obsolete owned nodes.
- [x] Update verifies every previous owned node before changing anything.
- [x] Uninstall uses the installed manifest hash.
- [x] Uninstall removes only exact owned nodes.
- [x] Empty containers are removed only when Drove created them.
- [x] A target is removed only when Drove created it and no foreign content
  remains.
- [x] No operation restores a complete backup.
- [x] One drifted node aborts the complete target transaction.
- [x] Node order and foreign array order remain stable.

## Transaction safety

- [x] The advisory lock location and five-second bound are explicit.
- [x] The target size limit is explicit.
- [x] A backup is synced before the pending record.
- [x] The pending record is durable before target mutation.
- [x] The target temporary file is in the target directory.
- [x] Existing target permissions are preserved.
- [x] New user and project target permissions are explicit.
- [x] The source digest is rechecked before rename.
- [x] The target directory is synced after rename or deletion.
- [x] The resulting bytes and nodes are verified before manifest commit.
- [x] The manifest directory is synced before pending removal.
- [x] Failures before and after target rename have distinct recovery behavior.
- [x] A non-cooperating writer is detected by digest verification.
- [x] Backup retention is bounded.

## Recovery

- [x] A before digest restores previous ownership metadata.
- [x] An after digest commits next ownership metadata.
- [x] A third digest is a conflict.
- [x] Status reports pending state without changing it.
- [x] Mutations recover a resolvable pending transaction under lock.
- [x] Repair can discard unresolved ownership metadata.
- [x] Repair does not delete unknown hook nodes.
- [x] Full-file automatic rollback is forbidden.

## Status

- [x] Configuration values and aggregation rules are explicit.
- [x] Ownership values and aggregation rules are explicit.
- [x] Policy values and evidence limits are explicit.
- [x] Claude trust remains unknown without a supported API.
- [x] Codex trust probing is read-only, bounded, and optional.
- [x] Runtime values depend on current-session evidence.
- [x] Capability values do not rely only on a version string.
- [x] Configuration presence is never reported as trust or activation.
- [x] The JSON result schema is versioned.
- [x] Human and JSON outputs omit sensitive config content.

## Idempotency

- [x] Repeated unchanged install and update write no bytes.
- [x] Unchanged operations create no backup.
- [x] Unchanged operations do not change timestamps.
- [x] Manifest serialization is deterministic.
- [x] Different targets can mutate concurrently.
- [x] One target has one serialized writer.
- [x] A plan is not silently recomputed during execution.

## Verification

- [x] Path, parsing, merge, ownership, drift, and uninstall cases are listed.
- [x] JSON and TOML golden tests are required.
- [x] Untouched TOML byte ranges are checked.
- [x] Every filesystem transaction stage supports fault injection.
- [x] Pending recovery covers before, after, absent, and conflict.
- [x] Concurrency runs under the race detector.
- [x] Permission and redaction checks are included.
- [x] Static policy, optional trust, and runtime status tests are included.
- [x] Isolated Claude and Codex end-to-end checks are included.
- [x] Full race, vet, Go build, Web typecheck, and Web build gates are included.
- [x] Each implementation commit is verified and pushed before the next.

## Approval

- [ ] The owner approved this specification for implementation.
