# Phase 1B 钩子管理调研

- 调研日期：2026-10-03
- Drove 基线：`271792720012b072a087e53839823898800fe05d`
- Codex 源码快照：[`86a54b0`](https://github.com/openai/codex/commit/86a54b051c08f34f373c507ae16a91915ab08700)
- 范围：显式安装、幂等更新、精确卸载、所有权和状态

## 建议

在统一的钩子管理服务后实现各厂商专用的配置适配器。Claude 使用设置文件中的 JSON，Codex 默认使用 `hooks.json`。安装、厂商信任、策略许可和实际执行情况是相互独立的事实。Drove 不得代替用户接受信任。

目标节点存在且已记录为 Drove 所有时，安装才算成功，但这不能证明厂商会执行这些节点。唯一可移植的启用证明，是当前 Drove 会话观测到有效的钩子信号。

## 当前配置模型

### Claude Code

Claude 设置文件使用严格 JSON。相关的可写层级如下：

| 作用域 | 默认位置 | 说明 |
| --- | --- | --- |
| 用户 | `~/.claude/settings.json` | 可通过 `CLAUDE_CONFIG_DIR` 更改基础目录。 |
| 项目 | `<repo>/.claude/settings.json` | 适合纳入版本控制。 |
| 项目本地 | `<repo>/.claude/settings.local.json` | 本地覆盖文件，通常由 Git 忽略。 |
| 托管 | 平台管理的设置，包括 macOS 上的 `/Library/Application Support/ClaudeCode/managed-settings.json` | 归管理员所有，Drove 不得编辑。 |
| CLI | 通过 `--settings` 提供的文件或 JSON | 归本次调用所有，Drove 不得向其中隐式持久化配置。 |

文档说明的优先级从高到低依次为托管、命令行、本地、项目和用户。普通标量设置按优先级覆盖，而各设置作用域中的钩子定义会合并。文档没有为普通用户或项目设置提供通用的独立钩子文件。插件钩子使用单独的插件清单模型。

来源：Claude 的[设置优先级和位置](https://code.claude.com/docs/en/settings#settings-precedence)、[钩子位置](https://code.claude.com/docs/en/hooks#hook-locations)和[配置诊断](https://code.claude.com/docs/en/debug-your-config)文档。

命令钩子以事件键为入口，事件键包含匹配器组，每个匹配器组包含处理器。以下是有效的设置片段：

```json
{
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "drove hook --vendor claude --managed-by drove/v1",
            "timeout": 10,
            "statusMessage": "Reporting session state to Drove"
          }
        ]
      }
    ],
    "Notification": [
      {
        "matcher": "permission_prompt|elicitation_dialog|elicitation_url_dialog|agent_needs_input|idle_prompt|quota_auto_resume_stale",
        "hooks": [
          {
            "type": "command",
            "command": "drove hook --vendor claude --managed-by drove/v1",
            "timeout": 10
          }
        ]
      }
    ]
  }
}
```

`--managed-by` 是拟议的无行为所有权标记。安装此片段前，必须先为 Drove 的中继命令添加该参数。当前中继命令仅接受 `--vendor`。

Phase 1B 应安装当前适配器允许的以下事件：
`SessionStart`、`UserPromptSubmit`、`PreToolUse`、`PostToolUse`、
`PostToolUseFailure`、`PostToolBatch`、`PermissionRequest`、`PermissionDenied`、
`Elicitation`、`ElicitationResult`、`Notification`、`Stop`、`StopFailure`、
`SubagentStart`、`SubagentStop`、`TaskCompleted` 和 `SessionEnd`。`Notification` 的匹配范围应保持收敛。处理器字段和事件限制见[钩子处理器字段](https://code.claude.com/docs/en/hooks#hook-handler-fields)与[匹配器模式](https://code.claude.com/docs/en/hooks#matcher-patterns)。

初期使用字符串形式的 `command`。Claude 还记录了 `args` 执行形式，但无法从官方发布记录中确认支持该形式的最低版本。不要生成 `once`，因为设置文件会忽略它。

### OpenAI Codex

`CODEX_HOME` 默认为 `~/.codex`。Codex 会在每个生效的配置层中查找独立的 `hooks.json`，或 `config.toml` 内联的 `[hooks]`。典型的可写位置如下：

| 作用域 | JSON | TOML |
| --- | --- | --- |
| 用户 | `$CODEX_HOME/hooks.json` | `$CODEX_HOME/config.toml` |
| 项目 | `<repo>/.codex/hooks.json` | `<repo>/.codex/config.toml` |
| 系统或托管 | 管理员控制的配置层 | Drove 不得编辑。 |

通用配置的优先级从高到低依次为命令行覆盖、从仓库根目录到当前目录的项目配置层（距离最近者优先）、选定的配置档、用户配置、系统配置和默认值。不同来源的钩子会累加，不会相互替换。同一配置层同时定义 JSON 和 TOML 钩子时，Codex 会合并两者并发出警告。因此，Drove 应保留该层已有的表示形式。如果两者都不存在，则创建 `hooks.json`。

来源：Codex 的[基础配置](https://developers.openai.com/codex/config-basic)、[高级钩子配置](https://developers.openai.com/codex/config-advanced#hooks)、[钩子发现](https://developers.openai.com/codex/hooks#where-codex-looks-for-hooks)和[托管配置](https://developers.openai.com/codex/enterprise/managed-configuration)文档。

Codex `hooks.json` 的顶层只能包含可选的 `description` 和 `hooks`，未知的顶层字段会被拒绝。以下是有效示例：

```json
{
  "description": "Drove lifecycle reporting",
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "drove hook --vendor codex --managed-by drove/v1",
            "timeout": 10,
            "statusMessage": "Reporting session state to Drove"
          }
        ]
      }
    ],
    "PermissionRequest": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "drove hook --vendor codex --managed-by drove/v1",
            "timeout": 10
          }
        ]
      }
    ]
  }
}
```

对应的有效内联 TOML 如下：

```toml
[hooks]
SessionStart = [
  { hooks = [
    { type = "command", command = "drove hook --vendor codex --managed-by drove/v1", timeout = 10, statusMessage = "Reporting session state to Drove" }
  ] }
]
PermissionRequest = [
  { hooks = [
    { type = "command", command = "drove hook --vendor codex --managed-by drove/v1", timeout = 10 }
  ] }
]
```

[`hook_config.rs:L10-L200`](https://github.com/openai/codex/blob/86a54b051c08f34f373c507ae16a91915ab08700/codex-rs/config/src/hook_config.rs#L10-L200) 证实了该配置结构。应安装当前定义的全部 12 个事件：`PreToolUse`、`PermissionRequest`、
`PostToolUse`、`PreCompact`、`PostCompact`、`SessionStart`、`SessionEnd`、
`UserPromptSubmit`、`SubagentStart`、`SubagentStop`、`Stop` 和 `Interrupt`。规范的功能键是 `features.hooks`，`codex_hooks` 已弃用。钩子目前默认启用，因此安装器无需添加功能标志。

公开文档称异步命令处理器会被解析但不会执行，而固定版本的源码会执行异步命令处理器。因此，发布版本的行为尚未验证，Drove 应只生成同步处理器。

## 信任与状态语义

### Claude

在用户接受工作区信任前，settings 文件中的钩子不会在交互式会话中运行。Print 模式和 SDK 会将工作目录视为已信任。`allowManagedHooksOnly` 可以排除非托管钩子，`disableAllHooks` 可以禁用钩子。`/hooks` 显示发现的钩子及其来源，`/status` 显示已加载的配置来源。文档未将两者定义为稳定、机器可读的信任 API。

Drove 可以静态报告配置和可见的策略值，但无法可靠判断用户是否已接受工作区信任，也无法判断某个具体钩子是否会执行。除非已观测到当前会话的信号，否则 Claude 的信任状态应报告为 `unknown`。来源：[工作区信任](https://code.claude.com/docs/en/hooks#workspace-trust)、[钩子安全](https://code.claude.com/docs/en/hooks#security-considerations)和 [`allowManagedHooksOnly`](https://code.claude.com/docs/en/settings-reference#allowmanagedhooksonly) 文档。

### Codex

Codex 只为受信任的项目加载项目配置。此外，每个非托管钩子都有一个规范化定义哈希。用户接受前，其状态为 `untrusted`；哈希匹配时为 `trusted`；定义发生变化后为 `modified`。托管钩子由策略设为可信。只有已启用且状态为 `managed` 或 `trusted` 的钩子才会执行。

[`discovery.rs:L666-L823`](https://github.com/openai/codex/blob/86a54b051c08f34f373c507ae16a91915ab08700/codex-rs/hooks/src/engine/discovery.rs#L666-L823) 验证了上述行为。当前源码通过 app-server `hooks/list` 暴露这些状态，参见[协议元数据](https://github.com/openai/codex/blob/86a54b051c08f34f373c507ae16a91915ab08700/codex-rs/app-server-protocol/src/protocol/v2/plugin.rs#L585-L606)。但公开文档无法证实该 RPC 的兼容性，也无法证实是否存在受支持的独立调用方式。因此，应将它视为受版本限制的可选探测方式，而不是默认契约。

Drove 必须引导用户前往厂商 UI 审查信任，绝不能写入 Codex 的 `hooks.state.*.trusted_hash`，也不能调用 `--dangerously-bypass-hook-trust`。

### 状态维度

`drove hooks status` 应分别报告以下字段：

| 维度 | 取值 | 证据 |
| --- | --- | --- |
| 配置 | `absent`、`present`、`partial`、`invalid` | 解析后的目标文件和预期节点 |
| 所有权 | `owned`、`foreign`、`drifted`、`unknown` | 清单和规范节点的精确哈希 |
| 策略 | `allowed`、`disabled`、`blocked`、`unknown` | 能够确定时可见的生效策略 |
| 厂商信任 | `trusted`、`untrusted`、`modified`、`managed`、`unknown` | 仅限受支持的厂商探测 |
| 运行时 | `observed`、`not_observed`、`stale` | 当前会话的有效信号 |
| 能力 | `supported`、`unsupported`、`unknown` | 版本和配置结构探测 |

不得将这些维度合并为单个布尔值。尤其是，`present` 不等于 `trusted`，`trusted` 也不等于 `observed`。

## 所有权与文件安全

Drove 的所有权清单存放在 `~/.drove/hooks/` 下，每个规范目标路径对应一条记录。清单应包含：

- 配置结构版本、厂商、作用域、规范路径和 JSON/TOML 表示形式；
- 稳定的逻辑节点 ID，以及规范节点值的 SHA-256 哈希；
- 目标文件和各层容器是否由 Drove 创建；
- 最近一次写入前后的目标摘要；
- 安装器或 Drove 版本、时间戳和备份路径。

命令标记有助于人工检查，但不足以证明所有权。更新或删除前，必须同时具备清单记录和精确的节点哈希。完全相同的既有外部节点可以满足配置要求，但其状态仍为 `foreign`。只有未来显式执行 `--adopt` 操作才能接管它。

每次修改都应按以下步骤执行：

1. 解析作用域和规范目标，不跟随非预期的符号链接。
2. 获取目标文件专用的建议锁，读取文件并记录其摘要。
3. 使用严格的 JSON 或 TOML 解析器。输入无效、重复键有歧义、配置结构不受支持或目标受托管时，拒绝操作。
4. 计算结构化合并结果。保留无关的键、数组顺序和外部钩子节点。仅对已证明归 Drove 所有的节点去重。
5. 如果规范结果没有变化，则返回 `unchanged`，不创建备份，也不重写文件。
6. 如果需要修改，则创建权限受限的备份，并以原子方式持久化 `pending` 清单。清单包含目标的新旧摘要和预期节点哈希。
7. 在同一目录写入临时文件，保留安全的文件权限并刷盘。重新检查源文件摘要后，以原子方式重命名文件，并在系统支持时刷写目录。
8. 目标文件写入成功后，以原子方式完成所有权清单。重启时，将实际目标与 `pending` 记录中的新旧摘要匹配，以恢复该记录。任何第三种摘要值都表示冲突，需要人工检查。

建议锁无法强制 Claude 或 Codex 配合，因此必须重新验证摘要。JSON 格式可能发生变化。编辑 TOML 时应使用可保留文档结构的解析器。如果无法保证安全的往返保留，应拒绝修改 TOML，以免破坏注释或格式。

卸载时只能删除精确匹配且归 Drove 所有的节点，然后清理由 Drove 创建且当前为空的容器。只有目标文件由 Drove 创建且不含外部内容时，才能删除整个文件。卸载时绝不能恢复完整备份，否则会抹掉用户在安装后所做的修改。如果发生漂移，应报告冲突并停止。独立的清单修复操作可以丢弃过时的所有权元数据，但不得删除未知节点。

## CLI 契约

```text
drove hooks install   --vendor claude|codex --scope user|project [--project DIR] [--dry-run] [--json]
drove hooks update    --vendor claude|codex --scope user|project [--project DIR] [--dry-run] [--json]
drove hooks uninstall --vendor claude|codex --scope user|project [--project DIR] [--dry-run] [--json]
drove hooks status    --vendor claude|codex|all [--scope user|project] [--project DIR] [--check] [--json]
```

- `install`：显式且幂等。添加缺失节点，精确匹配的自有节点和外部节点保持不变。发生所有权冲突时拒绝操作。
- `update`：仅替换精确匹配且记录为自有的节点。发生变化的节点属于冲突，不是更新对象。
- `uninstall`：精确删除匹配的自有节点和所有权记录。
- `status`：只读。状态为 `invalid`、`partial`、`drifted`、`unsupported`、被策略阻止或已知为 `untrusted` 时，`--check` 以非零状态退出。信任状态为 `unknown` 时应发出单独警告，不能静默视为已信任。
- `--dry-run`：解析并显示结构化差异，不创建备份，不写入目标或清单，也不更改信任状态。
- `--json`：输出稳定且带版本的结果，其中包含每个状态维度和证据路径，但不包含提示文本、令牌或原始钩子载荷。

首个版本不要提供通用的 `--force` 修改路径。恢复操作应明确：修复文件、重新安装，或仅丢弃过时的清单。

## 版本与能力处理

1. 如果 `claude --version` 或 `codex --version` 可用，则执行探测，但绝不能只根据版本判断是否支持。
2. 按 Drove 将要修改的字段解析现有文件。必须保留节点周围的未知字段。自有节点内出现未知字段时，应将该节点标记为外部或已漂移。
3. 为事件名称、处理器字段、信任探测和表示形式维护能力表。对于未知版本，使用保守的公共子集：同步 `command`、字符串命令、匹配器组，并且不自动处理信任。
4. 写入后，重新解析实际写入的字节，并验证每个预期节点。如果厂商提供有文档支持的配置诊断，可以选择执行。
5. 除非适配器使用了厂商新增的事件，否则无需重写现有安装。事件被删除或拒绝时，应将安装标记为 `unsupported`，不能视为部分成功。

无法从官方发布记录中确定当前 Claude `args` 形式、Codex `hooks.json` 和 Codex `hooks/list` 的已验证最低版本。这些能力需要运行时探测或保守回退。

## 测试矩阵

| 范围 | 必测用例 |
| --- | --- |
| 作用域与路径 | 用户和项目；自定义 `CLAUDE_CONFIG_DIR`；自定义 `CODEX_HOME`；主目录缺失；规范路径 |
| 解析 | 文件为空或缺失；有效的 JSON/TOML；格式错误的输入；重复键；未知字段；符号链接 |
| 合并 | 没有钩子；外部事件；相同事件和匹配器；多个组；完全相同的外部重复节点；自有节点与外部节点混合 |
| 幂等性 | 第一次修改后，连续执行两次安装和两次更新所得字节完全相同；不创建第二份备份 |
| 漂移 | 命令、匹配器、超时、顺序或自有节点的额外字段发生变化；写入期间目标发生变化 |
| 卸载 | 仅删除精确匹配的自有节点；共享数组；清理空容器；已创建的文件；安装后的用户修改 |
| 失败 | 权限不足；磁盘已满；重命名失败；清单写入失败；重命名前后崩溃；`pending` 记录恢复；锁竞争 |
| 信任与状态 | Claude 信任状态为 `unknown` 和策略阻止；Codex 的四种信任状态；可选探测不可用；会话状态为 `observed` 或 `stale` |
| 兼容性 | 受支持、旧版、未知和格式错误的版本输出；选择 JSON/TOML 表示形式 |
| 安全 | 路径含 shell 元字符；恶意配置值；受限的备份权限模式；输出不含秘密 |
| 端到端 | 全新安装；用户完成厂商信任；Drove 会话观测到 `SessionStart`；适用时更新后需要重新信任；精确卸载 |

合并结果使用 golden 文件测试。每个原子写入阶段使用故障注入，并发命令使用竞态测试，同时使用真实的临时 HOME 和项目目录。

## 风险与待确认问题

1. 通过裸命令名安装 `drove` 依赖厂商进程的 `PATH`。绝对可执行文件路径更可靠，但升级后可能变化，也会在项目配置中暴露当前机器的路径。必须在编写规格前确定方案。
2. 项目作用域的所有权清单存放在本地。共享的项目设置文件可以在不包含对应清单的情况下提交。其他机器必须将其中的节点标记为 `foreign`，直至显式接管。
3. 定义发生变化时，Codex 会重写信任哈希。更新自有钩子后，应有意将其保留为 `untrusted` 或 `modified`，交由用户审查。
4. Claude 没有提供有文档支持的非交互式工作区信任查询。除非官方提供 API，否则静态信任状态将保持为 `unknown`。
5. 厂商的确切最低版本和 Codex `hooks/list` 的稳定性尚未验证。
6. 修改配置期间，厂商进程可能重新加载配置。原子替换可以避免读取不完整文件，但无法保证运行中的会话何时采用新定义。

## 实施顺序

1. 为 `drove hook` 添加无行为的 `--managed-by drove/v1` 标记，并保持中继行为不变。
2. 定义通用的计划、结果、状态类型和所有权清单模式。所有 Claude/Codex 文件知识都应保留在 `internal/adapter` 中。
3. 实现严格读取器和纯结构化合并、反向合并规划器，并添加 golden 测试。
4. 实现锁、备份、摘要检查、目标文件原子写入和清单原子写入，并注入文件系统故障进行测试。
5. 添加 Claude 用户和项目作用域的安装、更新、卸载及静态状态查询。
6. 添加 Codex 表示形式选择和 JSON/TOML 支持。
7. 添加可选且受版本限制的厂商诊断与 Codex 信任探测。精确卸载不得依赖这些能力。
8. 添加信任与已观测信号的端到端测试，然后在 CLI 输出中说明由厂商控制的信任步骤。

该顺序先建立精确的所有权和崩溃安全的修改机制，再开放能够更改用户配置的命令。
