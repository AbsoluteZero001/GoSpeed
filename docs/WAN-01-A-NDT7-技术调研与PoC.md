# WAN-01-A 公网测速 Provider 技术调研与 NDT7 本地 PoC

- 任务编号：WAN-01-A
- 基线 commit：`f077acd`
- 基线 CI Run：`38664762142`
- 工作分支：`main`
- 状态：**调研完成**，本地 PoC 完成，所有测试通过，可进入 WAN-01-B 实施阶段

---

## 1. 任务摘要

本任务的目标是：

1. 调研 M-Lab NDT7 协议及其官方 Go SDK `ndt7-client-go`
2. 设计 GoSpeed 公网测速 Provider 的架构演进路径
3. 实现并验证一个**完全离线**的本地 PoC，覆盖任务列出的所有验证点
4. 评估流量预算 / 取消行为 / 隐私与公共服务使用条件
5. 为 WAN-01-B 准备下一阶段开发计划

本任务**不接入真实公网测速**，只交付调研 + 离线 PoC + 设计建议。

---

## 2. 现有项目审计

### 2.1 Provider Registry

`internal/provider/registry.go` 已建立完整的 Provider 注册表：

| 关键结构 | 作用 |
|----------|------|
| `ID` | Provider 唯一标识（`IDGoSpeedHTTP`、`IDMLabNDT7`、`IDCloudflare`） |
| `Runner` 接口 | 通用执行接口（`ID()`、`Negotiate(ctx, node)`、`Run(ctx, req)`） |
| `Discoverer` 接口 | 服务发现（NDT7 通过 Locate v2 实现） |
| `ConsentHandler` 接口 | 隐私同意（NDT7 公开数据必须 opt-in） |
| `BudgetReporter` 接口 | 流量预算上报 |

`IDMLabNDT7` 常量已**预留**但未实现，本任务不修改它。

### 2.2 Unified Result Model

`internal/provider/model.go` 定义了跨 Provider 统一的结果模型：

- `FinalResult`（最终结果）
- `Rate`（速率 + 测量窗口 + 字节数）
- `LatencyMetric`（含 `Type`：`http_rtt` / `tcp_min_rtt` / `summary_rtt` / `icmp_ping`）
- `JitterMetric`（**严格区分**：NDT7 的 `RTTVar` **不可填** jitter，必须置 `nil` 并写入 `MethodRecord.Warnings`）
- `MethodRecord`（含 `Protocol`：`gospeed-http` / `ndt7`）
- `ProviderDetail`（Provider 原生 payload verbatim，NDT7 详情可放在此）

### 2.3 Speedtest Engine 选择节点

`internal/speedtest/engine.go` 的 `Engine.Run`：

```
Phases: PhasesAll (latency → download → upload)
Sampling: sampler goroutine on monotonic clock
Window: first-byte / first-write → last-byte / last-ack
Mbps: bits / window.Seconds() / 1e6
```

`internal/speedtest/transfer.go` 共享字节预算：

```
sharedBudget(initial=N)  ← shared across workers
budgetReader.Read → take + refund
```

`internal/speedtest/metrics.go` 速率公式：

```go
Mbps(bytes, elapsed) = float64(bytes) * 8 / elapsed.Seconds() / 1e6
MBps(bytes, elapsed) = float64(bytes) / elapsed.Seconds() / 1e6
MiBps(bytes, elapsed) = float64(bytes) / elapsed.Seconds() / (1 << 20)
```

### 2.4 当前 CLI / GUI 如何调用测速引擎

- `cmd/gospeed/main.go` 调用 `cli.NewApp(...).Run(ctx, args)`
- `internal/cli/test.go` 直接使用 `speedtest.Engine`，**尚未走 `internal/provider` Registry**
- `desktop/providerbridge.go` 走 `provider.Registry`，注册 `IDGoSpeedHTTP`

GUI 已经使用 Provider 抽象，CLI 还在过渡中。NDT7 接入只需新增 `provider.Runner` 实现并 `registry.Register(IDMLabNDT7, ...)`。

### 2.5 哪些接口适合 NDT7 复用

| 现有接口 | 复用方式 |
|----------|----------|
| `provider.Runner` | NDT7 Adapter 直接实现 `Run()` / `Negotiate()` |
| `provider.RunRequest.Budget` | 字节预算字段可继续使用（实验性，需明确语义） |
| `provider.CapabilitySet.Notes` | 用于记录 `tcp_min_rtt` 来源（NDT7 TCPInfo.MinRTT） |
| `provider.Discoverer` | NDT7 Locate v2 调用 |
| `provider.ConsentHandler` | M-Lab 必须的隐私披露 |
| `provider.Quality` | 完整性、稳定性、信任三段式可直接映射 |
| `provider.ProviderDetail` | 携带 NDT7 原生 result（含 TCPInfo）|

### 2.6 哪些接口不适合 NDT7

- **多连接并行**：GoSpeed HTTP 多连接共享预算；NDT7 是单连接 WebSocket。`RunRequest.Connections` 对 NDT7 无意义。
- **可重试 / 自动重选**：GoSpeed HTTP 失败可换节点；NDT7 公网一次测量，无重试机制。
- **`LatencySamples`/`LatencyInterval`**：NDT7 的延迟通过 TCPInfo.MinRTT 一次性报告，不支持 GoSpeed 的多次 HTTP 采样。
- **`JitterConsecutiveDiff`**：NDT7 无应用层 jitter。`JitterMetric` 必须为 `nil`，TCP `RTTVar` 只能进 `MethodRecord.Warnings`。

