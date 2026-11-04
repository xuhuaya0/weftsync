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

## D2 · 方向未确认前不写业务代码（2026-11-04）

**决定**
只做工程基建（`.gitignore` / `README` / `.gitattributes` / 本文件 / `PLAN.md`），
不创建任何 `src/` 下的模块，不臆造 API。

**理由**
- 仓库名为唯一线索，产品意图有多个合理解释（见 `PLAN.md` 第 3 节假设 A/B/C）
- 猜错方向的代码要全部删掉，比不写更贵；且会污染后续 spec 的既有假设
- 用户明确要求「判断性结论需确认，不要自作主张」

**替代方案（已否决）**
- 按假设 A 直接搭骨架 → 万一方向不同，目录结构和契约都要返工

**退出条件**
Q1–Q4（`PLAN.md` 第 2 节）有答案后，本决策失效。

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
