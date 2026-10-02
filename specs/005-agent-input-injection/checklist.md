# Spec review checklist

## Scope

- [x] The phase delivers REST and CLI input as one complete vertical slice.
- [x] Bidirectional WebSocket input remains a separately specified follow-up.
- [x] Hook installation, Detector logic, resize, and attach remain out of scope.
- [x] Input does not change Agent state.
- [x] No database migration or new dependency is required.

## Input behavior

- [x] REST input is non-empty UTF-8 text.
- [x] The decoded input limit is 65536 bytes.
- [x] Whitespace and newline-only input are valid.
- [x] REST preserves the decoded data exactly.
- [x] Positional CLI input appends one newline.
- [x] `--stdin` preserves the input bytes.
- [x] Positional text and `--stdin` are mutually exclusive.
- [x] Input operations are not retried automatically.

## Concurrency

- [x] PTY writes complete fully or report the accepted prefix.
- [x] Concurrent writes to one PTY cannot interleave.
- [x] Input and process exit share one per-session ordering lock.
- [x] PTY output does not take the input lock.
- [x] The design does not introduce a second state writer.
- [x] Existing Stop and daemon-shutdown arbitration remains intact.

## Events and recovery

- [x] Successful input appends `agent.input`.
- [x] The audit payload stores only its version and byte count.
- [x] Input content and hashes are not persisted.
- [x] The projector treats the event as projection-neutral.
- [x] Unknown event types remain fatal to recovery.
- [x] Reader compatibility lands before event emission.
- [x] The rollback floor is explicit.

## API and client

- [x] The endpoint path and request body are explicit.
- [x] The endpoint returns 204 only after delivery and durable audit.
- [x] Body limits, strict JSON decoding, and content type are defined.
- [x] Unknown, detached, closed, invalid, oversized, write, and audit failures
  have defined statuses.
- [x] The client supports a bodyless success response.
- [x] Agent IDs are URL-escaped by the client.

## Verification

- [x] PTY, event, session, recovery, API, client, and CLI tests are specified.
- [x] A real interactive `/bin/cat` regression is required.
- [x] Full race, vet, Go build, Web typecheck, and Web build checks are required.
- [x] Each independently useful boundary is committed and pushed immediately.

## Approval

- [x] The owner directed implementation to continue from the open issues and
  RFC-001 on 2026-10-03.
