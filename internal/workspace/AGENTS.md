# AGENTS.md — internal/workspace

## 职责

**受管 Git worktree 生命周期**：为一个 Agent 创建独立 worktree，复制显式声明的本地
文件，枚举 Drove 管理的 worktree，并执行受保护的显式清理。

## 关键设计

- 默认路径是 `$DataDir/worktrees/<repo-hash>/<agent-id>`；repo hash 由规范化仓库根目录
  派生，Agent ID 必须是规范 UUID。
- `Prepare` 使用 `git worktree add` 创建或复用本地分支。未指定分支时使用
  `drove/<agent-id>`。
- 仓库根目录的 `.worktreeinclude` 使用 gitignore 语义；只有该文件匹配的未跟踪文件会
  复制到新 worktree，普通未跟踪文件不会复制。
- `List` 只枚举 Drove 根目录下符合路径约定的 worktree，并从 Git 查询仓库、分支和
  dirty 状态。每个成功创建的 worktree 都有同目录私有记录，用于识别 detached HEAD
  和修复目录已丢失但 Git 注册仍存在的情况。
- `Cleanup` 默认拒绝删除 dirty worktree；`force` 只放宽 dirty 检查。清理 worktree
  后保留分支，由用户自行合并或删除。
- `Discard` 只供创建事务在会话元数据持久化前回滚；它会删除本次新建的 worktree 和
  本次新建的分支。
- `.worktreeinclude` 匹配文件不受 Git 跟踪；只要 worktree 中存在匹配文件，`List`
  就保守报告 dirty，清理需要显式 `force`。

## 约束

- 禁止直接读取或修改 `.git` 内部结构；仓库事实必须通过 Git 命令查询。
- 删除路径必须先证明位于 Drove worktree 根目录内，禁止接受任意路径。
- 不自动 merge、rebase、push 或删除已交付会话的分支。
- 导出类型：`Manager`、`Workspace`、`ErrNotRepository`、`ErrInvalidBranch`、
  `ErrDirty`、`ErrNotFound`。
