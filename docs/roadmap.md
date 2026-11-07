# weftsync 开发路线图

> **这份文件的存在就是为了「不遗忘」。**
> 用户 2026-11-06：「后续是否依次做完，你不会忘了吧」
>
> 规矩：每完成一个 feature，**先更新本文件的「状态」列**，再开始下一个。
> 任何 feature 都不允许从本表消失，只允许改状态。
>
> 相关：需求 `PLAN.md` §1（R1–R18）、决策 `docs/decisions.md`、准则 `.specify/memory/constitution.md`

---

## 状态图例

| 标记 | 含义 |
|---|---|
| ✅ | 已完成并验证 |
| 🔄 | 进行中 |
| ⬜ | 未开始 |
| ⏸ | 受阻（写明阻塞原因） |
| ❌ | 已放弃（写明理由） |

---

## 总览

| # | Feature | 对应需求 | 里程碑 | 依赖 | 状态 |
|---|---|---|---|---|---|
| F1 | 飞书后端：只读 + 小文件上传 | R1, R2, R9, R10, R16 | M1–M2 | 无 | 🔄 61/75 任务；state 已实现并实测，剩 Linux 实机验证 |
| F2 | 分片上传 + 限流 + 重试 | R2, R9 | M3 | F1 | 🔄 分片与限速已完成并实测；续传未做 |
| F3 | 删除 / 更新 / 移动 + 删除保护 | R3, R15, A11, A13 | M4 | F1, F2 | 🔄 闸门/versioning/Move/重复副本清理已完成并实测；通知待 F5 |
| F4 | 状态库 + 增量判断 + 冲突检测 | R4, R16, A5 | M4.5 | F3 | ⬜ |
| F5 | 常驻进程 + 定时调度 + 托盘 + 开机自启 | R11, R12, R14, A15–A17 | M5 | F4 | ⬜ |
| F6 | 本地网页 UI（12 个功能） | R13, R14, R17, R18, A24 | M5.5 | F5 | ⬜ |
| F7 | 超 1500 节点拆分向导 | A22, A23 | M6 | F6 | ⬜ |
| F8 | Linux 平台验证 | R9 | M6.5 | F1–F6 | ⬜ |
| F9 | 双向同步 | R5 | M7 | F4 | ⏸ 待评估 |
| F10 | 多网盘（union / 多 pair） | R6, R7 | M8 | F4 | ⬜ 可选 |

---

## 每个 Feature 的验收标准

> 「完成」的定义写在这里。达不到就不许标 ✅（宪法原则 II：验证优先于断言）。

### F1 · 飞书后端：只读 + 小文件上传 🔄

**范围**
- rclone 后端注册（`feishu`），`weftsync help backends` 可见
- `NewFs` / `List` / `NewObject` / `Open`（只读）
- `Put`（≤20MB 走 `upload_all`）
- `Mkdir`（`create_folder`）
- 凭据从 `WEFTSYNC_FEISHU_*` 环境变量读取（first cut，正式凭据存储留给 F5/F6）

**验收标准（全部可执行验证）**
1. `weftsync lsf feishu:` 能列出授权文件夹的内容
2. `weftsync copy feishu:<路径> ./local` 能把文件拉回本地，字节内容与云端一致
3. `weftsync copy ./local feishu:<路径>` 能上传 ≤20MB 文件，飞书客户端可见
4. `weftsync lsd feishu:` 能列出子目录
5. 空目录能建（`weftsync mkdir feishu:<新目录>`）
6. 单测覆盖：`List` 解析、路径↔token 映射、错误码分类
7. `Size()` 返回真实值或 -1 的行为已记录（取决于最终策略）
8. 编译产物：Windows + Linux 各一个（`scripts/build-go.ps1 -Target both`）

**实测结果（2026-11-06，真实租户 `_bot_test`）**

