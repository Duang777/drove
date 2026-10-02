# Implementation tasks

## Commit 1: input event compatibility

### T1: Add the event type

Files:

- `internal/event/event.go`
- `internal/event/event_test.go`
- `internal/event/AGENTS.md`

Work:

- Add `TypeAgentInput` with wire value `agent.input`.
- Add a constructor that accepts an encoded redacted payload.
- Test the complete event envelope.

### T2: Make recovery understand the event

Files:

- `internal/session/projection.go`
- `internal/session/projection_test.go`

Work:

- Accept `agent.input` as projection-neutral.
- Validate non-empty and matching session and agent IDs.
- Do not create or update a session draft.
- Keep unknown event types fatal.

### T3: Update the TypeScript event contract

Files:

- `web/src/api/types.ts`
- `web/src/components/EventLog.tsx`

Work:

- Add `agent.input` to the event type union.
- Give the audit event an existing-console-compatible display style.
- Do not add an input control.

Verification:

```bash
go test ./internal/event ./internal/session -race
npm --prefix web run typecheck
npm --prefix web run build
git diff --check
```

Commit and push:

```text
feat: recognize agent input audit events
```

## Commit 2: complete PTY delivery and session audit

### T4: Strengthen PTY writes

Files:

- `internal/pty/pty.go`
- `internal/pty/pty_test.go`
- `internal/pty/AGENTS.md`

Work:

- Keep one PTY lock across a full write.
- Continue after short writes.
- Return `io.ErrShortWrite` after zero progress.
- Preserve the accepted byte count on failure.

### T5: Add the session operation

Files:

- `internal/session/session.go`
- `internal/session/session_test.go`
- `internal/session/AGENTS.md`

Work:

- Add `MaxInputBytes`, `InputResult`, and typed errors.
- Replace `Manager.Write` with `Manager.SendInput`.
- Add a narrow process interface for deterministic tests.
- Serialize input delivery and audit per attached session.
- Make process exit use the same ordering lock.
- Persist only `{"version":1,"bytes":N}` after a complete write.
- Do not change Agent state.

Verification:

```bash
go test ./internal/pty ./internal/session -race -count=20
go test ./... -race -count=1
git diff --check
```

Commit and push:

```text
feat: deliver and audit agent input
```

## Commit 3: REST and Go client

### T6: Add the REST endpoint

Files:

- `internal/api/server.go`
- `internal/api/server_test.go`
- `internal/api/AGENTS.md`

Work:

- Register `POST /api/v1/agents/{id}/input`.
- Require JSON and bound the encoded request body.
- Reject unknown fields and trailing values.
- Map session errors to the approved status codes.
- Return 204 only after a complete audited write.

### T7: Add client input support

Files:

- `internal/client/client.go`
- `internal/client/client_test.go`
- `internal/client/AGENTS.md`

Work:

- Add `Client.SendInput`.
- URL-escape the Agent ID.
- Let the JSON POST helper accept a nil output for 204 responses.
- Test request encoding, success, server errors, and invalid UTF-8.

Verification:

```bash
go test ./internal/api ./internal/client -race
go test ./... -race -count=1
git diff --check
```

Commit and push:

```text
feat: expose agent input API
```

## Commit 4: CLI and end-to-end verification

### T8: Add `drove send`

Files:

- `cmd/drove/main.go`
- `cmd/drove/main_test.go`
- `cmd/drove/AGENTS.md`
- `README.md`
- `docs/technical-notes.md`

Work:

- Register the command.
- Implement positional and `--stdin` input rules.
- Validate input before starting or contacting the daemon.
- Print the successful byte count.
- Document usage and the localhost security boundary.

### T9: Verify the completed slice

Run:

```bash
gofmt -w internal/event internal/pty internal/session internal/api internal/client cmd/drove
go test ./internal/event ./internal/pty ./internal/session ./internal/api ./internal/client ./cmd/drove -race -count=20
go test ./... -race -count=1
go vet ./...
make build
npm --prefix web run typecheck
npm --prefix web run build
git diff --check
```

Run an isolated daemon with an interactive `/bin/cat` session. Send input
through the REST API and `drove send`, then verify:

- both payloads are echoed;
- replay contains redacted `agent.input` events;
- no input content appears in those audit payloads;
- stopping the session leaves it detached and `stopped`;
- restarting the daemon restores the session without projection errors.

Commit and push:

```text
feat: add drove send
```

## Follow-up research

- Specify bidirectional WebSocket input as a versioned control protocol.
- Update RFC-001 Phase 1 from current Claude and Codex hook documentation.
- Specify ordered transition persistence before concurrent Detector signals.
