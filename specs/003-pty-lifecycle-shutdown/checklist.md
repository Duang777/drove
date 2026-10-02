# Spec review checklist

## Scope

- [x] The spec fixes PTY callback registration and daemon shutdown together.
- [x] Input, resize, RFC-001 runner modes, hooks, and detectors remain out of scope.
- [x] Process signal escalation and process-group cleanup remain out of scope.
- [x] Runtime persistence failure redesign remains out of scope.
- [x] REST, WebSocket, CLI, event, and database schemas remain unchanged.
- [x] The expected file count and reason are explicit.

## PTY lifecycle

- [x] Callbacks are immutable before goroutines start.
- [x] Start failure invokes no callback.
- [x] Output and final partial-line delivery are defined.
- [x] Duplicate delivery on data plus read error is prohibited.
- [x] Exit callback invocation is exactly once.
- [x] Session completion includes both loops and all callbacks.
- [x] `WaitCh` and `Session.Close` have distinct completion semantics.
- [x] Repeated and concurrent close calls are idempotent.
- [x] Write and resize behavior after close begins is explicit.

## Session lifecycle

- [x] The startup ready channel prevents exit-before-working corruption.
- [x] The startup failure path releases the ready channel before close.
- [x] Manager closure blocks new starts before creating history.
- [x] In-flight starts drain before the session snapshot.
- [x] Session close order is stable.
- [x] Individual close failures do not skip remaining sessions.
- [x] Manager closure relies on the existing exit callback for `stopped`.
- [x] Manager behavior after closure is explicit.

## Event and API behavior

- [x] Hub closure is idempotent.
- [x] Publish and Close cannot send on a closed channel.
- [x] Subscribe, Unsubscribe, and Publish behavior after close is explicit.
- [x] Existing WebSocket loops exit through closed subscription channels.
- [x] No API server code change is required.

## Daemon shutdown

- [x] Cancellation and Serve failure share one cleanup path.
- [x] The order is API, sessions, Hub, then store.
- [x] Existing WebSockets can receive final state events before Hub closure.
- [x] Store closure happens after all PTY callbacks finish.
- [x] Cleanup continues after an earlier error.
- [x] Cleanup errors retain component context.
- [x] Startup failures still close the store.
- [x] The conflicting daemon package rule is identified for correction.

## Ownership

- [x] PTY owns process, stream, callback, and completion mechanics.
- [x] Session owns Agent state and the set of attached PTYs.
- [x] Event owns subscription closure.
- [x] Daemon owns cross-component order.
- [x] Store remains unaware of process lifecycle.
- [x] No package dependency cycle is introduced.

## Verification

- [x] PTY tests cover short output, partial output, exit, and close waits.
- [x] Session tests cover immediate exit and Manager closure.
- [x] Hub tests cover concurrent close and publish.
- [x] Daemon tests verify process termination and persisted `stopped`.
- [x] Restart after normal shutdown is part of acceptance.
- [x] Repeated focused race runs are required.
- [x] Full race, vet, build, and diff checks are required.
- [x] Two independently useful commit and push boundaries are defined.

## Approval gate

- [x] The owner approves the scope, rule correction, and design before implementation starts.
