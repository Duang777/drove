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
  执行；Windows 的锁 key 必须折叠路径大小写，缺失目录的大小写别名也不能取得不同锁。
  workspace 记录先写入同目录临时文件并 fsync，原子安装最终文件后再 fsync
  父目录。
- Prepare 全程固定已打开的源仓库根目录；仓库识别、分支查询与创建、include 匹配和
  `git worktree add` 都必须从该根句柄执行，并禁用 Git hooks。Linux 使用
  `/proc/self/fd`，其他 Unix 通过继承目录描述符后 `fchdir`，Windows 持有不允许
  share-delete 的目录句柄并复核目录身份；不能提供等价约束的平台必须 fail-stop。
  macOS/BSD 上只依赖私有 Git directory 的查询必须从该目录句柄执行，禁止经由使用
  公开私有 Git 路径的 worktree helper；同时依赖 index 与 worktree 的查询必须由两个
  各自固定一侧的视角交叉验证。
  `git worktree add` 使用 `--no-checkout`，只接收 bucket 内随机且与 Agent ID 同前缀的
  staging 目录，禁止接触公开 Agent 路径。新 worktree 打开后先核对预先持久化的
  文件系统目录身份，再持久化 Git 私有目录。Linux 和 Windows 从同时固定的目标根与
  Git 根执行 `read-tree --reset -u`；macOS/BSD 只从固定私有 Git 根更新 index，
  用 `checkout-index --temp` 导出，并由 Drove 通过固定目标根安装普通文件、符号链接
  和 gitlink。macOS/BSD 遇到普通文件的 `filter` 属性必须 fail-stop，因为 filter
  无法同时取得固定 worktree cwd 与描述符绑定的私有 Git 上下文。初始化完成后把
  staging 目录 no-replace 原子提升到公开路径，再从固定 worktree 根执行
  `git worktree repair .`。仓库根目录的
  `.worktreeinclude` 使用 gitignore 语义，并从已打开文件读取一次。Unix 通过继承的
  只读文件描述符交给 Git，其余平台通过受复核的私有临时副本交给 Git，禁止 Git 再按
  源 manifest 路径打开。
  只有该文件匹配的未跟踪文件会复制到新 worktree，普通未跟踪文件不会复制；匹配项
  必须是普通文件，symlink 一律拒绝。创建时选中的规范相对路径保存在 version 4+
  sidecar 中；复制入口和出口都必须把重新打开的目标目录与 sidecar 目录身份比对，
  目标分支已跟踪的候选项不复制，sidecar 最终只保留实际复制路径；后续 dirty 检查
  不得重新解释目标 worktree 的 manifest。目标父目录必须从已固定 worktree root
  向下创建，不能从 repository bucket 重新解析可替换的公开路径。
