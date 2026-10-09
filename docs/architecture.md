# GoSpeed 架构设计

本文档描述 GoSpeed v0.3.0 的模块划分、多节点架构、并发模型、采样与统计方式、
测量窗口和安全边界。

## 设计目标

1. 测速算法与界面完全解耦：CLI、未来的 Wails 桌面端和 Web UI 都只是调用
   `internal/speedtest`。
2. 只使用真实测量数据：没有采集到的指标使用 `null` / `N/A` 表示。
3. 结果可解释、可复现：每个速率都记录了测量窗口、原始字节数、原始耗时；
   多连接结果记录逐连接证据；节点选择记录方法、原因与探测数据。
4. 不做“补偿”：任何 Mbps 结果都不会乘以经验系数去接近某个商业工具。
5. 标准库优先：v0.3.0 依然没有引入任何第三方依赖。
6. 安全默认：服务端默认只监听 `127.0.0.1`，不转发、不落盘、不暴露系统信息；
   节点地址受显式策略约束。

## 模块划分

| 模块 | 职责 | 依赖 |
| --- | --- | --- |
| `cmd/gospeed` | 进程入口，安装信号处理，调用 CLI | `internal/cli` |
| `internal/cli` | 命令行、节点子命令、进度与结果渲染 | `config`、`nodes`、`server`、`speedtest` |
| `internal/speedtest` | 测速引擎：延迟、多连接下载 / 上传、采样、统计、多轮汇总、能力协商 | 标准库、`internal/version` |
| `internal/nodes` | 节点模型、配置读写、地址策略、健康探测、自动选择 | 标准库 |
| `internal/server` | 自建测速服务端（HTTP API + 能力协商 + 并发保护） | 标准库、`internal/version` |
| `internal/config` | CLI 与测试共享的默认配置 | 标准库 |
| `internal/version` | 版本号与 User-Agent | 标准库 |

`internal/nodes` 内部文件职责：

| 文件 | 职责 |
| --- | --- |
| `node.go` | 节点模型、结构校验、配置文件读取 |
| `manager.go` | 内存节点管理：List / Enabled / Get / Add / Remove / Update / SetEnabled |
| `store.go` | 配置文件持久化：原子写入、增删改（先写盘再切换内存状态） |
| `security.go` | 地址策略：回环、链路本地、云元数据地址的准入规则 |
| `probe.go` | 健康探测：分段计时、有限重试、能力协商、延迟采样 |
| `select.go` | 自动选择：可用性、HTTP RTT 中位数、稳定 ID，并生成选择原因 |

`internal/speedtest` 内部文件职责：

| 文件 | 职责 |
| --- | --- |
| `engine.go` | 选项校验、阶段编排、统一状态机、预热、能力协商入口 |
| `download.go` / `upload.go` | 多连接传输、共享窗口、逐连接结果 |
| `transfer.go` | 共享预算、原子计数器、worker 调度、连接级告警 |
| `sample.go` | 实时采样器与描述性统计 |
| `summary.go` | 多轮测速聚合（`--repeat`） |
| `capabilities.go` | `GET /capabilities` 解析与服务端限额校验 |
| `result.go` / `metrics.go` / `errors.go` | 结果模型、单位换算、可判定错误 |

依赖方向是单向的：`cli -> {nodes, speedtest, server, config}`，
`speedtest` 不反向依赖任何表现层模块。

## 多节点数据流

```text
gospeed nodes check / auto
    +-- nodes.Store        static JSON configuration
    +-- nodes.Probe        DNS / TCP / TLS / HTTP stages + /health + /capabilities + /ping samples
    +-- nodes.SelectAuto   healthy before degraded, lower median HTTP RTT, then node ID

gospeed test --node / --auto
    +-- cli.resolveTarget  Node -> speedtest.Target
    +-- speedtest.Engine.Run
            +-- warmup          GET /health
            +-- capabilities    GET /capabilities + advertised limit check
            +-- latency phase   GET /ping samples
            +-- download phase  N connections, shared window
            +-- upload phase    N connections, server confirmation
```

## 节点模型与配置

静态配置与动态探测严格分离：

- `nodes.Node`：ID、Name、Country、Region、City、Provider、BaseURL、Protocol、
  Enabled、Description、Capabilities、Local —— 只有这些会写入 JSON 文件；
- `nodes.Probe`：状态、检查时间、尝试次数、DNS/TCP/TLS/HTTP RTT 分段耗时、
  延迟样本、服务端版本、能力与限额、错误 —— 只存在于内存与命令输出中，
  永远不写回配置文件。

配置文件格式：

```json
{
  "nodes": [
    {
      "id": "local",
      "name": "Local Test Server",
      "base_url": "http://127.0.0.1:8080",
      "protocol": "http",
      "enabled": true,
      "local": true
    }
  ]
}
```

`nodes.Store` 的写路径是“先写临时文件、fsync、rename 覆盖”：

