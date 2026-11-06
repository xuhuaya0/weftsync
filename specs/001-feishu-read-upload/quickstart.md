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
| □ | 21MB 报错含 "20MB" | US3 / SC-004 |
| □ | 连跑 3 次条目数不变 | SC-009 |
| □ | 改 1 文件后仍 1 条 | SC-010 |
| □ | 删 state 后不重复上传 | SC-011 |
| □ | 缺 scope 错误含 scope 名与链接 | US1 / SC-006 |
| □ | `go test ./...` 全绿 | 单元 |
| □ | Linux 跑通上述全部 | SC-007 |
