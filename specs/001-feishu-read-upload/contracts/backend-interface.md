# Contract：rclone 后端接口

**Feature**: `001-feishu-read-upload` | **Date**: 2026-11-06

> 本后端实现 rclone 的 `fs.Fs` / `fs.Object` 接口。签名以
> `github.com/rclone/rclone@v1.71.2` 为准（已实读源码确认）。

---

## 1. 必需接口：`fs.Fs`

```go
type Fs interface {
    Info
    List(ctx context.Context, dir string) (entries DirEntries, err error)
    NewObject(ctx context.Context, remote string) (Object, error)
    Put(ctx context.Context, in io.Reader, src ObjectInfo, options ...OpenOption) (Object, error)
    Mkdir(ctx context.Context, dir string) error
    Rmdir(ctx context.Context, dir string) error
}
```

### 1.1 各方法契约

| 方法 | 本后端行为 | 失败返回 |
|---|---|---|
| `List(dir)` | 解析 `dir` 为 folder_token → 分页调 `files.list` → 按名分组收敛（R5）→ 返回条目 | 路径不存在 → `fs.ErrorDirNotFound` |
| `NewObject(remote)` | 解析 `remote` 的父目录 → 在列表中找名字匹配（R5 收敛）→ 构造 Object | 不存在 → `fs.ErrorObjectNotFound`；是目录 → `fs.ErrorIsDir` |
| `Put(...)` | 本地 reader → 完整读入内存（≤20MB）→ `upload_all` 上传 | >20MB → 自定义 `ErrFileTooLarge`（含 "20MB" 字样） |
| `Mkdir(dir)` | 逐层解析；缺失的层用 `create_folder` 创建。**幂等**：已存在直接成功 | 权限不足 → `ErrMissingScope` |
| `Rmdir(dir)` | **不实现**，返回 `fs.ErrorCantRmdir` | F1 不删目录（属 F3） |

### 1.2 `Info` 部分

| 方法 | 返回值 | 依据 |
|---|---|---|
| `Name()` | 配置中的 remote 名（如 `feishu`） | — |
| `Root()` | 配置的根逻辑路径 | — |
| `String()` | `"Feishu root 'xxx'"` | — |
| **`Precision()`** | **`time.Second`** | E5 实测：`modified_time` 是整数秒。声明 `ModTimeNotSupported` 会让 rclone 永远判等（E6 用例 D） |
| **`Hashes()`** | **`hash.Set(hash.None)`** | E3 实测：飞书无服务端哈希 |
| `Features()` | 见下表 | — |

### 1.3 `Features` 声明

| 字段 | 值 | 依据 |
|---|---|---|
| `DuplicateFiles` | **`true`** | E2 实测：同文件夹可存在两条同名记录 |
| `CanHaveEmptyDirectories` | **`true`** | 飞书支持空目录（E4 实测建目录成功） |
| `ReadMimeType` | `false` | 不实现 |
| `WriteMimeType` | `false` | 不实现 |
| `CaseInsensitive` | `false` | 飞书区分大小写 |
| `SlowModTime` | `false` | mtime 随列表一起返回，无额外请求 |
| `PartialUploads` | `false` | 飞书上传是原子的，失败不留半成品 |
| `Purge` / `Copy` / `Move` / `DirMove` | 不设置（nil） | F1 不实现；`Move` 属 F3 |
| `About` | 不设置 | 不查配额 |

---

## 2. 必需接口：`fs.Object`

```go
type Object interface {
    ObjectInfo
    SetModTime(ctx context.Context, t time.Time) error
    Open(ctx context.Context, options ...OpenOption) (io.ReadCloser, error)
    Update(ctx context.Context, in io.Reader, src ObjectInfo, options ...OpenOption) error
    Remove(ctx context.Context) error
}
```

### 2.1 各方法契约

| 方法 | 本后端行为 | 依据/说明 |
|---|---|---|
| `Size()` | **`-1`** | 飞书列表不提供 size（E3）。research R6 说明了为何不现查 |
| `ModTime()` | 返回条目的 `modified_time` | 秒级；**语义是上传时刻** |
| `Hash(ty)` | 返回 `""` | 无哈希 |
| `Open(...)` | 调 `files/{token}/download`，支持 `Range` / `Seek` 选项 | 飞书支持 206 Partial Content（E3+ 实测） |
| **`Update(...)`** | **先上传新内容，再删除旧 token** | E1 实测无原地更新。rclone 因 `Features.Move == nil` 强制 inplace，目标存在时必然调 `Update`（research R8 有源码依据）。删除前用 `tokenStillPresent` 二次确认 |
| `Remove()` | 调 `DELETE /files/{token}` | B11 实测：不可逆，无回收站 |
| `SetModTime()` | **返回 `fs.ErrorCantSetModTime`** | 飞书不允许改 mtime |
| `Storable()` | `true` | — |

