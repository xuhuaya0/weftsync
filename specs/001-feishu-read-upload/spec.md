# Feature Specification: 飞书云盘后端 — 只读与上传

**Feature Branch**: `001-feishu-read-upload`

**Created**: 2026-11-06

**Status**: Draft

**Input**: 用户需求 R1–R18（`PLAN.md` §1）+ 需求访谈结论 A1–A25 + M0 实测结论（`PLAN.md` §3）

**Feature 序号**: F1（见 `docs/roadmap.md`）

---

## Clarifications

### Session 2026-11-06

- Q: 如果用户重复运行同步命令，云端出现多份同名文件，这种情况在 F1 阶段应该如何处理？ → A: 采用影子 mtime + 最小状态文件（state），实现「没变跳过、变了删旧传新」
- Q: 这个 state 文件（mtime + size + token）作为 F1 的一部分实现，你认可吗？ → A: 认可，包含 `mtime + size + token` 三项
- Q: state 文件的完整性校验和损坏恢复策略，采用哪一档？ → A: CRC32 + 原子写 + `.bak` 备份；校验失败时降级为「只传不删 + 重建」

---

## 背景与范围

本项目要让本地文件树单向同步到飞书云盘。完整产品还有定时、UI、冲突检测等，
但**本 feature 只交付最小可用闭环**：能列出飞书目录、能下载、能上传小文件、能建目录。

这一层的价值是：**证明「本地 ↔ 飞书」这条数据通道真的通**，
后续所有 feature（增量、删除、定时、UI）都建在它之上。

**能力边界（实测已确认，见 `PLAN.md` §3.1 / §3.2）**

| 事实 | 影响 |
|---|---|
| 飞书列目录不返回 `size` 和 `hash` | 后端无法提供文件大小与校验和 |
| 可用 1 字节 Range 请求拿到精确 size | 但成本是每文件一次请求，本 feature 先返回 -1 |
| 同名文件可共存，重传产生新 token | 后端必须声明允许重名；无法原地更新 |
| 单文件 >20MB 必须分片 | 本 feature 只做 ≤20MB 直传 |
| 建目录需 `space:folder:create` | 已获批，可自动建目录 |
| 目录最深 15 层、单层最多 1500 节点 | 超出会报错，本 feature 只做透传错误 |

---

## User Scenarios & Testing *(mandatory)*

### User Story 1 - 查看云端有什么 (Priority: P1)

用户配置好飞书凭据后，想确认程序能看到自己云盘里的文件，
并且能像操作本地目录一样浏览云端目录树。

**Why this priority**: 这是所有后续能力的前提。连不上、列不出，后面全部无从谈起。

**Independent Test**: 用 `weftsync lsf feishu:` 列出根目录；
用 `weftsync lsd feishu:` 列出子目录；用 `weftsync lsl feishu:<file>` 看单个文件详情。

**Acceptance Scenarios**:

1. **Given** 已配置有效的飞书应用凭据且目标文件夹已共享给应用所在的群，
   **When** 执行 `weftsync lsf feishu:`，
   **Then** 输出该文件夹下所有文件和目录的名称，退出码 0。

2. **Given** 根目录下有子目录 `docs/`，
   **When** 执行 `weftsync lsd feishu:`，
   **Then** 输出中出现 `docs` 这一行。

3. **Given** 根目录下有文件 `report.xlsx`，
   **When** 执行 `weftsync lsl feishu:report.xlsx`，
   **Then** 输出包含文件名、大小（`-1` 表示未知）、修改时间，退出码 0。

4. **Given** 目标文件夹是空的，
   **When** 执行 `weftsync lsf feishu:`，
   **Then** 输出为空，退出码 0（不是错误）。

---

### User Story 2 - 把云端文件拉回本地 (Priority: P1)

用户想把云盘上的文件取回本地，验证内容完全一致。

**Why this priority**: 下载是验证「链路真的通」的最强证据 —— 内容逐字节可核对。
同时它是 R5（云盘→本地拉取）的保底能力。

**Independent Test**: 上传一个已知内容的文件 → 删除本地副本 → 下载回来 → 比对哈希。

**Acceptance Scenarios**:

