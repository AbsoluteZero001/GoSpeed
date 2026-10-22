# WAN-01-B1 NDT7 Provider 工程化报告

> **基线校正（FIX3）**：本文档早期版本混用了三个互斥的 commit 指称，现按 `git log` / `git show` 核实后统一为：

| Commit | 实际含义（已核实） |
|--------|----------------|
| `f077acd` | CI-04 基线：`ci: upgrade GitHub Actions to Node.js 24 runtime` |
| `aea0bd1` | WAN-01-A 提交：`feat(wan01a): 添加 NDT7 协议 PoC 适配器及测试`，仅包含 `experiments/wan01a-poc/`，**不包含** `internal/provider/ndt7/` |
| `eab23cf` | WAN-01-B1 提交：`feat(ndt7): 添加NDT7协议生产适配器`，首次建立 `internal/provider/ndt7/` |
| `188462d` | WAN-01-B1 后续：`refactor(ndt7): 将测量窗口计算从系统时钟改为SDK报告数据源`，**即当前 HEAD，也是本次修改的 diff 基准** |

本文档描述的 `internal/provider/ndt7/` 工作区变更，均以 **`188462d`** 为基线。

- 任务编号：WAN-01-B1
- 基线 commit：`f077acd`
- 工作分支：`main`
- 范围：将 WAN-01-A 的 NDT7 PoC 工程化为生产代码，并将其接入 GoSpeed 的 Provider Registry。

---

## 1. 架构审计结论

### 1.1 现有 Provider 基础设施（已就绪）

`internal/provider/` 已经具备 NDT7 集成所需的一切抽象：

| 类型 | 用途 | NDT7 适用性 |
|------|------|-------------|
| `ID` 常量 | Provider 标识 | `IDMLabNDT7 = "mlab-ndt7"` 已预留 |
| `Node` | 服务节点 | 字段 `Endpoint/Provider/Ownership/Discovery` 直接复用 |
| `Capability` | 能力声明 | `CapDownload/CapUpload/CapLatency/CapJitter` 已定义 |
| `CapabilitySet` | 协商结果 | `Supported` + `Notes` 携带 NDT7 测量方法学 |
| `Direction` | 传输方向 | `DirectionDownload/DirectionUpload` |
| `BudgetLimits` | 流量预算 | `DownloadBytes/UploadBytes/TotalBytes` |
| `RunRequest` | 单次测量请求 | `Directions/Connections/Budget/Duration/...` |
| `Runner` | 执行接口 | `ID/Negotiate/Run` 三方法 |
| `Discoverer` | 服务发现 | NDT7 用 Locate v2 |
| `ConsentHandler` | 隐私同意 | NDT7 强制要求 opt-in |
| `BudgetReporter` | 预算计划 | NDT7 自带默认预算 |
| `FinalResult` | 统一结果模型 | `Download/Upload/Latency/Jitter/Rate` |
| `Rate` | 速率 | `Mbps/ElapsedMs/TransferredBytes/Window/StopReason` |
| `LatencyMetric` | 延迟 | `Type=MetricTCPMinRTT`, `Source` 描述 NDT7 TCPInfo.MinRTT 来源 |
| `JitterMetric` | 抖动 | **必须保持 nil**（NDT7 无应用层 jitter） |
| `Quality` | 完整性 | `Completeness/Stability/Trust` 三段 |
| `MethodRecord` | 方法学 | `Protocol="ndt7"`, `ConsentVersion` 记录披露版本 |
| `ProviderDetail` | 原生 payload | 携带 SDK 完整 `Result` 序列化 |

### 1.2 测量窗口分析

NDT7 没有 GoSpeed HTTP 那种"首字节 → 末字节"的清晰窗口。SDK 通过测量事件 channel 推送以下信号：

| 时序事件 | 数据源 | 何时记录 |
|---------|--------|---------|
| `httptrace.GotFirstResponseByte` | WebSocket upgrade + 第一字节到达 | adapter 不参与 |
| 应用层第一个 binary 帧读取 | `AppInfo.NumBytes` | adapter 记录此时间作为窗口起点 |
| 应用层最后一个 binary 帧读取 | `AppInfo.NumBytes` | adapter 记录此时间作为窗口终点 |
| WebSocket close | 异步发送 | **不计入窗口** |
| TLS close_notify | 异步发送 | **不计入窗口** |

**窗口公式（FIX3 已更正）**：`ElapsedMs = (LastElapsedUs - FirstElapsedUs) / 1000`，其中两个端点均取自 `Origin=client` 帧的 `AppInfo.ElapsedTime`（累计微秒）。
早期版本使用系统时钟 `lastFrameAt - firstFrameAt`，与 SDK 报告的字节数不同源，已删除。WebSocket / TLS 关闭握手的时间不在窗口内。

### 1.3 多连接与共享预算分析

| 维度 | GoSpeed HTTP | NDT7 | 处理 |
|------|--------------|------|------|
| 协议 | HTTP/1.1 | WebSocket over TLS | 隔离 |
| 多连接并行 | N 共享 byte budget | **不支持** | 拒绝 Connections > 1 |
| 单连接内多方向 | 可同时 download + upload | **不支持** | 拒绝 Directions > 1 |
| Byte budget | 共享预算精确执行 | SDK **不支持** | 在 SDK 之上做 wire-level budget |
| 取消 | context 立即传播 | SDK I/O deadline 7s | 在 socket 层强制 close |
| Latency | 多次 HTTP RTT 采样 | 一次 TCPInfo.MinRTT | TCP MinRTT，不能与 ICMP 混用 |
| Jitter | 自定义 consecutive_diff | **无应用层 jitter** | `JitterMetric == nil`，RTTVar 进 warnings |

### 1.4 推荐扩展点

1. **`internal/provider/ndt7/`** — 生产代码包，实现 `provider.Runner` / `Discoverer` / `ConsentHandler` / `BudgetReporter`
2. **复用 `internal/provider/ndt7/mock/`** — 把 WAN-01-A PoC 的 mock 提升为生产级测试夹具
3. **不动** `provider.Runner` 接口 / GoSpeed HTTP Provider / GUI / CLI 命令行
4. **新 SDK 依赖**：`gorilla/websocket v1.5.4-0.20250319132907-e064f32e3674` + `m-lab/ndt7-client-go v0.10.1`

---

## 2. Provider 设计

### 2.1 包结构

```
internal/provider/ndt7/
├── adapter.go       # SDK 封装（wire budget + 测量窗口 + 取消传播）
├── adapter.go 内的 trackerConn/registry
├── runner.go        # provider.Runner / Discoverer / ConsentHandler / BudgetReporter
└── mock/
    └── server.go    # localhost WebSocket mock（Scenarios: download / upload /
                     #   handshake_failure / network_disconnect；NewWithChunkDelay 支持分块间隔）

internal/provider/ndt7/runner_test.go  # 22 个测试覆盖任务矩阵
```

### 2.2 关键设计决策

| 决策 | 选择 | 理由 |
|------|------|------|
| SDK 引入方式 | **直接依赖**官方 SDK v0.10.1 | 协议细节繁重，自己实现风险高 |
| Byte budget | **SDK 之上的 wire-level 包装** | SDK 不支持 byte cap；通过 `websocket.Dialer.NetDialContext` 钩子注入 `trackedConn` |
| Subprotocol | **不设置 `Dialer.Subprotocols`** | SDK 内部硬编码 + headers.Add；重复会触发握手失败 |
| 测量窗口 | **首个 measurement 帧 → 末个 measurement 帧** | 排除 TCP+TLS+WebSocket 握手与关闭握手 |
| Mbps 计算 | **`AppInfo.NumBytes * 8 / elapsed / 1e6`** | 应用层 byte，不含 framing overhead |
| StopReason | **新增 ndt7_* 前缀** | 与 HTTP provider 区分，便于按协议聚合 |
| 错误分类 | **handshake_failed / network_error / timeout / cancelled / server_down** | 与已有 engine errorCode 对齐 |
| Consent | **Provider-level enforcement** | 没有 consent，Runner 不调用 Negotiate / Discover / Run |
| Connections > 1 | **参数错误 ErrConnectionsNotSupported** | 不静默降级 |
| 多方向 | **参数错误** | 同上 |
| Locate v2 调用 | **PoC 不实际调用，仅留 TODO** | 任何公网调用都在 WAN-01-B2 启用 |

### 2.3 与 WAN-01-A PoC 的差异

| 维度 | WAN-01-A PoC（`experiments/wan01a-poc/`） | WAN-01-B1 生产代码（`internal/provider/ndt7/`） |
|------|--------------------------|-------------------------------------|
| 适配目标 | 独立 `wan01a.New()` | `provider.Runner` 接口 |
| 公开 API | `Options{Server, Scheme, Direction, OverallTimeout, WireBudgetBytes}` | `Run(ctx, RunRequest) (FinalResult, error)` |
| 结果类型 | 自定义 `Result` | `provider.FinalResult`（统一模型） |
| Wire budget | 通过 `trackedConn` | 同一机制 |
| Mock | `experiments/wan01a-poc/mock/server.go` | 同一份代码，提升为生产级（`internal/provider/ndt7/mock/server.go`） |
| Consent | 无 | `ConsentHandler` 接口实现 + `ConsentNoticeVersion="ndt7-notice-v1"` |
| Discover | 无 | `Discoverer` 接口实现，返回 `ErrConsentRequired` 占位 |
| Locate v2 | 无 | TODO，留给 WAN-01-B2 |

**为什么不复制整个官方 SDK**：NDT7 协议涉及 WebSocket 帧解析、Measurement JSON schema、TCPInfo 字段、握手超时、I/O deadline 等细节，自实现极易出错。

**为什么不增加更多 SDK 依赖**：所有生产必需依赖已 lock，`gorilla/websocket` 已为根模块的 transitive。`m-lab/locate`、`m-lab/tcp-info`、`m-lab/go`、`m-lab/ndt-server`、`golang.org/x/net` 是 SDK 的 transitive，已经验证 license 兼容（Apache-2.0 / BSD-3-Clause）。

### 2.4 SDK 依赖

`go.mod` 主模块新增：
```
github.com/gorilla/websocket v1.5.4-0.20250319132907-e064f32e3674
github.com/m-lab/ndt7-client-go v0.10.1
```
加上 SDK 间接依赖（Apache-2.0 / BSD-3-Clause）：
```
github.com/araddon/dateparse (MIT, indirect)
github.com/m-lab/go (Apache-2.0, indirect)
github.com/m-lab/locate (Apache-2.0, indirect)
github.com/m-lab/ndt-server (Apache-2.0, indirect)
github.com/m-lab/tcp-info (Apache-2.0, indirect)
golang.org/x/net (BSD-3-Clause, indirect)
```

