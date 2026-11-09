# 作业文件（job file）参考

**状态**：F3 已实现。配置结构沿用 `PLAN.md` §10 的定义，本文是实现后的**准确定义**，
以代码（`src/go/job/config.go`）为准。

```powershell
weftsync run jobs/nightly.toml            # 真跑
weftsync run jobs/nightly.toml --plan     # 只报告将要做什么
```

---

## 1. 最小可用文件

```toml
[[pair]]
name = "work-docs"

[pair.local]
root = "D:/work/docs"

[pair.remote]
backend = "feishu"
root    = "team/docs"
```

默认行为：`variant = "update"` —— **只增不减**，永远不会删云端文件。

---

## 2. 结构

```
[job]                    作业头 + 默认值，被每个 pair 继承
  ├─ schedule            调度（F5 才会用到；现在接受但不生效）
  └─ sync                默认的删除策略
[[pair]]                 可重复
  ├─ local               本地一侧
  ├─ remote              远端一侧
  └─ sync                覆盖作业级默认值
```

---

## 3. `[job]`

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `name` | string | 空 | 仅用于日志 |
| `enabled` | bool | `true` | `false` 时整个作业跳过，退出码 0 |
| `variant` | string | `"update"` | 见 §6 |

## 4. `[[pair]]`

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `name` | string | 否 | 重复会报错；省略时自动为 `pair-N` |
| `variant` | string | 否 | 覆盖作业级默认 |
| `local.root` | string | 是 | 本地目录 |
| `remote.backend` | string | 否 | rclone 远端名，如 `feishu` |
| `remote.root` | string | 是 | 远端目录 |
| `remote.options` | table | 否 | 仅本 pair 生效的后端选项，见 §4.1 |

`backend` + `root` 会拼成 `<backend>:<root>`。`backend` 留空时 `root` 必须是完整路径
（含 `:`）；只写一个裸目录名会报错，因为「本地路径」和「远端路径」不能靠猜。

### 4.1 `remote.options` —— 让每个 pair 有自己的增量快照

```toml
[[pair]]
name = "work-docs"
[pair.local]
root = "D:/work/docs"
[pair.remote]
backend = "feishu"
root    = "team/docs"
[pair.remote.options]
state_file = "C:/Users/me/AppData/Roaming/weftsync/state/work.state.json"
```

**为什么需要它**：增量同步靠 state 快照。rclone 的 `--feishu-state-file` 是**进程级**
开关，一个进程里所有 pair 会共用同一份快照，于是互相把对方的记录当成「已同步」，
导致漏传。`remote.options` 是唯一能表达「每个 pair 一份」的方式。

**允许的键**：目前只有 `state_file`。其他键会报错而不是静默忽略。

**不允许放凭据**：`app_secret` / `root_token` / `password` 这类键会被**拒绝**。
原因不是矫情：选项会被拼进远端名，而 rclone 在日志和错误信息里会打印远端名，
所以作业文件里的密码一定会进日志。凭据请放在 `rclone.conf` 或对应的环境变量里。

**和 `rclone.conf` 的关系**：带选项的形式是 `<名字>,key="value":<root>`，
它**仍会读取同名配置节的其他字段**。已实测：只覆盖 `root_token` 时认证仍用配置节里的
凭据，报错是「文件夹找不到」而不是「凭据无效」。所以不必把凭据搬进作业文件。

## 5. `sync` 小节（删除策略）

可写在 `[job.sync]`（默认）或 `[pair.sync]`（覆盖）。

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `deletion` | string | `"permanent"` | `permanent` 或 `versioning` |
| `versioning_dir` | string | `"_weftsync_trash"` | 仅 `versioning` 生效；**必须是目标内的相对目录** |
| `versioning_naming` | string | `"timestamp-folder"` | `replace` / `timestamp-file` / `timestamp-folder` |
| `max_delete` | int | `50` | 一次运行允许删除的条目数上限；**超出则整轮中止，一个都不删** |
| `conflict` | string | `"report"` | 远端与快照不一致时的策略，见 §5.2 |
| `max_delete_size` | string | — | **尚未实现**，见 `docs/roadmap.md` F3 |

`max_delete = 0` 表示「一个都不许删」，是合法配置。负数会被拒绝 —— 它看起来像
「关闭保护」，不允许。

### 5.1 三种 versioning 命名（对标 FreeFileSync）

| `versioning_naming` | 被删除/覆盖的 `docs/a.txt` 落到 |
|---|---|
| `replace` | `_weftsync_trash/docs/a.txt`（下一轮覆盖同名旧版本） |
| `timestamp-file` | `_weftsync_trash/docs/a.20261107-171526.txt` |
| `timestamp-folder` | `_weftsync_trash/20261107-171526/docs/a.txt`（**可手工反向还原**） |