1. **Given** 云端存在文件 `data.csv`（内容已知），
   **When** 执行 `weftsync copy feishu:data.csv ./test/`，
   **Then** 本地出现 `./test/data.csv`，内容与云端**逐字节一致**（SHA-256 相同）。

2. **Given** 云端存在目录树 `docs/2024/*.pdf`，
   **When** 执行 `weftsync copy feishu:docs ./local-docs/`，
   **Then** 本地重建出相同的目录结构与文件，每个文件内容一致。

3. **Given** 云端文件 `big.bin` 大小为 18MB，
   **When** 下载该文件，
   **Then** 下载成功且大小正确（验证 ≤20MB 范围内无大小限制问题）。

4. **Given** 请求一个不存在的路径 `feishu:nope.txt`，
   **When** 执行 `copy feishu:nope.txt ./`，
   **Then** 报错信息明确指出「文件不存在」，退出码非 0。

---

### User Story 3 - 把本地文件推到云端 (Priority: P1)

用户把本地文件上传到云盘，在飞书客户端里能看到。

**Why this priority**: 这是产品的核心方向（R2 本地→云端单向同步）。
没有它，整个项目没有意义。

**Independent Test**: 上传一个本地文件，然后在飞书客户端里确认可见、可下载、
下载回来内容一致。

**Acceptance Scenarios**:

1. **Given** 本地有 `notes.txt`（内容已知），
   **When** 执行 `weftsync copy ./notes.txt feishu:`，
   **Then** 上传成功，飞书客户端里能看到 `notes.txt`，且下载回来内容一致。

2. **Given** 本地目录 `photos/` 含 20 个 png 文件（每个 < 1MB），
   **When** 执行 `weftsync copy ./photos feishu:photos`，
   **Then** 云端出现 `photos/` 目录及全部 20 个文件，逐个内容一致。

3. **Given** 本地文件大小为 **19.5MB**（接近 20MB 上限），
   **When** 上传该文件，
   **Then** 上传成功，云端大小正确。

4. **Given** 本地文件大小为 **21MB**（超出直传上限），
   **When** 上传该文件，
   **Then** 报错信息**明确说明**「超过 20MB 单文件直传上限」，
   并指出分片上传将在后续版本支持（**不是**含糊的"上传失败"）。

5. **Given** 已成功同步过一次，且本地文件**未做任何修改**，
   **When** 再次执行相同的同步命令，
   **Then** 云端不会新增任何同名文件（总数不变），且输出明确显示「跳过 N 个未变化的文件」。

6. **Given** 已成功同步过一次，`a.png` 内容被修改，
   **When** 再次执行同步命令，
   **Then** 云端 `a.png` **只有一条**记录，且内容与本地新版本一致
   （旧 token 对应的文件已被删除）。

---

### User Story 4 - 在云端建目录 (Priority: P2)

上传文件前，云端需要存在对应目录。

**Why this priority**: P2 而非 P1 —— 用户手工在飞书里建目录也能绕过。
但自动化同步里必须能建目录，否则空目录无法同步、新目录无法自动创建。

**Independent Test**: 用命令建一个嵌套目录，在飞书客户端里确认存在。

**Acceptance Scenarios**:

1. **Given** 云端根目录为空，
   **When** 执行 `weftsync mkdir feishu:newdir`，
   **Then** 云端出现空目录 `newdir`。

2. **Given** 云端已有 `a/b/`，
   **When** 执行 `weftsync mkdir feishu:a/b/c`，
   **Then** 建出 `c` 目录；已存在的 `a`、`b` 不报错（幂等）。

3. **Given** 目标目录名已存在，
   **When** 再次执行 `weftsync mkdir feishu:newdir`，
   **Then** 不报错（视为成功，幂等）。

4. **Given** 本地有**空目录** `archive/`，
   **When** 执行 `weftsync copy ./archive feishu:archive`，
   **Then** 云端建出空目录 `archive`（A20：空目录要建）。

---

### Edge Cases

- **路径含中文或空格**：本地 `季度报表 2024.xlsx` → 云端文件名一致，可正常回读
- **文件名为 200 个字符**：飞书是否有文件名长度限制？超限时给明确错误
- **同一目录下已有同名文件**：上传第二份时，云端出现**两条同名记录**（E2 实测）。
  本 feature 通过 state 机制**避免自己制造**重名：同一路径的更新会先删旧 token 再上传
