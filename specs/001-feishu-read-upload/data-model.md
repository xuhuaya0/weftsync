# Data Model：飞书后端（F1）

**Feature**: `001-feishu-read-upload` | **Date**: 2026-11-06

---

## 1. 飞书侧实体（只读，由 API 返回）

### 1.1 RemoteEntry（列表条目）

飞书 `GET /open-apis/drive/v1/files` 返回的单个条目，映射为内部结构。

| 字段 | 类型 | 来源 | 说明 |
|---|---|---|---|
| `Name` | string | `name` | 文件名或目录名（UTF-8） |
| `Token` | string | `token` | `file_token` 或 `folder_token`，飞书的唯一寻址方式 |
| `IsDir` | bool | `type == "folder"` | 其余 type（`file`/`docx`/`sheet`/`bitable`/…）均视为文件 |
| `ModTime` | int64 | `modified_time` | Unix 秒。**语义是"上传时刻"**，非内容修改时间（E5 实测） |
| `CreatedTime` | int64 | `created_time` | 同名字存在时用于选"最新"（见 R5） |
| `ParentToken` | string | `parent_token` | 所属目录的 folder_token |
| `RawType` | string | `type` | 保留原始值，便于日志与 `lsjson` 输出 |

**不存在的字段（重要）**

| 字段 | 状态 | 原因 |
|---|---|---|
| `Size` | **缺失** | 飞书列表不返回（E3 实测） |
| `Hash` | **缺失** | 飞书服务端无内容哈希（E3 实测） |

> 这是本后端与其它 rclone 后端的本质差异。所有依赖 size/hash 的逻辑必须走
> 我们自己的 state 机制，不能依赖 rclone 默认比较。

### 1.2 FolderMeta（目录元数据）

`POST /open-apis/drive/v1/metas/batch_query` 返回，用于：
- 验证 root token 是否有效（`NewFs` 阶段）
- 在错误信息里显示目录名

| 字段 | 类型 | 来源 |
|---|---|---|
| `Token` | string | `doc_token` |
| `Title` | string | `title` |
| `OwnerID` | string | `owner_id` |
| `URL` | string | `url` |

---

## 2. 本地状态实体（state file）

### 2.1 StateFile（文件级）

```go
type StateFile struct {
    Version int                       // 格式版本，当前 1
    PairID  string                    // 由本地根 + 云端根派生
    Entries map[string]StateEntry     // key = 相对路径（POSIX 分隔符）
}
```

**序列化后的磁盘格式**

```
# weftsync state v1  crc32=1a2b3c4d
{"version":1,"pair_id":"a1b2c3d4e5f6a7b8","entries":{...}}
```

- 第 1 行：人类可读的元信息 + CRC32（十六进制）
- 第 2 行起：JSON 正文
- CRC32 覆盖**第 2 行起的全部字节**

### 2.2 StateEntry（条目级）

| 字段 | JSON key | 类型 | 说明 |
|---|---|---|---|
| `Path` | `path` | string | 相对路径，`/` 分隔（即使 Windows 也存 `/`，保证跨平台一致） |
| `LocalMTime` | `local_mtime` | int64 | 上次成功同步时本地文件的 mtime（Unix 秒） |
| `LocalSize` | `local_size` | int64 | 上次成功同步时本地文件的字节数 |
| `RemoteToken` | `remote_token` | string | 云端对应文件的 `file_token`，**用于精确删除旧版本** |
| `SyncedAt` | `synced_at` | int64 | 上次成功同步的墙钟时间（仅诊断用） |

**为什么这三个字段缺一不可**

| 字段 | 若是缺失会怎样 |
|---|---|
| `LocalMTime` | 无法判断本地是否改动 → 每次都传或都不传 |
| `LocalSize` | mtime 被人工保留时检测不到内容变化 |
| `RemoteToken` | 覆盖时无法精确指定删哪一份 → 可能删错同名文件 |

### 2.3 路径规范化规则

