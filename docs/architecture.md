# GoSpeed 架构设计

本文档描述 GoSpeed v0.2.0 的模块划分、并发模型、采样与统计方式、测量窗口和扩展点。

## 设计目标

1. 测速算法与界面完全解耦：CLI、未来的 Wails 桌面端和 Web UI 都只是调用
   `internal/speedtest`。
2. 只使用真实测量数据：没有采集到的指标使用 `null` / `N/A` 表示。
3. 结果可解释、可复现：每个速率都记录了测量窗口、原始字节数、原始耗时，
   多连接结果还记录了每一条连接的独立证据。
4. 不做“补偿”：任何 Mbps 结果都不会乘以经验系数去接近某个商业工具。
5. 标准库优先：v0.2.0 依然没有引入任何第三方依赖。
6. 安全默认：测速服务器默认只监听 `127.0.0.1`，不转发、不落盘、不暴露系统信息。

## 模块划分

| 模块 | 职责 | 依赖 |
| --- | --- | --- |
| `cmd/gospeed` | 进程入口，安装信号处理，调用 CLI | `internal/cli` |
| `internal/cli` | 解析命令行、选择节点、渲染进度与结果 | `config`、`nodes`、`server`、`speedtest` |
| `internal/speedtest` | 测速引擎：延迟、多连接下载 / 上传、采样、统计、结果模型 | 标准库、`internal/version` |
| `internal/nodes` | 节点配置加载、校验、查询、可用性检查 | 标准库 |
| `internal/server` | 自建测速服务端（HTTP API） | 标准库、`internal/version` |
| `internal/config` | CLI 与测试共享的默认配置 | 标准库 |
| `internal/version` | 版本号与 User-Agent | 标准库 |

`internal/speedtest` 内部文件职责：

| 文件 | 职责 |
| --- | --- |
| `engine.go` | 选项校验、阶段编排、统一状态机、进度事件结构 |
| `download.go` | 下载阶段：worker 定义、聚合、结果组装 |
| `upload.go` | 上传阶段：流式载荷、服务端确认、结果组装 |
| `latency.go` | HTTP RTT 采样 |
| `transfer.go` | 共享预算、原子计数器、worker 调度、连接级记录与告警 |
| `sample.go` | 实时采样器与描述性统计 |
| `metrics.go` | 单位换算与格式化 |
| `result.go` | 稳定的 JSON 结果模型 |
| `errors.go` | 可判定的哨兵错误 |

依赖方向是单向的：`cli -> {nodes, speedtest, server, config}`，
`speedtest` 不反向依赖任何表现层模块。后续把 `speedtest` 接入 Wails 绑定层时
不需要改动测量代码。

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
  │     ├─ download 阶段:  GET /download    N 个 worker + 共享窗口
  │     └─ upload 阶段:    POST /upload     N 个 worker + 服务端确认
  │            │
  │            ├─ worker goroutine × N     各连接独立字节计数
  │            └─ sampler goroutine        真实累计字节 -> 实时采样
  │
  └─ Result (JSON)  ──► CLI 渲染 / 导出 / 未来 GUI
```

## 多连接并发设计

### worker 池

- `Options.Connections` 允许 1..`MaxConnections`（16）；默认 1，
  端到端测试覆盖 1 / 4 / 8 / 16。
- 每个 worker 是一个 goroutine，独立创建一次 HTTP 请求、独立占用一条连接、
  独立计数；结果按 worker 索引写回切片，因此不需要锁，也不会串数据。
- `runWorkers` 用 `sync.WaitGroup` 等待全部 worker，任一 worker 阻塞不会导致
  其他 worker 结果丢失；函数返回即代表所有 goroutine 已回收。
- 每个 worker 的原始字节只加到全局计数器一次（`sync/atomic`），
  `ConnectionReports` 的总和必须等于聚合字节数，重复计数会在测试中暴露。

### 共享字节预算

`sharedBudget` 是一个原子字节预算，供所有 worker 共用：

```text
worker 需要读/写 N 字节
  -> CAS 预留 min(N, remaining)
  -> 实际少读的部分退还（下载 Read 返回 n < 预留）
  -> 预算耗尽后所有 worker 得到 io.EOF，阶段自然结束
