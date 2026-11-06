# Quickstart：F1 端到端验证

**Feature**: `001-feishu-read-upload` | **Date**: 2026-11-06

> 全部为可执行命令，无手工步骤（宪法原则 VI：可复现）。
> 前置：Go 1.27.3 已安装；飞书应用凭据可用；目标文件夹已共享给应用所在的群。

---

## 0. 环境准备

```powershell
# 仓库根目录
Set-Location 'C:\Users\Jlsemi-62\OneDrive\_PC_Sync\Documents\3_workspace\my_folder\weftsync'

# Go 工具链（PATH 可能未刷新，用绝对路径）
$go = 'C:\Program Files\Go\bin\go.exe'
& $go version          # 期望：go version go1.27.3 windows/amd64
```

凭据（`.env` 已存在且被 gitignore）：

```
WEFTSYNC_FEISHU_APP_ID=cli_xxx
WEFTSYNC_FEISHU_APP_SECRET=xxx
WEFTSYNC_TEST_FOLDER_TOKEN=https://xxx.feishu.cn/drive/folder/fldcnXXX
```

---

## 1. 构建

```powershell
powershell -ExecutionPolicy Bypass -File scripts\build-go.ps1 -Target both
```

**期望输出**

```
building windows/amd64 -> ...\dist\weftsync-windows-amd64.exe
  ok, ~66 MB
building linux/amd64 -> ...\dist\weftsync-linux-amd64
  ok, ~66 MB
done.
```

**验证**

```powershell
& .\dist\weftsync-windows-amd64.exe version
# 期望：rclone v1.71.2-DEV

& .\dist\weftsync-windows-amd64.exe help backends | Select-String feishu
# 期望：feishu       Feishu / Lark Drive
```

---

## 2. 配置 remote

```powershell
$exe = '.\dist\weftsync-windows-amd64.exe'

& $exe config create feishu feishu `
    app_id $env:WEFTSYNC_FEISHU_APP_ID `
    app_secret $env:WEFTSYNC_FEISHU_APP_SECRET `
    root_token $env:WEFTSYNC_TEST_FOLDER_TOKEN `
    --non-interactive
```

> 也可以直接用环境变量方式，不写配置文件：
> `RCLONE_CONFIG_FEISHU_TYPE=feishu` 等。

---

## 3. 只读验证（User Story 1）

> **SC-001 验证**：从「环境准备 + 构建 + 配置 remote」到「能列出云端文件」，
> 按本文档只需 **3 步命令**（步骤 1、2、3.1）。本提示即为该标准的对照依据。

```powershell
# 3.1 列根目录
& $exe lsf feishu:
# 期望：列出文件与目录名，退出码 0

# 3.2 列出目录（含类型）
& $exe lsd feishu:
# 期望：目录名带 -1 结尾（rclone 的目录标记）

# 3.3 单文件详情
& $exe lsl feishu:<某个已存在的文件>
# 期望：大小显示 -1（飞书不提供 size），修改时间为整数秒

# 3.4 JSON 输出（便于脚本验证）
& $exe lsjson feishu:
# 期望：JSON 数组，每项含 Path/Name/Size=-1/ModTime/IsDir
```

**SC 对应**：SC-008（500 条 ≤ 30 秒）

---

## 4. 下载验证（User Story 2）

```powershell
# 4.1 先上传一个已知内容的文件（见步骤 6），或使用云端已有文件
# 4.2 下载
& $exe copy feishu:<远程文件> .\\.scratch\dl\

# 4.3 逐字节校验
$local = '.\\.scratch\dl\<文件名>'
(Get-FileHash $local -Algorithm SHA256).Hash
# 与云端内容的 SHA-256 比对（若是本地上传的，与本地上传前的哈希比）

# 4.4 递归下载目录
& $exe copy feishu:<某个目录> .\\.scratch\dl-tree\ --create-empty-src-dirs
# 期望：本地重建相同结构与空目录
```

**SC 对应**：SC-002（SHA-256 一致）

---

## 5. 建目录验证（User Story 4）

```powershell
# 5.1 单层
& $exe mkdir feishu:testdir
& $exe lsf feishu: | Select-String testdir
# 期望：出现 testdir

# 5.2 幂等（再建一次不报错）
& $exe mkdir feishu:testdir
# 期望：退出码 0

# 5.3 多层
& $exe mkdir feishu:testdir/a/b/c
# 期望：三层都建出来

# 5.4 空目录同步
New-Item -ItemType Directory -Path .\\.scratch\emptydir -Force | Out-Null
& $exe copy .\\.scratch\emptydir feishu:emptydir-test --create-empty-src-dirs
& $exe lsd feishu: | Select-String emptydir-test
# 期望：出现该空目录
```

---

