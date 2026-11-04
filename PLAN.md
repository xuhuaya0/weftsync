# weftsync 项目计划

> 状态：**方向待确认** · 最后更新 2026-11-04
> 本文件只放计划；决策理由见 `docs/decisions.md`。

---

## 1. 当前状态（已验证）

| 项 | 状态 | 证据 |
|---|---|---|
| 本地 Git 仓库 | 已初始化，`main` 分支 | `git rev-parse --is-inside-work-tree` |
| 远端 `origin` | `git@github.com:xuhuaya0/weftsync.git` | `git remote -v` |
| SSH 认证 | 通过，账号 `xuhuaya0` | `ssh -T git@github.com` |
| 首次推送 | 完成，`722020a` | `git ls-remote --heads origin` |
| 上行追踪 | `main...origin/main` 无差异 | `git status -sb` |
| Spec Kit | 1.3.0，Copilot 集成，PowerShell 脚本 | `.specify/init-options.json` |
| 项目宪法 | **仍是空白模板**（占位符未替换） | `.specify/memory/constitution.md` |
| 业务代码 | **零** | 工作区仅 `.github` / `.specify` / `.venv` |

结论：这是一个**只有脚手架、还没定义要造什么**的仓库。

---

## 2. 阻塞项：必须先回答的问题

| # | 问题 | 为什么阻塞 |
|---|---|---|
| Q1 | `weftsync` 要解决谁的什么问题？一句话说清 | 决定 spec 的全部内容 |
| Q2 | 交付形态：CLI / 可 import 的库 / 常驻服务 / 桌面 | 决定目录结构与对外契约 |
| Q3 | 第一个最小闭环（能跑通什么就算有价值） | 决定 tasks.md 的切分粒度 |
| Q4 | 输入输出是什么格式（文件？寄存器？仪器？HTTP？） | 决定接口设计，最难事后改 |

**在 Q1–Q4 确认前不写业务代码**，只做工程基建（理由见 `docs/decisions.md` D2）。

---

## 3. 我建议的默认方向（待你确认，可整体推翻）

仓库名 `weftsync` = weft（织机纬线，横向穿引）+ sync（同步），字面指向
**「把多路来源编织成一条对齐的时间线」**。

结合你现有工作场景（以太网一致性测试：DUT 侧寄存器 + 示波器侧 TekExpress），
最贴合的三个候选假设，按我判断的可能性排序：

### 假设 A：测试结果编织器（可能性最高）
把**同一轮测试**的多源数据（DUT 侧寄存器快照、仪器侧 Margin/report、执行日志）
按 PHY / 模式 / 配对 / 时间对齐成一张可比对的表，支持跨轮次回归对比（这次比上次差多少）。
- 关键价值：现在只有一堆 PDF 和日志，缺「一轮 vs 上一轮」的可比视图
- 最小闭环：读入两份结果目录 → 输出一张 diff 表

### 假设 B：多设备配置同步器
把一路已验证的配置（寄存器/脚本）安全地套用到另一台同型号设备，并校验回读一致。
- 关键价值：人工抄寄存器易错
- 最小闭环：`plan → apply → verify` 三步，先 dry-run

### 假设 C：通用数据同步/ETL 骨架
与测试无关，纯做「多源 → 归一 → 落库/落文件」的通用管道。
- 关键价值：可复用
- 风险：范围太宽，容易做成没有场景的框架

> 三个假设我都**没有开始实现**。选哪个、或者都不是，请直接说。

---

## 4. 已完成的工程基建

- [x] `.gitignore`：排除 `.venv/`、`__pycache__/`、`.scratch/`、`reports/`、`*.pdf`、`*.wfm`
- [x] `README.md`：仓库结构与推送方式
- [x] `docs/decisions.md`：决策记录
- [x] `.gitattributes`：统一换行符，消除提交时的 CRLF 警告
- [x] `docs/constitution-draft.md`：宪法草案（**未替换** `.specify/memory/constitution.md`，等确认）

---

## 5. 下一步（确认方向后执行）

1. **落宪法** —— 用 `speckit-constitution` 把草案转正到 `.specify/memory/constitution.md`
2. **写 spec** —— `speckit-specify`：只写「做什么/为什么」，不写技术方案
3. **澄清** —— `speckit-clarify` 补齐边界条件（失败路径、幂等性、数据规模）
4. **定方案** —— `speckit-plan` 出技术选型与目录结构
5. **拆任务** —— `speckit-tasks` 拆成可独立验证的小步
6. **实现** —— `speckit-implement`，每步跑测试再合入

---

## 6. 本仓库特有的约束

- **位于 OneDrive 同步目录内**：`.git` 有被同步进程锁定的风险。出现 `index.lock` 时优先排查 OneDrive。
- **只用 PowerShell 5.1 语法**：命令用 `;` 串联，不用 `&&`。
- **临时文件写 `.scratch/`**，不写 `%TEMP%`。
- **脚本一律纯 ASCII**（PS 5.1 按 GBK 读 `.ps1`，中文会解析失败）；中文只放 Markdown。
