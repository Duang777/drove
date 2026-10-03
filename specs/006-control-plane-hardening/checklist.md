# Spec review checklist

## Scope and ordering

- [x] All seven remaining issues have an explicit delivery boundary.
- [x] Ordered commit lands before concurrent hook signals.
- [x] Authentication lands before bidirectional WebSocket input.
- [x] Hook installation and remote API security remain out of scope.
- [x] No SQLite schema migration is required.

## Event consistency

- [x] One session-owned actor assigns every runtime event sequence.
- [x] Store append precedes projection mutation and Hub publication.
- [x] Store failure leaves sequence, projection, and subscribers unchanged.
- [x] Agent state plans are revision checked.
- [x] Hub accepts only already committed events.
- [x] Reader compatibility lands before `agent.signal` emission.

## Detection

- [x] Each attached session owns exactly one Detector actor.
- [x] Vendor payload types remain private to `internal/adapter`.
- [x] Hook signals are authoritative and heuristics are fallback.
- [x] Confidence, deduplication, blocked recovery, and idle confirmation have
  one owner.
- [x] Raw output is retained while only the classification view is sanitized.
- [x] Per-session signal credentials are distinct from the control token.

## Security

- [x] A 256-bit token is generated atomically with mode `0600`.
- [x] All REST and WebSocket control surfaces require bearer authentication.
- [x] WebSocket Origin is checked exactly.
- [x] Non-loopback binds are rejected.
- [x] The Vite proxy injects the local credential without exposing a token UI.

## Input protocol

- [x] Existing outbound event frames remain compatible.
- [x] Input, ack, and error messages are versioned and request correlated.
- [x] One connection writer owns every outbound WebSocket frame.
- [x] REST and WebSocket input share limits and domain errors.
- [x] Duplicate request IDs are rejected per connection.

## User-visible fixes

- [x] Empty config paths resolve the generated default file.
- [x] CLI daemon auto-start forwards the exact resolved config path.
- [x] Missing default config remains valid.
- [x] Log timestamps use local time.
- [x] Output events end with exactly one newline.

## Verification and delivery

- [x] Focused race tests are required for every concurrency boundary.
- [x] Authentication, Origin, WebSocket, hook, and restart E2E checks are
  required.
- [x] Full race, vet, Go build, Web typecheck, and Web build checks are required.
- [x] Each completed boundary is committed and pushed immediately.
- [x] Issues are closed only after merged behavior is verified.

## Approval

- [x] The owner directed completion of the open issue queue on 2026-10-03.
