# Implementation Plan: 飞书云盘后端 — 只读与上传

**Branch**: `001-feishu-read-upload` | **Date**: 2026-11-06 | **Spec**: [spec.md](./spec.md)

**Input**: `specs/001-feishu-read-upload/spec.md`（36 条 FR / 11 条 SC / 3 条澄清）

---

## Summary

为 rclone 增加一个 `feishu` 后端，实现飞书云盘的**列目录、下载、小文件上传、建目录**
四项能力，并配套一个带完整性校验的本地状态文件来实现增量判断与安全覆盖。

技术要点：
- 以**依赖库**方式使用 rclone（不 fork），本仓库只提供 `main.go` + `backend/feishu`
- `Size()` 返回 -1、`Hashes()` 返回 none（飞书能力所限，实测确认）
- `Update()` 返回 `fs.ErrorCantUpdate`，让 rclone 降级为「删+传」，配合 state 精确指定删哪一份
- 增量判断完全由自建 state 负责（`mtime + size + token`），不依赖 rclone 默认比较
- state 用 CRC32 校验 + 原子写 + `.bak`，损坏时降级为「只传不删」

---

## Technical Context

**Language/Version**: Go 1.27.3（已安装，`C:\Program Files\Go\bin\go.exe`）

**Primary Dependencies**:
- `github.com/rclone/rclone v1.71.2`（同步引擎，作为库依赖）
- 标准库：`net/http`、`encoding/json`、`hash/crc32`、`crypto/sha256`、`os`、`path/filepath`
- **不引入**任何 HTTP 客户端库（用 `net/http` 即可）
- **不引入**任何 CLI 框架（用 rclone 自带的 cobra 体系）

**Storage**:
- 状态文件：JSON + CRC32 头部，每 pair 一个，位于用户配置目录
- 无数据库

**Testing**:
- `go test` + 标准库 `testing`
- HTTP 层用 `net/http/httptest` 构造响应
- 集成测试打 `-tags=integration` 标记，默认不跑（需真实凭据）

**Target Platform**: Windows（首要）+ Linux（`GOOS` 交叉编译），amd64

**Project Type**: CLI（基于 rclone 命令集）+ 可复用的库（`backend/feishu`）

**Performance Goals**:
- 列目录 500 条 ≤ 30 秒（受飞书 20 QPS 限制）
- 未变化的文件在增量同步中产生 **0 次网络请求**

**Constraints**:
- 上传 5 QPS / 10000 次每天；列目录 20 QPS
- 单文件直传 ≤ 20MB
- 目录深度 ≤ 15 层；单层节点 ≤ 1500
- token 有效期 2 小时，剩 < 30 分钟时刷新

**Scale/Scope**:
- 单次同步 ≤ 几千文件、总量几十 GB
- 单文件很少 > 20MB，绝不 > 1GB
- 代码量预估：后端 ~1500 行 + 状态管理 ~400 行 + 测试 ~1200 行

---

## Constitution Check

*GATE: 必须在 Phase 0 研究前通过；Phase 1 设计后复检。*

对照 `.specify/memory/constitution.md` v1.0.0 的 8 条核心原则：

| # | 原则 | 本计划如何满足 | 结论 |
|---|---|---|---|
| **I** | **简单优先** | 不 fork rclone（只用库）；不引第三方 HTTP/CLI/DB 依赖；状态文件用 JSON 而非 SQLite；路径解析按需即时进行不建全树索引 | ✅ 通过 |
| **II** | **验证优先于断言** | 所有飞书能力假设均来自实测（E1–E6、权限矩阵、分片、删除不可逆），出处见 `PLAN.md` §3；`research.md` 每条决策都附依据；验收标准全部可执行（SC-001~011） | ✅ 通过 |
| **III** | **失败要显式** | 错误码逐条映射（`research.md` R9），每条带可操作信息；不重试不可恢复错误；state 校验失败显式降级并告警，不静默继续；禁止空 `catch` | ✅ 通过 |
| **IV** | **单一事实来源** | 增量判断只有 state 一处权威；能力声明（`Size`/`Hashes`/`Precision`）只由后端返回一处；错误码映射集中在一个表 | ✅ 通过 |
| **V** | **边界与最小权限** | 只申请 `space:folder:create` + 文件类 scope，**不申请 `drive:drive`**（实测证明不需要）；凭据只从环境变量读；写操作限定在指定 folder_token 下；删除前二次确认 token 存在 | ✅ 通过 |
| **VI** | **可复现** | 构建脚本 `scripts/build-go.ps1`（已存在，`-Target both`）；无手工步骤；测试可用 `go test ./...` 一条命令跑；state 目录含在 `.gitignore` | ✅ 通过 |
| **VII** | **渐进交付** | F1 只做只读+上传+最小 state；分片（F2）、完整删除策略（F3）、冲突检测（F4）明确排除；`docs/roadmap.md` 记录 F1–F10 与依赖关系 | ✅ 通过 |
| **VIII** | **改动前先理解** | 复用 rclone 的 `operations.Equal` / `ErrorCantUpdate` 降级机制前，已读源码确认行为（E6 探针 + `operations.go`）；不修改 rclone 内部代码 | ✅ 通过 |

**项目约束对照**