> ⚠️ **rclone 没有 `fs.ErrorCantUpdate`**。实现时若需要一个「无法更新」的错误，
> 用 `fs.ErrorNotImplemented`。但本后端应当真正实现 `Update`，否则更新已存在的文件
> 会直接失败（没有自动降级）。

### 2.2 `Open` 的 Range 支持

飞书下载端点支持 `Range` 头（E3+ 实测：`bytes=0-0` 返回 `206` +
`Content-Range: bytes 0-0/5000`）。因此：

- `fs.RangeOption` MUST 被转换为 HTTP `Range` 头
- `fs.SeekOption` MUST 被转换为前缀 Range
- 不支持 Range 时（服务端返回 200）MUST 在本地丢弃前缀字节

> rclone 的 `--multi-thread-cutoff` 依赖 Range，本后端声明支持。

---

## 3. 错误返回值对照

| 场景 | 返回的 rclone 错误 |
|---|---|
| 目录不存在 | `fs.ErrorDirNotFound` |
| 文件不存在 | `fs.ErrorObjectNotFound` |
| 目标是目录 | `fs.ErrorIsDir` |
| 目标是文件 | `fs.ErrorIsFile` |
| 无权限 | `fs.ErrorPermissionDenied` |
| 非法参数（含 >20MB） | `fs.ErrorInvalidArgument`（包装自定义错误） |
| 不支持原地更新 | `fs.ErrorCantUpdate` |
| 不支持改 mtime | `fs.ErrorCantSetModTime` |
| 不支持删目录 | `fs.ErrorCantRmdir` |
| 可重试（限流） | 包装 `fserrors.RetryError` |

---

## 4. 不实现的接口（明确）

| 接口 | 理由 | 计划 |
|---|---|---|
| `Mover` | `move` 接口实测可用，但 F1 不需要 | F3 |
| `DirMover` | 同上 | F3 |
| `Purger` | 批量删除需逐条调用，无更快方式 | F3 |
| `Copier` | 飞书有 copy 接口但 F1 不用 | 未计划 |
| `ListRer` | 飞书无递归列表接口，逐层遍历 | 不做 |
| `Abouter` | 需额外接口查配额，F1 不需要 | 未计划 |
| `ChunkWriter` / `OpenChunkWriter` | 分片上传 | **F2** |
| `SetModTimer` | 飞书不允许 | 永不 |

---

## 5. 配置项（`fs.Option`）

| 名称 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `app_id` | string | （必填） | 飞书应用 ID；为空时回落到 `WEFTSYNC_FEISHU_APP_ID` |
| `app_secret` | string | （必填，`IsPassword: true`） | 同上，回落到环境变量 |
| `root_token` | string | （必填） | 飞书文件夹 token（接受完整 URL 时自动提取） |
| `state_file` | string | 自动生成 | state 文件路径；为空时按 R1 规则生成 |
| `base_url` | string | `https://open.feishu.cn` | 便于私有化部署与测试注入 |

**凭据读取优先级**：配置项 > 环境变量。二者皆无 → `NewFs` 返回错误并指明缺哪项。

> F1 只支持这两处来源。「系统凭据库 / 加密文件」属 F5/F6（A18 决策）。

---

## 6. 契约测试清单

| # | 断言 | 层 |
|---|---|---|
| C1 | `List("")` 对空目录返回空切片 + `nil` 错误 | httptest |
| C2 | `List` 分页：构造 2 页共 250 条，返回 250 条 | httptest |
| C3 | `List` 同名收敛：返回 2 条同名，只对外给 1 条（按 R5 规则选） | 单元 |
| C4 | `NewObject` 路径不存在 → `fs.ErrorObjectNotFound` | httptest |
| C5 | `NewObject` 指向目录 → `fs.ErrorIsDir` | httptest |
| C6 | `Put` 21MB → 错误信息含 "20MB" | 单元（用大 reader） |
| C7 | `Put` 1MB → 成功，返回 Object 的 `Size()` = -1 | httptest |
| C8 | `Open` 带 `RangeOption{0,99}` → 只读 100 字节 | httptest |
| C9 | `Update` → `fs.ErrorCantUpdate` | 单元 |
| C10 | `SetModTime` → `fs.ErrorCantSetModTime` | 单元 |
| C11 | `Rmdir` → `fs.ErrorCantRmdir` | 单元 |
| C12 | `Mkdir` 幂等：连续两次不报错 | httptest |
| C13 | `Precision()` == `time.Second` | 单元 |
| C14 | `Hashes()` == `hash.Set(hash.None)` | 单元 |
| C15 | `Features().DuplicateFiles` == true | 单元 |
