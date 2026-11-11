# 决策记录

记录**为什么**这么做。计划本身见 `PLAN.md`。

---

## D1 · 远端与分支约定（2026-11-04）

**决定**
- 远端单一 `origin` = `git@github.com:xuhuaya0/weftsync.git`，走 SSH
- 主分支 `main`，已设 upstream，日常 `git push` 不带参数
- 初始提交 `722020a`，内容为 Spec Kit 脚手架

**理由**
- SSH 已验证可用（`Hi xuhuaya0! You've successfully authenticated`），无需管理 token
- 远端当时为空仓库，无历史冲突，直接 `git init -b main` 最简

**代价**
- 换机器需自行配置 SSH key 与 `.venv`

---

## D2 · 方向未确认前不写业务代码（2026-11-04，**已失效**）

**决定**：只做工程基建，不创建 `src/` 下任何模块。

**理由**：仓库名是唯一线索，产品意图不明。

**结局**：决定本身正确，但我在 `PLAN.md` 里补了三个**凭名字臆测**的方向（A/B/C），
已被用户指出「我还啥都没说啊」。臆测部分已全部删除，见 D6。

**状态**：用户已于 2026-11-04 给出真实需求，本决策退出，以 `PLAN.md` 为准。

---

## D3 · 统一换行符为 LF（2026-11-04）

**决定**
新增 `.gitattributes`：`* text=auto eol=lf`；二进制类型显式标记；随后 `git add --renormalize .`。

**理由**
- 本机 `core.autocrlf=true`，导致每次提交刷 31 条 `LF will be replaced by CRLF` 警告
- 警告虽不影响功能，但会淹没真正的错误信息
- 仓库内统一 LF，跨平台（含 WSL / Linux CI）行为一致

**代价**
- Windows 上工作区文件也是 LF；PowerShell 5.1 能正常读取 LF 脚本（已实测 `.specify/scripts/*.ps1` 为 LF）
- 若将来出现 `\r\n` 敏感的批处理，单独为 `*.bat`/`*.cmd` 设 `eol=crlf`

---

## D4 · 临时文件写 `.scratch/`，不写 `%TEMP%`（沿用既有约束）

**决定**
所有中间产物、导出、调试脚本输出一律落在仓库内 `.scratch/`，并已在 `.gitignore` 排除。

**理由**
- VS Code 终端对「工作区外写文件」默认拦截并要求人工审批（`chat.tools.terminal.blockDetectedFileWrites = "outsideWorkspace"`）
- `.scratch/` 同时满足「不触发审批」与「不污染 `git status`」

---

## D5 · 宪法先出草案、不直接替换正式文件（2026-11-04）

**决定**
原则写入 `docs/constitution-draft.md`，`.specify/memory/constitution.md` **保持空白模板不动**。

**理由**
- 宪法的 `Ratified` 字段代表用户正式批准；未经确认就填日期等于伪造审批记录
- 草案可自由改动，正式文件一旦被 `speckit-*` 流程引用就产生约束力

**转正条件**
用户确认草案 → 执行 `speckit-constitution` 写入正式文件并填 `Ratified` 日期。

---

## D6 · 凭据防护：三层机制（2026-11-04）

**背景**
用户明确：远端是**公共仓库**，任何敏感信息不得入库，且马上会提供可用的
飞书 app_id / app_secret 用于实测。所以防护必须在凭据落地**之前**建好。

**决定**（三层）

1. **`.gitignore` 兜底** —— `.env` / `secrets/` / `*credential*.json` / `*secret*` /
   `rclone.conf` / `*.pem` / `*.key` / `probe-output/` 等一律排除
2. **`scripts/check_secrets.py`** —— 按内容特征扫描（不是猜文件名），覆盖：
   私钥块、GitHub token、AWS key、Slack token、飞书 `t-`/`u-`/`cli_`、
   「赋值给 secret 类变量的长字面量」、32 位小写字母数字字面量（飞书 secret 形态）
3. **pre-commit 钩子** —— 提交前跑 `--staged`，命中即中止提交

**已验证（不是「写完就算」）**
- 自测样本 6 处真密钥全部命中，3 处占位符（`your_..._xxxx` / `fldcnXXXXXXXX` / `${VAR}`）零误报
- 全仓库 35 个已跟踪文件扫描通过
- **实拨提交测试**：把 `app_secret = "z9y8..."` 写进 `docs/leak-test.py` 并 `git add`，
  执行 `git commit` → 被钩子拦下，`git log` 未产生新提交

**为什么不满足于 `.gitignore`**
`.gitignore` 只在文件未被跟踪时生效；一旦某次 `git add -f` 或路径变更导致入库，
就再也挡不住。内容扫描是第二道闸。

**已知局限**
- 无法识别「语义上是密钥但形态不特殊」的内容（如自定义长度 token）
- 钩子可用 `--no-verify` 绕过；因此不能替代自查

---

## D7 · 技术路线改为 rclone 二次开发（推翻 D2 期间的初判）

**背景**
用户直接质疑：「为什么不用 rclone？如果用的话你只需要做飞书这一个，其他类型的云盘
大多可以复用啊」。此前我在 `PLAN.md` 第 6 节写了「rclone 成本高于收益」。

**先查证**

| 我的断言 | 查证结果 |
|---|---|
| 「rclone 无官方飞书支持」 | ✅ 属实。官方 overview 约 60 个后端逐个核对无 Feishu/Lark；仓库全文检索 `feishu` / `lark` / `open.feishu.cn` 无结果 |
| 「多 target 非 rclone 强项」 | ❌ **错**。`union` 后端 + 多 remote 即可 |
| 「飞书的缺陷 rclone 表达不了」（隐含） | ❌ **错** |

**推翻依据（rclone Go API 文档 v1.75.2 原文）**

- `fs.Features` 已内建能力声明：`DuplicateFiles`、`CaseInsensitive`、
  `CanHaveEmptyDirectories`、`SlowModTime`、`PartialUploads`
- `SetModTime` 是**可选接口**，不实现就自动降级
- `Fs.Put` 注释：「for unknown-sized objects (indicated by `src.Size() == -1`)」
  → 未知 size 是 rclone 的一等公民
- `fs.ConfigInfo.IgnoreSize` → `--ignore-size`，size 不可比时可关闭比较

**决定**
- 产品 = **rclone fork（新增 `backend/feishu`，Go）** + 极薄封装（Python 可选）
- 不再自研比较/重试/限流/过滤/续传 —— 这些 rclone 已有且更成熟
- 自带约 60 个后端，直接满足 R6（可插拔）与 R7（多网盘）
- 单文件产物，`GOOS=windows` / `GOOS=linux` 交叉编译，满足 R9

**代价（诚实记录）**
- 需要 Go 工具链
- 仍需自己写飞书 API 层（这是唯一真正新增的工作）
- `Size() == -1` 会让默认比较认为「总是不同」→ 必须 `--ignore-size`，
  且该假设必须用实测 E6 验证，否则架构不成立

**教训**
我又一次在**没查证**的情况下给出了带方向的结论（上一次是凭仓库名编三个产品方向）。
规矩：凡是「某方案不可行 / 成本高于收益」的判断，必须附**文档原文或实测**，
否则不许写进计划。

---

## D8 · GitHub SSH 走 443 备用通道（2026-11-04）

**现象**
本会话中途 `git push` 突然全部失败：

```
git@github.com: Permission denied (publickey).
```

**排查过程与证据**

| 检查 | 结果 | 结论 |
|---|---|---|
| 本地私钥/公钥指纹 | 都是 `SHA256:pU39JoG6...`，一致 | 密钥未被改动（文件日期 2022-11-03） |
| 私钥是否可读 | 文件头是 `OPENSSH PRIVATE KEY` 的 PEM 封装 | 未损坏 |
| `known_hosts` 中 github 记录 | `ssh-ed25519` + `ssh-rsa`，与 GitHub 公布指纹一致 | **无中间人** |
| DNS | `github.com` → `20.205.243.166` | 正常 |
| 端口可达性 | 22 与 443 均 `True` | TCP 层都通 |
| `ssh -T git@github.com`（22 端口） | ❌ `Permission denied` | 失败 |
| `ssh -T -p 443 git@ssh.github.com` | ✅ `Hi xuhuaya0! You've successfully authenticated` | **成功** |
| 连续 5 次 443 测试 | OK / FAIL / FAIL / OK / FAIL（2/5） | **间歇性** |

**结论**
密钥有效且就在账号里，用户也确实没动过配置。问题在 **`github.com:22` 这条链路被中间网络
设备干扰**（TCP 能建、认证握手被破坏），且 `ssh.github.com:443` 也有约 60% 的丢包/篡改。
典型表现是「有时成功有时失败」，而不是稳定报错 —— 一开始容易误判成凭据失效。

**决定**
- `origin` 改为 `ssh://git@ssh.github.com:443/xuhuaya0/weftsync.git`（GitHub 官方备用通道）
- 仓库级 `core.sshCommand` 显式指定私钥路径
  （因为本机 `HOME=C:\SPB_Data`，而密钥在 `C:\Users\Jlsemi-62\.ssh`，默认查找会落空）
- 推送失败**自动重试**，最多 10 次

**代价**
- 443 通道仍有丢包，脚本化推送需要重试逻辑
- 换机器/换网络时需要重新确认这条配置

**教训**
「之前能用，现在不能用」不等于「有人改了」。
按「本地密钥 → 主机指纹 → DNS/端口 → 端口级差异 → 重复采样」的顺序排查，
**用重复采样区分「稳定失败」和「间歇失败」**，才能定位到链路而非账号。

---

## D9 · 最小权限方案：不需要 drive:drive（2026-11-04）

**背景**
最初 `create_folder` 报错，提示需要 `[drive:drive, space:folder:create]` 之一。
我当时的建议是「加 `drive:drive`」。用户反馈 **`drive:drive` 无法获批**，
只申请到了 `space:folder:create`。

**实测结果（申请后重跑）**

全部所需操作均通过，`drive:drive` 不在其中：

| 操作 | 结果 |
|---|---|
| `files.list` | ✅ |
| `create_folder` | ✅（`space:folder:create` 生效后） |
| `delete`（文件/目录） | ✅ |
| `upload_all` | ✅ |
| 分片 `prepare/part/finish` | ✅ |
| `copy` / `move` | ✅ |

**决定**
采用**最小权限**方案：文件读写类 scope + `space:folder:create`。
不申请 `drive:drive`。

**教训**
- 遇到「缺 scope」报错时，报错里列的是**任一可用**的候选，不是全部必需。
  我一开始把 `drive:drive` 当成首选推荐，实际它是最重的那个选项。
  **应该先看哪个 scope 语义最窄，而不是列表里第一个。**
- 用户侧「无法获批」不是障碍，而是约束条件 —— 应该当成需求的一部分去满足。

---

## D10 · 分片上传与 20MB 边界（2026-11-04）

**决定**
`Put()` 按大小分流：≤20MB 走 `upload_all`，>20MB 走分片。

**实测依据**

| 场景 | 结果 |
|---|---|
| `upload_all` 21MB | `code=1061002 params error.` → **20MB 是硬限制** |
| 分片 25MB | ✅ 成功 |
| `prepare` 返回 | `block_size=4194304`（4MB）、`block_num=7` |
| 结果校验 | `Content-Range: bytes 0-0/26214400` = 25MB，与本地字节数**完全一致** |
| 耗时 | 25MB / 8.3 秒 ≈ 3 MB/s |

**关键约束**
- **分片粒度由服务端 `prepare` 决定**（4MB），客户端不得写死 ——
  文档未承诺固定值，未来可能变化。
- `checksum`（Adler-32）是**可选**参数；实测不传也能成功。
  注意 Python 里 `adler32` 在 `zlib` 而非 `hashlib`（我踩过一次）。

**影响**
- rclone 的 `OpenChunkWriter` 接口正好对得上：`ChunkSize` 每次由后端返回
- 断点续传需要持久化 `upload_id`（有效期内可继续传剩余分片）

---

## D11 · 不 fork rclone，把它当依赖库（2026-11-04）

**背景**
原计划是 `vendor/rclone` + fork。实际尝试克隆时发现 GitHub HTTPS 也被干扰
（`Recv failure: Connection was reset`），5 次重试全部失败。

**替代路径**
走 Go 模块代理（`goproxy.cn`）拉取 rclone 源码：

```
go get github.com/rclone/rclone@v1.71.2
```

一次成功。rclone 的根目录只有一个 `rclone.go`：

```go
import (
    _ "github.com/rclone/rclone/backend/all"
    "github.com/rclone/rclone/cmd"
    _ "github.com/rclone/rclone/cmd/all"
    _ "github.com/rclone/rclone/lib/plugin"
)
func main() { cmd.Main() }
```

**决定**
不 fork。新建独立模块 `src/go`（`module weftsync`），依赖 rclone，
`main.go` 照抄上面结构 + 多一行引入我们自己的 `weftsync/backend/feishu`。

**验证（实测）**
- `go build` 成功
- `weftsync version` → `rclone v1.71.2-DEV`
- `weftsync help backends` → 列表里有 `feishu   Feishu / Lark Drive`
- `scripts/build-go.ps1 -Target both` 交叉编译出 Windows 66.2MB + Linux 66MB
- Linux 产物魔数 `7F 45 4C 46`（ELF）确认正确

**为什么这比 fork 好**
| 维度 | fork | 依赖库 |
|---|---|---|
| 跟进上游 | 要 rebase / cherry-pick | 改版本号即可 |
| 代码量 | 整个 rclone 进仓库 | 我们只有几个文件 |
| 上游 bug 修复 | 手动同步 | `go get -u` |
| 构建产物体积 | 一样 | 一样（都链接了 backend/all） |

**代价**
- 无法修改 rclone 内部代码。目前的需求（写一个新后端）不需要改内部。
- 若将来真需要改内部，可以用 Go 的 `replace` 指令指向本地 fork，不必改架构。

**教训**
「必须 fork」是我没验证的假设。实际上 rclone 的 `fs.Register` 是公开 API，
自定义构建是官方支持的用法。**先找有没有更轻的集成方式，再决定要不要 fork。**

---

## D12 · E6 结论：size=-1 无害，但 mtime 语义是真坑（2026-11-04）

**背景**
原计划把「`Size() == -1` 导致每次全量重传」列为本项目最大架构风险（B3 副作用）。

**验证方式**
写了 `src/go/probes/e6_size_probe.go`，直接调用 `operations.Equal`，
用 stub 对象构造 7 种组合。**不接网络，可重复运行。**

**结果：原判断是错的。** 源码里：

```go
if ci.IgnoreSize || src.Size() < 0 || dst.Size() < 0 {
    return false        // sizeDiffers = false，即「size 不构成差异」
}
```

`Size() < 0` 只是**跳过** size 检查，比较继续走 mtime。

**但发现了真问题：mtime 语义不一致。**
飞书 `modified_time` 是**上传时刻**，本地 mtime 是**真实修改时间**，两者天然不同。
在 `Precision() = 1s` 下 → 每轮都 DIFFER → 每次全量重传。

而 `--checksum` 也救不了：无 hash 时源码直接 `return false`。

`Precision() = ModTimeNotSupported` 同样不可取 —— 用例 D 证明它会让
「本地改了也永不重传」。

**结论**
E6 风险**没有消除，只是换了位置**：从「size 问题」变成「mtime 问题」。
三种应对方案已列入 `PLAN.md` 3.2，**需要用户拍板**，因为涉及
「对远端变化失明」这个 R4（多机器）相关的取舍。

**教训**
- 把「风险」写进计划时，必须区分**已验证的事实**和**推断的后果**。
  我写「会导致每次全量重传」时并没有读过 `sizeDiffers` 的实现。
- 能用 stub 直接测的函数，就不要靠读代码猜。20 行 Go 换来确定性答案。

---

## D13 · mtime 问题采用「影子 mtime + token 追踪」，冲突默认 report（2026-11-05）

**背景**
E6 证明真正的问题是 mtime 语义不一致（飞书 `modified_time` = 上传时刻，本地 mtime = 真实修改时间），
不是 size。同时用户明确了 R4 的实际形态：

> 「多设备按道理应该不会有同名的文件需要备份到一个地方。但是如果出现这种情况，
> 我还想需要能分辨出来。」

即：**同名冲突预期罕见，但必须可检测、可区分**。

**关键观察：飞书上的冲突是「可检测的硬信号」，不需要靠 mtime 猜。**
这来自 E1 + E2 的组合效应：

| 事实（均已实测） | 后果 |
|---|---|
| E1：同名重传产生**新 token**，没有原地更新 | 别的机器上传同名文件时**不会修改**已有文件 |
| E2：同一文件夹**允许同名共存** | 会**新增**第二条同名记录，token 不同 |

所以「远端被改」表现为：token 变了，或同名出现多条。两者都是确定的标识符比较，
不是时间戳推断。

**决定**

1. **增量判断**：`ModTime()` 返回「上次同步时记录的本地 mtime」（影子），
   而非飞书的上传时刻。本地没变 → EQUAL → 跳过；本地变了 → 上传。
2. **冲突检测**（三种硬信号）：state 中的 token 在云端消失 / 某名字出现 ≥2 条 /
   同名但 token ≠ state 记录。
3. **默认策略 `report`**：只输出冲突表，**不改动任何云端文件**，退出码非 0。
   另有 `keep-both`（本地那份改名）与 `overwrite`（显式覆盖）可选。

**为什么不选方案 C（自建三方比较引擎）**
冲突可检测靠的是 token，而 token 本来就由 backend 的 `List` 返回。
在 rclone 之上做一层 scan/resolve 就能拿到这些信号，
不需要重写同步引擎、也不需要放弃 `--max-delete` 等现成保护。

**为什么默认不用 `keep-both`**
`keep-both` 会自动往云端写新文件。用户要的是「能分辨出来」，
自动处理反而会让云端产生预期外的文件。安全默认 + 显式覆盖更符合这一诉求。

**风险与代价**
- 需要持久化 state（本地 sqlite 或 json）。首版用 json 足够，条目少且可读。
- 影子 mtime 只在本机可信。换机器/清 state → 首次同步会把全部文件视为「新增」
  （`report` 模式下只报告不写入，不会造成破坏）。
- 若将来 rclone 上传了从别处复制来的文件（token 非本机产生），
  state 中缺少对应记录 → 会报告为冲突。这是可接受的保守行为。

---

## D14 · 术语与设计对齐 FreeFileSync（2026-11-05）

**背景**
用户要求："参考 FreeFileSync"，并明确指出我用术语却没解释。
我先试图用 `fetch_webpage` 抓 `manual.php?page=...`，**全部只返回导航栏**；
后来发现真实 URL 是 `manual.php?topic=...`，且页面内容需要浏览器渲染。
用内置浏览器打开后读到了全部正文，并截图了同步设置对话框原图。

**我此前凭记忆说错的（已纠正）**

| 我之前的说法 | 实际（手册原文 + 截图） |
|---|---|
| 「四种变体 Two Way / Mirror / Update / Custom」 | **三种基本变体**（Two way / Mirror / Update）**+ Custom**。原文："Three basic variants are available" |
| 「删除保护是 Recycle Bin / Versioning，没有 max_delete」 | 对一半：选项叫 **Delete and overwrite**，三档 `Recycle bin` / `Permanent` / `Versioning`。而且管的是**删除+覆盖**两类 |
| 不知道 Versioning 的细节 | 有**三种命名约定**：`Replace` / `Time stamp [File]` / `Time stamp [Folder]`，还可用 `%date%` 宏控制粒度 |
| 没提比较策略 | **三种比较变体**：File Time and Size / File Content / File size。其中后两者手册明确用于 **"modification times are not available or reliable"** |

**核实到的关键事实**

| 事实 | 对本项目的意义 |
|---|---|
| `Use database file to detect changes`（`sync.ffs_db`）| **验证了我们的影子 mtime 设计**。FFS 也是用数据库记录上次同步状态来判定变更与方向 |
| `Recycle bin` **"only available for local drives"** | 飞书云盘**不能用这一档**，只能用 Versioning 模拟 |
| `FileTimeTolerance` 默认 **2 秒** | 我们比较 mtime 也该有容差 |
| `LockDirectoriesDuringSync`（`sync.ffs_lock`）**主要用途是多用户并发写同一网络文件夹** | 正是 R4 的 FFS 解法，但它是**文件锁**；飞书无原子锁，**无法照搬** → 我方的 token 冲突检测才是替代方案 |
| **FFS 没有内置调度器**，批处理靠 `Windows Task Scheduler` / `cron` | 我们 R11/R12 要常驻 + 定时，与 FFS 不同 → **自建调度器** |
| FFS 有**通知区域（托盘）图标**，双击出进度 | 与用户选定的「本地网页 + 系统托盘」一致 |
| 有**邮件通知 + 严重级别** | 对应 B14 通知渠道待定 |

**决定**
1. **术语全面改用 FFS 用词**：`deletion`（`permanent`/`versioning`）、`versioning_naming`、
   `compare strategy`、`time_tolerance_sec`、`variant`（`two-way`/`mirror`/`update`/`custom`）。
   弃用我自造的 `recycle` / `recycle_dir`。
2. **`max_delete` 保留，但明确标注它是 FFS 没有的额外安全阀**。
   理由：FFS 靠「先预览再人工确认」，我们定时无人值守，没有那道关卡。
3. **比较策略默认 `state+size`**，不含 `content`（飞书无 hash，下载比对太贵）。
4. **UI 结构照 FFS 三段式**：Comparison / Filter / Synchronization 三个页签，
   变体带方向示意图，Custom 用 Create/Update/Delete × ←/→ 矩阵。

**教训**
- 用户说「你实在不行打开截图看呢，不要让我判断」是对的。
  **能自己查证的，就不要让用户替我的记忆背书。**
- `fetch_webpage` 抓不到内容时，换内置浏览器渲染页面 + 截图，是可行的第二条路。
- 我对竞品的记忆有实质性错误（变体数量、删除档位名称、Versioning 细节）。
  **凡引用外部产品行为，必须实读文档，不能凭印象。**

---

## D15 · 宪法改为「通用准则为主干」（2026-11-06）

**背景**
用户问：「这个宪法不应该是通用的么？如果我没有什么要求，你是不是应该放一些大家验证的比较好的通用准则」

**先承认问题**
v0.1 的 5 条原则**全部**来自「本项目已经犯过的错」：
验证优先（因为我未验证就宣称完成）、计划落盘（因为我一口气写 5 个文档）、
脚本可复现（因为曾误删 35 个文件）、按内容扫描（因为文件名拼错漏删）、
工作区边界（因为往 %TEMP% 写过）。

