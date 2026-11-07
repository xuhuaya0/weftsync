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

## 6. 变体（variant）

| 值 | 语义 | 会删除吗 |
|---|---|---|
| `update` | 只增不减（默认） | ❌ |
| `mirror` | 目标是源的精确副本 | ✅ 受 `max_delete` 管控 |
| `two-way` | 双向 | **未实现**，明确报错 |
| `custom` | 按矩阵定制 | **未实现**，明确报错 |

未知值（例如拼错成 `mirorr`）**直接报错**，不会退化成某个默认值 —— 退化成 `update`
会悄悄停止同步删除，退化成 `mirror` 会突然开始删数据。

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
| 挂载/定时未实现 | 需手动 `weftsync run` | F5 |
| `max_delete_size` 未实现 | 只能按数量限，不能按体积限 | 见 roadmap |
| `schedule` 小节不生效 | 写了也不会自动跑 | F5 |