- `Prepare` 在创建新分支和执行 `git worktree add` 前持久化未提交的 preparation
  sidecar，并在受约束 repository bucket 内预创建随机 staging 目录、固定目录身份、同步父目录
  后把身份写回 sidecar。每次 preparation 都有唯一 operation ID，用于拒绝另一 Manager
  的冲突清理。
  version 5 sidecar 记录源 worktree、源私有 Git directory 和 common Git directory
  的规范路径及目录实例身份。重启恢复必须先重开并逐项匹配这些证据，再取得 rooted
  repository capability；源路径、linked-worktree `.git` 指针或 common Git directory
  被替换时，在执行清理前 fail-stop。capability 的 Git 命令以已打开的 common Git
  directory 为执行目录，不再从源路径重新发现 `.git`；执行前清除继承的 `GIT_*`
  环境变量，再显式设置受约束的 `GIT_DIR` 与 `GIT_WORK_TREE`。include 规则的
  `ls-files` 必须先从已打开的私有 Git directory 读取 linked-worktree index，再在
  私有 Git 路径身份仍匹配时从已打开的源 worktree root 复核；两次输出必须一致，并用
  top-level pathspec 覆盖整个 worktree。Git 返回的候选文件名再次用于 tracked 检查时
  必须禁用 pathspec 解释。
  version 4 只有源 worktree 与 common Git directory 证据；pending version 3/4 记录可
  读取但不能授权基于路径的回滚或 ownership marker 清理；已提交的 version 1-4 记录可
  直接兼容采纳，不执行缺少完整 identity evidence 的 Git 清理。
  新分支与私有 ownership ref 通过同一 `git update-ref --stdin` transaction 创建，并用
  含 operation ID 的 reflog subject 标记 ref 世代；回滚先让 `git update-ref` prepare
  并锁定 branch/marker refs，再在锁内校验最新 reflog subject，只有 marker、OID 和
  reflog 世代同时匹配才提交删除，避免外部分支删除后同 OID 重建形成 ABA。session
  创建事件 durable 后必须调用 `AcknowledgePreparation`；
  `ReconcilePreparations` 在重启时只采纳与 session 私有 metadata 完全匹配的 pending
  preparation，其余工作区及本次新建分支全部回滚；匹配但已带 removal intent 的记录只
  标记存在，交给后续 `ReconcileRemovals` 收敛。version 1/2 sidecar 兼容视为已提交。
  已提交的 version 5 sidecar 在重启采纳时仍必须重开 repository lease 并复核目标
  worktree 身份；只有仍持有进程内 preparation lease 的幂等确认允许快速返回。
  `AcknowledgePreparation` 在提交 sidecar 或清理 ownership ref 前必须重新验证当前目标
  目录身份、Git registration、私有 Git directory，以及源 worktree、源私有 Git
  directory 和 common Git directory 的公开路径仍绑定 preparation lease 固定的目录
  实例；源 worktree 的 `.git` 指针还必须重新解析到固定的私有 Git directory 和
  common Git directory，两个目录必须由同一次 rooted Git 查询取得，输出只接受唯一一对
  可打开的绝对目录，不能按固定行数切分或跨进程拼接。提交 sidecar 后、清理 ownership ref
  前后及最终记录更新后都要重新复核目标与仓库，不能只信 session 携带
  的历史值。
- `List` 只枚举 Drove 根目录下符合路径约定的 worktree，并从 Git 查询仓库、分支和
  dirty 状态。每个成功创建的 worktree 都有同目录私有记录，用于识别 detached HEAD
  和修复目录已丢失但 Git 注册仍存在的情况。路径存在时必须同时匹配记录中的 Git
  私有目录和文件系统目录身份，复制或替换同名目录必须 fail-stop。
- `Remove` 默认拒绝 dirty、detached HEAD 和保护来源未知的 version 1 sidecar；
  调用方必须先停止所有可通过 worktree 路径或既有文件/目录句柄写入的进程；session
  层在 Agent 为 Done/Stopped、无 attached/resuming session 且持有 cleanup reservation
  时才调用。成功删除以绑定目录身份的 canonical 到 quarantine 原子 rename 为逻辑
  线性化点；非强制 dirty 检查只保护该点前可观察到的改动，因为各平台都无法可移植地
  撤销 rename 前已被其他进程持有的 cwd、dirfd 或可写文件句柄。
  `force` 可显式放宽这些检查。删除前必须先原子持久化带 operation ID 的 removal
  intent。物理删除完成后保留 sidecar，直到 session tombstone durable 后由
  `AcknowledgeRemoval` 校验 token 并删除。`ReconcileRemovals` 在重启时收敛 path 与
  Git registration 的四种组合。非强制 intent 每次继续前都重新检查；路径存在但
  registration 丢失，或路径丢失但 registration 为 detached 时保留 intent 并
  fail-stop。显式 force 会原子升级已有的非强制 intent 并保留 operation ID；每个新
  worktree 记录同时持久化 Git 私有目录路径与文件系统目录实例身份，每个 removal intent
  另有随机目录 token，隔离前写入 worktree 并随原目录移动。物理路径存在时，创建
  removal intent 前必须取得并校验两种身份；任何已开始或待恢复的删除只要缺少任一身份
  或目录 token 都必须 fail-stop。调用任何物理删除前先把已检查的 workspace 原子移动到
  operation ID 隔离名；非强制删除必须在隔离后再次验证身份、marker 和 dirty 状态，
  发现 dirty 时恢复原路径并拒绝删除。复核通过后才持久化 `quarantined + started`；
  此后只删除隔离名，失败必须保留 intent 并在重启后继续，不能再因 dirty 状态回滚。
  每次恢复和递归删除隔离目录前都必须复核 Git 身份、目录
  token 与已打开句柄；`Started` 不代表隔离路径永久可信，且 Started 状态只允许验证
  既有 token，禁止为当前路径重新创建 marker。新 intent 会记录创建时原路径是否已缺失；此后同名路径出现
  时必须 fail-stop，禁止把替代目录当成旧 workspace 删除。version 2 的历史 removal
  intent 兼容视为已开始。version 5 removal 的 registration 查询与 prune 必须通过
  sidecar repository evidence 重开的 capability；拒绝未开始的 removal 时必须先验证并
  持久删除目录 marker，再清除 intent。ack sidecar 隔离名在正式记录缺失时必须按
  Agent ID 恢复并回读 operation ID，使 session 重试和重启 reconciliation 能继续；
  同 operation 且 `os.SameFile` 的多个 ack 名必须折叠，任一不同身份仍 fail-stop；
  正式记录已存在时不得恢复旧 ack。对 version 1-4 记录发起新删除时，必须在写 intent
  前取得并持久化完整的 version 5 repository evidence；已经存在但缺少该证据的 removal
  intent 只能 fail-stop，禁止在恢复时重新信任记录中的公开仓库路径。清理始终保留分支。
