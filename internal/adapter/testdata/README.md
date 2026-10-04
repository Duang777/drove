# Screen fixture provenance

These fixtures are minimized, redacted terminal frames. They retain only
vendor UI labels, cursor operations, and synthetic non-sensitive content.
They contain no user prompt, response, path, token, or source text.

| Directory | Vendor version | Evidence source |
| --- | --- | --- |
| `claude/` | Claude Code 2.1.181 | Isolated startup capture recorded for Issue #13 plus UI labels verified in the 2.1.181 binary |
| `codex/` | Codex CLI 0.160.0 | Issue #14 startup capture plus 0.160.0 TUI snapshot labels |

`approval.bin` renders a visible approval prompt. `idle_after_approval.bin`
uses cursor movement and erasure to replace that prompt with the vendor's
idle composer. Claude's `interrupted.bin` renders its interrupt result and
ends inside an incomplete CSI frame. Each `near_miss.bin` contains ordinary
output with `Error` and incomplete approval wording that must not match.
