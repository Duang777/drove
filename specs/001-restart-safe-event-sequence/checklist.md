# Spec review checklist

## Scope

- [x] The spec addresses one reproduced failure.
- [x] Session projection recovery is out of scope.
- [x] Process reconnection is out of scope.
- [x] API and database schema changes are out of scope.
- [x] The expected implementation is limited to five files.

## Behavior

- [x] Empty-database behavior is defined.
- [x] Existing-database behavior is defined.
- [x] Sequence ordering is defined.
- [x] Startup failure behavior is defined.
- [x] Backward compatibility is defined.

## Design

- [x] The public Go signature change is explicit.
- [x] The daemon startup order is explicit.
- [x] The single-writer assumption is explicit.
- [x] The rejected multi-writer design is outside the current scope.
- [x] Preset sequence behavior is intentionally unchanged.

## Verification

- [x] Unit tests cover zero and nonzero initial sequences.
- [x] A daemon-level test proves store-to-Hub initialization.
- [x] The manual restart regression is specified.
- [x] Full race tests, vet, and builds are required.
- [x] Rollback does not require a database migration.

## Approval gate

- [ ] The owner approves the scope and design before implementation starts.
