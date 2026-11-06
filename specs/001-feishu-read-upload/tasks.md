# Tasks: 飞书云盘后端 — 只读与上传

**Feature**: `001-feishu-read-upload`
**Branch**: `001-feishu-read-upload`
**Date**: 2026-11-06
**Input**: [spec.md](./spec.md) · [plan.md](./plan.md) · [research.md](./research.md) · [data-model.md](./data-model.md) · [contracts/](./contracts/) · [quickstart.md](./quickstart.md)

**Task 总数**: 75 ｜ **可并行**: 14 ｜ **用户故事**: 4（US1–US4）
**覆盖**: FR 36/36、SC 11/11、契约测试 C1–C15 / S1–S13 / E1–E9 全部有对应任务

---

## 格式说明

每个任务遵循：`- [ ] Tnnn [P?] [US?] 描述 + 文件路径`

- `[P]` — 可并行（不同文件、无未完成依赖）
- `[USn]` — 所属用户故事（仅用户故事阶段有）
- 任务描述末尾的 `{FR-xxx}` / `{SC-xxx}` 标明该任务实现的验收依据

---

## Phase 1 · Setup（项目初始化）

**目标**：仓库骨架能编译、能跑，`feishu` 已注册。

- [X] **T001** 在 `src/go/go.mod` 确认 module 名为 `weftsync`、依赖 `github.com/rclone/rclone v1.71.2`，`go.sum` 完整
- [X] **T002** [P] 创建 `src/go/backend/feishu/feishu.go`：定义 `init()` 中 `fs.Register` 注册 `feishu`，`Name`/`Description`/`NewFs`/`Options` 齐备
- [X] **T003** [P] 创建 `src/go/backend/feishu/feishu.go` 的 `Options`：`app_id`、`app_secret`（`IsPassword: true`）、`root_token`、`state_file`、`base_url`（默认 `https://open.feishu.cn`），与 `contracts/backend-interface.md` §5 一致
- [X] **T004** `main.go` 匿名导入 `_ "weftsync/backend/feishu"`，`go build` 通过
- [X] **T005** 验证：`go build -o .scratch/w.exe .` → `.scratch/w.exe help backends` 输出含 `feishu`
- [X] **T006** [P] 创建 `src/go/backend/feishu/errors.go`：定义 `ErrMissingScope`、`ErrTokenInvalid`、`ErrForbidden`、`ErrParentNotFound`、`ErrRetryable`、`ErrInvalidArgument`、`ErrSizeMismatch`、`ErrDepthExceeded`、`ErrTooManyNodes`、`ErrUnknown` 十个错误类型（`contracts/error-codes.md` §1）
- [X] **T007** [P] 创建 `src/go/backend/feishu/errors_test.go`：验证 E1–E9 中的映射分类（先测类型与 `IsRetryable()` 判定，不测文案）

**Phase 1 完成标准**：`go build ./...` 与 `go test ./backend/feishu -run TestErrors` 均通过

---

## Phase 2 · Foundational（所有用户故事的共同基础）

**目标**：认证、路径解析、HTTP 传输层、state 存储。这四块是 US1–US4 全都需要的。

### 2.1 认证（auth）

- [X] **T008** 创建 `src/go/backend/feishu/auth.go`：`TokenSource` 结构，持有 `appID`/`appSecret`/`baseURL`/缓存 token/过期时间
- [X] **T009** 实现 `TokenSource.Token(ctx)`：缓存有效（剩余 > 30 分钟）直接返回；否则 `POST /open-apis/auth/v3/tenant_access_token/internal` 刷新。并发安全（`sync.Mutex`）`{FR-002}` `{FR-003}`
- [X] **T010** 实现 `TokenSource.Invalidate()`：401 时调用，强制下次刷新
- [X] **T011** [P] 创建 `src/go/backend/feishu/auth_test.go`：用 `httptest` 模拟 token 端点，验证 ①首次获取 ②缓存复用（第二次不发请求）③接近过期时刷新 ④`Invalidate` 后重新获取

### 2.2 HTTP 传输层（transport）