- **云端存在非本机创建的同名文件**：state 里无记录 → 按「新增」上传，
  云端会出现同名两条。本 feature **不处理**（需人工清理或等 F4 冲突检测）
- **state 文件被手工修改**：CRC32 校验失败 → 降级为「只传不删 + 重建」，
  并在日志中明确告警
- **state 文件被删除**：重建 state（扫描云端）+ 只传不删，不产生重复上传
- **本地文件 mtime 被人为保留（`touch -d`）但内容已变**：由 `size` 兼任兜底判据；
  若两者都未变则无法检测（已知局限，记录在 Assumptions）
- **权限不足**：凭证有效但缺 scope → 报错必须指出**缺哪个 scope** 及申请链接
- **应用未共享该文件夹**：报 `1061004 forbidden` → 提示「需要把文件夹共享给应用所在的群」
- **目录超过 15 层**：报错并指出超限
- **单目录超过 1500 节点**：报错并指出超限
- **token 过期**：2 小时有效期，过期后自动刷新并重试一次（用户无感）
- **网络中断**：报错清晰，已上传的部分不产生半成品文件（飞书上传是原子的）
- **凭据缺失**：启动即报错，指出缺哪个环境变量，不进入"假装成功"状态

---

## Requirements *(mandatory)*

### Functional Requirements

**认证**

- **FR-001**: 系统 MUST 从环境变量 `WEFTSYNC_FEISHU_APP_ID` 与
  `WEFTSYNC_FEISHU_APP_SECRET` 读取凭据；缺失时 MUST 在启动阶段报错并指明缺哪一项
- **FR-002**: 系统 MUST 用 `app_id` + `app_secret` 换取 `tenant_access_token`，
  并缓存复用；token 有效期 2 小时
- **FR-003**: 系统 MUST 在 token 过期前自动刷新（剩余 < 30 分钟时刷新），
  刷新失败 MUST 报错而不是继续用旧 token

**列目录**

- **FR-004**: 系统 MUST 能列出指定文件夹的**直接子项**（文件 + 子目录），
  自动处理分页（`page_size` ≤ 200，`has_more` / `page_token`）
- **FR-005**: 系统 MUST 能从逻辑路径（`a/b/c`）解析到 `folder_token`，
  逐层查找；路径不存在时 MUST 返回「路径不存在」而非空列表
- **FR-006**: 系统 MUST 把飞书的 `type` 字段映射为「文件」或「目录」；
  `folder` → 目录，其余（`file`/`docx`/`sheet` 等）→ 文件
- **FR-007**: 系统 MUST 在列表结果中提供 `modified_time`（秒级）；
  `size` MUST 返回 `-1`（表示未知），因为飞书列表不提供该字段

**下载**

- **FR-008**: 系统 MUST 能按逻辑路径下载单个文件到本地，内容逐字节一致
- **FR-009**: 系统 MUST 支持递归下载整个目录树，并在本地重建相同的目录结构
- **FR-010**: 下载 MUST 写入临时文件后再重命名，
  避免中断时留下半成品文件

**上传**

- **FR-011**: 系统 MUST 能上传单个文件到指定云端目录
- **FR-012**: 系统 MUST 在上传前确认目标目录存在；不存在时
  MUST 根据配置自动创建（默认自动创建）
- **FR-013**: 系统 MUST 拒绝上传 **> 20MB** 的文件，并在错误信息中
  明确指出「超过 20MB 单文件直传上限，分片上传待后续版本支持」
- **FR-014**: 系统 MUST 支持递归上传本地目录树，
  包括**空目录**（A20：空目录要在云端建出来）
- **FR-015**: 上传 MUST 使用 UTF-8 文件名，中文与空格名称 MUST 保持不变

**建目录**

- **FR-016**: 系统 MUST 能创建目录，且 MUST 幂等
  （目录已存在时不报错，视为成功）
- **FR-017**: 系统 MUST 支持多级目录创建（`a/b/c` 自动创建缺失的中间层）

**能力声明（供上层同步引擎决策）**

- **FR-018**: 后端 MUST 声明「允许同名文件共存」
  （实测 E2：同一文件夹可存在两条同名记录）