这是**补丁集合**，不是**地基**。用户是对的：宪法应该先是通用的工程准则，
项目特异的约束另开一节。

**决定：v0.1 → v0.2**

核心原则换成 8 条通用准则，每条附「出处」：

| # | 原则 | 出处 |
|---|---|---|
| I | 简单优先（KISS / YAGNI） | XP 实践、UNIX 哲学 |
| II | 验证优先于断言 | 经验主义、TDD |
| III | 失败要显式（Fail Loud） | Fail-fast, Martin Fowler |
| IV | 单一事实来源 | DRY, Pragmatic Programmer |
| V | 边界与最小权限 | Saltzer & Schroeder 1975 |
| VI | 可复现 | Reproducible builds, IaC |
| VII | 渐进交付 | 迭代开发、CI |
| VIII | 改动前先理解 | Chesterton's Fence |

项目特异内容移到第 2 节「项目约束」：平台（Windows/Linux）、
安全红线（公开仓库、凭据不入库）、技术栈（Go/rclone）、
工程约定（测试门禁、PS 5.1 纯 ASCII）。

**为每条加上「如何检查」**
这是关键改动 —— 否则 Constitution Check 只能主观判断。
每条原则都给出**可对照的具体检查点**，例如：
- 原则 II：结论能否指出证据（命令输出/测试名/文档链接/源码行号）？
- 原则 V：破坏性操作的三条保护（确认/快照/撤销）是否齐全？
- 原则 VI：删掉产物后能否用脚本完整重建？

**保留的态度**
- 例外是允许的，但必须写明理由 + 更简单方案为何不可行
- 未说明理由的违反 = 不合规

**未做的**
- 没有把 `Ratified` 日期填上 —— 那代表用户的正式批准，我不能自己写
- 没有替换 `.specify/memory/constitution.md` —— 等用户确认草案后再转正

**教训**
「从错误中学习」和「采用公认准则」是两件事。
前者产生的是补丁，后者才是基线。
**先问「这个领域公认的最佳实践是什么」，再叠加本项目特有的教训。**


---

## D6 · 删除凭仓库名臆测的产品方向（2026-11-04）

**背景**
我根据 `weftsync` = weft（纬线）+ sync，臆测了三个方向（测试结果编织器 /
多设备配置同步器 / 通用 ETL），并写进 `PLAN.md` 让用户在假设里选。
用户回复「我还啥都没说啊」—— 完全正确，那是无依据的脑补。

**决定**
1. `PLAN.md` 第 3 节（三个假设）整段删除，替换为用户实际给出的需求
2. 记录教训：**线索（仓库名）≠ 需求**。没有用户输入时不生成「候选方向」

**真实需求（用户原话要点）**
本地文件 → 飞书云盘的单向同步工具；飞书走**应用权限**认证；支持完全/增量等模式；
多本地机器同步到同一云盘；双向同步尽力而为，退路是单独做「云盘 → 本地拉取」；
后端可插拔（未来换 SMB 或其他网盘）；多网盘同步为可选高级功能；可借鉴现有轮子。

---

## D7 · 后端抽象必须携带「能力声明」，而非只有方法签名（2026-11-04）

**决定**
`Backend` 接口除方法外，必须暴露 `Caps`（has_size / has_mtime / has_hash /
can_update_in_place / can_set_mtime / recursive_list / max_single_upload / rate_limit_qps）。
同步引擎按能力**降级决策**，不假设所有后端行为一致。

**理由**
实测飞书官方文档后确认，飞书与本地/SMB 的能力差距是数量级的：
- 飞书列目录**不返回 size，也没有 content hash**，`modified_time` 只有秒级
- 飞书只能按 token 寻址，没有真实路径；上传 5 QPS、单文件 20MB
- 本地/SMB 应有尽有

若只抽象成 `upload/download/list` 三个方法，引擎必然写出「假设有 mtime/hash」的逻辑，
换后端时静默出错。能力声明把「不确定」显式化。

**代价**
Planner 逻辑会分支变多；每个后端都要诚实填写 Caps（填错比不填更危险）。

---

## D8 · 默认不删除远端文件，删除必须显式开关 + 数量熔断（2026-11-04）

**决定**
- `delete_extra` 默认 `false`
- 开启后仍受 `max_delete`（默认 50）限制，超限即整体中止

**理由**
单向同步最危险的失败模式是「本地根目录配错 → 远端被镜像清空」，且飞书端删除无法从本工具恢复。
熔断把「配置错误」从灾难降级为可发现的报错。

---

## D9 · 不引入飞书官方 SDK（lark-oapi），用 httpx 直连（2026-11-04，建议）

**决定**：HTTP 层自己薄封装，只依赖 `httpx`。

**理由**
- 需要自己控制 `tenant_access_token` 的缓存与提前刷新（文档明确：剩余 <30min 时调用会产生两个并存 token）
- 需要按错误码分类重试（`1061045` 可重试 vs `1061004` 不可重试）
- 需要在 5 QPS / 10000 次每日的限额下做全局限速，SDK 不提供

**代价**：endpoint 变更要自己跟；只用到少数几个接口，风险可控。

---

## D10 · 先做探针（M0）再写引擎（2026-11-04）

**决定**
在写同步引擎前，先完成 E1–E5 五个实测（`PLAN.md` 第 3 节），结论写入 `docs/feishu-probe.md`。

**理由**
这五个答案直接决定：增量是覆盖还是删+传、路径映射是否可靠、冲突检测是否可信。
猜错的代价是整个 Planner 重写。

**退出条件**
E1–E5 全部有答案且有原始返回记录 → 进入 M1。

---

## D16 · 增量不碰 rclone 的比对逻辑，而是让 Object 报「影子 mtime」（2026-11-06）

**背景**
飞书列出目录不返回 size，`modified_time` 是**上传时刻**而不是文件修改时间（E3/E5）。
rclone 的同步引擎用「src 的 mtime/size vs dst 的 mtime/size」判等，因此不加干预时
**每一轮都会判定全部不同 → 全量重传**，直接烧掉每天 10000 次的上传额度。

**候选方案**

| 方案 | 问题 |
|---|---|
| A. fork rclone 改比对函数 | 宪法 II/D11：能不改依赖就不改；维护成本高 |
| B. 自己写同步循环，不用 rclone 引擎 | 放弃校验、重试、统计、断点等成熟能力，工作量大 |
| C. 让 `Object.ModTime()`/`Size()` 在可信时返回「上次上传时的本地指纹」 | 零改动引擎 |

**选 C**

**做法**
state 里为每个已同步文件记下 `local_mtime` / `local_size` / `remote_token`。
列目录时若「云端当前 token == state 记录的 token」，则 `ModTime()`/`Size()`
返回**当时本地的值**；否则返回云端真实值。

于是 rclone 拿「当前本地文件」跟「上次上传时的本地文件」比 —— 两个值语义相同、同一台机器、无时区问题。
token 不相同意味着云端那份不是我们传的（被替换/删除/别人动过），此时立刻退回真实 `modified_time` 强制重传。

**为什么 token 校验不能省**
没有它，云端的文件被别人删掉或替换后，`ModTime()` 仍会返回我们的旧指纹，
rclone 判定「未变化」而跳过 → **云端少了文件却没人知道**。这是静默的数据缺失。

**实测**（真实租户，7 轮，见 `quickstart.md` §9）
第二轮 `new=0 replaced=0 unchanged=3`；改一个文件只传一个；mtime 不变但 size 变也能识别。

---

## D17 · 锁在 state 包里实现，但 F1 的 backend 不自动持有（2026-11-06）

**背景**
同一 pair 若被两个进程同时同步，会各自以为掌握云端状态，可能互删对方刚传的文件。

**先说一个被实测推翻的假设**
`AcquireLock` 最初有个「PID 等于自己 → 视为自己的陈旧锁并接管」的分支，
理由写的是「上次崩溃留下的」。测试立刻打脸：同进程第二次加锁成功了。
根因是这个分支本身多余 —— **进程崩溃后 PID 就不存活了，`pidAlive` 会返回 false，正常走接管路径**；
而 PID 仍然存活，就只能是我们自己持有。删掉该分支。

**第二个被推翻的假设**：进程内也应该互斥。
不成立 —— rclone 会为同一个 remote 建多个 Fs 实例（`NewFs` 带文件路径时还会再建一次），
进程内互斥会让正常使用直接卡死。改为**进程内共享 + 引用计数，跨进程互斥**。
并且用**真实子进程**验证（`TestLockIsExclusiveAcrossProcesses`），不用同进程模拟代替。

**决定**
- `state.AcquireLock` 保留并测试通过，但 F1 的 `newFs` **不调用**它
- 原因：`fs.Fs` 没有关闭钩子，拿不到自然的 `Release()` 时机；每次退出都留一个锁文件，
  下一轮就要打印「接管陈旧锁」告警 —— 用噪声换一个 F1 还没有的并发场景
- 真正的持有者应该是 F5 的 daemon / 未来的 `weftsync sync` 命令，那里有明确的生命周期
- F1 的兜底：state 的写入是「原子替换 + CRC」，两个进程并发写最坏是丢一次记录 →
  下一轮走降级模式（重传、不删除）。**不会丢数据，只会多传**

---

## D18 · `Rmdir` 只删空目录，且 `Rmdir("")` 允许删 Fs 根（2026-11-06）

**背景**
F1 原本计划让 `Rmdir` 返回 `fs.ErrorCantRmdir`（「F1 不删目录」）。

**实测发现这会造成最坏的失败形态**
`weftsync purge remote:path` 返回 **exit=0 且什么都没删**。用户看到成功，实际上文件还在。
对一个「云端删除不可恢复」的产品，静默的成功最危险。

**根因（两个叠加）**
1. 返回 `ErrorCantRmdir` 让 purge 的收尾 rm`dir("")` 必然失败
2. 更隐蔽：`Rmdir` 里我把 `f.root` 又拼了一次路径，变成 `_inc_test/_inc_test`，
   所以连子目录也永远「directory not found」

**决定**
- `dir` 一律按「相对 Fs 根」解释，**不再拼 `f.root`**
- 删除前**先用新鲜列表确认目录为空**，非空则明确拒绝并报出条目数
  理由：飞书删目录是**递归**的，且没有回收站；空目录检查是唯一能防「连带删掉内容」的闸门
- `Rmdir("")`（删除 Fs 根本身）**允许**，这是 `purge remote:path` 的正常语义
- 唯一永不允许的是删除 `root_token` 指向的那层 —— 那是用户的云盘目录，不是我们建的

---

## D19 · 降级重传产生的同名副本，**不自动清理**（2026-11-06）

**事实**
state 丢失/损坏时，按 `contracts/state-file-format.md` §5 必须「不跳过、不删除」，
于是把本地全量重传一遍，云端出现同名重复副本。实测确认了这一点（`quickstart.md` §9 run 5）。

**更糟的一半**
`Fs.List` 会把同名条目折叠成一条（`pickRemoteEntry`，为了避免 rclone 见到同名对象而误删），
所以 `purge` 只能删掉被折叠出来的那一个，**其余副本 rclone 根本看不到，也删不掉**。

**为什么不自动清理**
要清理就得判断「哪一条是我传的」。降级模式下这个信息恰好就是我们丢掉的东西。
若按「删掉除最新一条以外的同名项」去猜，会在多台机器同步同一云盘时
**删掉另一台机器刚传的新版本**，而飞书没有回收站。

**决定**
1. 降级告警里明确写出「weftsync 不会替你删这些重复副本」及原因
2. 自动清理方案（按 state 记录精确删除多余 token）归入 **F3**，且必须满足 FR-035 的二次确认
3. 现阶段用 `.scratch/cleanup_remote.py`（开发工具，不随产品发布）清理测试租户

**教训**
「可清理」不等于「可接受」。降级路径的后果必须写在用户看得见的地方，
而不是在 contract 里写一句「可能包含重复」就算交代了。

---

## D20 · 错误码含义只能有**一张表**（2026-11-06）

**背景**
上线真实租户后 `files.delete` 间歇返回 `code=1061001`（msg 是 "unknown error"），
同一条删除重试即成功。我把它加进了 `APIError.Retryable()` 的可重试集合。

**但重试没发生**
因为 `businessError()` 里还有**另一份**硬编码的「哪些码包装成 `APIError`」清单，
1061001 不在其中 → 永远变成 `UncategorizedError` → `classify()` 根本看不到它。
三处清单（构造函数清单 / `Retryable()` / `classify()`）各自为政，改一处等于没改。

**决定**
引入 `codeTable map[int]codeInfo{retryable, class}` 作为**唯一事实来源**，
三个判断全部查它。并加 `TestCodeTableIsSelfConsistent`：
遍历表里每个码，断言「businessError 产出 `*APIError`」「`Retryable()` 与表一致」「`classify()` 与表一致」。
以后再往表里加码，三处不可能再漂移。

**教训**
同一份知识在两个地方各写一遍，**不是「冗余」，是「迟早不一致」**。
发现自己在第二个地方补同一个常量时，就该先把它们合并。

---

## D21 · 分片上传串行发块，不实现 OpenChunkWriter（2026-11-06）

**背景**
F2 要解除 20MB 限制。rclone 提供了 `fs.OpenChunkWriter`（`ChunkWriterInfo` 里有
`Concurrency` 字段）用于并发分片上传，看起来正好对口。

**官方文档写得很明确**（`upload_prepare` / `upload_part` / `upload_finish` 三页都有）：

> This API does not support concurrent calls, and the call frequency limit is
> 5 QPS, 10,000 times/day. Otherwise, error code 1061045 will be returned.

**决定**
不实现 `OpenChunkWriter`，分片在 `Fs.Put` 里**串行**发块。

**理由**
1. 并发是被官方禁止的，`Concurrency` 只能填 1，实现它没有任何性能收益
2. 实现了反而多出一堆要处理的路径：乱序提交、`LeavePartsOnError`、
   partial commit 与 rclone 重试的交互
3. rclone 只有在 `OpenChunkWriter != nil || OpenWriterAt != nil` 时才走
   `multithreadCopy`；两者都为 nil 时它一律走 `Put`。这正是我们要的
4. 内存仍然只占一个块（服务端给的 `block_size`，4MB），不是整个文件

**其他两条从文档确认的事实**
- `block_size` 是**服务端决定的**，文档明说「The platform consistently chunks files
  into 4MB segments」，但客户端仍不得写死：`prepare` 返回值才是唯一依据
- upload_id 与已传进度**保留 24 小时**，可续传；且**没有 abort 接口**。
  所以中断后只能放任它自己过期，错误信息里必须把这点讲清楚，
  否则用户会以为留下了垃圾数据要自己清

**没有做的**
- 断点续传（F2 验收第 5 条）。要做就得把 `upload_id` 持久化进 state，
  并且能重新定位本地文件的读位置；价值低于先把主链路做稳。已在
  `docs/roadmap.md` F2 中标为未完成

---

## D22 · 下载必须流式，不能复用 JSON 的那条 8MB 上限（2026-11-06）

**怎么发现的**
F2 真实验证里，20MiB / 21MiB / 60MiB 三个文件下载回来 **SHA-256 全不一致，而且三者相同**。
不同大小的文件不可能同哈希 → 一定是都被截成了同一个长度。查文件：
**三个都是 8388608 字节**。

**根因**
`api.go` 里 `maxResponseSize = 8 << 20`，本意是防止一个失控的列表响应吃光内存，
但它被 `client.do` 用在**所有**响应上，包括 `files/{token}/download`。

**为什么 F1 没发现**
SC-002 要求「下载 **10MB** 文件后 SHA-256 一致」，但 F1 验收时我实际用的是一个小文件。
10MB > 8MiB，按当时的代码必然截断。**这是把「跑通了一个例子」当成「满足需求」**，
也正是宪法原则 II（验证优先于断言）要防的事。

**决定**
下载走独立的 `client.downloadStream`：自己做请求与重试，成功后**直接把 `resp.Body`
交给调用方**，不缓冲、不设上限。

**连带修的第二处**
流式之后 `Open` 拿不到状态码了，而它需要知道「服务器是否真的按 Range 返回了 206」：
- 206 → body 已经只是那一段，**不能再跳过 offset**
- 200 → 服务器忽略了 Range，必须自己跳过 offset 并截断

于是 `downloadStream` 返回 `downloadBody{Body, Partial}`。
之前无条件跳过 offset 的写法在服务器**遵守** Range 时会把整段吃掉
（`TestOpenHonoursRange` 实测返回 0 字节）。

**后缀 Range 的特殊处理**
`bytes=-N`（取最后 N 字节）在流式下无法定位，需要总长度。如果服务器忽略了它，
**宁可报错也不返回前 N 字节**——返回错误的数据比返回错误更糟。

---

## D23 · 分片上传改变 SC-004 的语义，改契约而不是绕过（2026-11-06）

F1 时 FR-013 / SC-004 写的是「拒绝 >20MB，错误信息含 20MB 字样」。
F2 交付分片上传后，21MB 应当**成功**，这条要求自动失效。

处理方式：直接改 `spec.md` 的 FR-013、SC-004 和验收场景 4，并在原处标注
「F1 时是 X，F2 后改为 Y」以及原因。

**不这样做的后果**
如果保留旧断言，就会为了「让测试通过」而在代码里留下一个假的 20MB 错误路径，
或者把已经过时的测试删掉却不说。两者都会让下一个读代码的人以为 20MB 是文件大小上限，
而它其实只是**单次请求**的上限。

现在真正需要告诉用户的边界是「文件超过飞书单文件上限」（code `1061043`，
阈值由租户套餐决定），以及租户/个人空间写满（`1061101` / `1061061`）。

---

## D24 · `Update` 读旧 token 的时机错了，而且 `lsf` 掩盖了它（2026-11-06）

**现象**
F2 验证收尾时发现 `purge` 之后云端总会剩 2 个文件、且目录删不掉。
用原始 API 列表（不折叠同名）一看：**每个换过内容的文件都留下了两个同名副本**。

**根因**
`Object.Update` 的顺序是：

```go
newObj := o.fs.Put(...)          // 内部 recordUpload 已把 state 改成新 token
oldToken := o.fs.previousToken() // 读到的是新 token
if oldToken != newToken { ... }  // 永远相等 → 永远不删
```

**为什么这么久没发现**
因为 `Fs.List` **按设计折叠同名条目**（避免 rclone 看到两个同名对象而误删）。
于是 `lsf` 永远只报 1 条，SC-009（连跑条目数不变）和 SC-010（改 1 文件仍 1 条）
都是用 `lsf` 量的 —— **用一个会隐藏问题的工具去验证那个问题不存在**。

这不是实现 bug 连带出的测量失误，是方法论错误：**度量工具必须能看见缺陷本身**。

**决定**
1. 修 `Update`：在 `Put` **之前**取旧 token
2. 新增 `TestUpdateDeletesPreviousToken`，并且**验证它真的会红** ——
   把修复临时回退后该测试立刻失败（`deleted tokens = []`）
3. 新增 `.scratch/raw_list.py`：直接调原始 API 列条目并统计同名重复数。
   以后凡是要断言「没有重复」，必须用它，不能用 `lsf`
4. 真实租户重测：连续 4 次替换（含 1MB 直传与 25MB 分片两条路径）
   → `duplicate-names=0`，且回读内容为最新版本

**教训**
一个为了容错而做的「合并」动作，会顺带把证据一起合并掉。
凡是「我加了一层归一化/去重/折叠」，就必须同时准备一条**绕过它的观测通道**，
否则从那一刻起，这一类缺陷对测试完全不可见。

---

## D25 · `max_delete` 必须自己实现，rclone 的语义是反的（2026-11-07）

**背景**
F3 要「一次同步超过 N 个待删 → 整轮中止，一个都不删」（`PLAN.md` B12，用户 2026-11-05 定）。

**rclone 自带 `--max-delete`，但语义不同**

源码 `fs/accounting/stats.go`：
```go
if ci.MaxDelete >= 0 && s.deletes+1 > ci.MaxDelete {
    return errMaxDelete
}
```
它在**每次删除前**判断，所以是「删到上限再停」。rclone 自己的测试
`TestMaxDelete`（`ci.MaxDelete = 2`，3 个文件）断言**剩下 1 个** —— 即删了 2 个。

实测确认（真实租户，2 个待删文件）：

| 命令 | 结果 |
|---|---|
| `--dry-run` | 2 条 `Skipped delete`，远端仍是 4 个文件 |
| `--max-delete 1` | **`f2.txt: Deleted`**，然后 `--max-delete threshold reached`，远端 4 → 3 |
| 无限制 | 4 → 2 |

对一个**删除不可恢复**的目标，删掉一半就退出比完全不删更糟：用户以为被拦住了，
实际已经丢数据。

**决定**
在 `job` 层做两轮：预检轮用 dry-run 统计，越限则整轮中止；否则执行轮真跑。

**dry-run 能准确统计的前提**（已核对源码 + 实测）：`DeleteFileWithBackupDir` 里
`accounting.Stats(ctx).DeleteFile(...)` 在 `SkipDestructive` **之前**调用，
所以 dry-run 下计数照常累加、只是不执行 `dst.Remove`。

**预检和执行之间目标被改动怎么办**
执行轮结束后再核对一次实际删除数；若超过上限（说明两轮之间别人写了目标），
报错并明确说明。不静默放行。

---

## D26 · 实现服务端 `Move`，以及一个只有真实租户才暴露的「路径坐标系」缺陷（2026-11-07）

**为什么做 Move**
`deletion = versioning` 要把退休文件挪进回收目录。没有服务端 Move，rclone 的
`--backup-dir` 只能「下载 + 重新上传」，对云目标等于把流量付两遍。

**先探针，再写代码**（`.scratch/probe_move.py`，真实租户）：

| 问题 | 实测结果 |
|---|---|
| scope 够不够 | ✅ 移动成功 |
| 文件移动是否同步 | ✅ 同步完成，`data: {}`，无 task_id |
| **移动后 token 变不变** | ✅ **不变**（`CjYxbi...` → 同一 token）→ state 条目仍有效 |
| 移到同名已存在的位置 | 不报错、不覆盖，变成**两个同名条目**（与上传一致） |
| 移目录 | 异步，返回 task_id |
| 频率限制 | **20 次/分钟**（远低于其它接口的 5 QPS） |

**踩到的缺陷（单测抓不到，真实调用才暴露）**

`moveto remote:a.txt remote:sub/a.txt` 报告 `Moved (server-side)`，但文件**根本没动**。

根因：rclone 给 `Move` 的目标 Fs 是**以目标目录为 root** 的（这里 root = `sub`），
而 `remote` 参数是**相对该 Fs 根**的（`a.txt`），`src.Remote()` 却是**相对源 Fs 根**的
（也是 `a.txt`）。我直接比较两个字符串，于是永远相等 → 命中「无需动作」提前返回。

