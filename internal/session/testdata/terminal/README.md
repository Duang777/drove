# Terminal acceptance fixture provenance

These minimized, redacted streams come from the adapter fixtures so the
session acceptance tests freeze the bytes that cross the production output
boundary. Cursor rows are translated from the adapter's 20-row unit-test
viewport to the production session's fixed 40-row viewport. UI text and
control-sequence behavior are otherwise unchanged.

| Prefix | Vendor version | Source |
| --- | --- | --- |
| `claude_` | Claude Code 2.1.181 | Isolated Issue #13 capture and verified UI labels |
| `codex_` | Codex CLI 0.160.0 | Issue #14 capture and verified TUI labels |

The streams contain only vendor UI labels, cursor operations, and synthetic
content. They contain no user prompt, response, path, token, or source text.
`query_replies.json` records the terminal queries required by the captured
startup flows and the replies expected from the pinned x/vt controller.