| # | 验收项 | 结果 | 证据 |
|---|---|---|---|
| 1 | `lsf` 列目录 | ✅ | 空目录退出 0；有文件时正确列出 |
| 2 | 下载并比对 SHA-256 | ✅ **SC-002** | `AA5B2942...` 两侧完全一致 |
| 3 | 上传后可见 | ✅ | `lsf` 可见，回读内容为 `MODIFIED content v2` |
| 4 | `lsd` 列目录 | ✅ | 输出 `-1 2026-11-06 16:07:33 -1 go-testdir` |
| 5 | `mkdir` 建目录 | ✅ | 建后 `lsd` 可见 |
| 6 | 单测 | ✅ | 20+ 用例全绿，`go vet` 通过 |
| 7 | `Size() = -1` | ✅ 已记录 | research R6 |
| 8 | 双平台产物 | ✅ | Windows 66.2MB + Linux 66.1MB |

**额外验证（spec 里没有但实际跑过）**

| 场景 | 结果 |
|---|---|
| 递归上传含空目录（`--create-empty-src-dirs`） | ✅ 结构完整重建 |
| **20MB 边界**：19.5MB 成功 | ✅ |
| **20MB 边界**：21MB 拒绝，信息含 "20MB" | ✅ **SC-004** |
| **重复运行不产生同名重复**（连跑 2 次，条目数不变） | ⚠️ **当时是错判** | 用 `lsf` 量的，而 `lsf` 会折叠同名。见 D24 |
| **修改文件后仍只有 1 条记录，内容已更新** | ⚠️ **当时是错判** | 同上；实际每次替换都留了旧副本 |
| 新建目录树一次成功（自动建父目录，无重试） | ✅ FR-012 |
| **替换后无同名重复**（原始 API 列表统计） | ✅ **SC-009 / SC-010** | 4 次连续替换（直传 + 分片），`duplicate-names=0` |
| **第二轮零上传**（state 生效） | ✅ `new=0 replaced=0 unchanged=3` |
| **改一个文件只传一个** | ✅ `replaced=1 unchanged=2` |
| **mtime 不变、size 变也能识别** | ✅ `replaced=1` |
| **删掉 state 后降级：全重传且不删除** | ✅ `replaced=3`，云端条目只增不减 **SC-011** |
| **state 主文件损坏 → 从 `.bak` 恢复并告警** | ✅ |
| `purge remote:path` 真正删净（含空目录） | ✅ |

**实现过程中发现并修正的问题**
1. rclone 用 `Mkdir("")` 表示「创建 Fs 的 root」→ 原实现直接返回 nil，导致 `mkdir` 静默无效
2. `resolveDir` 忽略了 `rootPath`，把「Fs 根」当成 base folder
3. rclone 把 `remote:dir/file.txt` 当作 root → 需要 `NewFs` 检测并返回父目录 + `ErrorIsFile`
4. 并发列目录惊群：16 个 goroutine 各列一次 → 加 in-flight 合并
5. `fs.ErrorCantUpdate` 不存在（research R8 已修正为「实现 Update，先传后删」）
6. **缓存填充覆盖并发 invalidate**：列表请求在 `createFolder` 之前发出，回来时把陈旧快照写回缓存，
   下一个 `resolveDir` 又报「目录不存在」→ 加入**代际计数**（请求开始时快照 `gen`，返回时 `gen` 已变则不入缓存）
7. **`isNotFound` 后的重试自相矛盾**：失效后立即重查会 join 到同一个陈旧 inflight 请求 → 新增 `listFresh` 绕过缓存与 inflight
8. **`Rmdir` 重复拼 `f.root`**：路径变成 `_inc_test/_inc_test`，子目录永远「directory not found」
9. **`Rmdir` 返回 `ErrorCantRmdir` 让 `purge` 变成 exit=0 的静默 no-op** → 改为「只删空目录」（D18）
10. **错误码三处清单不一致**：`1061001` 加进了 `Retryable()` 却不在 `businessError` 的构造清单里，
    于是永远变成 `UncategorizedError`、永远不会重试 → 合并为单一 `codeTable` + 一致性测试（D20）
