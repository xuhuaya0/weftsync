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

## 3. 为什么这些命令在后端而不是 `weftsync run`

`weftsync run` 走的是 rclone 的同步引擎，而引擎看到的目录已经折叠过同名条目 ——
它**看不见**副本，自然也删不掉。只有后端能同时拿到原始列表和快照 token，
所以命令放在这里（`docs/decisions.md` D29）。

---

## 4. 一次性用 `dedupe` 清理别人留下的同名文件？

不行。`dedupe` 的判据是「这个 token 是不是我记录过的」，
所以它只能清掉**自己造成的**重复。清理他人文件请直接在飞书客户端里操作。
