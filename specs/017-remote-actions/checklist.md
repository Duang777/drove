# Audited remote actions checklist

Related issue: [#28](https://github.com/Duang777/drove/issues/28)

## Architecture

- [x] `internal/agent` owns the vendor-neutral action enum.
- [x] `internal/adapter` is the only package with Claude/Codex key mappings.
- [x] `internal/session` owns state freshness, screen actionability, PTY writes,
      response fencing, and action/input audit.
- [ ] `internal/notify` owns ticket signing, expiry, device binding, and replay
      state, but cannot write a PTY.
- [ ] `internal/respond` owns ticket-consume-before-session-execute ordering.
- [ ] The API only parses, invokes the response service, and maps errors.
- [x] The global Committer never performs PTY I/O.

## State and concurrency

- [x] Every Agent exposes the sequence of its current committed state.
- [x] Recovery restores the last accepted state event sequence.
- [x] The per-session control gate covers every user or terminal PTY writer,
      Detector state commit, stop, and exit ownership.
- [x] Remote action validation and PTY write happen while the same gate is held.
- [x] The terminal actor checks the current approval screen immediately before
      entering the action callback.
- [x] Earlier admitted PTY output cannot be overtaken by an action.
- [x] A local response or another remote response fences the same Blocked
      occurrence after the first positive byte.
- [x] Different Agents can write independently.

## Action contract

- [x] Claude approve, deny, and reply bytes are fixture-tested.
- [x] Codex approve, deny, and reply bytes are fixture-tested.
- [x] Generic and non-approval Blocked screens expose no actions.
- [x] Reply text is UTF-8, bounded, non-empty, and contains no control bytes.
- [x] Approve and deny reject reply text.
- [x] No automatic approval path exists.

## Audit and recovery

- [x] `agent.action` has a versioned, strict, redacted payload.
- [x] A complete action writes adjacent `agent.action` and `agent.input` rows in
      one transaction.
- [x] The action event records action, channel, device ID, Blocked sequence,
      prompt rule, and reply byte count only.
- [x] Recovery accepts valid action/input pairs and rejects malformed,
      duplicate, unpaired, or wrong-Blocked action events.
- [x] Partial writes and post-write audit failures say not to retry.

## Ticket security

- [ ] A distinct 256-bit HMAC key is atomically created as a regular `0600`
      file.
- [ ] Tickets bind JTI, Agent, Blocked sequence, action, device, issue time, and
      expiry.
- [ ] Ticket parsing rejects non-canonical encoding, unknown fields, bad MAC,
      future issue times, and excessive lifetime.
- [ ] Ticket rows contain only digest and bounded metadata.
- [ ] One concurrent consume wins and replay remains rejected after restart.
- [ ] Revoked devices cannot use outstanding tickets.
- [ ] Tickets are consumed before PTY execution and are never automatically
      retried.

## API and PWA

- [ ] Action context and action routes require existing authentication.
- [ ] Cookie POST requests require an exact allowed Origin.
- [ ] Request size, media type, UTF-8, unknown fields, and extra JSON values are
      rejected.
- [x] Sequence values cross JavaScript boundaries as decimal strings.
- [ ] Web Push payloads contain action tickets but no screen or reply text.
- [ ] The service worker never approves directly.
- [ ] Direct denial posts once; any failure opens the PWA approval page.
- [ ] Approve, reply, empty actions, and unsupported action buttons open the
      approval page without putting tickets in the URL.
- [ ] The page shows the bounded live approval view, confirms approval, accepts
      a bounded reply, and waits for authoritative state changes.
- [ ] `agent.action` is visible in replay without changing frontend state.

## Verification

- [x] Focused Go unit and race tests pass.
- [x] `go test ./... -race -count=1` passes or any unrelated timeout is recorded.
      On 2026-10-08 all packages except `internal/workspace` passed; that
      package reached the existing 10-minute aggregate timeout while
      `TestAcknowledgeRejectsBrokenWorktreeAfterPrepareSourceSwap` had run for
      4 seconds.
- [x] `go vet ./...` passes.
- [x] `make build` passes.
- [x] `scripts/check-workspace-platforms.sh` passes.
- [x] Web typecheck, unit tests, and production build pass.
- [ ] Playwright covers notification actions and the approval page.
- [ ] Approval UI has no horizontal overflow at 320, 375, and 1280 px.
- [ ] Git diff and generated Web assets contain no secret or reply fixture.