11. **并发写 state 抢同一个 `.tmp`**：Windows 上 rename 报「文件被另一进程占用」→ 加 `stateMu` 串行化
12. **`state.New()` 不带路径**：`Save` 报「cannot save a snapshot with no path」→ 新增 `SetPath`

**尚未实现（因此本 feature 未标 ✅）**

| 缺口 | 对应任务 | 影响 |
|---|---|---|
| 下载相关单测（范围请求 / 404） | T044 | 功能已验证，缺回归测试 |
| 上传 20MB 边界的单测 | T063 | 已手工实测，缺单测 |
| 契约清单逐条对齐 | T068–T070 | 部分已完成 |
| **Linux 实机跑验收** | T073 | 只验证了产物生成，未在 Linux 运行 |
| 路线图更新 | T075 | 本次更新 |

> **state 已完成**（2026-11-06）：从「每轮全量重传」变为「第二轮零上传」，实测 7 轮见 `specs/001-feishu-read-upload/quickstart.md` §9。
> 降级路径（state 丢失/损坏）会全量重传并产生同名副本，**weftsync 不会自动删这些副本**（D19），
> 清理手段归入 F3。

**不做（留给后续 feature）**
- 分片上传（F2）
- 移动（F3）
- 冲突检测（F4）
- UI、定时（F5/F6）

**关键设计决策（来自 PLAN.md / decisions.md）**
- 不 fork rclone，依赖库方式（D11）
- `DuplicateFiles = true`（E2 实测同名可共存）
- `Update()` 先上传后删旧（E1 实测无原地更新；`fs.ErrorCantUpdate` 不存在，见 D13）
- **增量靠「影子 mtime」**：`Object.ModTime()`/`Size()` 在 token 匹配时返回上次上传时的本地指纹（D16）
- `Hashes()` 返回 `hash.None`（E3 实测无 hash）
- `Precision() = 1s`（E5 实测秒级）
- 建目录需要 `space:folder:create`（实测已获批）
- state 的锁已实现并测过，但 F1 的 backend 不自动持有（D17）

---

### F2 · 分片上传 + 限流 + 重试 🔄

**验收**
1. >20MB 文件能上传成功（实测 25MB 走 7 个 4MB 块）
2. 上传后 `Content-Range` 显示的总字节数 = 本地文件大小
3. `--tpslimit 5` 生效，不触发 1061045
4. 限流错误能自动退避重试
5. 上传中断后能续传（`upload_id` 持久化）

**实测结果（2026-11-06，真实租户）**

| # | 验收项 | 结果 | 证据 |
|---|---|---|---|
| 1 | >20MB 上传成功 | ✅ | 20MiB 直传；21MiB → 6 块；60MiB → 15 块 |
| 2 | 总量与本地一致 | ✅ | 三个文件回读 SHA-256 全部 MATCH |
| 3 | 限速：不超 5 QPS | ✅ **默认开启** | 客户端 `rate.Limiter`，默认 5 QPS、burst 1；`--feishu-qps` 可调，0 关闭 |
| 4 | 限流错误能自动退避重试 | ✅ | `1061045` / `1061001` / `1064230` 可重试并退避（1s/2s/4s） |
| 5 | 断点续传 | ⬜ 未做 | 见下 |

> **为什么不能靠 rclone 的 `--tpslimit`**：后端用自己的 `http.Client`，不经过 rclone 的
> `fshttp` 传输层，所以 `--tpslimit` 对本后端**无效**；而且它是进程级单一阀值，
> 无法表达「move 是 20/分钟」。详见 D30。

**额外验证**

