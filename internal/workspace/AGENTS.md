# AGENTS.md — internal/workspace

## 职责

**受管 Git worktree 生命周期**：为一个 Agent 创建独立 worktree，复制显式声明的本地
文件，枚举 Drove 管理的 worktree，并执行受保护的显式清理。

## 关键设计

- 默认路径是 `$DataDir/worktrees/<repo-hash>/<agent-id>`；repo hash 由规范化仓库根目录
  派生，Agent ID 必须是规范 UUID。
- `Prepare` 使用 `git worktree add` 创建或复用本地分支。未指定分支时使用
  `drove/<agent-id>`。
- 同一 Manager 的 Prepare、List、Remove、ReconcileRemovals 和 Discard 串行执行。
  workspace 记录先写入同目录临时文件并 fsync，原子安装最终文件后再 fsync 父目录。
- 仓库根目录的 `.worktreeinclude` 使用 gitignore 语义；只有该文件匹配的未跟踪文件会
  复制到新 worktree，普通未跟踪文件不会复制。创建时选中的规范相对路径保存在
  version 2 sidecar 中，后续 dirty 检查不得重新解释目标 worktree 的 manifest。
- `List` 只枚举 Drove 根目录下符合路径约定的 worktree，并从 Git 查询仓库、分支和
  dirty 状态。每个成功创建的 worktree 都有同目录私有记录，用于识别 detached HEAD
  和修复目录已丢失但 Git 注册仍存在的情况。
- `Remove` 默认拒绝 dirty、detached HEAD 和保护来源未知的 version 1 sidecar；
  `force` 可显式放宽这些检查。删除前必须先原子持久化带 operation ID 的 removal
  intent。物理删除完成后保留 sidecar，直到 session tombstone durable 后由
  `AcknowledgeRemoval` 校验 token 并删除。`ReconcileRemovals` 在重启时收敛 path 与
  Git registration 的四种组合。非强制 intent 每次继续前都重新检查；路径存在但
  registration 丢失，或路径丢失但 registration 为 detached 时保留 intent 并
  fail-stop。清理始终保留分支。
- Manager 创建不预先查找 Git；只有实际查询或变更 worktree 时才解析并执行 `git`，
  因此没有受管 workspace 的 daemon 可在未安装 Git 时启动。
- `Discard` 只供创建事务在会话元数据持久化前回滚；它会删除本次新建的 worktree 和
  本次新建的分支。未注册残留目录通过已验证的 `os.Root` 相对操作删除，任一中间
  symlink 或目录替换都会使回滚失败。
- `.worktreeinclude` 匹配文件不受 Git 跟踪；只要 worktree 中存在创建时记录的路径，
  `List` 就保守报告 dirty，清理需要显式 `force`。非强制 Remove 在 Git 删除前再次检查，
  防止首次枚举后出现的 include 文件被当作 ignored 内容删除。

## 约束

- 禁止直接读取或修改 `.git` 内部结构；仓库事实必须通过 Git 命令查询。
- 删除路径必须先证明位于 Drove worktree 根目录内，禁止接受任意路径。
- 不自动 merge、rebase、push 或删除已交付会话的分支。
- 导出类型：`Manager`、`Workspace`、`Removal`、`RemovalState`、`RemovalResult`、
  `ErrNotRepository`、`ErrInvalidBranch`、`ErrDirty`、`ErrNotFound`。