`versioning` 同时作用于**删除**和**覆盖**（这就是 FFS 的 "Delete and overwrite"）。
因为后端实现了服务端 `Move`，退休一个文件只花一次 API 调用，不产生流量。

`_weftsync_trash` 会被自动排除出同步集 —— 否则下一轮 mirror 会把回收站当成
「多余的目录」再删一次。

### 5.2 `conflict`：远端与快照不一致怎么办

飞书很适合作冲突检测，因为它的两个特性把「远端被改」变成了**硬信号**：
同名重传必然产生新 token（无原地更新），而且同一文件夹允许同名共存。所以不需要靠
mtime 猜谁更新（飞书的 mtime 就是上传时刻）。

**三种信号**

| 信号（`kind`） | 含义 |
|---|---|
| `remote-deleted` | 快照记录的 token 在该目录里**完全不存在** |
| `remote-replaced` | 名字还在，但已不是我们记录的那个 token |
| `duplicate-name` | 同一名字有 ≥ 2 个条目 |

**三种策略**

| `conflict` | 行为 | 退出码 |
|---|---|---|
| `report`（**默认**） | 打印冲突表，**该路径整轮不碰** | 非 0 |
| `overwrite` | 照常同步，用本地版本覆盖同名 | 0 |
| `keep-both` | 把本地那份改名上传（`<名> (from <主机> <时间>)<扩展名>`），远端原名不动 | 0 |

`report` 的实现方式是**把冲突路径从同步集里过滤掉**，而不是「跑完再回滚」——
所以冲突路径不可能被半处理。

> **`overwrite` 不会变得更干净**：如果记录的那个 token 已经不在了（有人替换过），
> 我们只能上传自己的新版本，**不能删对方那一份**，因为无法证明那是我们的文件。
> 结果就是同一名字下多一份。清重复请用 `backend dedupe`（见
> `docs/backend-commands.md`）。

**没有 state_file 时**：无法检测冲突（没有基线），后端命令会直接报错而不是说
「没有冲突」。

### 5.3 `max_nodes_per_dir`：单目录条目上限（三态）

有些后端限制一个目录能放多少直接子项（飞书：1500，见 `docs/daemon.md` §10）。
预检会在同步前拦下超限目录。这个设置决定拦在哪：

| 写法 | 含义 |
|---|---|
| 不写（**推荐**） | 用后端声明的上限；后端没有上限则**不做检查** |
| `0` | 完全不检查 |
| `n > 0` | 超过 `n` 就拦下 |

```toml
[pair.sync]
max_nodes_per_dir = 1400
```

**为什么是三个状态而不是两个**：「不写」和「0」必须能区分。只有两个状态时，
没有 `0` 就没有办法表达「这个后端没有上限」，结果是飞书的数字被套到每个 pair 上 ——
一个 `local -> local`、1401 个文件的同步被拒绝，报的还是「超过飞书单层上限」。
这是 2026-11-09 修掉的真实缺陷（F12）。

负数是**错误**，不是「不限」的同义词：安全设置里的笔误不该被解读成放宽。

### 5.4 `compare`：比较动作（三档）

对齐 FreeFileSync 的比较策略。决定「两边算不算同一个文件」。

| 值 | 判据 | 成本 |
|---|---|---|
| `time-size`（默认） | 与快照记录的修改时间 + 大小比 | 只读本地与状态文件，秒级 |
| `size-only` | 只看大小，忽略修改时间 | 同上。**内容变了但长度不变时漏传** |
| `content` | 把云端那份下载回来逐字节比对 | 每个要传的文件下载一遍 |

```toml
[pair.sync]
compare = "content"
```

`content` 在飞书上是唯一能真比内容的方式 —— 飞书返回 `hash.None`（M0/E3 实测），
没有哈希可比。它的结果会**排除**那些其实相同的路径，所以预览说「一样」时，
执行也真的不会再传（不是只改个标签）。

### 5.5 `filter`：包含 / 排除

```toml
[pair.sync.filter]
include = ["*.xlsx", "reports/**"]
exclude = ["*.tmp", "_*", "_*/**"]
```

- `include` 是**白名单**：一旦写了，只有匹配的路径参与同步。
- `exclude` 从白名单里再挖洞。
- 作业级的 `[job.sync.filter]` 作为所有 pair 的默认值，pair 自己的叠加在后面。
- 设置页的全局「过滤规则」只有 exclude，会再叠加一层。

**两个容易踩的坑**

1. **只写 `_*` 漏目录**：`_*` 只排除 `_` 开头的**文件**，`_dir/` 里面的东西照样同步。
   要写 `_*` **和** `_*/**` 两条。
