# 配置 Agent 状态 hooks

Drove 通过 Claude Code 或 Codex 的 command hook 接收状态信号。Drove 不会修改
厂商配置，也不会代替你接受 workspace、project 或 hook trust。

## 准备

先构建并安装 `drove`，然后确认 hook 子进程能找到它：

```bash
make build
command -v drove
```

如果 `command -v drove` 没有返回路径，请在下面的配置中使用
`/absolute/path/to/drove`。不要依赖仅在交互 shell 中设置的 alias。

Drove 为启用 hook 的会话注入三个环境变量：

```text
DROVE_AGENT_ID
DROVE_SIGNAL_URL
DROVE_SIGNAL_TOKEN
```

`drove hook` 从标准输入读取一份厂商 JSON，并把它发到当前会话的 loopback
signal endpoint。该命令不会启动 daemon，也不会读取 Drove 控制令牌。

## 配置 Claude Code

把配置合并到 `~/.claude/settings.json`、项目的
`.claude/settings.json`，或项目的 `.claude/settings.local.json`。保留文件中
已有的设置和 hook。

下面的最小配置覆盖激活、工作、工具活动、人工等待、恢复、Idle 和会话结束：

```json
{
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/absolute/path/to/drove hook --vendor claude",
            "timeout": 5
          }
        ]
      }
    ],
    "UserPromptSubmit": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/absolute/path/to/drove hook --vendor claude",
            "timeout": 5
          }
        ]
      }
    ],
    "PreToolUse": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/absolute/path/to/drove hook --vendor claude",
            "timeout": 5
          }
        ]
      }
    ],
    "PostToolUse": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/absolute/path/to/drove hook --vendor claude",
            "timeout": 5
          }
        ]
      }
    ],
    "PermissionRequest": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/absolute/path/to/drove hook --vendor claude",
            "timeout": 5
          }
        ]
      }
    ],
    "Elicitation": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/absolute/path/to/drove hook --vendor claude",
            "timeout": 5
          }
        ]
      }
    ],
    "ElicitationResult": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/absolute/path/to/drove hook --vendor claude",
            "timeout": 5
          }
        ]
      }
    ],
    "Notification": [
      {
        "matcher": "permission_prompt|elicitation_dialog|elicitation_url_dialog|agent_needs_input|quota_auto_resume_stale|idle_prompt",
        "hooks": [
          {
            "type": "command",
            "command": "/absolute/path/to/drove hook --vendor claude",
            "timeout": 5
          }
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/absolute/path/to/drove hook --vendor claude",
            "timeout": 5
          }
        ]
      }
    ],
    "SessionEnd": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/absolute/path/to/drove hook --vendor claude",
            "timeout": 5
          }
        ]
      }
    ]
  }
}
```

Claude Code 会合并不同 settings scope 中的 hook。使用 `/hooks` 检查最终配置。
在你信任 workspace 之前，项目 hook 不会运行。管理员也可以通过
`allowManagedHooksOnly` 禁用非 managed hook。

Drove adapter 还接受 `PostToolUseFailure`、`PostToolBatch`、
`PermissionDenied`、`StopFailure`、`SubagentStart`、`SubagentStop` 和
`TaskCompleted`。需要记录这些审计事件时，为它们添加相同的 command handler。

