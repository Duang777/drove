# Audited REST and CLI agent input

Status: Approved for implementation

Owner direction: continue implementation from the open issues and RFC-001.

Related issue: [#4](https://github.com/Duang777/drove/issues/4)

## Problem

Interactive agents now remain attached to a PTY, but Drove exposes no supported
way to write to that PTY. `session.Manager.Write` exists without an API,
client, or CLI caller. It also ignores partial writes and records no audit
event.

Issue #4 also proposes bidirectional WebSocket input. The existing `/ws`
endpoint is a global outbound event stream. It has no target agent, request ID,
acknowledgement, input error frame, or single outbound writer for events and
command results. Adding inbound commands there would define a new protocol,
not merely expose the existing write method.

## Goal

Deliver the first complete input path:

```text
drove send
    -> internal/client
    -> POST /api/v1/agents/{id}/input
    -> session.Manager.SendInput
    -> PTY
```

Successful input must be written completely, audited without storing its
contents, and available through replay and the existing outbound WebSocket
event stream.

## Scope

This phase includes:

- `POST /api/v1/agents/{id}/input`;
- `drove send <agent-id> <text>`;
- `drove send <agent-id> --stdin`;
- complete, non-interleaved PTY writes;
- a redacted `agent.input` event;
- restart-safe projection handling for the new event;
- Go client support;
- TypeScript event-contract compatibility.

This phase intentionally leaves issue #4 open for bidirectional WebSocket
input.

## Non-goals

This phase does not:

- accept input frames on `/ws`;
- add terminal attach mode or resize APIs;
- change Agent state after input;
- install vendor hooks or add `internal/detect`;
- add authentication for the daemon;
- support binary input or automatic retries;
- change the SQLite schema;
- fix default config-file discovery.

## User contract

### REST

The endpoint accepts one JSON object:

```json
{"data":"continue\n"}
```

`data` is non-empty UTF-8 text with a maximum decoded size of 65536 bytes.
Whitespace and newline-only input are valid. REST does not add a newline.

A successful complete write and durable audit returns `204 No Content`.

### CLI

Positional input appends one newline:

```bash
drove send <agent-id> "continue with the failing test"
```

Standard-input mode preserves the bytes read from stdin:

```bash
printf 'continue\n' | drove send <agent-id> --stdin
```

The two forms are mutually exclusive. Empty, oversized, and invalid UTF-8
input fails before the CLI contacts the daemon.

## Domain design

`internal/session` owns the complete operation:

```go
const MaxInputBytes = 64 * 1024

type InputResult struct {
	BytesWritten int
}

func (m *Manager) SendInput(id agent.ID, data []byte) (InputResult, error)
```

The method:

1. validates size and UTF-8;
2. rejects a closed Manager;
3. resolves the Agent and its attached runtime session;
4. serializes input operations for that session;
5. rechecks attachment and exit ownership;
6. writes the complete payload to the PTY;
7. appends and publishes one redacted `agent.input` event;
8. returns only after the audit event is durable.

`Manager.Write` is replaced. Keeping both methods would expose two operations
with different guarantees.

Input does not call `Agent.Transition`. Phase 1's future Detector remains
responsible for `blocked` or `idle` to `working`.

## PTY write contract

`pty.Session.Write` keeps its current signature but strengthens its behavior:

```go
func (s *Session) Write(data []byte) (int, error)
```

It holds the existing PTY lock and loops until every byte is accepted or a
write fails. A zero-byte write without an error becomes `io.ErrShortWrite`.
Concurrent input calls cannot interleave.

## Concurrency and ordering

Each `runningSession` adds an input mutex.

`SendInput` holds it across PTY delivery and audit persistence. `onExit` takes
the same mutex before claiming the process exit and recording its terminal
state.

This creates two valid outcomes:

- input wins, so complete delivery and its audit precede the terminal state;
- exit wins, so the input call reports that the session is detached.

The PTY output callback does not take this mutex. A large echoed input can fill
the PTY while the write is still running; blocking the reader behind the writer
would deadlock. An echoed output event may therefore precede the post-write
input audit event.

## Event contract

Add:

```go
const TypeAgentInput Type = "agent.input"
```

The event has:

- `reason`: `accepted`;
- `session_id` and `agent_id`: the target Agent ID;
- `payload`: `{"version":1,"bytes":N}`.

The payload never stores the input text, an excerpt, or a digest. Prompts can
contain source code, credentials, and approval answers.

The recovery projector explicitly accepts `agent.input`, validates the session
and agent IDs, and applies no projection change. It does not parse the payload.
Unknown event types remain errors.

## Failure semantics

Session exposes sentinel errors for stable transport mapping:

| Condition | HTTP |
| --- | ---: |
| Malformed, trailing, unknown-field, empty, or invalid UTF-8 input | 400 |
| Decoded input over 65536 bytes or encoded body over its cap | 413 |
| Unknown Agent | 404 |
| Known Agent without an attached PTY | 409 |
| Manager shutdown in progress | 503 |
| PTY write failure or partial write | 500 |
| Complete delivery followed by audit persistence failure | 500 |
| Complete delivery and durable audit | 204 |

Partial delivery and delivered-but-unaudited errors include the byte count and
state that callers must not retry automatically. PTY I/O and SQLite cannot be
one transaction.

The endpoint uses the daemon's existing localhost trust model. This change does
not make non-loopback binding safe.

## API and client details

The API:

- registers `POST /api/v1/agents/{id}/input`;
- limits the encoded request body;
- requires `application/json`;
- rejects unknown fields and trailing JSON values;
- delegates all domain behavior to `Manager.SendInput`;
- keeps the existing JSON error envelope.

The client adds:

```go
func (c *Client) SendInput(ctx context.Context, id string, data []byte) error
```

It URL-escapes the Agent ID. The existing POST helper accepts a nil output for
successful responses without a body.

## Compatibility and rollout

No database migration is required.

The compatibility reader must land before any code emits `agent.input`.
After an input event is written, binaries older than that reader cannot
bootstrap the database because they reject unknown event types. The first
implementation commit is therefore the rollback floor.

The TypeScript `EventType` union adds `agent.input` before runtime emission.
No Web UI input control is added.

## Verification

Tests must cover:

- complete and partial PTY writes;
- zero-progress writes and write/close races;
- input validation and typed session errors;
- concurrent non-interleaved sends;
- send versus process-exit ordering;
- redacted audit content;
- projection neutrality across restart;
- API status mapping and strict body decoding;
- client handling of a `204` response;
- CLI positional and stdin semantics;
- an interactive `/bin/cat` API/CLI round trip;
- full race, vet, Go build, Web typecheck, and Web build checks.

## Delivery boundaries

1. Add reader and frontend compatibility for `agent.input`; do not emit it.
2. Strengthen PTY writes and add audited session input.
3. Expose the REST endpoint and Go client.
4. Add `drove send`, update docs, and run the real daemon regression.

Each boundary must pass focused tests, then be committed and pushed before the
next boundary starts.

## Follow-up

After this phase:

1. design bidirectional WebSocket input with target IDs, request IDs,
   acknowledgements, bounded reads, and one connection writer;
2. specify RFC-001 Phase 1 using current Claude and Codex hook contracts;
3. resolve ordered state persistence before introducing concurrent Detector
   signals.
