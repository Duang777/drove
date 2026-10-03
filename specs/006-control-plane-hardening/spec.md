# Control-plane hardening and state detection

Status: Approved for implementation

Owner direction: complete the remaining open issues in a dedicated worktree.

Related issues: #2, #3, #4, #5, #7, #8, #9.

## Problem

Drove can start, observe, stop, and send audited REST input to PTY-backed
agents, but the remaining control-plane behavior is not yet safe or complete:

- the generated default config file is not loaded consistently;
- replay output formatting loses line boundaries and displays UTC;
- event sequence allocation, persistence, projection mutation, and publication
  are split across concurrent callers;
- REST and WebSocket endpoints have no authentication;
- WebSocket clients cannot send input and receive correlated results;
- heuristic classification consumes ANSI-decorated terminal text;
- state detection ignores confidence and has no authoritative hook signal path.

These gaps interact. Hook delivery adds another concurrent state producer, and
input over WebSocket expands the unauthenticated control surface. Ordered event
commit and authentication must therefore land before the Detector.

## Goal

Complete the local control plane while preserving the project invariants:

1. every persisted event has one globally ordered sequence;
2. projections and subscribers observe only durably committed events;
3. all management and input surfaces require one generated local credential;
4. WebSocket input is versioned, bounded, correlated, and serialized;
5. vendor hook payloads terminate at the adapter boundary;
6. one Detector actor per live session is the only runtime state-signal merger;
7. raw output remains replayable while heuristics receive sanitized text.

## Non-goals

This phase does not:

- expose a remotely secure API or add TLS;
- install or edit Claude Code or Codex hook configuration automatically;
- restore PTYs after daemon restart;
- introduce a terminal screen emulator or full attach mode;
- add token entry UI;
- make detector thresholds user configurable;
- change the SQLite schema or persisted output representation.

## Architecture

```text
PTY output --------------------+
                               v
vendor hook -> adapter -> Detector actor -> transition plan
                                  |                |
                                  +------> session Committer
REST / WS input ------------------------------->  |
                                                  v
                                      Store -> Agent projection -> Hub
```

### Global Committer

`internal/session` owns one Committer goroutine. All runtime event writes pass
through it. For each request it:

1. reads the current committed sequence;
2. assigns consecutive sequence numbers to the request batch;
3. appends the full batch to the Store;
4. applies a prevalidated projection mutation;
5. publishes the committed events to the Hub in sequence order;
6. replies to the caller.

If append fails, the sequence, Agent projection, and Hub remain unchanged. A
runtime Store failure is fatal to the daemon because continuing could expose a
projection that cannot be recovered.

The Hub becomes committed-event fan-out only. It never allocates sequence
numbers. Event constructors continue accepting `seq == 0` as drafts before the
Committer assigns their final values.

### Agent state plans

`internal/agent` remains the state authority but separates validation from
mutation:

```go
type TransitionPlan struct {
    Revision uint64
    From     State
    To       State
    Reason   string
}

func (a *Agent) PlanTransition(to State, reason string) (TransitionPlan, error)
func (a *Agent) ApplyTransition(plan TransitionPlan, at time.Time) error
```

Plans are revision checked. A stale plan cannot overwrite a newer committed
state. The existing callback mutation path is removed after all callers use the
Committer.

### Detector actor

Each attached session owns one Detector goroutine and input channel. It accepts
normalized signals from two sources:

- hook signals, which are authoritative while healthy;
- sanitized terminal hints, which are fallback evidence.

The Detector owns confidence thresholds, blocked recovery, idle confirmation,
timer generation, and delivery deduplication. It proposes transitions through
the session Committer and never mutates an Agent directly.

Hook policy is the only exposed setting:

- `off`: heuristics only;
- `auto`: prefer valid hook signals and fall back to heuristics;
- `required`: reject session startup when hook signaling cannot be provisioned.

The default is `auto`.

### Adapter boundary

`internal/adapter` parses vendor JSON into a common signal:

```go
type Signal struct {
    DeliveryID string
    Kind       SignalKind
    SessionRef string
    Evidence   string
    Confidence float64
    At         time.Time
}
```

Claude and Codex payload structs remain private to the adapter package. Unknown
hook events are rejected without changing state. Raw hook JSON is not stored.

### Hook relay

`drove hook --vendor <claude|codex>` reads one JSON document from stdin and
forwards it to the loopback daemon signal endpoint. A live session receives:

- its Agent ID;
- a generated per-session signal token;
- the daemon signal endpoint;
- the selected vendor.

These values are injected through environment variables. The signal token is
distinct from the control-plane token and authorizes only that Agent's signal
endpoint. Hook installation remains a documented user action.

### Authentication

`internal/auth` owns a generated 256-bit control token. The token file:

- is created on first daemon startup;
- uses mode `0600`;
- is written atomically under the configured data directory;
- is read by the CLI and Vite development proxy;
- is compared in constant time.

All REST routes and the WebSocket upgrade require
`Authorization: Bearer <token>`. WebSocket also accepts only an absent Origin or
an exact configured local console Origin. Non-loopback API binds are rejected
until a remote transport contract exists.

Per-session signal tokens are generated separately, kept in memory, and
accepted only by `POST /api/v1/agents/{id}/signal`.

### WebSocket protocol

Existing outbound event objects remain unchanged for compatibility. Clients may
also send:

```json
{"version":1,"type":"input","request_id":"r1","agent_id":"...","data":"continue\n"}
```

The server responds on the same connection:

```json
{"version":1,"type":"ack","request_id":"r1","bytes":9}
{"version":1,"type":"error","request_id":"r1","code":"not_attached","message":"..."}
```

There is one reader goroutine and one writer goroutine per connection. The
writer owns all event, ack, error, ping, and close frames. Input limits and
session error semantics match the REST endpoint. Duplicate `request_id` values
on one connection are rejected.

### Output sanitization

Persisted and streamed output remains the current trimmed raw line, including
ANSI bytes. Before heuristic classification, a separate sanitizer removes
common CSI, OSC, and single-character escape sequences. Sanitization never
changes replay payloads.

### Config and log fixes

`config.Load("")` resolves the default `~/.drove/config.json`, tolerating a
missing file. Resolution returns the path used so CLI auto-start passes that
exact path to `droved --config`.

`drove log` converts timestamps with `Local()` and writes exactly one newline
per output event after trimming existing CR/LF suffixes.

## Compatibility and rollout

Reader support for `agent.signal` lands before runtime emission. Recovery treats
the event as projection-neutral after validating identity fields.

The existing `agent.input` envelope and outbound WebSocket event shape remain
compatible. Authentication is intentionally a local breaking change: old
clients receive `401` until upgraded to read the token.

No database migration is required.

## Verification

Focused tests must prove:

- default and explicit config paths reach both CLI and auto-started daemon;
- log output uses local time and one line per event;
- concurrent event producers produce durable, gap-free, publish-ordered
  sequences;
- failed Store appends do not mutate Agent state or reach subscribers;
- token creation is atomic, private, and stable across restarts;
- REST bearer and strict WebSocket Origin checks reject unauthorized callers;
- WebSocket input returns correlated ack/error frames without concurrent writes;
- ANSI cleaning improves classification without changing event payloads;
- hook decoding, session-token isolation, deduplication, confidence filtering,
  blocked recovery, and idle confirmation work under the race detector.

End-to-end verification uses an isolated data directory and `/bin/cat` to cover
CLI/REST/WebSocket input, replay, authentication, daemon restart, and continuous
event sequence allocation.

## Delivery boundaries

1. Land this specification.
2. Fix config loading and log formatting.
3. Add reader compatibility, Agent plans, and the global Committer.
4. Add local authentication and Origin checks.
5. Add versioned WebSocket input.
6. Sanitize heuristic terminal text.
7. Add hook adapters, relay, signal endpoint, and Detector.
8. Run full verification, open and merge the PR, then close all satisfied
   issues with evidence.

Each independently verifiable boundary is committed and pushed before the next
one begins.