| 约束 | 满足情况 |
|---|---|
| Windows 首要 + Linux 可用 | `GOOS` 交叉编译；路径用 `filepath` 不硬编码分隔符（FR-026） |
| 公开仓库，凭据不入库 | 只读环境变量；`scripts/check_secrets.py` + pre-commit 已挂；state 目录 gitignore（FR-036） |
| Go 主实现，Python 只做辅助 | 本 feature 无 Python 代码 |
| 复用 rclone 不重写引擎 | 依赖库方式，零修改 |
| 提交前跑测试 | `go test ./...`；`tasks.md` 中设为门禁 |
| PowerShell 脚本纯 ASCII | `build-go.ps1` 已是纯 ASCII |

**Gate 结论：PASS（无违规，无需 Complexity Tracking）**

---

## Project Structure

### Documentation (this feature)

```text
specs/001-feishu-read-upload/
├── plan.md              # 本文件
├── research.md          # Phase 0 输出（已完成）
├── data-model.md        # Phase 1 输出
├── quickstart.md        # Phase 1 输出
├── contracts/           # Phase 1 输出
│   ├── backend-interface.md
│   ├── state-file-format.md
│   └── error-codes.md
└── tasks.md             # Phase 2 输出（/speckit-tasks）
```

### Source Code (repository root)

```text
src/go/
├── go.mod                        # module weftsync，依赖 rclone v1.71.2
├── go.sum
├── main.go                       # 引入 rclone cmd + 我们的 backend
├── backend/
│   └── feishu/
│       ├── feishu.go             # Fs 注册、NewFs、Options
│       ├── fs.go                 # Fs 结构、List、Mkdir、Rmdir
│       ├── object.go             # Object 结构、Size/ModTime/Hash/Open/Update/Remove
│       ├── auth.go               # tenant_access_token 缓存与刷新
│       ├── api.go                # HTTP 端点薄封装（列目录/上传/下载/建目录/删除）
│       ├── errors.go             # 错误码映射（research.md R9）
│       ├── path.go               # 逻辑路径 ↔ folder_token 解析 + 单次运行缓存
│       ├── feishu_test.go
│       ├── api_test.go           # httptest 层测试
│       ├── errors_test.go
│       └── path_test.go
├── state/
│   ├── state.go                  # state 结构、读写、CRC32 校验、原子写、.bak
│   ├── state_test.go
│   └── repair.go                 # 损坏时的重建 + 只传不删模式
└── probes/
    └── e6_size_probe.go          # 已有，E6 验证工具（build ignore）
```

**Structure Decision**

选择「单一 Go 模块 + 两个内部包（`backend/feishu`、`state`）」：
- `backend/feishu` 是 rclone 的插件，必须能被 `main.go` 匿名导入
- `state` 独立成包因为它不依赖 rclone，可单独测试（符合宪法 I 简单优先）
- 不拆 `internal/`（Go 惯例但此处无外部使用者，增加路径长度无收益）
- 不建 `pkg/`（Go 社区已不推荐）

---

## Phase 1 设计产物

见同目录：
- [`data-model.md`](./data-model.md) — 实体与状态字段定义
- [`contracts/backend-interface.md`](./contracts/backend-interface.md) — rclone 接口契约
- [`contracts/state-file-format.md`](./contracts/state-file-format.md) — state 文件格式
- [`contracts/error-codes.md`](./contracts/error-codes.md) — 错误码映射表
- [`quickstart.md`](./quickstart.md) — 端到端验证步骤

---

## 实现顺序（供 tasks.md 参考）

```
1. 骨架：backend/feishu 注册 + NewFs 返回错误（保证能编译）
2. auth：token 获取与缓存
3. path：路径解析 + 缓存
4. fs：List（含分页、同名收敛）
5. object：Size/ModTime/Hash/Open（只读能力完整）
   → 此时可验证：lsf / lsjson / copy 下载
6. object：Update 返回 ErrorCantUpdate；Remove 实现
7. fs：Mkdir（幂等）
8. api：upload_all；object：Put（≤20MB，>20MB 明确报错）
   → 此时可验证：上传 / 回读
9. state：结构 + CRC32 + 原子写 + .bak + 损坏降级
10. 增量：把 state 接入上传流程（跳过未变化 / 删旧传新）
    → 此时可验证 SC-009/010/011
11. 集成验证：Windows + Linux 双平台跑全部 SC
```

---

## Phase 1 后 Constitution Check 复检

| 原则 | 复检结论 |
|---|---|
| I 简单优先 | ✅ 设计未引入额外依赖；`state` 独立包无循环依赖 |
| II 验证优先 | ✅ 每个契约文档都标注了实测依据来源 |
| III 失败显式 | ✅ `error-codes.md` 列出了 10 种错误及是否重试 |
| IV 单一来源 | ✅ state 格式只有 `state-file-format.md` 一处定义 |
| V 最小权限 | ✅ 契约中明确「不申请 drive:drive」 |
| VI 可复现 | ✅ quickstart 步骤全部为命令，无手工操作 |
| VII 渐进交付 | ✅ 实现顺序按可验证里程碑切分（读到写再到增量） |
| VIII 改动前理解 | ✅ R8 说明了为何用 `ErrorCantUpdate` 而非自己实现删+传 |

**复检结论：仍然 PASS。**

---

## Complexity Tracking

> 无需填写 —— Constitution Check 无违规。
