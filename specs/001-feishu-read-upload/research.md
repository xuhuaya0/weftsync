# Phase 0 研究：飞书后端实现细节

**Feature**: `001-feishu-read-upload`
**Date**: 2026-11-06

> 本文只记录**实现层面**的决策与依据。需求层面的结论见 `spec.md`，
> 实测原始数据见 `PLAN.md` §3 与 `docs/feishu-probe.md`。

---

## R1. 状态文件的位置与命名

**Decision**
默认位置：
- Windows：`%APPDATA%\weftsync\state\<pair-id>.state.json`
- Linux：`~/.config/weftsync/state/<pair-id>.state.json`

`<pair-id>` 由「本地根路径 + 云端根路径」的 SHA-256 前 16 位生成，
保证同一对目录总是映射到同一文件，且不同 pair 互不干扰。

**Rationale**
- 与同步目标解耦：本地目录可以整体搬走，state 不跟着走（否则会被同步到云端，污染远端）
- 放在用户配置目录而非仓库内：符合 OS 惯例，且不会被误提交
- 用路径哈希而非序号：避免「重命名 pair 导致 state 错配」

**Alternatives considered**
- 放在本地根目录内（如 `<local>/.weftsync.state`）：会被同步到云端，且用户删目录时一起丢
- 放在程序目录：多用户/只读安装目录下不可写
- 用固定文件名（不分 pair）：多个 pair 会互相覆盖

---

## R2. CRC32 校验的具体实现

**Decision**
用 Go 标准库 `hash/crc32`（IEEE 多项式），把 JSON 正文的 CRC32 写在文件**头部**：

```
# weftsync state v1  crc32=1a2b3c4d
{ ... JSON 正文 ... }
```

读取时：先解析第一行拿 CRC，再对剩余正文算 CRC32 比对。

**Rationale**
- CRC32 是标准库自带，零依赖
- 头部格式对人和工具都友好（`head -1` 就能看到校验值）
- 不用 JSON 字段存 CRC（因为 CRC 要覆盖 JSON 本身，自引用无解）

**Alternatives considered**
- JSON 内嵌 `"crc32"` 字段并排除自身：解析器要特殊处理，容易出错
- SHA-256：见 spec 澄清（防的是恶意伪造，威胁模型外，收益为零）
- 二进制格式（如 gob）：不可读，排查困难

---

## R3. 原子写入的具体步骤

**Decision**
```
1. 写入 <state>.tmp          （同目录，保证同一文件系统）
2. f.Sync()                  （fsync，确保落盘）
3. f.Close()
4. 若 <state> 已存在 → copy 为 <state>.bak （覆盖旧 .bak）
5. os.Rename(<state>.tmp, <state>)   （原子替换）
```

**Rationale**
- `os.Rename` 在同一文件系统内是原子的（POSIX 保证；Windows 用 `MoveFileEx` 语义）
- 先 fsync 再 rename，避免 rename 成功但数据未落盘（断电后得到空文件）
- `.bak` 在 rename 之前生成，保存的是**上一个成功版本**

**Alternatives considered**
- 直接 `os.WriteFile`：中断会产生半损文件
- 双文件轮转（A/B）：实现复杂，`.bak` 已够用
- 数据库（SQLite）：F1 用不上事务/查询能力，违反宪法「简单优先」

---

## R4. 路径 ↔ token 的解析策略

**Decision**
**按需即时解析 + 单次运行内缓存**。

```
解析 "a/b/c.xlsx"：
  cursor = root_token
  for part in ["a", "b"]:
      entries = List(cursor)          ← 有缓存则复用
      cursor = 找 name==part 且 type==folder 的 token
      找不到 → 返回 ErrDirNotFound
  返回 cursor（即 "a/b" 的 folder_token）
```

单次运行内用 `map[folder_token][]entry` 缓存列表结果，
**不跨运行持久化**。

**Rationale**
- F1 不需要跨运行缓存（那是 F4 的优化）
- 即时解析保证正确性：云端被手动改动后不会用到过期 token
- 缓存避免重复列同一目录（一个目录有 500 个文件时，逐个解析路径会重复调用）

**Alternatives considered**
- 全树预扫描再建索引：F1 数据量小（几千文件）不必，且内存占用不可控
- 持久化路径映射：属 F4，且引入一致性维护负担

---

## R5. 同名文件的处理（关键）

**Decision**
后端 `List` **按名字分组**，同名多条时：
- 若 state 中有该路径的 `remote_token` → 返回**token 匹配的那一条**
- 若 state 中无记录 → 返回**`created_time` 最新的那一条**，并在 DEBUG 日志记录
  「同名 N 条，选最新」+ 各 token

**Rationale**
- rclone 的同步引擎按 `remote` 路径建立索引，**同名多条会让它混乱**（官方文档明确警告
  duplicate files "greatly confuse" syncing）
- 选 state 中记录的那条 = 已知的、我们自己创建的那份，语义正确
- 无记录时选最新：符合"云端被别处更新过"的直觉，且不会误删

**Alternatives considered**
- 全部返回给 rclone：按官方文档会混乱，不采用
- 直接报错：用户无法修复（飞书界面里同名文件难以区分）
- 选 `created_time` 最早的：会把别人的新版本当旧的，方向错

**对 spec 的影响**
这是实现 `List` 时的必要收敛，不改变 spec 中「F1 不做冲突检测」的边界 ——
冲突列表与人工处置属 F4。

---

## R6. `Size()` 返回什么

**Decision**
`Size()` 返回 **-1**（未知）。

**Rationale**
- 飞书列表接口不提供 size（E3 实测）
- 若在 `Size()` 里同步发 Range 请求，会让每个条目多一次网络调用，
  列表 500 条 = 500 次请求，在 20 QPS 下 25 秒（仅为了填充一个字段）
