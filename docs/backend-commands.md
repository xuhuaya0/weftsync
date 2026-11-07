# 后端命令

列目录时，本后端会**把同名条目折叠成一条**（避免 rclone 看到两个同名对象而误删）。
副作用是：云端真实存在的同名副本对普通命令完全不可见。下面这些命令直接查原始列表，
所以能看见它们。

```powershell
weftsync backend help feishu            # 列出可用命令
```

---

## 1. `duplicates` —— 只列出，不改动

```powershell
weftsync backend duplicates 'feishu:team/docs' -o ...
```

输出 JSON：

```json
{
  "action": "list",
  "dry_run": true,
  "groups": [
    {
      "dir": "",
      "name": "dup.txt",
      "entries": [
        {"name":"dup.txt","token":"COnMb9wX...","created_at":1794044663,"keep":false},
        {"name":"dup.txt","token":"FrTUbHXf...","created_at":1794044667,"keep":false}
      ]
    }
  ],
  "summary": "found 1 name(s) with duplicates; nothing was changed"
}
```

`dir` 是相对远端根目录的路径。多个 pair 指向同一远端时**一定要传路径参数**，
否则会把整棵树都扫一遍：

```powershell
weftsync backend duplicates 'feishu:team/docs'
```

---

## 2. `dedupe` —— 删除多余副本

```powershell
# 先看会删什么（默认就是只报告）
weftsync backend dedupe 'feishu:team/docs' --feishu-state-file <state>

# 确认后真删
weftsync backend dedupe 'feishu:team/docs' --feishu-state-file <state> -o confirm=true
```

### 保留哪一份

**只保留 state 快照里记录的那个 token。** 其他同名的全部删除。

### 三条硬性拒绝条件

| 条件 | 行为 | 为什么 |
|---|---|---|
| 没有 `state_file` | 拒绝删除，只报告 | 没有快照就无法区分「我传的」和「别人传的」，而飞书删除不可恢复 |
| `state_file` 本轮刚重建（repair 模式） | 拒绝删除 | 重建出来的快照此刻还不能作为证据 |
| 快照记录的 token 不在该目录里 | **该名字整体跳过**，只报告 | 说明这个文件已被外部替换/删除；此时删「其他的」可能删掉刚上传的新版本 |

**不做启发式判断**。诸如「保留最新的一份」看似合理，但在多台机器同步同一云盘的场景下，
它会删掉另一台机器刚上传的新版本，且无法撤销。

### 什么时候会有重复副本

正常同步**不会**产生重复：替换文件时 `Update` 是先传新版本再删旧 token。
重复副本来自**降级模式**——state 丢失或损坏时，按契约必须「不跳过、不删除」，
于是全量重传（`docs/decisions.md` D19）。

---

## 3. `resolve` —— 处理一条冲突（只改快照）

```powershell
# 以本地为准：删掉该路径的记录，下次运行上传本地文件覆盖远端
weftsync backend resolve 'feishu:team/docs' -o action=forget -o path=a.txt

# 以云端为准：把远端 token + 本地指纹记为基线（必须先自己下载过）
weftsync backend resolve 'feishu:team/docs' -o action=adopt `
    -o path=a.txt -o token=XXX -o mtime=1794064076 -o size=12
```

这条命令**不会上传、移动或删除任何云端文件**。错误的点击最多让下一轮重做同样的事。

| 参数 | 说明 |
|---|---|
| `-o action=` | `forget` 或 `adopt`，必填 |
| `-o path=` | 相对根目录的文件路径，必填 |
| `-o token=` / `-o mtime=` / `-o size=` | `adopt` 必填；`mtime` 是 Unix 秒 |

**路径为什么用 `-o path=` 而不是位置参数**：rclone 会把位置参数当作目录（Fs）解析，
所以 `backend resolve 'feishu:dir/a.txt'` 会被拒为 `is a file not a directory`（实测）。
参数形式在 CLI 和 daemon 内部两条路上都成立。

**`adopt` 会先校验 token 当前确实在文件夹里**。不校验的话，快照会指向一个不存在的
东西，而冲突下一轮又回来 —— 比不改更糟。

拒绝的情形（都会明确报错，不会静默不做事）：
没有 `state_file`、快照本轮刚重建（`repair`）、`action` 未知、`token` 不在文件夹里。

---

## 4. 为什么这些命令在后端而不是 `weftsync run`

`weftsync run` 走的是 rclone 的同步引擎，而引擎看到的目录已经折叠过同名条目 ——
它**看不见**副本，自然也删不掉。只有后端能同时拿到原始列表和快照 token，
所以命令放在这里（`docs/decisions.md` D29）。

---

## 5. 一次性用 `dedupe` 清理别人留下的同名文件？

不行。`dedupe` 的判据是「这个 token 是不是我记录过的」，
所以它只能清掉**自己造成的**重复。清理他人文件请直接在飞书客户端里操作。