- Manager 创建不预先查找 Git，也不创建目录；只有实际查询或变更 worktree 时才解析并
  执行 `git`，因此没有受管 workspace 的 daemon 可在未安装 Git 时启动。缺失的
  DataDir 必须从已打开的卷根通过 `os.Root` 逐级创建，禁止 `MkdirAll` 沿可替换
  的公开路径创建。
- Manager 初始化时固定既有 data directory、worktrees root 和 repository bucket 的
  文件身份，新建目录则在首次打开时固定。后续通过 `os.Root` 逐级打开并持续复核；
  任一中间实目录或 symlink 被替换时必须 fail-stop。List 与 record scan 全程持有固定
  root/bucket 句柄。sidecar 的原子写、确认读取和删除必须在受约束 bucket 句柄内完成。
  初次 sidecar 安装必须使用 no-replace 原语；removal acknowledgement 先把匹配 token 的
  空 marker 目录从私有 staging 名 no-replace 提升到 canonical Agent 路径，持有该
  namespace reservation 复核 quarantine 与 Git registration 均缺失，再把匹配 token 的
  sidecar 原子移动到 operation ID 隔离名，校验后再移动到同格式的新随机私有名并按已打开
  文件身份删除；sidecar 进入隔离名后才把 reservation 原子移回私有名并删除，崩溃后从任一
  隔离名或 canonical marker reservation 恢复。List 与 removal reconciliation 必须把
  匹配 sidecar token 的 reservation 视为缺失 worktree，其他同名对象一律 fail-stop。
  sidecar 与确认隔离名均缺失时必须 fail-closed；
  只有当前 Manager 已成功删除同一 Agent ID 与 operation ID 的 sidecar 后，进程内重复确认
  才可幂等成功。
  sidecar 安装在原子改名前后都要确认已打开的 repository bucket 仍位于规范 hash 路径，
  文件改名先把已校验源链接到内部随机别名，再从别名安装最终目标；公开临时路径被替换
  只能导致失败，不得覆盖最终记录。清理临时名时也必须确认它仍指向已打开文件。缺少
  对应原子原语的平台必须返回错误。空 repository bucket 在创建后永久保留，禁止按公开
  名称删除并清除已固定的目录身份。
- `Discard` 只供创建事务在会话元数据持久化前回滚；它会删除本次新建的 worktree 和
  本次新建的分支。普通或重启后的 Discard 遇到路径存在但 preparation sidecar 尚未持久化
  完整 Git 身份时必须 fail-stop；同一次 Prepare 仍持有原始仓库根句柄时，如果 sidecar
  已有预创建目标的目录身份但尚无 Git 私有目录，可验证公开 worktree registration 的
  路径、分支和预期 HEAD 后通过固定目录句柄回滚。不得扫描或推测 Git 私有 worktree
  管理目录名；路径删除后由 `git worktree prune --expire now` 收敛任意私有后缀。
  重启后的 Discard 和 AcknowledgePreparation 必须使用 version 5 仓库证据重开 capability，
  禁止退回 `git -C <recorded-path>`。
  禁止把同名替代目录当作失败创建的残留删除。未注册残留目录通过已验证的 `os.Root`
  相对操作删除，任一中间 symlink 或目录替换都会使回滚失败。Remove 与 Discard 均先通过 `os.Root` 删除物理
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