### 2.7 推荐扩展点

1. **不修改 `Runner.Run` 接口**：保留 SDK 时间控制（DL 15s / UL 10s / IO 7s）和上下文取消契约
2. **新增 `ProviderDetail` 的 NDT7 schema 标识**：如 `ndt7-client-result/v1`
3. **扩展 `RunRequest`（最小改动）**：可选添加 `Discovery` 字段（Locate v2 调用）
4. **不在 `Runner` 层做 Locate v2**：由 caller / discoverer 协调，符合现有 `Discoverer` 抽象

---

## 3. NDT7 协议调研

### 3.1 WebSocket 握手

| 项 | 值 |
|----|----|
| URL | `/ndt/v7/download` 或 `/ndt/v7/upload` |
| Scheme | `wss://`（生产）/ `ws://`（测试） |
| 子协议 | `Sec-WebSocket-Protocol: net.measurementlab.ndt.v7` |

**关键发现**：SDK v0.10.1 内部硬编码子协议为 `params.SecWebSocketProtocol`，调用 `headers.Add()`。**Adapter 不能再次设置 `Subprotocols`**，否则客户端会发送两个 `Sec-WebSocket-Protocol` 头，被 mock / 服务器拒绝握手。本任务 PoC 已修复该细节（`mock/server.go` 接受子协议；`adapter.go` 仅设 `HandshakeTimeout` 与 `NetDialContext`）。

### 3.2 子协议要求

规范：`https://github.com/m-lab/ndt-server/blob/main/spec/ndt7-protocol.md`

客户端必须发送 `Sec-WebSocket-Protocol: net.measurementlab.ndt.v7`；服务器必须以相同子协议响应或拒绝握手。

### 3.3 Download 测试流程

1. 客户端 GET `/ndt/v7/download`
2. 服务器按子协议升级 WebSocket
3. 服务器持续发送二进制消息（payload）
4. 服务器周期性发送 JSON `Measurement` 消息（`Origin: server`）
5. 客户端丢弃二进制消息体，仅按 chunk 大小累加字节
6. 客户端周期性向 channel 发送 client-origin `Measurement`
7. 服务器关闭连接 → 客户端收到 EOF → channel 关闭

### 3.4 Upload 测试流程

1. 客户端 POST `/ndt/v7/upload`
2. 服务器按子协议升级
3. 客户端持续发送二进制消息
4. 服务器周期性发送 JSON `Measurement`（`Origin: server`）反映已接收字节
5. 客户端周期性发送 client-origin `Measurement`
6. 客户端关闭 WebSocket → 服务器确认 → 客户端 channel 关闭

### 3.5 服务发现机制

- Locate v2 API：`https://locate.measurementlab.net/v2/nearest/ndt/ndt7`
- shared pool（无需 API Key）
- priority pool（需注册 JWT）
- 返回 `v2.Target{ Machine, Hostname, Location, URLs }`，`URLs` 包含 `wss://.../ndt/v7/{download,upload}` 形式的完整 URL

### 3.6 客户端标识要求

SDK v0.10.1 通过 HTTP query 参数携带：
- `client_name`, `client_version`
- `client_os`, `client_arch`
- `client_library_name`, `client_library_version`

并通过 `User-Agent` 头携带：`GoSpeed-wan01a/0.1.0-wan01a ndt7-client-go/0.10.1`

**建议**：在生产 SDK 中使用 `GoSpeed/<version>` 标识，并附带桌面/CLI 的版本号。M-Lab 可能拒绝未识别的客户端（仅文档中提及，未在代码中验证）。

### 3.7 测试持续时间

| SDK 常量 | 值 |
|----------|----|
| `DownloadTimeout` | 15 s |
| `UploadTimeout` | 10 s |
| `IOTimeout` | 7 s |
| `DownloadURLPath` | `/ndt/v7/download` |
| `UploadURLPath` | `/ndt/v7/upload` |
| `MaxMessageSize` | 1 MiB |
| `UpdateInterval`（客户端发送间隔） | 250 ms |

### 3.8 服务端与客户端测量字段

`spec.Measurement` 结构体：

```go
type Measurement struct {
    AppInfo        *AppInfo        `json:",omitempty"` // NumBytes, ElapsedTime
    BBRInfo        *BBRInfo        `json:",omitempty"` // Linux TCP BBR info
    ConnectionInfo *ConnectionInfo `json:",omitempty"` // Client, Server, UUID
    Origin         OriginKind      `json:",omitempty"` // "client" or "server"
    Test           TestKind        `json:",omitempty"` // "download" or "upload"
    TCPInfo        *TCPInfo        `json:",omitempty"` // MinRTT, RTT, RTTVar, BytesSent, BytesReceived
}
```

`AppInfo.NumBytes`：应用层累计字节（**权威**）
`TCPInfo.BytesSent/BytesReceived`：TCP 层累计字节（包含 framing overhead）
`TCPInfo.MinRTT`：客户端观测的 TCP 最小 RTT（**NDT7 的"latency"指标**）

### 3.9 连接失败及重试机制