- **FR-019**: 后端 MUST 声明「不支持无 hash」（实测 E3：列表无 hash 字段）
- **FR-020**: 后端 MUST 声明「修改时间精度为 1 秒」（实测 E5）
- **FR-021**: 后端 MUST 实现「替换已有文件」，因为飞书无原地更新（E1：同名重传产生新 token）。
  替换 MUST 采用「先上传新内容，成功后再删除旧 token」的顺序，
  因为删除不可逆（B11），先删后传若失败会丢失文件

**状态文件（state）—— 增量判断与安全覆盖的基础**

> 澄清结论（Session 2026-11-06）：采用「影子 mtime + 最小状态文件」方案。
> 不做这个，重复运行会因飞书允许同名共存而导致云端堆积同名文件。

- **FR-027**: 系统 MUST 维护一个本地状态文件，记录每个已同步文件的
  `{本地 mtime, 本地 size, 云端 token}`。默认位置：
  `<state_dir>/<pair-id>.state.json`（`state_dir` 可配置）
- **FR-028**: 增量判断 MUST 基于「本地当前指纹」与「state 中记录的本地指纹」比较，
  **MUST NOT** 比较「本地 mtime」与「云端 `modified_time`」
  （两者语义不同：前者是内容修改时间，后者是上传时刻，永远不可能相等）
- **FR-029**: 判断规则：
  - state 中无该路径 → **上传**（新增）
  - state 中有且 `mtime` 与 `size` 均相同 → **跳过**（不做任何网络请求）
  - state 中有且 `mtime` 或 `size` 不同 → **删除云端旧 token 对应文件 → 上传新的 → 更新 state**
  - state 中有但记录的 token 在云端已不存在 → **直接上传并更新 state**（不尝试删除）
- **FR-030**: 覆盖操作 MUST 使用 state 中记录的 `token` 精确删除，
  **MUST NOT** 按文件名删除（飞书允许同名共存，按名删可能删错）

**状态文件的完整性（防删除、防篡改）**

- **FR-031**: state 文件 MUST 带完整性校验（CRC32）。
  读取时校验不通过 MUST 拒绝使用该文件
- **FR-032**: state 文件 MUST 原子写入：写临时文件 → `fsync` → `rename` 覆盖，
  MUST NOT 直接原地覆盖（防止写到一半中断产生半损文件）
- **FR-033**: 每次写入前 MUST 保留上一版本为 `.bak`；
  主文件校验失败且 `.bak` 校验通过时 MUST 使用 `.bak` 并输出警告
- **FR-034**: **当 state 文件缺失、校验失败且无可用 `.bak` 时，
  系统 MUST 进入「只传不删」模式**：
  重新扫描云端现状重建 state，本次运行 MUST NOT 执行任何删除操作。
  重建完成后 MUST 明确告知用户「state 已重建」
- **FR-035**: state 中记录的 token MUST 在删除前二次确认其存在于当前云端列表中；
  不存在则跳过删除（避免因 state 过期误删）
- **FR-036**: state 目录 MUST 在版本库中忽略（`.gitignore`），
  MUST NOT 提交到仓库

**错误处理**

- **FR-022**: 飞书错误码 MUST 被分类映射，至少区分：
  认证失败 / 权限不足（附缺失 scope 与申请链接）/ 路径不存在 /
  超容量限制（深度、节点数）/ 需要重试（限流）/ 其他
- **FR-023**: 限流类错误（`1061045`）MUST 自动退避重试，
  重试次数上限可配置，超限后报错
- **FR-024**: 所有错误 MUST 输出**可操作的信息**
  （缺什么权限、哪个路径、怎么修），不得只输出错误码

**跨平台**

- **FR-025**: 代码 MUST 在 Windows 与 Linux 上编译通过且行为一致；
  平台差异只在「凭据存储方式」与「启动方式」两处（属 F5/F6）