**修法**：两侧都先展开成绝对路径再比较
（`joinPath(srcObj.fs.root, srcObj.remote)` vs `joinPath(f.root, remote)`），
再把「是否需要搬家」「是否需要改名」分成两个独立判断。

**连带影响**
这个缺陷同时让 `deletion=versioning` 失效：`MoveBackupDir` 传进来的目标 Fs root 是
回收目录，`remote` 与 `src.Remote()` 又相等 → 不搬 → 旧文件留在原地，随后新版本上传，
于是每个被覆盖/删除的文件都多出一个**同名副本**。实测确认（`2x keep.txt`）。

**教训**
`src.Remote()` 和 `Move` 的 `remote` 参数**不是同一个坐标系**，rclone 的接口文档不会
强调这一点。这类「两个看起来一样的字符串其实是两种坐标」的 bug，单测只要复用同一个
Fs 实例就永远抓不到 —— 必须用**两个 root 不同的 Fs** 去测
（`TestMoveAcrossDifferentFsRoots`）。

---

## D27 · sync 层放在 `job` 包，而不是塞进 backend（2026-11-07）

**问题**
「该不该删这 900 个文件」不是 backend 能回答的：backend 只看到单次调用。判断需要
**整个 pair 的视角**，还要读配置（variant / max_delete / deletion）。

**决定**
新增 `src/go/job`（配置 + 受保护的运行）与 `src/go/cmd/run`（`weftsync run` 命令）。
backend 保持只做「一个文件的增删改查」。

**不这样做的后果**
如果让 backend 自己拦删除，它就只能看到 `Remove` 被调用的次数，看不到「总共会删多少」，
于是又退回「删到上限再停」——正是 D25 否掉的那种行为。

**顺带定下的两件事**

1. **默认 `variant = "update"`**（不是 mirror）。一个备份工具默认开始删数据，比默认多留
   一些文件危险得多。
2. **未知 variant 直接报错**。拼错成 `mirorr` 时：退化成 update 会悄悄停止同步删除；
   退化成 mirror 会突然开始删。两种都不能接受。
3. **`max_delete` 负数拒绝**。它看起来像「关闭保护」，不提供这个口子。

---

## D28 · 用 `filter.AddConfig` 加排除规则，不能用 `filter.NewFilter` + `ReplaceConfig`（2026-11-07）

**背景**
回收目录在目标内部，rclone 拒绝与目标重叠的 `--backup-dir`，除非该目录被过滤掉
（`operations.OverlappingFilterCheck` → `filterCheck` 判断目录是否在同步集内）。
而且不排除它，下一轮 mirror 会真的把回收站删掉。

**试错记录（实测于本地目录）**

| 做法 | 结果 |
|---|---|
| 不改 filter | ✅ 传 1 / 删 1 |
| `filter.NewFilter(&Options{})` + `filter.ReplaceConfig` | ❌ **传 0 / 删 0**（即使一条规则都没加） |
| 同上 + `Add(false, "/_trash/**")` | ❌ 同样全被排除 |
| `filter.AddConfig(ctx)` + `Add(false, "/_trash/**")` | ✅ 传 1 / 删 1，且回收目录被排除 |

一个「零规则的过滤器」竟然让所有文件都不参与同步 —— 单看 API 完全看不出来。
所以：**在已有 filter 的副本上追加规则**。

**教训**
「新建一个空对象应该等于默认行为」是直觉，不是事实。凡是替换掉某个既有配置对象，
先做一个最小对照实验（改前 / 改后各一次），别假设空对象 == 默认。

---

## D29 · 重复副本清理做成后端命令，而不是 `run` 的一个选项（2026-11-07）

**背景**
F1 留下的坑（D19）：降级模式重传会产生同名副本，而 `Fs.List` 折叠同名，
所以 `purge` 只删得到折叠后的那一个，其余副本 rclone 根本看不见。

**为什么不能放进 `weftsync run`**
`run` 走 rclone 的同步引擎，引擎拿到的目录是**已经折叠过**的。
放进 `run` 等于让一个看不见副本的层去删副本 —— 它连"这里有几个"都不知道。

**决定**
做成后端的 `Command`：`backend duplicates` / `backend dedupe`。
理由：只有后端同时握有**原始列表**（`listCached` 不折叠）和**快照 token**。

**三条硬性拒绝条件（都不是可配置的）**

| 条件 | 为什么 |
|---|---|
| 没有 `state_file` | 无法区分「我传的」与「别人传的」，而删除不可恢复 |
| 处在 repair 模式 | 刚重建的快照此刻不构成证据 |
| 记录的 token 不在该目录 | 说明文件被外部替换；此时删「其他」可能删掉刚上传的新版本 |

**明确不做的**
「保留最新一份，删其他」看起来最省事，但在多机同步同一云盘时会**删掉另一台机器
刚传的新版本**。宁可留一堆副本让用户手工清，也不能猜。

**确认机制**
必须 `-o confirm=true` 才真删，默认只报告。另外 `-n/--dry-run` 照样生效。

**实测**（真实租户）：用原始上传 API 造 3 个同名 `dup.txt` →
`duplicates` 列出 1 组 3 条；不带 confirm 时一个不删；
带 confirm 时保留快照记录的 token `COnMb9wX...`、删掉另外 2 个，剩 1 条；
不带 `state_file` 时拒绝并说明原因。

**踩到的坑**
一开始想用 `copyto` 造重复副本 —— 造不出来，因为 F2 修好的 `Update` 会先传后删。
**产品自己已经不会制造这种状态了**，所以测试必须绕过它，用原始 API
（`.scratch/make_duplicates.py`）。这反过来也是一个好信号：正常路径不再产生副本。

---

## D30 · 限速自己做，`--tpslimit` 对本后端无效（2026-11-07）

**背景**
飞书几乎所有接口限 5 QPS / 10000 次每天，超出返回 `1061045`。
rclone 有 `--tpslimit`，看似直接可用。

**它对本后端无效，原因有两条**

1. **请求不经过 rclone 的传输层**。`newFs` 用的是自己 new 的
   `&http.Client{Timeout: ...}`，而 `--tpslimit` 是在 `fshttp` 的 `Transport` 里生效的。
   所以这个开关对本后端**一个请求都管不到**。
2. **它是进程级单一阈值**，而飞书不同接口的额度差 15 倍：
   普通接口 5 QPS，**move 只有 20/分钟**（0.33 QPS）。用一个阈值表达两个限额，
   要么把普通请求卡死，要么让 move 超限。

**决定**
在 client 里放两个 `rate.Limiter`：

| 限速器 | 默认 | 作用范围 |
|---|---|---|
| `limiter` | 5 QPS，burst **1** | 所有普通请求 |
| `moveLimiter` | 20/分钟，burst 1 | 仅 `files.move` |

**为什么 burst = 1**
官方明确写「does not support concurrent calls」。允许突发只会更快地撞上限，
然后把时间花在退避重试上。匀速发送开销只是几百毫秒延迟，却**根本不会产生 1061045**。

**为什么在发请求之前等，而不是出错后重试**
重试也会消耗当天的 10000 次额度。先排队再用，额度用在该用的地方。

**可配置**
`--feishu-qps`（浮点，0 = 关闭）与 `--feishu-move-per-minute`。
默认值写进 `RegInfo`，所以不配也是安全的 —— 这一点很重要：
「默认不限速」等于把 1061045 留给用户第一次跑大目录时踩。

**实测**
- 单测直接用时间戳断言间隔：4 次调用在 20 QPS 下相邻间隔 ≥30ms；
  5 个**并发**调用者也被拉平成 ≥40ms 总时长 —— 并发不能绕过限速。
- move 限速器独立：普通请求与 move 交替调用时，只有 move 被拉开间隔。
- 真实租户：12 次 CLI 调用在默认 5 QPS 下 14.09s，`--feishu-qps 0` 时 12.60s
  （每次是独立进程，所以差值是每进程 2 个请求各等 200ms）。
- 9MiB 上传 + 下载回读 SHA-256 仍 MATCH，说明限速没有破坏功能。

**教训**
「上游有这个开关」不等于「这个开关管到我的代码」。用之前先确认自己的请求
是否真的走那条链路 —— 这里差点把一个**完全没生效**的配置当成已实现。

---

## D31 · 冲突检测放后端，策略放 job，两层用 `Features().Command` 通信（2026-11-07）

**背景**
F4 要三种冲突信号（token 消失 / token 不匹配 / 同名多条）。前两种 `List` 也能看，
**第三种必须看原始列表** —— 而 job 层手里的 `fs.Fs` 是折叠过的。

**候选方案**

| 方案 | 问题 |
|---|---|
| A. 给 job 定义接口，feishu 实现 | feishu 要 import job，方向反了（D27 说后端只管单文件） |
| B. 类型定义放第三方叶子包 | 为了 5 个字段开一个新包，过度设计 |
| C. 用 `fs.Features().Command` 调后端命令 | 零新依赖，后端自己保留原始列表与快照 |

**选 C**

job 层 `f.Features().Command(ctx, "conflicts", []string{dir}, nil)`，
把返回的 JSON 解成自己的 `Conflict` 结构。好处：
- 不共享类型，不产生包依赖
- 任何后端只要实现了同名命令就能用；没有命令的后端**自然没有冲突**
  （`fs.ErrorCommandNotFound` → 视为无冲突，而不是让整个 run 失败）
- 将来换后端（SMB / 其它网盘）只需实现同一个命令

**否决了什么，为什么**
`report` 策略不能「先跑再回滚」：rclone 的同步引擎没有事务，
回滚意味着重新上传/删除，反而更危险。改成**把冲突路径从同步集里过滤掉**
（`filter.AddConfig` 追加排除规则），于是引擎从第一步就不会碰这些路径 ——
不存在「半处理」的中间态。

**`overwrite` 会留下多余副本，这是故意的**
如果记录的那个 token 已经不在了（别人替换过），我们只能上传自己的新版本，
**不能删对方那一份** —— 无法证明那是我们的文件，而删除不可恢复。
实测：4 个文件的目录在 overwrite 后变成 6 个（`replaced.txt` 变成两条）。
清重复是 `backend dedupe` 的职责，不是 overwrite 的。

**实测**
后端命令在一次真实扫描里同时报出三种 `kind`；`report` 策略下 run 退出码 1
且原始列表前后**逐字节相同**；`overwrite` 下回读到的确实是本地内容。

---

## D32 · Linux 实机验证抓到的两个缺陷（2026-11-07）

跑 WSL2 Ubuntu 的 23 项验收，22 项通过、2 项真失败（另一项是我脚本自己的错）。
两个失败都是**在 Windows 上不可能出现**的那类问题。

### 缺陷一：反斜杠被无条件当成路径分隔符

`normalizePath` 里写的是 `strings.ReplaceAll(p, "\\", "/")`，两处（`state` 与
`backend/feishu`）都是。在 Windows 上这没问题——反斜杠只能是分隔符。
**在 Linux 上反斜杠是合法的文件名字符**，于是：

```
本地文件  weird\name.txt          （一个文件名里带反斜杠的文件）
上传后变成 weird/name.txt          （一个叫 weird 的目录 + 一个叫 name.txt 的文件）
```

这不是「命名难看」，是**访问了另一个路径**：快照里记的 key 也变了，之后对账全错。

**修法**：改用 `filepath.ToSlash`。它在 Windows 上把 `\` 变成 `/`，
在其它平台**原样保留** —— 正好是「分隔符转换要跟平台走」的语义。

**测试**：拆成两个带 build tag 的文件（`normalize_unix_test.go` /
`normalize_windows_test.go`），因为断言本身是平台相关的。原来那个写在
`state_test.go` 里的 Windows 断言在 Linux 上会变成错的期望。

### 缺陷二：没有 `state_file` 的 pair 整体失败

F4 的冲突检测在「没有快照」时返回 error：

```go
return nil, fmt.Errorf("conflict detection needs a state_file: ...")
```

看起来是「不能假装没有冲突」的严谨做法，实际后果是：**没配 `state_file` 的 pair
整个 run 直接失败**。而没配 `state_file` 就是默认配置 —— 等于最常见的情况不可用。

**为什么之前没发现**：F3/F4/F5 每次验证都顺手配了 `state_file`（为了测增量），
Windows 上那批 job 文件也都带着它。**测试总是用「完整配置」，于是默认配置没人走过。**

**修法**：区分「没有冲突」与「无法检测」。后端返回 `available: false` + 说明，
job 层打印一行 note 后继续运行。

**测试**：`TestConflictsUnavailableWithoutState` 覆盖后端行为，
并**把修复回退验证过它会红**。注意 job 层那个测试用的是 local 后端，
根本走不到 feishu 的代码 —— 我一开始以为它证明了一切，实际没有。
**回归测试要打在真正包含缺陷的那一层。**

### 另外三个「失败」是我脚本自己的错

| 现象 | 真因 |
|---|---|
| `state header is '\n-1 …'` | 我定义了 `head()` 函数，**遮蔽了 `head` 命令** |
| `message missing` | grep 模式写错（该匹配 `NOTHING was`，两处文案不同） |
| `state perms 777` | 在 `/mnt/c`（DrvFs）上测 POSIX 权限，那里权限是模拟的 |

**教训**
「跑出来 6 个失败」里可能一半是工具/脚本自己的问题。先逐条核对**每一条失败是否真的是
被测对象的问题**，再动手改代码 —— 否则会为 3 个不存在的缺陷改代码，同时漏掉 2 个真的。

---

## D33 · 写回 job 文件用「手写紧凑 TOML + 只把注释算作损失」（2026-11-07）

网页里改配置要写回 `.toml`。两个绕不过去的问题：**怎么写出人能读的文件**，
以及**什么才算「破坏用户的东西」**。

### 编码器必须是手写的

先用 `go-toml/v2` 的结构体编码器，结果对一个 4 行的配置写出来是这样：

```toml
pair = []
mode = ''
deletion = ''
```

零值字段全被写出来。这份文件是**给人手工编辑的**，写成这样既不可读，
也让「往返一致」检查失去意义 —— 手写的文件永远不可能和它逐字节相同。

**改为手写紧凑写出器**：只写非空字段、固定顺序。写出后必须
**重新 parse + validate + 与输入逐字段比对**（`sameConfig`），
所以编码器写错会当场失败，而不是悄悄写出语义不同的文件。

### 「有损」的定义：只有注释

第一版按「逐字节往返一致」判断，结果**任何手写文件都被判为有损** ——
空行位置、字段顺序本来就不同，而这些不承载任何用户意图。
一个每存必弹的警告等于没有警告。

**改为只检测注释**（`HasComments`，只认注释行，空行/顺序不算）。
无注释 ⇒ 直接写；有注释 ⇒ 返回 `ErrLossyRewrite`，除非显式 `Force`。
无论哪种情况，旧内容都留 `.bak`。

### 写入顺序

检测有损 → 编码 → parse → validate → `sameConfig` 自检 → 写 `.bak` → 原子写（tmp+fsync+rename）。
**校验在写盘之前**，所以一次失败的保存不会留下任何痕迹。

---

## D34 · 保存配置必须是「在旧配置上叠加」，且作业默认值不能从 pair[0] 取（2026-11-07）

在浏览器里实测配置页时，用 `grep` 比对落盘内容和界面控件，抓到三个**界面上有控件、
保存时被丢掉**的缺陷。共同点：**用户被告知保存成功，但设置根本没写进去。**

### 1. pair 级同步设置完全没映射

界面每个 pair 都有「删除方式 / 版本目录 / 版本命名 / 冲突策略 / 删除上限」，
`PairView` 也带 `Sync`，`job.Pair` 也支持，唯独 `ApplyConfigView` 构造 `job.Pair`
时**没有 `Sync` 字段**。选了 versioning，保存后落盘什么都没有 → 下一轮按默认
`permanent` **永久删除**。这不是显示问题，是安全设置被静默丢弃。

### 2. 作业级只留了 conflict 和 max_delete

`cfg.Job.Sync = job.SyncSpec{Conflict: in.Sync.Conflict}`，于是
`deletion` / `versioning_dir` / `versioning_naming` / `max_delete_size` 全部被清空。
同上，后果是「以为在进版本库，实际在永久删」。

### 3. 表单不认识的字段被一并抹掉

`ApplyConfigView` 原来是**从零构造**一个 `job.Config`，所以任何表单没渲染的字段
（`max_delete_size`、未来的新字段）在**第一次保存时就消失**。
函数注释当时还写着「view 里没有的字段保留旧值」——注释是对的，代码是反的。

**修法**：`cfg` 从 `previous` 出发，只覆盖表单负责的字段；`mergeSync` 以
「提交值 vs 当初展示给用户的值」判断**用户是否真的改过**，没改就原样保留
（显式仍显式、继承仍继承）。

### 4. 顺带发现：作业默认值是从 pair[0] 算出来的

`ViewConfig` 里 `jobResolved, _ := cfg.Pair[0].EffectiveSync(cfg)`。
`EffectiveSync` 的优先级是「pair → 作业 → 内置默认」，所以只要 pair[0] 有自己的覆盖，
界面的「默认同步设置（可被每个 pair 覆盖）」一栏**显示的就是 pair[0] 的覆盖值**。
用户以为自己看到的是默认值，其实不是；保存路径也比对了错误的值。

**修法**：用**没有覆盖的 pair** 去解析（`(&job.Pair{}).EffectiveSync(cfg)`），
得到的就是纯作业级默认。实测：作业级显示 `_weftsync_trash`（内置默认），
pair 级显示 `_pairkeep`（自己的覆盖），互不干扰。

### 5. 保存能造出「daemon 起不来」的文件

`job.Save` 只跑 `validate()`，而 `validate()` **完全不看调度**；`HH:MM` 的校验住在
`EffectiveSchedule()` 里。于是界面能保存 `start_at = "10m"` 并且提示「已保存」，
落盘后 daemon 下次启动直接退出。**实测踩到了**：改完配置后 daemon 起不来，
`/api/health` 连不上。

**修法**：把 `EffectiveSchedule()` 移进 `validate()`。`Load` 与 `Save` 都走 `validate`，
于是「能被保存的文件」与「能被启动的文件」是同一集合。

### 教训

- **回归测试要按「界面上有几个控件」逐项断言，而不是挑几个方便的字段。**
  我先测了变体、调度、pair 改名 —— 全都通过，却漏掉了整整两类被丢弃的字段。
- **`Save` 与 `Load` 必须共用同一套校验**，否则能写进去的东西不一定能读出来。
- 布局类「差异」（行尾空格）先用**字节级检查**确认，别照着终端渲染去改代码 ——
  这次差点为终端补白去「修」编码器。

---

## D35 · 开机自启记录「当前正在跑的命令」，并把路径转绝对（2026-11-07）

### 写的是什么

`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`（Windows）/
`$XDG_CONFIG_HOME/autostart/weftsync.desktop`（Linux）。都只写**当前用户**：
开机自启是单用户便利，不该要管理员权限。

命令来源是**正在运行的进程**（`os.Executable()` + 解析后的参数），不是浏览器传来的字符串。
浏览器不知道二进制在哪、在用哪些作业文件；让它传等于把「开机自启」变成一个
往用户启动项里注册任意命令的入口。

### 三个必须处理的点

**1. 相对路径会让启动项静默失效。**
登录会话的工作目录没有意义。写进 `job.toml` 这种相对路径，登录后会在某个系统目录里找，
找不到就退出 —— 界面上「已启用」，实际每天早上什么都没发生。
所以作业文件、`--log-file`、`--config` 一律转绝对路径。

**实测**：把注册表里那条命令**在 `C:\` 下原样执行**，`/api/health` 返回 200，
`/api/status` 正常列出作业。换工作目录不影响。

**2. 默认值不能写进去。**
`--addr` 等于默认值时跳过。写进去就等于把当时的默认值冻结：以后默认值改了，
这条老启动项还传着旧值，而用户以为自己用的是默认。

**3. 引号在两个平台上规则不同，而且 Linux 是两层嵌套。**
- Windows：`syscall.EscapeArg`（与 `CreateProcess` 同一套）。路径带空格不加引号，
  是把程序放进 `C:\Program Files` 之后启动项失效的经典原因。
- Linux：先按**值**规则把 `\` 写成 `\\`，再按**引号**规则把 `"` `` ` `` `$` 转义，
  并且字面 `%` 要写 `%%`。只做一层，带空格的路径会被拆成多个参数。
  读取时按同一套规则反解，`matches` 才可信。

### `matches` 这个字段

`enabled:true` + `matches:false` = 入口存在但跑的不是这个进程的命令（换了可执行文件位置、
换了作业文件）。界面单独提示并说明「重新勾选会覆盖」。只说「已启用」是正确但会误导。

### 测试怎么不污染机器

`runKeyPath` / `runValue` 从常量改成变量，测试指向 `Software\weftsync-test\Run` 并在
`t.Cleanup` 里删掉。**测试绝不能改自己所在这台机器的真实启动项。**

顺带踩到：`registry.DeleteKey` 只能删空键，先删 `\Run` 再删父键，否则每跑一次测试
就在注册表里留一个空壳。第一版就是这么漏的，靠跑完检查 `HKCU:\Software\weftsync-test`
是否存在才发现。

Linux 侧把 `XDG_CONFIG_HOME` 指到 `t.TempDir()`，`os.UserConfigDir()` 就跟着走，不需要
为测试改代码路径。

### 没做到的

- Linux 的 `.desktop` 没跑成 `desktop-file-validate`（WSL 里没装，装它要 root + 网络）。
  格式按 Desktop Entry 规范写，解析往返有单测覆盖，但**规范校验器这一关是空的**。
- 托盘图标仍未做（F5 剩余）。

---

## D36 · 托盘：纯 Go 无 cgo，但必须假定它会坏（2026-11-07）

实现方式是 `Shell_NotifyIcon` + 一个隐藏窗口 + 消息循环，全部走 `syscall`，
**没有 cgo**（从 Linux 交叉编译 Windows 程序的能力不能丢）。图标用像素现画，
不打包 `.ico`，二进制保持单文件。

### 让托盘永远不是「必须成功」的东西

它是 web UI 的快捷方式，丢了不能影响同步。两处落实：

1. `Start` 返回 error ⇒ 打印一行后继续跑。没有通知区域（服务会话、Explorer 没起）
   是完全正常的状态。