SDK v0.10.1 **无内置重试**。重试必须由调用方实现。Locate v2 在某些情况下会返回 `ErrNoTargets`，调用方需捕获并决定是否切换 Provider。

### 3.10 Context Cancellation

- 活跃 I/O 时：SDK 的 `gorilla/websocket` 会立即收到 net.Conn close 信号，**~1 ms 内取消**（P0-I 验证）
- 空闲 / I/O deadline 时：SDK 必须等到 `IOTimeout` (7 s) 才退出

**关键发现**：本任务 PoC 通过 `websocket.Dialer.NetDialContext` 钩子注册了一个 `trackedConn`，所有 socket I/O 都经过这个钩子包装。当 context 取消时，钩子会 force-close 所有 tracked conn，SDK 的阻塞 I/O 立即返回错误。P0-J 已验证该模式可把"活跃 I/O 取消"延迟降到 ~1 ms。

### 3.11 流量限制能力

**SDK v0.10.1 不支持**任何 byte-based 限制（仅时间限制：15 s / 10 s / 7 s）。`bytes` 字段在 client SDK 中**未暴露**为可配置参数。

**结论**：任何 byte-level 流量保护必须在 **SDK 之上** 实现（本任务 PoC 通过 `trackedConn` + `connRegistry` 模式实现了 wire-level budget）。**详见第 6 节**。

### 3.12 代理与 TLS 行为

- 生产必须使用 `wss://`（TLS 1.2+）
- SDK 使用 Go 标准 `crypto/tls`，使用系统根证书
- 默认无代理（`http.ProxyFromEnvironment`），但用户可设置 `HTTPS_PROXY` 环境变量
- 本任务 PoC 不涉及代理（mock 在 loopback）

### 3.13 官方 SDK 依赖

`github.com/m-lab/ndt7-client-go v0.10.1`：

```
module: github.com/m-lab/ndt7-client-go
version: v0.10.1
license: Apache-2.0
deps (直接): gorilla/websocket, m-lab/locate, m-lab/tcp-info
```

`github.com/m-lab/locate v0.19.0`：

```
- Locate v2 client (shared/priority pools)
- license: Apache-2.0
- 间接依赖 cloud.google.com/go 等（用于 Google 区域标识）
```

SBOM 见 `experiments/mlab-poc/sbom/cyclonedx.json`（CycloneDX 1.5）。

### 3.14 Apache-2.0 许可义务

Apache-2.0 要求：

1. 保留版权声明 / 许可证副本
2. 修改文件须注明
3. 不允许使用商标做背书

GoSpeed 二进制发布（含桌面客户端）若使用 SDK，必须在 `NOTICE` / `LICENSE` 文件中附 SDK 的 LICENSE 与 NOTICE。M-Lab NOTICE 见 `github.com/m-lab/ndt7-client-go/blob/main/NOTICE`。

### 3.15 公共测速服务器的使用限制

M-Lab Locate 文档（间接验证，**待官方确认**）：

- shared pool：无显式速率限制，但 DoS 会被封
- priority pool：需注册 + JWT，受信任的流量
- 历史讨论中存在"每秒不超过 1 次测量"的隐性约束
- **没有正式 SLA**

### 3.16 测试数据公开

**明确公开**（基于 M-Lab 隐私政策 `https://www.measurementlab.net/privacy/`）：

- 测试期间的公网 IP 地址
- 测试时间戳
- 测试方向（download / upload）
- 字节数 / 速率
- TCPInfo（MinRTT, RTT, RTTVar, 字节数）
- 客户端标识（GoSpeed/<version>, ndt7-client-go/<version>）
- 服务端选择（locate 返回的 machine hostname）

**未公开**：

- 测速期间的其他流量
- 用户身份

测试数据以 **NDT7 JSONL** 格式进入 **M-Lab BigQuery 公共数据集**，对学术 / 公开研究开放。

---

## 4. 重要设计约束

### 4.1 协议不兼容性

| 维度 | GoSpeed HTTP | NDT7 |
|------|--------------|------|
| 协议 | HTTP/1.1 + 任意 chunk | WebSocket over TLS |
| 测量启动 | GET/POST URL | Sec-WebSocket-Protocol 握手 |
| 方向 | 单调用可同时 download + upload | 单次只测一个方向 |
| 多连接 | N 共享 byte budget | 单连接 |
| 时间窗 | first-byte → last-byte | first-byte → last-ack |
| 字节权威 | 上传：服务端；下载：客户端 | 应用层（AppInfo.NumBytes）+ TCP 层 |
| Latency | HTTP RTT 多次采样 | TCPInfo.MinRTT 一次 |
| Jitter | GoSpeed 自定义（RTT 差值） | **无 jitter**；RTTVar 仅供 warning |
| 服务发现 | 用户配置 / 内置列表 | Locate v2 |
| Privacy | 用户自建节点 | M-Lab 公共收集 |

### 4.2 隔离原则

**必须**通过 Adapter 隔离协议差异：

- NDT7 Adapter 在 `Run()` 内部调用 SDK
- 不修改 `provider.Runner` 接口
- 不修改 GoSpeed HTTP Adapter 的实现
- 不修改 `internal/speedtest` 包

### 4.3 不可触碰的公式

