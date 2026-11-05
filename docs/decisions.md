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
