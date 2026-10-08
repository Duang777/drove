# Durable push notifications

Status: Approved for implementation

Related issue: [#27](https://github.com/Duang777/drove/issues/27)

## Problem

Drove can identify a blocked Agent and show it in the Web fleet, but the
operator must keep that page visible. The existing event Hub cannot be used as
a notification queue because it deliberately drops events for slow
subscribers and does not replay events after a daemon restart.

Push delivery also introduces secrets and third-party network calls. Those
operations must not block the session Committer, expose terminal contents by
default, or weaken the loopback control-plane boundary.

## Goal

Deliver a notification when a committed Agent state enters `blocked`:

1. Web Push works for an installed PWA.
2. ntfy works as an optional self-hosted channel.
3. subscriptions and pending deliveries survive daemon restarts.
4. a dropped Hub wakeup cannot lose a committed notification trigger.
5. the first notification is immediate and repeated Blocked flaps are
   suppressed for 30 seconds.
6. visible browser clients can suppress notifications when
   `quiet_when_active` is enabled.
7. default notification payloads contain no terminal screen, input, prompt,
   token, or vendor credential.

## Non-goals

This specification does not:

- approve, deny, or reply to an Agent;
- add an automatic approval path;
- include screenshots or terminal excerpts in a notification;
- expose the daemon on a non-loopback listener;
- guarantee exactly-once delivery after a push provider accepts a request;
- modify the append-only session event schema;
- persist an ntfy token in `config.json`.

Remote actions are tracked by
[#28](https://github.com/Duang777/drove/issues/28). Notification payloads
reserve a decimal `blocked_seq` and an Agent deep link for that later work.

## Operator flow

The browser flow is:

```text
open authenticated console
  -> open notification settings
  -> install or use the PWA
  -> grant browser notification permission
  -> create PushSubscription with the daemon VAPID public key
  -> register the subscription with Drove
  -> receive a Blocked notification
  -> click it to open /?agent=<agent-id>
```

The optional ntfy flow is configured before daemon startup:

```json
{
  "notify": {
    "on": ["blocked"],
    "debounce_seconds": 30,
    "quiet_when_active": true,
    "web_push": {
      "enabled": true,
      "vapid_subject": "mailto:operator@example.com"
    },
    "ntfy": {
      "enabled": true,
      "base_url": "https://ntfy.example.com",
      "topic": "drove",
      "token_file": "/home/operator/.drove/ntfy.token"
    }
  }
}
```

The token file is optional and must be a regular `0600` file. The public
configuration file never contains the token or a VAPID private key.

## Architecture

```text
session Committer
  -> drove.db append
  -> Agent projection
  -> event Hub publish
       |
       +-> wake only

drove.db state_changed rows
  -> notify planner
  -> notify.db cursor + outbox
  -> Web Push / ntfy workers

authenticated Web console
  -> push subscription API
  -> notify.db
```

`internal/notify` is one deep module. It owns policy, event scanning, the
notification projection, durable outbox, delivery leases, debounce, active
browser presence, VAPID credentials, and channel error classification.

`internal/daemon` owns the notification service lifecycle. `internal/api`
depends on a narrow notification interface and contains only HTTP parsing,
authentication-bound routing, limits, and status mapping.

### Published event feed

The Hub is a wakeup mechanism, not the source of truth. The planner:

1. reads `Hub.LastSeq()` as the highest event that completed publication;
2. reads bounded event ranges from `drove.db` after its durable cursor and no
   later than that published sequence;
3. projects state events and creates outbox rows in one `notify.db`
   transaction;
4. advances its cursor in the same transaction;
5. also polls once per second so a dropped Hub wakeup cannot stall delivery.

The first time notification storage opens, its cursor starts at the current
published sequence. Existing history does not generate a notification storm.
Later restarts resume the stored cursor and any pending delivery leases.

The notification database is separate from `drove.db`. Session events remain
append-only, while notification subscriptions, leases, revocations, retries,
and presence are mutable delivery state.

### Notification storage

`<data_dir>/notify.db` is a regular `0600` SQLite file using WAL,
`foreign_keys=ON`, and `secure_delete=ON`.

Version 1 contains:

```text
notify_cursor(singleton, through_seq, initialized_at)

push_subscriptions(
  id, endpoint, p256dh, auth, device_name, created_at, revoked_at
)

notify_agents(
  agent_id, state, state_seq, blocked_seq, last_notified_at
)

notify_deliveries(
  id, source_seq, agent_id, channel, target_id, payload,
  state, attempts, not_before, lease_until, last_error_code,
  created_at, delivered_at
)
```

The schema has a unique key on `(source_seq, channel, target_id)` and an index
on `(state, not_before)`. A subscription receives only events at or after its
creation time. Revocation cancels its pending deliveries.

Delivery is at least once. If the provider accepts a request and the daemon
exits before recording success, a retry can produce one duplicate. Stable Web
Push `Topic` and browser notification `tag` values collapse that duplicate.
The implementation does not claim exactly-once external delivery.

### Notification model

The core payload is an allowlist:

```go
type Notification struct {
    ID         string
    AgentID    string
    AgentName  string
    Vendor     string
    State      string
    BlockedSeq uint64
    OccurredAt time.Time
    DeepLink   string
}
```

It has no screen, output, input, prompt, free-form event payload, endpoint key,
or long-lived credential field.

Entering Blocked creates immediate deliveries. A later transition out of
Blocked marks unsent deliveries obsolete. A second Blocked transition for the
same Agent within the configured window is suppressed. When
`quiet_when_active` is enabled, a recent visible-page heartbeat suppresses the
delivery.

### Channels

The internal channel interface is:

```go
type Channel interface {
    Kind() ChannelKind
    Send(context.Context, Delivery) (SendResult, error)
}
```

Web Push uses `github.com/SherClockHolmes/webpush-go` pinned at `v1.4.0`.
The project is not archived, the release includes the current JWT security
update, and the dependency allows an injected HTTP client for tests.

The daemon creates `<data_dir>/notify/vapid.json` atomically when Web Push is
enabled. The directory is `0700`; the file is a regular `0600` file. Existing
files with a different type or mode are rejected rather than modified.

Web Push responses `404` and `410` revoke that subscription. Timeouts, `429`,
and `5xx` responses use bounded exponential retry. Other permanent responses
dead-letter the delivery.

ntfy posts plain UTF-8 text to one configured topic. Its bearer token is read
from a `0600` token file at startup. The UI states that notification metadata
passes through the configured ntfy server.

### API

All routes stay behind the existing Bearer or HttpOnly cookie middleware:

```text
GET    /api/v1/notifications
GET    /api/v1/push/subscriptions
POST   /api/v1/push/subscriptions
DELETE /api/v1/push/subscriptions/{id}
POST   /api/v1/notifications/presence
POST   /api/v1/notifications/test
```

The API validates subscription URLs, Base64URL keys, device names, body
limits, content type, and unknown fields. Cookie-authenticated writes retain
the existing exact Origin requirement.

`GET /api/v1/notifications` returns channel availability, the VAPID public key,
policy, and active device count. It never returns endpoints, subscription keys,
private keys, or ntfy credentials.

Configured HTTPS console origins are accepted for a loopback reverse proxy
such as Tailscale Serve. Their exact hosts join the Host allowlist. HTTP
origins remain restricted to loopback. The daemon listener itself remains
loopback-only.

### PWA

Vite copies these files into the embedded production build:

- `manifest.webmanifest`;
- `service-worker.js`;
- local Drove application icons.

The API static handler explicitly serves those root files. The service worker
uses `Cache-Control: no-cache`; the manifest does not use immutable caching.
The service worker does not cache control HTML, `/api`, `/ws`, or login
responses.

The fleet header gets a notification icon button. It expands an inline
settings section that follows the existing console tokens and button styles.
The panel shows browser support, permission, current device state, install
guidance for iOS, and ntfy availability. It can enable, test, and revoke the
current browser subscription.

The visible page sends a bounded presence heartbeat. A push click focuses an
existing Drove window or opens `/?agent=<agent-id>`.

## Lifecycle

Startup order:

1. validate config and open control credentials;
2. open `drove.db` and recover session projections;
3. open `notify.db` and VAPID credentials;
4. initialize or resume the notification cursor;
5. start planner and delivery workers;
6. start the authenticated API;
7. run optional Agent auto-resume.

Shutdown order:

1. stop HTTP admission;
2. close sessions so final committed transitions are visible;
3. let the notification planner reach the published head;
4. stop notification workers and return leases;
5. close the Hub and both stores.

Notification storage or network failures never mutate Agent state. A failure
to open enabled notification storage or required credentials fails daemon
startup. Runtime channel failures retry or dead-letter without failing the
session Committer.

## Verification

Tests must prove:

- first enablement starts at the current published sequence;
- dropped Hub wakeups are repaired by the SQLite scan;
- cursor advancement and outbox creation are atomic and idempotent;
- pending leases recover after restart;
- the first Blocked event is immediate and a 30-second flap is suppressed;
- leaving Blocked cancels an unsent notification;
- visible-browser suppression uses an expiring heartbeat;
- subscriptions survive restart and revoked devices receive no new delivery;
- Web Push `404` and `410` revoke a device;
- ntfy retries transient failures without logging its token;
- VAPID and token files enforce type and `0600` mode;
- default payload JSON has no terminal, input, prompt, token, or raw event
  content;
- API authentication, Host, Origin, body limits, and strict JSON parsing still
  hold;
- manifest and service worker use the intended cache headers;
- Vitest covers boundary parsing and subscription state;
- Playwright covers service worker registration, enable/test/revoke, deep-link
  clicks, and 320 px and 375 px layouts;
- `go test ./... -race`, `go vet ./...`, Web typecheck/test/build, and the
  repository platform checks pass.

Current Android Chrome, desktop Chrome and Firefox, and installed iOS PWA
remain a manual device acceptance matrix because local Playwright cannot prove
delivery through each browser vendor's production push service.

## Synthesis decision

Candidate 2 is the base because its independent notification database keeps
mutable delivery state out of the append-only session event store. The final
design takes candidate 1's published Hub watermark, first-run cursor,
subscription creation cutoff, PWA build paths, static cache policy, and exact
HTTPS reverse-proxy Origin handling.

Candidate 2's proposed Committer clock change is rejected. `Hub.LastSeq()`
already advances only after successful publication and is sufficient as the
scan upper bound. Candidate 3's looser PWA and first-start behavior are also
rejected.

The architecture arena completed with three candidates. Candidate 1 completed
after its runner was stopped and was still included in the comparison.

## Tradeoffs

- The design accepts a second SQLite file in exchange for isolating mutable
  delivery state from the session event log.
- The design accepts one possible provider-level duplicate after an uncertain
  network outcome in exchange for restart recovery.
- The design suppresses old pending notifications after an Agent leaves
  Blocked, even if the operator never saw the original event.
- The first version sends metadata-only notifications. Approval previews stay
  in #28 and require explicit opt-in.

## Next implementation step

Implement the notification database, bounded committed-event reader, and
planner tests before adding a network channel or browser UI.
