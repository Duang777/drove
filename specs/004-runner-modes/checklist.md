# Spec review checklist

## Scope

- [x] Phase 0 adds only interactive and oneshot runner behavior.
- [x] Input, resize, hooks, detectors, and Blocked recovery remain out of scope.
- [x] Process reconnection and graceful signal escalation remain out of scope.
- [x] The existing adapter registry remains in place.
- [x] No SQLite migration, event type, production package, or dependency is added.
- [x] Expected files and two implementation commit boundaries are explicit.

## Runner mode

- [x] `agent.RunMode` is the single Go domain type.
- [x] New requests default to interactive.
- [x] Historical metadata without mode falls back to oneshot.
- [x] Interactive and oneshot are the only valid normalized values.
- [x] Invalid request values are rejected before events or processes are created.
- [x] Agent remains the durable metadata authority.
- [x] Status exposes one normalized mode.

## Adapter boundary

- [x] Vendor command knowledge remains in `internal/adapter`.
- [x] Claude maps to `claude` or `claude --print`.
- [x] Codex maps to `codex` or `codex exec`.
- [x] Generic has no adapter-supplied command in either mode.
- [x] Custom command and argument values are not rewritten.
- [x] PTY receives only a resolved command and does not import mode.
- [x] A registry-wide launch-resolution refactor is excluded.

## Persistence and recovery

- [x] Creation metadata stays at version 1.
- [x] New writers always include mode.
- [x] A pointer distinguishes a missing legacy field from an invalid empty field.
- [x] Invalid persisted mode blocks bootstrap with event and session context.
- [x] State-only legacy histories recover as oneshot.
- [x] Old binaries can ignore the additive mode field.
- [x] Historical done still reconciles to stopped without an interruption error.

## Exit semantics

- [x] Successful natural oneshot exit reaches done.
- [x] Natural interactive exit reaches stopped.
- [x] Failed natural exit reaches stopped and persists the process error.
- [x] A stop cause that wins the exit claim always reaches stopped.
- [x] Expected kill errors are not stored as LastError.
- [x] Stop intent and exit claim share one Manager lock.
- [x] The first lock-protected claim determines concurrent exit behavior.
- [x] Daemon shutdown marks all attached sessions before closing any PTY.
- [x] Natural exit detaches the matching PTY after terminal state persistence.
- [x] Stop after done or stopped is idempotent.
- [x] `idle -> done` is added for successful oneshot exit.
- [x] Interactive Agents ignore heuristic Done hints.

## Public contracts

- [x] `StartRequest` uses lowercase JSON tags.
- [x] Go decoding remains compatible with old capitalized field names.
- [x] Invalid mode maps to HTTP 400.
- [x] Other start failures keep their current HTTP mapping.
- [x] `drove up` adds `--oneshot`.
- [x] `drove up` and `drove ps` display mode.
- [x] TypeScript uses the same mode literals and JSON fields.
- [x] The Web UI does not add a mode control in this phase.

## Verification

- [x] Agent tests cover valid values, defaults, restoration, and state transitions.
- [x] Adapter tests cover every vendor and mode combination.
- [x] Session tests cover request defaulting and custom command preservation.
- [x] Recovery tests cover old, new, missing, and corrupt metadata.
- [x] Process tests cover successful, failed, requested, and shutdown exits.
- [x] Race tests cover concurrent Stop and natural exit.
- [x] API tests cover HTTP 400 for invalid mode.
- [x] CLI tests cover the flag and request construction.
- [x] Web type checking and build are required.
- [x] Full race tests, vet, Go builds, and diff checks are required.
- [x] Manual verification covers interactive launch, oneshot completion, and restart.
- [x] Each implementation commit must be pushed after verification.

## Approval gate

- [x] The owner approves the scope and design before implementation starts.