2. **include 需要兜底规则才生效**：rclone 的规则集在什么都不匹配时返回「包含」，
   所以单加 include 只是「重新包含」，不构成限制。实现里补了一条 `/**` 排除作为
   兜底（与 rclone 自己的 CLI 一致，见 `fs/filter/rules.go` 的 addImplicitExclude）。
   这一点是实测发现的：只加 include 时三个文件全被传了。

## 6. 变体（variant）

| 值 | 语义 | 会删除吗 |
|---|---|---|
| `update` | 只增不减（默认） | ❌ |
| `mirror` | 目标是源的精确副本 | ✅ 受 `max_delete` 管控 |
| `two-way` | 双向：本地改动上传；**云端改动且本地未变时**下载覆盖本地 | 必须配 `state_file`。删除与新文件默认**只报告**，需显式开关（见 6.1） |
| `custom` | 按矩阵定制 | **未实现**，明确报错 |

未知值（例如拼错成 `mirorr`）**直接报错**，不会退化成某个默认值 —— 退化成 `update`
会悄悄停止同步删除，退化成 `mirror` 会突然开始删数据。

### 6.1 `two-way` 的三个传播开关（默认全关）

```toml
[pair.sync]
propagate_local_delete  = false   # 本地没了 -> 删云端那份
propagate_remote_delete = false   # 云端没了 -> 删本地那份（不可恢复）
pull_new_files          = false   # 云端多出来的 -> 下载下来
max_pull_size           = "100M"  # 单个新文件的下载上限，留空用默认 100M
```

**为什么是三个开关而不是一个「像 FreeFileSync 那样」，因为它们的失败方式完全不同：**

| 开关 | 做了什么 | 出错时最坏情况 | 能撤销吗 |
|---|---|---|---|
| `propagate_local_delete` | 删云端文件 | 你只是把文件挪走（或外接盘没挂载）→ 云端也没了 | `deletion = "versioning"` 时可以 |
| `propagate_remote_delete` | **删本地文件** | 别人在飞书里误删一个目录 → 本地跟着没 | **不能** |
| `pull_new_files` | 把云端内容写进本地磁盘 | 别人扔了个 4GB 视频进来 | 能（再删掉） |

所以：一次只开一个，看清楚它会做什么再开下一个。

两个内置保护：

- `propagate_remote_delete` **只在本地文件自上次同步后没变过时才删**。本地改过就保留 ——
  那时本地那份是唯一的，删掉就等于扔掉用户的工作。
- `pull_new_files` 在**快照为空（首次同步）时什么都不做**。否则「首次同步」
  会把云端已有的文件全当成「别人新放的」下载下来。

这三个开关**只对 `variant = "two-way"` 有效**；写在 `update` / `mirror` 里会直接报错，
不会被静默忽略 —— 以为自己开了删除传播其实没开，是这里最糟的误解。

---

## 7. 删除闸门的行为

`mirror` 分两轮：

1. **预检轮**（dry-run）：照常比对，把「将要删除」的数量统计出来，**不删任何东西**
2. 若 `待删文件数 + 待删空目录数 > max_delete` → **整轮中止**，退出码 1，一个都不删
3. 否则 **执行轮** 真跑，并在结束后再次核对实际删除数；若比预检时多（说明目标在
   两轮之间被改动），报错说明

这和 rclone 自带的 `--max-delete` **不同**：后者是「删到上限再中止」。
实测 `--max-delete 1` 面对 2 个待删文件时，**先删掉 1 个**才退出（见
`docs/decisions.md` D25）。云端删除不可恢复，所以本项目必须自己实现。

---

## 8. 已知限制

| 限制 | 影响 | 绕法 |
|---|---|---|
| `remote.options` 只支持 `state_file` | 其他后端选项只能在 `rclone.conf` 里配 | 给不同 pair 用不同远端名 |
| `union` 不能内联写 | `:union:/a:/b` 报「empty upstream」 | 在 `rclone.conf` 里定义 `[name]` + `upstreams = ...`，然后 `backend = "name"` |
| **Windows 上远程名不要起单个字母** | `u:` 会被 rclone 当成 U 盘，报 `mkdir \\?\U:\: The system cannot find the path specified` | 用两个字母以上，如 `uu`、`feishu2` |

### 8.1 一个 job 里可以混用不同后端
每个 `pair` 的 `remote` 都是一条独立的 rclone 路径，所以下面这个是合法的：

```toml
[job]
name = "mixed"

[[pair]]
name = "to-feishu"
local = { root = "D:/docs" }
remote = { backend = "feishu", root = "token/docs", options = { state_file = "..." } }

[[pair]]
name = "to-disk"
local = { root = "D:/photos" }
remote = { root = ":local:E:/backup/photos" }
```

两个 pair 各自写自己的 state（`to-disk` 没配 `state_file` 就靠 mtime + 大小判断），
互不影响；**一个 pair 失败不会阻止另一个**，但整体退出码非 0。