| 场景 | 结果 |
|---|---|
| 分片大小取自服务端 `block_size` | ✅ 日志显示 `4194304` 来自 `upload_prepare` |
| 分片文件参与增量 | ✅ 二次运行 `skipped=3` |
| 改一个分片文件，其余仍跳过 | ✅ `replaced=1 skipped=2` |
| 替换分片文件不留重复 | ✅ 60MiB→30MiB，远端仍 3 条 |
| `block_num` 与 `block_size` 矛盾时拒绝上传 | ✅ 单测 |
| 源文件比声明短/长 | ✅ 单测：都在 `upload_finish` 前中止 |
| 某块失败就停，不继续发剩余块 | ✅ 单测 |

**本 feature 额外修掉的真实缺陷（不属于 F2 但被它暴露）**

1. **下载 >8MiB 被静默截断**：`client.do` 的 `maxResponseSize = 8MB`（给 JSON 列表的护栏）
   也套在了下载上，导致 20/21/60MiB 三个文件下载回来都是 8388608 字节且**不报错**。
   已改为流式下载（D22）。这也说明 SC-002 之前判错了——它要求「下载 10MB 一致」，
   而 F1 只跑过小文件。
2. **替换文件时旧版本从未被删除**：`Update` 在 `Put` 之后才去读旧 token，
   而 `Put` 已经把 state 改成新 token，于是「删旧」被无条件跳过。
   **更严重的是它一直被 `lsf` 掩盖**（`List` 折叠同名），所以 SC-009/SC-010
   之前的结论作废（D24）。已加回归测试并验证它会红。

**未做（因此本 feature 未标 ✅）**

| 缺口 | 原因 |
|---|---|
| 全局限速 5 QPS | 官方对 `upload_*` 三个接口都限 5 QPS / 10000 每天；现在靠「出错后退避」而不是「事先不发超」 |
| 断点续传 | 需把 `upload_id` 持久化进 state，并能重新定位本地读位置；官方保留 24 小时且**无 abort 接口** |
| 并发分片 | 官方明说不支持并发调用，故意不做（D21） |

---

### F3 · 删除 / 更新 / 移动 + 删除保护 🔄

**验收**

| # | 验收项 | 结果 | 证据 |
|---|---|---|---|
| 1 | `deletion = permanent` 删除后云端文件消失 | ✅ | `_f3perm` 只剩 1 个在册文件，无回收目录 |
| 2 | `deletion = versioning` 落到 `_weftsync_trash/<时间戳>/` | ✅ | `_trash/20261107-171809/` 内含 2 个被删 + 1 个被覆盖的文件，回读字节 MATCH |
| 3 | 待删超过 `max_delete` 时整轮中止、一个都不删 | ✅ | 6 个待删 / 上限 2 → exit 1，远端 7 个文件**一个不少** |
| 4a | `variant = update` 只增不减 | ✅ | 远端独有文件保留；`max_delete = 0` 也不报错 |
| 4b | `variant = mirror` + 删除 | ✅ | 恰好 2 个待删 / 上限 2 → 放行并删掉 2（边界不差一） |
| 4c | `variant = two-way` / `custom` | ⬜ 明确报错「未实现」 | 不静默降级 |
| 5 | 熔断时通知 | ⬜ 未做 | 现在只有明确的错误信息 + exit 1；通知基础设施在 F5 |
| 6 | 服务端 `Move` | ✅ | `moveto` 后文件确实进入子目录；token 不变；单测覆盖跨 root 场景 |
| 7 | `--plan` 只报告不改动 | ✅ | 报告 `deleted=3`，远端文件数不变 |

**设计要点（详见 `docs/decisions.md`）**
- `max_delete` 是**两轮**闸门：预检轮（dry-run，照常计数但不删）+ 执行轮（D25）
- rclone 自带的 `--max-delete` **语义相反**（删到上限再停），实测确认，不能直接用
- `versioning` 复用 rclone 的 `--backup-dir`，因后端实现了服务端 `Move` 而不产生流量（D26）
- 回收目录必须排除出同步集，用 `filter.AddConfig` 追加规则（D28）
- sync 层独立成 `job` 包，backend 只管单文件（D27）
- 作业文件格式见 `docs/job-file.md`