2. **消息线程必须 `recover`。** 第一版把 `CreateDIBSection` / `CreateBitmap` /
   `DeleteObject` 写成 `user32.NewProc(...)`，实际它们在 `gdi32`。
   `syscall.LazyProc.Call` 在找不到函数时是 **panic**，而 panic 在 goroutine 里
   ⇒ 整个进程退出。也就是「画不出图标」升级成了「同步守护进程被杀」。
   加 `recover` 后这类错误变成一条日志。

### 一个窗口类只能有一个

第一版类名写死。第二个托盘实例 `RegisterClassEx` 失败（类已存在），
而更糟的是：**窗口类携带 WndProc**，如果复用类名，第二个图标的消息会被派发到
第一个实例的处理器。改成类名带自增序号，并在 `WM_DESTROY` 里 `UnregisterClass`，
否则每次 Start/Stop 泄漏一个已注册类。

### 清理挂在 `WM_DESTROY` 而不是 `WM_CLOSE`

窗口也可能被包外的东西销毁（实测用 `taskkill` 不带 `/F` 就会）。把 `NIM_DELETE`
放在 `WM_DESTROY` 里，两条路径都能清掉图标；只挂 `WM_CLOSE` 会留下一个
「鼠标划过才消失」的幽灵图标。

### 怎么验证一个看不见的东西

托盘图标在通知区域，截屏截不到（任务栏不在捕获范围）。所以验证走程序化路径：

- `Shell_NotifyIcon(NIM_ADD)` 返回非 0 —— 失败的话 `Start` 会返回 error，日志里会有
- `IsWindow(hwnd)` 为真
- `Notify`（即 `NIM_MODIFY`）成功
- 进程存活、`/api/health` 返回 200
- 把托盘窗口打掉后 daemon 继续服务

图标**画得对不对**单独验：把像素填充抽成 `fillIconPixels`，用测试断言
（四角透明、圆盘偏蓝即通道没反、中点是白色箭杆、箭头在右），再借
`WEFTSYNC_ICON_PNG` 环境变量导出一张 PNG 亲眼看。第一版测试在 `mid-1` 采样，
那正是箭杆所在行，于是「箭头被镜像了」是**断言写错**，不是代码错 —— 看 PNG 才发现。

### 顺带修掉的一个真问题

`--tray` 是开关而不是默认，所以开机自启的命令必须带上它。漏了的话：
用户勾了自启 + 托盘，重启后 daemon 起来了但图标没了，且没有任何提示。
为此把 `SpecFromInvocation` 的参数列表改成 `Invocation` 结构体 ——
一排位置布尔参数是那种「某天两个被写反」的东西。

---

## D37 · 预览、实时进度、停止：三个都需要「不骗人」的细节（2026-11-07）

### 预览就是 `job.Options.DryRun`，但要能说明「跑不了」

`POST /api/run?job=X&plan=true` 走同一条 `job.Run`，只把 `DryRun` 打开。
`RunPair` 里 mirror 本来就有「先 dry pass 量一遍」的结构，所以预览复用的正是
保护机制已经依赖的那段逻辑，而不是另写一套估算。

但预览有两种「什么都没发生」，必须分开：

| 情况 | 计数 | 界面应该说 |
|---|---|---|
| 确实没有变更 | 全零 | 「没有需要变更的内容」 |
| 打不开目标 / 配置错了 | 全零 + error | 「预览失败，不能据此判断」 |

第一版 `PlanStatus` 没有 `Error` 字段，于是**打不开目的地时会告诉用户「没有需要变更的内容」**。
这是实测踩到的（作业文件写了错误的 backend 名）。加了 `Error` 并且预览失败时把
「按此执行」置灰。

### 实时进度必须按作业分组

转发生在 rclone 的引擎里，唯一的数据源是它的 accounting。默认是全局计数 ——
两个作业同时跑就变成两者之和，「当前文件」还可能显示另一个作业的文件。
用 `accounting.WithStatsGroup(ctx, name)` 让每次运行记到自己的组。

三个实现细节：

1. **组的名字按作业固定，不按运行递增。** rclone 没有导出删除组的接口，
   每次运行新建一个组 = 每次泄漏一个平均速度 goroutine，直到进程退出。
   而且必须复用同一个 `StatsInfo`，否则传输写进一个、UI 读另一个。
   （第一版用了 `NewStatsGroup`，被 `TestNewTrackerReusesGroup` 抓住。）
2. **`runPass` 从 `GlobalStats()` 改为 `accounting.Stats(ctx)`** —— 有组用组，
   没组用全局，所以命令行行为不变。
3. **不能信 rclone 的 `speed`。** 它只统计**已完成**的传输：一屋子并发上传时，
   整个运行期间 `speed` 都是 0 —— 正是用户在看的时候（实测 CLI 输出
   `bytes=16777216 ... speed=0KiB/s`）。改为自己算：字节 ÷ 本次 pass 已用时间。
   ETA 也从同一个速率推。单位是「本次 pass 的平均速度」，会滞后于爬坡，但不会乱跳。

### 停止：看自己的 context，不看返回的 error

`job.Run` 的 context 一取消就中断。但**取消后它可能返回 nil**：
本地后端实测就是这样。如果按返回的 error 判断「是不是被停的」，
同一个操作会因为后端不同而时对时错。所以分类用 `runCtx.Err()`。

「停止」的呈现也必须与「失败」分开：`last_ok=true` + `last_stopped=true` +
`error=""`，历史里标「已停止」。记成失败会让人去找一个不存在的问题。

**取消后不会留半截文件**（实测：24MiB 上传中途停止，远端文件数 0）。
原因是分片上传必须走 `upload_finish` 才提交，取消时根本不会调用它；
`upload_all` 本来就是整文件原子。代价是已发分片白传 —— 取消就是取消，不承诺续传。

### 手动执行默认先预览

PLAN.md 要求手动「立即运行」先预览再确认。实现成：点 `立即运行` 默认只跑预览，
面板里点 `按此执行` 才真的跑；另留一个「跳过预览」勾选框。
定时任务仍然直接执行 —— 没人在旁边读预览。

### 界面上的一个具体陷阱

进度每秒都在变。如果把进度放进「数据没变就不重绘」的判据里，
作业表每秒整体重绘一次，**「停止」按钮会在鼠标按下的瞬间被替换掉**。
所以进度写进已经存在的 `<span id="prog-作业名">`，按钮结构只在状态变化时重绘。
这是 F6 早期那个「点击被吞」缺陷的同一类问题，只是换了个触发源。

---

## D38 · 冲突处理只给两个按钮，而且都不删云端（2026-11-07）

需求里写的是「保留两份 / 覆盖 / 忽略」三个按钮。实现成了两个，理由如下。

### 为什么没有「忽略」

一条冲突只有两种结局：本地赢，或者远端赢。所谓「忽略」实际等于
「以后每一轮都继续报同一条冲突」—— 那是噪音，不是解决。
真正意义上的「忽略」需要一份持久化的静默清单，而那会让快照同时表示两件事
（「这是同步过的」和「这是故意不同步的」），代价远大于收益。

### 为什么「保留两份」指向 pair 级策略而不是按钮

`conflict = "keep-both"` 在同步路径里已经实现并测试（上传本地副本为新名字，
并让原路径退出本轮同步集）。做成逐条按钮的话，需要把「原路径永久不再同步」
这件事持久化 —— 又是一份静默清单。所以按钮旁边直接写明去配置页设 keep-both。

### 两个动作都得是真话

| 动作 | 语义 | 实现 |
|---|---|---|
| `push`（以本地为准） | 下次运行上传本地文件覆盖远端 | 删掉快照里那条记录 |
| `pull`（以云端为准） | 远端内容成为本地内容 | **先把远端下载覆盖本地**，再把远端 token + 本地指纹记为基线 |

`pull` 之所以必须先下载：快照里的本地指纹的含义是「远端确实有这个内容」。
只改指纹不下载，快照就在宣称一次从未发生的上传，下一轮比较会基于假数据。
下载后还必须把本地 mtime **截断到整秒**（`truncateMTime`），因为快照存的是秒；
不截断的话下一次比较会认为文件变了，一传一传没完。

`adopt` 还要求在写之前**确认该 token 当前确实在文件夹里**。否则快照指向一个
不存在的东西，冲突下一轮还会回来 —— 比不改更糟。

### 三个实测抓到的缺陷

1. **`resolve` 的路径不能当位置参数**。rclone 把位置参数按目录（Fs）解析，
   `backend resolve remote:a.txt` 直接报 `is a file not a directory`。
   改成 `-o path=`，CLI 与 daemon 内部两条路都成立。
2. **界面读 `r.Conflicts`，而 API 输出 `conflicts`**。冲突表一直空着，
   直到真实制造一条冲突才暴露。根因是我在上一批给 `job.PairResult` 加了 json tag，
   而界面代码写于加 tag 之前 —— 两边没有共同的真源。
3. **冲突列表只在内存里**。daemon 重启后表就空了，而按钮正依赖这份列表。
   修法：预览（dry-run）同样会检测冲突，所以列表也读 `plan.results`，
   界面明说「点一次预览即可重新检测」。这条如果不说，用户会以为冲突消失了。

### 验证

真实租户端到端 17/17：用「删掉再上传」制造 `remote-replaced`（飞书没有原地覆盖），
`adopt` 后冲突归零且下一轮零上传；`forget` 后下一轮重新上传、且旧 token 被
`Update` 清掉（只剩一个同名条目）；另有 4 条拒绝路径（未知 action、过期 token、
无 state_file、`../` 逃逸）。

> 写这轮验证脚本时我自己犯了三次错：`rclone copy` 的目标是目录（要用 `copyto`）、
> grep 状态文件头写成了 `state file`（实际是 `weftsync state v1`）、
> CLI 调用没带 `state_file` 于是看到的是另一个（空的）快照。
> **脚本报 FAIL 时，先怀疑脚本。**

---

## D39 · 凭据：一个真实的安全缺陷，以及为什么不能靠「看出来」区分两种存储形式（2026-11-07）

做凭据页时先去读「密钥到底存在哪、以什么形式」，结果发现一个**早就存在的高危缺陷**。

### 缺陷：`IsPassword` 选项后端从不 reveal

`app_secret` 声明为 `IsPassword: true`，含义是「rclone 的工具会把它的值混淆后存」。
rclone 里每一个声明了该选项的后端都会调用 `obscure.Reveal`，**我们的飞书后端没有**，
于是把混淆后的密文当成密钥发给了飞书。

复现：`weftsync config create` 给一个 1 字符的密钥，文件里写入的是 23 字符的密文，
后端拿它去认证必然失败。也就是说**用 `rclone config` 交互式创建的远程，从来没能认证成功过**。
（之前的验证全部用手写明文配置，所以一条都没暴露 —— 又是「测试只覆盖了一条路径」。）

### 为什么不能简单改成「总是 reveal」

`Obscure` 的实现是 `base64url(IV[16] || AES-CTR(plaintext))`，而 `Reveal` 只校验
base64 能不能解、长度是否 ≥ 16 字节。**飞书密钥是 32 位字母数字，本身就是合法的
base64url**，于是 `Reveal(明文)` 不报错，返回 8 字节乱码。

实测（不是推理）：32 位明文 `Reveal` 后得到 8 字节，"成功"。所以：

| 改法 | 后果 |
|---|---|
| 不 reveal（原状） | `rclone config` 建的远程全废 |
| 总是 reveal | **所有手写明文配置全废**，而且是静默变成乱码 |

结构上无法区分两者 —— 密文和明文都满足「能 base64 解码且 ≥16 字节」。

### 决定：不猜，让认证调用裁决

候选列表最多两个（先解出来的、再原文），由 `obscured.Forms` 产生；
`tokenSource` 在**认证被拒**时用第二个再试一次，成功后把胜者换成主密钥，
之后的刷新不再探测。

- 传输层错误（网络断了）**不重试**：那不是凭据问题，重试只是把延迟翻倍。
- 两种都失败时，错误信息里加一句「both the plaintext and the obscured form were tried」，
  这样用户知道该怀疑密钥本身，而不是文件的编码。
- 一个副产品：`Forms` 还会判断解出来的是不是可打印 ASCII，所以**手写明文在绝大多数
  情况下不会产生那次多余的请求**。

顺带发现 rclone 自己的 `config create` 也有同一个盲点：它用「Reveal 失败就混淆」
来判断，于是一个 32 位密钥会被它当成「已经混淆过」而**原样存成明文**；只有交互式输入
（走 `MustObscure`，不做这个探测）才会真的混淆。两条路都要支持，这也是必须两种都试的原因。

### 写盘时自己决定形态

凭据页保存时不依赖 rclone 的判断，直接 `obscured.Encode`：**我们要知道文件里写进去的
到底是什么**。读取端两种都接受，所以这样做不会把谁的配置写坏。

保存在写之前校验全部 key（未知 key 直接 400 并指名）——半应用的编辑会留下用户没要求的凭据；
写完把权限收紧到 `0600`（Unix 上实测）。

### 页面上的三条硬约束

1. **密钥永不返回**。不是遮罩、不是后四位。返回的是 `set` / `length` / `encoded`。
   长度是有用的诊断（多余空格、意外换行），`encoded` 说明的是「我们会怎么读它」，
   与同步路径首要尝试的读法一致。
2. **长度要报有效长度**。第一版报的是存储文本长度，混淆后显示 64 而密钥是 32，
   这个提示就失去意义了（浏览器实测发现）。
3. 编辑的是 rclone 启动时那个文件，**不接受请求里的路径**。

### 自己引入又修掉的两个缺陷

- 给 `Options` 加了一个未导出字段存备用密钥 → `configstruct.Set` 用反射遍历字段，
  **遇到未导出字段直接 panic**，整个进程挂掉。改成由 `resolveEnv` 返回候选列表，
  派生值不再进配置结构。
- 保存成功后会重新拉取列表（为了刷新「已设置（N 个字符）」），
  而那次重绘把提示所在的行也替换掉了 —— **用户点保存后什么都没看到**。
  改为用一个跨重绘的 flash 表，并且只应用一次（十分钟前的「已保存」比没有更糟）。

### 验证

- 单测：不泄露密钥（明文与密文都不出现在响应里）、混淆后能解回、空白被去掉、
  未知 key / 未知远程被拒、清除与「不动」语义、`0600`、`used_by` 覆盖
  `backend=` 与 `root="remote:path"` 两种写法、环境变量来源不被回显。
- 真实租户 5/5：明文可用、混淆可用、`config create` 建的可用、错密钥明确报错。
- 证据：把密钥改错后「测试连接」返回 `code=10014 app secret invalid`，
  改回后立刻恢复 —— 证明页面写的确实是认证会读的那条路径。

---

## D40 · 拆分：七个「看起来是细节」但会决定成败的选择（2026-11-08）

F7 要做的事一句话说完：飞书单层最多 1500 个直接子项，超了就把它拆成几个子目录。
真正难的不是算法，而是「移动用户本地文件」这件事**不能靠再点一次来补救**。
下面七条都是先做错、被实测打回来才定下来的。

### 1. 本地先动，云端后动

执行顺序是 `快照 → 移动本地 → 建云端目录 → 移动云端 → 校验`。

反过来（先动云端）曾经看起来更合理：云端慢、失败率高，早点知道能不能成就早点放弃。
但那样在云端移到一半失败时，本地还没动 —— 状态文件已经在改，云端一半在桶里一半在根上，
**没有任何一个阶段是干净的**。本地先动的好处是：本地重命名是毫秒级，失败几乎只可能是
权限/占用，此时云端一个字节都没变，`rollbackLocal` 可以立刻退回去。

代价是本地动完之后云端失败的状态更难看。所以本地阶段结束就落一次账本，且两个阶段都做成幂等
（见下一条）。

### 2. 两个阶段都必须幂等 —— 因为账本一定会丢

原本的写法是每移动一项就写一次账本，保证随时可恢复。实测直接崩了：

```
write ...\split-reports-*.json: Access is denied.
```

原因是 1450 项 × 每次重写整个 JSON（约 200KB），在 OneDrive 同步目录下几乎每秒
几十次写入，被杀毒/OneDrive 的占用检测拦下。改成「只在阶段结束时落盘」就快，但
进程在这中间被杀，重启后就是「一半做过、一半没做」。

结论：**恢复能力不能靠「记录得多勤」，只能靠「重复执行安全」**。

- 本地阶段：源不存在 + 目标存在 → 判为「已做过」，跳过；源和目标都在 → 拒绝（真冲突）。
- 云端阶段：先 `Exists` 目标，已存在就跳过。
- 折中落盘：阶段结束各一次，云端每 25 次追加一次。

### 3. 云端重命名必须走后端的 `repath`，不能只改 state 文件

最省事的做法是移动完文件、把 state 文件里的 key 改掉。实际不行：

rclone 的 `fs/cache` **没有单条失效接口**，`ClearMappingsPrefix` 只清名字映射。
已经打开的后端实例还在内存里攥着一份 state，你改磁盘上的文件，它下次 `flushState`
就把你的修改覆盖回去。实测现象是：拆分完成后立刻同步，1450 个文件**又传了一遍**。

所以移动 + 改 key 必须在同一个持有内存状态的地方做 —— 也就是在 feishu 后端里加一个
`repath` 命令，由 split 包通过 `backend` 命令调用它。移动和 key 迁移在同一把锁内完成。

### 4. `repath` 的参数是「交替 from/to」，不是用 NUL 分隔

第一版用 `\x00` 拼一串 from/to 传给后端。在 bash 里跑（F8 的 Linux 验证）时，
`execve` 的参数字符串在 NUL 处被截断 —— 后端只收到第一个路径，报「参数个数为偶数」。
Windows 上因为走的是不同的命令行构造路径，这个问题**完全看不出来**。

改成交替参数 `[from, to, from, to, ...]`：没有分隔符，就没有截断。

### 5. 首字母拆分不做拼音

中文文件名按拼音归堆看起来更"懂用户"，但要引入一个几 MB 的词库，或者只覆盖常用字。
两种都不接受：前者为一个可选功能加一个重量级依赖，后者会在某天给出
「张、李 明明都在，却都进了 `_other`」。归到 `_other` 只是难看，猜错字是错误。

`initial` 方案里非 ASCII 一律进 `_other`，并在文档里写明。

### 6. 1400 而不是 1500

硬限是 1500。我们拦在 1400：预检和真正开始移动之间有人的反应时间，期间可能有新文件。
余量 0 会出现「检查说没问题、同步却报超限」，这是最难向用户解释的一类现象。

### 7. 「已撤销」是一个独立状态

`Undo` 之后把 `Completed` 置 false 就够了 —— 想当然是这样。实际效果是界面显示
`未完成（剩 1450）`，并且给了一个「继续」的入口。因为 undo 过的账本里每一项的
`LocalDone`/`RemoteDone` 都是 false，和「进程被杀在中间」**完全无法区分**。

修法是在账本上加 `Undone`/`UndoneAt`，`Pending()` 对 undone 直接返回 0，
界面优先渲染「已撤销」并隐藏撤销按钮。教训：**「不是完成」和「没做完」是两个状态，
不要用一个 bool 的 false 同时表示它们。**

### 验证

- 单测：`split` 包覆盖四种方案的分桶结果、超限再分到同级 `-NN`、名称冲突检测、
  路径逃逸拒绝、幂等重入、账本 `undone` 标记。
- 真实租户 12/12：1450 项的 `reports` 被同步拦下（文案含「1400」，远端 0 文件）；
  四种方案都跑通（`_pdf 484 / _png 483 / _xlsx 483`；`Q-Z-01` 1400 / `Q-Z-02` 50；
  `2026-01/02`；`part-01 1400 / part-02 50`）；执行 `moved 1450, verified true`；
  撤销后本地回到 1450 个平铺文件、0 个目录。
- 界面：拆分 → 执行 → 撤销 → 账本显示「已撤销」且无撤销按钮；再撤一次另一条账本，
  两行都是「已撤销」，检查面板回到「没有目录超过上限」。

---

## D41 · `max_delete_size`：读不到的数字，就借别人的尺子量（2026-11-08）

`max_delete`（文件数上限）从 F3 就在，`max_delete_size`（字节上限）一直标着「未实现」，
原因写得很实在：rclone 统计了被删的字节，但**没有任何读接口** ——
`StatsInfo.deletesSize` 不导出，`RemoteStats` 里也没有这一项。

这确实是个障碍，但它只挡住了「读这个数」，没挡住「判断有没有超」。

### 做法

只在**预检的那一趟**把 `ci.MaxDeleteSize` 设成配置值。rclone 的这个检查位于
`StatsInfo.DeleteFile` 里，**在真正删除之前**：

```go
if ci.MaxDeleteSize >= 0 && s.deletesSize+size > int64(ci.MaxDeleteSize) {
    return errMaxDeleteSize
}
```

预检本来就是 dry-run，所以触发时什么也没删。这正好是本项目要的语义。

不能把它打开在真跑那一趟：那样就会「删到接近上限、然后停下」，留下删了一半的目标端 ——
和 `--max-delete` 一样的问题，也正是当初拒绝 rclone 自带闸门的原因。

### 判定：不靠字符串匹配

rclone 把 `errMaxDeleteSize` 交给 `fserrors.FatalError`，但 `DeleteFilesWithBackupDir`
最后返回的是**聚合错误** `failed to delete N files` —— 真正的两条消息
（`--max-delete-size threshold reached`）只进了日志。所以按文本匹配会失败：
第一版就是这么写的，测试直接报「got *fmt.wrapError: ... failed to delete 3 files」。

改成**再测一次**：预检报错时，把上限关掉重跑一遍 dry pass。

- 这趟能跑完，且统计到的待删数**比上次多** → 拦住它的是体积上限 → `SizeGuardError`。
- 这趟照样失败 → 是真的出错了 → 原样报出，绝不冒充「什么都没删」。

这个判定没有任何字符串依赖，且顺带拿到真实的待删数，错误信息可以说
「想删 4 个文件，超过 1 KiB」而不是「超过上限」。代价是拒绝路径多一次 dry pass ——
拒绝是稀有事件，这个代价可以接受。

### 只在配置时校验，不在使用时才炸

`max_delete_size` 是字符串（`"500M"` / `"1G"`），用 `fs.SizeSuffix.Set` 解析。
解析放在 `EffectiveSync` 里，`Load` 与 `Save` 都会走到，于是：

- 写错 `"1 kilobyte"` → **保存就被拒**，错误里点名该字段；
- 不会出现「文件存下来了，直到某天真的要删时才发现这个限制是死的」。

反过来更危险：如果解析失败被当成 0，而 0 又是合法的「不限」，一个拼写错误就会
**静默卸掉整道闸门**。所以解析失败一律是错误，不允许回退成 0。

### 仍然存在的局限

真跑那一趟不设体积上限，所以「预检通过 → 目标端被别人改 → 真跑删更多」这条路上，
只有文件数上限会事后复核（原有逻辑），字节数不复核。写进 roadmap，不假装没有。

