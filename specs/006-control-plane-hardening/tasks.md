# Implementation tasks

## Commit 1: specification

- [x] Add the approved architecture, review checklist, and delivery plan.
- [x] Run `git diff --check`.
- [x] Commit and push as `docs: specify control-plane hardening`.

## Commit 2: config and log fixes

- [x] Add config tests for missing default, present default, explicit path, and
  environment precedence.
- [x] Resolve and return the effective config path.
- [x] Pass that exact path to an auto-started daemon.
- [x] Add log formatting tests for local time and line endings.
- [x] Fix output formatting without changing stored UTC timestamps.
- [x] Run focused race tests, commit, push, and comment on #7 and #8.

## Commit 3: ordered event commit

- [x] Add projection-neutral `agent.signal` reader compatibility first.
- [x] Add failure and concurrency tests for durable event ordering.
- [x] Make Hub committed-event fan-out only.
- [x] Add revisioned Agent transition plans.
- [x] Add the session-owned Committer actor.
- [x] Migrate lifecycle, output, input, error, and recovery reconciliation.
- [x] Add daemon fail-stop propagation for runtime Store failure.
- [x] Run focused and full race tests, commit, and push.

## Commit 4: local authentication

- [x] Add `internal/auth` and its package rules.
- [x] Generate and atomically persist a stable 256-bit token with mode `0600`.
- [x] Reject non-loopback API binds.
- [x] Require bearer auth for REST and WebSocket.
- [x] Enforce exact local WebSocket Origin policy.
- [x] Teach the Go client and CLI to read and send the token.
- [x] Inject the token in the Vite development proxy.
- [x] Run security-focused tests and Web build, commit, push, and comment on #9.

## Commit 5: WebSocket input

- [ ] Define versioned input, ack, and error messages.
- [ ] Refactor each connection to one reader and one writer.
- [ ] Preserve the current outbound event envelope.
- [ ] Validate request IDs, Agent IDs, UTF-8, and size.
- [ ] Map session failures to stable protocol error codes.
- [ ] Add race and end-to-end WebSocket tests.
- [ ] Commit, push, and comment on #4.

## Commit 6: terminal sanitization

- [ ] Add tests for SGR, CSI cursor/erase, OSC, and single-character escapes.
- [ ] Sanitize only the adapter classification view.
- [ ] Prove persisted and streamed output stays byte-for-byte compatible.
- [ ] Commit, push, and comment on #5.

## Commit 7: Detector and hook relay

- [ ] Add `internal/detect` and its package rules.
- [ ] Add normalized hook signal types and private Claude/Codex decoders.
- [ ] Generate isolated per-session signal credentials.
- [ ] Inject hook relay environment into supported sessions.
- [ ] Add the authenticated signal endpoint.
- [ ] Add `drove hook --vendor` stdin relay.
- [ ] Implement hook authority, fallback heuristics, confidence filtering,
  deduplication, blocked recovery, and idle confirmation.
- [ ] Add focused race tests and hook protocol fixtures.
- [ ] Commit, push, and comment on #2 and #3.

## Final verification and delivery

- [ ] Run `gofmt` and `git diff --check`.
- [ ] Run `go test ./... -race -count=1`.
- [ ] Run `go vet ./...` and `make build`.
- [ ] Run Web typecheck and build.
- [ ] Run isolated daemon/CLI `/bin/cat`, bearer, Origin, WebSocket input, hook,
  replay, and restart-sequence regressions.
- [ ] Review the final diff for scope and project-rule compliance.
- [ ] Open the PR, wait for CI, fix failures, and merge.
- [ ] Update and close #2, #3, #4, #5, #7, #8, and #9 with evidence.
