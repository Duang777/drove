# Implementation tasks

Implementation starts only after the owner approves `spec.md`.

## T1: Lock the Hub sequence contract with tests

Files:

- `internal/event/event_test.go`

Work:

- Update existing tests to construct a Hub with initial sequence zero.
- Add coverage for a nonzero initial sequence.
- Assert consecutive allocation after initialization.

Verification:

```bash
go test ./internal/event -race
```

## T2: Make the initial sequence explicit

Files:

- `internal/event/event.go`
- Existing `NewHub` call sites

Work:

- Change `NewHub` to require `initialSeq uint64`.
- Initialize the atomic sequence field from that value.
- Pass zero from tests and non-daemon callers.

Verification:

```bash
go test ./internal/event -race
```

## T3: Restore the sequence during daemon startup

Files:

- `internal/daemon/daemon.go`
- `internal/daemon/daemon_test.go`

Work:

- Read `Store.LastSeq()` after opening the database.
- Return a wrapped startup error if the read fails.
- Construct the Hub from the persisted maximum.
- Keep the helper private to the daemon package.
- Test both an empty database and a database with a nonzero maximum.

Verification:

```bash
go test ./internal/daemon -race
```

## T4: Run the restart regression

Work:

- Build both binaries.
- Run the isolated two-start daemon scenario from `spec.md`.
- Confirm that replay contains events from the post-restart session.
- Confirm that the database sequence exceeds the pre-restart maximum.

Verification:

```bash
go test ./... -race
go vet ./...
make build
```

## T5: Update the technical note

Files:

- `docs/technical-notes.md`

Work:

- Mark the restart sequence collision as fixed.
- Preserve the separate open item for session projection recovery.
- Record the exact verification commands and results.

Verification:

```bash
git diff --check
```