| 场景 | 规则 |
|---|---|
| 分隔符 | 一律用 `/`（Windows 的 `\` 在写入前转换） |
| 大小写 | **保留原始大小写**，不做归一化（Windows 不区分但飞书区分） |
| 首尾分隔符 | 去除前导 `/`，无尾随 `/` |
| 空路径 | 表示根目录，不写入 entries |
| Unicode | 不做 NFC/NFD 归一化（与本地文件系统保持字节一致） |

---

## 3. 增量判断状态机

对每个本地文件，根据 state 与本地当前状态决定动作：

```
                     ┌─────────────────┐
                     │ 读取本地文件信息 │
                     │ (path,mtime,size)│
                     └────────┬────────┘
                              │
                    state 中有该 path ?
                     ┌────────┴────────┐
                   无│                 │有
                     ▼                 ▼
              ┌──────────┐    ┌──────────────────┐
              │  UPLOAD  │    │ mtime 和 size 都相同?│
              │  (新增)   │    └────────┬─────────┘
              └──────────┘      是│      │否
                                   ▼      ▼
                          ┌────────┐  ┌──────────────┐
                          │  SKIP  │  │ remote_token │
                          │(0 请求) │  │ 在当前云端存在? │
                          └────────┘  └──────┬───────┘
                                        是│    │否
                                          ▼    ▼
                              ┌──────────────┐ ┌──────────┐
                              │  REPLACE     │ │  UPLOAD  │
                              │ 删旧 token   │ │ (直接传)  │
                              │ 再上传新的   │ │          │
                              └──────────────┘ └──────────┘
```

**REPLACE 的子步骤（顺序不可颠倒）**

```
1. 上传新文件       → 得到 new_token
2. 删除 old_token   （此时才删，若上传失败则旧文件仍在）
3. 更新 state       （mtime/size/new_token）
```

> **为什么先传后删**：若先删后传且上传失败，云端文件就消失了（API 无回收站，
> B11 实测不可逆）。先传后删的最坏情况是"云端暂时有两份"，可再跑一次修复。

**降级模式（state 不可用时）**

```
state 缺失 / CRC 失败且无 .bak
        ↓
扫描云端现状，重建 state
        ↓
本次运行：只允许 UPLOAD 和 SKIP
          禁止 REPLACE（即禁止删除）
        ↓
输出明确告警："state 已重建，本次运行不执行删除"
```

---

## 4. 实体关系

```
Fs (一个后端实例)
 │
 ├── rootToken : string            ← 从配置/环境解析
 ├── rootPath  : string            ← 逻辑路径前缀
 ├── listCache : map[token][]RemoteEntry   ← 单次运行内缓存
 │
 ├── Object (N 个)                 ← 由 List 产生，惰性
 │    ├── remote : string          ← 相对路径
 │    ├── entry  : RemoteEntry     ← token / mtime / type
 │    └── fs     : *Fs             ← 回指
 │
 └── state : *StateFile            ← 与 Fs 同生命周期
      └── entries : map[path]StateEntry
```

---

## 5. 数据规模与边界

| 项 | 上限 | 来源 |
|---|---|---|
| 单目录条目数 | 1500（硬限）/ 2000（我们的报错阈值） | 飞书文档 + research R11 |
| 目录深度 | 15 层 | 飞书文档 |
| 单文件直传 | 20MB | 实测（21MB 报 1061002） |
| state 条目数 | 无硬限；几千条约数百 KB JSON | 估算 |
| token 有效期 | 2 小时 | 飞书文档 |

---

## 6. 不建模的东西（明确排除）

| 项 | 原因 |
|---|---|
| 飞书原生文档（docx/sheet/bitable）的语义 | A14 确认只同步普通文件 |
| Wiki 知识库结构 | 同上 |
| 权限/分享设置 | F1 不涉及 |
| 云端回收站 | API 不提供（B11 实测） |
| 跨 pair 的全局索引 | F1 每 pair 独立 state；多 pair 协调属 F10 |
