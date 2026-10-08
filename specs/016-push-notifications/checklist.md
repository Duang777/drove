# Durable push notifications checklist

Related issue: [#27](https://github.com/Duang777/drove/issues/27)

## Architecture

- [x] `drove.db` remains an append-only session event log.
- [x] Mutable notification state lives in a separate `notify.db`.
- [x] `internal/notify` owns policy, cursor, outbox, retries, and channels.
- [x] `internal/daemon` owns notification lifecycle and dependency injection.
- [x] `internal/api` depends only on narrow notification interfaces.
- [x] The Hub is used only as a wakeup; SQLite event ranges repair gaps.
- [x] The scan upper bound never exceeds `Hub.LastSeq()`.

## Persistence

- [x] A new `notify.db` is created as a regular `0600` file.
- [x] Notification migrations are versioned and idempotent.
- [x] First enablement starts at the current published event sequence.
- [x] Cursor advancement and outbox creation share one transaction.
- [x] Subscription endpoint, keys, device name, and creation time survive restart.
- [x] Revoking a subscription cancels its pending deliveries.
- [x] Delivery leases recover after an interrupted daemon.

## Policy

- [x] Entering Blocked creates an immediate notification.
- [x] Leaving Blocked makes pending notifications obsolete.
- [x] Repeated Blocked transitions within 30 seconds do not create visible spam.
- [x] A recent visible-browser heartbeat suppresses delivery when configured.
- [x] Normal daemon shutdown does not emit a Stopped notification.
- [x] The default payload contains no screen, output, prompt, input, or token.

## Channels

- [x] Web Push uses a stable topic and notification tag.
- [x] VAPID keys are generated atomically and stored in a regular `0600` file.
- [x] Web Push `404` and `410` revoke the affected subscription.
- [x] Web Push transient failures retry with a bounded backoff.
- [x] ntfy supports an HTTPS or loopback self-hosted endpoint.
- [x] An ntfy bearer token is read only from a regular `0600` token file.
- [x] ntfy failures never log the token or notification credentials.

## API and security

- [x] Notification routes use existing Bearer/cookie authentication.
- [x] Cookie writes require an exact configured Origin.
- [x] Push subscription JSON rejects unknown fields and invalid key material.
- [x] Device names and request bodies have explicit limits.
- [x] Public notification status never exposes endpoints or secrets.
- [x] Explicit HTTPS console origins can be used through a loopback reverse proxy.
- [x] The daemon listener remains loopback-only.

## PWA

- [x] The production build contains a manifest, service worker, and app icons.
- [x] Root PWA files are served by the embedded daemon.
- [x] The service worker and manifest do not use immutable caching.
- [x] The service worker never caches API, WebSocket, login, or control HTML.
- [x] A push click opens or focuses the matching Agent detail.
- [x] The settings panel shows support, permission, subscription, and ntfy state.
- [x] The current device can enable, test, and revoke Web Push.
- [x] Visible pages send expiring presence heartbeats.
- [x] The panel works at 320 px and is keyboard accessible.

## Documentation

- [x] `README.md` and `README.en.md` describe notification setup and limits.
- [x] `docs/remote-access.md` documents Tailscale Serve and SSH forwarding.
- [x] Package `AGENTS.md` files describe notification ownership and privacy.
- [x] The MVP roadmap marks #27 complete without marking #28 complete.

## Verification

- [x] `gofmt -w .`
- [x] `go test ./internal/notify ./internal/store ./internal/api ./internal/daemon -race -count=1`
- [ ] `go test ./... -race -count=1`
- [x] `go vet ./...`
- [x] `make build`
- [x] `scripts/check-workspace-platforms.sh`
- [x] `npm --prefix web run typecheck`
- [x] `npm --prefix web run test -- --run`
- [x] `npm --prefix web run build`
- [x] `npm --prefix web run test:e2e`
- [x] `git diff --check`
- [ ] Manual Web Push checks are recorded for available browsers and devices.

## Verification record

Run on 2026-10-08:

- The focused Go race suite, `go vet`, binary build, workspace cross-compilation,
  TypeScript check, 64 Vitest tests, and production Web build passed.
- `go test ./... -race -count=1` passed every package except
  `internal/workspace`, which reached Go's 10-minute package timeout after 605
  seconds while running its 239-test suite. The interrupted
  `TestAcknowledgeRejectsBrokenWorktreeAfterPrepareSourceSwap` passed alone
  under the race detector in 7.9 seconds. This branch does not change
  `internal/workspace`.
- Playwright passed all three system-Chrome scenarios: fleet handling, Web Push
  enable/test/revoke, and notification-click deep linking.
- The generated production assets contain the manifest, service worker, and 192
  px and 512 px PNG icons.
- The remote-login shell snippet passes `bash -n`. Tailscale is not installed in
  the test environment, so the documented Serve commands were not executed.
- Production-provider delivery remains unverified on Android Chrome, desktop
  Firefox, and an installed iOS PWA because those devices and credentials are
  not available in the local test environment.