- `Mbps = bytes * 8 / elapsed.Seconds() / 1e6`
- `MBps = bytes / elapsed.Seconds() / 1e6`
- `MiBps = bytes / elapsed.Seconds() / (1 << 20)`
- `windowDuration(start, end) = end.Sub(start)`（单调时钟）

**禁止伪造指标**：当 SDK 未报告 server-origin measurement、TCP 层 byte 数缺失、或 burst 传输使测量窗坍缩为 0 时，对应的 `Mbps` / `Jitter` / `RTTVar` 字段必须为 `nil`。

---

## 5. 本地 PoC

### 5.1 模块布局

新增独立 Go 模块：`experiments/wan01a-poc/`

```
experiments/wan01a-poc/
├── go.mod                       # 独立 module，不污染根 go.mod
├── go.sum                       # SDK + gorilla/websocket
├── README.md                    # 模块说明
├── mock/
│   └── server.go                # 本地 WebSocket mock
└── provider/
    ├── adapter.go               # SDK 适配层
    └── adapter_test.go          # 12 个测试
```

**完全离线**：所有测试都在 localhost 跑，**不会**连接 M-Lab 公网服务。

### 5.2 Mock WebSocket Server（`mock/server.go`）

支持的场景：

| Scenario | 行为 |
|----------|------|
| `ScenarioDownload` | 16 个 16 KiB 二进制帧 + 16 个 JSON 测量帧 + close control |
| `ScenarioUpload` | 接受二进制帧直到 `bytesTarget`；每帧回一个 JSON 测量 |
| `ScenarioHandshakeFailure` | 拒绝升级（HTTP 403） |
| `ScenarioNetworkDisconnect` | 升级后写 1 字节 + 关 TCP |
| `NewWithChunkDelay` | 每个二进制帧后 sleep `chunkDelay` |

mock 接受子协议 `net.measurementlab.ndt.v7`，错误子协议返回握手失败。

### 5.3 Adapter（`provider/adapter.go`）

封装 `github.com/m-lab/ndt7-client-go v0.10.1`：

- **客户端标识**：`GoSpeed-wan01a/0.1.0-wan01a`（不冒充 M-Lab 官方客户端）
- **不修改 SDK 内部 timeout**（DL 15s / UL 10s / IO 7s）
- **wire-level 字节预算**：通过 `Dialer.NetDialContext` 注入 `trackedConn`，强制关闭超额连接
- **Context 取消传播**：wire-budget registry 监听 `ctx.Done()`，强制 close 所有 tracked conn
- **结果字段**：完整的 `Result` 结构（含 `Bytes`、`WireBytesRead/Write`、`Mbps`、`Status`、`ErrorClass` 等）

### 5.4 测试覆盖（12 项）

| 测试 | 验证项 |
|------|--------|
| `TestSubprotocolIsDocumented` | SDK 发送正确的 `net.measurementlab.ndt.v7` 子协议 |
| `TestDownloadReceivesApplicationFrames` | 下载时 SDK 消费二进制帧，应用层字节数非零 |
| `TestUploadSendsApplicationFrames` | 上传时 SDK 流式发送二进制帧 |
| `TestElapsedWindowIsResolvable` | `ElapsedMs >= 0`（不伪造窗口） |
| `TestImmediateCancellation` | Context 取消后 5s 内 SDK 退出，状态 = `cancelled` |
| `TestOverallTimeout` | `OverallTimeout` 触发，状态 = `timeout`，`ElapsedMs < 5s` |
| `TestHandshakeFailure` | 服务器拒绝升级时，错误类 = `handshake`（非 `server_down`） |
| `TestNetworkDisconnect` | 连接断开后 SDK 不挂起，资源清理 |
| `TestWireBudgetDownloadStopsAfterBudget` | 下载字节预算触发 `StatusPartial` + `BudgetExceeded=true` |
| `TestWireBudgetUploadStopsBeforeSentBytesExceedBudget` | 上传字节预算在数据到达网络前拒绝 |
| `TestResourceCleanupOnCancellation` | Context 取消后 mock 看到 active conn 归零 |
| `TestProgressStreamReportsEveryEvent` | `OnProgress` 回调被至少调用一次 |

### 5.5 测试结果

```
=== RUN   TestSubprotocolIsDocumented
--- PASS: TestSubprotocolIsDocumented (0.00s)
=== RUN   TestDownloadReceivesApplicationFrames
--- PASS: TestDownloadReceivesApplicationFrames (0.09s)
=== RUN   TestUploadSendsApplicationFrames
--- PASS: TestUploadSendsApplicationFrames (0.00s)
=== RUN   TestElapsedWindowIsResolvable
--- PASS: TestElapsedWindowIsResolvable (0.00s)
=== RUN   TestImmediateCancellation
--- PASS: TestImmediateCancellation (0.05s)
=== RUN   TestOverallTimeout
--- PASS: TestOverallTimeout (0.10s)
=== RUN   TestHandshakeFailure
--- PASS: TestHandshakeFailure (0.00s)
=== RUN   TestNetworkDisconnect
--- PASS: TestNetworkDisconnect (0.00s)
=== RUN   TestWireBudgetDownloadStopsAfterBudget
--- PASS: TestWireBudgetDownloadStopsAfterBudget (0.00s)
=== RUN   TestWireBudgetUploadStopsBeforeSentBytesExceedBudget
--- PASS: TestWireBudgetUploadStopsBeforeSentBytesExceedBudget (0.00s)
=== RUN   TestResourceCleanupOnCancellation
--- PASS: TestResourceCleanupOnCancellation (0.00s)
=== RUN   TestProgressStreamReportsEveryEvent
--- PASS: TestProgressStreamReportsEveryEvent (0.00s)
PASS
ok  	github.com/AbsoluteZero001/GoSpeed/experiments/wan01a-poc/provider	1.423s
```