### 验证

- 单测：`SizeGuardError` 触发且远端一字未动；低于上限时正常删除；预览只报告不失败；
  非尺寸字符串加载即拒；作业级默认被 pair 继承；未配置时为 0（不限）而不是「全禁」。
- 真实进程（local 后端，4 个 1 KiB 文件、只有 4 个待删，`max_delete = 50` 放行数量闸门）：
  `1k` → `BLOCKED` / exit 1 / 远端 4 个不变；`1M` → 删 4 个 / exit 0；
  `"1 kilobyte"` → 加载即拒。
- 界面：作业级与 pair 级都渲染出「删除体积上限」；`750M → 2G` 往返正确并写盘；
  填 `lots` 被 400 拒且文件不变。

---

## D42 · 通知只发该发的三种，而「发了」这件事本身要留痕（2026-11-08）

定时同步是无人值守的。失败、被闸门拦下、有冲突 —— 这三种情况需要人做决定，
但它们的共同点是「没人正看着页面」。于是要在系统托盘上弹一个通知。

### 只发三种

| 情况 | 发 | 理由 |
|---|---|---|
| 运行失败 | ✅ | 需要人介入 |
| 被闸门拦下（`max_delete` / `max_delete_size` / 超限目录） | ✅ | **一个文件都没删**，但同步也没成 |
| 有冲突未处理 | ✅ | 云端内容需要人判断 |
| 成功 | ❌ | 每次成功都弹，等于训练用户关掉通知 |
| 预览 | ❌ | 用户刚点的按钮，人就在屏幕前 |
| 用户手动停止 | ❌ | 是他自己停的 |

「成功不通知」是这类工具最容易做错的地方：多弹一条的成本看起来是零，
实际是把「通知」这个通道的带宽用光，真正要命的那条也就没人看了。

### 通知必须同时写日志

弹出的气泡会自己消失，而且用户可能压根没看见 —— 所以 `notifyf` 在弹之前先
`logf("notify: <标题>")`。这样「有没有被告知过」这个问题，日志能给答案。

### 没有托盘就什么都不做

`Daemon.notify` 是 `nil` 时 `notifyf` 直接返回。Linux 没有托盘（D36 说明过为什么不做），
无头服务器也没有 —— 那种情况下不该有第二种失败路径，日志就是记录。

反过来说，**托盘启动成功才安装通知回调**，所以「托盘坏了但同步还在跑」这个状态
依然成立：`tray.Start` 失败只打印一行，daemon 继续工作。

### 验证

- 单测 6 个：失败 / 闸门 / 冲突各有且仅有一次通知；成功 / 预览 / 无托盘时零通知。
  `runOnce` 用 `d.Stop()` 等 run goroutine 结束，而不是等状态标志 ——
  通知是在 `execute` 末尾发的，等 `State != running` 会与之赛跑（第一版就是错的）。
- 真实进程：故意给一个不存在的本地目录，日志出现
  `tray: icon shown; failures will raise a notification` 与
  `notify: weftsync：bad 同步失败`，且**没有** `tray notify: ...` 报错 ——
  说明 `Shell_NotifyIcon(NIM_MODIFY)` 接受了这个气泡。
- 反向：正常目录跑一轮，上传成功、日志里 `notify:` 行数为 **0**。

---

## D43 · 双向同步：能做，但「能删」和「能拉新文件」必须分开谈（2026-11-08）

F9 一直挂在「待 F4 完成后评估」。F4 已完成，这是评估结论。

### 先回答那个决定性的问题：云端改动看得出来吗

双向同步的全部难度集中在一句话：**能不能不下载就判断云端内容变了？**

`state.Entry` 现在只记本地指纹（`LocalMTime`/`LocalSize`）和 `RemoteToken`。
后端的 `Object.ModTime()` 是**影子 mtime**（D16：token 对得上时返回当初上传时的本地指纹），
所以 rclone 的 rsync 也看不见云端改动。

实测（真实租户，2026-11-08）：

1. 上传 `probe.txt` → state 记下 `remote_token = C7erb…`。
2. 绕过工具，用同一个名字把云端内容换成 `CLOUD EDITED THIS`。
3. 再跑一次 `run`（`conflict = "report"`）：

```
pair "f4": 1 conflict(s) between the folder and the sync state
  probe.txt    remote-replaced    this name now carries a different file than the one we uploaded
conflict policy: report - these paths are left untouched
exit=1
```

结论：**file_token 在内容被替换后会变**，而 token 身份是现成的、零成本的云端变更信号。
双向同步不需要下载比对，也不需要新增 state 字段。

> 排查记录：中途我曾用自己写的 python 探针得出过「token 不变」的错误结论。
> 那个探针列同一目录只返回 1 项，而 `weftsync lsf` 返回 2 项 —— **工具本身错了**。
> 换成产品自己的 `run`/`lsf` 才看到真相。教训是老的：探针的结果先拿产品核对一遍。

### 三路比较，四种情况

记下每个路径的 `(local_mtime, local_size, remote_token)` 作为基线，之后：

| 本地 | 云端 token | 判断 | 动作 |
|---|---|---|---|
| 未变 | == 基线 | 都没变 | 跳过 |
| 变了 | == 基线 | 本地改了 | 上传（现有行为） |
| 未变 | != 基线（存在） | **云端改了** | 下载覆盖本地 |
| 变了 | != 基线 | 两边都改 | 冲突，不动 |
| 不存在 | == 基线 | 本地删了 | 删除云端（需允许） |
| 存在且未变 | 不存在 | 云端删了 | 删除本地（需允许） |
| 新增 | 不存在 | 本地新增 | 上传 |
| — | 新增（不在基线） | **云端新增** | 下载（需允许） |

这套比较不需要新 state 字段，因为 token 已经足够表示「云端这一项是不是我们上次那一份」。

### 真正的问题不是算法，是三个决定

**1. 删谁。** 单向同步里 `max_delete` 已经是一道闸门。双向会多出一个方向：
云端删了 → 本地也删。这条链上没有任何"云端回收站"可退回（B11 实测：飞书删除后**无法通过
API 恢复**）。所以第一版**不传播删除**，只报告「云端少了 N 个文件 / 本地少了 N 个文件」。

**2. 拉谁。** 「云端新增的文件下载下来」听起来无害，但它意味着**任意云端内容进入本地磁盘**，
包括别人放进来的、名字和本地某个文件冲突的、以及超大文件。第一版**不拉新文件**，只报告。

**3. 覆盖谁。** 「云端改了 → 覆盖本地」是唯一真正有价值的双向能力（用户在飞书里改了文档，
本地要同步下来），但它也是**唯一会覆盖用户本地数据的动作**。所以它必须有闸门：
只在「本地自上次同步以来没变」时执行，并且同样受体积/数量上限保护。

### 结论：拆成两期，第一期只做"安全的那个方向"

| 期 | 内容 | 风险 |
|---|---|---|
| 第一期 | `variant = "two-way"`：本地改动上传（现有）+ **云端改动下载覆盖本地**；删除与云端新增文件**只报告不动手** | 低：唯一写本地的前提是"本地未变" |
| 第二期 | 删除传播 + 拉取云端新增文件，各自配独立闸门与开关 | 高：需要单独评估与专门测试 |

第二期要不要做，取决于第一期在真实使用里是否存在「云端新增文件」和「删除」的实际需求 ——
如果用户的真实场景只是"我在飞书里改了文件，本地要跟上"，那第一期就够，第二期不该做。

### 第一期落地要点

- 判定放在 `job` 层（整对视角），token 比较用后端的 `conflicts` 报告（已有）。
- 下载复用 `job/conflict_resolve.go` 的 `pullConflict`（下载覆盖本地 + 采用 token/mtime/size）。
- 上传分支完全复用现有 `VariantUpdate` 路径，不新增同步实现。
- 「云端改了」与「本地改了」同时成立 → 走现有冲突策略（默认 report，什么都不动）。
- 首次运行没有基线：所有云端文件都算"云端新增" → 按第一期规则只报告，不动手。

---

## D44 · 双向第一期落地：两个只有真机才会暴露的坑（2026-11-08）

设计在 D43。真正写下来的时候踩了两处，都值得记。

### 1. 「只报告」和「继续上传」放在一起就是谎话

第一版把「云端删了的文件」列进 held 列表并打印「左边不动手」，然后**继续跑上传阶段**。
结果：本地那个文件在云端已经没了，上传阶段把它**又传了一遍** —— 等于撤销了用户在飞书里的删除，
而报告里写着「已保留、等你决定」。真机第 8 步直接暴露：

```
doc.txt   remote-deleted   the file recorded in the snapshot is no longer in the folder
two-way: 1 path(s) need a decision and were left alone
tw        two-way   transferred 1   ← 说不动手，却传了
```

修法：held 路径同样要从本轮同步集里排除（`excludePaths`），和 `conflict = report`
策略一直用的做法一致。修完 `transferred 0`，云端保持已删除。

**教训**：凡是要写「我没动它」的文案，就必须保证后面**真的没有代码动它**。
两条路径拼接（报告 + 同步阶段）时，这种矛盾只有跑起来才看得见。

### 2. `objectToken` 会返回 `?`，而 `?` 会让采用失败

拉取之后要把云端 token 写进 state（调用后端的 `resolve adopt`）。第一版用的是
「拿下载下来的那个对象问 token」，代码对真实租户返回了 `?`：

```
pair "tw": download "doc.txt": resolve: token ? is not at "doc.txt" any more
```

后果不是一次失败那么简单：**state 没被更新**，于是同一处冲突每轮都会重现，
每次都想拉一遍。真机第 5 步（应该「无事可做」）变成了又是冲突。

修法：token 不用猜 —— 冲突报告里已经列了「现在拿这个名字的是哪个 token」
（`remote-replaced` 的 `found`）。双向走 `pullConflictToken`，把 `found[0]` 直接传下去；
`found` 不是恰好 1 个就按 held 处理，不赌。

同时把 `objectToken` 返回 `?` 变成显式错误，而不是带着一个假 token 去写 state。

**教训**：能从上一份报告里直接读到的事实，不要再去问一次。问第二次得到的可能是
`?`，而 `?` 会被写进状态文件，把一次失败变成一串失败。

### 验证

真实租户 8 步全对：上传 → 云端改 → 拉下来 → 无事可做 → 本地改 → 上传 → 
云端删 → 报告且不复活、不删本地。单测 7 个覆盖同样分支（含「两边都改」「只在云端
存在的文件不拉」「预览不下载」「无 state 时降级」）。

### 3. 界面把「连接串」当成了远端路径显示

配对配了 `remote.options`（例如 `state_file`）时，`RemotePath()` 返回的是 rclone 的
**连接串**形式：

```
test,state_file="C:/Users/…/.scratch/twui/state.json":FZivfuh4hlEeLBdI4S5c7T0Cnsb/twui
```

这串东西被直接塞进了 `PairResult.remote`，于是预览面板里显示出来 —— 一行看不懂的字符串，
顺便把**本地绝对路径发布到了一个网页上**。

连接串是「怎么打开这个 Fs」的技术细节，不是给人看的信息。新增 `Pair.RemoteDisplay()`
只拼 `backend:root`，`PairResult.remote` 改用它；真正打开 Fs 时仍然用连接串。

**教训**：一个值同时承担「机器要用」和「人要读」两种职责时，迟早会在人那一侧出丑。
凡是会进日志/界面的字段，都要问一句：这个形式是给人看的吗。

### 4. `pulled` / `held` 必须出现在界面上

第一版只把它们放进了 JSON。历史和预览里看不到，用户就只会看到 `transferred 0`
—— 而这一轮其实下载了文件。功能做完了不代表用户看得见它做完了。
现在历史表在上传数后面显示 `↓N`，预览面板多两行「从云端下载」「需要你判断」。

---

## D45 · 断点续传：能续到「块」为止，续不到「跨进程」（2026-11-08）

F2 一直挂着一项「上传中断后能续传（`upload_id` 持久化）」。这次把它结掉，
结论是**一半做、一半不做**，而且不做的那个不是因为难。

### 能做的：块级长重试

`client.do` 已经对每个请求重试 3 次（1s/2s/4s 指数退避）。它覆盖的是「抖一下」。
它不覆盖的是「传 1GB 到第 200 块（共 250 块）时笔记本切换了网络 / VPN 重连了」。

那种情况下，`client.do` 三次都失败 → `putChunked` 返回错误 → rclone 重试**整个文件**
→ 前 199 块白传。而实际上只需要重发第 200 块。

所以加了 `uploadPartRetry`：同一块最多再试 4 轮，退避 2s/4s/8s/16s。

两个必须守住的性质：

1. **只重试可重试的**。`classify(err)` 不是 `classRetryable` 就立刻返回。重试一个权限错误
   只会烧掉每天 10000 次的配额，并把用户真正需要看的消息延后 30 秒。
2. **退避期间可以被打断**。用 `select` 等 `ctx.Done()` 而不是 `time.Sleep`：收到停止请求
   要立即返回，而不是等 16 秒。停止是安全的 —— 没有任何块被提交。

### 不做的：`upload_id` 落盘

看起来只要把 `upload_id` 存进 state，进程重启后接着传就行了。做不到，因为缺一个前提：

**Feishu 没有任何接口能告诉你「这个 `upload_id` 已经收到哪些块了」。**
端点只有三个：

```
POST /open-apis/drive/v1/files/upload_prepare   → upload_id, block_size, block_num
POST /open-apis/drive/v1/files/upload_part      → 收下第 seq 块
POST /open-apis/drive/v1/files/upload_finish    → 收下 file_token
```

`block_num` 是「总共要几块」，不是「已经收了几块」。所以重启后从落盘的 `upload_id`
接着传，唯一能确定安全的起点是**第 0 块** —— 那就等于把「重传整个文件」
改名为「续传」。

有人会说：客户端自己记「我传到第 N 块了」不就行了。问题恰恰在这：进程被杀的那一刻，
第 N 块可能已经到了服务器、也可能还在路上。两种情况下客户端都只知道「第 N 块发出去了」。
于是只能保守地从第 N-1 块重发 —— 这需要 `upload_part` 对同一 seq 的重复上传是幂等的。
这个我没有验证成功（自写探针在 `upload_part` 上拿到 1062009 `size` 不一致，
而产品自己的分片上传是通的，说明是探针的问题，不是 API 的问题），
**在没验证幂等性之前，我不会基于它写一个会产出内容混杂文件的续传逻辑**。

### 真正需要续传的场景，已经有答案了

「续传」这个需求最常出现的形态不是单个 1GB 文件，而是「500 个文件传到第 300 个时断了」。
那种情况 state 增量同步已经解决了：重跑只传没传完的，前 300 个不再上传。
单文件超过 20MB 才走分片，而这类大文件在「本地文件到云盘」的同步里本来就是少数。

所以结论是：**把成本花在覆盖 90% 场景的地方（已由 state 覆盖 + 块级重试），
不为一个需要赌幂等性的 10% 写复杂代码。**

如果将来真的需要跨进程续传，前置条件是先把「同一 seq 重复上传」的语义测准，
那件事该用一个不依赖 python multipart 手写的探针来做（例如给 `uploadPart` 加一个
带 build tag 的集成测试）。

---

## D46 · 多网盘：本来就支持，但有两个真实的坑（2026-11-08）

F10 一直标着「可选、未做」。实测下来它**本来就工作**，因为 `pair` 的 `remote`
就是一条 rclone 路径，后端是飞书还是本地磁盘还是 union 与同步逻辑无关。
这一条记录实测结果和两个容易踩的坑。

### 已经能用的

一个 job 里两个 pair 指向完全不同的后端，各自独立：

```
to-feishu   update   1   ← feishu:FZiv.../multi/cloud
to-disk     update   1   ← :local:<root>/.scratch/multibackend/copies
```

- 各自写自己的 state（`to-feishu` 配了 `state_file`，`to-disk` 没配 → 靠 mtime+size）
- 第二轮两个都 `transferred 0`
- **一个 pair 失败不阻止另一个**：把 `to-feishu` 的本地目录改成不存在 →
  `to-feishu` 报错退出码 1，`to-disk` 仍然 `transferred 1` 正常完成

`union` 后端也能用，前提是在 **rclone.conf 里声明 upstreams**：

```ini
[unionlocal]
type = union
upstreams = "/path/shard-a" "/path/shard-b"
```

然后 `remote = { backend = "unionlocal", root = "" }`。实测 2 个文件写进 shard-a，
`lsf unionlocal:` 能同时列出两个 shard 的内容，第二轮跳过。

### 坑一：`:union:a:b` 内联写法不成立

`remote.root = ":union:/a:/b"` 会报：

```
union can't point to an empty upstream - check the value of the upstreams setting
```

union 的 upstreams 是一个配置项，不是一个路径。所以它**必须**先在 rclone.conf 里
定义成一个远程。

我没有为此放开 `remote.options` 白名单。job 文件描述的是「同步什么」，
不是「怎么连」；连接配置（包括 union 的 upstreams）属于 rclone.conf。
一旦开始往白名单里加业务参数，就得为每个后端的每个参数做一次「这个安全吗」的判断，
而白名单的价值恰恰在于它很短。

### 坑二：单字母远程名在 Windows 上被当成盘符

第一次用 `[u]` 做 union 远程名，结果：

```
ERROR : one.txt: Failed to copy: mkdir \\?\U:\: The system cannot find the path specified.
```

rclone 把 `u:` 当成 U 盘。**这不是我们的 bug，但用户会踩**：
一个叫 `d` 或 `e` 的远程名在 Windows 上永远不工作，而错误信息里出现的是
`\\?\U:\` 这种和配置毫无关系的字符串，几乎无法自己推出来。

所以文档里必须写：**Windows 上远程名不要用单个字母**。

### 结论

F10 标 ✅：验收的两条（多后端混用 / union 后端）都实测通过。
局限写清楚：union 需要 rclone.conf 预定义；单字母远程名在 Windows 上不可用。

---

## D47 · 全局设置放在 daemon 自己的文件里，不进 job 文件（2026-11-08）

F6 第 11 项「设置页」的定义是「全局默认值、通知开关、开机自启、过滤规则默认值」。
后两项做完了，这一条记录前两项放哪里、以及为什么不放在 job 文件里。

### 不进 job 文件

`job.toml` 描述的是「同步什么」——哪个本地目录、哪个云端目录、怎么删。
通知开关和全局过滤规则描述的是「程序怎么表现」，和某个作业无关：

- 放进 job 文件 → 每个作业都有自己的通知开关，用户要改 5 次，且它们互相矛盾时
  没有正确答案
- 放在 daemon 级 → 一处修改，语义唯一

所以有了 `daemon/settings.go`，存 `os.UserConfigDir()/weftsync/settings.json`。
文件缺失 = 全新安装，不是错误；文件损坏 = 记一行日志并用默认值继续，
**不能让一个坏掉的设置文件阻止同步**。

### 过滤规则只能排除

`exclude` 里的规则追加到每次运行已有的过滤器上（`filter.AddConfig` 是追加，
不是替换 —— 替换会把「冲突路径排除」和「回收目录排除」一起抹掉，那是另一个 bug 的入口）。

方向是单向的：全局规则只能把文件排除掉，不能把作业自己排除掉的文件加回来。
理由是可推理性 —— 「作业文件说了算，全局规则只做减法」比一张优先级表好懂，
而优先级表的每一种读写顺序都可能变成 bug。界面上写了这句话。

### 通知开关的判断必须在 notifyf 里

不能只在界面上隐藏复选框：设置是持久化的，而 daemon 会在后台继续跑。
`notifyf` 每次检查 `settings.NotificationsOn()`，所以关掉之后真的不弹。

反过来也要验证：重新打开后必须恢复。实测两个方向：

| 步骤 | 结果 |
|---|---|
| 关掉通知 → 让一次运行失败 | `last_ok=false`，日志里 `notify:` 行数 **0** |
| 打开通知 → 再失败一次 | `notify:` 1 行：`notify: weftsync：settest 同步失败`，且无 `tray notify:` 报错 |

如果只测「关掉不弹」，一个「永远不弹」的实现也能通过。

### 没有托盘时不假装有这个开关

`GET /api/settings` 带 `notifications_available`。Linux、无头机器、没加 `--tray`
的时候界面显示的是说明文字而不是一个点了也没用的复选框 —— 可选控件的最坏形态是
「看起来能用但不起作用」。

### 「全局默认值」这一项：不做，并说明为什么

PLAN.md 第 11 项里还写着「全局默认值」。最初我加了一个 `default_sync` 字段准备在
新建 pair 时使用，写完发现 UI 里没有地方用它 —— 新建 pair 用的是**当前作业的
`[job.sync]` 默认值**，那已经是最贴切的答案。一个存着但没人读的字段比没有更糟
（下一个人会以为它在起作用），所以删掉。

再加一层全局默认意味着：新建 pair 的初始值取决于用户**当前没在看**的另一个文件。
少点一次点击的收益，换不来这个歧义。

---

## D48 · keep-both 最大的问题是它「每轮都说一次」（2026-11-08）

`keep-both` 是 F4 里唯一没在真实租户上跑过的策略（单测早就过了）。
这次补跑，第一次运行就拓出三个问题，其中一个是数据增长型的。

### 一、每轮都多上传一份副本

第一轮之后云端：

```
a.txt                                    = CLOUD EDITED   （原件，未动）
a (from JL-10313-hyxu 20261108-094030).txt = local v1
```

第二轮之后：

```
a.txt                                    = CLOUD EDITED
a (from JL-10313-hyxu 20261108-094030).txt = local v1
a (from JL-10313-hyxu 20261108-094039).txt = local v1   ← 新的
```

原因很直白：keep-both 把本地版本传成一个**新名字**，但快照里 `a.txt` 的
token 还是**两轮前**那一个。所以下一轮冲突照旧复活，再传一份。
没有人会每天去看云端，于是这个循环会一直转到目录撞上 1500 节点上限。

修法：上传完新副本之后，把**云端那颗**记为原路径的基线。

这里要承认它「说了一个善意的谎」：快照从此声称「本地的 a.txt 对应云端那颗 token」，
而那颗的内容其实是别人的。上一份决策（D44 的第 2 条）恰好说过不能这么干 ——
但那是 `pull` 的语境：`pull` 要求「本地内容 = 云端内容」才允许 adopt，因为它的
承诺是「我已经采纳了云端那份」。keep-both 的承诺不一样：

> 两份都留下来了，而且我不希望再被提醒第二次。

在这个承诺下，「基线对齐到云端那颗」是正确的：我们的版本在 `newPath` 上、在云里，
原件没被动过，而用户已经被明确告知过一次。

### 二、一句话里两个相反的结论

同一个运行里先打印：

```
kept both: "a.txt" also uploaded as "a (from …).txt"
```

再打印：

```
pair "kb" has 1 conflict(s) … NOTHING was uploaded, overwritten or deleted.
```

**明明上传了。** 这句 `NOTHING was uploaded` 原本是为 `report` 策略写的，
在那种情况下它是真的；keep-both 复用同一种错误类型就变成了自相矛盾，
而用户没法从两条相反的陈述里推断出到底发生了什么。

修法：`ConflictError` 增加 `KeptBoth`，两种策略各说各的。
`report` 保留强措辞（它确实什么都没做），keep-both 改成
「你的版本已用新名字上传，云端那份没有动，什么都没删」。
两者仍然都返回非 0 退出码 —— 冲突发生过这件事必须看得见。

### 三、`(token ?)`

日志里出现过 `uploaded "a.txt" as "…" (token ?)`。同样是 `objectToken` 在拿不到
`ID()` 时返回 `?` 的老问题（D44 第 2 条）。这次的规则是：日志里要写的 token 如果
是 `?`，就**根本不写 token 这一段** —— 一行读起来像成功的信息里跟一个问号，
比不写更让人怀疑。

### 顺带一个同类的修正

`adoptCloudCopy` 最初自己去读远端对象拿 token，于是本地后端（没有 `ID()`）直接失败。
改成**由调用方传入 token** —— 冲突报告里已经有 `found`，那正是「现在拿这个名字的
是哪几个 token」。这跟 D44 记下的是同一条教训：能从上一份报告读到的事实，不要再问一次。

后端没有 `resolve` 命令时（本地磁盘对拷）keep-both 仍然可用：上传新副本照做，
只是没有基线可记，界面上会明说「这个后端不保留快照」。这比直接让整轮失败好。

### 验证

真实租户：第一轮 → 云端 2 个文件（原件内容 = 云端版，新名字内容 = 本地版）；
**第二轮 → 无冲突、无新副本**。
单测：`TestKeepBothRecordsTheCloudCopyAsBaseline`（检查传给 adopt 的 token 与
本地指纹）、`TestKeepBothRefusesToRecordANonexistentToken`（空/`?` 都拒绝）、
`TestKeepBothAdoptFailsWhenThePathIsGone`、`TestConflictErrorWordingMatchesWhatHappened`。

---

## D49 · 并发上传会把同一个目录创建成多份，而 F9 第二期差点因此删掉正常的本地文件（2026-11-08）

这一条记录的是一次真实事故链：一个从没被测过的并发路径，一个因此产生的
数据分裂，以及它差点让第二期的「云端删了也删本地」删掉完全正常的文件。

### 现象

跑 F9 第二期验证时，5 个文件上传进一个新建子目录，`transferred 5`，
第二轮却报 3 个 `remote-deleted`：

```
f1.txt  remote-deleted   the file recorded in the snapshot is no longer in the folder
f2.txt  remote-deleted   ...
f3.txt  remote-deleted   ...
```

而递归列出整个云端，三个文件明明都在。

### 根因：并发的「检查-然后创建」

rclone 默认 8 个并发传输。每个上传都走 `resolvePutParent`：

```
resolver.resolveDir(parent)  ->  不存在
f.Mkdir(ctx, parent)         ->  列父目录，没找到同名子目录，创建一个
```

8 个 goroutine 同时判定「不存在」，于是各建一个。真机实测（用原始 API 列出）：

```
4x folder   s2raw  token=[Uytv…, LVcu…, XIZd…, Y7sm…]
3x folder   s2lag  token=[K3ak…, N2fB…, Rmmv…]
3x folder   stage2 token=[Jk9T…, Scqp…, Unso…]
```

文件被分散到各个副本里。之后任何一次「按名字解析路径」只会命中**其中一份**，
另一份里的文件对这次运行就是「不存在」——于是被报成 `remote-deleted`。

**这就是为什么它一直没被发现**：之前的验证要么是扁平目录，要么目标目录已经存在
（`resolveDir` 直接成功，根本不进 `Mkdir`）。要触发它必须同时满足
「目标是新建子目录」+「子目录里文件数 > 1」。

### 为什么它危险

在这之前，`remote-deleted` 只是「报告、不动手」，最多算烦人。
但 D43/D49 的三号开关 `propagate_remote_delete` 一旦打开，这条链路就变成：

```
列表只能看到一份副本
  -> 另一份里的文件被判定为"云端已删除"
  -> 本地那份被 os.Remove 删掉