`desktop/go.mod` 自动 bump `gorilla/websocket` 的 transitive 版本（与根模块统一），无 API 变化。

---

## 3. 测量字段定义

### 3.1 测量字段定义（FIX3 已统一）

**核心规则：测速窗口的分子与分母必须来自同一个 `Origin=client` 帧系列。**

NDT7 协议中两种 `AppInfo` 是**不同量**：

| Origin | `NumBytes` | `ElapsedTime` |
|--------|-----------|---------------|
| `client` | 客户端自计的应用层字节 | 客户端时钟的累计微秒 |
| `server` | **服务端**自计的字节 | **服务端**时钟的累计微秒 |

拿客户端帧减服务端帧得到的不是速率，而是“一方的字节 ÷ 另一方的秒数”。

**最终公式：**

```
Mbps = (LastAppBytes - FirstAppBytes) * 8
       / ((LastElapsedUs - FirstElapsedUs) / 1e6) / 1e6
```

字节与时间均只来自 `Origin=client`。

| 字段 | 来源 | 含义 |
|------|------|------|
| `Download/Upload.Mbps` | `deltaBytes * 8 / windowUs / 1e6` | 应用层 goodput；无法形成窗口时为 **nil** |
| `Download/Upload.ElapsedMs` | `windowUs / 1000` | 客户端帧窗口（不含握手） |
| `Download/Upload.TransferredBytes` | `deltaBytes`（窗口 delta） | 应用层 payload，**窗口内**，非累计值 |
| `Download/Upload.Window` | `ndt7_app_first_measurement_to_last_measurement` / `..._to_last_server_confirmation` | 文档化的窗口命名 |
| `Download/Upload.StopReason` | adapter | 见 5.1 |
| `Status` | adapter | completed / budget_exceeded / cancelled / timeout / failed |
| `Latency` | **nil** | NDT7 run 一次；TCP MinRTT 进 Detail.Payload |
| `Jitter` | **nil** | NDT7 无应用层 jitter；RTTVar 不得冒充 |
| `Detail.Format` | `ndt7-client-result/v1` | 消费者判定能否解码 |
| `MethodRecord.Protocol` | `ndt7` | 跨协议聚合 |
| `MethodRecord.ConsentVersion` | `ndt7-notice-v1` | 持久化同意时记录的披露版本 |

**nativePayload 中的字段定义：**

| 字段 | 含义 |
|------|------|
| `applicationBytes` / `deltaBytes` | 窗口字节 delta（= `lastAppBytes - firstAppBytes`） |
| `windowUs` | 窗口时长（微秒） |
| `firstAppBytes/firstElapsedUs` / `lastAppBytes/lastElapsedUs` | 窗口两端的**客户端** AppInfo 样本，供消费者独立重算 |
| `cumulativeAppBytes` / `cumulativeElapsedUs` | SDK 自开始的**累计**客户端值（与窗口 delta 不同） |
| `measurementSource` | `ndt7_client_appinfo` 或 `ndt7_unavailable` |
| `windowRejectReason` | 无法形成窗口的原因（非空时必填） |
| `serverOriginFrames` / `serverFrameSeen` | 服务端帧数量（**仅活性观测，非终止证据**） |
| `serverAppBytes` / `serverElapsedUs` | 服务端自计值，**仅供旁证，绝不进入公式** |
| `wireBytesRead` / `wireBytesWritten` | 本地 socket 观测值（含 WebSocket/TLS 开销） |
| `budgetLimitBytes` / `budgetExceeded` | 预算上限与是否触发 |

### 3.2 Wire vs Application bytes

| 层级 | 含义 | 是否计入 `TransferredBytes` |
|------|------|---------------------------|
| 应用层（`AppInfo.NumBytes`） | NDT7 协议层 payload | ✅（权威） |
| WebSocket framing | 含在 wire 层 | ❌ |
| TLS record | 含在 wire 层（仅 wss://） | ❌ |
| TCP/IP 头 | 不在用户态可见 | ❌ |
| TCP retransmit | 不在用户态可见 | ❌ |
| kernel buffer 未读 | 不在用户态可见 | ❌ |

`WireBytesRead/Write` 在 Detail.Payload 中报告，但 **不** 进入速率计算。这样：
- **下限**：客户端读到 `N` 字节后停止，服务器可能仍在发送 framing / close，**实际 ≥ N**
- **上限**：TCP retransmit / kernel buffer 等不可见

### 3.3 不冒充未观测指标

- `Latency == nil`：NDT7 不报告独立 latency metric；`TCPInfo.MinRTT` 仅作为 TCP 测量，**不冒充 ICMP Ping**
- `Jitter == nil`：NDT7 无应用层 jitter；TCP RTTVar 不填入 `JitterMetric`
- `Mbps == nil`：当 elapsed ≤ 0（窗口不可解析），不报告速率
- `TransferredBytes == 0` + `Window == ""`：当 0 字节，窗口名仍保留，便于 UI 显示 "N/A"

---

## 4. 流量预算语义

### 4.1 客户端已停止 ≠ 服务器已停止

```
t0 ──── TCP+TLS+WebSocket 握手 ───────────┐
                                          │
t1 ──── 第一个 measurement 帧 ────────┐   │   应用层窗口
                                     │   │
t2 ──── 最后一个 measurement 帧 ──┐   │   │
                                  │   │   │
t3 ──── client 停止 Read ────────┤   │   │
t4 ──── WebSocket close ─────────┤   │   │
t5 ──── 服务器收到 close ────────┘   │   │
                                   │   │
t6 ──── 服务器停止发送 ──────────┘   │   │
                                    └───┘
```

- 客户端 `adapter.Run` 在 `t2` 之后立即返回（通过 SDK channel close 检测）
- 服务器可能在 `t6` 才停止写入；其间已发出的 framing / close / TLS close_notify 仍占客户端网络
- 因此 `MaxBytes=N` 不严格等于"公网消耗 = N"

### 4.2 Budget 语义

| 维度 | 声明 | 实际 |
|------|------|------|
| 客户端预算触发 | **明确** | `trackedConn` 检测到 ≥ budget 时立即 force-close |
| 客户端 stop read | **明确** | 同上 |
| 服务器停止发送 | 不确定 | 异步，可能在 budget 触发后仍发几个 byte 的 framing |
| 公网消耗 = N | **不保证** | 服务器 framing + TLS overhead 不在用户态可见 |

**结论**：`BudgetLimitBytes` 是 **实验性 PoC 默认值**，不替代 ISP 计费 / 真实网络消耗上限。这点已在 `consent.go` 的 notice 文本中明确披露。

### 4.3 强制立即取消

```
ctx.Cancel() → registry.forceCloseAll()
              ↓
trackedConn.Close() → 唤醒任何阻塞的 Read/Write
              ↓
SDK 的 gorilla/websocket I/O 立即返回错误
              ↓
SDK exits its Run() loop
              ↓
measurement channel closes
              ↓
adapter.Run() 返回 StatusCancelled
```

实测延迟：~1ms（活跃 I/O）vs ~7s（idle，等待 SDK I/O deadline）。P0-I 报告已验证。

---

## 5. StopReason 映射

### 5.1 映射表（FIX3 已核实）

| adapter 状态 | 内部 StopReason | provider.Rate.StopReason | 依据 |
|--------------|----------------|--------------------------|------|
| 完成，窗口成立 | `StopReasonCompleted` | `ndt7_completed` | 通道关闭且客户端帧形成了正增量 |
| 完成，无法形成窗口 | `StopReasonWindowUnavailable` | `ndt7_window_unavailable` | 有字节但 `windowRejectReason` 非空 |
| 客户端零字节 | `StopReasonNoData` | `ndt7_no_application_bytes` | `lastAppBytes == 0` |
| 线级预算超限 | `StopReasonBudgetExceeded` | `ndt7_budget_exceeded` | `registry.exceeded()`（**已观测**） |
| Context cancel | `StopReasonCancelled` | `ndt7_cancelled` | `ctx.Err() == Canceled`（**已观测**） |
| OverallTimeout | `StopReasonTimeout` | `ndt7_timeout` | `ctx.Err() == DeadlineExceeded`（**已观测**） |
| 握手失败 | （未设置） | （无） | 在测量循环前返回 |

### 5.2 为什么不存在 `server_eof` 与 `duration_elapsed`

这两个值已从实现中移除，且不会由本 adapter 产出。两个原因：

1. **`Origin=server` 帧不是终止事件。** 读 SDK `internal/download/download.go`：服务端 TextMessage 帧在测试
   **运行期间**持续上报（官方约每 250 ms），与测试如何结束无关。
2. **SDK 丢弃了终止错误。** `ndt7.go` 的 `collectData`：

   ```go
   go f(ctx, conn, inch)   // 返回值被丢弃
   ```

   协议循环的 error 无任何出口，客户端唯一可观测的事件是**渠道关闭**。

因此 adapter 无法区分 SDK deadline、服务端关闭与异常断连，一律使用中性值 `ndt7_completed`，
并在 `Method.Warnings` 中写明该限制。**只有真正观测到的事件（预算、取消、超时）才使用对应 StopReason。**

### 5.3 Status → ErrorCode（已统一）

| Status | ErrorCode | Completeness |
|--------|-----------|--------------|
| `completed` | —（空，表示成功） | `complete` |
| `budget_exceeded` | `budget_exceeded` | `partial` |
| `cancelled` | `cancelled` | `incomplete` |
| `timeout` | `timeout` | `incomplete` |
| `failed` | `handshake_failed` / `network_error` / `server_down` / `measurement_failed` | `incomplete` |
| `consent_required` | `consent_required` | `incomplete` |

**预算语义统一说明（FIX2 修复）：**

- **不存在 `StatusPartial`**。核对 `internal/provider/model.go`，`MeasurementStatus` 共 6 个值：
  `completed` / `failed` / `cancelled` / `timeout` / `budget_exceeded` / `consent_required`。
- 预算提前终止 → `Status = StatusBudgetExceeded`（契约既有值）+ `ErrorCode = "budget_exceeded"` +
  `StopReason = ndt7_budget_exceeded`。
- “部分完成”由 `Quality.Completeness = provider.Partial` 表达，而非新增状态值。
  新增 `MeasurementStatus("partial")` 会破坏 HTTP Provider 与 GUI 共用的契约。
