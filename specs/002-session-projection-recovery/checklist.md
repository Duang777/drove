# Spec review checklist

## Scope

- [x] The spec restores session projections without reconnecting old PTYs.
- [x] SQLite remains an append-only event log.
- [x] No mutable session snapshot table is added.
- [x] REST, WebSocket, CLI, and frontend schemas remain unchanged.
- [x] Runtime persistence failure redesign is explicitly out of scope.
- [x] The implementation file count and reason are explicit.

## Recovery semantics

- [x] Daemon startup ordering is explicit.
- [x] Every historical state without a current PTY resolves to `stopped`.
- [x] `done -> stopped` preserves completion history without claiming control.
- [x] Recovery reconciliation is append-only, atomic, and idempotent.
- [x] Recovery events are not broadcast as live WebSocket events.
- [x] Hub sequence initialization uses the committed recovery maximum.

## Compatibility

- [x] Existing schema version 1 databases require no migration.
- [x] Legacy name and vendor fallback values are explicit.
- [x] Sequence gaps are accepted.
- [x] Missing state prefixes and later chain gaps have bounded rules.
- [x] PID and LastError limitations are explicit.
- [x] Failed historical starts have defined recovery behavior.

## Ownership

- [x] Store owns ordered reads and atomic inserts only.
- [x] Session owns event projection and lifecycle payload semantics.
- [x] Agent remains the only state authority.
- [x] Event owns the envelope without importing session types.
- [x] Daemon remains the composition root.
- [x] No new cross-package dependency cycle is introduced.

## Failure handling

- [x] Projection-critical corruption blocks daemon readiness.
- [x] Errors include sequence and session context.
- [x] Failed reconciliation leaves no partial batch.
- [x] Listener creation happens only after recovery succeeds.
- [x] A bind failure after reconciliation does not rewrite history.
- [x] Multi-daemon writes remain unsupported and are detected at commit.

## Verification

- [x] Store transaction and scan tests are defined.
- [x] Agent restore tests are defined.
- [x] Pure projector tests cover every state and compatibility path.
- [x] Bootstrap idempotence tests are defined.
- [x] Session lifecycle and failed-start tests are defined.
- [x] A real three-start daemon regression is defined.
- [x] Race tests, vet, and builds are required.
- [x] Three independent commit and push boundaries are defined.

## Approval gate

- [ ] The owner approves the scope and design before implementation starts.