```text
validate (structure + address policy + unique ID)
  -> write a temporary file in the same directory
  -> Sync
  -> os.Rename (atomic replace)
  -> swap the in-memory manager only after the write succeeded
```

写失败时内存状态保持不变，磁盘上仍是旧配置；`configs/nodes.example.json`
被明确视为只读示例，CLI 拒绝写入。

## 节点地址策略

节点定义是唯一能把地址带进工具的地方，因此加载与保存时都执行同一套策略
（`nodes.ValidateTarget`）：

| 目标 | 规则 |
| --- | --- |
| 回环地址（127.0.0.0/8、::1、localhost） | 仅当节点显式标记 `"local": true` 时允许 |
| 链路本地（169.254.0.0/16、fe80::/10） | 始终拒绝 |
| 云元数据（169.254.169.254、100.100.100.200、fd00:ec2::254、metadata.*） | 始终拒绝 |
| 私网地址（RFC1918、ULA） | 允许：测试局域网服务器是合法场景 |
| 公网地址与域名 | 允许；HTTPS 由 `net/http` 正常校验证书 |

一个共享来的节点清单因此不能偷偷把客户端变成对内网管理面或云元数据的探测器。
GoSpeed 也不提供任何 URL 转发或开放代理能力，节点只用于固定的
`/health`、`/ping`、`/capabilities`、`/download`、`/upload` 请求。

## 健康探测

一次探测（`Manager.Probe`）内部的顺序：

1. `GET /health`，通过 `httptrace` 记录四个独立阶段：
   - `DNSStart` 到 `DNSDone`：DNS 解析耗时（IP 字面量不触发，显示 N/A）；
   - `ConnectStart` 到 `ConnectDone`：TCP 连接耗时；
   - `TLSHandshakeStart` 到 `TLSHandshakeDone`：TLS 握手耗时（仅 HTTPS）；
   - 请求开始到 `GotFirstResponseByte`：HTTP RTT。
2. 解析健康响应，要求 HTTP 200 且 `status = "ok"`；
3. `GET /capabilities`（可选端点）；
4. 可选的 `/ping` 多次采样，用于自动选择的 RTT 中位数。

阶段时间戳是相对单调时钟原点的偏移，墙钟跳变不会产生负数或零值；
平台时钟无法分辨的极短阶段会读作 0，该值表示“低于时钟分辨率(N/A)”，
而不是“耗时为零”。延迟采样中低于分辨率的样本会被单独计数
（`latency_below_resolution`），中位数只在可测量样本上计算。

状态判定：

| 状态 | 含义 |
| --- | --- |
| `healthy` | 第一次尝试就确认健康（旧服务端缺少 `/capabilities` 不算问题） |
| `degraded` | 主机有响应但健康接口未确认正常，或需要重试才成功，或能力响应异常 |
| `unavailable` | 所有尝试都在 DNS / TCP / TLS / HTTP 层失败 |
| `unknown` | 尚未探测 |

重试次数有上限（1..5，默认 2），每次尝试都受 `context` 与单次超时约束；
一次失败不会被表述为“永久不可用”。

## 能力协商

服务端 `GET /capabilities` 返回：

```json
{
  "protocol_version": 1,
  "server_version": "0.3.0",
  "capabilities": ["latency", "download", "upload"],
  "limits": {
    "max_connections_per_test": 16,
    "max_concurrent_tests": 32,
    "max_duration_seconds": 60,
    "max_download_bytes": 4294967296,
    "max_upload_bytes": 1073741824
  }
}
```

客户端行为：

- 404 / 405：记录为“旧服务端不支持能力协商”，继续测速（v0.2.0 兼容）；
- 其它状态或 JSON 解析失败：记录错误但不伪造能力信息，测速结果中如实标注；
- 协商成功：把协议版本、服务端版本、能力列表与限额写入结果；
- 限额校验：客户端请求的连接数、时长或字节预算超过服务端公布值时，
  直接返回 `ErrServerLimitExceeded`，**不会静默裁剪**用户请求；
- 能力校验：服务端公布的能力列表缺少本次需要的阶段时返回
  `ErrServerCapabilityMissing`，不会盲目假设远程服务端支持全部功能。

## 自动选择算法

`nodes.SelectAuto` 只使用实测数据，规则固定且可解释：

1. 排除本次探测中 `unavailable` / `unknown` 的节点（不可用节点仍会出现在
   `nodes check` / `nodes auto` 的探测表格中，只是不参与排名）；
2. `healthy` 排在 `degraded` 之前；
3. 同状态下按实测 HTTP RTT 中位数升序；没有可测量样本的节点排在最后；
4. 仍并列时按节点 ID 升序（稳定、可复现），并在原因中明确写出是并列。

选择结果 `Selection` 包含：被选中节点、方法（auto / manual / default）、
选择原因、获胜节点的 Probe、全部候选的排名与原因，以及固定提示
“延迟排序只用于挑选候选节点，不代表服务器带宽排名”。