- **修复前**该路径的 `ErrorCode` 为**空字符串**（adapter 设 `ErrorNone`，而
  `errorCodeForRunError` 对 `ErrorNone` 返回 `""`）。

---

## 6. 错误处理

| 错误 | 检测点 | Status | ErrorCode | StopReason |
|------|--------|--------|-----------|-----------|
| WebSocket 握手失败 | `classifyStartError` | `failed` | `handshake_failed` | — |
| TLS / 连接失败 | `classifyStartError` | `failed` | `server_down` | — |
| 网络断开（有字节） | 渠道关闭 | `completed` | — | `ndt7_completed` |
| 网络断开（无字节） | `lastAppBytes == 0` | `failed` | `network_error` | `ndt7_no_application_bytes` |
| Context cancel | `ctx.Err()` | `cancelled` | `cancelled` | `ndt7_cancelled` |
| OverallTimeout | `ctx.Err()` | `timeout` | `timeout` | `ndt7_timeout` |
| 线级字节预算 | `registry.exceeded()` | `budget_exceeded` | `budget_exceeded` | `ndt7_budget_exceeded` |
| 没有无效数据 | `windowRejectReason` | `completed` | — | `ndt7_window_unavailable` |

**关键说明**：“网络断开”与“正常关闭”在客户端侧**无法区分**（见 5.2）。
因此两者都是 `completed` + `ndt7_completed`，而不是冒充“服务端 EOF”。

---

## 7. 测试矩阵

### 7.1 实测数量（以 `go test -list '^Test' ./internal/provider/ndt7/` 为准）

```
go test -list '^Test' ./internal/provider/ndt7/   → 60 个 Test* 函数
```

| 项 | 数量 |
|----|------|
| 测试函数（`package ndt7_test`，`runner_test.go`） | 25 |
| 测试函数（`package ndt7`，`window_internal_test.go`） | 3 |
| 测试函数（`package ndt7`，`serviceurl_internal_test.go`） | 6 |
| 测试函数（`package ndt7`，`locate_internal_test.go`） | 26 |
| **测试函数合计** | **60** |
| `t.Run` 子测试数 | 28 |

**不同层级分开计数，不混算**：54 个测试函数与 28 个子测试是不同的计数单位。

> WAN-01-NDT7-URL-FIX 与 WAN-01-NDT7-DISCOVER 两轮分别新增 6 与 20 个测试函数；WAN-01-NDT7-DISCOVER-ACCEPTANCE 审计再新增 6 个（见 16.8）。

### 7.2 测试列表

**内包测试（`package ndt7`，确定性，无时钟之争）：**

| # | 测试 | 验证项 |
|---|------|--------|
| 1 | `TestWindowRejectReasonCoversEveryDegenerateCase` | 窗口守卡的完整决策表：无帧 / 字节回退 / 时钟回退 / 零窗口 / 负窗口 / 优先级 / 健康窗口（8 个子测试） |
| 2 | `TestMbsFromBytesRefusesNonMeasurableInputs` | 零窗口 / 负窗口 / 零字节 / 1 MiB·s = 8.388608 Mbps / 1 MB·s / 10 Gbps / 子毫秒窗口（7 个子测试） |
| 3 | `TestMbsFromBytesNeverReturnsInfinityOrNaN` | 任何输入下结果必为有限且非负（属性测试） |

**外包测试（`package ndt7_test`，端到端）：**

| # | 测试 | 验证项 |
|---|------|--------|
| 4 | `TestRegistrationThroughRunner` | Runner 实现 Provider/ConsentHandler/BudgetReporter 全部接口 |
| 5 | `TestConsentGateBlocksEveryNetworkCall` | 无 consent 时 Negotiate/Discover/Run 全部 ErrConsentRequired |
| 6 | `TestNegotiateReturnsDocumentedCapabilityShape` | Negotiate 返回 Download/Upload/Latency，Jitter 不声称 |
| 7 | `TestRefusesMultiConnectionAndMultiDirection` | Connections > 1 拒绝；多方向拒绝；不受支持 direction 拒绝（3 个子测试） |
| 8 | `TestDownloadSuccessAndJsonShape` | 下载成功；Mbps > 0；JSON round-trip 无损 |
| 9 | `TestUploadSuccessAndServerConfirmedBytes` | 上传成功；mock 看到 ≥ 客户端字节数 |
| 10 | `TestHandshakeFailureClassifiesAsHandshake` | 握手失败 ErrorCode=handshake_failed，TransferredBytes=0 |
| 11 | `TestConnectionFailureDoesNotLeakGoroutines` | TCP 断开后 Active conn → 0 |
| 12 | `TestImmediateCancellationForceClosesUnderlyingSocket` | 50 ms 后 cancel，< 3 s 完成，StatusCancelled |
| 13 | `TestOverallTimeoutFiresBeforeSDKDeadline` | 100 ms Duration 触发 StatusTimeout，< 3 s 完成 |
| 14 | `TestWireBudgetStopsDownloadWithPartialStatus` | DownloadBytes=32 KiB 触发 StatusBudgetExceeded |
| 15 | `TestWireBudgetRejectsUploadBeforeBytesReachNetwork` | UploadBytes=32 KiB 触发 StatusBudgetExceeded |
| 16 | `TestMeasurementWindowSpansFirstToLastFrame` | Window 非空，且严格小于 fixture 总时长（窗口从首帧开始） |
| 17 | `TestMbpsFromApplicationBytesIsHonest` | Mbps 可由 `deltaBytes/windowUs` 精确重算；`ElapsedMs == WindowUs/1000`；窗口 delta < 累计值 |
| 18 | `TestResourceCleanupAfterRun` | 8 次 Run 后 mock.Active = 0 |
| 19 | `TestRunnerIsConcurrencySafeForSequentialRuns` | 并发 Run 不崩溃 |
| 20 | `TestStopReasonMappingsAreStable` | 完成运行 StopReason = `ndt7_completed`，且 Warnings 必须披露 SDK 限制 |
| 21 | `TestProviderImplementsConsentHandler` | 满足 provider.ConsentHandler 接口断言 |
| 22 | `TestDetailSchemaVersionIsPinned` | Detail.Format = `ndt7-client-result/v1`，Payload 非空 |
| 23 | `TestServerMeasurementFrameIsNotTerminationEvidence` | 先断言 fixture 产生服务端帧，再断言 StopReason 仍为中性值 |
| 24 | `TestRateNeverMixesClientAndServerCounters` | 公式可由客户端输入重算；客户端率与服务端率差异 > 1 Mbps；窗口起点不是 sentinel 服务端帧 |
| 25 | `TestWindowUnavailableEndToEnd` | 两种合法结果均满足不同不变量：窗口可用 → 可重算；窗口不可用 → 不发布 Mbps 且披露原因 |
| 26 | `TestCompletedStatusIsNeverAccompaniedByAFabricatedCause` | 正常关闭 / 异常断连均不声称未观测原因（2 个子测试） |
| 27 | `TestSDKDeadlineRunIsNotLabelledServerEOF` | 已观测的 timeout 不得被重标为 `server_eof` |
| 28 | `TestBudgetTruncationIsNotPresentedAsACompleteMeasurement` | `budget_exceeded` + `ErrorCode=budget_exceeded` + Completeness ≠ complete |

### 7.3 定窗口与无 skip 保证（FIX3 修复）

原 `TestWindowUnavailableWhenNoClientFrameArrives` 驱动一个在 SDK `UpdateInterval`（250 ms）内结束的 mock，
然后在平台实际发出了客户端帧时调用 `t.Skipf`。这使测试结果变成了机器负载的函数：
忙碌的 CI runner 会**静默跳过**，守卡失去覆盖而 suite 仍然绿。

修复方案：

1. 将退化情形（无帧 / 单帧 / 回退 / 零窗口）抽成纯函数 `windowRejectReason`，
   在**同包**测试中无条件断言（测试 #1）——这些情形本来就需要赶在 250 ms 间隔上
   才能触发，现在不再需要墙钟。
2. 端到端测试改为**接受两种合法结果并分别断言不变量**（测试 #25），
   而不再在“窗口存在”时跳过。
3. `TestRateNeverMixesClientAndServerCounters` 中的 `t.Skip` 也改为 `t.Fatalf`：流式 fixture 保证有服务端帧。

**现在 `internal/provider/ndt7/` 下无任何 `t.Skip` / `t.Skipf`。**

### 7.4 执行结果

| 命令 | 结果 |
|------|------|
| `go test -list '^Test' ./internal/provider/ndt7/` | 28 个测试函数 |
| `go test -count=60 -run TestWindowUnavailableEndToEnd` | 通过（无跳过） |

---

## 8. CI 变更

`.github/workflows/ci.yml`：

- **保留**四个 CI Job 的语义：
  - `test` matrix（ubuntu / windows / macOS）：保留所有现有步骤
  - `desktop`（windows-only）：保留所有现有步骤
- **保留** Node.js 24、Go 缓存、Race Detector、Node.js cache、Wails CLI v2.16.0
- **新增** `ndt7` Job：
  - 独立 module，cache-dependency-path 指向 `experiments/wan01a-poc/go.sum`
  - `go-version-file` 指向 `experiments/wan01a-poc/go.mod`
  - ubuntu-latest（与 PoC 一致）
  - 步骤：Checkout → Setup Go → `go test ./...` → `go test -race ./...`
  - 唯一保留的 NDT7 测试目标：**PoC**，因为生产 provider 已通过根模块的 `go test ./...` 覆盖

完整 jobs 列表：`test` / `desktop` / `ndt7`（5 个 jobs）。

---

## 9. 已知限制

### 9.1 SDK 强加的限制

1. **NDT7 不支持并发多连接**：测量必须使用单一 WebSocket 连接。`Connections > 1` 返回 `ErrConnectionsNotSupported`（不静默降级）。
2. **Byte budget SDK 不支持**：必须在 SDK 之上做 wire-level 包装（已实现 `trackedConn` / `registry`）。
3. **取消延迟**：活跃 I/O ~1ms；空闲 I/O 必须等待 SDK I/O deadline 7s（通过 `forceCloseAll` 强制关闭 socket 解决）。
4. **0 字节窗口**：在极速 loopback 上可能产生 0 字节窗口，速率 `Mbps == nil`（不冒充 0）。
5. **TCP RTTVar 不填 jitter**：`JitterMetric == nil`，RTTVar 仅在 native payload 的 `warnings` 中记录（如未来需要）。

### 9.2 未覆盖的 PoC 范围（保留给 WAN-01-B2+）

