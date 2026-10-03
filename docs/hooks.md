# 配置 Agent 状态 hooks

Drove 通过 Claude Code command hooks 和 Codex notify 接收状态信号。默认路径
只修改 Drove 启动的子进程，不修改厂商持久配置，也不代替你接受 workspace、
project 或 hook trust。

## 默认会话注入

`drove up` 对支持的 adapter 默认使用 `signal_injection: "auto"`：

- Claude：在 `<data_dir>/sessions/<agent-id>/claude-settings.json` 写入仅含
  hooks 的 `0600` 临时文件，并通过 `--settings` 加载。进程退出后删除目录。
- Codex：通过 `-c notify=[...]` 注入 argv relay，不创建配置文件。notify 只
  证明 turn 已结束，因此只能确认 Idle，不能把会话标记为 `hook_active`。

用户或项目配置仍按厂商规则生效。Claude 会合并各层 hooks；相同 command 会
去重。`disableAllHooks` 和 managed policy 仍可阻止注入的 hook。Codex 已手工
配置且获信任的原生 hook 仍可成为完整状态权威。

可按厂商关闭：

```json
{
  "agents": {
    "claude": {
      "signal_injection": "off"
    },
    "codex": {
      "signal_injection": "off"
    }
  }
}
```

调用方显式传入的 Claude `--bare`、Claude `--settings` 或 Codex
`-c notify=...` 由调用方拥有。Drove 不覆盖这些参数，并把本次注入记录为
`skipped`。会话 API 通过
`signal_injection`、`signal_injection_status` 和 `signal_injection_reason`
报告启动结果；`hook_status` 单独报告运行时权威状态。

如果 daemon 找不到可执行的 `drove` relay，`auto` 继续启动会话，并将原因记录
为 `relay_unavailable`。`required` 仍会等待手工配置的原生 hook，并在 5 秒内
没有收到合法 hook 时停止会话。

Codex 的 OSC 9 通知和屏幕模型由 Issue #14 跟踪。Drove 不注入未文档化的
session trust hash，也不使用 trust bypass。

持久安装器是延期的可选能力。它只适用于需要让 Drove 之外的厂商进程也上报
状态的场景。

## 手工原生 hook 配置

手工配置适合需要完整 Codex Working / Blocked 信号，或关闭默认注入的场景。
它也会影响 Drove 之外直接启动的厂商会话。

### 准备

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

### 配置 Claude Code

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
            "command": "/absolute/path/to/drove hook --vendor claude --managed-by drove/v1",
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
            "command": "/absolute/path/to/drove hook --vendor claude --managed-by drove/v1",
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
            "command": "/absolute/path/to/drove hook --vendor claude --managed-by drove/v1",
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
            "command": "/absolute/path/to/drove hook --vendor claude --managed-by drove/v1",
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
            "command": "/absolute/path/to/drove hook --vendor claude --managed-by drove/v1",
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
            "command": "/absolute/path/to/drove hook --vendor claude --managed-by drove/v1",
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
            "command": "/absolute/path/to/drove hook --vendor claude --managed-by drove/v1",
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
            "command": "/absolute/path/to/drove hook --vendor claude --managed-by drove/v1",
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
            "command": "/absolute/path/to/drove hook --vendor claude --managed-by drove/v1",
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
            "command": "/absolute/path/to/drove hook --vendor claude --managed-by drove/v1",
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

### 配置 Codex

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
            "command": "/absolute/path/to/drove hook --vendor codex --managed-by drove/v1",
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
            "command": "/absolute/path/to/drove hook --vendor codex --managed-by drove/v1",
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
            "command": "/absolute/path/to/drove hook --vendor codex --managed-by drove/v1",
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
            "command": "/absolute/path/to/drove hook --vendor codex --managed-by drove/v1",
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
            "command": "/absolute/path/to/drove hook --vendor codex --managed-by drove/v1",
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
            "command": "/absolute/path/to/drove hook --vendor codex --managed-by drove/v1",
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
            "command": "/absolute/path/to/drove hook --vendor codex --managed-by drove/v1",
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
            "command": "/absolute/path/to/drove hook --vendor codex --managed-by drove/v1",
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

- `auto` 是 Claude 和 Codex 的默认值。Drove 先尝试会话注入，再等待 5 秒；
  如果未收到合法原生 hook，则启用终端启发式。Claude 通常通过注入的
  `SessionStart` 激活。Codex notify 不激活 hooks，后续合法原生 hook 仍可成为
  权威信号源。
- `off` 不注入 relay 环境，并立即启用终端启发式。
- `required` 只接受原生 hook 权威。如果 5 秒内没有合法 hook，Drove 会停止
  会话并返回错误；Codex notify 不能满足该策略。

不支持 hook 的 adapter 默认使用 `off`，且不能使用 `required`。

`agents.<vendor>.signal_injection: "off"` 只关闭会话配置注入。配合
`--hooks auto` 或 `--hooks required` 时，手工配置的原生 hook 仍可使用注入的
会话环境变量，并可激活 hook 权威。

`GET /api/v1/agents/{id}` 返回 hook 策略、运行时状态和注入结果。配置已注入
不代表 hook 已运行。只有 `hook_status: "hook_active"` 表示当前会话提交过
合法原生 hook。

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

2026-10-03 的会话注入回归基于 Drove 提交 `48d68bd`：

- Claude Code `2.1.288` 在隔离 HOME 中运行。`required` 会话通过注入的
  `SessionStart` 激活 hook 权威，并收到 `SessionEnd`。临时目录和 settings
  文件权限分别为 `0700` 和 `0600`，进程退出后目录被删除。
- Claude Code `2.1.288` 对用户 settings 与会话 settings 中的相同 command
  去重，只产生一条 `SessionStart` signal。用户设置
  `disableAllHooks: true` 时没有原生 hook signal，`auto` 在 5 秒后进入
  fallback。
- Codex CLI `0.160.0` 使用隔离的本地 Responses SSE provider 完成真实
  oneshot turn。Codex 调用进程级 notify，Drove 收到版本 2 的
  `agent-turn-complete` signal，且 `hook_status` 没有变为 `hook_active`。
- 长生命周期 relay 回归确认 fallback 中的 notify 在 1 秒后把 Working 改为
  Idle，同时保持 `hook_status: "fallback"`。SQLite 未保存测试 prompt、
  assistant response 或 Codex 内部标题 prompt。
- `signal_injection: "off"` 没有增加参数或临时文件。关闭注入后，手工 Claude
  hook 仍能满足 `required`。
- Claude 三层 settings 的运行前后哈希一致。Codex oneshot 和显式 `off`
  场景的 `config.toml` 运行前后哈希一致。Codex TUI 自己写入了 `[tui]`
  状态，Drove 没有写入该文件。

自动化测试还覆盖全部受支持事件、hook 与 fallback 的 Blocked 恢复、timer
竞态、持久化失败和重启恢复。

Codex TUI 会在绘制首屏前查询终端能力。当前 PTY 桥接器按行交付输出，不能
完成该终端查询握手。真实 Codex notify 因此通过 oneshot 路径验证；TUI 屏幕
处理和 OSC 解析继续由 Issue #14 跟踪。
