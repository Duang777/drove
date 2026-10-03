# Managed hook configuration

Status: Draft, awaiting owner approval

Date: 2026-10-03

Owner direction: continue RFC-001 Phase 1B with explicit hook installation,
idempotent updates, exact uninstall, ownership tracking, and separate trust
status.

Related issue:

- [#15](https://github.com/Duang777/drove/issues/15), hook availability without
  manual configuration

Research:

- [RFC-001](../../docs/rfc-001-agent-state-and-control.md)
- [Phase 1B hook management research](../../docs/phase-1b-hook-management-research.md)
- [Manual hook configuration](../../docs/hooks.md)

## Problem

Phase 1A can receive, normalize, persist, and arbitrate Claude Code and Codex
hook signals. It does not configure either vendor. Most new Drove
installations therefore enter heuristic fallback after the five-second hook
activation interval.

Editing vendor configuration is more dangerous than emitting a generated
snippet. The target files can contain unrelated settings, comments, other
hooks, organization policy, and changes made while Drove is running. A whole
file overwrite or backup restore can delete user work. A command string alone
also does not prove that Drove created the node.

Configuration presence is not runtime activation:

- Claude can suppress hooks through workspace trust, `disableAllHooks`, or
  `allowManagedHooksOnly`.
- Codex requires project trust and per-hook trust for non-managed hooks.
- A configured and trusted hook can still fail because the executable is not
  available in the vendor process environment.
- The only runtime proof is a valid hook signal observed by the current Drove
  session.

Phase 1B needs an explicit local configuration manager that preserves foreign
content, records exact ownership, survives interruption, and never writes
vendor trust state.

## Goal

Deliver this local workflow:

```text
drove hooks install, update, uninstall, status, or repair
    -> resolve one vendor and scope target
    -> parse the complete target document
    -> classify expected, owned, foreign, and drifted nodes
    -> produce an immutable change plan
    -> acquire the target lock for a mutation
    -> write a backup and pending transaction record
    -> atomically replace or remove the target
    -> commit the ownership manifest
    -> report configuration, ownership, policy, trust, runtime, and capability
```

The same planner powers dry runs and mutations. Dry-run output must describe
the bytes and ownership changes that the real operation would attempt from the
same starting content.

## Scope

This phase includes:

- `drove hooks install`, `update`, `uninstall`, `status`, and `repair`;
- user and project scopes for Claude Code and Codex;
- strict, structured JSON parsing and merge planning;
- syntax-preserving edits to Codex `config.toml`;
- deterministic selection between Codex `hooks.json` and `config.toml`;
- stable logical node IDs and canonical SHA-256 node hashes;
- committed ownership manifests and recoverable pending transaction records;
- per-target advisory locks, pre-write backups, digest rechecks, atomic target
  replacement, and directory sync;
- exact uninstall and empty-container cleanup;
- a no-behavior `--managed-by drove/v1` marker on `drove hook`;
- separate configuration, ownership, policy, vendor trust, runtime, and
  capability status dimensions;
- optional read-only Codex trust probing during `status --check`;
- versioned JSON output and deterministic text output;
- fault injection, concurrent process tests, golden merge tests, and isolated
  vendor end-to-end checks.

The implementation is expected to touch more than eight files. The package
split is required because vendor document rules belong to `internal/adapter`,
while filesystem transactions and ownership belong to a new
`internal/hookconfig` package.

## Non-goals

This phase does not:

- install hooks automatically from `drove up`;
- inject per-session `--settings`, `-c notify`, or OSC configuration;
- add OSC terminal parsing or Codex legacy notify normalization;
- add a Gemini adapter;
- modify managed or system-owned vendor configuration;
- accept Claude workspace trust, Codex project trust, or Codex hook trust;
- write `hooks.state.*.trusted_hash`;
- use `--dangerously-bypass-hook-trust`;
- adopt an identical foreign node;
- provide a generic `--force` overwrite;
- restore a whole vendor file from a backup during uninstall;
- expose hook management through the daemon REST API or Web UI;
- persist hook-management actions in the session event log;
- support Windows in the first implementation;
- change Phase 1A signal, Detector, or session-state semantics.

## Chosen architecture

The design has three owners.

`cmd/drove` owns command parsing, text or JSON rendering, and exit-code
mapping. Hook configuration commands run locally. Install, update, uninstall,
and repair do not start or contact `droved`.

`internal/hookconfig` owns target locking, file reads, raw file digests,
backups, pending transactions, atomic replacement, ownership manifests,
recovery, and operation orchestration. It has no Claude or Codex branches.

`internal/adapter` owns vendor target resolution, representation selection,
expected hook definitions, JSON or TOML inspection, merge and unmerge
planning, visible policy interpretation, capability probes, and trust probes.
No other package branches on Claude or Codex configuration fields.

The dependency direction is:

```text
cmd/drove
    -> internal/hookconfig
        -> internal/adapter
        -> internal/config
        -> internal/version

cmd/drove
    -> internal/client
        -> daemon, only for optional runtime evidence in hooks status
```

`internal/hookconfig` does not import `internal/client` or
`internal/session`. The CLI may attach one already-fetched runtime observation
to a static status result. This keeps local configuration usable while the
daemon is stopped.

## CLI contract

The command tree is:

```text
drove hooks install   --vendor claude|codex --scope user|project [--project DIR] [--dry-run] [--json]
drove hooks update    --vendor claude|codex --scope user|project [--project DIR] [--dry-run] [--json]
drove hooks uninstall --vendor claude|codex --scope user|project [--project DIR] [--dry-run] [--json]
drove hooks status    --vendor claude|codex|all [--scope user|project] [--project DIR] [--agent ID] [--check] [--json]
drove hooks repair    --vendor claude|codex --scope user|project [--project DIR] --discard-ownership [--dry-run] [--json]
```

Every mutating command requires both `--vendor` and `--scope`. `status`
defaults to `--vendor all` and reports both scopes only when each target can be
resolved. If `--scope` is omitted, status reports the user scope and the
project scope rooted at the current directory.

`--project` is valid only when the selected scopes include `project`. The
default project directory is the current working directory. Drove resolves it
to an absolute path, evaluates the project-directory symlink once, and
requires the result to be an existing directory.

### Install

`install` adds each missing expected node.

- An exact Drove-owned node remains unchanged.
- An exact foreign node remains foreign and satisfies that logical event.
- A missing node is added and recorded as owned.
- A manifest that points to a changed or missing node is a conflict.
- An owned node from an older Drove definition is not changed. The command
  returns `upgrade_required` and directs the user to `update`.
- A second install against unchanged bytes creates no backup and performs no
  write.

### Update

`update` requires an existing committed manifest.

- It replaces only a node whose current canonical hash equals the hash in the
  manifest.
- It removes an obsolete owned node only when the same exact-hash check
  passes.
- It adds a newly required node when no equivalent foreign node exists.
- It leaves an exact foreign node unchanged.
- Any changed or missing owned node aborts the complete target transaction.
- A second update against the current definition performs no write.

### Uninstall

`uninstall` removes only nodes whose location and canonical hash match the
committed manifest.

After node removal, it removes an empty matcher group or event container only
when the manifest says that Drove created that container. It removes the
target file only when Drove created the file and the remaining document has no
foreign content. It never restores a complete backup.

A missing manifest performs no deletion and returns `unchanged` with a
`not_owned` warning, even if matching commands exist in the target.

### Status

`status` reads files and manifests but changes nothing. It does not create a
lock file, complete a pending transaction, start the daemon, or change vendor
trust.

`--check` adds bounded external probes and returns a nonzero status when:

- configuration is `invalid` or `partial`;
- ownership is `drifted`;
- policy is `disabled` or `blocked`;
- vendor trust is known to be `untrusted` or `modified`;
- capability is `unsupported`;
- `--agent` names a matching live session whose runtime status is
  `not_observed`;
- a pending transaction has a target digest that matches neither its before
  nor after digest.

Unknown trust produces a warning and does not by itself fail `--check`.
Unknown capability fails only when the target structure is known to be
unsupported. A runtime value of `stale` fails only when the user supplied
`--agent`.

`--agent` asks an already-running daemon for one session status. The command
does not start a daemon. The Agent vendor must equal the selected vendor.

### Repair

`repair --discard-ownership` deletes the committed manifest and pending
transaction record without changing the vendor target. Existing hook nodes
become foreign.

This command is the only metadata escape hatch. It exists for a pending
transaction conflict or a manifest whose owned node was edited manually.
There is no adoption or forced target rewrite in this phase.

### Dry run

`--dry-run` parses the current target and produces the same immutable plan as
the real operation. It creates no directories, locks, backups, temporary
files, manifests, or pending records.

The plan includes:

- the canonical target path and representation;
- the current raw-file SHA-256 digest;
- logical node additions, replacements, removals, and unchanged nodes;
- containers and files that would be created or removed;
- ownership records that would be added, replaced, or deleted;
- warnings and conflicts.

Dry run does not promise that a later real command sees the same file. The real
command always replans under its lock.

### Exit codes

The CLI keeps the repository convention:

| Exit code | Meaning |
| ---: | --- |
| 0 | The operation succeeded, was unchanged, or produced a clean dry run. |
| 1 | Arguments are invalid, or `status --check` found an actionable state. |
| 2 | Parsing, locking, probing, filesystem, transaction, or internal work failed. |

The main package maps a typed check result to exit code 1. It does not infer
the result from an error string.

## Output contract

Human-readable output is deterministic and contains no raw hook payload,
token, complete foreign command, or backup content. It prints one target per
section and one line for each status dimension.

`--json` emits one JSON document and no explanatory text. The top-level schema
is version 1:

```json
{
  "version": 1,
  "operation": "status",
  "results": [
    {
      "vendor": "claude",
      "scope": "user",
      "target_path": "/home/user/.claude/settings.json",
      "representation": "json",
      "change": "unchanged",
      "configuration": "present",
      "ownership": "owned",
      "policy": "unknown",
      "trust": "unknown",
      "runtime": "stale",
      "capability": "supported",
      "target_sha256": "sha256:...",
      "manifest_path": "/home/user/.drove/hooks/manifests/....json",
      "pending": false,
      "nodes": [
        {
          "id": "claude.SessionStart.v1",
          "configuration": "present",
          "ownership": "owned",
          "installed_sha256": "sha256:..."
        }
      ],
      "warnings": []
    }
  ]
}
```

Stable enum values are:

| Dimension | Values |
| --- | --- |
| `change` | `changed`, `unchanged`, `would_change`, `conflict` |
| `configuration` | `absent`, `present`, `partial`, `invalid` |
| `ownership` | `owned`, `foreign`, `drifted`, `unknown` |
| `policy` | `allowed`, `disabled`, `blocked`, `unknown` |
| `trust` | `trusted`, `untrusted`, `modified`, `managed`, `unknown` |
| `runtime` | `observed`, `not_observed`, `stale` |
| `capability` | `supported`, `unsupported`, `unknown` |

The aggregate ownership value is `drifted` if any recorded node fails its
exact check. It is `owned` when every expected node is present and owned. It
is `foreign` when every expected node is exact and none is owned. Other
combinations are `unknown`; the per-node entries explain mixed ownership.

Paths in JSON output are necessary evidence. Diagnostics must not include the
content of vendor config values or environment variables.

## Target resolution

The adapters resolve these targets:

| Vendor | Scope | Default target |
| --- | --- | --- |
| Claude | user | `$CLAUDE_CONFIG_DIR/settings.json`, or `~/.claude/settings.json` |
| Claude | project | `<project>/.claude/settings.json` |
| Codex | user | `$CODEX_HOME/hooks.json` or `$CODEX_HOME/config.toml`, with `~/.codex` as the default home |
| Codex | project | `<project>/.codex/hooks.json` or `<project>/.codex/config.toml` |

Environment overrides apply only to user scope. An empty override is treated
as unset. Relative override paths are resolved against the current working
directory and then made absolute.

Drove rejects:

- a target that is a symbolic link;
- a symbolic link in `.claude` or `.codex` below the resolved scope root;
- a target that is not a regular file;
- a target larger than 4 MiB;
- a project path that does not exist or is not a directory;
- a target path outside the resolved scope root.

The target path stored in a manifest is the cleaned absolute path. The target
ID is the lowercase hexadecimal SHA-256 of:

```text
vendor NUL scope NUL canonical-target-path
```

### Codex representation selection

For one scope, the Codex adapter applies these rules in order:

1. If a committed manifest exists, use its recorded representation and target.
2. If both `hooks.json` and `config.toml` contain a `hooks` definition, report
   `invalid` and refuse a mutation.
3. If only `config.toml` contains a `hooks` table, use TOML.
4. If `hooks.json` exists, use JSON.
5. Otherwise, create `hooks.json`.

An existing `config.toml` without hooks does not cause Drove to add a second
concern to that file. Drove creates `hooks.json`.

Codex `hooks.json` may contain only `description` and `hooks`. Unknown
top-level keys make the target invalid because Codex rejects them. Drove
preserves an existing description. A new file uses
`"Drove lifecycle reporting"` as its description.

## Managed command identity

Every generated command contains this no-behavior marker:

```text
drove hook --vendor <vendor> --managed-by drove/v1
```

`drove hook` accepts `--managed-by` only when its value is empty or
`drove/v1`. The flag does not change relay behavior, output, retry, or exit
status. A different nonempty value is a usage error.

For user scope, the installer uses `os.Executable`, resolves symbolic links,
and embeds the absolute current `drove` path. It quotes each POSIX shell
argument independently.

For project scope, the installer emits the literal command name `drove`.
Project configuration is portable and therefore depends on `drove` being in
the vendor process `PATH`. Install and status report a warning when
`exec.LookPath("drove")` fails in the current environment.

The quoting function uses single quotes and replaces an embedded single quote
with the standard POSIX shell sequence. The project does not support Windows
in this phase.

An executable path change creates a new desired node hash. `update` replaces
the old exact owned node. `install` reports `upgrade_required`.

## Expected hook nodes

Every generated handler is synchronous and has:

- `type = command`;
- the managed command string;
- `timeout = 5`;
- no `once`, async, decision, or trust field.

The Claude adapter defines these logical nodes:

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

The `Notification` node uses this exact matcher:

```text
permission_prompt|elicitation_dialog|elicitation_url_dialog|agent_needs_input|idle_prompt|quota_auto_resume_stale
```

All other generated Claude groups omit `matcher`.

The Codex adapter defines all 12 current event nodes:

```text
PreToolUse
PermissionRequest
PostToolUse
PreCompact
PostCompact
SessionStart
SessionEnd
UserPromptSubmit
SubagentStart
SubagentStop
Stop
Interrupt
```

Each logical node is one matcher group with one command handler. The stable ID
is `<vendor>.<event>.v1`, except the Claude Notification ID is
`claude.Notification.waiting.v1`.

The canonical node hash covers the complete matcher group. The canonical JSON
encoder sorts object keys, preserves array order, emits UTF-8 without HTML
escaping, and accepts only strings, booleans, null, arrays, objects, and
base-10 integers. The stored form is `sha256:` followed by lowercase
hexadecimal.

An exact foreign group can satisfy one logical node. Drove never records that
group as owned. A group with the marker but no matching manifest is still
foreign.

## Structured document handling

### JSON

The JSON document reader:

- accepts one UTF-8 JSON object and trailing whitespace;
- rejects a byte-order mark, trailing values, and duplicate keys at any
  depth;
- preserves unknown object members and all array order;
- validates the vendor-specific hook structure before planning;
- treats an owned node with an unknown field as drifted;
- treats an external node with unknown fields as foreign and leaves it
  unchanged.

The implementation uses `encoding/json.Decoder` tokens to build a private
ordered JSON tree. It does not use regular expressions or string replacement.
Writing JSON uses two-space indentation and one trailing newline. A changed
JSON file can therefore have formatting changes, but its unrelated values and
array order remain the same.

### TOML

Codex TOML support uses `github.com/pelletier/go-toml/v2` pinned to `v2.4.3`.
The public decoder performs semantic validation, including duplicate-key and
table-redefinition checks. The `unstable` parser supplies byte ranges for
syntax-preserving edits. A private adapter wrapper contains all use of the
unstable API, and golden tests pin its behavior before any dependency update.

The first version supports one ordinary `[hooks]` table with event values as
arrays of inline matcher groups. It refuses these equivalent but ambiguous
forms:

- a dotted `hooks.<event>` assignment outside `[hooks]`;
- `hooks` as an inline table;
- repeated or redefined `[hooks]` tables;
- an array-of-tables encoding for a hook event;
- a hook event value that is not an array of inline tables.

For supported input, Drove edits only the byte ranges for the affected event
arrays. Untouched keys, comments, whitespace, line endings, quoting, and
ordering remain byte-identical. A new event is appended to the existing
`[hooks]` table using its line-ending style. A new `[hooks]` table is appended
with one separating blank line.

After every plan, the adapter parses the complete proposed bytes again with
the public semantic decoder and verifies every expected node. A parse or
verification failure prevents a filesystem transaction.

## Ownership manifest

The default ownership root is:

```text
~/.drove/hooks/
```

When `DROVE_DATA_DIR` or `data_dir` changes the Drove data directory, the
ownership root is `<data_dir>/hooks/`.

It contains:

```text
hooks/
  locks/
    <target-id>.lock
  manifests/
    <target-id>.json
    <target-id>.pending.json
  backups/
    <target-id>/
      <utc-timestamp>-<before-digest>.<json|toml>
```

The hooks root, manifest directory, lock directory, and backup directory use
mode `0700`. Manifests, pending records, lock files, and backups use mode
`0600`.

A committed manifest has version 1:

```json
{
  "version": 1,
  "state": "committed",
  "target_id": "lowercase-hex-sha256",
  "vendor": "claude",
  "scope": "user",
  "project_root": "",
  "target_path": "/home/user/.claude/settings.json",
  "representation": "json",
  "target_created": false,
  "created_containers": [
    "hooks.SessionStart"
  ],
  "nodes": [
    {
      "id": "claude.SessionStart.v1",
      "event": "SessionStart",
      "installed_sha256": "sha256:..."
    }
  ],
  "last_before_sha256": "sha256:...",
  "last_after_sha256": "sha256:...",
  "backup_path": "/home/user/.drove/hooks/backups/.../....json",
  "writer_version": "dev",
  "writer_commit": "unknown",
  "created_at": "2026-10-03T00:00:00Z",
  "updated_at": "2026-10-03T00:00:00Z"
}
```

`target_created` remains true across updates until Drove removes the target.
`created_containers` contains only containers Drove created and still owns.
The manifest records only Drove-owned nodes. It does not record exact foreign
nodes that satisfy part of the desired configuration.

The manifest contains no vendor file bytes, token, environment value, raw hook
payload, or foreign command.

Unknown manifest versions make ownership `unknown` and block mutations.
Malformed manifests make ownership `drifted` and block mutations.

## Pending transaction record

One pending record exists per target. Its version 1 shape is:

```json
{
  "version": 1,
  "state": "pending",
  "transaction_id": "uuid",
  "operation": "install",
  "target_id": "lowercase-hex-sha256",
  "target_path": "/home/user/.claude/settings.json",
  "before_target_sha256": "sha256:...",
  "after_target_sha256": "sha256:...",
  "previous_manifest": null,
  "next_manifest": {},
  "backup_path": "/home/user/.drove/hooks/backups/.../....json",
  "started_at": "2026-10-03T00:00:00Z"
}
```

The literal value `absent` replaces a target digest when the target does not
exist before or after the transaction. `previous_manifest` and
`next_manifest` contain complete validated version 1 manifests or null.

The pending record is the recovery authority:

- If the actual target digest equals `before_target_sha256`, recovery restores
  `previous_manifest` and removes the pending record.
- If the actual target digest equals `after_target_sha256`, recovery installs
  `next_manifest` and removes the pending record.
- Any third digest is a conflict. Recovery changes nothing.

Status reports these cases but remains read-only. Install, update, and
uninstall run recovery under the target lock before they create a new plan.
Repair can discard both ownership records without changing the target.

## Filesystem transaction

A mutation uses this order:

1. Resolve the scope, target, target ID, representation, command identity, and
   ownership paths.
2. Create the ownership directories with mode `0700`.
3. Open `<target-id>.lock` with mode `0600` and acquire an exclusive advisory
   lock. Retry every 50 milliseconds for at most five seconds.
4. Recover an existing pending transaction when its target digest matches the
   recorded before or after digest. Stop on a third digest.
5. Use `Lstat` to reject target and managed subdirectory symlinks. Read at most
   4 MiB plus one byte.
6. Parse the complete target and committed manifest.
7. Build and validate one immutable plan.
8. If the proposed target bytes and ownership manifest are unchanged, return
   `unchanged` without a backup or write.
9. If the target exists, write a mode `0600` backup under the ownership root
   and sync it.
10. Atomically write and sync the pending record.
11. Write the proposed target to a temporary file in the target directory.
    Preserve an existing target's permission bits. New user files use `0600`;
    new project files use `0644`.
12. Sync and close the temporary target.
13. Re-read the target path and compare its raw digest with the plan's before
    digest. If they differ, remove the temporary file and pending record, keep
    the backup, and return a concurrent-modification error.
14. Rename the temporary target over the target, or remove the target for a
    planned file deletion. Sync the target directory.
15. Re-read and parse the resulting target. Verify the after digest and every
    planned node.
16. Atomically write and sync `next_manifest`, or remove the committed
    manifest when it is null. Sync the manifest directory.
17. Remove the pending record and sync the manifest directory.
18. Release the lock.

If a failure happens before step 14, the target remains unchanged. If a
failure happens at or after step 14, the pending record remains the recovery
authority.

An operating-system process that does not take Drove's advisory lock can still
write the target. The before-digest check prevents Drove from overwriting a
change made before rename. The after-digest verification exposes a write that
races after rename.

The transaction engine keeps the five newest committed backups per target and
every backup named by a pending record. It prunes older backups only after a
successful manifest commit.

## Merge and ownership rules

The planner classifies each logical node as:

- `missing`, when no equivalent group exists;
- `owned_exact`, when the manifest location and hash match;
- `foreign_exact`, when an equivalent group exists without matching
  ownership;
- `owned_drifted`, when a manifest location exists but its hash differs;
- `owned_missing`, when the manifest names a node that no longer exists;
- `foreign_conflict`, when a marker or event location resembles Drove's node
  but the complete group differs.

Node order is stable. Drove appends a new group after existing groups for that
event and appends a new event after existing event keys. It never sorts a
foreign array.

An operation is target-atomic. One drifted node prevents every addition,
replacement, or removal for that target.

Uninstall uses the node hash stored in the manifest, not the current binary's
desired hash. A newer Drove binary can therefore remove an older exact owned
installation.

An update can change the expected node set. It removes obsolete exact owned
nodes, replaces changed exact owned nodes, adds missing new nodes, and leaves
foreign exact nodes in place.

## Status dimensions

### Configuration

- `absent`: no expected node is present.
- `present`: every expected node is present exactly.
- `partial`: at least one but not every expected node is present.
- `invalid`: the document or supported hook structure cannot be parsed.

### Ownership

- `owned`: every expected node is exact and listed in the committed manifest.
- `foreign`: every expected node is exact and none is listed in the manifest.
- `drifted`: any manifest node is missing, moved, or hash-mismatched.
- `unknown`: there is no conclusive aggregate, including mixed owned and
  foreign nodes or an unknown manifest version.

### Policy

Adapters inspect only documented, readable policy sources.

For Claude:

- `disabled` means an effective visible `disableAllHooks = true`.
- `blocked` means a visible managed `allowManagedHooksOnly = true` excludes the
  selected non-managed target.
- `allowed` requires a successful supported diagnostic that reports the target
  hooks as enabled.
- other cases are `unknown`.

For Codex:

- `disabled` means the effective supported probe reports hooks disabled.
- `blocked` means managed requirements allow only managed hooks.
- `allowed` means the supported probe reports the selected hooks enabled.
- other cases are `unknown`.

Static local files can prove a disabling value but cannot prove that no
higher-precedence or cloud-managed policy exists.

### Vendor trust

Claude trust is `unknown`. Claude has no documented machine-readable workspace
trust API.

For Codex, `status --check` may start `codex app-server` with a two-second
deadline and call `hooks/list` only when the running protocol advertises that
method. The adapter matches returned nodes by target source and canonical
hash:

- `trusted`, `untrusted`, `modified`, and `managed` mirror the returned state;
- a missing method, timeout, malformed response, unknown version, or unmatched
  node produces `unknown`;
- the probe never calls a mutation RPC and never writes trust state.

The ordinary `status` command does not start a vendor process and reports
`unknown`.

### Runtime

Without `--agent`, runtime is `stale` because a static target cannot prove
activation for a current session.

With `--agent`:

- `observed` means the existing daemon reports the matching vendor session as
  `hook_active`;
- `not_observed` means the matching attached session reports
  `awaiting_hook`, `fallback`, or `required_failed`;
- `stale` means the daemon is unavailable or the session is detached or
  terminal.

The CLI rejects a vendor mismatch between `--agent` and the selected target.
Runtime evidence never changes configuration or ownership status.

### Capability

- `supported`: Drove understands the selected representation, current target
  structure, and expected node schema.
- `unsupported`: Drove recognizes a structure or probed vendor response that
  it cannot modify safely.
- `unknown`: the executable is absent, version output is malformed, or an
  optional probe is unavailable.

Version output is evidence, not the sole support decision. Install and update
always reparse their own output.

## Concurrency and idempotency

The lock serializes Drove processes for one canonical target. Different
targets use different locks and can change concurrently.

The immutable plan includes the raw before digest. The executor rejects a plan
when the target changes before rename. It never silently replans midway
through a transaction.

For unchanged input:

- repeated install emits identical target and manifest bytes;
- repeated update emits identical target and manifest bytes;
- repeated uninstall after successful removal returns `unchanged` with a
  `not_owned` warning;
- repeated status emits the same static fields;
- no unchanged operation updates timestamps or creates a backup.

Manifest JSON uses deterministic key order, two-space indentation, UTC
RFC3339Nano timestamps, and one trailing newline.

## Failure handling and rollback

Drove does not automatically restore the full backup after a completed target
rename. The user may have changed unrelated settings after installation.

The supported recovery actions are:

- rerun the same mutation to complete a pending before-or-after state;
- fix the target manually, then rerun status;
- use `repair --discard-ownership` to abandon stale ownership metadata;
- use a named backup as a manual reference.

The CLI reports the backup path after a changed operation. It never prints
backup content.

The manifest format is reader-first. Before the first writer is enabled, tests
must prove that unknown manifest and pending versions are read-only and block
mutations. After version 1 manifests exist, a binary that does not understand
them must not edit their targets.

## Security and privacy

- Hook management is an explicit local command.
- Drove never changes vendor trust or invokes a trust bypass.
- Target and ownership paths reject unexpected symbolic links.
- Temporary target files are created in the target directory.
- Backups and ownership metadata use mode `0600`.
- New user configuration uses mode `0600`.
- Existing target permission bits are preserved.
- The command builder shell-quotes each argument.
- No token is written to a vendor file or ownership record.
- JSON output includes evidence paths and hashes, not config values.
- Errors wrap causes but redact target content and external process output.
- Vendor probes have bounded input, output, and duration.
- Codex app-server output is parsed as a strict protocol. Unrecognized
  messages cannot change status from `unknown`.

## Compatibility

This phase adds one dependency:

```text
github.com/pelletier/go-toml/v2 v2.4.3
```

The dependency requires Go 1.21 and is compatible with the repository's Go
1.23 baseline. All use of its unstable parser stays behind one private TOML
document wrapper.

The existing relay command remains compatible:

```text
drove hook --vendor claude
drove hook --vendor codex
```

The optional marker is ignored after validation:

```text
drove hook --vendor claude --managed-by drove/v1
```

No event, database, daemon API, or session status schema changes.

## Module ownership

| Module | Owns | Does not own |
| --- | --- | --- |
| `cmd/drove` | flags, local command wiring, rendering, exit codes, optional daemon lookup for `--agent` | parsing vendor files, merge rules, filesystem transactions |
| `internal/hookconfig` | operations, locks, digests, backups, manifests, pending recovery, atomic writes | Claude or Codex keys, hook event lists, trust mutation |
| `internal/adapter` | target resolution, representation choice, expected nodes, structured merge and unmerge, policy and trust probes | locks, backups, manifest persistence, CLI rendering |
| `internal/config` | Drove data directory used for ownership records | vendor config paths |
| `internal/client` | read-only lookup of an existing session for runtime evidence | starting a daemon for hook status, local file edits |
| `internal/version` | writer version and commit recorded in manifests | ownership decisions |

The new `internal/hookconfig` package gets an `AGENTS.md` before code lands.
`internal/AGENTS.md` adds the dependency edge
`cmd/drove -> hookconfig -> adapter`.

## Alternatives considered

### Per-session injection

Claude `--settings` can inject hooks without changing user files. Codex can
inject legacy notify and OSC settings with `-c`. These mechanisms reduce setup
but create a second configuration lifecycle and do not solve explicit
ownership, update, or uninstall. Codex session hook trust also relies on
version-sensitive behavior.

This phase follows the RFC and the approved continuation direction: implement
an explicit installer first. Per-session injection remains separate work under
Issue #15 and must reuse the same adapter node definitions if implemented.

### Whole-file backup restore

Restoring the pre-install file would remove user edits made after
installation. The chosen design uses backups only as evidence and removes
exact owned nodes.

### Command text as ownership

Another tool or the user can create the same command. A marker improves human
inspection but does not prove ownership. The chosen design requires both a
committed manifest and an exact node hash.

### Rewrite all TOML

A normal TOML marshal loses comments and creates large diffs. The chosen
design validates semantics with the public decoder and edits only AST byte
ranges.

### Upgrade the repository to Go 1.26

Newer format-preserving TOML libraries require Go 1.26. Phase 1B does not
justify a toolchain upgrade. The pinned Go 1.23-compatible parser keeps the
change within the current baseline.

## Verification

Automated tests must cover:

- every path and environment override in the target table;
- missing home, missing project, non-directory project, and relative override
  paths;
- regular files, target symlinks, managed-directory symlinks, and oversized
  files;
- empty, valid, malformed, duplicate-key, trailing-value, and invalid UTF-8
  JSON;
- valid TOML, duplicate keys, table redefinition, unsupported equivalent
  forms, CRLF input, comments, and multiline arrays;
- Codex representation selection with neither, either, and both hook sources;
- all 17 Claude nodes and all 12 Codex nodes;
- Notification matcher exactness;
- exact foreign, owned exact, mixed, drifted, missing, and marker-only nodes;
- install, update, uninstall, and repair plans as golden files;
- byte-identical untouched TOML ranges;
- semantic preservation of unrelated JSON values and all foreign array order;
- user absolute executable commands, project portable commands, shell quoting,
  and executable movement;
- no-op install and update with no backup or timestamp change;
- uninstall cleanup for shared arrays, created containers, created targets,
  and targets with later foreign content;
- every pending recovery state, including before, after, absent, and third
  digest;
- failures while creating, writing, syncing, closing, renaming, deleting, and
  syncing directories;
- target changes before rename and after rename;
- lock contention and independent-target concurrency under the race detector;
- file and directory permission modes;
- backup retention and pending-backup retention;
- unknown and malformed manifest versions;
- static policy values and unknown higher-precedence policy;
- Codex trust probe success, timeout, unsupported method, malformed output,
  and output limit;
- runtime observed, not observed, stale, daemon unavailable, and vendor
  mismatch;
- text output redaction and versioned JSON output;
- `--check` exit codes and no daemon auto-start;
- legacy `drove hook` behavior with and without the managed marker.

Manual verification must use an isolated HOME and project directory:

1. Install Claude user hooks into a missing file.
2. Verify idempotent install and update.
3. Run a trusted Claude session and observe `SessionStart`.
4. Confirm that a blocked Claude policy remains distinct from configuration
   presence.
5. Uninstall and verify that an unrelated setting and foreign hook remain.
6. Install Codex JSON hooks, review trust in Codex, and observe a signal.
7. Install into an existing Codex TOML `[hooks]` table and verify that
   unrelated bytes remain identical.
8. Update a Codex node and confirm that Codex requires trust review again.
9. Edit an owned node manually and verify that update and uninstall stop with
   drift.
10. Interrupt each transaction stage and verify pending recovery.
11. Confirm that no real user config, trust state, or Drove session token
    changes during the isolated checks.

Repository gates are:

```bash
gofmt -w cmd internal
go test ./internal/adapter ./internal/hookconfig ./cmd/drove -race -count=20
go test ./... -race -count=1
go vet ./...
make build
npm --prefix web run typecheck
npm --prefix web run build
git diff --check
```

## Acceptance criteria

- Hook configuration changes happen only after an explicit local CLI command.
- `drove up` does not install or update vendor files.
- Vendor file knowledge remains in `internal/adapter`.
- Filesystem transaction and ownership policy remain in
  `internal/hookconfig`.
- Install and update are byte-idempotent.
- Dry run writes nothing.
- JSON changes preserve unrelated values and array order.
- TOML changes preserve every untouched byte range.
- Drove never overwrites a concurrent target change.
- Every target mutation has a synced pending record first.
- A restart resolves before and after digests deterministically.
- A third digest stops recovery and requires explicit repair.
- Update and uninstall affect only exact manifest-owned nodes.
- Uninstall never restores a whole backup.
- Foreign hooks remain unchanged.
- Drove-created empty containers are removed safely.
- Trust, policy, configuration, ownership, capability, and runtime remain
  separate status dimensions.
- Unknown trust is never presented as trusted.
- Drove does not write trust state or use a bypass flag.
- Existing relay calls without `--managed-by` remain valid.
- Human and JSON output contain no token, raw hook payload, or foreign config
  value.
- The complete automated and isolated manual verification suites pass.
- Each independently verifiable implementation commit is pushed before the
  next starts.

## Risks and controls

### The TOML parser API is unstable

The parser module labels its range API unstable. One private wrapper and
golden byte-preservation tests contain that risk. Dependency upgrades require
those tests before merge.

### An absolute user command becomes stale

Moving or replacing the Drove executable changes the desired node. Status
reports the mismatch, and update replaces the exact old owned node.

### A project command is absent from the vendor PATH

Project configuration uses the portable `drove` command. Install and status
warn when the current environment cannot resolve it. Runtime activation
remains the final proof.

### A vendor changes its hook schema

The adapter rejects unsupported structures and reports capability
`unsupported`. It does not partially write a guessed format.

### A process changes the file after Drove's digest check

Atomic rename prevents partial bytes, but no advisory lock can exclude a
non-cooperating writer. Post-rename verification leaves a pending conflict if
the resulting digest is not the planned digest.

### A backup stores sensitive vendor settings

Backups stay under the Drove data directory with mode `0600`. Output reports
only the backup path. The retention limit is five committed backups per
target.

## Fragile assumption

This plan assumes Codex continues to accept a normal `[hooks]` table and
`hooks.json` with the documented matcher-group structure. If that assumption
fails, the adapter reports `unsupported` and performs no mutation. Claude
management remains usable because the transaction engine and vendor planners
are separate.

## Approval gate

Implementation starts only after the owner approves this specification,
`checklist.md`, and `tasks.md`.