```

**用户一个字都没改，本地文件就没了，而且不可恢复。**
这正是我在给第二期加开关时必须重新验证第一期的原因：
一个只读的误报是无害的，同一份误报接到一个写操作上就是数据丢失。

### 修法

`Fs.Mkdir` 加 `mkdirMu` 串行化「检查-然后创建」，并把检查改用 `listFresh`
（不信缓存：另一个 goroutine 可能刚建好，而缓存里还是没有）。

单锁足够：临界区只有一次列目录加最多一次创建，且只在目录确实不存在时才进入。
已存在目录的上传路径根本不走 `Mkdir`，所以常见情况零成本。

### 验证

- 单测 `TestConcurrentUploadCreatesOneFolder`：8 个 goroutine 并发 `Put`
  进同一个新目录，断言**恰好 1 个**目录且里面 8 个文件。
  把锁去掉后连跑 5 次，分别创建了 5/5/4/5/3 个同名目录 —— 与真机现象一致。
- 真机：8 文件并发上传进新目录 → `folders named 'racefix': 1`，内部 8 个文件，
  第二轮 `transferred 0`、无幽灵删除。
- 清理：`purge test:path` 按名字只删一份，删不掉其余副本；
  新写了 `.scratch/cleanup_dups.py` 按名字列出**所有**副本并逐个删除。

### 顺带修掉的第二个 dry-run 缺陷

同一轮验证还发现：`run --dry-run` **真的删掉了云端文件**。

原因是 `--dry-run` 是 rclone 的**全局** 标志，它只把 rclone 自己那一趟变成 dry run，
并不会传给 `job.Options.DryRun`。于是出现了最坏的组合：

- rclone 的同步那一趟：`would copy`，什么都没做
- 我们自己的第二期删除：**真的执行了**

修法：`cmd/run` 里 `plan := dryRun || fs.GetConfig(ctx).DryRun`，两个标志都算。
回归测试 `TestDryRunNeverDeletes` 与 `TestStageTwoPreviewChangesNothing`
（后者要求在 dry run 下云端、本地、下载三个方向都零变化）。

### 教训

1. **并发路径必须显式测**。单线程单测全绿，掩盖了 8 路并发下的检查-创建竞态。
2. **只读误报升级成写操作之前，要重新跑一遍会喂给它数据的全部路径**。
   `propagate_remote_delete` 吃的是 `remote-deleted`，而那个信号的可靠性
   在它还是只读报告时没人认真查过。
3. **「dry run」必须是一个贯穿全层的概念**，不能是「rclone 那一层 dry」。
   两个不同的 dry-run 标志各管一半，是最容易让人放心地丢数据的组合。

---

## D50 · 快照每传一个文件就整份重写一遍，在真实数据上是 O(n²)（2026-11-08）

第一轮真实数据同步（3,309 个文件、1.94 GB、27 分 24 秒）**功能上全对**：
`transferred 3309 / 3309`、`errors 0`、无 1061045 限流、无超限拒绝、
`_` 前缀内容零泄漏、无重复目录，第二次运行 `transferred 0`。

但终端里刷了几十条：

```
WARNING: could not save the sync state to .../state.json:
  state: replace ...: rename state.json.tmp state.json: Access is denied.
```

### 问题一：rename 撞上正在扫这个目录的东西，而且没有重试

快照就放在被同步的目录旁边（`.scratch/jl6test/`，位于 OneDrive 同步范围内）。
OneDrive 客户端和 Windows Defender 会短暂持有目标文件，`os.Rename` 于是报
`Access is denied`。

`state.Save` 里**没有任何重试** —— 一次瞬时失败就丢掉那一次保存。
而 `split` 的账本早就修过同一个坑（`saveSnapshot` 重试 4 次，注释写着
"the write was made both rarer and retried"），**这里漏了**。

这一次最终没丢数据纯属运气：保存被调用了很多次，最后一次恰好成功
（`state.json` 里 3309 条齐全）。如果最后一次失败，整轮记录全丢，
下一轮重传 3,309 个文件、烧光当天配额。

### 问题二：每上传一个文件就重写 1.16 MB，是 O(n²)

`recordUpload` 每成功一个文件就调一次 `flushState()` → 整份 JSON 重写 +
复制一份 `.bak`。实测数字：

| 项 | 值 |
|---|---|
| 实际传输 | 1.9 GB |
| 快照大小 | 1.16 MB |
| 保存次数 | 3,309 |
| 磁盘写入 | 3,309 × 2.3 MB ≈ **7.6 GB** |

对完整的 74,313 个文件，快照会长到约 26 MB，每次保存 52 MB，
写入量会到**几百 GB** 量级 —— 只为了记住「哪些文件已经传过」。

### 修法

**节流**：两次保存至少间隔 `stateFlushInterval`（15 秒）。被跳过的保存置
`stPending`，本轮结束时由 `FlushState()` 强制落盘。
`job.RunPair` 用 `defer flushStateOf(fdst, w)` 接上，覆盖全部 7 个 return 路径。

**重试**：rename 重试 4 次、退避 20/40/60 ms，与 `split` 保持一致。
`.bak` 失败**降级为警告**继续，因为 rename 是原子替换、正文不可能写坏，
丢掉 `.bak` 只损失回退能力，而中止保存会丢掉整批记录。

### 一个自己写出来又自己抓到的 bug

第一版 `flushStateLocked` 是这样：

```go
if !f.stPending && time.Since(f.stLastFlush) < stateFlushInterval {
    return          // ← 直接返回，没有设 stPending
}
```

在**新建的 Fs** 上，`stLastFlush` 是零值，第一次「太近」判断永远不会命中，
所以这个分支看起来安全。但一旦命中，记录既不写盘也不标记 pending，
`FlushState` 便无事可做，这条记录就**永久消失**。正确写法是无条件置
`stPending = true` 再返回。

`TestFlushStateThrottlesButKeepsThePendingFlag` 锁住这一点；
把 `stPending = true` 删掉，测试立刻红（已做 mutation 验证）。

### 验证（都是实测，不是推断）

| 验证 | 结果 |
|---|---|
| 3 文件 / 9 秒的真实同步，**有**收尾 flush | `entries = 3` |
| 同一场景，**删掉** `defer flushStateOf` 重建 | `entries = 1`（只剩第 1 个） |
| 恢复后重跑 | `entries = 3`，第二轮 `transferred 0` |
| 3,309 文件用新 exe 回归 | `transferred 0`、`state 3309`、99 秒 |
| 单测 | `TestFlushState*` 4 条全绿；mutation 后变红 |

9 秒 < 15 秒的节流窗口，所以第 2、3 个文件**必然**走节流路径 ——
这就是「没有收尾 flush 就会丢」的直接证据，而不是理论推演。

### 教训

1. **「每次变更就保存」在文件会变大的场景里是 O(n²)**，量级一旦上千就不可接受。
   写之前先估一下 n × size。
2. **同一个坑修过一次，要把修法做成模式去找第二次**。`split` 的注释里明明写着
   "rarer and retried"，`state` 这边一个都没有。
3. **静默降级成 WARNING 的错误要数一下次数**。几十条 `Access is denied`
   出现在滚动日志里，功能又是对的，很容易被当成噪音读过去。
4. **节流必须有收尾**。凡是「攒着最后一起写」的设计，都要能回答
   「进程正常结束时谁来写最后一次」，否则就是把丢数据伪装成省 IO。

---

## D51 · 「这一次要不要重试」和「这一轮还要不要继续」是两个问题（2026-11-08）

真实数据同步跑通之后，剩下的活儿是把 74,313 个文件推上去。这暴露了一个
**单测永远碰不到、只有按真实规模算才看得见**的问题。

### 算术是怎么来的

| 已知事实 | 来源 |
|---|---|
| 本层对 `1061045` 重试 3 次，退避 1s + 2s | `client.do`，`maxAttempts=3` |
| rclone `retries` 默认 3（整轮再跑 3 遍） | `fs/config.go` 实测确认 |
| `upload_*` 每天 10,000 次 | 飞书官方限频文档 |
| 数据集 74,313 个文件 | 本地实测统计 |

配额约 2 小时用完（47 文件/秒的传输速度下其实更快），此后每个剩余文件都要
把自己 3 次重试跑满（≈3.5 秒）再失败：

```
64,000 文件 × 3.5 秒 × 4 遍（1 + rclone 的 3 次重跑）≈ 10 天
```

**这十天里没有任何进展，日志只会是几千条互不相关的 1061045。**
用户看到的是「同步在跑」，实际是在空转。

### 为什么不能靠「重试次数」解决

重试策略回答的是**单次调用**的问题。缺的是**整轮**的判断：
当每一次调用都注定失败时，继续调用只是把失败摊到剩下的文件上。

### 修法：连续耗尽即熔断

| 条件 | 动作 |
|---|---|
| 连续 3 次调用**耗尽全部重试** | 锁存；触发一次 abort 钩子 |
| 锁存后 | 所有调用立即返回，**不发请求、不等待** |
| 任何一次成功 | 计数清零 |
| 不可重试错误（权限/路径/参数） | 完全不计入 |

`job.RunPair` 用 `context.WithCancel` 建本轮 ctx，把 `cancel` 交给后端做钩子，
于是 rclone 的 `rsync` 会在几秒内停住，而不是走完整棵树。

**为什么阈值是 3 而不是 1**：单次尖峰很常见，我们自己的限流器就是为了制造
「偶发被拒」这种状态；1 次就熔断会把正常波动当成死亡。而 3 次连续、
每次都跑满退避的拒绝，在 5 QPS 的自限流下不可能是巧合。

**为什么不可重试错误不计入**：三个文件名非法、权限不对的文件，一次就失败、
根本不进重试循环。若计入，三个坏文件就能中止一整个 7 万文件的迁移。

### 结果怎么报

`job.runPass` 在每趟结束后**向后端查询锁存状态**，命中则返回
`job.SystemicError`：

```
stopped after 9987 transfer(s): the backend could not make progress;
run again later and it will continue from the saved state
  (feishu files.upload_all: code=1061045 msg=frequency limit)
```

必须查询而不是靠错误字符串：rclone 会把「ctx 已取消」报成**每文件一条**
普通失败，靠错误内容猜原因等于把一条真相稀释成几千条噪音。

### 验证

| 验证 | 结果 |
|---|---|
| 单测：3 次耗尽后 HTTP 请求总数 = `maxAttempts × 3` | 通过 |
| 单测：锁存后再调 5 次，**0 次**新增请求 | 通过 |
| 单测：钩子恰好执行 1 次（并发 8 路也不重复） | 通过 |
| 单测：成功会清零计数，不会误熔断 | 通过 |
| 单测：不可重试错误 10 次也不熔断 | 通过 |
| 单测：下载流式路径同样熔断 | 通过 |
| mutation：去掉快速失败 | 测试报「多打了 15 次请求」，红 |
| 真机：健康的 3,309 文件增量跑 | `transferred 0`，日志 0 条熔断字样 |

### 顺带修正的一个旧结论

`TestBlockRetryReportsExhaustion` 原本断言服务端会收到
`maxAttempts × blockRetries = 12` 次请求，即「一个文件把重试全用光是正确行为」。
熔断之后变成 9 次（3 次耗尽即锁存），测试随之更新并写明原因 ——
**旧断言编码的正是要消灭的行为**，不能为了让它变绿就把它删掉。

### 教训

1. **单次重试 ≠ 整轮策略**。写重试逻辑时同时问：如果每次都会失败，
   这轮什么时候停？
2. **规模一上来，「行为正确」和「能在有限时间内结束」是两件事**。
   3,309 个文件时这问题完全看不见，74,313 个时它是决定性的。
3. **默认值要实测，不要凭印象**。rclone 的 `retries=3` 是从源码读出来的，
   「大概会重试几次」的猜测会让整段算术差一个数量级。
4. **熔断要能被复位**。计数被成功清零、锁存只在进程内有效，
   才不至于一次尖峰毁掉整个进程。

---

## D52 · 后端的上限必须由后端声明，不能写进共享代码（2026-11-09）

用户看到 F11 的记录后问了一句：「这个分桶是飞书独有的哦，如果以后我把 feishu 云盘
换成 smb 之类的，这个功能其实是不必要的」。

顺着这条核实，发现**不用等到换后端，现在就已经错了**。

### 实测：本地同步被飞书的上限拒绝

```
local -> local，源目录 1401 个文件，全程没有飞书参与
exit=1，目标目录 0 个文件
报错：目录 "(根目录)" 有 1401 个直接子项，超过飞书单层上限
      （我们按 1400 拦截，飞书硬限 1500）。NOTHING was uploaded。