当所有启用节点都不可用时返回 `ErrNoNodeAvailable`，不做“猜一个”的降级。

## 多连接传输（v0.2.0 起沿用）

- 每个 worker 一个 goroutine、一次独立 HTTP 请求、独立字节计数；
- `sharedBudget` 用 CAS 预留字节，未用完的部分退还，聚合字节数不会重复或超发；
- 所有连接共享一个测量窗口：下载从最早的第一个响应字节到聚合停止，
  上传从最早的第一次请求体写入到最后一个服务端确认；
- 速率 = 共享窗口内的总字节 x 8 / 窗口时长 / 1e6，绝不把连接时间或 Mbps 相加；
- 每条连接的结果进入 `connection_reports`；部分连接失败时输出结果 + 告警
  （含有效连接数），全部失败时返回 `ErrAllConnectionsFailed`；
- 上传要求客户端发送字节数等于服务端确认字节数，否则返回
  `ErrByteCountMismatch`，不输出速率。

## 多次测速与汇总

`gospeed test --repeat N` 顺序执行 N 次独立测试：

- 每次仍生成完整的 `Result`（保留原始字节、样本、逐连接证据）；
- 全部完成后由 `speedtest.SummarizeRuns` 生成 `Summary`：
  运行次数、完成 / 失败 / 取消计数、逐轮明细、以及
  Download / Upload / HTTP RTT / Jitter 的平均、中位、最小、最大、
  样本标准差（n-1）与变异系数；
- 只有 `completed` 运行进入统计；失败与取消仍然计数并保留明细；
- 目标、连接数、时长或字节预算不同的运行会被拒绝（`ErrMixedConditions`），
  不同条件永远不会被合并成同一个统计总体；
- `--repeat 1`（默认）保持 v0.2.0 的单一 `Result` JSON 结构不变；
  `--repeat > 1` 输出 `{ "summary": ..., "results": [...] }`。

## 服务端安全边界

- 固定路由：`GET /health`、`GET /ping`、`GET /capabilities`、
  `GET /download`、`POST /upload`；不存在任何 URL 转发、文件读取或目录映射；
- 默认监听 `127.0.0.1`；监听其他地址时 CLI 打印显式警告；
- 并发测速上限（默认 32）：`/download` 与 `/upload` 超过上限立即返回
  503 + `Retry-After`，`/health`、`/ping`、`/capabilities` 不受影响；
- 单请求上传 / 下载字节上限、单请求时长上限、HTTP 读写超时；
  写超时必须大于最大下载时长，避免服务端自己截断长下载；
- 上传数据直接丢弃，不落盘；随机下载块只读共享；
- 所有响应带 `Cache-Control: no-store` 与 `X-Content-Type-Options: nosniff`。

尚未实现（见路线图）：节点身份验证、全局字节配额、单客户端配额、监控告警。
公开部署前必须自行增加这些能力，并清楚单次测速会消耗真实公网流量。

## 结果模型（v0.3.0 增量）

在 v0.2.0 的基础上新增：

- `target.network_scope`：`local` / `lan` / `remote` / `unknown`
  （按配置地址分类；域名保持 `unknown`，不做猜测）；
- `target.selection_method` / `target.selection_reason`：节点选择方式与原因；
- `target.health_status` / `target.health_latency_ns`：预热或探测得到的健康信息；
- `target.capabilities`：能力协商结果（supported / unsupported / protocol_version /
  server_version / capabilities / limits / error）；
- `warnings[]`：部分连接失败等非致命问题；
- `Summary` 与 `BatchResult`：重复测速的聚合结构与原始结果列表。

所有新增字段都是增量字段，旧消费者可以继续读取原有字段。

## 与 v0.2.0 的兼容性

- CLI：原有命令与参数保持不变；新增 `nodes` 子命令、`test --node/--auto/--repeat/--capabilities`；
- `gospeed nodes`（无子命令）仍等价于 `nodes list`，`nodes --check` 仍可用；
- 服务端 API 只新增 `GET /capabilities`，v0.2.0 客户端不受影响；
- 单连接结果的窗口名称、`connections`、`stop_reason` 取值保持原样；
- 默认服务端监听地址保持 `127.0.0.1`。

## 未来扩展点

| 方向 | 落点 |
| --- | --- |
| ICMP / UDP / TCP Connect 延迟 | 新增采集器，扩展 `LatencyType` |
| 多节点并行对比测速 | 复用 `Engine.Run` 与 `Summary`，在 CLI 侧调度 |
| 历史记录与导出 | 消费 `Result` / `Summary` JSON，可在 `internal/history` 中实现 |
| 节点认证与配额 | `nodes.Node` 增加凭据引用，服务端增加按字节的全局配额 |
| Wails + Vue 3 桌面端 | 通过 binding 调用 `Engine.Run`，消费 `Progress`、`Result`、`Selection` |
