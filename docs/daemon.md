# 常驻进程（daemon）

```powershell
weftsync daemon jobs/nightly.toml                # 按调度跑
weftsync daemon jobs/*.toml --run-now            # 先立刻跑一遍
weftsync daemon jobs/a.toml --addr 127.0.0.1:5600 --log-file daemon.log
```

---

## 1. 调度

写在第个作业文件的 `[job.schedule]` 里：

```toml
[job.schedule]
mode = "interval"        # interval | daily_time | cron（不写 = 只能手动触发）
interval = "1h"
start_at = "08:00"
align_to_start = true
```

| `mode` | 必填 | 行为 |
|---|---|---|
| （不写） | — | **只响应手动触发**，永不自作主张 |
| `interval` | `interval` | 每隔这么久跑一次（最小 `1m`） |
| `daily_time` | `daily_time` | 每天本地时间 `HH:MM` 跑一次 |
| `cron` | `cron` | 标准五字段表达式 |

**`align_to_start`**：只在 `interval` 下有意义，且必须配 `start_at`。

| `align_to_start` | `interval = "2h"`、`start_at = "08:00"` 时的实际时刻 |
|---|---|
| `true` | 08:00、10:00、12:00…（**锚定时刻**，重启也不漂移） |
| `false` | 从进程启动那一刻起每 2 小时（重启就会漂） |

**为什么不接受 `@daily` 这类描述符**：读回配置时它是有歧义的，本项目要求配置一眼能看懂。

---

## 2. 本地 HTTP API

只绑 `127.0.0.1`。传 `0.0.0.0`、局域网地址或 `:5572`（空主机）会**直接拒绝**：
这个 API 能启动同步且没有认证，放到网络上等于把云盘交出去。

| 方法 | 路径 | 作用 |
|---|---|---|
| GET | `/api/status` | 每个作业的状态、调度、下次/上次运行、上次结果 |
| POST | `/api/run?job=名称` | 立刻启动一个作业 |
| POST | `/api/run` | 启动所有启用的作业 |
| GET | `/api/health` | 存活检查 |

`/api/status` 示例：

```json
{
  "jobs": [{
    "name": "nightly",
    "state": "idle",
    "enabled": true,
    "pairs": 2,
    "schedule": "every 2h0m0s from 08:00",
    "next_run": "2026-11-07T18:00:00+08:00",
    "last_run": "2026-11-07T16:00:03+08:00",
    "last_ok": true,
    "skipped": 0,
    "last_result": [{"Pair":"work","Actual":{"Transferred":3,"Deleted":0}}]
  }],
  "now": "2026-11-07T17:12:00+08:00"
}
```

`state`：`idle` / `running` / `disabled`。
`skipped`：因为上一轮还没结束而被跳过的次数。

---

## 3. 单实例

**监听端口本身就是单实例锁。** 第二个 daemon 绑同一地址会立刻失败：

```
daemon: 127.0.0.1:5572 is already in use, which normally means another
weftsync daemon is running. Stop it first, or pass a different --addr
```

比 pid 文件可靠：进程崩了端口自动释放，不会留下需要人手工清理的残留锁。
想跑两个 daemon（不同作业、不同端口）是可以的，显式传 `--addr` 即可。

---

## 4. 防重叠

同一个作业上一轮还没结束，不会再起一轮：

- 定时触发：**跳过并计数**（`skipped`），日志里写一行原因
- API 触发：返回 **HTTP 409**，说明「已经在跑」

不同作业之间互不影响，可以并行。

---

## 5. 停止

`Ctrl+C`（或 `SIGTERM`）→ 停止调度 → **等进行中的那轮跑完** → 退出。

**不会**在中途强杀上传：飞书没有 abort 接口，分片上传断开后只能等它 24 小时自动过期。
让它跑完比留下一个半成品更好。

---

## 6. 与删除保护的关系

daemon **只决定什么时候跑**，不改变任何一个 pair 的安全规则：
`max_delete` 闸门、`deletion` 档位、`conflict` 策略全部照旧生效。
定时任务和人手动跑走的是同一条 `job.Run` 路径 —— 不存在「自动跑就宽松一点」的分支。

---

## 7. 尚未实现

| 缺口 | 现状 |
|---|---|
| 系统托盘图标 | 未做。纯 Go 无 cgo 的托盘要用 `Shell_NotifyIcon` 自己写消息循环，尚未实现 |
| 开机自启 | 未做。现在需要把 `weftsync daemon ...` 放进启动项或计划任务 |
| 退出时确认「有同步在进行」 | 现在是**默默等它跑完**，没有弹窗确认 |
| 分片 `upload_id` 落盘 | 未做（F2 遗留） |
| 网页界面 | F6 |