| 能力 | 状态 |
|------|------|
| Locate v2 服务发现 | **已实现**（WAN-01-NDT7-DISCOVER，见第 16 节）：Locate v2 `ndt/ndt7` 自动发现，含域名白名单与同意门 |
| 真实 M-Lab 公网测量 | **不执行**（PoC 离线） |
| TCPInfo.MinRTT 进 Latency metric | 当前 `LatencyMetric == nil`，原始值在 Detail.Payload；未来可填 `MetricTCPMinRTT` |
| TCPInfo.RTTVar 进 Warnings | 当前 RTTVar 仅在 SDK 观测层记录；如需可注入 `MethodRecord.Warnings` |
| 自动重试 | **不做**（NDT7 是单次测量） |
| 真实 Locate 调用 | 代码已就绪，**公网调用仍未执行**（本阶段禁止连公网） |
| GUI 注册 NDT7 provider | WAN-01-B2（任务约束：本次不改 GUI） |
| CLI `gospeed test --provider mlab-ndt7` 子命令 | WAN-01-B2（任务约束：本次不破坏 CLI） |

### 9.3 安全与合规（已核对官方文档，核验日期 2026-10-19）

以下条款均来自 M-Lab 官方页面，已逐字核对，非推测：

- AUP：<https://www.measurementlab.net/aup/>（页面标注“Last updated: July 15, 2025”）
- Develop：<https://www.measurementlab.net/develop/>
- Privacy：<https://www.measurementlab.net/privacy/>（当前版本 v5，May 3, 2026）

| 事项 | 官方原文要点 | 对 GoSpeed 的含义 |
|------|------|------|
| 测试频率（交互式） | “Interactive users must not run more than 40 tests per day.” | 用户手动发起的测速，每日不得超过 40 次 |
| 测试频率（自动） | “Automated software or hardware clients **should not run more than 4 tests per day**, and test times should be randomized” | GoSpeed 如果未来提供批量/自动化测速，每日上限为 **4 次**，且必须随机化起起时间 |
| 知情同意 | “measurements must be actively initiated based on informed consent”；“Clients are required to provide informed consent about collection of personal information” | 必须在测试前取得用户主动同意，且通知文案需链接到 M-Lab 隐私政策 |
| 公开数据 | “required that the resulting data to be shared”；“All data collected by M-Lab tests are available to the public without restriction under a No Rights Reserved Creative Commons Zero Waiver”；“there is no mechanism to exempt certain tests from this collection” | 可以不录入公共数据集，**不可以** | 
| 数据保留 | “All experiment data is retained indefinitely and published publicly” | 无删除 API，无法撤回 |
| 集成者身份 | AUP：“Integration developers…”；Develop FAQ：“Can commercial or closed source services use M-Lab? **Yes.** Anyone may use M-Lab's services as long as they follow the AUP and obtain user consent.” | **商业闭源客户端是被允许的**，无需开源发布客户端代码 |
| 租户名下约定 | AUP：“The software (both client and server) running the experiment will be released under an OSI approved license”；Develop FAQ：“the clients that use these server resources…are **not required to be open source**” | 实验及服务端需开源；**调用方客户端无开源要求** |
| 商业贡献 | AUP：“Integration developers with commercial applications need to contribute to the platform in some way through financial sponsorship or in-kind contributions. Contact M-Lab at hello@measurementlab.net” | **已知前提：商业集成需贡献（资金或实物）** —— 待与 M-Lab 确认具体形式 |
| 禁止行为 | AUP：“modify, falsify, misrepresent, or alter any test results”；“Offering compensation or other incentivizes to run a test is strictly prohibited” | 不得修改测试数据；不得以赏动用户运行测试为营利手段 |
| PII | “M-Lab does not allow the storage of personally identifiable information (PII) on the platform…Experiments may collect the public-facing IP address” | 只能采集公网 IP，不得上传其他个人信息 |
| Locate | Develop：“Locate API v2…new integrations should use the v2 API” | 已按 v2 接入（本阶段未实施，见 10.1） |

**待确认事项（未得得当作已确认事实）：**

1. GoSpeed 作为“OSS 集成方”提供 NDT7 入口，是否需要向 M-Lab 登记（AUP 仅说 M-Lab 可能要求注册并收取费用，未规定登记必要性）。
2. 商业贡献的具体形式与阈值。
3. 每日 4 次自动测试上限是否适用于“用户手动触发”的 GUI 场景（AUP 对交互式用户是 40 次/日，对自动客户端是 4 次/日，GoSpeed 属于哪一类需与 M-Lab 确认）。
4. 路由与带宽供应商使用 M-Lab 测速服务的额外条款。

**已纠正的旧表述（FIX2）：** 旧版本写“AUP 要求不可作为商业产品的隐含组件”。
核对 AUP 原文后确认：**AUP 并未包含该条款**。AUP 对商业使用的实际要求是“贡献平台”，
而不是禁止商业使用。当时的描述与官方文档不符，已删除。

---

## 10. WAN-01-B2 公网验证计划

### 10.1 前置条件

1. **向 M-Lab 邮件确认** OSS 集成合规（人工任务）
2. **桌面 GUI 注册 NDT7 Provider**：`desktop/providerbridge.go` 增加 opt-in 注册（需 GUI 同意）
3. **CLI 子命令**：增加 `gospeed test --provider mlab-ndt7`（需 CLI 改造）
4. **磁盘上持久化 ConsentRecord**：避免每次启动都要同意

### 10.2 公网验证步骤（人工执行）

```
1. 用户启动 GoSpeed GUI 或 CLI
2. UI 显示 "M-Lab NDT7 measurement disclosure v1" notice
3. 用户点击 "我同意"
4. ConsentRecord 持久化到磁盘
5. 用户选择 NDT7 provider
6. CLI/GUI 触发 Locate v2 Nearest 查询（这是首次网络活动）
7. 返回最近服务器列表（共享池或 priority 池）
8. 用户选择服务器（或自动选择）
9. Run 一次下载（默认 24 MiB wire budget）
10. Run 一次上传（默认 16 MiB wire budget）
11. 结果展示 + 持久化
12. 实验性 flag（如启用）允许 repeat，但**绝不自动**
```

### 10.3 验收清单

- [ ] M-Lab 邮件确认
- [ ] GUI 改造（desktop/providerbridge.go 注册 NDT7）
- [ ] CLI 改造（增加 subcommand）
- [ ] 持久化 ConsentRecord
- [ ] 实际公网 Run 一次（人工触发）
- [ ] 跨地区路径延迟（HK / TW / JP）测量
- [ ] ISP 流量消耗核对
- [ ] Result 持久化到磁盘
- [ ] SBOM 更新（MLab dependencies）

---

## 11. 变更清单

| 文件 | 状态 |
|------|------|
| `go.mod` | 修改（新增 gorilla/websocket + ndt7-client-go） |
| `go.sum` | 修改（依赖 lock） |
| `desktop/go.mod` | 修改（transitive gorilla/websocket bump） |
| `desktop/go.sum` | 修改（匹配） |
| `.github/workflows/ci.yml` | 修改（新增 ndt7 Job） |
| `internal/provider/ndt7/adapter.go` | **新增** |
| `internal/provider/ndt7/runner.go` | **新增** |
| `internal/provider/ndt7/mock/server.go` | **新增** |
| `internal/provider/ndt7/runner_test.go` | **新增** |
| `docs/WAN-01-B1-NDT7-Provider-工程化报告.md` | **新增**（本文件） |
| `.gitignore` | 修改（使该报告可被 Git 跟踪） |

### 11.1 WAN-01-B1-FIX2 修改的文件（本次）

| 文件 | 修改内容 |
|------|------|
| `internal/provider/ndt7/adapter.go` | 测量循环仅采用 client-origin 帧；删除 `LastFrameWasServerOrigin`；新增 `MeasurementSource`、`ServerOriginFrames`、`FirstAppBytes/LastAppBytes` 等；单调回退检测；StopReason 中性化 |
| `internal/provider/ndt7/runner.go` | 删除 `server_eof` 推断；新增 `StopReasonNDT7Completed` / `StopReasonNDT7WindowUnavailable`；预算超限补全 `budget_exceeded` ErrorCode；Warnings 披露 SDK 限制与来源差异；nativePayload 扩展 |
| `internal/provider/ndt7/runner_test.go` | 新增 7 个测试；加强 Mbps 可重算断言；修正 2 个编码了错误语义的断言 |
| `internal/provider/ndt7/mock/server.go` | 新增 `NewStreaming` / `minDuration`；上传循环配速；新增 sentinel 服务端帧作为 origin 标记 |
| `docs/WAN-01-B1-NDT7-Provider-工程化报告.md` | 修正 9.3 合规表述（已核对官方文档）；新增第 13 章 FIX2 修复记录 |

### 11.2 WAN-01-B1-FIX3 修改的文件（本次）

| 文件 | 修改内容 |
|------|------|
| `internal/provider/ndt7/adapter.go` | 抽出纯函数 `windowRejectReason`；**删除** `MbpsFromBytesForTest` 导出 |
| `internal/provider/ndt7/window_internal_test.go` | **新增**（`package ndt7`）：窗口守卡决策表 + `mbpsFromBytes` 边界，无时钟之争 |
| `internal/provider/ndt7/runner_test.go` | 重写窗口测试（移除 `t.Skipf`）；另一处 `t.Skip` 改为 `t.Fatalf`；删除依赖已删导出的测试 |
| `docs/WAN-01-B1-NDT7-Provider-工程化报告.md` | 统一基线校正；重写 1.2 / 3.1 / 5 / 6 / 7；新增第 14 章 FIX3 |

**未修改**：
- `internal/speedtest/`（HTTP 引擎）
- `internal/server/`（HTTP 服务端）
- `internal/provider/provider.go` / `model.go` / `registry.go`（Provider 接口）
- `internal/provider/gospeedhttp.go`（HTTP Provider 实现）
- `cmd/gospeed/`（CLI）
- `desktop/`（除 go.mod/go.sum 自动更新）
- `desktop/runner.go` / `desktop/providerbridge.go`（GUI 不改）
- `internal/netdiag/` / `internal/nodes/` / `internal/config/` / `internal/cli/`
- `experiments/wan01a-poc/`（保留为 focused PoC）
- 任何测速公式
- Release Workflow（无）

---

## 12. 风险清单

