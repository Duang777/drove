# Audited remote actions implementation tasks

Complete each unit with focused tests, the relevant common gates, diff review,
commit, and push.

## Common gates

```bash
gofmt -w .
go test ./... -race -count=1
go vet ./...
make build
scripts/check-workspace-platforms.sh
npm --prefix web run typecheck
npm --prefix web run test -- --run
npm --prefix web run build
git diff --check
```

## Spec

Add the approved design, checklist, and implementation sequence.

Commit:

```text
docs: specify audited remote actions
```

## Unit 1: action and event contracts

Add:

- `agent.ActionKind`;
- strict adapter-owned approval action plans;
- `agent.action` payload, constructor, decoder, and event allowlists;
- current state sequence projection and recovery support;
- event, adapter, projection, and compatibility tests.

Commit:

```text
feat: define audited agent actions
```

## Unit 2: session execution

Add:

- one per-session control gate for PTY writers, state commits, stop, and exit;
- a recording/terminal actor action path with a current-screen check;
- local and remote response fencing by state sequence;
- `Manager.ActionContext` and `Manager.Respond`;
- race, partial-write, stale, prompt-clear, and audit-failure tests.

Commit:

```text
feat: execute stale-safe agent actions
```

## Unit 3: tickets and API

Add:

- atomic `0600` HMAC key loading;
- `notify.db` action-ticket migration;
- strict issue, verify, consume, expiry, replay, and revocation behavior;
- push payload action enrichment;
- the `internal/respond` orchestration service;
- authenticated context and action routes with stable status mapping;
- daemon wiring and focused security tests.

Commit:

```text
feat: secure remote action tickets
```

## Unit 4: PWA approval flow

Add:

- versioned service-worker action parsing and click handling;
- direct denial plus safe page fallback;
- strict TypeScript action/context parsers and API client;
- approval navigation and an inline approval panel;
- live bounded screen preview, approve confirmation, and reply validation;
- EventLog support, component tests, and Playwright coverage.

Commit:

```text
feat: add pwa approval controls
```

## Unit 5: documentation and acceptance

Update:

- English and Chinese README behavior and safety limits;
- remote access guidance;
- package ownership notes;
- this checklist with measured results.

Run all common gates and the complete Blocked to notification to response
scenario. Record unavailable physical-device checks explicitly.

Commit:

```text
docs: document remote actions
```