**本 feature 修复的真实缺陷**
1. **`Move` 的两个路径坐标系搞混**：目标 Fs 的 root 与源 Fs 的 root 不同（`moveto` 的
   典型情况），直接比较 `remote` 和 `src.Remote()` 导致永远判定为「无需动作」，
   命令却报 `Moved (server-side)`。同一个缺陷还让 `versioning` 失效并制造同名副本。
2. **`filter.NewFilter` + `ReplaceConfig` 会让所有文件都不参与同步**（D28）。

**F1 遗留进来的一项：清理同名重复副本 ✅ 已完成**

降级模式（state 丢失/损坏）会全量重传，云端因此出现同名重复副本（D19 有完整背景）。
问题不是「不知道要不要删」，而是**根本删不到**：`Fs.List` 把同名条目折叠成一条，
其余副本对 rclone 完全不可见。

实现为后端命令（D29，用法见 `docs/backend-commands.md`）：

| 命令 | 作用 |
|---|---|
| `backend duplicates` | 列出同名重复，不改动 |
| `backend dedupe -o confirm=true` | 删除非快照记录的副本 |

拒绝删除的三种情况：无 `state_file` / repair 模式 / 记录的 token 不在目录里。
不做「保留最新」这类启发式（多机同步时会删掉别人刚传的新版本）。

验收实测：造 3 个同名副本 → 保留快照记录的 token、删掉 2 个、剩 1 个；
不带 `confirm` 时一个不删；无 state 时拒绝。

**未做（因此本 feature 未标 ✅）**

| 缺口 | 原因 |
|---|---|
| 熔断通知（系统托盘/消息栏） | 依赖 F5 的通知基础设施 |
| `max_delete_size`（按体积限） | rclone 的 stats 没有暴露 `deletesSize` 的读接口 |
| `two-way` / `custom` | 需要 F4 的冲突检测先就位 |
| 作业文件里配 `state_file` | ✅ 已支持：`[pair.remote.options]`（只允许非敏感项，凭据会被拒绝） |

---

### F4 · 状态库 + 增量判断 + 冲突检测 ⬜

**验收**
1. 第二次同步不重传未变化的文件（state 生效）
2. `shadow mtime` 解决飞书 mtime 语义问题（E6 结论）
3. 三种冲突信号都能检出：token 消失 / 同名多条 / token 不匹配
4. 冲突默认 `report`：只报告，不改动任何云端文件，退出码非 0
5. state 可重建（删掉后首次同步能自愈）

---

### F5 · 常驻进程 + 定时调度 + 托盘 + 开机自启 ⬜

**验收**
1. 双击启动 → 托盘出现图标 + 网页服务起在 `127.0.0.1`
2. 三种定时模式都生效：`interval` / `daily_time` / `cron`
3. `align_to_start` 勾选与不勾选行为不同（可验证）
4. 防重叠：上一轮未结束时不启动新一轮
5. 开机自启：勾选后重启机器自动运行（Windows 注册表 / Linux autostart）
6. 单实例：重复双击不启动第二个进程
7. 退出时若有同步进行 → 提示确认 + `upload_id` 落盘

---

### F6 · 本地网页 UI ⬜

**验收：12 个功能逐个可操作**
1. job 列表（含启用开关、上次结果）
2. job 详情（pair 列表 + 配置摘要）
3. 变更预览（可展开到文件级）
4. 运行进度（当前文件、N/总数、速度、剩余）
5. 立即运行一次
6. 停止（优雅停止）
7. 历史记录
8. 日志查看（级别过滤、搜索、导出）
9. 冲突列表（保留两份 / 覆盖 / 忽略）
10. 凭据管理（Windows 凭据管理器 / Linux 加密文件 + 测试连接）
11. 设置页（全局默认、通知开关、开机自启、过滤规则）
12. 超 1500 拆分向导（见 F7）