| # | 风险 | 等级 | 缓解 |
|---|------|------|------|
| R-1 | M-Lab AUP 不允许 OSS 集成 | **高** | WAN-01-B2 启动前确认 |
| R-2 | Locate v2 在 CI 环境失败 | 低 | PoC 离线；WAN-01-B2 真实环境 |
| R-3 | SDK v0.10.1 升级破坏 Adapter | 中 | go.mod pin v0.10.1；升级需额外 PoC |
| R-4 | 公网测量导致用户意外消耗流量 | **高** | wire-budget 24/16 MiB + UI 显式确认 |
| R-5 | 用户数据进入 M-Lab 公共数据集 | **高** | ConsentHandler 强制 opt-in |
| R-6 | 服务器仍发送数据，客户端已停止 | 中 | wire budget + 强 close；本地 loopback 测试通过 |
| R-7 | 中国大陆到 M-Lab 网络不佳 | **高** | HK / TW / JP 节点可能可用；P0-H 已记录 |
| R-8 | 流量消耗不受 SDK 控制 | 中 | trackedConn wire-level budget；明确告知非 ISP 计费 |
| R-9 | SDK 长时间占用 socket | 低 | wire budget 可缩短至 1s 触发 force-close |
| R-10 | Locate 返回非 TLS URL | 低 | allow list 强制 `wss://`（PoC 已有） |
| R-11 | SDK internal change breaks our hooks | 中 | PoC `experiments/wan01a-poc/` 是回归测试 |

---


---

## 13. WAN-01-B1-FIX2 审计与修复

基线 `188462d`（当前 HEAD；`aea0bd1` 仅包含 `experiments/wan01a-poc/`，不含 `internal/provider/ndt7/`）。
本节记录四类问题的根因、修复与测试证据。

### 13.1 StopReason 判定

#### 问题：把 `Origin=server` 当作终止依据

修前 `adapter.go` 的测量循环里有：

```go
if m.Origin == spec.OriginServer {
    lastFrameWasServerOrigin = true
}
```

之后 `runner.go` 用它推断终止原因：

```go
if r.Status == StatusCompleted && r.LastFrameWasServerOrigin {
    stopReason = StopReasonServerEOF   // "服务器清洗了流"
}
```

**这个推断是成立不置的，两个原因：**

1. **`Origin=server` 帧不是终止事件。** 直接读取 SDK 源码 `internal/download/download.go`：服务端的
   TextMessage 帧在测试**运行期间**就会不断发送（官方约每 250 ms 一帧），
   它们是进度上报，与测试如何结束无关。
2. **SDK 丢弃了终止错误。** `ndt7.go` 的 `collectData`：

   ```go
   func (c *Client) collectData(ctx context.Context, f testFn, conn websocketx.Conn, outch chan<- spec.Measurement) {
       inch := make(chan spec.Measurement)
       defer close(outch)
       go f(ctx, conn, inch)          // <== 返回值被丢弃
       for m := range inch { ... }
   }
   ```

   协议循环的 error 没有任何出口。客户端能看见的唯一事件是 **渠道关闭**。

因此 adapter 无法区分：SDK 内部的 `DownloadTimeout`(15s)/`UploadTimeout`(10s) 到期、
服务器主动关闭、还是异常断连。

#### 修复

改为**中性完成状态** + 明确记录原因未知：

- 删除 `Result.LastFrameWasServerOrigin` 字段与全部推断代码。
- 新增 `StopReasonCompleted = "ndt7_completed"`，正常结束统一使用。
- `StopReasonServerEOF` 常量**保留但不再产出**，并在注释中写明原因，避免消费者切换时编译不过。
- 每次完成都在 `Method.Warnings` 写入完整原因说明（引用 SDK `collectData` 的具体行为）。

#### 测试证据

| 测试 | 作用 |
|------|------|
| `TestServerMeasurementFrameIsNotTerminationEvidence` | 先断言 fixture 确实产生了服务端帧（163 帧），再断言 StopReason 仍为中性值 |
| `TestSDKDeadlineRunIsNotLabelledServerEOF` | 已观测到的 timeout 不得被重标为 `server_eof` |
| `TestCompletedStatusIsNeverAccompaniedByAFabricatedCause` | 正常关闭 / 异常断连两种终止形态均不得声称未观测的原因 |
| `TestStopReasonMappingsAreStable` | 锁定中性值，并断言 Warnings 必须披露 SDK 限制 |

**变异测试（mutation）：** 将 `result.StopReason = StopReasonCompleted` 改回
`if serverFrameSeen { result.StopReason = StopReasonServerEOF }` 后，上述 2 个测试立即失败。

### 13.2 测量来源一致性

#### 问题：首尾帧可能混用客户端与服务端计数器

修前的循环对**任意**带 `AppInfo` 的帧更新首尾值：

```go
if m.AppInfo != nil {            // 不区分 Origin
    if !firstSeen { firstSeen = true; firstAppBytes = m.AppInfo.NumBytes; ... }
    lastAppBytes = m.AppInfo.NumBytes
}
```

NDT7 协议中两种 `AppInfo` 是**不同量**：

| Origin | NumBytes | ElapsedTime |
|-------|----------|-------------|
| `client` | 客户端自计的应用层字节（下载 = 已收到；上传 = 已发出） | 客户端时钟测量的累计微秒 |
| `server` | **服务端**自计的字节（对下载是已发出，对上传是已收到） | **服务端**时钟的累计微秒 |

两者是不同的计数器 + 不同的时钟。拿客户端帧的首值减服务端帧的尾值，
得到的不是速率，而是“一个方的字节数 ÷ 另一方的秒数”。

#### 修复

- 测量窗口只采用 **`spec.OriginClient`** 帧；服务端帧移出窗口。
- 新增字段记录但不参与计算：`ServerOriginFrames` / `ServerFrameSeen` / `ServerAppBytes` / `ServerElapsedUs`。
- 新增 `MeasurementSource`（`ndt7_client_appinfo` / `ndt7_unavailable`）明确声明采用哪一个 origin。
- **单调返回检测**：累计字节数或累计时钟任一回退，则拒绝形成窗口（`counterRegressed` / `timeRegressed`）。
- 拒绝因写入 `WindowRejectReason`，StopReason 变为 `ndt7_window_unavailable`。
- 新增 `FirstAppBytes/FirstElapsedUs/LastAppBytes/LastElapsedUs`，全部进入 nativePayload，使消费者可独立重算公式。

#### 最终 Mbps 公式

```
Mbps = (LastAppBytes - FirstAppBytes) * 8
       / ((LastElapsedUs - FirstElapsedUs) / 1e6) / 1e6
```

分子与分母**同源**：均为 `Origin=client` 帧的 `AppInfo.NumBytes` 与 `AppInfo.ElapsedTime`。
`windowUs <= 0` 或 `counterRegressed` / `timeRegressed` 时，`Mbps` 不发布（runner 转为 `nil`）。
正值零字节但时长正数的窗口属于**合法结果**，发布 `Mbps = 0`。

#### 测试证据

| 测试 | 作用 |
|------|------|
| `TestRateNeverMixesClientAndServerCounters` | 断言公式可由 `DeltaBytes/WindowUs` 精确重算；断言客户端率与服务端率差异大于 1 Mbps（否则无法区分来源）；断言窗口起点不是 mock 的 sentinel 服务端帧 |
| `TestMbpsFromApplicationBytesIsHonest` | 新增断言：公式可重算、`ElapsedMs == WindowUs/1000`、窗口 delta **<** 累计值 |
| `TestWindowUnavailableEndToEnd`（FIX3 重写） | 无跳过地断言两种合法结果：可重算的速率，或不发布 Mbps 并披露原因 |
| `TestWindowRejectReasonCoversEveryDegenerateCase`（同包） | 窗口守卡完整决策表，无时钟之争 |
| `TestMbsFromBytesRefusesNonMeasurableInputs`（同包） | 零/负窗口、零字节、1 MiB/s = 8.388608 Mbps 等 |
| `TestMbsFromBytesNeverReturnsInfinityOrNaN`（同包） | 任意输入下结果必为有限非负 |

**变异测试：** 让服务端帧也参与窗口计算后，**8 个测试失败**，包括专项的
`TestRateNeverMixesClientAndServerCounters`。

#### 测试夹具修正（必要的同步修复）

修正原因不只在被测代码：原 mock `download()` 只发 16 个 16 KiB 块、平均延迟 5 ms，
整体约 80 ms，**短于 SDK 的 250 ms `UpdateInterval`**。即 SDK 一个客户端帧都不会发出。
旧测试能通过，恰好是因为它们测的是**服务端帧**——即本次修复的混用问题。

mock 新增 `NewStreaming(scenario, bytesTarget, chunkDelay, minDuration)`：保持传输至少 `minDuration`。
测试用 `streamDuration = 900ms`（> 3 倍 UpdateInterval）保证至少 2 帧。
mock 另在首个二进制块之前发送一帧 `NumBytes=1` 的 sentinel 服务端帧
（符合 ndt-server 启动即上报的行为），作为 origin 混用的确定性标记：SDK 不可能在读到完整
二进制消息前发出客户端帧，故该值不会与任何客户端样本相等。

### 13.3 预算状态语义

#### 问题：预算提前终止的 ErrorCode 为空

修前：预算超限时 adapter 设 `ErrorClass = ErrorNone`（属“有计划的停止”而非协议失败），
`errorCodeForRunError` 对 `ErrorNone` 返回 `""`。结果是 `status=budget_exceeded` 但 `errorCode` 为空字符串——
消费者无法识别为何被截断。

#### 契约证据：Provider 不存在 `StatusPartial`

任务要求“按既定契约标记为 StatusPartial”。核对 `internal/provider/model.go`：

```go
type MeasurementStatus string
const (
    StatusCompleted      MeasurementStatus = "completed"
    StatusFailed         MeasurementStatus = "failed"
    StatusCancelled      MeasurementStatus = "cancelled"
    StatusTimeout        MeasurementStatus = "timeout"
    StatusBudgetExceeded MeasurementStatus = "budget_exceeded"
    StatusConsentNeeded  MeasurementStatus = "consent_required"
)
```

`MeasurementStatus` 共 6 个值，**不存在 `partial`**。“部分完成”的概念在
`Quality.Completeness` 中表达（`provider.Partial`），而不在 `Status` 中。
若新增 `MeasurementStatus("partial")` 会破坏 HTTP Provider 与 GUI 共用的契约。
**因此不新增状态值**，保持契约原有映射，仅修复 ErrorCode。

#### 修复

```go
if r.BudgetExceeded && result.ErrorCode == "" {
    result.ErrorCode = "budget_exceeded"
}
```

与 HTTP Provider 的现有语义一致（`gospeedhttp.go` 也返回 `StatusBudgetExceeded` + `budget_exceeded`）。

#### 测试证据