- **FR-026**: 路径处理 MUST 使用标准库的跨平台方式，
  MUST NOT 硬编码 `/` 或 `\` 作为分隔符

### Key Entities

- **逻辑路径（remote path）**：如 `docs/2024/report.xlsx`，
  飞书没有真实路径，需由后端逐层映射为 `folder_token`
- **后端条目（entry）**：`name` / `type`（文件或目录）/ `size`（-1 表示未知）/
  `modified_time`（秒）/ `token`（飞书的 file_token 或 folder_token）
- **路径映射**：`逻辑路径 ↔ token` 的对应关系；
  本 feature 按需即时解析，**不持久化**
- **状态条目（state entry）**：每个已同步文件的同步快照
  - `path`：相对路径（POSIX 风格分隔符）
  - `local_mtime`：上次成功同步时本地文件的修改时间（Unix 秒）
  - `local_size`：上次成功同步时本地文件的字节数
  - `remote_token`：云端对应文件的 `file_token`（用于精确删除旧版本）
  - `synced_at`：上次成功同步的时间戳（便于排查）
- **状态文件（state file）**：`state entry` 的集合，序列化为 JSON，
  带 `version` / `crc32` 头部字段；每个 pair 一个文件

---

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 从零配置到成功列出云端文件，用户按文档操作**不超过 3 步命令**
- **SC-002**: 下载 10MB 文件后，本地与云端的 SHA-256 **完全一致**（逐字节校验）
- **SC-003**: 上传 100 个各 100KB 的文件，**全部成功且可逐个回读校验一致**
- **SC-004**: 上传 21MB 文件时，错误信息中**明确包含 "20MB" 字样**，
  用户不需要查文档就知道原因
- **SC-005**: 目录树（含 3 层嵌套、含空目录）上传后，
  云端结构与本地**完全对应**，目录数、文件数一致
- **SC-006**: 权限不足时，错误信息包含**缺失的 scope 名称**
- **SC-007**: Windows 与 Linux 编译产物都能通过上述全部验收项
- **SC-008**: 单次列目录 500 个条目的耗时**不超过 30 秒**（20 QPS 限制下的合理值）
- **SC-009**: 连续运行**同一同步命令 3 次**，云端文件总数**保持不变**
  （验证 state 机制确实避免了重复上传）
- **SC-010**: 修改 1 个文件后重跑，云端该文件名下的记录数**仍为 1**
  （验证「删旧传新」生效，不产生同名堆积）
- **SC-011**: 删除 state 文件后重跑，**不产生重复上传**
  （验证「重建 state + 只传不删」降级路径）

---

## Assumptions

- **凭据可用**：用户已有飞书自建应用，且已开启「机器人」能力、
  已把目标文件夹共享给应用所在的群、已获批 `space:folder:create` 权限
  （M0 实测已确认这套配置可用）
- **目标类型**：个人云盘的普通文件夹（A14 已确认），**不涉及** Wiki 知识库、
  共享空间、飞书原生文档（docx/sheet/bitable）
- **文件类型**：普通文件（Excel、图片、PDF、压缩包等），无特殊格式解析
- **state 位置**：默认在用户配置目录下（如 `%APPDATA%\weftsync\` /
  `~/.config/weftsync/`），与同步目标解耦；具体路径待 `plan.md` 确定
- **state 与 F4 的关系**：F1 的 state 只做「增量判断 + 安全覆盖」；
  F4 会扩展它用于「冲突检测」（同名多条、token 不属于本机等情况）
- **已知局限**：若本地文件内容改变但 `mtime` 与 `size` 都被人工保持原值，
  系统无法检测（需要内容 hash，而飞书不支持服务端 hash，本地算 hash 也要额外 IO）
- **云端已有的同名文件**（非本机创建）：F1 不做去重，会在 state 中记为「新增」
  并上传，导致同名两条。处理属于 F4
- **本 feature 不含**：分片上传（F2）、删除/更新/移动的完整策略（F3）、
  冲突检测（F4）、UI（F6）、定时（F5）
- **但本 feature 包含**：最小 state（增量判断 + 安全覆盖），`Delete` 接口
  （仅供覆盖旧版本时内部使用，不对外暴露 `sync --delete` 行为）
- **`size` 返回 -1**：列表不提供 size 时返回 -1；
  「用 Range 请求取真实 size」的优化留到 F4（需要 state 缓存才有意义）
- **凭据来源**：本 feature 只支持环境变量（开发态）；
  「凭据管理器 / 加密文件」属 F5/F6
- **限流**：上传 5 QPS、列目录 20 QPS、每天 10000 次上传
  （均为官方文档值）。本 feature 只做基本的退避重试，
  精细限速属 F2
