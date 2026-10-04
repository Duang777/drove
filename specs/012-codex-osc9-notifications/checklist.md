# Spec review checklist

## Direction

- [x] Issue #40 is the implementation source.
- [x] PR #37 is the runtime identifier baseline.
- [x] OSC 9 adds Blocked evidence only.
- [x] Legacy notify remains responsible for completed-turn Idle evidence.
- [x] Screen rules remain an independent fallback.

## Ownership

- [x] `internal/term` owns OSC and tmux framing.
- [x] `internal/adapter` owns Codex configuration and message semantics.
- [x] `internal/session` owns actor lifecycle and durable ordering.
- [x] `internal/detect` owns authority, candidates, and transitions.
- [x] `internal/event` owns wire validation and compatibility.

## Injection

- [x] The plan subscribes only to `approval-requested`.
- [x] All four managed Codex keys are injected atomically.
- [x] Global options precede the oneshot `exec` subcommand.
- [x] Base and request arguments receive the same conflict scan.
- [x] All four Codex config spellings are covered.
- [x] A conflict preserves caller arguments and skips the whole plan.
- [x] Only a fully applied plan enables terminal decoding.

## Framing

- [x] Direct BEL termination is supported.
- [x] Direct ST termination is supported.
- [x] One Codex tmux DCS wrapper is supported.
- [x] Doubled tmux escape bytes are handled.
- [x] Arbitrary chunk boundaries are supported.
- [x] Payload memory is bounded.
- [x] Malformed and incomplete frames are discarded without content errors.
- [x] Reset discards parser state.
- [x] Committed offset, sequence, and time are retained.

## Normalization

- [x] Only the three Codex 0.160.0 approval prefixes are accepted.
- [x] Invalid UTF-8 is ignored.
- [x] Turn completion is ignored.
- [x] Title-generation completion is excluded by the injected allowlist.
- [x] Signals contain fixed metadata only.
- [x] Free-form OSC text does not cross the adapter boundary.

## Authority

- [x] Terminal OSC reuses `SourceNotify`.
- [x] Terminal OSC does not activate hooks.
- [x] Terminal OSC does not satisfy `required`.
- [x] Awaiting and active hooks suppress terminal OSC.
- [x] Off and fallback modes may arm the permission candidate.
- [x] The existing 750 ms confirmation is reused.
- [x] Terminal OSC never infers Idle or Done.
- [x] Screen approval clearance cancels a pending permission candidate.
- [x] Stale canceled timers cannot change state.

## Compatibility

- [x] Signal payload v4 has a reader before its writer.
- [x] State evidence v4 has a reader before its writer.
- [x] Versions 1 through 3 retain their validators.
- [x] Unknown supplemental versions retain skip behavior.
- [x] Relay and terminal notify variants have separate invariants.
- [x] Writers use `vendor_session_ref`.

## Ordering and lifecycle

- [x] Output bytes commit before scanning.
- [x] Output activity is delivered before same-batch terminal notifications.
- [x] Store failure prevents scanner input.
- [x] Offset and sequence validation precede parsing.
- [x] Process exit fences trailing terminal notifications.
- [x] End-of-output drops incomplete frames.
- [x] Terminal actor remains the single owner of parser state.

## Privacy

- [x] Sanitization occurs before Store persistence and Hub publication.
- [x] Sanitization preserves framing, byte count, and output offsets.
- [x] Only adapter-provided fixed prefixes remain readable.
- [x] Unknown and incomplete OSC 9 bodies are masked.
- [x] OSC body is absent from signal and state payloads.
- [x] OSC body is absent from errors and logs.
- [x] OSC body is absent from Store attachments, Hub, replay, raw tail, and explain.
- [x] Terminal attribution is typed and bounded.
- [x] No prompt, response, path, command, or server name is retained.

## Verification

- [x] Scanner split-point and overflow tests are specified.
- [x] Adapter injection and normalization tests are specified.
- [x] Event reader and writer tests are specified.
- [x] Detector timer tests are specified.
- [x] Session ordering and failure tests are specified.
- [x] Direct and tmux Codex 0.160.0 fixtures are required.
- [x] Full race, vet, build, and diff checks are required.
- [x] Each independently verified implementation part is pushed separately.