**交互规则**
- 定时任务：**不预览**，直接执行
- 手动「立即运行」：**先预览**，确认后执行

---

### F7 · 超 1500 节点拆分向导 ⬜

**验收**
1. 预检发现 >1400 直接子节点 → 拒绝同步并提示
2. 能生成 4 种候选方案（按类型 / 首字母 / 年份 / 数量均分）
3. 用户勾选 + 输入目录名确认后才执行
4. 执行顺序：快照 → 移动本地 → 建云端目录 → 移动云端 → 校验
5. 「撤销拆分」能把文件移回原位（本地 + 云端）
6. 定时任务永不自动拆分，只发通知

---

### F8 · Linux 平台验证 ⬜

**验收**
1. 同一份源码在 Linux 编译成功（`GOOS=linux`）
2. F1–F6 的验收标准在 Linux 上全部重跑通过
3. 凭据走 Linux 加密文件路径
4. 开机自启走 `~/.config/autostart/weftsync.desktop`

---

### F9 · 双向同步 ⏸

**阻塞原因**：R5 说"尽力而为"，需先评估 F4 的 state 机制能否支撑。
FFS 用 `sync.ffs_db` 记录双向变更；我们的 state 结构类似，但要额外记录"远端已知状态"。
**待 F4 完成后再评估可行性。**

---

### F10 · 多网盘 ⬜ 可选

**验收**：一个 job 里多个 pair 指向不同后端（feishu / smb / local），
或者用 rclone 的 `union` 后端。

---

## 更新规则

1. **开始一个 feature 前**：把状态改为 🔄，并在下方「进度日志」加一行
2. **完成一个 feature**：逐条核实验收标准 → 全部通过才改 ✅
3. **受阻**：改 ⏸ 并写明原因
4. **发现新需求**：在 `PLAN.md` 加需求编号，然后在本表加 feature，**不允许只记在对话里**

---

## 进度日志

| 日期 | 事件 |
|---|---|
| 2026-11-06 | 创建本文件。F1 开始（`speckit-specify`）。A1–A25 需求访谈完成，宪法 v1.0.0 转正，M0 实测完成 |
| 2026-11-06 | F1 规格链完成：spec（36 FR / 11 SC）→ clarify（3 个问题）→ plan（Constitution Check 全过）→ tasks（75 项） |
| 2026-11-06 | F1 **M1 主体实现并实测**：`backend/feishu` 只读+上传+建目录可用，48/75 任务完成。真实租户验证 SC-002/004/009/010 通过。**state 未实现**（27 项待做） |
| 2026-11-06 | F1 **state 实现并实测**：`state` 包 18 个测试全绿；接进 backend 后实测「第二轮零上传」（7 轮验证见 quickstart §11），SC-011 通过。61/75。同轮修掉 7 个真实缺陷 |
| 2026-11-06 | **F2 分片上传完成并实测**：20MiB 直传、21MiB/60MiB 走 prepare/part/finish，回读 SHA-256 全部一致；分片文件也参与增量。同轮发现并修掉两个严重缺陷：**下载 >8MiB 静默截断**（D22）与**替换时旧版本从未删除**（D24，被 `lsf` 的折叠掩盖，SC-009/010 旧结论作废） |
| 2026-11-07 | **F3 主体完成并实测**：新增 `job` 配置层与 `weftsync run` 命令。删除闸门（预检轮 + 执行轮，超限**一个不删**）、`deletion=permanent/versioning`（三种命名）、`variant=update/mirror` 全部通过真实租户验收；作业文件格式见 `docs/job-file.md`。实现服务端 `Move`（探针确认 token 不变），并修掉两个只有真实调用才暴露的缺陷：`Move` 的路径坐标系混用（D26）、`filter.NewFilter` 让所有文件不参与同步（D28） |