## 6. 上传验证（User Story 3）

```powershell
# 6.1 准备测试文件
New-Item -ItemType Directory -Path .\\.scratch\up -Force | Out-Null
'hello weftsync' | Set-Content .\\.scratch\up\notes.txt -Encoding utf8

# 6.2 上传
& $exe copy .\\.scratch\up feishu:testdir --create-empty-src-dirs
& $exe lsf feishu:testdir
# 期望：出现 notes.txt

# 6.3 回读校验
& $exe copy feishu:testdir/notes.txt .\\.scratch\up-back\
$h1 = (Get-FileHash .\\.scratch\up\notes.txt -Algorithm SHA256).Hash
$h2 = (Get-FileHash .\\.scratch\up-back\notes.txt -Algorithm SHA256).Hash
if ($h1 -eq $h2) { 'PASS: content identical' } else { 'FAIL' }
```

**20MB 边界验证**

```powershell
# 6.4 19.5MB 应成功
$fs = [System.IO.File]::Create('.\\.scratch\up\near20.bin')
$fs.SetLength(19.5MB); $fs.Close()
& $exe copy .\\.scratch\up\near20.bin feishu:testdir
# 期望：成功

# 6.5 21MB 应失败且信息含 "20MB"
$fs = [System.IO.File]::Create('.\\.scratch\up\over20.bin')
$fs.SetLength(21MB); $fs.Close()
& $exe copy .\\.scratch\up\over20.bin feishu:testdir 2>&1 | Select-String '20MB'
# 期望：错误信息含 "20MB"
```

**SC 对应**：SC-003、SC-004、SC-005

---

## 7. 增量验证（state 机制）

```powershell
# 7.1 第一次同步
& $exe copy .\\.scratch\up feishu:testdir

# 7.2 记录云端条目数
$before = (& $exe lsf feishu:testdir | Measure-Object -Line).Lines

# 7.3 不改任何文件，再跑一次
& $exe copy .\\.scratch\up feishu:testdir

# 7.4 条目数应不变（SC-009）
$after = (& $exe lsf feishu:testdir | Measure-Object -Line).Lines
if ($before -eq $after) { "PASS SC-009: $after entries unchanged" } else { "FAIL: $before -> $after" }

# 7.5 修改一个文件后重跑
'modified content' | Set-Content .\\.scratch\up\notes.txt -Encoding utf8
& $exe copy .\\.scratch\up feishu:testdir

# 7.6 该文件名下仍只有 1 条（SC-010）
$n = (& $exe lsf feishu:testdir | Select-String 'notes.txt' | Measure-Object -Line).Lines
if ($n -eq 1) { 'PASS SC-010: still 1 entry' } else { "FAIL: $n entries" }
```

**state 损坏降级验证（SC-011）**

```powershell
# 7.7 找到 state 文件并删除
$stateDir = "$env:APPDATA\weftsync\state"
Get-ChildItem $stateDir
Remove-Item "$stateDir\*.state.json"
Remove-Item "$stateDir\*.state.json.bak"

# 7.8 重跑
& $exe copy .\\.scratch\up feishu:testdir 2>&1
# 期望：输出含"state 已重建"与"不执行删除"

# 7.9 仍无删除（SC-011）
$n2 = (& $exe lsf feishu:testdir | Select-String 'notes.txt' | Measure-Object -Line).Lines
# 期望：条目数不减少
```

---

## 8. 错误路径验证

```powershell
# 8.1 不存在的路径
& $exe lsf feishu:nonexistent-dir
# 期望：错误含"目录不存在"，退出码非 0

# 8.2 错误的凭据
$env:WEFTSYNC_FEISHU_APP_SECRET = 'wrong'
& $exe lsf feishu:
# 期望：错误含认证失败提示

# 8.3 缺凭据
Remove-Item Env:WEFTSYNC_FEISHU_APP_ID
& $exe lsf feishu:
# 期望：错误指明缺 WEFTSYNC_FEISHU_APP_ID
```

---

## 9. 单元测试

```powershell
Set-Location src\go
& $go test ./... -v
# 期望：全部 PASS

# 只跑契约测试
& $go test ./backend/feishu -run 'TestContract' -v

# 覆盖率
& $go test ./... -cover
```

**集成测试**（需真实凭据，默认不跑）

```powershell
& $go test ./backend/feishu -tags=integration -v
```

---

## 10. Linux 验证

**在 Linux 环境中**（WSL2 / 容器 / 真机）：

```bash
cd src/go
go build -o ../../dist/weftsync-linux ./           # 或本地 go build
./../../dist/weftsync-linux version

export WEFTSYNC_FEISHU_APP_ID=cli_xxx
export WEFTSYNC_FEISHU_APP_SECRET=xxx
export WEFTSYNC_TEST_FOLDER_TOKEN=fldcnXXX

./../../dist/weftsync-linux lsf feishu:
# 期望：与 Windows 结果一致

# state 路径应为 ~/.config/weftsync/state/
ls ~/.config/weftsync/state/
```

