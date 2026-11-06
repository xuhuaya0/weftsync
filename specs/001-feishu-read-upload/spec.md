# Feature Specification: 飞书云盘后端 — 只读与上传

**Feature Branch**: `001-feishu-read-upload`

**Created**: 2026-11-06

**Status**: Draft

**Input**: 用户需求 R1–R18（`PLAN.md` §1）+ 需求访谈结论 A1–A25 + M0 实测结论（`PLAN.md` §3）

**Feature 序号**: F1（见 `docs/roadmap.md`）

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
  本 feature **接受**这个行为，不做去重（冲突处理属 F4）
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
- **FR-021**: 后端 MUST 拒绝原地更新（实测 E1：同名重传产生新 token，
  无法保持 token 不变），并返回「不支持原地更新」的明确错误

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
  本 feature 按需即时解析，**不持久化**（持久化属 F4 的 state 库）

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

---

## Assumptions

- **凭据可用**：用户已有飞书自建应用，且已开启「机器人」能力、
  已把目标文件夹共享给应用所在的群、已获批 `space:folder:create` 权限
  （M0 实测已确认这套配置可用）
- **目标类型**：个人云盘的普通文件夹（A14 已确认），**不涉及** Wiki 知识库、
  共享空间、飞书原生文档（docx/sheet/bitable）
- **文件类型**：普通文件（Excel、图片、PDF、压缩包等），无特殊格式解析
- **本 feature 不含**：分片上传（F2）、删除/更新/移动（F3）、
  增量判断与 state（F4）、冲突检测（F4）、UI（F6）、定时（F5）
- **`size` 返回 -1**：本 feature 先按最简方案实现；
  「用 Range 请求取真实 size」的优化留到 F4（需要 state 缓存才有意义）
- **凭据来源**：本 feature 只支持环境变量（开发态）；
  「凭据管理器 / 加密文件」属 F5/F6
- **限流**：上传 5 QPS、列目录 20 QPS、每天 10000 次上传
  （均为官方文档值）。本 feature 只做基本的退避重试，
  精细限速属 F2
