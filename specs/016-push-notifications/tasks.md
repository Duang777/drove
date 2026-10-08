# Durable push notifications implementation tasks

Complete each commit in order. Every product-code commit ends with focused
tests, the common gate, diff inspection, commit, push, and remote verification.

## Common gate

```bash
gofmt -w .
go test ./... -race -count=1
go vet ./...
make build
npm --prefix web run typecheck
npm --prefix web run test -- --run
npm --prefix web run build
git diff --check
```

## Spec commit

Add the approved specification, checklist, and implementation tasks.

Commit:

```text
docs: specify durable push notifications
```

## Commit 1: durable notification core

Add:

- a bounded global event reader that never hydrates output attachments;
- an independent `0600` notification database;
- versioned notification migrations;
- persistent subscriptions, cursor, Agent projection, outbox, and leases;
- pure Blocked policy, debounce, obsolete delivery, and presence logic;
- the notification service actor using Hub as a wakeup only;
- restart, dropped-wakeup, crash-boundary, and privacy tests.

Commit:

```text
feat: add durable notification pipeline
```

## Commit 2: delivery channels

Add:

- atomically generated `0600` VAPID credentials;
- Web Push delivery with stable collapse identifiers;
- subscription revocation for `404` and `410`;
- bounded retry classification for transient failures;
- ntfy delivery with credentials loaded from a `0600` token file;
- fake-provider tests and sanitized structured logs.

Commit:

```text
feat: deliver blocked agent notifications
```

## Commit 3: API and PWA

Add:

- authenticated notification status, subscription, presence, and test routes;
- strict request parsing, size limits, and semantic errors;
- explicit HTTPS reverse-proxy Origin and Host support;
- the PWA manifest, service worker, and application icons;
- the inline notification settings panel;
- enable, test, revoke, heartbeat, and deep-link behavior;
- Vitest and Playwright coverage, including 320 px and 375 px layouts.

Commit:

```text
feat: add notification pwa controls
```

## Commit 4: documentation and acceptance

Update:

- English and Chinese README feature tables and configuration;
- remote access documentation for Tailscale Serve and SSH forwarding;
- package ownership documentation;
- the checklist with measured results.

Run the common gate, workspace platform compilation, and available manual
browser checks. Record device checks that cannot run locally as explicit
remaining acceptance items.

Commit:

```text
docs: document push notification setup
```
