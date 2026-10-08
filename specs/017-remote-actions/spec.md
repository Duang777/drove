# Audited remote actions

Status: Approved for implementation

Related issue: [#28](https://github.com/Duang777/drove/issues/28)

## Problem

A Blocked notification can wake the operator, but it cannot resolve the wait.
The operator still has to return to the original terminal.

A remote action is safe only when all of these facts still hold at the PTY
write:

1. the Agent is attached and still Blocked;
2. the request names the exact committed `state_changed -> blocked` sequence;
3. the current terminal screen still matches the adapter-owned approval rule;
4. no local input or other remote action has started a response;
5. the vendor-specific key sequence is written without another PTY writer
   interleaving;
6. the completed write produces adjacent redacted action and input events.

The notification projection cannot authorize the write. It follows the
append-only event stream asynchronously and may be behind the session state.

## Goal

Add authenticated, one-time approve, deny, and reply actions for Claude and
Codex approval prompts:

- Web Push may expose action buttons.
- Browsers without notification actions open an approval page.
- Approval always requires confirmation in the PWA for the MVP.
- A stale, replayed, expired, already answered, or non-approval request writes
  no bytes.
- A successful response appends `agent.action` and `agent.input` in one batch.
- No ticket, reply text, terminal text, prompt, token, or provider credential
  enters an event or log.
- No automatic approval path exists.

## Non-goals

This specification does not:

- add rule-based or unattended approval;
- interpret arbitrary Blocked states as approval prompts;
- persist approval screenshots;
- put action tickets in URLs, local storage, session storage, events, or logs;
- make a PTY write and two SQLite databases one transaction;
- support remote actions when no active Web Push device exists;
- promise that unsupported browser notification buttons are displayed.

## Operator flow

```text
Agent enters Blocked
  -> notify planner creates a durable delivery
  -> Web Push worker confirms the live approval prompt
  -> worker issues per-device, per-action tickets
  -> service worker shows Approve / Deny / Reply where supported
  -> direct Deny posts once with cookie + Origin + ticket
  -> Approve, Reply, empty clicks, and failures open the PWA approval page
  -> page requests fresh tickets for the same Blocked sequence
  -> user confirms or writes a bounded reply
  -> session rechecks state, sequence, response fence, and current screen
  -> adapter maps the action to bytes
  -> real PTY receives the bytes
  -> Committer appends agent.action + agent.input
  -> later screen/hook evidence moves the Agent out of Blocked
```

The UI never changes Agent state after an action response. It waits for the
authoritative `state_changed` event or the normal REST refresh.

## Domain model

`internal/agent` owns the vendor-neutral action vocabulary:

```go
type ActionKind string

const (
	ActionApprove ActionKind = "approve"
	ActionDeny    ActionKind = "deny"
	ActionReply   ActionKind = "reply"
)
```

`internal/session` owns execution:

```go
type StateSeq uint64

type ActionRequest struct {
	ExpectedStateSeq StateSeq
	Kind             agent.ActionKind
	Reply            string
	Channel          string
	DeviceID         string
}

type ActionResult struct {
	StateSeq     StateSeq
	BytesWritten int
	ActionSeq    uint64
	InputSeq     uint64
}

type ActionContext struct {
	StateSeq StateSeq
	Actions  []agent.ActionKind
	Screen   *ExplainScreen
}

func (m *Manager) ActionContext(
	context.Context,
	agent.ID,
	StateSeq,
) (ActionContext, error)

func (m *Manager) Respond(
	context.Context,
	agent.ID,
	ActionRequest,
) (ActionResult, error)
```

`Status` exposes `state_seq` as a JSON decimal string. `managedAgent` stores
the current state event sequence as an in-memory projection. The Committer is
its only runtime writer; recovery restores it from accepted `state_changed`
rows.

## Adapter action plans

The approval matcher and its input mapping live in the same private
`screenRuleDefinition`. No other package branches on a vendor name.

```go
type ApprovalActionPlan struct {
	kind  agent.ActionKind
	rule  string
	input []byte
}

func (c *ScreenClassifier) AvailableApprovalActions(
	term.Snapshot,
) []agent.ActionKind

func (c *ScreenClassifier) PlanApprovalAction(
	term.Snapshot,
	agent.ActionKind,
	string,
) (ApprovalActionPlan, error)
```

Initial mappings are pinned to the existing redacted fixtures:

| Rule | Approve | Deny | Reply |
| --- | --- | --- | --- |
| `claude.approval_prompt` | `1` + Enter | Esc | `2` + Enter + text + Enter |
| `codex.approval_prompt` | Enter | Esc | Esc + text + Enter |

Reply text must be valid UTF-8, 1 to 4096 bytes after trimming, and contain no
C0/C1 control characters, CR, LF, NUL, or Esc. Approve and deny reject a reply.
The adapter returns copied bytes and never retains the reply.

## Per-session ordering

Each `runningSession` owns one control gate and one response fence. The gate
replaces the existing input mutex and covers:

- REST and WebSocket input;
- writable attachment input;
- terminal query replies;
- Detector decision commits;
- stop and exit ownership;
- remote action validation, PTY write, and audit.

The response fence records the first local or remote response that writes at
least one byte for a state sequence. A different `state_seq` makes the old
fence irrelevant. A zero-byte backpressure result releases a provisional
remote claim. A partial or complete remote write keeps the claim.

Remote action ordering is:

```text
recording actor barrier
  -> terminal actor current snapshot
  -> adapter approval match and action plan
  -> TryLock per-session control gate
  -> attached / exit / Blocked / state_seq / response-fence checks
  -> one PTY Write
  -> one Committer batch [agent.action, agent.input]
  -> release control gate
```

The recording actor barrier ensures all earlier admitted PTY output has been
persisted and fed into the terminal model. The terminal actor checks the
current controller snapshot and calls a fixed session callback while no later
terminal feed can run. The callback may acquire the control gate. Code holding
that gate must never call the recording or terminal actor.

The observation actor acquires the same gate around `Decide` and
`CommitDecision`. Therefore either the state transition commits first and the
action sees a stale sequence, or the action writes and audits before the state
transition can commit.

The global Committer never performs PTY I/O. Different Agents can write their
PTYs concurrently.

## Event contract

Add `agent.action` with a versioned, redacted payload:

```go
type AgentActionPayloadV1 struct {
	Version    int    `json:"version"`
	Action     string `json:"action"`
	Channel    string `json:"channel"`
	DeviceID   string `json:"device_id"`
	BlockedSeq string `json:"blocked_seq"`
	ReplyBytes int    `json:"reply_bytes"`
	PromptRule string `json:"prompt_rule"`
}
```

The successful batch is always:

```text
seq N     agent.action
seq N + 1 agent.input
```

Both rows share one commit timestamp. The action payload never contains the
ticket, ticket identifier, reply, terminal contents, endpoint, or credential.
Recovery validates the action payload, the referenced Blocked transition, the
one-action-per-occurrence rule, and the adjacent input event. It does not
change Agent state.

## Ticket security

`internal/notify` owns a separate action-ticket key and mutable replay state.
The key is a random 256-bit value stored in
`<data_dir>/notify/action-ticket.key` as a regular `0600` file. It is not the
control token or VAPID private key.

A ticket is:

```text
v1.<base64url canonical JSON claims>.<base64url HMAC-SHA256>
```

Claims bind:

- a random JTI;
- Agent ID;
- decimal `blocked_seq`;
- one action;
- one active push subscription ID;
- issue and expiry times.

The default lifetime is ten minutes. `notify.db` schema version 2 adds issued
ticket rows containing only a SHA-256 JTI digest and bounded metadata.
Consumption verifies the HMAC and all bindings, checks that the device remains
active, then atomically marks one row consumed. The transaction commits before
session execution. A consumed ticket is never restored after a later PTY
failure.

Push retries issue fresh tickets immediately before each provider attempt.
The session response fence prevents distinct valid tickets for the same
Blocked occurrence from causing multiple writes.

## Cross-domain service

`internal/respond` owns the order spanning ticket state and session execution.
It has one deep API for each user intent:

```go
func (s *Service) Context(
	context.Context,
	agent.ID,
	session.StateSeq,
	string,
) (Context, error)

func (s *Service) Execute(
	context.Context,
	agent.ID,
	string,
	string,
) (Result, error)
```

`Context` asks session whether the exact Blocked occurrence is currently
actionable, then asks notify to issue tickets for that active device.
`Execute` consumes and binds the ticket before calling `Manager.Respond`.

This ordering accepts a consumed-but-not-executed crash window. It prevents a
network retry from replaying an uncertain PTY write. The page can request a
fresh ticket only if session still reports the same actionable Blocked state.

## API

Both routes stay under the existing authenticated control mux:

```text
POST /api/v1/agents/{id}/action-context
POST /api/v1/agents/{id}/actions
```

The context request includes a decimal string `blocked_seq` and a canonical
active `device_id`. The response includes the same sequence, available actions,
fresh action-bound tickets, expiry, and the live bounded screen view.

The action request includes one ticket and an optional reply. Agent ID, action,
Blocked sequence, channel, and device come from verified claims, not duplicate
request fields.

Cookie writes retain exact Origin checks. Bearer and cookie authentication,
Host validation, body limits, UTF-8 checks, unknown-field rejection, and the
existing JSON error envelope remain unchanged.

Stable status mapping:

| Condition | HTTP |
| --- | ---: |
| invalid body or reply | 400 |
| missing authentication | 401 |
| invalid Origin or ticket | 403 |
| unknown Agent | 404 |
| expired ticket or detached Agent | 410 |
| stale sequence, used ticket, prompt absent, or already answered | 409 |
| unsupported action | 422 |
| gate or PTY backpressure | 503 |
| partial write or post-write audit failure | 500 |

Errors never include the ticket, reply, device endpoint, terminal text, or
vendor input bytes.

## Web Push and PWA

Web Push payloads remain metadata-only except for short action tickets. Screen
rows are not sent through the push provider.

The service worker:

- shows only actions present in the versioned payload;
- never approves directly;
- may POST a denial once with `credentials: "same-origin"`;
- opens the approval page for approve, reply, an empty action, an unknown
  action, or any direct-denial failure;
- puts Agent ID and decimal Blocked sequence in the query string;
- never puts a ticket in a URL or browser storage.

The approval page gets the current device ID from the existing subscription
state, requests fresh tickets, and displays the server-provided bounded live
screen view. It requires confirmation for approve and a bounded text area for
reply. It clears reply and ticket state after submission.

The page is an inline section in the existing agent detail route. It uses the
current light utility-console tokens, global CSS, 4/6/8 px radius scale, and
existing button styles. It has no new modal or decorative card. At 320 px,
actions stack and every target remains at least 40 px high.

The service worker and page never infer `Working` from an action response.
The existing WebSocket event projection and five-second REST refresh update the
desktop grid after a committed state change.

## Failure semantics

- Invalid request data is rejected before ticket consumption where possible.
- Ticket consumption is durable before PTY execution.
- Zero-byte write failures produce no success event. The consumed ticket stays
  consumed.
- Any positive partial write closes the session response fence, produces no
  success event, and returns a do-not-retry error.
- A complete write followed by audit failure closes the fence, returns a
  do-not-retry error, and triggers the existing Committer fail-stop path.
- Client cancellation after the first written byte cannot cancel audit.
- Restart invalidates browser cookie sessions and destroys old PTYs. Recovered
  action events remain visible, but do not reopen a response fence.

## Synthesis decision

Candidate 2 is the base because it is the only proposal that explicitly
covers every current PTY writer and state transition with a per-session gate
while keeping the global Committer free of PTY I/O. Its terminal callback and
recording actor barrier preserve a current-screen check without introducing a
second emulator.

The final design takes Candidate 1's single cross-domain service so API callers
cannot reorder ticket consumption and session execution. It takes Candidate
3's rule that tickets never enter URLs; the approval page requests fresh
tickets after opening.

Candidate 1's asynchronous screen pump is rejected because it changes the
terminal-to-detector delivery semantics and still omitted the query-reply PTY
writer. Candidate 3's command actor and output/terminal lease graph is rejected
because it creates a larger migration and a cycle between command, output, and
observation unless earlier state evidence is reordered.

The cross-judge scored Candidate 2 at 27/30, Candidate 1 at 26/30, and
Candidate 3 at 19/30. The final design removes Candidate 2's API-level
two-step orchestration and transport tags from session types.

## Tradeoffs

- We accept changes across existing input paths in exchange for one explicit
  per-session PTY write order.
- We accept consumed tickets on stale or busy attempts in exchange for durable
  replay prevention.
- We accept a conservative one-response fence after any positive local input
  in the same Blocked occurrence.
- We accept version-sensitive adapter mappings in exchange for keeping vendor
  behavior isolated and fixture-tested.
- We accept a live-only text rendering of the approval screen in exchange for
  not persisting or pushing terminal contents.
- We accept that native browser action support varies and keep the PWA as the
  complete fallback.

## Verification

Tests must prove:

- valid Claude and Codex fixtures produce exact approve, deny, and reply bytes;
- idle, cleared, generic, and non-approval Blocked screens expose no actions;
- state-first and action-first races have deterministic outcomes;
- local input, query replies, attachment input, action, stop, and exit do not
  interleave unsafe PTY writes;
- two tickets for one Blocked sequence cause one PTY response;
- stale, expired, replayed, wrong-device, and tampered tickets write no bytes;
- action and input audits are adjacent, redacted, and recovery-valid;
- partial write and audit failure remain explicit do-not-retry outcomes;
- cookie, Origin, Host, and Bearer policies protect both routes;
- push payloads contain no screen or reply text;
- direct denial and every fallback path behave as specified;
- the approval page is keyboard accessible and has no horizontal overflow at
  320, 375, and 1280 px;
- `go test ./... -race -count=1`, `go vet ./...`, `make build`, workspace
  platform checks, Web typecheck, unit tests, build, and Playwright pass.

## Next implementation step

Add the action enum, `agent.action` event contract, state-sequence projection,
and recovery tests before any endpoint can produce an action.
