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
