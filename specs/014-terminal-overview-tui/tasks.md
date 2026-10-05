# Terminal overview TUI implementation tasks

Complete the commits in order. For each product-code commit:

1. Implement only that commit's tasks.
2. Run its focused tests.
3. Run the common Go gate.
4. Inspect the full diff.
5. Commit with the listed message.
6. Push immediately.
7. Confirm that the remote branch contains the commit.

## Common Go gate

```bash
gofmt -w .
go test ./... -race -count=1
go vet ./...
make build
git diff --check
```

Run the Web gate after the final documentation commit:

```bash
npm --prefix web run test --if-present -- --run
npm --prefix web run typecheck
npm --prefix web run build
```

## Spec commit

Add the approved specification, checklist, and implementation tasks.

Verification:

```bash
git diff --check
git status --short --branch -uall
```

Commit and push:

```text
docs: specify terminal overview tui
```

## Commit 1: fleet model

### T1: Add the package boundary

Files:

- `internal/clitui/AGENTS.md`
- `internal/AGENTS.md`

Define `internal/clitui` as the owner of Bubble Tea state, fleet presentation,
snapshot lifecycle, local actions, and attach handoff. Record the forbidden
dependencies from the specification.

### T2: Add direct UI dependencies

Files:

- `go.mod`
- `go.sum`

Add:

```text
github.com/charmbracelet/bubbletea v1.3.10
github.com/charmbracelet/bubbles   v1.0.0
github.com/charmbracelet/lipgloss  v1.1.0
```

Do not add a `toolchain` directive.

### T3: Add pure fleet projection

Files:

- `internal/clitui/fleet.go`
- `internal/clitui/fleet_test.go`

Implement:

- copied display rows from `session.Status`;
- all eight filter values;
- stable Blocked-first ordering;
- Agent-ID selection with a fallback index;
- clamped navigation;
- transition source and event display fields.

Focused verification:

```bash
go test ./internal/clitui -race -run 'Test.*(Rows|Filter|Selection)' -count=20
```

Commit and push:

```text
feat: add terminal overview fleet model
```

## Commit 2: snapshot preview owner

### T4: Add one-owner preview lifecycle

Files:

- `internal/clitui/preview.go`
- `internal/clitui/preview_test.go`

Implement a private actor with:

- a capacity-one latest-target channel;
- a capacity-one latest-event channel;
- one joined generation worker;
- one WebSocket and one `Next` caller per generation;
- snapshot-only subscription fields;
- Agent ID and generation checks;
- cancelable reconnect waits capped at 2 seconds;
- idempotent close and completion wait.

Tests must use scripted streams and completion channels. Fail a test when two
`Next` calls overlap.

Focused verification:

```bash
go test ./internal/clitui -race -run 'TestPreview' -count=50
```

Commit and push:

```text
feat: stream selected terminal previews
```

## Commit 3: Bubble Tea interaction

### T5: Add polling and actions

Files:

- `internal/clitui/model.go`
- `internal/clitui/model_test.go`

Implement:

- immediate and 500 millisecond fleet refresh;
- one in-flight request and one trailing refresh;
- list and filter focus;
- send editor with exact newline behavior;
- stop confirmation with `y/n`;
- typed explain loading and viewport state;
- writable and read-only attach commands;
- Agent-ID retention after attach;
- stale action and preview result checks;
- quit without stop.

### T6: Add rendering

Files:

- `internal/clitui/view.go`
- `internal/clitui/view_test.go`

Render:

- the header and counts;
- the fleet table and selected marker;
- the selected snapshot or its loading/error/empty state;
- filter, send, stop, and explain focus states;
- current keyboard help;
- wide side-by-side and narrow stacked layouts.

Focused verification:

```bash
go test ./internal/clitui -race -run 'Test.*(Model|View|Action|Attach|Poll)' -count=20
```

Commit and push:

```text
feat: add terminal overview interactions
```

## Commit 4: runner, command, and acceptance

### T7: Add the runtime

Files:

- `internal/clitui/run.go`
- `internal/clitui/run_test.go`

Implement `Run`, production dependency wiring, Bubble Tea alternate-screen
startup, `tea.ExecCommand`, context cancellation, and preview actor cleanup.

### T8: Register `drove tui`

Files:

- `cmd/drove/main.go`
- `cmd/drove/main_test.go`
- `cmd/drove/AGENTS.md`

Add the no-argument command. Keep Cobra limited to client construction and
runner invocation.

### T9: Add acceptance coverage

Files:

- `internal/clitui/acceptance_test.go`

Prove:

- ten changed sessions render before a one-second deadline;
- writable and read-only attach preserve selection;
- repeated resize and selection replacement join all preview workers;
- quit calls no stop operation;
- repeated `tea.Exec` handoff restores a pseudo-terminal.

Focused verification:

```bash
go test ./internal/clitui ./cmd/drove -race -count=20
```

Commit and push:

```text
feat: add drove tui command
```

## Commit 5: user documentation

### T10: Document the command

Files:

- `README.md`
- `README.en.md`
- `docs/technical-notes.md`

Document:

- how to start the TUI;
- the key map;
- the 500 millisecond authoritative refresh;
- selected-only bounded snapshot behavior;
- Ctrl-Q attach return;
- quit without remote stop.

Focused verification:

```bash
rg -n 'drove tui|Ctrl-Q|500' README.md README.en.md docs/technical-notes.md
git diff --check
```

Commit and push:

```text
docs: document terminal overview tui
```

## Final verification and delivery

Run:

```bash
gofmt -w .
go test ./... -race -count=1
go vet ./...
make build
npm --prefix web run test --if-present -- --run
npm --prefix web run typecheck
npm --prefix web run build
git diff --check
git status --short --branch -uall
```

Then:

1. Run a real pseudo-terminal smoke test for list, filter, attach return, and
   quit.
2. Push the final branch.
3. Open a pull request that closes Issue #41.
4. Wait for every required CI job.
5. Add the verification evidence to Issue #41.
6. Merge the pull request.
7. Confirm that Issue #41 is closed and local `main` matches `origin/main`.
