# GoSpeed（秒速）

[![CI](https://github.com/AbsoluteZero001/GoSpeed/actions/workflows/ci.yml/badge.svg)](https://github.com/AbsoluteZero001/GoSpeed/actions/workflows/ci.yml)

> 基于 Go 语言的跨平台网络测速与网络质量分析工具。
>
> A cross-platform network speed test and network quality analysis tool written in Go.

GoSpeed 不是"发一个 HTTP 请求算个除法"的演示程序。v0.2.0 提供一套
**可解释、可复现、可验证**的测速内核：真实字节计数、明确的测量窗口、
多连接并发、实时采样与描述性统计，以及绝不伪造任何指标的输出策略。

## 功能特性

### 已实现（v0.2.0）

- 多连接并发下载 / 上传：1..16 连接（端到端验证 1 / 4 / 8 / 16）
- 共享字节预算：`--max-bytes` 是整个阶段的总量，不随连接数放大
- 统一测量窗口：多连接速率 = 累计字节 / 共享窗口，绝不把连接时间或速率相加
- 连接级证据：每条连接的状态、字节数；上传还记录服务端确认字节数与错误信息
- 部分连接失败：输出结果 + `warnings` + 有效连接数；全部失败则直接报错
- 实时速率采样：默认 200 ms 可配置，使用真实单调时钟窗口计算瞬时速率
- 描述性统计：平均、中位、最小、最大、样本标准差（n-1）、变异系数
- HTTP RTT 延迟测试：多采样、min / avg / max、相邻样本抖动
- 自建测速服务端：`/health`、`/ping`、`/download`、`/upload`（已验证 16 路并发）
- CLI：TTY 下动态进度条，重定向时自动降级为普通文本；JSON 模式 stdout 是纯 JSON
- 结构化 JSON 结果：原始字节、原始耗时、测量窗口、采样序列、逐连接报告
- 安全默认：服务端默认只监听 `127.0.0.1`，不转发、不落盘、不暴露系统信息
- 生命周期控制：`context` 取消、阶段超时、零时长保护、无模拟数据
- 单元测试、并发测试、`go vet`、`-race`、GitHub Actions CI

### 未实现（Planned，正在规划中）

以下能力**尚未实现**，请勿当作已有功能：

- ICMP Ping / UDP 丢包测试（丢包率显示 `N/A`，不使用 HTTP 失败率冒充）
- TCP Connect RTT、负载延迟（loaded latency）、Bufferbloat 分析
- 四分位（IQR）与多次测速聚合（计划 v0.3.0）
- 多节点自动选择、公网公共测速节点（没有经过验证的公共节点）
- 完整的 Wails + Vue 3 桌面 GUI、Web UI、历史记录、CSV 导出
- 网络接口协商速率采集（且永远不会把协商速率当成实测吞吐）

## 开发状态

| 项目 | 状态 |
| --- | --- |
| 版本 | v0.2.0（多连接 + 实时采样 + 统计） |
| Go Modules | `module github.com/AbsoluteZero001/GoSpeed` |
| 本地验证环境 | `go1.27.2 windows/amd64`（`go.mod` 最低要求 Go 1.25） |
| 第三方依赖 | 无（仅标准库） |
| CI | Windows / Linux / macOS：`gofmt` 检查、`go vet`、`go test`、Linux `-race`、跨平台构建 |
| 测试状态 | `go test ./...`、`go test -race ./...` 全部通过 |

## 技术栈

- **语言**：Go（标准库优先：`net`、`net/http`、`net/http/httptrace`、`context`、
  `sync/atomic`、`io`、`encoding/json`、`crypto/rand`、`time`）
- **CLI**：标准库 `flag`，无第三方 CLI 框架
- **测速服务端**：标准库 `net/http`，固定路由，无开放代理
- **未来 GUI**：Wails + Vue 3 + TypeScript（当前阶段不引入，先保证测速内核独立可测）

## 项目结构

```text
GoSpeed/
├── cmd/
│   └── gospeed/
│       └── main.go            # 进程入口与信号处理
├── internal/
│   ├── cli/                   # 命令行：test / server / nodes / version
│   ├── config/                # CLI 与测试共享的默认配置
│   ├── nodes/                 # 节点模型、JSON 加载、校验、可用性检查
│   ├── server/                # 自建 HTTP 测速服务端
│   ├── speedtest/             # 测速引擎
│   │   ├── engine.go          #   选项校验、阶段编排、统一状态机
│   │   ├── download.go        #   多连接下载
│   │   ├── upload.go          #   多连接上传 + 服务端确认
│   │   ├── latency.go         #   HTTP RTT 采样
│   │   ├── transfer.go        #   共享预算、worker 调度、连接级报告
│   │   ├── sample.go          #   实时采样与描述性统计
│   │   ├── metrics.go         #   单位换算
│   │   ├── result.go          #   JSON 结果模型
│   │   └── errors.go          #   可判定错误
│   └── version/               # 版本与 User-Agent
├── configs/
│   └── nodes.example.json     # 节点配置示例（只包含本机回环节点）
├── docs/
│   ├── architecture.md        # 模块划分、并发模型、采样与统计
│   ├── benchmark.md           # 测速原理、单位、误差来源
│   └── roadmap.md             # 版本路线图
├── .github/workflows/ci.yml   # CI（格式化、vet、单测、race、构建）
├── go.mod
├── LICENSE                    # MIT
└── README.md
```

## 环境要求

- Go 1.25 或更高版本（本地开发使用 go1.27.2 验证）
- Windows / Linux / macOS（同一套代码，纯标准库，无 CGO 依赖）
- 运行本地回环测试不需要额外服务，使用 `gospeed server` 即可

## 安装方式

```bash
go install github.com/AbsoluteZero001/GoSpeed/cmd/gospeed@latest
```

```powershell
# Windows (PowerShell)
go build -o bin\gospeed.exe .\cmd\gospeed
.\bin\gospeed.exe version
```

```bash
# Linux / macOS
go build -o bin/gospeed ./cmd/gospeed
./bin/gospeed version
```

## 快速开始

终端 1，启动本地测速服务器（默认只监听 `127.0.0.1:8080`）：

```bash
go run ./cmd/gospeed server
```

终端 2，运行一次多连接测速：

```bash
go run ./cmd/gospeed test --connections 4 --duration 10s
```

## 多连接使用示例

```bash
# 单连接（v0.1.0 行为，结果字段保持兼容）
gospeed test --server local --connections 1 --duration 10s

# 4 / 8 / 16 连接
gospeed test --server local --connections 4 --duration 10s
gospeed test --server local --connections 8 --duration 10s
gospeed test --server local --connections 16 --duration 10s

# 限制整个阶段的传输总量为 64 MiB（所有连接共享该预算）
gospeed test --server local --connections 8 --max-bytes 67108864 --json
```

并发连接注意事项：

1. `--max-bytes` 是整个阶段的共享预算，不是每条连接的配额；
2. 结果中的 `connections` 是请求连接数，`active_connections` 是实际参与传输的连接数，
   `failed_connections` 是失败数；吞吐量只代表实际参与的连接；
3. 连接数越多不代表越准确：客户端 CPU、服务器容量、中间设备队列与拥塞控制
   都会影响结果；建议在相同窗口下对比 1 / 4 / 8 / 16 再决定对外报告的口径；
4. 有 `warnings` 的结果不是"干净结果"，脚本消费时应检查该字段。

## CLI 使用方法

```text
gospeed test [flags]      运行一次测速
gospeed server [flags]    启动本地测速服务端
gospeed nodes [flags]     查看节点列表 / 健康检查
gospeed version           查看版本
gospeed help              查看帮助
```

### gospeed test

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--server` | 第一个启用节点 | 节点 ID，或直接的绝对地址（如 `http://127.0.0.1:8080`） |
| `--config` | 自动查找 | 节点配置文件；默认依次查找 `configs/nodes.json`、`configs/nodes.example.json` |
| `--connections` | `1` | 并发连接数，范围 1..16；端到端测试覆盖 1 / 4 / 8 / 16 |
| `--duration` | `10s` | 下载 / 上传阶段的共享传输时长窗口 |
| `--timeout` | `30s` | 单个阶段的超时，必须大于 `--duration` |
| `--sample-interval` | `200ms` | 实时速率采样间隔 |
| `--max-bytes` | `0` | 阶段总字节预算；`0` 表示只按时长结束 |
| `--latency-samples` | `5` | HTTP RTT 采样次数 |
| `--latency-interval` | `100ms` | 采样间隔 |
| `--warmup` | `true` | 正式测量前发送一次 `/health` 预热并快速失败 |
| `--json` | `false` | 完整结果以 JSON 输出到 stdout（进度信息输出到 stderr） |

### gospeed server

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--addr` | `127.0.0.1:8080` | 监听地址；非回环地址会打印安全警告 |
| `--max-upload-bytes` | `1 GiB` | 单请求上传上限 |
| `--max-download-bytes` | `4 GiB` | 单请求下载上限 |
| `--max-download-duration` | `60s` | 单请求下载时长上限 |
| `--default-download-duration` | `10s` | 未指定 `bytes` / `duration_ms` 时的默认下载时长 |
| `--read-timeout` | `60s` | HTTP 读超时 |
| `--write-timeout` | `75s` | HTTP 写超时，必须大于最大下载时长 |

### gospeed nodes

```bash
gospeed nodes                     # 列出节点
gospeed nodes --check --timeout 3s # 对每个节点执行 GET /health
gospeed nodes --json              # JSON 输出
```

## 本地测速服务器

```bash
go run ./cmd/gospeed server --addr 127.0.0.1:8080
```

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/health` | 健康检查，返回 `{"status":"ok","service":"gospeed","version":"..."}` |
| `GET` | `/ping` | 极简响应（`204`），用于 HTTP RTT 测量 |
| `GET` | `/download?bytes=N` 或 `?duration_ms=N` | 流式下载不可压缩随机数据 |
| `POST` | `/upload` | 流式接收上传，返回实际收到的字节数与服务端耗时 |

安全说明：

1. 默认只监听 `127.0.0.1`，开发阶段不会意外向公网开放；
2. 只有固定路由，不存在 URL 转发、开放代理、文件读取等能力；
3. 上传数据直接丢弃，不落盘；上传与下载都有大小和时长限制；
4. 多个并发请求各自独立计数，共享的只有只读随机块；
5. 部署到公网节点前，请自行增加 HTTPS、反向代理、流量限制、认证与监控。

## 测速结果示例

以下输出均为 v0.2.0 在开发机上对**本机回环服务器**执行的真实结果
（2026-10-09）。回环数值只反映本机协议栈能力，**不代表公网带宽**。

### 人类可读输出（4 连接，1 秒窗口）

重定向输出时自动降级为普通文本；在交互式终端中同一行会显示
`[########------------] 40%` 形式的进度条：

```text
====================================
           GoSpeed v0.2.0
         Network Speed Test
====================================
Server:      127.0.0.1:18082 (custom)
Address:     http://127.0.0.1:18082
Protocol:    http
Connections: 4
Mode:        Local Loopback Test - does not represent internet bandwidth

Testing latency...
  HTTP RTT avg: 0.17 ms

Testing download...
download [########------------]  40%  current 26466.62 Mbps  average 26466.62 Mbps  4 conn  eta 600ms
download 25838.80 Mbps   3.01 GiB in 1.00 s   4 connection(s)

Testing upload...
upload   17362.55 Mbps   2.02 GiB in 1.00 s   4 connection(s)

====================================
Test ID:     gs-2d84cd0f2344f13d
Status:      completed
Server:      127.0.0.1:18082 (custom)
Address:     http://127.0.0.1:18082
Protocol:    http
Connections: 4 requested
Sampling:    200ms
Mode:        Local Loopback Test - does not represent internet bandwidth

Latency:  0.00 ms min / 0.17 ms avg / 0.55 ms max (5/5 http_rtt samples)
Jitter:   0.14 ms (mean absolute difference of consecutive HTTP RTT samples)
Packet loss: N/A (no packet level test is implemented)
Download: 25838.80 Mbps (3229.85 MB/s, 3.01 GiB in 1.00 s, 4 connection(s))
  Active:  4 of 4 connections (0 failed)
  Window:  shared_window_first_response_byte_to_last_body_byte (duration_elapsed)
  Samples: 4 samples, median 25231.64 Mbps, stddev 2229.51 Mbps (sample_n_minus_1), cv 8.68%
Upload:   17362.55 Mbps (2170.32 MB/s, 2.02 GiB in 1.00 s, 4 connection(s))
  Server confirmed 2.02 GiB (client sent 2.02 GiB)
  Active:  4 of 4 connections (0 failed)
  Window:  shared_window_first_body_write_to_last_server_confirmation (duration_elapsed)
  Samples: 4 samples, median 17312.97 Mbps, stddev 354.75 Mbps (sample_n_minus_1), cv 2.05%
====================================
```

### JSON 输出（另一次 4 连接、1 秒窗口的真实运行，截取关键字段）

```json
{
  "test_id": "gs-3046562d73a094be",
  "timestamp": "2026-10-09T08:17:22.240186Z",
  "status": "completed",
  "target": {
    "server_id": "custom",
    "server_name": "127.0.0.1:18082",
    "server_address": "http://127.0.0.1:18082",
    "protocol": "http",
    "local": true
  },
  "settings": {
    "phases": ["latency", "download", "upload"],
    "duration_ns": 1000000000,
    "timeout_ns": 15000000000,
    "max_bytes": 0,
    "connections": 4,
    "sample_interval_ns": 200000000,
    "latency_samples": 5,
    "latency_interval_ns": 100000000,
    "warmup": true
  },
  "latency": {
    "type": "http_rtt",
    "attempts": 5,
    "successful_samples": 5,
    "failed_samples": 0,
    "min_ns": 0,
    "average_ns": 120880,
    "max_ns": 604400,
    "jitter_ns": 151100,
    "packet_loss_percent": null
  },
  "download": {
    "bytes": 3274977259,
    "duration_ns": 999276400,
    "mbps": 26218.7899884356,
    "mb_per_second": 3277.34874855445,
    "connections": 4,
    "active_connections": 4,
    "failed_connections": 0,
    "measurement_window": "shared_window_first_response_byte_to_last_body_byte",
    "stop_reason": "duration_elapsed",
    "samples": [
      {
        "phase": "download",
        "timestamp": "2026-10-09T08:17:23.0464593Z",
        "elapsed_ns": 399276400,
        "bytes_transferred": 1357447168,
        "current_mbps": 27198.14480395035,
        "average_mbps": 27198.14480395035,
        "active_connections": 4
      }
    ],
    "statistics": {
      "samples": 4,
      "mean_mbps": 25972.75,
      "median_mbps": 26177.70,
      "min_mbps": 24337.45,
      "max_mbps": 27198.14,
      "stddev_mbps": 1333.83,
      "cv_percent": 5.14,
      "stddev_kind": "sample_n_minus_1"
    },
    "connection_reports": [
      { "index": 0, "state": "completed", "bytes": 824905721, "duration_ns": 999472100 },
      { "index": 1, "state": "completed", "bytes": 826413049, "duration_ns": 999472100 },
      { "index": 2, "state": "completed", "bytes": 812122112, "duration_ns": 999472100 },
      { "index": 3, "state": "completed", "bytes": 811536377, "duration_ns": 999472100 }
    ]
  },
  "upload": {
    "bytes": 2100068352,
    "duration_ns": 998971500,
    "mbps": 16817.84,
    "mb_per_second": 2102.2304960652,
    "connections": 4,
    "active_connections": 4,
    "failed_connections": 0,
    "measurement_window": "shared_window_first_body_write_to_last_server_confirmation",
    "stop_reason": "duration_elapsed",
    "statistics": { "samples": 3, "mean_mbps": 16869.19 },
    "connection_reports": [
      { "index": 0, "state": "completed", "bytes": 524845056, "server_confirmed_bytes": 524845056 },
      { "index": 1, "state": "completed", "bytes": 549978112, "server_confirmed_bytes": 549978112 },
      { "index": 2, "state": "completed", "bytes": 514719744, "server_confirmed_bytes": 514719744 },
      { "index": 3, "state": "completed", "bytes": 510525440, "server_confirmed_bytes": 510525440 }
    ],
    "server_confirmed_bytes": 2100068352,
    "server_duration_ns": 998971500
  }
}
```

说明：

- 逐连接字节数之和恰好等于聚合 `bytes`（`824905721 + 826413049 + 812122112 + 811536377 = 3274977259`），
  不存在重复计数；上传每条连接的 `server_confirmed_bytes` 都等于该连接发送的字节数；
- `min_ns = 0` 是因为 Windows 单调时钟无法分辨这段极短回环往返（低于时钟分辨率），
  引擎如实输出原始读数，而不是编造一个最小延迟；
- `packet_loss_percent` 为 `null`：没有数据包级丢包测试，不猜测、不填 0；
- 采样不足时统计字段为 `null`（N/A）；上面截取省略了部分采样点与字段，完整结构见
  [docs/architecture.md](docs/architecture.md)。

## 测速原理

```text
Mbps = bytes × 8 / seconds / 1,000,000
```

| 场景 | 测量窗口 |
| --- | --- |
| 单连接下载 | 第一个响应字节 → 传输停止 |
| 多连接下载 | 首个连接的第一个响应字节 → 聚合传输停止 |
| 单连接上传 | 第一次写请求体 → 服务端确认读完 |
| 多连接上传 | 首个连接第一次写请求体 → 最后一个服务端确认读完 |

- 多连接速率 = 所有连接在同一共享窗口内的字节总和 / 窗口时长，
  绝不把各连接的时间或 Mbps 相加；
- 采样点的瞬时速率使用两次真实观测之间的单调时钟差值计算，
  不使用标称采样间隔；
- 统计中的标准差为样本标准差（n-1），变异系数 = 标准差 / 平均值 × 100%；
- 丢包率 `N/A`：没有真实丢包测试能力就不输出任何百分比。

完整推导、单位约定、统计口径和误差来源见 [docs/benchmark.md](docs/benchmark.md)，
并发模型与扩展点见 [docs/architecture.md](docs/architecture.md)。

## 准确性说明

1. 使用单调时钟计时，采样速率按真实观测窗口计算，避免调度延迟造成误差；
2. 所有速率都基于真实传输字节数与真实耗时，绝不使用随机数或模拟值；
3. 每个连接独立计数，聚合值必须等于逐连接之和，测试中会校验；
4. 上传结果必须通过服务端字节数确认，不一致时报告异常而非正常结果；
5. 禁用 HTTP 压缩、禁用缓存，避免速率虚高；
6. 不对 Mbps 结果乘任何补偿系数；
7. 部分连接失败会记录告警与有效连接数，不会静默忽略；
8. **本地回环测速不代表公网速度**，CLI 会明确显示 `Local Loopback Test`；
9. 单个 HTTP 测速结果**不一定代表运营商接入带宽的理论最大值**，
   它同时受客户端、服务器、链路、连接数和服务器负载影响；
10. 测速值稳定不等于绝对准确；波动小只说明窗口内相对稳定。

## 已知限制

- 只支持 HTTP(S) 测速，没有 ICMP / UDP / TCP Connect 测试；
- 上传速率窗口包含确认往返，结果偏保守；
- Windows 等平台的时钟分辨率会导致极短回环测量读数偏小甚至为 0；
- 采样点数量受采样间隔与阶段时长限制，短阶段可能没有统计数据（N/A）；
- 没有四分位、多次测速聚合、GUI、历史记录、CSV 导出和公网节点自动选择；
- 单次测试受服务器性能影响，不能直接等同于链路容量。

## 开发路线图

| 版本 | 内容 |
| --- | --- |
| v0.1.0 | 项目初始化、CLI、本地测速服务端、HTTP RTT、单连接下载/上传 |
| v0.2.0（当前） | 多连接并发（1..16）、共享预算与窗口、实时采样、描述性统计、连接级报告 |
| v0.3.0 | 四分位与波动分析、多次测速聚合、多节点管理、公网节点、历史记录 |
| v0.4.0 | 本地 Web UI、实时曲线、节点选择界面 |
| v0.5.0 | Wails 桌面客户端、Windows 安装包、深色/浅色主题 |
| v0.6.0 | Linux / macOS 桌面支持、网络接口状态显示、诊断能力 |
| 未来 | ICMP / UDP 网络质量测试、负载延迟、Bufferbloat、持续监控 |

详细路线图见 [docs/roadmap.md](docs/roadmap.md)。

## 开发与测试

```bash
go mod tidy
gofmt -l .            # 期望无输出
go vet ./...
go test ./...
go test -race ./...   # Linux / 已安装 CGO 工具链的环境
go build ./cmd/gospeed
```

本地端到端验证：

```bash
# 终端 1
go run ./cmd/gospeed server --addr 127.0.0.1:8080

# 终端 2
curl http://127.0.0.1:8080/health
go run ./cmd/gospeed test --server http://127.0.0.1:8080 --connections 8 --duration 10s
```

## 开源许可证

本项目使用 [MIT License](LICENSE)。