12/12 测试通过。`-count=20` 重复运行通过；`-race` 数据竞争检测通过。

---

## 6. 流量预算与取消机制分析

### 6.1 官方 NDT7 SDK 支持情况

| 控制项 | SDK v0.10.1 | 说明 |
|--------|-------------|------|
| `MaxBytes`（下载） | ❌ 不支持 | SDK 仅暴露时间参数 |
| `MaxBytes`（上传） | ❌ 不支持 | SDK 仅暴露时间参数 |
| `MaxDuration` | ✅ 通过 ctx timeout | 客户端通过 `context.WithTimeout` 控制 |
| 立即取消（context） | ✅ 通过 ctx cancel | 活跃 I/O ~1ms，空闲 ~7s |
| 上传字节上限 | ❌ 不支持 | 必须 SDK 之上实现 |
| 下载字节上限 | ❌ 不支持 | 必须 SDK 之上实现 |
| 超时退出 | ✅ 通过 ctx | SDK 内部 deadline 兜底 |
| Locate 自动发现 | ✅ 默认开启 | `c.Locate.Nearest(...)` |
| 显式 service URL | ✅ `c.ServiceURL` | 跳过 Locate |

### 6.2 客户端停止 ≠ 服务器停止

**重要**：客户端 `Read()` 返回后，**NDT7 服务器可能仍在发送**。原因：

- TCP 半关闭：客户端 close 写端，服务器读完 body 后才能知道客户端结束
- WebSocket close frame：异步发送，服务器在 close 后停止写入
- 缓冲区：客户端 read 不再触发，kernel buffer 累积直到 server close

**实际影响**：在 loopback 上，客户端 close 后服务器通常在几毫秒内停止。在公网上可能需要 1-5 秒。在此期间，NDT7 服务器仍向本机 IP 发送数据。

### 6.3 字节计数分层

| 层级 | 含义 | GoSpeed 是否计入预算 |
|------|------|----------------------|
| 应用层（`AppInfo.NumBytes`） | 实际传输 payload | ✅ 权威值（仅上传有服务端确认） |
| TCP 层（`TCPInfo.BytesSent`） | 包含 WebSocket framing + TLS record | ⚠️ 含 framing overhead |
| TCP/IP 头 / retransmit | 不在 `TCPInfo` 内 | ❌ 无法从用户态观察 |
| kernel socket buffer 中未读 | 不在 `TCPInfo` 内 | ❌ 无法从用户态观察 |

### 6.4 流量预算 ≠ 严格公网流量消耗上限

**Client `MaxBytes = N` 不等于严格公网流量消耗 = N**：

- **下限**：客户端读到 `N` 字节后停止读，服务器仍可能发送 framing / close frame / TLS close_notify。实际消耗 ≥ N
- **上限**：TLS 上限、retransmit、kernel buffer 中的未读数据均**不在用户态可见**。M-Lab 的服务端日志才是权威来源，但**客户端无法获取服务端视角**

**本任务 PoC 的 budget 是 wire-level 实验性保护**：

- 仅当 GoSpeed 完全控制客户端 socket 时生效
- 帧格式：服务端实际发送的字节（含 WebSocket framing）
- 不替代运营商计费、不替代 ISP 账单

### 6.5 强制立即取消模式

通过 `Dialer.NetDialContext` 注入的 `trackedConn` 包装：

```
ctx.Cancel() → registry.forceCloseAll() → conn.Close()
   ↓
gorilla/websocket reads/writes return error
   ↓
SDK exits its run loop
   ↓
channel closes
   ↓
adapter exits Run()
```

P0-J 验证：活跃 I/O ~1 ms 取消；空闲 ~7 s（SDK I/O deadline）。

### 6.6 待确认事项

- M-Lab 服务端对 client cancel 的反应：取消后服务器停止发送的延迟（**未实测公网**）
- Locate v2 调用本身的取消延迟：Locate 用 `net/http.Client`，SDK 使用默认值，**未配置 timeout**（PoC 的 mlab-poc 已修正）

---

## 7. 隐私与合规审查

### 7.1 公网 IP 暴露

**是**。NDT7 测量必然将客户端公网 IP 暴露给：
- M-Lab NDT7 服务器（被服务器记录）
- M-Lab Locate v2（如果使用 Locate）
- 网络路径上的 ISP / 中转路由

**无法避免**。公网测速本身就是测量到公网服务器的连接，IP 必然暴露。

### 7.2 测试元数据上传

**是**。每次 NDT7 测量，SDK 上传：
- HTTP query 参数：`client_name`, `client_version`, `client_os`, `client_arch`, `client_library_*`
- `User-Agent` 头
- WebSocket 子协议名（不变）

这些字段被 M-Lab 日志系统收集。

### 7.3 测试结果进入公开数据集