**SC 对应**：SC-007（双平台通过全部验收）

---

## 完整验收清单

| # | 检查 | 对应 |
|---|---|---|
| □ | `version` 显示 rclone v1.71.2-DEV | 构建 |
| □ | `help backends` 含 feishu | 构建 |
| □ | `lsf` / `lsd` / `lsl` / `lsjson` 正常 | US1 |
| □ | 下载后 SHA-256 一致 | US2 / SC-002 |
| □ | 递归下载重建目录与空目录 | US2 |
| □ | `mkdir` 单层/多层/幂等 | US4 |
| □ | 上传后飞书可见、回读一致 | US3 / SC-003 |
| □ | 19.5MB 成功 | US3 |
| □ | 21MB 成功（走分片）且回读一致 | US3 / SC-004 |
| □ | 60MB 成功（15 块）且回读一致 | F2 |
| □ | 连跑 3 次条目数不变 | SC-009 |
| □ | 改 1 文件后仍 1 条 | SC-010 |
| □ | **替换后用原始 API 列表确认无同名重复**（不能用 `lsf`，它折叠同名） | SC-009 / SC-010 |
| □ | 删 state 后不重复上传 | SC-011 |
| □ | 缺 scope 错误含 scope 名与链接 | US1 / SC-006 |
| □ | `go test ./...` 全绿 | 单元 |
| □ | Linux 跑通上述全部 | SC-007 |

---

## 11. 增量同步实测记录（2026-11-06，真实租户）

**环境**：Windows，`weftsync.exe` 由 `src/go` 构建，三个文件（`a.txt` / `b.txt` / `sub/c.txt`）+ 一个空目录 `empty/`，
`--feishu-state-file .scratch/inc/state/aaaaaaaabbbbbbbb.state.json`。

| 轮次 | 操作 | `new` | `replaced` | `unchanged` 跳过 | 重复告警 | 结论 |
|---|---|---|---|---|---|---|
| 1 | 首次同步 | 3 | 0 | 0 | 0 | ✅ 全传 |
| 2 | 什么都不改 | 0 | **0** | **3** | 0 | ✅ **零上传，增量生效** |
| 3 | 改 `a.txt`（mtime 变化） | 0 | 1 | 2 | 0 | ✅ 只传一个 |
| 4 | 改 `b.txt`（**mtime 不变**，size 变） | 0 | 1 | 2 | 1 | ✅ size 兜住了 mtime 被保留的改动 |
| 5 | 删掉 state 文件 | 0 | 3 | 0 | 2 | ✅ 降级为全重传、**不删除**，重复副本由此产生 |
| 6 | 把 state 写成垃圾 | 0 | 1 | 2 | 3 | ✅ 主文件坏 → 从 `.bak` 恢复并告警 |
| 7 | 恢复正常 | 0 | **0** | **3** | 3 | ✅ 回到增量 |

**SC-011 结论**：删除 state 后重跑，云端条目**只增不减**（run 5 里 `replaced=3` 全是新增 + 删旧，
但因为降级禁止删除，旧副本全部保留）。这符合 `contracts/state-file-format.md` §5 的约定：
**宁可多传（可清理）也不误删（不可恢复）**。

**run 5 暴露的真实缺口**：降级重传会产生同名重复副本，而 `Fs.List` 会把同名条目折叠成一条
（`pickRemoteEntry`），因此 `purge` 只能删掉折叠后的那一个，**其余副本 rclone 看不到、删不掉**。
`weftsync` 不会自动删它们（无法证明是自己传的，删错不可恢复）。
清理手段与实现方案归入 F3，见 `docs/roadmap.md`。

**FR-010（下载原子落盘）判定**：不需要自己实现。rclone 的 `fs/operations/copy.go:93-119`
`checkPartial` 依据**目标端**的 `dstFeatures.PartialUploads` 决定是否「写临时名 → rename」：
下载时目标是 local backend（`PartialUploads=true`）→ 自动 temp+rename；
上传到飞书时目标是我们（`PartialUploads=false`）→ 直接写真名（飞书没有 rename API）。
`--inplace` 默认 false，`--partial-suffix` 默认 `.partial`。

---

## 12. 分片上传实测记录（2026-11-06，真实租户）

三个文件一次上传：`exact20.bin` 20MiB（正好在单次请求边界）、`twentyone.bin` 21MiB、
`sixty.bin` 60MiB。内容为确定性伪随机字节（不可压缩、可逐字节比对）。

