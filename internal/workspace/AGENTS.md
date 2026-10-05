# AGENTS.md — internal/workspace

## 职责

**受管 Git worktree 生命周期**：为一个 Agent 创建独立 worktree，复制显式声明的本地
文件，枚举 Drove 管理的 worktree，并执行受保护的显式清理。

## 关键设计

- 默认路径是 `$DataDir/worktrees/<repo-hash>/<agent-id>`；repo hash 由规范化仓库根目录
  派生，Agent ID 必须是规范 UUID。
- `Prepare` 使用 `git worktree add` 创建或复用本地分支。未指定分支时使用
  `drove/<agent-id>`。
- 指向同一规范 DataDir 的所有进程内 Manager 共享互斥锁；Prepare、List、Remove、
  ReconcilePreparations、AcknowledgePreparation、ReconcileRemovals 和 Discard 串行
  执行。workspace 记录先写入同目录临时文件并 fsync，原子安装最终文件后再 fsync
  父目录。
- Prepare 全程固定已打开的源仓库根目录；仓库根目录的 `.worktreeinclude` 使用
  gitignore 语义，并从已打开文件读取一次。Unix 通过继承的只读文件描述符交给 Git，
  其余平台通过受复核的私有临时副本交给 Git，禁止 Git 再按源 manifest 路径打开。
  只有该文件匹配的未跟踪文件会复制到新 worktree，普通未跟踪文件不会复制；匹配项
  必须是普通文件，symlink 一律拒绝。创建时选中的规范相对路径保存在 version 3
  sidecar 中，后续 dirty 检查不得重新解释目标 worktree 的 manifest。
- `Prepare` 在创建新分支和执行 `git worktree add` 前持久化未提交的 preparation
  sidecar，每次 preparation 都有唯一 operation ID，用于拒绝另一 Manager 的冲突清理。
  新分支与私有 ownership ref 通过同一 `git update-ref --stdin` transaction 创建，并用
  含 operation ID 的 reflog subject 标记 ref 世代；回滚先让 `git update-ref` prepare
  并锁定 branch/marker refs，再在锁内校验最新 reflog subject，只有 marker、OID 和
  reflog 世代同时匹配才提交删除，避免外部分支删除后同 OID 重建形成 ABA。session
  创建事件 durable 后必须调用 `AcknowledgePreparation`；
  `ReconcilePreparations` 在重启时只采纳与 session 私有 metadata 完全匹配的 pending
  preparation，其余工作区及本次新建分支全部回滚。version 1/2 sidecar 兼容视为已提交。
- `List` 只枚举 Drove 根目录下符合路径约定的 worktree，并从 Git 查询仓库、分支和
  dirty 状态。每个成功创建的 worktree 都有同目录私有记录，用于识别 detached HEAD
  和修复目录已丢失但 Git 注册仍存在的情况。
- `Remove` 默认拒绝 dirty、detached HEAD 和保护来源未知的 version 1 sidecar；
  `force` 可显式放宽这些检查。删除前必须先原子持久化带 operation ID 的 removal
  intent。物理删除完成后保留 sidecar，直到 session tombstone durable 后由
  `AcknowledgeRemoval` 校验 token 并删除。`ReconcileRemovals` 在重启时收敛 path 与
  Git registration 的四种组合。非强制 intent 每次继续前都重新检查；路径存在但
  registration 丢失，或路径丢失但 registration 为 detached 时保留 intent 并
  fail-stop。显式 force 会原子升级已有的非强制 intent 并保留 operation ID；每个新
  worktree 记录同时持久化 Git 私有目录路径与文件系统目录实例身份，每个 removal intent
  另有随机目录 token，隔离前写入 worktree 并随原目录移动。调用任何
  物理删除前先把已检查的 workspace 原子移动到 operation ID 隔离名，再持久化
  `quarantined + started`；之后只删除隔离名，失败必须保留 intent 并在重启后继续，
  不能再因 dirty 状态回滚。每次恢复和递归删除隔离目录前都必须复核 Git 身份、目录
  token 与已打开句柄；`Started` 不代表隔离路径永久可信，且 Started 状态只允许验证
  既有 token，禁止为当前路径重新创建 marker。新 intent 会记录创建时原路径是否已缺失；此后同名路径出现
  时必须 fail-stop，禁止把替代目录当成旧 workspace 删除。version 2 的历史 removal
  intent 兼容视为已开始。清理始终保留分支。
- Manager 创建不预先查找 Git；只有实际查询或变更 worktree 时才解析并执行 `git`，
  因此没有受管 workspace 的 daemon 可在未安装 Git 时启动。
- Manager 初始化时固定既有 data directory、worktrees root 和 repository bucket 的
  文件身份，新建目录则在首次打开时固定。后续通过 `os.Root` 逐级打开并持续复核；
  任一中间实目录或 symlink 被替换时必须 fail-stop。List 与 record scan 全程持有固定
  root/bucket 句柄。sidecar 的原子写、确认读取和删除必须在受约束 bucket 句柄内完成。
  初次 sidecar 安装必须使用 no-replace 原语；removal acknowledgement 先把匹配 token 的
  sidecar 原子移动到 operation ID 隔离名，再校验并删除，崩溃后从隔离名恢复。
  sidecar 安装在原子改名前后都要确认已打开的 repository bucket 仍位于规范 hash 路径，
  文件改名先把已校验源链接到内部随机别名，再从别名安装最终目标；公开临时路径被替换
  只能导致失败，不得覆盖最终记录。清理临时名时也必须确认它仍指向已打开文件。缺少
  对应原子原语的平台必须返回错误。
- `Discard` 只供创建事务在会话元数据持久化前回滚；它会删除本次新建的 worktree 和
  本次新建的分支。未注册残留目录通过已验证的 `os.Root` 相对操作删除，任一中间
  symlink 或目录替换都会使回滚失败。Remove 与 Discard 均先通过 `os.Root` 删除物理
  目录，再仅调用 `git worktree prune --expire now` 清理 stale registration；禁止把受管
  路径交给 Git 执行删除。
- `.worktreeinclude` 匹配文件不受 Git 跟踪；只要 worktree 中存在创建时记录的路径，
  `List` 就保守报告 dirty，清理需要显式 `force`。非强制 Remove 在 Git 删除前再次检查，
  防止首次枚举后出现的 include 文件被当作 ignored 内容删除。
  include 复制前后必须确认已打开源文件的大小、模式和修改时间未变化；临时文件创建、
  原子安装与目录同步都在实际目标父目录句柄内完成，新建的每一级父目录也同步其父目录。

## 约束

- 禁止直接读取或修改 `.git` 内部结构；仓库事实必须通过 Git 命令查询。
- 删除路径必须先证明位于 Drove worktree 根目录内，禁止接受任意路径。
- 不自动 merge、rebase、push 或删除已交付会话的分支。
- 导出类型：`Manager`、`Workspace`、`Removal`、`RemovalState`、`RemovalResult`、
  `ErrNotRepository`、`ErrInvalidBranch`、`ErrDirty`、`ErrNotFound`。
