# Codex OSC 9 notifications

Status: Approved for implementation

Approved: 2026-10-05

Approval source: Issue
[#40](https://github.com/Duang777/drove/issues/40) and the owner's request to
complete the claimed agent-2 batch.

Related work:

- [Session signal injection](../008-session-signal-injection/spec.md)
- [Terminal screen detection](../010-terminal-screen-detection/spec.md)
- [Issue #38](https://github.com/Duang777/drove/issues/38), screen candidate
  cancellation after hook activation
- [PR #37](https://github.com/Duang777/drove/pull/37), persisted vendor session
  references and native resume

## Problem

Codex legacy `notify` reports only completed turns. Screen rules can recognize
an approval prompt after rendering and sampling, but Codex also emits a
structured OSC 9 notification when an approval starts. Drove does not inject
that notification setting or decode the committed terminal stream.

As a result, Codex lacks a prompt, durable signal for the Blocked state. The
old documentation also points OSC 9 work at the already closed Issue #14.

## Goal

For Codex sessions whose complete Drove injection plan is active:

1. Inject the existing legacy `notify` command and an approval-only OSC 9
   configuration.
2. Redact free-form OSC 9 bodies before persistence, then parse frames only
   after the sanitized output bytes are durable.
3. Normalize recognized approval messages into a fixed, redacted notify
   signal.
4. Reuse the existing permission confirmation candidate to enter Blocked.
5. Preserve screen fallback and use its approval-cleared edge to cancel a
   pending terminal permission candidate.

OSC 9 remains non-authoritative. It does not activate hooks, satisfy a
`required` hook policy, infer Idle or Done, or retain the notification body.

## Injection contract

The Codex adapter owns four process-local keys:

```text
notify
tui.notifications
tui.notification_method
tui.notification_condition
```

A successful plan prepends:

```text
-c notify=["<relay>","hook","--vendor","codex",...]
-c tui.notifications=["approval-requested"]
-c tui.notification_method="osc9"
-c tui.notification_condition="always"
```

The options remain before the `exec` subcommand in oneshot mode. If either
base arguments or request arguments set any owned key through `-c`,
`--config`, `-c=`, or `--config=`, the adapter rejects the whole plan with
`ErrSignalInjectionConflict`. Session records the existing
`skipped/argument_conflict` result and leaves all caller arguments unchanged.

Only a complete applied plan enables terminal notification sanitization and
decoding. Claude, generic, configured-off, relay-unavailable, and conflict
paths do not scan or rewrite OSC 9.

## Pre-persistence privacy

`internal/term` provides a vendor-neutral streaming sanitizer for the same
direct and tmux framing as the scanner. `internal/adapter` supplies the three
Codex safe prefixes and constructs one sanitizer per injected session.
`internal/session` applies it after signal-token redaction and before creating
any `output.chunk`.

The sanitizer:

- preserves framing, non-OSC bytes, byte count, and output offsets;
- preserves one complete allowlisted prefix, including its trailing space;
- replaces every following body byte with `*`;
- replaces every byte of an unknown OSC 9 body with `*`;
- carries partial framing and prefixes across callbacks;
- masks any unresolved body when the output stream ends.

Original free-form OSC 9 bytes never enter a Store attachment, hydrated Hub
payload, replay, raw tail, terminal snapshot, error, log, or explanation.

## Terminal framing

`internal/term` owns a bounded stateful scanner over sanitized
`CommittedChunk`. It accepts:

- direct `ESC ] 9 ; <body> BEL`;
- direct `ESC ] 9 ; <body> ESC \`;
- the single tmux DCS passthrough emitted by Codex 0.160.0, including doubled
  inner escape bytes.

Every byte boundary may split a frame. The scanner ignores non-OSC-9 control
strings. Oversized, malformed, and incomplete frames produce no result and no
error containing frame content. Reset discards an incomplete frame.

Each accepted frame carries a copied redacted body plus committed provenance:

- exclusive output offset at the frame terminator;
- final committed output sequence for the containing callback batch;
- commit time.

The package does not interpret Codex text.

## Adapter normalization

`internal/adapter` interprets the frame body. Codex 0.160.0 approval messages
must begin with one of these fixed prefixes:

```text
Approval requested:
Codex wants to edit
Approval requested by
```

The decoder rejects invalid UTF-8 and ignores every other message, including
turn completion and title-generation output. A match produces one signal with
fixed metadata:

```text
source       = notify
kind         = permission_requested
vendor       = codex
vendor_event = tui_notification
scope        = root
notification = approval-requested
evidence     = approval requested
confidence   = 1
delivery_id  = empty
terminal.protocol = osc9
```

The body, command, path, server name, and other free-form suffixes do not enter
the signal, event payload, error, log, replay, or explanation.

## Typed attribution and compatibility

`agent.TerminalAttribution` contains:

```text
protocol
output_offset
last_output_seq
```

Signal payload version 4 and state evidence version 4 add an optional terminal
attribution. Versions 1 through 3 keep their current validators unchanged.

Version 4 distinguishes two `notify` variants:

- relay notify has a canonical delivery ID, no terminal attribution, and
  reports only root `turn_stopped`;
- terminal notify has no delivery ID, requires terminal attribution, and
  reports only root `permission_requested`.

Recovery and explain readers accept version 4 before any production writer
uses it. Unknown supplemental versions retain the current skip behavior.
Writers use `vendor_session_ref`; they do not restore `vendor_turn_id`.

## Detector authority

Terminal notify follows the same weak authority class as legacy notify:

- hooks off or hook fallback may arm the existing permission candidate;
- hook awaiting, hook active, and required policy suppress it;
- it never changes hook status;
- it never enters Blocked without the existing 750 ms confirmation;
- terminal notifications do not use delivery-ID deduplication because the
  terminal actor already enforces committed offset and sequence order.

When accepted, the notification cancels lower-confidence conflicting
fallback, idle, heuristic-blocked, and screen candidates before replacing the
permission candidate.

A `codex.approval_prompt` cleared screen edge always cancels the permission
candidate before applying its normal screen rule. This also applies while the
Agent is still Working, preventing a fast approval from causing a delayed
Blocked transition.

## Session ordering

The output path is:

```text
redact signal token
    -> sanitize OSC 9 free-form body when enabled
    -> persist output.chunk batch
    -> feed the committed bytes to the terminal actor
    -> receive normalized terminal observations
    -> deliver pending output activity
    -> deliver terminal observations
```

This order makes the output durable before parsing and prevents same-batch
activity from canceling a newly armed permission candidate. A Store failure
means the scanner never sees the bytes.

The terminal actor owns the scanner and decoder state. It validates offset and
sequence before parsing. After process exit, trailing output remains durable
and renderable but cannot create terminal or screen signals. End-of-output
resets the scanner and drops an incomplete frame.

## Verification

Automated tests cover:

- direct BEL, direct ST, tmux passthrough, all split points, byte-at-a-time
  input, multiple frames, malformed input, overflow recovery, and reset;
- exact Codex argument order and all conflict spellings in base and request
  arguments;
- the three accepted approval prefixes, near misses, invalid UTF-8, title
  completion, and free-form text removal;
- signal and state evidence version 4 validation, round trips, recovery, and
  explanation;
- detector authority, confirmation, stale timers, and fast approval;
- durable-before-observe, activity-before-notify, Store failure, process-exit
  fencing, and privacy across Store, Hub, replay, explain, logs, and errors;
- direct and tmux fixtures derived from Codex CLI 0.160.0.

The final gate is:

```bash
go test -race ./...
go vet ./...
make build
git diff --check
```

No dependency is added.