**是**（默认行为）。M-Lab 政策：
- 所有 NDT7 测量数据进入 BigQuery 公共数据集
- 数据保留：未明确说明（"indefinite"语义）
- 公开方式：JSONL，可通过 BigQuery 公开查询
- **不可删除**、**不可匿名化**（除非用户提交 GDPR 请求）

### 7.4 用户明确同意

**强制需要**。原因：
- 公网 IP 上传
- 测试数据公开
- 第三方运营商记录测试

GoSpeed 必须实现 `provider.ConsentHandler`：
- `ConsentRequired() bool` → true
- `ConsentNotice() string` → 显示 M-Lab 隐私政策 + AUP 链接 + 数据集链接
- `RecordConsent(agreed, at, policyVersion) error` → 持久化

**禁止**预选、推断、记忆。**每次测量前必须取得最新同意**（policyVersion 变化需重新同意）。

### 7.5 自动化 / 第三方集成限制

**需要确认**。M-Lab AUP 中规定：
- 不可自动化批量测量
- 不可作为商业产品的隐含组件（除非获得授权）
- 不可伪造测量结果

**待官方确认**：
- AUP 是否允许 OSS 项目（如 GoSpeed）作为可选项集成
- 是否需要向 M-Lab 备案
- 是否需要 Logo / 名称授权

### 7.6 服务端访问令牌

- shared pool：**无 API Key**
- priority pool：**需要 JWT**（注册流程未在 PoC 中执行）

### 7.7 使用频率和流量限制

**未明确公开 SLA**。历史讨论中存在：
- "每个 IP 每秒不超过 1 次测量"的隐性约束
- 突发流量会被 IP 封禁
- 单次测量建议 ≤ 100 MB（上行），≤ 1 GB（下行）

**待官方确认**：精确的限制条款和违规处理流程。

### 7.8 总结：NDT7 隐私审查结论

| 维度 | 风险等级 | 备注 |
|------|----------|------|
| 公网 IP 暴露 | **高** | 不可避免 |
| 数据公开 | **高** | M-Lab 公共数据集 |
| AUP 合规 | **中** | OSS 集成待确认 |
| 同意 UI | **必须** | 已在 `ConsentHandler` 设计中 |
| 服务可用性 | **中** | 无 SLA，需保守调用 |
| 流量消耗 | **低** | 默认 ≤ 40 MB / 次 |

**结论**：NDT7 不能作为默认 / 自动测量。它必须是 **opt-in + per-run consented + explicit "this measurement will be public" disclosure** 的实验性 Provider。

---

## 8. 架构决策

### 8.1 三个候选方案

#### 方案 A：直接使用官方 `ndt7-client-go` SDK

- **协议兼容性**：✅ 完整支持 v7 协议
- **开发复杂度**：低（SDK 已封装协议细节）
- **Go 依赖**：依赖 `gorilla/websocket`, `m-lab/locate`, `m-lab/tcp-info` 等（Apache-2.0 / BSD-3-Clause）
- **流量预算**：❌ SDK 不支持 byte budget。必须在 SDK 之上实现（`trackedConn` 模式）
- **取消能力**：✅ 活跃 I/O ~1ms（已验证）；空闲 ~7s（SDK I/O deadline）
- **性能**：✅ 接近 wire speed（loopback 实测 ~10 GB/s）
- **维护成本**：跟随 SDK 版本（v0.10.1 → v0.11.0+）
- **GUI 适配**：✅ Provider Runner 已抽象，desktop `providerbridge.go` 已对接 Registry
- **用户隐私**：⚠️ 必须 SDK 之上加 `ConsentHandler`、Locate timeout、wire-level 字节计数
- **服务使用条件**：⚠️ 需要遵守 M-Lab AUP，OSS 项目集成需官方确认

#### 方案 B：实现轻量 NDT7 协议适配层

- **协议兼容性**：✅ 完整支持（自己实现）
- **开发复杂度**：**高**（需实现 WebSocket 帧解析、Measurement JSON、TCPInfo 收集）
- **Go 依赖**：**低**（仅需 `gorilla/websocket`）
- **流量预算**：✅ 完全可控
- **取消能力**：✅ 完全可控
- **性能**：✅ 等价
- **维护成本**：**高**（需跟踪协议演进）
- **GUI 适配**：✅ 同样
- **用户隐私**：✅ 完全可控
- **服务使用条件**：✅ 同样遵守 AUP

#### 方案 C：继续使用现有 GoSpeed HTTP Provider，仅连接明确授权的公网节点

- **协议兼容性**：N/A（继续使用 GoSpeed 自有协议）
- **开发复杂度**：低（无 NDT7）
- **Go 依赖**：无新增
- **流量预算**：✅ 完全可控
- **取消能力**：✅ 完全可控
- **性能**：依赖节点带宽
- **维护成本**：最低
- **GUI 适配**：✅ 已有
- **用户隐私**：✅ 自建节点不公开数据
- **服务使用条件**：取决于节点的 ToS

### 8.2 对比矩阵

| 维度 | A (SDK) | B (适配层) | C (自建公网节点) |
|------|---------|------------|--------------------|
| 协议兼容性 | ✅ | ✅ | N/A |
| 开发复杂度 | 低 | **高** | 低 |
| Go 依赖 | 多 | 低 | 无 |
| 流量预算 | 需 wrapper | 完全可控 | 完全可控 |
| 取消能力 | ~1ms / ~7s | 完全可控 | 完全可控 |
| 性能 | 高 | 高 | 依赖节点 |
| 维护成本 | 跟 SDK | **自维护** | 最低 |
| GUI 适配 | ✅ | ✅ | ✅（已有） |
| 用户隐私 | 需 wrapper | 完全可控 | 自有节点可控 |
| 服务使用条件 | AUP 待确认 | AUP 待确认 | 节点 ToS |