- rclone 的 `sizeDiffers` 对 `Size() < 0` 会跳过 size 比较（E6 源码确认），
  不会因此误判

**如何补偿**
增量判断完全由**我们自己的 state 机制**负责（spec FR-028~030），
不依赖 rclone 的默认比较。上传前我们已知本地真实 size，
state 里也记着上次的 size，比对不依赖 `Size()`。

**Alternatives considered**
- 返回本地 size（若是源侧）：`fs.Object` 是远端对象，没有本地上下文
- 每个条目发 Range 请求：见上，成本过高
- 返回 0：`sizeDiffers` 会判定"大小不同" → 每次都传，最差

---

## R7. `Precision()` 返回什么

**Decision**
返回 **1 秒**（`time.Second`）。

**Rationale**
- E5 实测：`modified_time` 是整数秒
- 声明 1 秒让 rclone 的 modify window 至少为 1 秒，减少因亚秒抖动误判
- 不声明 `ModTimeNotSupported`：那会让 rclone **永远判定相等**（E6 用例 D 实证），
  导致本地改了也不传

**Alternatives considered**
- `ModTimeNotSupported`：见上，会让增量完全失效
- 纳秒：与飞书实际精度不符，且会让 modify window 变小，更容易误判

---

## R8. `Update()` 如何实现

**Decision**
返回 `fs.ErrorCantUpdate`，**不实现**。

**Rationale**
- E1 实测：同名重传产生**新 token**，无法原地更新内容
- 返回该错误后，rclone 的 `operations.Copy` 会自动降级为「删除旧对象 + 重新上传」
- 这正是我们要的语义（配合 state 精确指定删哪个 token）

**与 state 机制的配合**
rclone 的降级删除会调用 `Object.Remove()`，此时它操作的是 `List` 返回的那个
`Object`（即我们按 state 选出的那条），所以 token 是精确的 —— 不会误删别的同名文件。

**Alternatives considered**
- 实现 `Update()` 内部做「删+传」：与 rclone 的降级逻辑重复，增加出错面
- 实现 `Open` + 服务端覆盖：飞书无此接口

---

## R9. 错误码到 rclone 错误的映射

**Decision**

| 飞书 code | 含义 | 映射到 |
|---|---|---|
| `99991672` | 缺 scope | 自定义 `ErrMissingScope`（含 scope 名与申请链接），**不重试** |
| `99991663` | token 无效/过期 | 刷新 token 后重试一次 |
| `1061004` | forbidden（未共享文件夹） | `fs.ErrorPermissionDenied`，**不重试** |
| `1061044` | parent 不存在 | `fs.ErrorDirNotFound`，**不重试** |
| `1061045` | 可重试 | 包装为可重试错误，退避重试 |
| `1061002` | 参数错误（含 >20MB） | 自定义 `ErrInvalidArgument`，**不重试** |
| `1062009` | size 不一致 | 可重试一次（可能是上传中断） |
| `1062506` | 超目录深度 | 自定义 `ErrDepthExceeded`，**不重试** |
| `1062507` | 超单层节点 | 自定义 `ErrTooManyNodes`，**不重试** |
| 其他 | 未知 | 原样包装，**不重试**（保守） |

**Rationale**
- 重试不可恢复的错误只会浪费时间并放大限流压力
- 每条自定义错误都带**可操作信息**（spec FR-024 要求）

**Alternatives considered**
- 全部当可重试：会疯狂重试权限错误，浪费时间且刷日志
- 全部不重试：限流时直接失败，用户体验差

---

## R10. 测试策略

**Decision**
三层，不依赖真实网络：

| 层 | 内容 | 用什么 |
|---|---|---|
| **单元** | 路径解析、state 读写/校验、错误码映射、按名分组的收敛逻辑 | `testing` + 纯函数，无 IO |
| **后端契约** | 用 `httptest.Server` 模拟飞书响应，验证 `List`/`Put`/`Open`/`Mkdir` | `net/http/httptest` |
| **集成（可选）** | 对真实租户跑一遍（需凭据） | 打 `-tags=integration`，默认不跑 |

**Rationale**
- 单元测试快、可重复、不需要凭据（符合宪法「可复现」）
- `httptest` 能精确构造边界响应（同名多条、错误码、分页），比真实环境更可控
- 集成测试单独 gate，避免 CI 依赖外部服务

**Alternatives considered**
- 只做集成测试：无凭据的人无法验证，且慢
- mock 接口而非 HTTP 层：会漏掉 HTTP 层的问题（如 multipart 格式、header）

---

## R11. 分页与大目录

**Decision**
`List` 内部分页循环：`page_size=200`，跟 `has_more` / `next_page_token`
翻页直到结束，对调用方呈现为一次性返回。

同时设置安全上限：单目录超过 **2000** 条时报错（飞书硬限 1500，
留缓冲；超限时给出 spec 中要求的可操作错误）。

**Rationale**
- rclone 的 `Fs.List` 语义是"返回该目录全部条目"，必须内部分页
- 2000 条上限防止云端异常（如数千条同名）时无限循环

**Alternatives considered**
- 暴露分页给上层：rclone 接口不支持
- 不设上限：异常数据下会 OOM

---

## 待确认（交 Phase 1 或实现时决定）

| # | 问题 | 何时定 |
|---|---|---|
| Q1 | CLI 命令名：直接用 `weftsync` 二进制（rclone 命令集），还是包一层 `weftsync sync` 子命令？ | Phase 1 的 contracts |
| Q2 | `--dry-run` 是否在 F1 提供？ | Phase 1（可能留给 F3） |
| Q3 | 上传时是否发 `checksum`（Adler-32）字段？实测不传也能成功 | 实现时 |