| # | 检查 | 结果 | 证据 |
|---|---|---|---|
| 1 | 20MiB 走单次请求 | ✅ | 无 `uploading ... block` 日志 |
| 2 | 21MiB 走分片 | ✅ | `uploading "twentyone.bin" in 6 block(s) of 4194304 bytes` |
| 3 | 60MiB 走分片 | ✅ | `uploading "sixty.bin" in 15 block(s) of 4194304 bytes` |
| 4 | 分片大小取自服务端 | ✅ | 日志里的 `4194304` 是 `upload_prepare` 返回值，不是常量 |
| 5 | 回读字节一致 | ✅ | 三个文件 SHA-256 全部 MATCH（见下） |
| 6 | 分片文件参与增量 | ✅ | 第二次运行 `new=0 replaced=0 skipped=3` |
| 7 | 替换分片文件不留重复 | ✅ | 60MiB → 30MiB 后 `replaced=1`，远端条目数仍为 3 |
| 8 | 改一个分片文件后其余仍跳过 | ✅ | 改 `a.bin` 后 `replaced=1 skipped=2`，b/c 均有 `Unchanged skipping` |

```
exact20.bin      MATCH  9A6E8DABC759D2AB vs 9A6E8DABC759D2AB
sixty.bin        MATCH  844063F621231433 vs 844063F621231433
twentyone.bin    MATCH  DA757954AE99D915 vs DA757954AE99D915
```

### 缺陷 B（收尾时才发现）：替换时旧版本从未被删除

`purge` 之后云端总是剩 2 个文件、目录删不掉。用**原始 API 列表**（不折叠同名）一看：
每个换过内容的文件都留下了两个同名副本。

根因：`Object.Update` 在 `Put` **之后**才去读旧 token，而 `Put` 内部已经把 state 改成新
token，于是 `oldToken == newToken`，「删旧」那一步被无条件跳过。

**为什么之前没发现**：`Fs.List` 按设计折叠同名条目，所以 `lsf` 永远只报 1 条。
SC-009 / SC-010 之前的结论是用 `lsf` 量的，**用一个会隐藏问题的工具去证明问题不存在**。这两条结论已作废，见 `docs/decisions.md` D24。

修复后用原始列表重测：

```
=== upload 3 files (25MB each = chunked) ===
/_dup  items=3 files=3 dirs=0   duplicate-names=0
=== replace the 1MB file (direct path) ===
/_dup  items=3 files=3 dirs=0   duplicate-names=0
=== replace the 25MB file (chunked path) ===
/_dup  items=3 files=3 dirs=0   duplicate-names=0
=== replace it again, twice in a row ===
/_dup  items=3 files=3 dirs=0   duplicate-names=0
=== download check: content must be the latest ===
  small.bin    MATCH
  chunked.bin  MATCH
  other.bin    MATCH
```

回归测试 `TestUpdateDeletesPreviousToken` 已加入，并且**验证过它真的会红**：
把修复临时回退后该测试立刻失败（`deleted tokens = []`）。

### 缺陷 A（先被发现）：>8MiB 下载被静默截断

第一次跑上表时三个文件的 SHA-256 **全部不一致，且彼此相同**。查下去发现下载文件都是
**8388608 字节整**（8MiB）——`client.do` 把所有人都按 `maxResponseSize = 8MB`
（本来是给 JSON 列表用的护栏）`LimitReader` 了一遍，而下载走的是同一条路径。

后果：**任何大于 8MiB 的文件下载回来都是坏的，而且不报错**。

**这条也说明 SC-002 之前判错了。** SC-002 的要求是「下载 **10MB** 文件后 SHA-256 一致」，
但 F1 验收时我用的是一个小文件（`AA5B2942...`），10MB 那一档从来没跑过——
而 10MB > 8MiB，按当时的代码必然被截断。**小文件通过不等于需求通过**，
我把「有产出」当成了「满足需求」。现在 20MiB / 21MiB / 60MiB 三个文件都逐字节一致，
SC-002 才算真的有证据。

修复（见 `docs/decisions.md` D22）：下载改走独立的 `downloadStream`，
直接返回 `resp.Body` 流式读取，不再经过 JSON 的缓冲与上限。附带修掉两件事：

1. **内存**：1GB 文件不再需要 1GB 内存。
2. **Range 语义**：`downloadStream` 现在带回「是否 206」。原先 `Open` 无条件
   跳过 `offset` 字节，如果服务器**已经**按 Range 只返回那一段，就会把整段吃掉
   （实测 `TestOpenHonoursRange` 返回 0 字节）。

### 观测工具要求

凡是要断言「云端没有重复」，**必须**用 `.scratch/raw_list.py`（直接调原始 API 并按名字统计），
**不能**用 `weftsync lsf` —— 后者会折叠同名条目。