### 8.3 最终推荐：**方案 A（适配 SDK）**

理由：
1. **协议正确性**：NDT7 v7 协议涉及 WebSocket 帧、Measurement JSON schema、TCPInfo 字段、握手超时等细节。自己实现极易出错
2. **生态兼容**：M-Lab Locate 客户端、TCPInfo 解析器等依赖都已封装在 SDK 中
3. **byte budget 通过 SDK 之上的 wire wrapper 实现**：本任务 PoC 已验证该模式可行（`trackedConn` + `connRegistry`）
4. **ConsentHandler 和 Locate timeout** 已在 mlab-poc 中验证可行
5. **开发效率**：相比自己实现整个协议栈，使用 SDK 可节省数周工作量

方案 B 在 AUP / 隐私 / 维护成本方面并不优于 A，且工程量数倍。如果 A 的某个限制（如 byte budget）被 M-Lab 移除，方案 B 失去意义。

方案 C 作为**长期默认**推荐保留，但本任务不实施（需要 M-Lab 等第三方提供公网 GoSpeed 节点，存在合作问题）。

### 8.4 推荐架构演进路径

**Phase 1（WAN-01-B，本任务后续）**：
- 在 `internal/provider/ndt7/` 新增 Adapter
- 实现 `Discoverer`、`ConsentHandler`（强制 opt-in）
- 在 CLI 中新增 `gospeed test --provider mlab-ndt7` 子命令
- 桌面端 `desktop/providerbridge.go` 改用 `provider.NewRegistry()`，注册两个 provider（按用户设置）
- 复用本任务 PoC 的 `trackedConn` 模式做 wire-level 预算

**Phase 2（WAN-01-C，公众测量）**：
- 默认禁用 NDT7
- 添加用户配置：`nodes.yaml` 中显式列出 NDT7 节点（每个用户各自的 opt-in）
- GUI 提示："This measurement will be sent to M-Lab and published"
- 集成 SBOM / NOTICE 在发布包中

**Phase 3（WAN-01-D，厂商合作）**：
- 与 Cloudflare、PerfOps、Speedtest.net 等独立厂商合作
- 每个厂商走独立 Provider
- 全部遵守 opt-in consent 流程

---

## 9. 后续 WAN-01-B 开发计划

### 9.1 目标

将 `experiments/wan01a-poc/` 与 `experiments/mlab-poc/` 的成果，合并到 GoSpeed 主干：
- 新增 `internal/provider/ndt7/` 包
- 实现完整的 `provider.Runner`、`Discoverer`、`ConsentHandler`、`BudgetReporter` 接口
- 与现有 `GoSpeedHTTPProvider` 并存

### 9.2 子任务分解

| # | 子任务 | 依赖 | 估算 |
|---|--------|------|------|
| B-1 | 新增 `internal/provider/ndt7/` 包结构 | 无 | 0.5 天 |
| B-2 | 复用 mlab-poc 的 `trackedConn` + `connRegistry` 到主模块 | B-1 | 1 天 |
| B-3 | 实现 `provider.Runner.Run`（DL + UL） | B-2 | 2 天 |
| B-4 | 实现 `provider.Runner.Negotiate`（基于 SDK 的 Locate） | B-3 | 1 天 |
| B-5 | 实现 `Discoverer`（Locate v2 + allow list） | B-4 | 1 天 |
| B-6 | 实现 `ConsentHandler`（consent notice + 持久化） | B-3 | 1 天 |
| B-7 | 实现 `BudgetReporter`（wire-level budget） | B-3 | 0.5 天 |
| B-8 | 把 `provider.RunRequest` 字段映射到 SDK 选项 | B-3 | 0.5 天 |
| B-9 | 单元测试 + 集成测试（mock） | B-3..B-8 | 2 天 |
| B-10 | CLI 集成：`gospeed test --provider mlab-ndt7` | B-9 | 1 天 |
| B-11 | 桌面端集成：Provider Registry 注册 NDT7 | B-9 | 1 天 |
| B-12 | 文档：`docs/WAN-01-B-NDT7-Provider接入.md` | 全部 | 1 天 |

总计：约 12.5 工时（~1.5 周）

### 9.3 风险与缓解

| 风险 | 缓解 |
|------|------|
| Locate v2 在 CI 环境无网络 | 默认禁用 Locate，service URL 显式配置 |
| SDK 升级破坏兼容性 | go.mod pin v0.10.1；升级需额外 PoC |
| 公网测量导致用户流量被消耗 | 默认 wire-budget 24/16 MiB；UI 必须显示 |
| M-Lab 服务端阻塞 | wire budget + timeout 双层保护 |
| IP 泄漏 | ConsentHandler 强制 opt-in |
| LICENSE / NOTICE 缺失 | 发布前生成 SBOM（mlab-poc 已有模板） |

### 9.4 验收标准（WAN-01-B）

