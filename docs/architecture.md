# GoSpeed 架构设计

本文档描述 GoSpeed v0.1.0 的模块划分、数据流、测量窗口和扩展点。

## 设计目标

1. 测速算法与界面完全解耦：CLI、未来的 Wails 桌面端和 Web UI 都只是调用
   `internal/speedtest`。
2. 只使用真实测量数据：没有采集到的指标使用 `null` / `N/A` 表示。
3. 结果可解释、可复现：每个速率都记录了测量窗口、原始字节数和原始耗时。
4. 标准库优先：v0.1.0 没有引入任何第三方依赖。
5. 安全默认：测速服务器默认只监听 `127.0.0.1`，不转发、不落盘、不暴露系统信息。

## 模块划分

| 模块 | 职责 | 依赖 |
| --- | --- | --- |
| `cmd/gospeed` | 进程入口，安装信号处理，调用 CLI | `internal/cli` |
| `internal/cli` | 解析命令行、选择节点、渲染结果 | `config`、`nodes`、`server`、`speedtest` |
| `internal/speedtest` | 测速引擎：延迟、下载、上传、单位换算、结果模型 | 标准库、`internal/version` |
| `internal/nodes` | 节点配置加载、校验、查询、可用性检查 | 标准库 |
| `internal/server` | 自建测速服务端（HTTP API） | 标准库、`internal/version` |
| `internal/config` | CLI 与测试共享的默认配置 | 标准库 |
| `internal/version` | 版本号与 User-Agent | 标准库 |

依赖方向是单向的：`cli -> {nodes, speedtest, server, config}`，
`speedtest` 不反向依赖任何表现层模块。这样后续把 `speedtest` 直接接入 Wails
绑定层时不需要改动测量代码。

## 数据流

```text
gospeed test
  │
  ├─ internal/cli        解析参数、加载节点配置
  │     └─ internal/nodes   Node -> speedtest.Target
  │
  ├─ internal/speedtest.Engine.Run(ctx, Options)
  │     ├─ (可选) warmup:  GET /health      连接预热 + 快速失败
  │     ├─ latency 阶段:   GET /ping        HTTP RTT 采样
  │     ├─ download 阶段:  GET /download    真实字节计数
  │     └─ upload 阶段:    POST /upload     服务端确认字节数
  │
  └─ Result (JSON)  ──► CLI 渲染 / 导出
```

## 测量窗口定义

速率必须和测量窗口一起解释，因此 `Result` 中直接记录了窗口名称。

| 指标 | 窗口 | 字段 |
| --- | --- | --- |
| 下载 | 第一个响应字节 → 最后一个响应体字节 | `download.measurement_window = first_response_byte_to_last_body_byte` |
| 上传 | 请求体第一次被传输层读取 → 收到服务端确认 | `upload.measurement_window = request_body_write_start_to_server_confirmation` |

下载窗口排除了 DNS、TCP、TLS 和请求往返时间；上传窗口故意包含确认往返，
让结果偏保守而不会虚高。无论哪种窗口，分子都来自真实传输字节数：

- 下载使用客户端读到的字节数；
- 上传使用服务端确认的字节数，并要求与服务端收到的字节数完全一致，
  不一致时直接报错，不输出速率。

## 单位与公式

```text
Mbps   = bytes × 8 / seconds / 1,000,000
MB/s   = bytes / seconds / 1,000,000
MiB/s  = bytes / seconds / 2^20
```

- `Mbps` 是百万比特每秒，CLI 默认展示单位；
- `MB/s` 是十进制 MB 每秒，作为辅助信息展示；
- `MiB/s` 用于 2 的幂语义，v0.1.0 结果 JSON 不输出该字段；
- 任何除以零时长的计算都返回 `ErrZeroDuration`，不会输出 `+Inf` 或虚假数字。

## 取消、超时与并发

- 所有网络操作都接收 `context.Context`；Ctrl+C 会取消整个测试。
- 每个阶段有独立超时（`Options.Timeout`），且必须大于传输时长
  （`Options.Duration`），否则配置直接被拒绝。
- 下载使用时长窗口作为正常结束条件、阶段超时作为硬上限；
  由时长窗口到期导致的取消会被识别为正常结束（`stop_reason = duration_elapsed`）。
- 上传通过“读者返回 EOF”在时长耗尽后优雅结束请求，服务端可以完成统计并确认。
- v0.1.0 只允许单连接。`Options.Connections` 已进入结果快照，
  v0.2.0 会在同一接口下实现 worker pool，每个连接独立计数后再汇总。
- 共享状态只出现在需要跨 goroutine 读取的计数器上，使用 `sync/atomic`；
  上传进度 goroutine 在读取响应前会被 `WaitGroup` 回收，避免 goroutine 泄漏。

## HTTP 客户端约定

- `DisableCompression = true`，并显式发送 `Accept-Encoding: identity`：
  压缩会让“线上字节数”与“用户想要的载荷吞吐”不一致。
- 请求携带 `Cache-Control: no-store`，避免中间缓存伪造高速率。
- 复用连接：延迟阶段后续采样、下载、上传共享同一条 keep-alive 连接。

## 服务端安全边界

- 固定路由：`GET /health`、`GET /ping`、`GET /download`、`POST /upload`；
  不存在任何 URL 转发、文件读取或目录映射逻辑。
- 默认监听 `127.0.0.1`；监听其他地址时 CLI 会打印显式警告。
- 上传使用 `http.MaxBytesReader` 限制大小并直接丢弃数据，不写入磁盘；
  下载受最大字节数与最大时长双重限制。
- 读写超时可配置，且会校验“写超时必须大于最大下载时长”，
  避免长下载被服务器自身截断而误判为网络故障。
- 所有响应带 `Cache-Control: no-store` 与 `X-Content-Type-Options: nosniff`。

## 结果模型

`Result` 是唯一对外结果结构，字段稳定、可 JSON 序列化：

- 身份：`test_id`、`timestamp`
- 目标：`target.server_id / server_name / server_address / protocol / local`
- 配置快照：`settings.*`（时长、超时、连接数、采样数、是否预热）
- 延迟：`latency`（`type = http_rtt`、min/avg/max、原始样本、jitter）
- 下载：`download`（字节数、耗时、Mbps、MB/s、窗口、结束原因）
- 上传：`upload`（客户端字节数、服务端确认字节数、两侧耗时）
- 状态：`status`（`not_started` / `running` / `completed` / `failed` / `cancelled`）

未采集的指标使用可选类型。例如 `latency.packet_loss_percent` 在 v0.1.0
永远是 `null`，因为项目还没有 UDP/ICMP 丢包测试能力，也绝不用 HTTP
请求失败率冒充丢包率。

## 未来扩展点

| 方向 | 落点 |
| --- | --- |
| 多连接并发测速 | `speedtest` 新增阶段调度器，复用现有单连接实现 |
| ICMP / UDP / TCP Connect 延迟 | 新增 `latency` 采集器，`LatencyType` 增加取值 |
| 实时曲线 | 消费现有 `Progress` 事件流，无需修改测量逻辑 |
| 多节点自动选择 | `internal/nodes` 增加 RTT 探测与排序策略 |
| 历史记录与导出 | 直接消费 `Result` JSON，可在 `internal/history` 中实现 |
| Wails + Vue 3 桌面端 | 通过 binding 调用 `Engine.Run`，复用同一个 `Result` |