```

因此 `--max-bytes` 在 1 连接和 16 连接下含义一致：整个阶段的字节总量，
不会因为连接数变多而放大。并发预留和退还是 CAS 操作，`go test -race` 覆盖。

### 统一测量窗口

多连接不能把各连接耗时相加，也不能把各连接的 Mbps 相加。GoSpeed 使用一个
共享窗口：

- 窗口开始：所有连接中最早出现数据的时刻（下载为第一个响应字节，
  上传为第一次写出请求体）；
- 窗口结束：按字节结束时取最后一个数据字节的时刻，按时长结束时取统一的
  时长截止点；上传取最后一个服务端确认读完的时刻；
- 速率 = 所有连接在该窗口内传输的字节总和 × 8 / 窗口时长 / 1e6。

窗口名称随结果返回，见下文“测量窗口”一节。

### 部分连接失败

- 只要有连接成功，阶段仍产出结果，但结果必须显式携带
  `active_connections`、`failed_connections`、`connection_reports` 和
  `warnings`；吞吐量只代表“实际参与传输的连接”的合计能力。
- 全部连接失败时阶段直接返回错误（`ErrAllConnectionsFailed`），
  不输出任何 Mbps。
- 上传阶段额外要求：客户端总发送字节数 == 服务端确认总字节数，
  不一致时返回 `ErrByteCountMismatch`，不输出结果。

## 实时采样与统计

### 采样器

- 采样由独立的 sampler goroutine 完成，数据路径只做原子计数，
  不调用任何用户回调，因此回调不会阻塞数据传输。
- 采样间隔默认 200 ms，可由 `--sample-interval` 配置。
- 每次采样记录：`phase`、`timestamp`（UTC 墙上时间）、`elapsed_ns`
  （单调时钟）、`bytes_transferred`、`current_mbps`、`average_mbps`、
  `active_connections`。
- `current_mbps` 使用两次真实观测之间的单调时钟差值计算，**不使用标称采样间隔**，
  因此调度延迟不会把速率算错。
- 慢消费者策略：ticker 的 tick 会被 runtime 合并（丢弃过期 tick），
  采样点可能变少，但最终聚合速率与统计永远来自原子计数器和真实窗口，
  不会因为丢弃采样而丢失数据。
- 进度回调由采样器 goroutine 调用，回调应快速返回；回调异常变慢只会减少
  采样点数量，不会拖慢测速。

### 进度与状态机

统一状态（`RunState`）：

```text
idle -> preparing -> latency_testing -> download_testing -> upload_testing
                                                     \-> completed
                                                     \-> failed
                                                     \-> cancelled