```

本地文件系统没有单层 1500 的限制。这是一次**误拒绝**。

### 插拔本来是好的，错的是我放错的那一层

先确认不是「没预留插拔」：

- `main.go` 里 `_ "github.com/rclone/rclone/backend/all"` 注册了**全部上游后端**
- `split.Remote` / `split.StateMover` 本来就是接口，`NewRcloneRemote(fs.Fs)` 接受任意后端
- 除 `backend/feishu` 外，只有 `main.go` 一处空 import；通用层里的「feishu」字样
  全是注释和一条文案
- F10 实测过同一个 job 跑**飞书 + 本地磁盘 + union**，各自增量正确

问题精确地在一处：**rclone 抽象的是「操作」，不是「容量」**。核查
`rclone@v1.71.2/fs/features.go` 全文，`fs.Features` 有 27 个能力标志
（`CaseInsensitive`、`DuplicateFiles`、`CanHaveEmptyDirectories`、`BucketBased`、
`PartialUploads`、`SlowModTime`…）和能力函数（`Mkdir`、`Move`、`Purge`、`About`…），
**没有任何「一个目录最多放多少条」的字段**。这条信息在上游无处可放。

而我把这条后端专有策略写进了共享的预检，还：

1. 对**每个 pair 无条件**执行（`job/run.go:224`）
2. 在 `fs.NewFs` **之前**（那时根本不知道后端是谁）
3. job 文件里**没有任何开关**能关掉它（`Options.MaxNodesPerDir` 只有 Go 调用方能设，
   取 0 就落到 1400，**没有任何办法表达「不限」**）

### 修法

| 改动 | 说明 |
|---|---|
| 新增 `split.LimitReporter` | 后端声明 `MaxChildrenPerDir()` / `MaxDirDepth()`，0 = 无上限 |
| 新增 `split.LimitOf(fs.Fs)` | 断言接口；不实现就是「无上限」 |
| 飞书实现它 | `backend/feishu/limits.go`：1500 / 15，附官方出处 |
| 预检**移到打开后端之后** | 先问目的地，再决定要不要检查 |
| 后端没声明 → **完全跳过预检** | 不是「用默认值」，是根本不扫 |
| job 文件新增 `max_nodes_per_dir` | **三态**：不写=后端、`0`=不限、`n`=覆盖 |
| 错误文案去飞书化 | 报真实后端类型（`fs.ParseRemote` 解析，如 `feishu`），不再写死 |
| 拆分桶上限由后端推出 | `MaxPerBucketFor(后端硬限)`，与拦截阈值同一个数 |
| 拆分向导接口 | `feishu_limit` → `backend_limit` + `backend`；无上限时不报 finding |

### 三个自己踩到的坑（都是先测后改，不是推演）

1. **`fs.Find` 不能解析远端路径**。我先用了它，跑真机仍然显示「后端 test」。
   读 `rclone/fs/registry.go:401` 才发现 `Find` 只匹配**后端类型名**
   （`feishu`/`local`），不解析 `test:path`。正确的是 `fs.ParseRemote`
   —— 换掉之后真机输出变成 `后端 "feishu"`。
2. **测试里的「本地后端」其实一直是错的**。`daemon/split_test.go` 用 `:local:`
   却断言「飞书硬限 1500」；`job/precheck_test.go` 用 `localPair` 却测飞书限制。
   两个测试**自己就证明了这个 bug**：它们把飞书行为当成通用行为来断言。
   改法是让它们走 job 文件的 `max_nodes_per_dir`（真实用户路径），
   并新增一条「本地后端应当报 0 findings」的反向断言。
3. **顺手修的噪音**：`flushStateOf` / `installAbortHook` 的类型断言失败提示
   原本是 `note:`，对每个非飞书后端每轮都打一遍。改成 `fs.Debugf`。
   另外 `conflicts cannot be checked ()` 的空括号也修了 —— 后者是既有瑕疵。

### 验证

| 验证 | 结果 |
|---|---|
| 单测：`LimitOf(不实现接口的后端)` = 0/0 | 通过 |
| 单测：`RefuseAtFor` 覆盖 0 / 负数 / <Headroom / 1500 / 2000 | 通过 |
| 单测：`nodeLimit` 优先级（job 文件 > 后端 > 无） | 通过 |
| 单测：本地后端 1401 个文件不再被拒绝 | 通过（3.8s） |
| 单测：`max_nodes_per_dir = 0` 真的不限 | 通过 |
| 单测：负数被拒绝且错误信息点名该设置 | 通过 |
| 单测：`max_nodes_per_dir` 经过编码器能读回 | 通过 |
| mutation A：`LimitOf` 硬编码 1500/15 | 测试报 `OverLimit = [], want none`，红 |
| mutation B：编码器丢掉该字段 | 测试报 `Encode dropped the explicit zero`，红 |
| **真机**：local→local 1401 个文件 | `transferred 1401`，exit=0（修复前 exit=1、0 个文件） |
| **真机**：同样数据 → 真实飞书 | 仍拒绝，报 `后端 "feishu"`、1400/1500、`NOTHING was uploaded`，云端 0 条目 |
| 全量测试 + Linux 交叉编译 + gofmt | 全绿 |

### 教训

1. **「这个限制是哪个层级的」要在写代码前问一次**。数据约束（容量、命名规则、
   mtime 精度）是后端的；同步策略（删不删、留不留版本）是我们的。
   把前者的数字写进后者，一旦换后端就是误拒绝。
2. **测试用错了后端，等于给 bug 盖了章**。两个测试都用本地目录断言飞书数字，
   全绿，所以一直没人发现。**测试里的后端必须和断言里的限制属于同一类**。
3. **「没实现」和「默认值」必须能区分**。用 0 表示「用默认」的地方，
   就再也表达不出「不要这个行为」。三态指针（不写/0/正值）是这里唯一安全的形状。
4. **先核实工具再改代码**：`fs.Find` 那次如果只看函数名就下手，
   真机输出会一直是「后端 test」，而我可能把它当成「够用了」收工。

---

## D53 · 逐文件计划从 rclone 自己的日志里读，不另写一套比对（2026-11-09）

用户对界面的评价是：「不太好用。我觉得我还是喜欢 freefilesync 那种 UI 的逻辑」。

### 差距不在外观，在数据粒度

FreeFileSync 的流程是**先对比、逐文件看到动作、再汇总、最后才同步**，而且计划是
给人看的、可改的。我们的预览只有 5 个计数字段：

```
现在：preview → transferred 3309, deleted 0, bytes 1.9GiB
FFS ：preview → 3309 行，每行一个路径和一个动作
```

计数回答「有多少」，而「我能不能按下去」取决于「具体是哪些」。

### 关键决定：计划不能来自第二套实现

最自然的做法是自己写个 compare：遍历本地、列云端、用 state 比对。
**这条路我否掉了。**

理由是漂移。第二套实现迟早会和真正运行的 rclone 语义不一致（大小判据、
modtime 精度、过滤规则、`--backup-dir` 的影响…），而**一个和运行结果不一致的
预览比没有预览更糟** —— 它会训练用户不要再读预览。

改成从 rclone 自己的 dry-run 日志里读回来：

```go
// rclone fs/operations/operations.go SkipDestructive:
fs.Logf(subject, "Skipped %s as %s is set", fs.LogValue("skipped", action), flag)
```

- `log.Handler` 是**导出的包级变量**，用 `AddOutput(true, fn)` 挂一个 JSON 旁路
- JSON 行里有结构化的 `object`（路径）、`skipped`（动作）、`size`、`objectType`
- 于是每一行都是 **rclone 自己的决定**，计划和随后的运行**不可能不一致**

`AddOutput` 没有卸载接口，所以设计成「装一次、用标志位开关」：
进程内只允许一个采集器（`ErrBusy`），因为日志行不带作业标识，两个并发预览
会把彼此的记录混在一起。

### 方向怎么判断（这一步差点做错）

rclone 对上传和下载都记 `copy`。区分它们不能靠路径或作业变体猜，而是靠
`objectType` 与两个 Fs 类型比较：`SkipDestructive` 传的是**源对象**，所以
对象属于哪个 Fs 就决定了拷贝的方向。

### 中途被测试抓到的两件事

1. **`ApplySnapshot` 原地改切片**，第二次调用看到的是第一次改完的结果，
   误以为「无条件改」。改成返回拷贝 —— 一个函数名带 `Apply` 不代表调用方
   会预期自己被改。
2. **内容相同但时间戳不同的文件会被计划成 `touch`**。`TestPlanIsEmptyWhenNothingToDo`
   因此红了。这不是 bug，是 rclone 的诚实行为。于是：
   - 给元数据行一个独立动作 `Touch`，而不是塞进「其他」
   - `Summary.Empty()` **不把 Touch 算作变化**，否则已同步的目录永远显示「有变更」
   - 目录的 `set directory modification time` 也归到 Touch
     （实测一个 2 目录的作业产生 2 行「其他」，点进去看才发现是它）

### 验证（都是实测）

| 验证 | 结果 |
|---|---|
| 单测：复制方向、删除归属、目录动作、大小解析、去重、`ErrBusy` | 10 条全绿 |
| 端到端：真实 dry pass 的行数 == `PassResult.Transferred` | 7 == 7 |
| mutation：`dispatch` 直接 return | 测试报 `plan has 0 copy rows but the pass reported 7` |
| 真机：`jl6` 预览（已同步 3309 个） | 计划 **4214 新建 / 43 目录 / 241.5 MiB** |
| 交叉验证：同一 job 的独立 CLI dry-run | `would-xfer 4214` —— **完全一致** |
| 交叉验证：本地 `_` 前缀文件计数 | **4214** —— 数字来源被独立确认 |
| 浏览器：筛选、显示元数据、加载更多 | 13 / 25 / 23 行，**0 条控制台错误** |

> 那个 4214 一开始看起来像 bug（「明明已同步，怎么又要传 4214 个」）。
> 查下去才发现：之前真实同步用的 `--exclude '_*'` 是**命令行参数，不在 job 文件里**，
> daemon 没有这些规则，所以它确实会传那些文件。计划是对的，
> 而且顺带暴露了一个用户的真实配置缺口。

### 没做的部分

**逐行勾选**（只同步选中的行）没做。它需要给 `RunPair` 传一个「只做这些路径」的
include 过滤器，而现有基础设施只有 exclude 一侧，改动和验证都更大。
先把「看得见每行」做出来，让用户确认方向。

### 教训

1. **别写第二套实现去预测第一个实现**。能问的就问，能读的就读。
2. **`objectType` 这类元信息比路径猜测可靠得多**，前提是先读源码确认它确实存在。
3. **「相同」也要分层次**：内容相同、时间戳不同是两种状态，混在一起
   会让「已同步」这个判断失效。
4. **模糊的新数字要先找独立来源**。4214 和文件计数、CLI dry-run 三方对上之后，
   才敢说它是对的。

---

## D54 · 一个同步会话是四件事，不是一堆设置项（2026-11-09）

用户连问八条：如何加 pair、如何改统一过滤器、如何给单独 pair 加过滤器、
如何设同步动作、如何给单独 pair 设动作、执行比较还是同步、如何设比较动作、
如何给单独 pair 设比较动作。「这些逻辑你都一样么？」

核对结果是**一半不存在**：

| 要素 | 当时的状态 |
|---|---|
| 路径（pair） | ✅ 有 |
| 过滤 | ⚠️ 只有全局 exclude，没有 include，没有 pair 级 |
| 比较动作 | ❌ 完全没有这个概念 |
| 同步动作 | ✅ 有（变体），但藏在「配置」页长表单里 |

### 用 rclone 的规则集时踩的两个坑

**坑一：单加 include 不构成白名单。** 三个文件全被传了。读
`fs/filter/rules.go` 才发现 `ruleSet.include` 在什么都不匹配时返回 **true** ——
include 规则只是「重新包含」，要配一条兜底 `/**` 排除才形成白名单。rclone 自己的
CLI 就是这么做的（`addImplicitExclude` 分支），我照抄了它，并注明来源。

**坑二：手写编码器把嵌套表写成了扁平键。** `include`/`exclude` 属于
`[pair.sync.filter]`，我写成了 `[pair.sync]` 的直接键，于是保存出来的文件
**自己解析不了**（`strict mode: fields in the document are missing in the target
struct`）。测试是先过的 —— 因为 `TestEncodeCoversEverySyncField` 只填了它当时
知道的字段。补上新字段后才抓住。

### 内容比较的定位（用户纠正过我一次）

我先论证了「下载比对很贵、通常不划算」，用户回：「我讲的是这个功能，我可以做这个
功能但是不用啊，你在纠结什么」。

**这是我把「该不该用」和「做不做」混为一谈了。** 功能该做，用不用是使用者的事；
界面标注成本就够了。最终实现：

- `content` 会把云端那份下载回来逐字节比对（`operations.CheckIdenticalDownload`）
- 比对结果为「相同」的路径被**排除出同步**，所以预览说一样、执行就真的不传
- 只对「本来要传的文件」做，不是全树

### 验证

| 验证 | 结果 |
|---|---|
| 单测：三档解析、拼错被拒 | ✅ |
| 单测：过滤 job+pair 叠加、空行被拒 | ✅ |
| 单测：include 白名单真的限制传输 | ✅ 只剩 .xlsx |
| 单测：exclude 在预览与执行两趟一致 | ✅ |
| 单测：size-only 同大小不上传，默认策略会上传 | ✅ 两条对比测试 |
| mutation：去掉 include 兜底规则 | 红：`transferred = 3` |
| mutation：filter 写成扁平键 | 红：`Encode dropped "[pair.sync.filter]"` |
| 真机：配置页保存往返 | ✅ `size-only` + 两组规则完整读回 |
| 真机：同一作业预览 | ✅ 4 新建 + 2 删除 → 2 新建（生效） |

### 教训

1. **「功能该不该用」不是「功能该不该有」**。前者是使用者的事，后者是我的职责。
   可以标注代价，不能替用户否决。
2. **第三方规则集的默认值必须实测**。「include 就是白名单」是直觉，而
   `include` 的默认返回是 true —— 差一条兜底规则，语义完全相反。
3. **手写序列化器的字段覆盖测试要跟着字段走**。它只保护它知道的字段，
   新字段必须同步加进去，否则「测试通过」只是「没测」。

---

## D55 · 主界面是「一次同步」，其余是抽屉（2026-11-09）

> 用户 2026-11-09：「你还是按照之前你的菜单结构啊，没有按照 FFS 的 UI 结构做啊」
> 用户 2026-11-09：「方向对，但是还很简陋啊，添加 pair 好像也没有？你要不先仔细看看
> FFS 都有哪些按键功能，自己检查补一下？」

### 背景

F13 阶段 2 做出的对比视图本身是对的，但外面那层壳仍然是「先选作业 → 再进配置页」。
FreeFileSync 的壳是反的：**打开就是一组文件夹对，比较是这一屏，同步是它的下一步**。
作业列表、完整配置、冲突、历史、日志、拆分、凭据这些是维护用的，不该和主任务抢位置。

### 决定

**主屏 = 一个作业的全部文件夹对 + 比较 + 同步 + 对比结果。** 其余全部挪进右上角 ⚙
打开的抽屉。这个划分的依据是使用频率与危险程度：主屏上的每个动作都直接作用于数据，
抽屉里的动作要么只读（历史、日志、拆分快照），要么是一次性的设置。

**然后按 FFS 的控件清单逐项对照补齐**，不是凭印象加按钮：

| FFS 有的 | 这次做了什么 |
|---|---|
| 多文件夹对，每对一行 | ✅ 路径 + `…` + `⇄` + 方向 + `过滤…` + `✕` |
| 添加文件夹对 | ✅ 追加并落盘 |
| 交换左右 `⇄` | ✅ 交换两侧路径，同时清掉 `remote_backend`（它描述的是右侧，跟着换就错位） |
| 点列头排序 | ✅ 名称/大小/动作/右名称/右大小，升序↔降序 |
| 逐文件操作右键菜单 | ✅ 只同步这一项 / 取消勾选 / 取消同类 / 加入排除规则 |
| 分类开关 + 计数 | ✅（阶段 2 已有） |
| 保存/另存配置（profile） | ❌ 未做，见下 |
| 文件日期列 | ❌ 未做，见下 |
| 按大小/时间跨度过滤 | ❌ 未做，见下 |

**已知缺口，记账不装样子**：

1. **配置 profile（保存/另存）**：现在「保存」就是写回该作业的 job 文件，没有命名配置
   的概念。要做等于引入「一份配置 = 一个文件」的管理层，先等用户确认需要。
2. **日期列**：`plan.Row` 里没有 mtime。计划行从 rclone 的 dry-run 日志读回来，
   而那条日志只带 `size`，不带时间 —— 要显示日期必须另取一次目录列表，
   与本设计「计划只能有一个来源」相冲突。要么接受多一次列表，要么放弃日期列。
3. **大小/时间跨度过滤**：需要把过滤条件传进 rclone 的 `--min-size` / `--max-age`，
   属于 `job.Options` 的新字段，不是界面活。

### 逐行勾选为什么不是 FFS 的复制

FFS 只能按*类别*过滤，不能只勾一个文件。这里加了逐行勾选，因为它对「先试一个」
这种真实需求是必需的。代价是必须保证**勾选真的限制运行**，否则它就是个装饰。

实现上最容易错的一步是「哪些入口读勾选」。第一版只有结果区的按钮读，
顶部那个照旧全量跑 —— 实测 5 个文件全被传输，勾选形同虚设。现在三个入口
（顶部同步 / 按此执行 / 右键）全部走 `syncOnly(job)`，并要求面板确实在显示这个作业。

### 验证

| 验证 | 结果 |
|---|---|
| 勾选后页脚提示 | ✅ 7/7 → 6/7 |
| 取消勾选的文件不进运行 | ✅ `transferred 2`（19 B），dst 无该文件，守护进程日志也无该行 |
| 排序升降 | ✅ 3 B → 10 B → 502 B → 2.9 KiB（▲）→ 反转（▼） |
| 右键「只同步这一项」 | ✅ 勾选 1/4 |
| 右键「加入排除规则」 | ✅ 写入 `[job.sync.filter]`，重新比较后该行消失 |
| `⇄` 交换 | ✅ src/dst 互换并可换回 |
| 添加/删除文件夹对 | ✅ 1 → 2 → 1，两次都落盘 |
| 控制台错误 | ✅ 无 |

### 教训

1. **「NUL 分隔符」在 HTML 属性里是不存在的**。`data-key` 带 `\u0000` 会被浏览器丢掉，
   于是 key 永远对不上、取消勾选永远存不住。凡是往 DOM 属性里塞复合 key，
   先问一句「这个字符浏览器收不收」。
2. **同一件事有多个入口，就必须只有一个实现**。三个同步按钮各写一遍，
   必然有一个忘记新规则。合并成 `syncOnly()` 之后，加规则只改一处。
3. **CSS 的 `.hidden` 不是万能的**。同特异度、后出现的 `display: grid` 会压过它，
   面板就关不掉。布局容器的显隐规则要写 `:not(.hidden)`。

---

## D57 · 设置放进对话框，主界面只留路径、表格、和底部两个按钮（2026-11-10）

> 用户 2026-11-10：「我就要你参考 ffs 的这套点击流程，而不是自己开发一个什么方向、
> 什么同步的框在这里，我都不知道什么时候点什么」

D56 把选项做进了页面正中的卡片，理由是「这两个选择决定会不会删文件，不该藏起来」。
用户否掉了这个判断，而且给出的理由比我的更硬：**一个页面上同时摆着路径输入、
两套单选、过滤按钮和结果表，人不知道该先点哪个**。FFS 不这么做，不是因为它没地方，
而是因为这些是「设一次就走」的东西。

### 决定

照 FFS 的三段式：

```
工具条      [≙ 比较设置: 时间+大小] [≡ 过滤器: 未设置] [⇄ 同步设置: 更新]
路径行      [左路径][浏览] ⇄ [右路径][浏览] [⚙][−]
结果表      对比结果（始终在，未比较时是占位说明）
底部条      状态                       [停止] [比较] [同步]
```

**每个设置按钮上印着当前值**，所以不打开任何窗也知道这个配置是怎么设的；
要改就点那个显示着它的按钮。这正是 FFS 工具条的用法。

三个对话框（比较 / 过滤器 / 同步）内容与 D56 相同：每个选项带一句「它做什么」。
点确定才写进内存配置并标记未保存，取消恢复打开时的快照 ——
否则一次误点会被下一次「比较」悄悄存进文件。

### 单个文件夹对的设置

FFS 完全没有这一层：要不同行为就另存一份配置。程序能存，所以保留，
但**移出主界面**，放进每行右侧的 `⚙`；那一对如果有独立设置，图标变成 `●`。
对话框里每组的第一项是「跟配置」，选中它就等于没改。

### 对比结果常驻

FFS 的对比表格就是主区域，不是事后弹出的卡片。这里改成常驻：未比较时显示
「还没有比较结果。点右下角「比较」……」。若这个作业之前比较过（`/api/status`
里带着 plan），载入时直接把那次结果显示出来，标题里带时间戳 —— 否则空表格
会被读成「没有需要变更的」，而它其实只意味着「还没比过」。

### 挖出的两个真 bug

1. **`SyncView.Compare` 是生效值，不是 pair 自己写的值**。于是每个 pair 看起来
   都有 compare 覆盖，`●` 永远亮着，「跟配置」也无法正确显示。加了
   `compare_set`（对应已有的 `max_delete_set` 做法）。清空覆盖时文件里那行要真的
   删掉，不能把继承值回写成显式值 —— 那会把 pair 钉死在今天的默认值上。
2. **勾选在同步时又失效了一次**。`runSession` 里先 `saveSession()` 再
   `loadSession()`，而 `loadSession` 会重建页面并清空对比结果，于是请求发出时
   勾选状态已经没了 —— 确认框写着「只同步勾选的 1 / 2 项」，实际传了 6 个。
   改成**在保存往返之前**读取勾选。这条错的正是「确认框说的和实际做的不是一回事」，
   比起多传几个文件，更糟的是它骗人。

### 验证

| 验证 | 结果 |
|---|---|
| 工具条三个按钮显示当前值 | ✅ `时间 + 大小` / `未设置` / `更新` |
| pair 覆盖时同步按钮显示真实生效值 | ✅ `镜像×1`（作业 update + pair mirror） |
| 三个对话框打开、选项与描述、Esc 关闭 | ✅ 比较 3 项、同步 3 项、过滤两栏 |
| 同步对话框的风险提示按 pair 计数 | ✅ `1 / 1 个文件夹对会删除云端多余文件` |
| 取消不落盘 | ✅ Esc 后工具栏不变、无未保存标记 |
| 确定才写入文件 | ✅ 改 update → `variant = "update"` |
| 「全部跟配置」清掉覆盖 | ✅ 文件里 `variant` 行消失，`compare_set` 变 false |
| pair 对话框首项为「跟配置」 | ✅ 未设置时选中它（`compare_set` 修复前会误选 time-size） |
| 勾选真的限制运行 | ✅ 取消 `skip_me.txt` → `transferred 1`，目标目录无该文件 |
| 结果常驻 + 载入旧结果 | ✅ 标题带 `· demo · 2026-11-10 11:40` |
| 单测 | ✅ `CompareSet` 4 例 + 清空覆盖 1 例 |
| gofmt / vet / test / Linux 交叉编译 | ✅ 全绿 |

---

## D58 · 视觉重做，顺带修掉三个只有画出来才会暴露的问题（2026-11-10）

> 用户 2026-11-10：「可以再优化一下前端视觉效果么？然后我再试一下」

### 做了什么

建立了一套设计令牌（圆角 / 阴影 / 两级墨色 / 强调色），然后按层次重排：

| 区域 | 改动 |
|---|---|
| 字号 | 基准 13.5px，标题/标签/等宽三档分明；数字用 `tabular-nums` 对齐 |
| 工具条 | 图标放进 30px 方块，标签小写大字距，当前值加粗；`hover` 有淡阴影 |
| 文件夹对 | 输入框改无边框「凹槽」，聚焦才描边；上下文按钮 `⇄` 有独立方块；删除键 hover 变红底 |
| 对比结果 | 去掉卡片标题与内层标题重复；表头与上方「本地/动作/云端」用同一套列宽 |
| 动作列 | 从裸箭头改成彩色胶囊 —— 12px 的箭头是个小点，颜色要独自承担「这会删东西」 |
| 分类开关 | 胶囊带彩色描边，计数用等宽数字；禁用的降到 38% |
| 底部条 | 加边框卡片化，主按钮实心蓝 |
| 对话框 | 140ms 淡入缩放；选中项整行描边+淡蓝底（只有名字变蓝太安静） |
| 滚动条 | 统一 10px 细条，`scrollbar-width: thin` |

### 画出来才发现的三个真问题

1. **底部条 `position: sticky` 会压住结果表**。短视口下它浮在表格上方，最后几行被盖掉
   （实测：3 行的计划被压掉 1 行）。FFS 能把按钮固定在窗口底部，是因为整个程序就是一个
   固定窗口；网页会滚动。改成普通流式布局。

2. **`input[type=text]` 匹配不到没有 `type` 属性的 `<input>`**。结果区的「过滤路径…」
   没有写 `type`，于是拿到浏览器默认样式 —— 暗色模式下是一个刺眼的白框。
   选择器补 `input:not([type])`，并给那个 input 补上 `type="text"`。

3. **sticky 顶栏会吃掉锚点跳转后的点击**。`scrollIntoView` 把元素滚到顶边，正好被
   54px 的顶栏盖住，点上去命中的是顶栏（实测 Playwright 反复报
   `<div class="topbar"> intercepts pointer events`）。加 `html { scroll-padding-top: 68px }`。

4. 附带：表头与表格列宽不一致导致「动作」标签比动作列偏 7px，两行读起来像两张
   互不认同的表。用 `table-layout: fixed` + `<colgroup>` 固定六列，顶部网格用同样的
   `34px 1fr 62px 68px 1fr 62px`，实测两行左边界完全一致（74 / 299 / 361 / 429）。

### 验证

| 验证 | 结果 |
|---|---|
| 列对齐 | ✅ 表头 74/299/361/429，上方分组 74/…/429，逐像素一致 |
| 暗色模式 | ✅ 无白底控件；输入框底色 `rgb(21,26,33)` |
| 底部条不遮挡 | ✅ 结果表三行完整可见 |
| 勾选与页脚提示 | ✅ 取消 1 行 → `已勾选 2 / 3 项，只处理勾选的` |
| 横向溢出 | ✅ 0 |
| 控制台错误 | ✅ 无 |
| gofmt / vet / test / Linux 交叉编译 | ✅ 全绿 |

### 环境教训

**共享浏览器页的绘制尺寸和 `innerWidth` 可以不一致**。这一页的 layout 视口是 766px，
绘制面却是 1536×1076；`Emulation.setDeviceMetricsOverride` 能改 `innerWidth`，
但截图仍按真实面画。一度据此判断「布局没铺满」，其实是测量口径不同。
判读视觉前先量 `getBoundingClientRect()`，别信截图比例。

---

## D59 · 「已开始比较」是个假状态，还有四个名词没人看得懂（2026-11-10）

> 用户 2026-11-10：「左上角的 demo 是什么意思？对应 FFS 是什么」「比较 过滤 同步
> 三个标签对应 FFS 在哪里，我们这样排列好看么」「添加文件夹对，这个描述是不是
> 不太清晰，名词不太专业吧」「为什么又两行都有动作这个词，勾选的新建又是什么意思，
> 元数据又是什么意思，为什么一点从设计引导用户操作的概念都没有」
> 「我点了比较之后就显示已开始比较，等了半天没结果啊」

### 最严重的一条是最后那条：状态行永远不会变

`runSession` 提交后把状态写成「已开始比较。」，然后**再没有任何代码改它**。
一次本地比较 50ms 就结束了，而屏幕上的表格是上一次比较留下的、长得一模一样 ——
于是唯一的结论就是「还在跑」。用户等了半天，其实早跑完了。

三处一起修：

1. **进行中要能看出在动**。`busyJob` 记录这次是 page 自己发起的，`refresh()` 里
   每秒重算：`正在扫描并比较　已用 3 秒` + 旋转点。**秒数是预览唯一能给的存活信号** ——
   预览不传任何字节，字节计数器恒为 0，只看那个数字会以为卡死。
2. **结束要说结果**。`比较完成：3 项要处理` / `两侧一致` /
   `同步完成：传输 3 项，无错误`，失败则红字。状态行是同一个位置，语义前后接得上。
3. **进行中不要把旧结果留在屏幕上**。`showPlanBusy()` 换成「正在比较…」并置灰
   「按此执行」，否则前后两次表格一模一样，等于告诉用户什么都没发生。

### 名词：一个东西三个名字

`作业` / `会话` / `配置` 在界面里混着用，同一个概念。统一取 **配置**：
FreeFileSync 里它就是当前打开的那个 `.ffs_gui`，而「作业」暗示调度、「会话」
暗示一次使用过程，都不对。顶栏那个下拉因此加了「配置」标签 —— 这就是用户问的
「左上角的 demo 是什么」。

`文件夹对` → **`目录对`**。`文件夹` 是口语，`目录` 是术语；而且这一行的意思是
「左目录 + 右目录」，说「对」比说「文件夹对」准确。下面补一句解释，
因为「添加目录对」这个按钮只有在读者已经知道什么是目录对时才成立：

> 一组目录对 = 左边一个目录 + 右边一个目录，同步就在这两边之间发生。

### 工具条：三个字形是我编的，其中一个还撞车

`≙ ≡ ⇄` 三个符号没有任何约定含义，而 `⇄` **同时是路径行的左右交换按钮** ——
同一个字形两个用途。改成不依赖字形：两行文字（小标签 + 当前值）+ 一个 `›`
表示「点了会开窗」。标签改成 FFS 的原意：比较方式 / 过滤规则 / 同步方式。

### 两行都有「动作」

上方分组行写着 `同步 │ 本地 │ 动作 │ 云端`，下面表头又写 `名称 大小 动作 名称 大小`。
同一个词相隔两行出现两次，读起来像两张互不认同的表头。分组行的中间格改成空
（只留分隔线），「动作」只在表头出现一次。

### 勾选框有两种语义，这比缺失说明更糟

行首方框 = 这一行要不要同步；底部那排 = 哪些**类别显示**在这张表里。
两个都是勾选框，含义完全不同 —— 用户把「元数据」取消勾选，会以为元数据不同步了。

改法：底部那排从 `<input type=checkbox>` 改成 `aria-pressed` 按钮，
取消显示时加删除线，前面加「显示」二字锁住语义，footer 改成
「另有 N 项被上面的「显示」按钮隐藏」。并补一条可展开的图例，逐条解释
新建 / 更新 / 下载 / 删除 / 目录 / 元数据 各是什么意思，以及
**左侧方框控制要不要同步，底部按钮只控制显示**。

### 验证

| 验证 | 结果 |
|---|---|
| 比较进行中的状态 | ✅ `stat busy` + `正在扫描并比较…` + 结果区「正在比较…」 |
| 比较结束的状态 | ✅ `stat done` + `比较完成：3 项要处理` |
| 同步进行中 / 结束 | ✅ `正在同步…` → `同步完成：传输 3 项，无错误` |
| 顶栏标签 | ✅ `配置` + 下拉 `demo` |
| 工具条 | ✅ 三个按钮均无字形图标、有 `›`、标签为 比较方式/过滤规则/同步方式 |
| 「动作」只出现一次 | ✅ 分组行中间格为空 |
| 底部按钮语义 | ✅ `aria-pressed`，元数据默认 false（加删除线），点开后行数 3 → 5 |
| 图例 | ✅ 6 条解释 + 选中/显示的区别 |
| 目录对术语与说明 | ✅ `+ 添加目录对` + 一句解释 |
| gofmt / test | ✅ 全绿 |

---

## D60 · 照 FFS 做 = 照它的手册和截图做，不是照我的想象做（2026-11-10）

> 用户 2026-11-10：「没有两条路，你只有看起 FFS 一条路」「我说的就是要仿照 FFS，
> 如果你连照葫芦画瓢都做不好我还怎么给你提更多的要求」

### 之前错在哪

前面几轮我一直在「凭印象仿 FFS」，结果是连它的**结构**都没对上：

- 没有菜单栏，配置管理是一个下拉框；
- Compare / Synchronize 在页面底部，而 FFS 把它们放在工具栏；
- 用了自造的分类词（新建 / 更新 / 下载 / 删除 / 目录 / 元数据），FFS 用的是**方向符号**；
- 没有任何保存 / 打开配置文件的能力，却把它叫作「配置」。

这一轮改成先取权威依据再动手：FFS 手册的主界面页给出了编号的布局图与基本顺序
（「选左右目录 → 比较 → 选同步设置 → 点 Synchronize」），配合官方截图逐块比。
真正可信的是那张截图：它给出了工具栏里每个控件的形状、Configuration 面板的
四个按钮、Overview 的列、底部的统计栏。

### 现在的结构（对应截图）

```
标题栏  weftsync — <配置名>[*]            File  Tools  Help
工具栏  [比较设置] [⊞ Compare / 时间+大小 ▾] [漏斗] [同步设置] [⇄ Synchronize / 更新 ▾] [■]
左停靠   Configuration: New / Open… / Save / Save as…  + 配置列表（✔ 标当前）
        Overview:      Folder | Items | Size
右工作   目录对（左 浏览 ⇄ 右 浏览）  +  结果表  +  分类统计条
```

**两个大按钮的第二行印着当前方式**（`Compare / 时间 + 大小`、`Synchronize / 更新`），
这是截图上最显眼、也最容易被漏掉的一点：FFS 不需要单独的「当前设置」读数，
因为按钮自己就写着。▾ 用来快速切换，不必先开对话框。

### 分类符号改回 FFS 的五个

| 符号 | 含义 | 我们的哪种动作 |
|---|---|---|
| `→` | 往右复制 | create / update / mkdir |
| `←` | 往左复制 | download |
| `=` | 内容相同 | touch / other（只改时间戳） |
| `✗` | 删除 | delete-remote / delete-local / rmdir |

FFS 按**方向**分类，不按动作种类分，所以「新建」和「更新」都是同一个 `→`。
我原来把它的一个维度劈成两个词，还多造了一个「元数据」类别。现在符号回归五个，
中文说明只放在 title 里。底部统计栏是 `→ n ← n = n ✗ n` + `Statistics: n files / n bytes`。

**代价要说清楚**：这样丢掉了「哪个是新建、哪个是覆盖」。要看具体是哪种，悬停有
原文；`plan.Row.Action` 本身没变，只是显示层换成 FFS 的语言。

### 配置成为一等文档

新增 `/api/config/files`、`/api/config/open`、`/api/config/save-as`（见 D59 前一段
提交）。UI 上是 Configuration 面板的四个按钮 + File 菜单，两边都能用；
标题栏显示配置名，未保存时带 `*`。

走查时抓到两个必须修的：

1. **另存后编辑器没有切过去**。`switchTo` 里原本先 `loadSession()` 再依赖
   `syncSessionPicker()` 找名字，但那份列表来自 `/api/status` 快照，
   新配置还没进去，于是选择被回退到旧配置 —— 提示「已另存为」而界面还停在原地。
   改成先 `await refresh()` 再 `loadSession()`。
2. **New 之后标题仍显示旧名字**。草稿没有名字，`updateTitle` 却回退到
   `sessionJob`。改成显式区分「没有配置 / 有名字 / 草稿（未命名）」。

### 去掉的东西

- **三步引导**（①看 ②取消勾选 ③点同步）：用户说「谁家软件这么引导」，
  完全正确 —— 那是教程语言。控件自己说不清楚才需要编号图，那就该改控件。
- **可展开图例**：FFS 没有它。符号的含义进了 title 和对话框描述。
- 「添加目录对」那句解释、工具条上的中文小标题、页面级的居中窄栏布局。

### 验证

| 验证 | 结果 |
|---|---|
| 菜单栏 / File 菜单六项 | ✅ New / Open… / Save / Save as… / Compare(F5) / Synchronize(F9) |
| 工具栏两个大按钮带副标题 | ✅ `Compare 时间 + 大小`、`Synchronize 更新` |
| Configuration 面板 | ✅ 四按钮 + 列表，当前项蓝底带 ✔ |
| Overview 面板 | ✅ `Folder / Items / Size`，比较后 `src 2 7 B` |
| 另存为 | ✅ 生成 work-copy.toml，标题切到 work-copy，列表出现第二项 |
| New | ✅ 标题变「（未命名）*」，Save 自动转为 Save as |
| Open | ✅ 列出目录内 .toml，选 job.toml 后切回 demo |
| 分类符号 | ✅ `→ 2 ← 0 = 1 ✗ 0`，点 `=` 只控制显示（行数 2 ↔ 3） |
| 完整链路 | ✅ Compare → `比较完成：2 项要处理` → Synchronize → `同步完成：传输 2 项，无错误` |
| 引导与图例已移除 | ✅ 两者都不在 DOM 里 |
| 控制台错误 | ✅ 无 |
| gofmt / test | ✅ 全绿 |

### 教训

1. **「照 X 做」要去拿 X 的原始资料**，不要用记忆里的 X。手册给了布局编号，
   截图给了控件形状，两者一对就把我前几轮的偏差全暴露了。
2. **emoji 不是图标**。`🗋 🗁 🖫` 在不同系统上渲染成彩色图形或方框，和线条图标
   放在一起就是脏。改成内联 SVG，颜色跟 `currentColor` 走。
3. **用户否掉的不是工作量，是判断**。我说「两条路（补齐 / 改名）」，答案是没有
   两条路 —— 该做的是补齐。把选择题抛回去，等于把自己的判断失误转成用户的负担。

---

## D56 · 云端路径不能靠手抄，选项不能只给名字（2026-11-10）

> 用户 2026-11-10：「虽然现在更像FFS了，但是细节差很多，同步和过滤的选项的表示方式，
> 描述」「文件夹对如果是飞书路径如何选，我也没搞懂」「UI也不好看」

### 三个问题，三种病因

| 现象 | 真因 |
|---|---|
| 飞书路径不知道怎么选 | `…` 只会列本地目录。云端那一侧根本没有选择器，只能手抄 `feishu:token/子目录` —— 而 token 是一串没人记得住的字符 |
| 选项只有名字 | 两个下拉框。`镜像（会删）` 三个字承担了「会删云端数据」的全部重量，选项之间也无法对照 |
| 界面不好看 | 路径行被压扁：`grid-template-columns: 1fr 40px 1fr auto` 里两个 `1fr` 有 min-content 下限，长路径没有空格可断，于是把输入框挤成一条缝，按钮还被 `auto` 列推到行外 |

### 云端浏览：必须走后端，不能查配置

`/api/browse` 现在同时接受本机路径和 `<remote>:<path>`：远端那一半用
`fs.NewFs` 打开再 `List`，因为**只有真正列一次才知道凭据是否有效**。
读 rclone.conf 得到目录名是不可行的（配置文件里没有目录树），而
「先在配置里写对、等下次运行才报错」正是要消灭的体验。

路径分类是这整块的铰链，写成 `isRemotePath`：

- 一个字母 + 冒号 = Windows 盘符，不是 remote（`C:\data`）
- `:local:C:/data` 是 rclone 的即时后端写法，属于**本地**，先剥前缀再判
- 其余 `name:...` 才是云端

选择器一次给出两组起点：**本机**（盘符 / `/`）与**云端**（每个已配置 remote，
带类型标签）。这样无论从哪一侧打开，都能跳到另一侧，不必关掉重开。

### 选项：名字后面必须有代价

FFS 把这些放在对话框里，因为原生窗口没地方。网页上这个选择就是页面的重点，
所以直接摊开：`比较方式` 与 `同步方式` 各三个单选，每个带一句「它到底做什么」。
比较的描述与后端 `compare_help` 是同一批句子，避免页面和服务器说两套话。

**关键一点**：底部风险提示必须看 pair，不能只看作业。实测过：作业级改成 `update`、
pair 仍写着 `mirror`，页脚差点显示「当前不会删除任何文件」—— 那比不显示更糟。
现在它数一遍生效结果是 mirror 的 pair，输出「N 个文件夹对会删除云端多余文件」。

### 教训

1. **一个 `1fr` 网格轨道有 min-content 下限**。长路径没有可断行的地方，两个 `1fr`
   就会互相挤压并把旁边的按钮挤出可视区。要允许收缩必须写 `minmax(0,1fr)`。
2. **「出错才给的东西」等于「平时没有」**。`browseRoots()` 原来只在列目录失败时
   才写进返回值 —— 于是 Windows 上永远没有盘符列表，从 C: 打开就再也出不去。
   这类「只在异常分支赋值」的字段，要有一条断言正常分支也带着它。
3. **危险的默认值要单独说一遍**。三个单选的视觉重量一样，光靠描述末尾的一句
   提醒不够；底部那条「会删 / 不会删」是把不可逆性单独拎出来讲。

### 验证

| 验证 | 结果 |
|---|---|
| 真实飞书列目录 | ✅ `test:` → 2 个根目录；下钻一层后 `parent` 正确、`↑` 可用 |
| 云端选中回填 | ✅ 选 `test:KARRfvo…` 后 pair 的 remote_root 立即变成该值 |
| 两组起点 | ✅ 本机 `C:\ L:\ X:\` + 云端 `test:`（带 feishu 标签） |
| 路径框不再被压扁 | ✅ 两个框各 403px，卡片 1036px，横向溢出 0 |
| 选项描述可见 | ✅ 6 个 `.opt` 全部渲染出描述行 |
| 风险提示跟随 pair | ✅ 作业 update + pair mirror → 「1 个文件夹对会删除云端多余文件」 |
| 选择落盘 | ✅ 改同步方式为 update 后点比较，文件里 `variant = "update"` |
| 控制台错误 | ✅ 无 |
| 单测 | ✅ `isRemotePath` 14 例、路径拼接/父级/显示 14 例、根列表与双世界 4 例 |






## D61 · 「还是很丑」的答案不是再加功能，是把三处不齐的地方对齐（2026-11-11）

用户圈着工具栏中间一块 623px 的空白问「这是啥啊」。查了 DOM，那块里只有一个
`<span class="grow">`：宽 623px、无边框、透明。**它什么都不是。** 空白在行尾
读作「这行结束了」，在行中间读作「这里少了个控件」。

### 这一轮改了什么

| 问题 | 改法 |
|---|---|
| 工具栏中间一块空 | 删掉「组间弹性空隙」，多出来的宽度留在行尾 |
| 行尾 `停止` 是个没边没字的灰方块 | 变成 `■ 停止` 带标签；禁用时去掉边框和底色，让它不像「一个坏掉的按钮」 |
| 六个控件挤成一排分不出组 | 加两处分隔竖线：比较组 · 两个设置 · 同步组 + 停止 |
| 齿轮图标在 16px 下看着像太阳 | 换成两条横滑杆；每对的独立设置换成三条竖推子（原来那条横滑杆和右边的 `⇄` 撞脸） |
| `⇄` 图标被压成 6×15 的竖片 | `.pair-swap` 是 flex 容器，图标没写 `flex:none` |
| 两个路径框长得一模一样 | 各加一个 `本机` / `云端` 标签；placeholder 里那句提示一旦有值就没了 |
| `⚙ ● − +` 是文字字符 | 换内联 SVG：emoji 字符在某些系统上渲染成彩色字形或方框，那不是图标 |
| 表头写着 `同步`，下面是勾选框 | 表头改成真正的全选框（全选 / 全不选 / indeterminate） |
| 同一个「全选」有两条路 | 删掉筛选条上的 `全选` `全不选` 两个按钮 |
| `共 4 项` 下面是 2 行 | 筛选条写 `共 4 项 · 已列出 2 项`，页脚只说「另有 2 项被分类开关隐藏」 |
| 中英混排 | `File/Tools/Help` `Configuration/Overview` `New/Open/Save/Save as` `Compare/Synchronize` `Changes/Size` `2 files` 全部改成中文 |
| 三层同样粗的 1px 边框套在一起 | 去掉 `.ffs` 自己那圈，卡片已经有边框 |
| 同屏三种圆角 6 / 9 / 13 | 容器统一 `--r`(9) |
| 筛选条白、分类条灰 | 同一个底色 |

### 顺带修掉的两个只有真画出来才会发现的

1. **`admin` 时钟和连接徽章粘成一句**：`17:15已连接`。`.menubar` 是 `gap:0`，
   `#now` 还带着行内 `margin:0`，类里的 margin 改不过行内样式。
2. **823px 宽下首屏全是「配置 + 概览」**：停靠面板的断点是 860px，而这一页要看的
   是对比结果。断点降到 700px，823px 下仍是左停靠（实测 dock 236 / work 561，
   横向溢出 0）。

### 验证

| 验证 | 结果 |
|---|---|
| 控制台错误 | ✅ 无（每次重建都重挂 `pageerror` 监听） |
| 全选框 | ✅ 全勾 `true/false/false`；取消一行 `false/true/false`；再点回全勾 |
| 勾选提示 | ✅ 取消一行后出现「已勾选 1 / 2 项，只处理勾选的」 |
| 表头与表格列 | ✅ `.ffs-grid` 与 `thead th` 四条边界逐一相同：289 / 517.5 / 585.5 / 814 |
| 823px 布局 | ✅ dock x=8 w=236，work x=254 w=561，`scrollWidth == innerWidth` |
| 暗色模式 | ✅ 无白底控件、无不可读文字 |
| `⇄` 图标尺寸 | ✅ 15×15（修前 6×15） |
| gofmt / vet / test / Linux 交叉编译 | ✅ 全部通过 |

### 教训

1. **用户圈出来的空白要先查 DOM，别猜**。一测就知道那 623px 里只有一个 spacer，
   于是问题从「这里该放什么」变成「这个 spacer 为什么在这」。
2. **同屏两种语言比两种语言都不好**。`比较 / 时间 + 大小` 这种半中半英，比全英
   或全中更像半成品。
3. **`flex` 容器里的 `<svg>` 必须 `flex:none`**，否则会被压成一条竖片。这类问题
   只有量 `getBoundingClientRect()` 才发现，肉眼看只是「图标有点怪」。

## D62 · 「不协调」是比例，不是细节（2026-11-11）

上一轮把颜色、图标、名词都收拾干净了，用户还是说布局不协调。这一轮先用数字
量了一遍他真正的窗口：`outerWidth 1702`，而我一直是在 823px 的面板里看的。
把布局按 1702 展开重测，问题立刻显形：

| 位置 | 823px | 1702px | 结论 |
|---|---|---|---|
| 停靠面板 : 工作区 | 240 : 559（30%） | 236 : 1440（14%） | 固定宽度在宽窗口里退化成一个窄条 |
| 单个路径框 | 90px | 553px | 窄窗口下主要控件只显示 13 个字符 |
| 名称列 | ~200px | 606px | 宽窗口下一列空白比内容多 |
| 工具栏控件末端 | 652 / 823 | **656 / 1702** | 一行控件只占 38%，后面是整条横线 |

### 改法

1. **给应用壳一个最大宽度**（`--shell: 1280px`，`margin: 0 auto`）。菜单栏、工具栏、
   内容区共用它。窗口再宽，行也不跟着拉长。
2. **宽屏（≥1180px）时同步组压到右端**：比较在左、同步在右，各占一端，中间那片
   空白读作「两组之间的间隔」。窄屏不做 —— 823px 下压右边会甩出 170px 空档，
   又变成 D61 里被圈出来的那块空白。
3. **目录对在 ≤1023px 时改成上下两行**：交换键竖着跨两行，操作键也跨两行。
   实测路径框从 90px 变成 317px。
4. **工具栏整行统一 40px 高**。以前每个控件自己长，两个带副标题的大按钮把行撑到
   51px，旁边四个方按钮就成了 40×51 的竖条，图标缩在中间像个空盒子。
5. **去掉工具栏那条通栏下边框**。留着它就等于画了一个矩形，矩形右半截是空的；
   去掉之后右边只是页面本身。这是 D61 第 2 条教训，上一轮我又把线加回来了。
6. **统一面板底色**（`--tint`）。`plan-head` 是 45% 灰、`ffs-tools`/`ffs-bar`/
   `panel-head` 是 55% 灰 —— 差 2% 的两个灰堆在同一张卡里，读起来是一处失误。
7. **修掉 8px 节奏里的一个 24px**：`.card` 自带 `margin-bottom:16px`（给抽屉里一叠
   卡片用的），放进 `gap:8` 的 `.work` 就叠成 24。实测目录对→结果是 8，
   结果→状态条是 24。
8. **页脚那行「另有 N 项被折叠」并进筛选条的计数**，`.ffs-more:empty` 不再占 14px。
9. **动作列的 `→` 换字体**：等宽字体里的箭头是一条细横杠，11px 下和「—」分不清。

### 验证

| 验证 | 结果 |
|---|---|
| 宽窗口（强制 1702）壳宽 | ✅ 工具栏/内容区 1280 居中（x=211） |
| 宽窗口分栏比例 | ✅ 240 : 1016（19% / 81%），修前 236 : 1440（14% / 86%） |
| 宽屏同步组贴右 | ✅ 同步 1209–1409、停止至 1483，与壳右内边距 1491 对齐 |
| 窄屏（823）不启用该规则 | ✅ 控件位置与上一轮一致，无中间空档 |
| 目录对上下两行 | ✅ 路径框 317px（修前 90px），横向溢出 0 |
| 垂直节奏 | ✅ 目录对→卡片 8px，卡片→状态条 8px |
| 工具栏行高 | ✅ 52px（修前 64），四个图标按钮 40×40（修前 40×51） |
| 控制台错误 / 暗色模式 | ✅ 无 / 正常 |
| gofmt / vet / test / Linux 交叉编译 | ✅ 全部通过 |

### 教训

1. **「不协调」先量窗口，别量自己的截图**。我一直在 823px 的面板里调，而用户的窗口
   是 1702px —— 同一份 CSS 在两个宽度下是两套比例。截图里的白边一开始还被我当成
   布局问题，其实那是画布比视口大。
2. **固定宽度的侧栏 + 无上限的主区 = 随窗口漂移的比例**。要么给容器上限，要么让侧栏
   按比例长；两者都不做，窗口一宽就散。
3. **`margin` 会和外层 `gap` 叠加**。网格/弹性容器统一用 `gap` 之后，子元素自带的
   `margin-bottom` 就该在容器内清零，否则同一条缝会出现两种宽度。
4. **媒体查询断点选在「控件还塞得下」的位置**，不是选个整数。1023 是两栏并排时
   路径框开始低于 300px 的地方。
