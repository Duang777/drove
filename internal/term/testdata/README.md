# Terminal protocol fixture provenance

`codex-0.160.0-osc9-direct.hex` and
`codex-0.160.0-osc9-tmux.hex` are redacted release test vectors derived from
OpenAI Codex tag `rust-v0.160.0`, commit
`a956835d020762cb2b570053af06f643a11c0ecc`.

The framing comes from
`codex-rs/tui/src/notifications/osc9.rs::PostNotification::write_ansi`.
The retained approval prefixes come from
`codex-rs/tui/src/chatwidget/notifications.rs::Notification::message`.
Free-form command and path values are synthetic redacted text. These fixtures
are source-derived vectors, not a live terminal recording.

The files contain hexadecimal bytes so framing controls remain reviewable.
Tests decode all ASCII whitespace before use.

`query_replies.json` contains terminal query and reply cases for the pinned
x/vt controller.