```

`Progress` 事件包含：状态、阶段、阶段开始时间、已耗时、累计字节、
窗口平均 Mbps、瞬时 Mbps、活跃连接数、阶段进度条（fraction）与剩余时间估计。
`Budget` 中 fraction / remaining 为 nil 时表示“无法计算”，调用方必须显示 N/A。

### 统计

对采样点中的 `current_mbps` 计算：

| 指标 | 规则 |
| --- | --- |
| 平均值 | 算术平均 |
| 中位数 | 排序后取中间值（偶数个取中间两个的平均） |
| 最小值 / 最大值 | 采样点的极值 |
| 标准差 | 样本标准差（n-1），结果中 `stddev_kind = sample_n_minus_1` |
| 变异系数 | 标准差 / 平均值 × 100%，平均值 ≤ 0 时不计算 |

采样点不足时对应字段为 `null`：0 个采样点只保留 `samples: 0`，
1 个采样点可以给出平均/中位/最小/最大，但标准差与变异系数为 `null`。
瞬时吞吐量（`current_mbps`）、窗口平均吞吐量（`mbps`）与未来多次测速平均
是三个不同概念，结果字段分开存放，绝不混用。

## 测量窗口

速率必须和测量窗口一起解释，因此 `Result` 中直接记录了窗口名称。

| 场景 | 窗口 | 字段值 |
| --- | --- | --- |
| 单连接下载 | 第一个响应字节 → 传输停止 | `first_response_byte_to_last_body_byte` |
| 多连接下载 | 首个连接第一个响应字节 → 聚合传输停止 | `shared_window_first_response_byte_to_last_body_byte` |
| 单连接上传 | 第一次写请求体 → 服务端确认读完 | `request_body_write_start_to_server_confirmation` |
| 多连接上传 | 首个连接第一次写请求体 → 最后一个服务端确认读完 | `shared_window_first_body_write_to_last_server_confirmation` |

下载窗口排除 DNS、TCP、TLS 和请求往返时间；上传窗口故意包含确认往返，
让结果偏保守而不会虚高。分子一律来自真实传输字节数：下载用客户端实际读到的
字节数，上传用服务端确认的字节数（并要求与客户端发送数完全一致）。

## 单位与公式

```text
Mbps   = bytes × 8 / seconds / 1,000,000
MB/s   = bytes / seconds / 1,000,000
MiB/s  = bytes / seconds / 2^20
```

- `Mbps` 是百万比特每秒，CLI 默认展示单位；
- `MB/s` 是十进制 MB 每秒，作为辅助信息展示；
- `MiB/s` 仅用于 2 的幂语义展示（CLI 的字节量单位），结果 JSON 不输出该字段；
- 任何除以零时长的计算都返回 `ErrZeroDuration`，不会输出 `+Inf` 或虚假数字。

## 取消、超时与生命周期

- 所有网络操作都接收 `context.Context`；Ctrl+C 通过 `signal.NotifyContext`
  取消整个测试，engine 返回 `cancelled`，CLI 退出码 130。
- 每个阶段有独立超时（`Options.Timeout`），且必须大于传输时长
  （`Options.Duration`），否则配置直接被拒绝。
- 下载使用时长窗口作为正常结束条件、阶段超时作为硬上限；
  由时长窗口到期导致的取消被识别为正常结束（`stop_reason = duration_elapsed`）。
- 上传通过“reader 返回 io.EOF”在共享时长截止点优雅结束请求，
  服务端可以完成统计并确认，因此确认等待时间不会被误当成纯发送时间。
- 所有 worker 由 `WaitGroup` 回收；采样器由 `done` channel + `WaitGroup` 回收；
  上传 body 的读写在 net/http 自己的 goroutine 中，使用原子计数避免 data race。

## HTTP 客户端约定

- `DisableCompression = true`，并显式发送 `Accept-Encoding: identity`：
  压缩会让“线上字节数”与“用户想要的载荷吞吐”不一致。
- 请求携带 `Cache-Control: no-store`，避免中间缓存伪造高速率。
- 连接复用：延迟阶段后续采样、下载、上传共享 keep-alive 连接池；
  多连接阶段由 Go 的 `http.Transport` 为每个并发请求分配独立连接。

## 服务端安全边界

- 固定路由：`GET /health`、`GET /ping`、`GET /download`、`POST /upload`；
  不存在任何 URL 转发、文件读取或目录映射逻辑。
- 默认监听 `127.0.0.1`；监听其他地址时 CLI 会打印显式警告。
- 上传使用 `http.MaxBytesReader` 限制大小并直接丢弃数据，不写入磁盘；
  下载受最大字节数与最大时长双重限制。
- 服务端为并发设计做了验证：多个并行下载 / 上传各自独立计数、互不干扰，
  共享的只有只读随机块；`TestServerHandlesConcurrentTransfers` 覆盖 16 路并发。
- 读写超时可配置，且会校验“写超时必须大于最大下载时长”，
  避免长下载被服务器自身截断而误判为网络故障。
- 所有响应带 `Cache-Control: no-store` 与 `X-Content-Type-Options: nosniff`。

## 结果模型

`Result` 是唯一对外结果结构，字段稳定、可 JSON 序列化：

- 身份：`test_id`、`timestamp`
- 目标：`target.server_id / server_name / server_address / protocol / local`
- 配置快照：`settings.*`（时长、超时、连接数、采样间隔、采样数、是否预热）
- 延迟：`latency`（`type = http_rtt`、min/avg/max、原始样本、jitter）
- 下载 / 上传：`bytes`、`duration_ns`、`mbps`、`mb_per_second`、
  `connections`（请求数）、`active_connections`、`failed_connections`、
  `measurement_window`、`stop_reason`
- 采样：`samples[]`（原始采样点，可复算统计）
- 统计：`statistics`（mean/median/min/max/stddev/cv，无法计算时为 `null`）
- 证据：`connection_reports[]`（每条连接的状态、字节数、
  上传的服务端确认字节数与错误信息）
- 告警：`warnings[]`（例如部分连接失败；有告警不等于测试干净）
- 状态：`status`（`not_started` / `running` / `completed` / `failed` / `cancelled`）

未采集的指标使用可选类型。例如 `latency.packet_loss_percent` 永远是 `null`，
因为项目还没有 UDP/ICMP 丢包测试能力，也绝不用 HTTP 请求失败率冒充丢包率。

## 与 v0.1.0 的兼容性

- CLI 命令和原有参数保持不变，新增 `--sample-interval`；
  `--connections` 从“只允许 1”扩展为 1..16。
- 单连接结果中的窗口名称、`connections`、`stop_reason` 取值保持原样，
  v0.1.0 的 JSON 消费者可以继续读取旧字段。
- 新增字段（`active_connections`、`samples`、`statistics`、
  `connection_reports`、`warnings`）都是增量字段。
- 测速服务端 API 未做任何不兼容修改，v0.1.0 客户端仍可对 v0.2.0 服务端测速。

## 未来扩展点

| 方向 | 落点 |
| --- | --- |
| ICMP / UDP / TCP Connect 延迟 | 新增采集器，扩展 `LatencyType` |
| 多节点自动选择 | `internal/nodes` 增加 RTT 探测与排序策略 |
| 历史记录与导出 | 消费 `Result` JSON，可在 `internal/history` 中实现 |
| 多次测速取中位数 | 在结果之上增加 run 级聚合，复用 `Statistics` |
| Wails + Vue 3 桌面端 | 通过 binding 调用 `Engine.Run`，消费 `Progress` 与 `Result` |
| 更细的并发研究 | 复用 `ConnectionReports` 对比不同连接数、拥塞控制与服务器容量 |