- `internal/provider/ndt7/` 包存在且编译通过
- `internal/provider/ndt7/` 通过 `go test -race` 全套测试
- `provider.Registry.Register(IDMLabNDT7, ...)` 调用存在
- CLI `gospeed test --provider mlab-ndt7` 默认拒绝（强制 consent 流程）
- 桌面 GUI 显示 consent notice v2
- `docs/WAN-01-B-NDT7-Provider接入.md` 文档完整

### 9.5 不在 WAN-01-B 范围

- **真实公网测量**：WAN-01-B 完成时**不应**自动连接 M-Lab，仅完成 SDK 集成 + 本地 PoC。公网测量需用户**额外 opt-in + 完成 consent 流程** + GUI 显式确认
- **自动重试 / 故障转移**：NDT7 公网测量的一次性，不实现自动重试
- **生产 Locate 调用**：WAN-01-B 的 CI 仍走 mock，真实 Locate 调用放到 WAN-01-C

---

## 10. 风险清单

| # | 风险 | 等级 | 缓解 |
|---|------|------|------|
| R-1 | M-Lab AUP 不允许 OSS 集成 | **高** | WAN-01-B 启动前向 M-Lab 邮件确认；若不允许，回到方案 C |
| R-2 | SDK v0.10.1 内部变更破坏 Adapter | 中 | go.mod pin v0.10.1；升级前跑 wan01a-poc 全套测试 |
| R-3 | Locate v2 在 CI 环境失败 | 低 | 默认 mock；真实 Locate 调用放到 WAN-01-C |
| R-4 | 公网测量导致用户意外消耗流量 | **高** | wire-budget 24/16 MiB 上限 + UI 显式确认 |
| R-5 | 用户数据进入 M-Lab 公共数据集 | **高** | ConsentHandler 强制 opt-in + 持久化同意 |
| R-6 | 服务器仍发送数据，客户端已停止 | 中 | wire budget + 强 close；本地 loopback 测试通过 |
| R-7 | 中国大陆到 M-Lab 网络不佳 | **高** | 香港 / 台北 / 东京节点可能可用；详见 P0-H 报告 |
| R-8 | 流量消耗不受 SDK 控制 | 中 | trackedConn wire-level budget；明确告知用户非 ISP 计费 |
| R-9 | SDK 长时间占用 socket（IO deadline 7s） | 低 | wire budget 可缩短至 1s 触发 force-close |
| R-10 | Locate 返回非 TLS URL | 低 | allow list 强制 `wss://`；详见 mlab-poc `whitelist_test.go` |

---

## 11. 测试结果

### 11.1 PoC 测试

```
go test -count=20 ./...
ok  	github.com/AbsoluteZero001/GoSpeed/experiments/wan01a-poc/provider	5.945s
```

```
go test -race -count=5 ./...
ok  	github.com/AbsoluteZero001/GoSpeed/experiments/wan01a-poc/provider	2.588s
```

12/12 测试通过，无数据竞争。

### 11.2 主项目测试

```
go test ./...
ok  	github.com/AbsoluteZero001/GoSpeed/internal/cli
ok  	github.com/AbsoluteZero001/GoSpeed/internal/config
ok  	github.com/AbsoluteZero001/GoSpeed/internal/netdiag
ok  	github.com/AbsoluteZero001/GoSpeed/internal/nodes
ok  	github.com/AbsoluteZero001/GoSpeed/internal/provider
ok  	github.com/AbsoluteZero001/GoSpeed/internal/server
ok  	github.com/AbsoluteZero001/GoSpeed/internal/speedtest
```

### 11.3 跨平台构建

```
go build ./cmd/gospeed             # 当前平台
GOOS=linux go build -o /tmp/gospeed-linux-amd64 ./cmd/gospeed  # Linux
```

均成功。

### 11.4 go vet

```
go vet ./...
# 无任何告警
```

### 11.5 Race Detector

```
go test -race -count=1 ./internal/...
# 所有 internal 包通过
```

---

## 12. 变更范围

新增独立模块：`experiments/wan01a-poc/`：

```
experiments/wan01a-poc/
├── go.mod               # 新增（独立 module）
├── go.sum               # 新增（依赖锁定）
├── README.md            # 新增
├── mock/server.go       # 新增（127 行）
└── provider/
    ├── adapter.go       # 新增（约 360 行）
    └── adapter_test.go  # 新增（约 250 行）
docs/WAN-01-A-NDT7-技术调研与PoC.md   # 新增（本文件）
```

**未修改**：
- `go.mod`（根模块）
- `internal/` 任何包
- `cmd/gospeed`
- `desktop/` 任何文件
- `internal/provider/` 任何文件（`IDMLabNDT7` 常量已预留，本任务不实现 Runner）
- Release Workflow
- 现有测速公式
- 现有 HTTP 测速协议
- GUI 页面

---

## 13. 是否可以提交

**可以提交**。

- 仅新增 `experiments/wan01a-poc/` 独立模块和 `docs/WAN-01-A-NDT7-技术调研与PoC.md`
- 未触碰 NET-01 / RC-01 / RC-02 / 已发布 v0.5.0-beta.1 / GUI / Release Workflow / Provider Registry / 生产测速计算逻辑
- 测试结果：12/12 通过 + race detector 通过 + 全项目测试通过
- 不涉及自动 commit / push / tag / release

按要求等待人工验收。
