# Durable push notifications checklist

Related issue: [#27](https://github.com/Duang777/drove/issues/27)

## Architecture

- [ ] `drove.db` remains an append-only session event log.
- [ ] Mutable notification state lives in a separate `notify.db`.
- [ ] `internal/notify` owns policy, cursor, outbox, retries, and channels.
- [ ] `internal/daemon` owns notification lifecycle and dependency injection.
- [ ] `internal/api` depends only on narrow notification interfaces.
- [ ] The Hub is used only as a wakeup; SQLite event ranges repair gaps.
- [ ] The scan upper bound never exceeds `Hub.LastSeq()`.

## Persistence

- [ ] A new `notify.db` is created as a regular `0600` file.
- [ ] Notification migrations are versioned and idempotent.
- [ ] First enablement starts at the current published event sequence.
- [ ] Cursor advancement and outbox creation share one transaction.
- [ ] Subscription endpoint, keys, device name, and creation time survive restart.
- [ ] Revoking a subscription cancels its pending deliveries.
- [ ] Delivery leases recover after an interrupted daemon.

## Policy

- [ ] Entering Blocked creates an immediate notification.
- [ ] Leaving Blocked makes pending notifications obsolete.
- [ ] Repeated Blocked transitions within 30 seconds do not create visible spam.
- [ ] A recent visible-browser heartbeat suppresses delivery when configured.
- [ ] Normal daemon shutdown does not emit a Stopped notification.
- [ ] The default payload contains no screen, output, prompt, input, or token.

## Channels

- [ ] Web Push uses a stable topic and notification tag.
- [ ] VAPID keys are generated atomically and stored in a regular `0600` file.
- [ ] Web Push `404` and `410` revoke the affected subscription.
- [ ] Web Push transient failures retry with a bounded backoff.
- [ ] ntfy supports an HTTPS or loopback self-hosted endpoint.
- [ ] An ntfy bearer token is read only from a regular `0600` token file.
- [ ] ntfy failures never log the token or notification credentials.

## API and security

- [ ] Notification routes use existing Bearer/cookie authentication.
- [ ] Cookie writes require an exact configured Origin.
- [ ] Push subscription JSON rejects unknown fields and invalid key material.
- [ ] Device names and request bodies have explicit limits.
- [ ] Public notification status never exposes endpoints or secrets.
- [ ] Explicit HTTPS console origins can be used through a loopback reverse proxy.
- [ ] The daemon listener remains loopback-only.

## PWA

- [ ] The production build contains a manifest, service worker, and app icons.
- [ ] Root PWA files are served by the embedded daemon.
- [ ] The service worker and manifest do not use immutable caching.
- [ ] The service worker never caches API, WebSocket, login, or control HTML.
- [ ] A push click opens or focuses the matching Agent detail.
- [ ] The settings panel shows support, permission, subscription, and ntfy state.
- [ ] The current device can enable, test, and revoke Web Push.
- [ ] Visible pages send expiring presence heartbeats.
- [ ] The panel works at 320 px and is keyboard accessible.

## Documentation

- [ ] `README.md` and `README.en.md` describe notification setup and limits.
- [ ] `docs/remote-access.md` documents Tailscale Serve and SSH forwarding.
- [ ] Package `AGENTS.md` files describe notification ownership and privacy.
- [ ] The MVP roadmap marks #27 complete without marking #28 complete.

## Verification

- [ ] `gofmt -w .`
- [ ] `go test ./internal/notify ./internal/store ./internal/api ./internal/daemon -race -count=1`
- [ ] `go test ./... -race -count=1`
- [ ] `go vet ./...`
- [ ] `make build`
- [ ] `scripts/check-workspace-platforms.sh`
- [ ] `npm --prefix web run typecheck`
- [ ] `npm --prefix web run test -- --run`
- [ ] `npm --prefix web run build`
- [ ] `npm --prefix web run test:e2e`
- [ ] `git diff --check`
- [ ] Manual Web Push checks are recorded for available browsers and devices.
