# Contract：错误码映射

**Feature**: `001-feishu-read-upload` | **Date**: 2026-11-06

> 飞书错误码来源：官方文档 + M0 实测（`PLAN.md` §3、`docs/feishu-probe.md`）。

---

## 1. 映射表

| 飞书 code | 飞书 msg 含义 | 内部错误 | 重试？ | rclone 错误 | 对用户说什么 |
|---|---|---|---|---|---|
| `99991672` | 缺 scope | `ErrMissingScope` | ❌ | `fs.ErrorPermissionDenied` | 「应用缺少权限 `<scope>`。请访问 `<链接>` 申请并发布新版本」 |
| `99991663` | token 无效/过期 | `ErrTokenInvalid` | ✅ 一次（刷新后） | — | （内部处理，用户无感） |
| `1061004` | forbidden | `ErrForbidden` | ❌ | `fs.ErrorPermissionDenied` | 「无权访问 `<路径>`。请确认该文件夹已共享给应用所在的群，且权限为"可编辑"」 |
| `1061044` | parent 不存在 | `ErrParentNotFound` | ❌ | `fs.ErrorDirNotFound` | 「云端目录不存在：`<路径>`」 |
| `1061045` | 可重试 | `ErrRetryable` | ✅ 退避 | `fserrors.RetryError` | （内部重试；超限后）「飞书接口限流，已重试 N 次仍失败，请稍后再试」 |
| `1061002` | 参数错误 | `ErrInvalidArgument` | ❌ | `fs.ErrorInvalidArgument` | 「请求参数不被接受：`<详情>`」（**含 >20MB 情形**） |
| `1062009` | size 不一致 | `ErrSizeMismatch` | ✅ 一次 | `fserrors.RetryError` | 「上传后大小校验不一致，已重试」 |
| `1062506` | 超目录深度 | `ErrDepthExceeded` | ❌ | `fs.ErrorInvalidArgument` | 「目录层级超过飞书上限（15 层）：`<路径>`」 |
| `1062507` | 超单层节点 | `ErrTooManyNodes` | ❌ | `fs.ErrorInvalidArgument` | 「目录 `<路径>` 下条目数超过飞书上限（1500），请拆分目录」 |
| 其他 | — | `ErrUnknown` | ❌（保守） | 原样包装 | 「飞书返回未预期的错误 code=`<n>`：`<msg>`」 |

**HTTP 层错误**

| HTTP 状态 | 内部错误 | 重试？ |
|---|---|---|
| 401 | `ErrTokenInvalid` | ✅ 一次（刷新后） |
| 403 | `ErrForbidden` | ❌ |
| 404 | 按上下文：`ErrObjectNotFound` / `ErrParentNotFound` | ❌ |
| 429 | `ErrRetryable` | ✅ 退避 |
| 5xx | `ErrRetryable` | ✅ 退避 |
| 网络错误（超时、连接重置） | `ErrRetryable` | ✅ 退避 |

---

## 2. 重试策略

| 参数 | 值 | 说明 |
|---|---|---|
| 最大重试次数 | 3 | 可配置 |
| 退避算法 | 指数：1s, 2s, 4s | 加 ±20% 抖动 |
| 上限 | 单次等待 ≤ 10s | — |
| 可重试 | `ErrRetryable` / `ErrSizeMismatch` / `ErrTokenInvalid`（仅一次） | — |
| 不可重试 | 权限、参数、路径、容量、未知 | 重试只会浪费配额 |

**token 刷新特例**：`ErrTokenInvalid` 触发 1 次刷新 + 1 次重试，
不会进入指数退避循环（避免反复刷新 token 导致并存的 token 过多）。

---

## 3. 错误信息构造规则

每条错误信息必须包含三要素：

```
<发生了什么>。 <为什么>。 <怎么修>。
```

**示例**

```
✗ 缺 scope：
  "应用缺少权限 space:folder:create。
   该权限用于创建云端目录。
   请访问 https://open.feishu.cn/app/cli_xxx/auth?q=space:folder:create
   申请权限，并在「版本管理与发布」中发布新版本。"

✗ 文件太大：
  "文件 xxx.mp4 大小为 25.0 MB，超过飞书单文件直传上限 20 MB。
   分片上传将在后续版本支持（F2）。"

✗ 目录超限：
  "目录 D:/work/reports/ 下有 1632 个直接子项，超过飞书单层上限 1500。
   请拆分为多个子目录，或在过滤规则中排除部分文件。"

✗ 未共享文件夹：
  "无权访问文件夹 KARRfvoYtlX9T3dOoaTcmdELnyv。
   应用身份访问个人云盘需要三步授权：① 应用开启机器人能力
   ② 建群并把应用拉入 ③ 在飞书里把目标文件夹共享给该群（需可编辑权限）。"

✗ 未知错误：
  "飞书返回未预期的错误 code=99999999: internal error。
   如持续出现，请携带上方 log_id 联系飞书支持。"
```

---

## 4. 日志要求

| 事件 | 级别 | 内容 |
|---|---|---|
| 重试发生 | `WARN` | 第 N 次重试，原因，下次等待时间 |
| 降级模式进入 | `WARN` | 原因（CRC/缺失/版本）+ "本次不执行删除" |
| 同名多条 | `DEBUG` | 路径、若干 token、选中规则 |
| 跳过未变化文件 | `DEBUG` | 路径（数量在汇总行输出） |
| 上传/下载开始与结束 | `DEBUG` | 路径、大小、耗时 |
| 删除操作 | `INFO` | 路径、token（这是破坏性操作，必须留痕） |
| 权限/容量错误 | `ERROR` | 完整三要素信息 |

**禁止**：日志中输出 `app_secret`、`tenant_access_token`（宪法原则 V）。

---

## 5. 测试清单

| # | 断言 |
|---|---|
| E1 | `99991672` → `ErrMissingScope`，错误信息含 scope 名与 URL |
| E2 | `1061004` → `ErrForbidden`，信息含"共享给群"提示 |
| E3 | `1062507` → `ErrTooManyNodes`，信息含"1500" |
| E4 | `1061002` + 21MB 上传 → 信息含"20MB" |
| E5 | `1061045` → 被标记可重试，重试 3 次后报错 |
| E6 | 5xx → 可重试，退避间隔递增 |
| E7 | 401 → 刷新 token 后重试成功（用 httptest 模拟第一次 401） |
| E8 | 未知 code → 错误信息含原始 code 与 msg |
| E9 | 所有错误信息不含 `app_secret` / token 字样 |