`TestBudgetTruncationIsNotPresentedAsACompleteMeasurement`：断言 `Status == StatusBudgetExceeded`、
`Status != StatusCompleted`、`ErrorCode == "budget_exceeded"`、`ErrorMessage != ""`、
`Quality.Completeness != Complete`、`StopReason == ndt7_budget_exceeded`、native payload 记录 `budgetExceeded=true`。

### 13.4 最终状态映射

| 触发条件 | Status | StopReason | ErrorCode | Completeness |
|----------|--------|-----------|-----------|--------------|
| 正常完成，窗口成立 | `completed` | `ndt7_completed` | — | `complete` |
| 有字节但无法形成窗口 | `completed` | `ndt7_window_unavailable` | — | `complete` |
| 客户端零字节 | `failed` | `ndt7_no_application_bytes` | `network_error` | `incomplete` |
| 线级预算超限 | `budget_exceeded` | `ndt7_budget_exceeded` | `budget_exceeded` | `partial` |
| 用户取消 | `cancelled` | `ndt7_cancelled` | `cancelled` | `incomplete` |
| OverallTimeout 到期 | `timeout` | `ndt7_timeout` | `timeout` | `incomplete` |
| WebSocket 握手失败 | `failed` | — | `handshake_failed` | `incomplete` |
| 未同意 | `consent_required` | — | `consent_required` | `incomplete` |

`server_eof` 与 `duration_elapsed` **已不再由本 adapter 产出**，原因见 13.1。

### 13.5 测试统计（实测，无补齐项）

```
go test -list '^Test' ./internal/provider/ndt7/   → 26 个 Test* 函数
```

基线 `188462d` 为 19 个测试函数，本次新增 7 个，**无任何删除或降级**（已通过名称比对确认）。
后续 FIX3 又新增内包测试 3 个、重写 1 个，当前合计 **28 个测试函数**（详见 7.1）。
`TestRefusesMultiConnectionAndMultiDirection` 含 3 个 `t.Run` 子测试。

本次新增：`TestServerMeasurementFrameIsNotTerminationEvidence`、`TestRateNeverMixesClientAndServerCounters`、
`TestWindowUnavailableWhenNoClientFrameArrives`（FIX3 已更名为 `TestWindowUnavailableEndToEnd` 并重写）、
`TestCompletedStatusIsNeverAccompaniedByAFabricatedCause`、`TestSDKDeadlineRunIsNotLabelledServerEOF`、`TestBudgetTruncationIsNotPresentedAsACompleteMeasurement`。
原 `TestZeroWindowIsNeverPublishedAsARate` 依赖一个为测试而导出的生产函数 `MbpsFromBytesForTest`；FIX3 已删除该导出，测试迁移到同包的 `TestMbsFromBytesRefusesNonMeasurableInputs` / `TestMbsFromBytesNeverReturnsInfinityOrNaN`（见 7.2）。

### 13.6 完整验证结果

| 命令 | 结果 |
|------|------|
| `go test ./...` | 通过 |
| `go vet ./...` | 通过，无告警 |
| `go test -race ./...` | 通过，无 data race |
| `go test -count=20 ./internal/provider/ndt7/` | 通过（149 s） |
| `experiments/wan01a-poc`: test / vet / race | 全部通过 |
| `desktop`: test / vet | 通过 |
| 交叉构建 windows / linux / darwin amd64 | 全部成功（仅交叉编译，未在对应平台实际运行） |
| `git diff --check` | 通过（仅 CRLF 提示，无空白冲突） |

### 13.7 剩余风险

1. **终止原因无法区分**（SDK 限制，无法在不修改 SDK 的前提下解决）。
2. **客户端字节数与平台传输量不等价**：`ApplicationBytes` 不含 WebSocket/TLS 帧头与内核缓冲。
3. **上传未经服务端确认**：发布的是客户端已发字节，服务端 TCPInfo 仅作窗口外的旁证记录。
4. **同步延迟依赖墙钟**：测试用 900 ms 流式 fixture 保证多于 2 倍 250 ms UpdateInterval，非碰运气。
5. **公网验证未完成**：本阶段不连接 M-Lab，13.1–13.4 的结论均基于本地 mock 与 SDK 源码审计。
6. **合规事项 4 项待确认**（见 9.3）。

---


---

## 14. WAN-01-B1-FIX3 提交前收尾

### 14.1 修复了什么

| 问题 | 位置 | 修复 |
|------|------|------|
| 测试结果依赖机器速度 | `runner_test.go` `TestWindowUnavailableWhenNoClientFrameArrives` | 重写为 `TestWindowUnavailableEndToEnd`，接受两种合法结果并分别断言不变量，**不再跳过** |
| 退化情形只能靠赶时钟触发 | `adapter.go` | 抽出纯函数 `windowRejectReason`，在同包测试中无条件断言 |
| 测试专用生产 API | `adapter.go` `MbpsFromBytesForTest` | **删除**，测试迁移到同包直接调用 `mbpsFromBytes` |
| 报告内容与实现矛盾 | 本文档 | 重写 3.1 / 5 / 6 / 7，统一基线与测试计数 |

### 14.2 测试修复前后对比

| | 修复前 | 修复后 |
|---|--------|--------|
| 测试函数 | 26 | **28**（+3 同包确定性，原 1 个重写） |
| `t.Skip` / `t.Skipf` | 2 处 | **0** |
| 退化情形覆盖 | 依赖机器速度，可能跳过 | 表驱动，**无时钟之争**（8 个子测试） |
| 生产 API | 含 `MbpsFromBytesForTest` | 该导出已删除 |
| 窗口守卡测试 | 仅端到端一条 | 端到端 + 完整决策表 |

### 14.3 通过写法上的改动

- 窗口守卡从 `Run()` 内联的 switch 抽为纯函数 `windowRejectReason`，**行为不变**，
  仅使其可在无网络、无时钟条件下被完整覆盖。
- `MbpsFromBytesForTest` 删除。它只是为了让外包测试能调用一个未导出函数而存在的生产函数。
  两个位于**同包**的新测试直接调用 `mbpsFromBytes`，覆盖范围不减少。
- 新增测试文件 `window_internal_test.go`（`package ndt7`）。测试函数因此不再会为了
  测试而扩大生产 API。

### 14.4 通过证据补充的一处实现细节

新增属性测试 `TestMbsFromBytesNeverReturnsInfinityOrNaN` 时发现：`mbpsFromBytes(-1, time.Second)`
会返回负数。这在生产中不可达（`Adapter.Run` 在调用前已拒绝 `deltaBytes < 0`），
但为了让测试通过而在生产函数里加一个防御性 clamp，属于**为测试改生产语义**，故未采用。
测试改为只覆盖生产中真实可能达到的输入集，并在测试注释中说明该保证实现在哪一层。


## 15. 是否建议提交

**建议提交。**依据：

- 28 个测试函数通过（另含 6 个子测试）；`-count=20` 重复通过；Race Detector 无报告。
- 测试结果不再依赖机器速度：`internal/provider/ndt7/` 下 **0 处 `t.Skip` / `t.Skipf`**，且本次 diff **未新增任何 `time.Sleep`**（仓库中现有的 3 处 Sleep 均为 HEAD 已有的轮询等待循环）。
- 生产 API 未因测试扩张：`MbpsFromBytesForTest` 已删除。
- 报告内容与实现一致：StopReason、Status/ErrorCode、测量字段、基线 commit 已全部核对。
- 未修改测速公式、HTTP Provider、GUI、CLI、Release Workflow、NET-01 / RC-01 / RC-02。
- 本阶段未连接 M-Lab 公网服务器，全部测试使用本地 mock。

**等待人工验收**，未自动 commit / push / tag / release。

---

## 16. WAN-01-NDT7-DISCOVER：Locate v2 自动发现

- 任务编号：WAN-01-NDT7-DISCOVER
- 基线 commit：`d99a091`（`fix(wan): normalize NDT7 service URLs`）
- 范围：实现 `internal/provider/ndt7` 的 `Discover()`，通过 M-Lab Locate API v2 自动发现可用测速节点
- 状态：实现完成，**未连接 M-Lab 公网**，全部测试使用本地 TLS 测试服务器

### 16.1 实现方式

新增文件 `internal/provider/ndt7/locate.go`，修改 `internal/provider/ndt7/runner.go` 的 `Discover()`。

| 环节 | 实现 |
|------|------|
| 协议 | Locate v2 `nearest` 查询，**单次 GET，不重试** |
| 服务名 | `ndt/ndt7` |
| 请求 URL | `https://locate.measurementlab.net/v2/nearest/ndt/ndt7` |
| User-Agent | `GoSpeed/<version> ndt7-client-go/0.10.1`（复用 SDK 的 `MakeUserAgent`，非空） |
| 取消/超时 | `context.WithTimeout`（默认 `DefaultLocateTimeout = 10s`） |
| 响应类型 | 官方 `v2.NearestResult` / `v2.Target` / `v2.NextRequest` |
| 依赖 | `github.com/m-lab/locate v0.19.0`（**由 indirect 提升为 direct，版本未变，go.sum 无变化**） |

**为何不直接使用官方 `locate.Client.Nearest()`**：该方法把响应解析为 `[]v2.Target` 并**丢弃 `next_request` 字段**。而 Locate v2 官方文档写明客户端 *SHOULD NOT* 比 `NextRequest` 更频繁地发起请求，且服务端 *MAY reject requests indefinitely*。要遵守这条限制就必须读到该字段，因此本实现自行发起这一次 GET，并复用官方的 URL 拼接规则（把服务名 join 到 base path）、官方的响应类型与官方的响应体大小上限（512 KiB）——**协议本身没有重复实现**。

### 16.2 完整服务 URL 的保留策略

Locate v2 返回的服务 URL 已包含协议、主机、路径与鉴权参数：

```
wss://ndt-mlab3-hkg03.mlab-oti.measurement-lab.org/ndt/v7/download?access_token=<JWT>&index=0&locate_version=v2
```

- **整体保留**：`provider.Node.Endpoint` 存放 Locate 发出的**完整 URL**，包含 `access_token` 与全部 query 参数。
- **不重新拼接**：绝不由 hostname 重新构造地址（会丢掉凭据）。
- **不重编码**：不做 decode/re-encode，不移除、不替换任何鉴权参数。
- **方向由 Direction 决定**：`adapter.serviceURL()` 负责把路径规范化为 `/ndt/v7/download` 或 `/ndt/v7/upload`（WAN-01-NDT7-URL-FIX 的成果），query 原样保留，因此同一个 Endpoint 对两个方向都可用。
- **凭据不外泄**：token 不进入 `Node.ID`、`Node.Name`、错误消息或任何结果（`provider.FinalResult` 本就不含 endpoint）。