- [X] **T012** 创建 `src/go/backend/feishu/api.go`：`client` 结构封装 `http.Client` + `TokenSource` + `baseURL`
- [X] **T013** 实现 `client.do(ctx, method, path, body, contentType, query)`：自动加 `Authorization: Bearer`，解析响应 JSON，把飞书 `code != 0` 转换为 `errors.go` 中对应错误（`contracts/error-codes.md` §1 全表）`{FR-022}` `{FR-024}`
- [X] **T014** 实现重试逻辑：仅对 `IsRetryable()` 为 true 的错误重试，最多 3 次，指数退避 1s/2s/4s + ±20% 抖动，单次上限 10s（`contracts/error-codes.md` §2）`{FR-023}`
- [X] **T015** 实现 `ErrTokenInvalid` 特例：刷新 token 后重试**一次**，不进入退避循环
- [X] **T016** [P] 创建 `src/go/backend/feishu/api_test.go`：httptest 验证 ①各种错误码正确映射 ②5xx 重试并递增间隔 ③401 刷新后成功 ④不可重试错误立即返回 ⑤所有错误信息不含 token/secret（E9）

### 2.3 路径解析（path）

- [X] **T017** 创建 `src/go/backend/feishu/path.go`：`pathResolver` 结构，持有 `rootToken` + 单次运行内的 `map[folderToken][]RemoteEntry` 缓存
- [X] **T018** 实现 `ResolveDir(ctx, logicalPath) (folderToken, error)`：逐层解析，用缓存避免重复列目录；任一层不存在 → `fs.ErrorDirNotFound` `{FR-005}`
- [X] **T019** 实现 `ResolveFile(ctx, logicalPath) (RemoteEntry, error)`：解析父目录后在列表中按名字查找（含同名收敛规则，见 T023）
- [X] **T020** 实现路径规范化：`\` → `/`、去前导 `/`、无尾随 `/`、保留大小写、不做 Unicode 归一化（`data-model.md` §2.3）`{FR-026}`
- [X] **T021** [P] 创建 `src/go/backend/feishu/path_test.go`：验证 ①单层 ②多层 ③不存在 ④中文/空格 ⑤Windows 反斜杠 ⑥缓存命中（用 mock 计数调用次数）

### 2.4 state 存储

- [X] **T022** 创建 `src/go/state/state.go`：`StateFile` / `StateEntry` 结构（字段名与 `contracts/state-file-format.md` §3 完全一致）
- [X] **T023** 实现 `Load(path) (*StateFile, error)`：按 §3.2 的 7 步校验顺序；CRC 不符尝试 `.bak`；全部失败 → 返回 `ErrStateCorrupt`（不是普通错误，要能被调用方识别并进入降级）`{FR-031}` `{FR-033}`
- [X] **T024** 实现 `Save(path, sf)`：按 §4 的 8 步原子写流程（写 `.tmp` → 回填 crc → fsync → 复制 `.bak` → rename）`{FR-032}`
- [X] **T025** 实现 CRC32 计算与回填（`hash/crc32` IEEE，覆盖第 2 行起全部字节，头部格式 `# weftsync state v1  crc32=<8位十六进制>`）
- [X] **T026** 实现 `PairID(localRoot, remoteRoot) string`：`sha256(local + "\x00" + remote)[:16]` 小写十六进制
- [X] **T027** 实现 `DefaultStatePath(pairID) string`：Windows `%APPDATA%\weftsync\state\`，Linux `~/.config/weftsync/state/`；同时将 state 目录加入 `.gitignore` `{FR-036}`
- [X] **T028** 实现锁文件：`<pair-id>.lock` 用 `O_CREATE|O_EXCL`；发现残留锁且 PID 不存活 → 接管并告警；退出时删除
  - 实现差异：进程内**共享并引用计数**（rclone 会为同一 remote 建多个 Fs 实例），跨进程互斥。用真实子进程做了验证（`TestLockIsExclusiveAcrossProcesses`），未假定
  - 未在 F1 的 backend 内自动加锁：Fs 没有关闭钩子，会导致每次运行都留下「陈旧锁」告警。锁留给 F5 daemon 的生命周期持有（见 `docs/decisions.md` D17）
- [X] **T029** 创建 `src/go/state/repair.go`：`Rebuild(remoteEntries)` 从云端现状构造新 state（只有 path/token，`local_mtime`/`local_size` 置 0 表示未知）
  - 实现差异：改为 `state.Repair()`，并新增 `Entry.Rebuilt` 标记。原方案 `local_mtime=0` 会被 `validEntry` 判为非法而静默丢弃，是设计矛盾（见 D16）。F1 未从 backend 调用它：重建需要全量递归遍历，代价过高；降级路径用空 state 就能达到同样的「不跳过、不删除」效果
- [X] **T030** [P] 创建 `src/go/state/state_test.go`：验证 S1–S13 全部（见 `contracts/state-file-format.md` §7），含「写入中途杀进程」用写入一半的 `.tmp` 模拟
  - 实际 18 个测试，S1–S13 全覆盖 + 4 个补充（未编码路径、Windows 反斜杠、PairID 不撞值、Repair 条目可用）

**Phase 2 完成标准**：`go test ./...` 全绿；state 的 13 条测试全过；api 的错误映射 9 条测试全过

---

## Phase 3 · US1 查看云端有什么（P1）

**故事目标**：`lsf` / `lsd` / `lsl` / `lsjson` 能正确列出飞书目录。
**独立测试**：用 quickstart 步骤 3 验证；不依赖上传能力。

- [X] **T031** [US1] 创建 `src/go/backend/feishu/fs.go`：`Fs` 结构（`rootToken`、`rootPath`、`client`、`resolver`、能力声明）
- [X] **T032** [US1] 实现 `Fs.NewFs(ctx, name, root, config)`：读配置 → 凭据（配置项优先，回落环境变量，皆无则报错指明缺哪项）→ 验证 root token（调 `metas.batch_query` 确认存在）→ 构造 `Fs` `{FR-001}`
- [X] **T033** [US1] 实现 `Fs.Precision()` 返回 `time.Second`、`Hashes()` 返回 `hash.Set(hash.None)` `{FR-019}` `{FR-020}`
- [X] **T034** [US1] 实现 `Fs.Features()`：`DuplicateFiles=true`、`CanHaveEmptyDirectories=true`、其余按 `contracts/backend-interface.md` §1.3 `{FR-018}`
- [X] **T035** [US1] 实现 `api.listFolder(ctx, folderToken) ([]RemoteEntry, error)`：`page_size=200`，跟 `has_more`/`next_page_token` 翻页；单目录超 2000 条报 `ErrTooManyNodes`
- [X] **T036** [US1] 实现 `Fs.List(ctx, dir)`：`ResolveDir` → `listFolder` → **按名分组收敛**（同名多条时：state 中有记录则选 token 匹配的，否则选 `created_time` 最新的，DEBUG 日志记录）→ 转成 `fs.DirEntries` `{FR-004}` `{FR-006}` `{FR-007}`
- [X] **T037** [US1] 创建 `src/go/backend/feishu/object.go`：`Object` 结构（`fs`/`remote`/`entry`），实现 `Fs()`/`Remote()`/`String()`/`Size()`(返回 -1)/`ModTime()`/`Hash()`(返回 "")
- [X] **T038** [US1] 实现 `Fs.NewObject(ctx, remote)`：`ResolveFile` → 构造 `Object`；不存在 → `fs.ErrorObjectNotFound`；是目录 → `fs.ErrorIsDir`
- [X] **T039** [US1] 实现 `Object.Open(ctx, options)`：调 `api.download`，把 `fs.RangeOption`/`fs.SeekOption` 转为 HTTP `Range` 头；服务端不支持 Range（返回 200）时本地丢弃前缀 `{FR-008}`
- [X] **T040** [US1] 创建 `src/go/backend/feishu/api_test.go` 中的 list/download 部分：C1（空目录）、C2（分页 250 条）、C3（同名收敛）、C4（不存在）、C5（是目录）、C8（Range 读 100 字节）
- [X] **T041** [US1] 手工验证 quickstart 步骤 3（lsf/lsd/lsl/lsjson）与步骤 4（下载 + SHA-256 比对），结果记录到 `specs/001-feishu-read-upload/quickstart.md` 的验收清单 `{SC-001}` `{SC-006}` `{SC-008}`

**Phase 3 完成标准**：C1–C5、C8 通过；quickstart 步骤 3、4 实测通过

---

## Phase 4 · US2 把云端文件拉回本地（P1）

**故事目标**：`copy feishu:X ./local` 能下载，内容逐字节一致；递归下载重建目录树。
**独立测试**：上传已知内容 → 删除本地 → 下载 → SHA-256 比对。

> 注：US2 依赖 US1 的 `List`/`Open`。下载路径本身在 US1 已实现（`Object.Open`），
> 本阶段补的是**递归下载与目录重建**的正确性。

- [X] **T042** [US2] 验证 `copy feishu:<目录> ./local --create-empty-src-dirs` 递归下载：目录结构、空目录都要重建（依赖 `Fs.List` 对 `IsDir` 的正确标记）`{FR-009}`
- [X] **T043** [US2] 实现下载的**原子落盘**：写 `<name>.partial` → 完成后 `rename`；中断时删除 `.partial`（FR-010）`{FR-010}`
  - 实现方式：**由 rclone 提供，无须自写**。`fs/operations/copy.go:93-119` 的 `checkPartial` 依据**目标端**的 `dstFeatures.PartialUploads` 决定是否写临时名后 rename；下载时目标是 local backend（`PartialUploads=true`）→ 自动 temp+rename；上传到飞书时目标是我们（`PartialUploads=false`）→ 直接写真名。已核对源码与 `--inplace`/`--partial-suffix` 帮助文本
- [ ] **T044** [US2] [P] 在 `src/go/backend/feishu/api_test.go` 补下载测试：范围请求、大文件分块读取、404 处理
- [X] **T045** [US2] 手工验证 quickstart 步骤 4：递归下载 + SHA-256 一致 + 空目录重建（`--create-empty-src-dirs`），结果记录到 `specs/001-feishu-read-upload/quickstart.md` `{SC-002}`

**Phase 4 完成标准**：SC-002（SHA-256 一致）；递归下载结构一致

---

## Phase 5 · US4 在云端建目录（P2）

**故事目标**：`mkdir` 可建单层/多层，幂等。
**独立测试**：建目录后在 `lsf` 中可见；重复建不报错。

> 先做 US4 再做 US3：上传需要目录存在，建目录是上传的前置能力。

- [X] **T046** [US4] 实现 `api.createFolder(ctx, parentToken, name) (token, error)`
- [X] **T047** [US4] 实现 `Fs.Mkdir(ctx, dir)`：逐层解析，缺失层用 `create_folder` 创建；**幂等**（已存在不报错）；权限不足 → `ErrMissingScope` 含 scope 名与申请链接 `{FR-016}` `{FR-017}`
- [X] **T048** [US4] 实现 `Fs.Rmdir(ctx, dir)`：返回 `fs.ErrorCantRmdir`（F1 不删目录）
  - 实现差异：改为「**只删除空目录**」。返回 `ErrorCantRmdir` 会让 `purge` 永远失败（实测：exit=0 但什么都没删，属于最坏的静默失败）。当前语义：目录非空 → 明确拒绝并报出条目数；`Rmdir("")` 允许删除 Fs 根目录（`purge remote:path` 需要），但 `root_token` 指向的那层永远拒删
- [X] **T049** [US4] [P] 在 `api_test.go` 补 T/C12：mkdir 单层、多层、幂等、权限错误映射
- [X] **T050** [US4] 手工验证 quickstart 步骤 5（含空目录同步 `--create-empty-src-dirs`），结果记录到 `specs/001-feishu-read-upload/quickstart.md` `{SC-005}`

**Phase 5 完成标准**：C12 通过；quickstart 步骤 5 实测通过

---

## Phase 6 · US3 把本地文件推到云端（P1）

**故事目标**：`copy ./local feishu:X` 能上传 ≤20MB；>20MB 明确报错；重复运行不产生同名堆积。
**独立测试**：上传 → 飞书可见 → 回读比对；连跑 3 次条目数不变。

- [X] **T051** [US3] 实现 `api.uploadAll(ctx, parentToken, name, content) (fileToken, error)`：multipart 构造（`file_name`/`parent_type=explorer`/`parent_node`/`size`/`file`），不传 `checksum`（实测可选）
- [X] **T052** [US3] 实现 `Fs.Put(ctx, in, src, options...)`：读入内存 → **>20MB 检查**（`src.Size() > 20*1024*1024` 或读入后判断）→ 返回含 "20MB" 字样的 `ErrInvalidArgument`；≤20MB 走 `uploadAll` `{FR-011}` `{FR-013}`
- [X] **T053** [US3] 在 `Fs.Put` 中实现上传前目录检查：目标目录不存在时按配置自动创建（调 `Fs.Mkdir`，默认开）`{FR-012}`
- [X] **T054** [US3] 验证文件名编码：上传中文名（`季度报表 2024.xlsx`）与含空格名，云端名字保持不变、可回读 `{FR-015}`
- [X] **T055** [US3] 验证递归上传：`copy ./photos feishu:photos` 重建目录树，含空目录（依赖 T047 的 Mkdir）`{FR-014}`
- [X] **T056** [US3] 实现 `Object.Update()`：**先上传新版本，成功后再删旧 token**（FR-021 / `data-model.md` §3）
  - 实现差异：原任务写的「返回 `fs.ErrorCantUpdate` 触发 rclone 删+传降级」**不成立**——rclone 里没有这个错误值（全仓 grep 核实）。因为 `Features.Move == nil`，`checkPartial` 强制 `inplace=true`，已存在的目标一律走 `dst.Update()`，返回错误就是同步直接失败、没有降级。故必须真正实现（见 D13）
- [X] **T057** [US3] 实现 `Object.Remove(ctx)`：调 `api.deleteFile`，成功后从 state 删除该条；失败按 `error-codes.md` 映射
- [X] **T058** [US3] 实现 `Object.SetModTime()` 返回 `fs.ErrorCantSetModTime`
- [X] **T059** [US3] 在 `Fs.Put` 成功后**写入 state**：记录 `path`/`local_mtime`/`local_size`/`remote_token`/`synced_at` `{FR-027}`
- [X] **T060** [US3] 实现增量判断接入：`Fs` 在 `NewObject`/`Put` 路径中查 state —— 本地 `mtime` 与 `size` 均与 state 一致 → 视为未变化（配合 rclone 的跳过逻辑）；不同 → 先上传新文件，**成功后再删** `remote_token` 对应的旧文件（先传后删，`data-model.md` §3）`{FR-028}` `{FR-029}` `{FR-030}`
  - 实现方式：不是改 rclone 的比对逻辑，而是让 `Object.ModTime()` / `Object.Size()` 在「state 记录的 token == 当前 remote token」时返回**上次上传时的本地指纹**。rclone 拿它跟当前本地文件比，相等就跳过。好处是零改动引擎，且 token 不匹配（云端被替换/删除）时自动退回真实 `modified_time` 强制重传
- [X] **T061** [US3] 实现删除前二次确认：`remote_token` 必须仍存在于当前 `listFolder` 结果中（FR-035），否则跳过删除并告警 `{FR-035}`
- [X] **T062** [US3] 实现 state 降级模式：`ErrStateCorrupt` 时进入 `ReadOnlyRepair` —— 允许上传与跳过以外的操作，**禁止任何删除**，输出明确告警（`contracts/state-file-format.md` §5）`{FR-034}`
- [ ] **T063** [US3] [P] 在 `api_test.go` 补上传测试：C6（21MB 报错含 "20MB"）、C7（1MB 成功）
- [X] **T064** [US3] 在 `src/go/state/state_test.go` 补增量相关测试：第一次全传、第二次零请求、改一个文件只传一个、删 state 后降级不删
  - 实现方式：state 包只能单测存储，增量行为只能在真实租户上验证。已用 `.scratch/verify_incremental.ps1` 跑 7 轮实测（结果记入 `quickstart.md`）
- [X] **T065** [US3] 手工验证 quickstart 步骤 6（上传、回读、20MB 边界）与步骤 7（SC-009/010/011），结果记录到 `specs/001-feishu-read-upload/quickstart.md` `{SC-003}` `{SC-004}` `{SC-009}` `{SC-010}` `{SC-011}`

**Phase 6 完成标准**：SC-003、SC-004、SC-009、SC-010、SC-011 全部实测通过

---

## Phase 7 · Polish（收尾与跨切面）

- [X] **T066** [P] 检查所有错误信息符合 `error-codes.md` §3 的三要素格式（发生了什么 / 为什么 / 怎么修）
- [X] **T067** [P] 检查所有日志符合 `error-codes.md` §4 的级别要求；确认无 token/secret 泄露（E9）
- [ ] **T068** 测试清单对齐：`contracts/backend-interface.md` §6 的 C1–C15 逐条确认有对应测试且通过
- [ ] **T069** 测试清单对齐：`contracts/state-file-format.md` §7 的 S1–S13 逐条确认通过
- [ ] **T070** 测试清单对齐：`contracts/error-codes.md` §5 的 E1–E9 逐条确认通过
- [X] **T071** 运行 `go vet ./...` 与 `go test ./... -race`，修掉所有报告项
- [X] **T072** 执行 `scripts/build-go.ps1 -Target both`，确认双平台产物均生成 `{FR-025}`
- [ ] **T073** 在 Linux 环境（WSL2）跑 quickstart 步骤 1–8、10，结果记录到 `specs/001-feishu-read-upload/quickstart.md` 的验收清单 `{SC-007}`
- [ ] **T074** 按 `specs/001-feishu-read-upload/quickstart.md` 末尾的「完整验收清单」逐项打勾，全部通过才算 F1 完成 `{SC-001}`–`{SC-011}`
- [ ] **T075** 更新 `docs/roadmap.md`：F1 状态改 ✅，进度日志加一行

---

## 依赖关系

```
Phase 1 (T001–T007)
        ↓