完整 settings schema 和 trust 规则见
[Claude Code hooks 官方文档](https://code.claude.com/docs/en/hooks)。

## 配置 Codex

将配置放到 `$CODEX_HOME/hooks.json`，默认路径是 `~/.codex/hooks.json`。项目级
配置放到 `.codex/hooks.json`。如果同一配置层已经在 `config.toml` 中内联定义
hook，请继续使用 TOML。Codex 会合并同层的两种表示，但会在启动时警告。

```json
{
  "description": "Report agent state to Drove",
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/absolute/path/to/drove hook --vendor codex",
            "timeout": 5
          }
        ]
      }
    ],
    "UserPromptSubmit": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/absolute/path/to/drove hook --vendor codex",
            "timeout": 5
          }
        ]
      }
    ],
    "PreToolUse": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/absolute/path/to/drove hook --vendor codex",
            "timeout": 5
          }
        ]
      }
    ],
    "PostToolUse": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/absolute/path/to/drove hook --vendor codex",
            "timeout": 5
          }
        ]
      }
    ],
    "PermissionRequest": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/absolute/path/to/drove hook --vendor codex",
            "timeout": 5
          }
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/absolute/path/to/drove hook --vendor codex",
            "timeout": 5
          }
        ]
      }
    ],
    "Interrupt": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/absolute/path/to/drove hook --vendor codex",
            "timeout": 5
          }
        ]
      }
    ],
    "SessionEnd": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/absolute/path/to/drove hook --vendor codex",
            "timeout": 5
          }
        ]
      }
    ]
  }
}
```

Codex 要求用户分别信任项目配置和每个非 managed hook。打开 `/hooks`，检查
具体命令并自行批准。定义变化会产生新 hash，并需要再次审核。Drove 不写入
Codex trust 状态，也不使用 `--dangerously-bypass-hook-trust`。

Drove adapter 还接受 `PreCompact`、`PostCompact`、`SubagentStart` 和
`SubagentStop`。

当前 schema 和 trust 流程见
[Codex hooks 官方文档](https://developers.openai.com/codex/hooks)。

## 选择 hook 策略

启动会话时指定策略：

```bash
drove up claude --hooks auto
drove up claude --hooks off
drove up claude --hooks required
```

三个策略的含义如下：

- `auto` 是 Claude 和 Codex 的默认值。Drove 等待 5 秒；如果未收到合法信号，
  则启用终端启发式。后续合法 hook 仍会成为权威信号源。
- `off` 不注入 relay 环境，并立即启用终端启发式。
- `required` 只接受 hook 权威。如果 5 秒内没有合法信号，Drove 会停止会话并
  返回错误。

不支持 hook 的 adapter 默认使用 `off`，且不能使用 `required`。

`GET /api/v1/agents/{id}` 返回 `hook_policy` 和 `hook_status`。配置文件存在
不代表 hook 已运行。只有 `hook_status: "hook_active"` 表示当前会话提交过
合法信号。

## 失败行为与数据限制

`drove hook` 只上报观察结果。Cobra 接受命令形状后，即使输入校验、认证或投递
失败，relay 也以状态 0 退出。失败时只向 stderr 写一条通用诊断，不向 stdout
写内容，因此不会阻断厂商动作。

relay 接受一个不超过 1 MiB 的 UTF-8 JSON 对象。网络错误、HTTP 429 或 HTTP
503 会在 100 ms 后重试一次。两次请求使用同一个 delivery ID，每次请求的超时
为 750 ms。

Drove 只保存规范化后的事件名、标识符、时间、scope、confidence 和固定证据
标签。它不保存厂商原始 JSON、prompt、tool input、transcript path、assistant
text 或 capability token。

## 验证记录

2026-10-03 的回归基于 Drove 提交 `2717927`：

- Claude Code `2.1.181` 使用隔离的 `--settings` 文件运行。真实 command hook
  上报了 `SessionStart`、`UserPromptSubmit` 和 `SessionEnd`。
- Claude Code `2.1.181` 在 `--safe-mode` 下没有上报 hook。`auto` 进入
  fallback，`required` 返回 HTTP 503。
- Codex CLI `0.160.0` 通过 `npx` 运行未信任的项目 hook，且未使用 trust
  bypass。Drove 没有收到 hook，`auto` 进入 fallback。
- 自动化过程不能代替用户批准 Codex hook，因此没有执行受信任的 Codex 厂商
  端到端测试。Codex relay 与 adapter 使用官方形状 fixture 验证。隔离 relay
  回归确认两种 vendor schema 都能驱动 `Working`、`Blocked` 和 `Idle`。
- 重复 delivery 只生成一条 `agent.signal`。SQLite 中未出现测试 prompt、
  tool input、transcript path 或 capability token。

自动化测试还覆盖全部受支持事件、hook 与 fallback 的 Blocked 恢复、timer
竞态、持久化失败和重启恢复。