### 16.3 节点安全校验

每个候选必须**全部**通过下列检查，否则被丢弃（`maxLocateURLsPerTarget = 16` 防止异常响应）：

| 检查 | 规则 | 依据 |
|------|------|------|
| URL key | 必须存在 `wss:///ndt/v7/download` **与** `wss:///ndt/v7/upload` | 官方 SDK 按 `scheme + "://" + path` 取键 |
| 协议 | 必须为 `wss` | 测量数据公开发布，不得明文 |
| userinfo | 必须为空 | URL 内不得嵌凭据 |
| fragment | 必须为空 | 拒绝异常构造 |
| 端口 | 必须为空或 `443` | 非标准端口不得绕过主机名检查 |
| 主机名 | 必须在可信后缀内 | 见下 |
| 路径 | 必须精确等于该方向的 NDT7 路径 | 官方 SDK 逐字节比较 |

**可信后缀**：`measurement-lab.org`、`measurementlab.net`。

**严格边界匹配**（`hostAllowed`）：大小写不敏感、容忍单个结尾点（FQDN 形式），但强制 DNS 标签边界——`evilmeasurement-lab.org` 不匹配 `measurement-lab.org`，`measurement-lab.org.evil.example` 也不匹配。归一化后主机名必须只含 `[a-z0-9.-]`；控制字符、Unicode 仿冒字符一律拒绝，IP 字面量（`127.0.0.1`、`::1`）也不通过（字面地址绝不应满足可信主机名后缀）。

该逻辑与 `experiments/mlab-poc/provider/locate.go` 中已审计的实现同源；因 `experiments/mlab-poc` 是**独立 Go 模块**、无法被根模块 import，故在 `internal/provider/ndt7` 内按同一规则重写并配以上述表驱动测试。

### 16.4 用户同意要求

- `Discover()` 的**第一步**就是同意门检查，未同意直接返回 `ErrConsentRequired`，**不构造任何 HTTP 请求**（测试用请求计数断言了这一点）。
- 同意由 `provider.ConsentHandler.RecordConsent` 记录，且 `PolicyVersion` 必须等于当前 `consentVersion`；旧版本或未同意的记录一律拒绝。
- **本轮把 `consentVersion` 从 `ndt7-notice-v1` 提升为 `ndt7-notice-v2`**：发现路径现在真的会向 M-Lab 发出外部请求，而 Locate 查询本身就会把客户端公网 IP 暴露给 M-Lab 与路径上的每一台路由器。第 4 条告知文本已据此改写，**旧版本号的已存同意不再能静默复用**（这正是版本号存在的意义）。
- **发现节点 ≠ 授权测速**：`Discover()` 只返回地址，不启动下载、上传或任何高流量操作；真正的测速仍需 `Run()` 走同一道同意门与预算。
- 定位查询被明确视为外部网络请求：`PRIVACY.md` 第 3 节已写明「未同意时：不执行 M-Lab 节点查询（Locate），不建立任何测速连接，零网络活动」。

### 16.5 错误处理与限流策略

| 场景 | 行为 |
|------|------|
| 未同意 | `ErrConsentRequired`，零网络活动 |
| Locate 返回空结果 | `ErrNoLocateServers` |
| 全部候选校验失败 | `ErrNoValidLocateServer`（附带被拒数量，不回显主机名） |
| 部分候选无效 | 返回有效候选，无错误 |
| Locate 请求失败 / 超时 / 取消 | 原样包装返回（`context.Canceled` / `context.DeadlineExceeded` 可用 `errors.Is` 匹配） |
| Locate 返回重定向 | **`ErrLocateRedirect`，拒绝跟随**（见 16.8） |
| 凭据过期 | 不缓存，返回可诊断错误 |

**限流与缓存**：

1. 两次 Locate 查询之间的最短间隔 `DefaultDiscoveryCacheTTL = 5 分钟`，因此调用方每次刷新 UI 不会都发出请求。
2. 服务端返回的 `next_request.nbf` 被采纳：下一次查询的时间取 `max(本地 TTL, 服务端 nbf)`。
3. 缓存有效期取 `min(max(本地 TTL, 服务端 nbf), access_token 过期时间 - 60s)`——**绝不缓存过期 token**；token 的 `exp` 声明从 JWT 载荷读取（不验签，仅用于判断可用性）。token 无法解析时**完全不缓存**。
4. 缓存条目已不可用但服务端要求继续等待时，返回 `ErrLocateRateLimited` 并给出允许再次查询的时间——**宁可报错，也不违反服务端公布的限流**。

### 16.6 测试覆盖（离线）

新增 `internal/provider/ndt7/locate_internal_test.go`，**20 个测试函数 / 22 个子测试**，全部离线：

- 使用 `httptest.NewTLSServer` 作为本地 Locate 端点，**不访问任何公网**；
- token 为本地构造的假 JWT，**不使用真实凭据**；
- **无 `time.Sleep`**，取消与超时用例通过服务端阻塞至 `r.Context().Done()` 触发，不靠碰运气。

| # | 需求 | 测试 |
|---|------|------|
| 1 | 发现单个节点 | `TestDiscoverReturnsOneNode` |
| 2 | 发现多个节点 | `TestDiscoverReturnsSeveralNodes` |
| 3 | 正确使用 `ndt/ndt7` | `TestDiscoverQueriesTheNDT7Service`（断言请求路径 `/v2/nearest/ndt/ndt7` 与非空 UA） |
| 4 | 保留完整 download URL | `TestDiscoverKeepsTheCompleteServiceURL` |
| 5 | 保留 `access_token` 与其他 query | 同上 + `TestDiscoveredNodeDrivesBothDirections` |
| 6 | 正确规范化下载/上传路径 | `TestDiscoveredNodeDrivesBothDirections` |
| 7 | 空结果 | `TestDiscoverRejectsAnEmptyAnswer` |
| 8 | Locate 请求失败 | `TestDiscoverReportsALocateFailure` |
| 9 | Context 取消 | `TestDiscoverHonoursContextCancellation` |
| 10 | 请求超时 | `TestDiscoverHonoursTheRequestTimeout` |
| 11 | 非法 URL | `TestDiscoverRejectsInvalidCandidates/unparseable_url` |
| 12 | 非 wss 服务地址 | `.../not_encrypted` |
| 13 | 非白名单域名 | `.../host_outside_the_trusted_list` |
| 14 | 伪造域名后缀 | `.../forged_suffix` + `TestHostAllowedRejectsForgedSuffixes` |
| 15 | 缺失下载或上传 URL | `.../missing_upload_url` |
| 16 | 部分无效、其余有效 | `TestDiscoverKeepsTheValidCandidates` |
| 17 | 全部无效 | `TestDiscoverFailsWhenEveryCandidateIsInvalid` |
| 18 | 未同意时零外部请求 | `TestDiscoverSendsNothingWithoutConsent`（三种未同意状态） |
| 19 | 不泄露 `access_token` | `TestDiscoverDoesNotLeakTheAccessToken` + 各拒绝用例 |
| 20 | 结果可直接驱动 Adapter | `TestDiscoveredNodeDrivesBothDirections` |

附加用例：`TestDiscoveryReusesTheCachedAnswer`、`TestDiscoveryDoesNotCacheAnExpiredToken`、`TestDiscoveryHonoursTheServerSchedule`、`TestAccessTokenExpiryReadsTheClaim`、`TestDiscoverDoesNotStartAMeasurement`。

**变异有效性已验证**（避免"空变异抓不到 bug"）：

| 变异 | 结果 |
|------|------|
| `serviceURL` 改回"追加"语义（即 URL-FIX 修掉的原缺陷） | `TestDiscoveredNodeDrivesBothDirections`、`TestServiceURLNormalisesThePath` **FAIL** |
| 域名后缀匹配去掉标签边界 | `TestHostAllowedRejectsForgedSuffixes/evilmeasurement-lab.org`、`TestDiscoverRejectsInvalidCandidates/forged_suffix` **FAIL** |
| 移除 `Discover()` 的同意门 | `TestDiscoverSendsNothingWithoutConsent` **FAIL** |

### 16.7 尚未解决的限制

1. **公网调用未执行**：本阶段禁止连 M-Lab。代码路径与真实公网行为的一致性仍需在 WAN-01-B2 的公网验证中确认（尤其是 Locate 返回的真实 URL 形态与 token 时效）。
2. **官方客户端未直接复用**：见 16.1。协议未重复实现，但确实没有调用 `locate.Client.Nearest()`；若上游将来暴露 `NextRequest`，应改回官方客户端。
3. **服务端缓存限制的可观测性有限**：官方客户端丢弃响应头，因此只能读到 `next_request` 字段，读不到 HTTP `Cache-Control`；本实现的 TTL 取保守值。
4. **token 时效按 JWT `exp` 判断**：不验签，只用于判断可用性。若 M-Lab 将来改用非 JWT 凭据，缓存会自动退化为「不缓存」（安全侧失败）。
5. **未接入 CLI / GUI**：`mlab-ndt7` 仍未注册进 Provider Registry，GUI 中看不到 M-Lab 节点。属下一阶段范围。
6. **节点数量由服务端决定**：Locate v2 一次返回若干候选，本实现不截断。

### 16.8 WAN-01-NDT7-DISCOVER-ACCEPTANCE 审计补记

源码级静态安全审计发现并修复 1 项问题，新增 6 个离线测试。

**问题：Locate HTTP 重定向未被限制。** Go 的 `http.Client` 默认跟随最多 10 次重定向。审计用本地两个 TLS 服务器复现：被指向的服务器返回 302 后，**第二个（未经校验的）服务器确实收到了请求**。这意味着一个 3xx 响应会让 GoSpeed 向一个从未通过信任门的主机发出额外请求——正是「不会因重试、重定向或隐藏调用而增加未经授权的外部请求」这条约束要排除的情况。

*影响*：Locate 请求本身**不携带凭据**（`access_token` 在响应里而不在请求里），因此重定向不会泄露 token；但它仍会把客户端 IP 暴露给重定向目标，并让「只有 M-Lab 官方端点会被查询」这一保证失效。

*修复*（最小、边界清晰）：`queryLocate` 复制 HTTP 客户端（不改动注入的客户端）并设置 `CheckRedirect` 一律返回新的哨兵错误 `ErrLocateRedirect`。重定向目标**不写入错误消息**——`Location` 头由对端控制，而该消息会进入 GUI。