Phase 2 (T008–T030)  ← 四个子模块：auth / transport / path / state
        ↓
   ┌────┴────┬─────────┬──────────┐
   ▼         ▼         ▼          ▼
 US1       US4       US2        US3
(T031-41) (T046-50) (T042-45) (T051-62)
   │         │         │          │
   └─────────┴─────────┴──────────┘
                 ↓
          Phase 7 (T066–T075)
```

**关键依赖链**

| 任务 | 依赖 |
|---|---|
| US1 全部 | Phase 2 的 auth + transport + path |
| US2 全部 | US1 的 `List` + `Object.Open` |
| US4 全部 | Phase 2 的 path |
| US3 全部 | US1 的 `List` + US4 的 `Mkdir` + Phase 2 的 state |

**US1 是 MVP**：完成后可以列目录、下载，已有实用价值（相当于只读挂载）。

---

## 并行执行示例

### Phase 2 内可并行（4 组互不依赖）

```powershell
# 终端 A：认证
go test ./backend/feishu -run TestAuth -v

# 终端 B：传输层
go test ./backend/feishu -run TestClient -v

# 终端 C：路径解析
go test ./backend/feishu -run TestPath -v

# 终端 D：state
go test ./state -v
```

### US1 与 US4 可并行（互不依赖）

US1 只需要读接口，US4 只需要建目录接口，两者在 Phase 2 之后可同时开发。
### US2 与 US3 可并行（在 US1 + US4 完成后）

US2 走下载路径，US3 走上传路径，文件不重叠。

---

## 实现策略

### MVP 优先

**MVP = Phase 1 + Phase 2 + US1**（T001–T041）

做完即可：
- 列出云端目录
- 下载文件到本地
- 证明「飞书 → 本地」通道可用

这是 R5（云盘→本地拉取）的保底能力，也是最快能验证链路的方式。

### 增量交付顺序

| 里程碑 | 任务范围 | 可验证能力 |
|---|---|---|
| **M1** | T001–T041 | 只读（lsf/lsd/copy 下载） |
| **M2** | + T046–T050 | 建目录 |
| **M3** | + T051–T065 | 上传 + 增量 + state |
| **M4** | + T066–T075 | 双平台全验收 |

### 每个任务的验证方式

| 类型 | 验证 |
|---|---|
| 实现类 | 对应测试通过 + `go build` 通过 |
| 测试类 | 测试本身通过 |
| 手工验证类（T041/T045/T050/T065/T073） | quickstart 对应步骤的期望输出 |
| 检查类（T066–T070） | 逐条对照清单打勾 |

---

## 已知风险与对策

| 风险 | 影响任务 | 对策 |
|---|---|---|
| `Fs.Put` 需要 `src.Size()`，但 rclone 可能传 -1 | T052 | 用 `io.LimitReader` 读到 20MB+1 判断实际大小 |
| 同名收敛可能让「删旧传新」选错对象 | T036, T057 | state 的 `remote_token` 优先；无记录时选最新 |
| `.partial` 文件残留 | T043 | 启动时清理超 24 小时的 `.partial` |
| 锁文件残留导致无法运行 | T028 | PID 存活检查 + 超时接管 |
| Linux 与 Windows 的 state 路径不同 | T027 | 用 `os.UserConfigDir()`，Go 标准库已处理 |
| 测试需要真实凭据 | T041/T045/T050/T065/T073 | 标 `-tags=integration`，默认跳过；手工验证用 `.env` |

---

## Task 统计

| Phase | 任务数 | 可并行 |
|---|---|---|
| 1 Setup | 7 (T001–T007) | 3 |
| 2 Foundational | 23 (T008–T030) | 5 |
| 3 US1 | 11 (T031–T041) | 0 |
| 4 US2 | 4 (T042–T045) | 1 |
| 5 US4 | 5 (T046–T050) | 1 |
| 6 US3 | 15 (T051–T065) | 1 |
| 7 Polish | 10 (T066–T075) | 2 |
| **合计** | **75** | **13** |