*离线测试*：`TestDiscoverRefusesARedirect` 断言重定向被拒绝，**且重定向目标收到的请求数为 0**。变异验证：移除 `CheckRedirect` 后该测试立即失败。

**审计同时补齐的 4 项缓存/限流边界测试**：

| 测试 | 覆盖的边界 |
|------|-----------|
| `TestDiscoverReportsTheServerRateLimit` | 服务端要求等待、但缓存已不可用 → `ErrLocateRateLimited`，且**不发出新请求** |
| `TestDiscoveryDoesNotCacheAnUnreadableToken` | token 无法解析 → **完全不缓存**，下次必须重新查询 |
| `TestCancelledDiscoveryDoesNotFillTheCache` | 被取消的查询**不写入缓存**（不会留下半截答案） |
| `TestRunFailureDoesNotLeakTheAccessToken` | 真实拨号失败路径下，错误文本、`FinalResult.ErrorMessage` 与**序列化后的结果**均**不含 token** |
| `TestDiscoveredNodeIsNotSerialisedIntoAResult` | 记录「节点列表确实带 endpoint（拨号需要），但节点身份不带凭据」这一事实，供下一阶段脱敏 |

**审计发现但本阶段未修复的项**（需产品决策，见交付报告）：

1. `provider.Node` 的 JSON 形状**包含 `endpoint`**，而该 endpoint 在 M-Lab 场景下带 `access_token`。当前生产代码**从不序列化节点**，因此今天没有泄露；但下一阶段把节点列表送进 GUI 时，若直接 `json.Marshal` 就会把 token 送到前端。`provider.Node` 的 JSON 形状是既有契约（`TestNodeSplitsOwnershipFromDiscovery` 断言往返一致），改它属契约变更，超出本阶段范围。
2. `PRIVACY.md` 第 3 节承诺「每次实际执行公网测速前还会进行**单次确认**，确认不跨测速记忆」，但生产 `internal/provider/ndt7` 只有持久化的 `ConsentRecord`，**没有单次确认机制**（该机制只存在于实验模块 `experiments/mlab-poc`，且其实验告知文本写的是「没有单次测试豁免机制」）。GUI 尚未实现，本阶段按要求**只记录缺口，不描述为已完成**。
3. 同意告知与隐私政策均未提及 AUP 的每日测试上限（交互式 ≤ 40 次/日、自动化 ≤ 4 次/日），也未说明 GoSpeed 本地保存的同意记录与发现缓存。
### 16.9 WAN-01-NDT7-CONSENT-BOUNDARY 授权边界

本节关闭 16.8 列出的三项缺口（其中第 1 项以「新增 GUI DTO」的方式解决，`provider.Node` 的既有 JSON 契约保持不变），并按 M-Lab AUP 增加每日测速上限。

#### 16.9.1 GUI 序列化边界（任务 A）

**新增 `desktop/providernodes.go`**：`ProviderNodeView` + `providerNodeStore`。

| 机制 | 说明 |
|------|------|
| 投影而非过滤 | GUI 收到的是 `ProviderNodeView`，其字段集**在类型层面就无法承载**凭据：没有 `Endpoint`、没有 URL、没有 query 字段 |
| `Ref` 句柄 | 由后端计数器铸造（`pnode-1`…），不含地址/名称/凭据；前端自造的值无法解析 |
| 受控映射 | `endpointFor(ref)` 是**唯一**产出 endpoint 的入口，仅后端可调；前端回传的只有 `ref` |
| 解析而非截断 | `Host` / `ServicePath` 由 `url.Parse` 推导；无法解析的 endpoint 得到**空值**，绝不返回字符串前缀 |
| 刷新即作废 | 每次 `publish` 整体替换（旧 token 短时效），旧 `ref` 立即失效 |
| `forget()` | 撤销同意或结束会话时立即丢弃内存中的 token |

**离线测试**（`desktop/providernodes_test.go`，9 项）：字段集反射断言（新增任何可承载凭据的字段即失败）、递归遍历序列化结果、恶意/畸形 endpoint、展示字段完整性、后端仍能取到完整 endpoint、伪造 `ref`、刷新作废、`forget`、`%+v`/`%#v` 格式化不泄露。

**已知残留**：`provider.Node` 的 `json:"endpoint"` 标签**未改动**（属既有契约，改动需另立契约变更）。生产代码目前无任何位置序列化 `provider.Node`；GUI 侧由 DTO 阻断。

#### 16.9.2 单次测速授权（任务 B）

**新增 `internal/provider/ndt7/authorization.go`**。两道门完全独立：

| | 持久化同意 `ConsentRecord` | 单次授权 `RunGrant` |
|---|---|---|
| 覆盖 | Locate 发现 | 一次具体测速 |
| 生命周期 | 跨运行保存 | 发出即绑定「节点 + 方向」，被一次 `Run` 消耗 |
| 入口 | `RecordConsent` / `NewRunnerWithConsent` | `AuthorizeRun(nodeID, direction)` |
| 缺失时 | `ErrConsentRequired` | `ErrRunAuthorizationRequired` |
| 不匹配 | — | `ErrRunAuthorizationMismatch` |

**执行入口**：`Runner.Run` 在**建立 socket 之前**强制两道门。GUI 是否显示对话框不参与判定——未调用 `AuthorizeRun` 的调用方零流量被拒。

**消费时机**：授权在**每日计数同一时刻**（连接建立前）消耗。此前所有前置拒绝（请求格式错误、ctx 已取消、超过每日上限、授权缺失/不匹配）都**不消耗**确认；此后无论成功、失败、超时、取消，确认都已用尽，重试必须重新确认。`RevokeRunAuthorization()` 用于「用户关闭确认框」——不调用也不影响安全性，但使拒绝可观测。

**错误码分离**：`consent_required` / `measurement_not_authorized` / `measurement_authorization_mismatch` / `daily_limit_reached` / `daily_limit_unavailable`，调用方无需解析错误字符串即可区分。

**离线测试**（`authorization_test.go`，11 项）：仅持久化同意 → 零流量且连接数为 0；缺授权连跑 3 次 → 连接数仍为 0；授权成功 1 次且连接数恰为 1，再次运行被拒且连接数不变；节点/方向不匹配 → `ErrRunAuthorizationMismatch` 且原授权仍有效；传输中被取消 → 授权不可复用；传输前被取消 → 零流量、零计数、确认保持；失败后不可复用；计数 API；非法授权请求；错误文本与 `%+v` 不含 token；被拒结果的序列化不含 token。

#### 16.9.3 每日测速上限（任务 C）

依据 M-Lab AUP「Automated software or hardware clients should not run more than 4 tests per day」。

**新增 `internal/provider/ndt7/dailylimit.go`**：

- `MaxDailyRuns = 4`，按**本地自然日**（`t.Format("2006-01-02")`，非 UTC）。
- `DailyRunStore` 接口：`Used(now)` 只读预检、`Reserve(now)` 原子占用。
- `FileDailyRunStore`：用户配置目录 `GoSpeed/ndt7-daily-runs.json`，**重启不清零**；临时文件 + rename 原子写；保留 8 天。
- `MemoryDailyRunStore`：测试与自选存储使用。
- **失败即关闭**：计数器不可读/不可写 → `ErrDailyRunLimitUnavailable`，拒绝测速，而不是无限制放行。文件损坏或版本不符时**不重置**，宁可拒绝。
- 文件只存日期与计数，**不含**任何 host/token。

**计数阶段**：在授权消费之后、adapter 建立之前——即「真正要开始传输」的那一刻。因此：请求非法 / 缺授权 / 超上限的请求**不计数**；已开始传输后失败或取消**仍计数**（服务器已记录）。

**并发语义**：`Used` 预检与 `Reserve` 之间存在竞争窗口，**以 `Reserve` 为准**。被 `Reserve` 竞争淘汰的运行**不退还名额**——上限宁可高估不可低估（`TestConcurrentReserveRaceRefusesAndKeepsTheSlot` 确定性覆盖）。

**离线测试**（`dailylimit_test.go` 14 项 + `dailylimit_internal_test.go` 2 项）：跨日分桶、本地日界线（UTC+8 固定时区）、重启后计数仍在、保留窗口内外的裁剪、损坏/异版本文件失败即关闭且**不重写原文件**、32 路并发原子计数、计数文件不含凭据、第 5 次运行被拒且连接数恰为 4、上限拒绝不消耗确认、未开始的运行不计数、计数器不可用则拒绝、8 路并发不超过上限。

#### 16.9.4 隐私告知统一（任务 D）

| 文件 | 变更 |
|------|------|
| `runner.go` `defaultConsentNotice` | 升为 `ndt7-notice-v3`：新增「两个独立决定」「每日 4 次上限」「本地保存内容」三项，原 4/5/6/7/8 顺延为 4/5/6/7/8/9 |
| `consentVersion` | `ndt7-notice-v2` → **`ndt7-notice-v3`**，旧版本同意记录不再打开授权门 |
| `PRIVACY.md` | 1.1 → **1.2**：§2 拆出 Locate 外部请求与 M-Lab 长期公开保留；§3 重写为 3.1 持久化同意 / 3.2 单次授权 / 3.3 每日上限 / 3.4 本地保存 / 3.5 取消拒绝撤销 |
| `experiments/mlab-poc/provider/consent.go` | `gospeed-mlab-notice-2` → **3**：明确写出**本实验不具备**单次确认与每日上限，请勿反复运行 |
| 新增 `notice_consistency_test.go` | 6 项：告知文本与被强制的上限互相锁定（含 `strconv.Itoa(MaxDailyRuns)`）、版本一致性、PRIVACY.md 事实核对、实验告知缺口声明、两个 sentinel 不得互相 `errors.Is` |

**未实现、已在文档中标注为缺口**：GUI 侧的单次授权对话框与每日剩余次数展示（`desktop` 目前未接入 `mlab-ndt7`，见下节）。

#### 16.9.5 进入 Registry / GUI 集成的前置条件

1. `desktop` 的 Registry 需注册 `mlab-ndt7`，且 `StartTest` 路径必须先调用 `AuthorizeRun`（用 `providerNodeStore.endpointFor(ref)` 取 endpoint，**不得**让前端回传带 token 的 endpoint）。
2. 撤销同意 / 结束会话时调用 `RevokeRunAuthorization()` 与 `providerNodeStore.forget()`。
3. GUI 必须展示 `ndt7-notice-v3` 全文，且在版本变化时重新征求同意。
4. 每日剩余次数可展示，但**不得**作为唯一防线——强制点在 `Runner.Run`。
5. `mlab-poc` 实验客户端仍不具备上述两项保护，不得用于替代产品路径反复测速。
