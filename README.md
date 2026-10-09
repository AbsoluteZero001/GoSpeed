# GoSpeed（秒速）

[![CI](https://github.com/AbsoluteZero001/GoSpeed/actions/workflows/ci.yml/badge.svg)](https://github.com/AbsoluteZero001/GoSpeed/actions/workflows/ci.yml)

> 基于 Go 语言的跨平台网络测速与网络质量分析工具。
>
> A cross-platform network speed test and network quality analysis tool written in Go.

GoSpeed 不是"发一个 HTTP 请求算个除法"的演示程序。v0.1.0 的目标是提供一套
**可解释、可复现、可验证**的测速内核：真实的字节计数、明确的测量窗口、严格的
单位换算，以及绝不伪造任何指标的输出策略。

## 功能特性

### 已实现（v0.1.0）

- 真实 HTTP 下载测速：流式读取、固定缓冲区、不可压缩载荷、真实字节计数
- 真实 HTTP 上传测速：随机载荷流式上传、服务端确认字节数、双向一致性校验
- HTTP RTT 延迟测试：多次采样、min / avg / max、相邻样本抖动（HTTP RTT jitter）
- 自建测速服务端：`/health`、`/ping`、`/download`、`/upload`
- CLI：`gospeed test` / `server` / `nodes` / `version`
- 节点配置：本地 JSON 文件、唯一 ID 校验、可用性健康检查
- 结构化 JSON 结果：原始字节数、原始耗时、测量窗口、配置快照、`test_id`
- 安全默认：服务端默认只监听 `127.0.0.1`，不转发、不落盘、不暴露系统信息
- 生命周期控制：`context` 取消、阶段超时、零时长保护、无模拟数据
- 单元测试（`httptest` 本地环境）、`go vet`、`-race`、GitHub Actions CI

### 未实现（Planned，正在规划中）

以下能力**尚未实现**，请勿当作已有功能：

- 多连接并发测速（v0.2.0；v0.1.0 只支持单连接，指定 `--connections > 1` 会明确报错）
- ICMP Ping / UDP 丢包测试（丢包率当前显示 `N/A`，不使用 HTTP 失败率冒充）
- TCP Connect RTT、负载延迟（loaded latency）、Bufferbloat 分析
- 多节点自动选择、公网公共测速节点（没有经过验证的公共节点）
- 完整的 Wails + Vue 3 桌面 GUI、Web UI、历史记录、CSV 导出
- 网络接口协商速率采集（且永远不会把协商速率当成实测吞吐）

## 开发状态

| 项目 | 状态 |
| --- | --- |
| 版本 | v0.1.0（初始可运行框架） |
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
│   ├── speedtest/             # 测速引擎（延迟 / 下载 / 上传 / 指标 / 结果模型）
│   └── version/               # 版本与 User-Agent
├── configs/
│   └── nodes.example.json     # 节点配置示例（只包含本机回环节点）
├── docs/
│   ├── architecture.md        # 模块划分、测量窗口、扩展点
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

方式一，直接安装：

```bash
go install github.com/AbsoluteZero001/GoSpeed/cmd/gospeed@latest
```

方式二，从源码构建（推荐先克隆仓库）：

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

终端 2，运行一次测速：

```bash
go run ./cmd/gospeed test --duration 10s
```

`test` 默认执行延迟、下载、上传三个阶段，并自动使用
`configs/nodes.example.json` 中的 `local` 节点。

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
| `--duration` | `10s` | 下载 / 上传阶段的传输时长窗口 |
| `--timeout` | `30s` | 单个阶段的超时，必须大于 `--duration` |
| `--connections` | `1` | 并发连接数；v0.1.0 只支持 `1`，其他值会明确报错 |
| `--latency-samples` | `5` | HTTP RTT 采样次数 |
| `--latency-interval` | `100ms` | 采样间隔 |
| `--max-bytes` | `0` | 每个阶段的最大字节数；`0` 表示只按时长结束 |
| `--warmup` | `true` | 正式测量前发送一次 `/health` 预热并快速失败 |
| `--json` | `false` | 将完整结果以 JSON 输出到 stdout（进度信息输出到 stderr） |

示例：

```bash
# 对本地服务器做一次 10 秒测速
gospeed test --server local --duration 10s

# 限制传输 64 MiB，输出 JSON，便于脚本消费
gospeed test --server http://127.0.0.1:8080 --max-bytes 67108864 --json
```

### gospeed server

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--addr` | `127.0.0.1:8080` | 监听地址；非回环地址会打印安全警告 |
| `--max-upload-bytes` | `1 GiB` | 单次上传上限 |
| `--max-download-bytes` | `4 GiB` | 单次下载上限 |
| `--max-download-duration` | `60s` | 单次下载时长上限 |
| `--default-download-duration` | `10s` | 未指定 `bytes` / `duration_ms` 时的默认下载时长 |
| `--read-timeout` | `60s` | HTTP 读超时 |
| `--write-timeout` | `75s` | HTTP 写超时，必须大于最大下载时长 |

### gospeed nodes

```bash
# 列出节点
gospeed nodes

# 对每个节点执行 GET /health 健康检查
gospeed nodes --check --timeout 3s

# JSON 输出，便于脚本处理
gospeed nodes --json
```

## 本地测速服务器

```bash
go run ./cmd/gospeed server --addr 127.0.0.1:8080
```

HTTP API：

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
4. 部署到公网节点前，请自行增加 HTTPS、反向代理、流量限制、认证与监控。

## 测速结果示例

下面是 v0.1.0 在开发机上对**本机回环服务器**执行的真实输出（2026-10-09）。
回环数值只反映本机协议栈能力，**不代表公网带宽**：

```text
====================================
              GoSpeed
         Network Speed Test
====================================
Server:   127.0.0.1:18080 (custom)
Address:  http://127.0.0.1:18080
Protocol: http
Mode:     Local Loopback Test - does not represent internet bandwidth

Testing latency...
  HTTP RTT avg: 0.27 ms

Testing download...

  download    6608.53 Mbps   197.31 MiB transferred
  download    7442.19 Mbps   444.38 MiB transferred
  download    7134.78 Mbps   638.81 MiB transferred

Testing upload...

  upload      7247.26 Mbps   215.41 MiB transferred
  upload      7113.64 Mbps   423.44 MiB transferred
  upload      7284.32 Mbps   650.69 MiB transferred
  upload      7461.69 Mbps   888.91 MiB transferred

====================================
Test ID:  gs-c46d3c526e41d17c
Status:   completed
Server:   127.0.0.1:18080 (custom)
Address:  http://127.0.0.1:18080
Protocol: http
Mode:     Local Loopback Test - does not represent internet bandwidth

Latency:  0.00 ms min / 0.27 ms avg / 0.83 ms max (5/5 HTTP RTT samples)
Jitter:   0.46 ms (mean absolute difference of consecutive HTTP RTT samples)
Packet loss: N/A (not measured in v0.1.0)
Download: 6596.79 Mbps (824.60 MB/s, 786.38 MiB in 1.00 s, 1 connection)
Upload:   7457.32 Mbps (932.17 MB/s, 889.38 MiB in 1.00 s, server confirmed 889.38 MiB)
====================================
```

`--json` 输出（同样来自真实运行，此处截取了关键字段）：

```json
{
  "test_id": "gs-f00ad2d2eb93367f",
  "timestamp": "2026-10-09T07:55:51.3708541Z",
  "status": "completed",
  "target": {
    "server_id": "custom",
    "server_name": "127.0.0.1:18080",
    "server_address": "http://127.0.0.1:18080",
    "protocol": "http",
    "local": true
  },
  "settings": {
    "phases": ["latency", "download", "upload"],
    "duration_ns": 5000000000,
    "timeout_ns": 20000000000,
    "max_bytes": 67108864,
    "connections": 1,
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
    "average_ns": 100940,
    "max_ns": 504700,
    "jitter_ns": 252350,
    "samples_ns": [0, 0, 504700, 0, 0],
    "packet_loss_percent": null
  },
  "download": {
    "bytes": 67108864,
    "duration_ns": 42672400,
    "mbps": 12581.221398374593,
    "mb_per_second": 1572.6526747968242,
    "connections": 1,
    "measurement_window": "first_response_byte_to_last_body_byte",
    "stop_reason": "requested_bytes_reached"
  },
  "upload": {
    "bytes": 67108864,
    "duration_ns": 67045800,
    "mbps": 8007.524885973468,
    "mb_per_second": 1000.9406107466835,
    "connections": 1,
    "measurement_window": "request_body_write_start_to_server_confirmation",
    "stop_reason": "requested_bytes_reached",
    "server_confirmed_bytes": 67108864,
    "server_duration_ns": 66481500
  }
}
```

说明：

- `min_ns = 0` 是因为 Windows 单调时钟无法分辨这段极短的回环往返（低于时钟分辨率），
  引擎如实输出原始读数，而不是编造一个最小延迟；
- `packet_loss_percent` 为 `null`：v0.1.0 没有数据包级丢包测试，不猜测、不填 0；
- 上传的 `server_confirmed_bytes` 与客户端 `bytes` 一致，不一致时引擎会直接报错。

## 测速原理

```text
Mbps = bytes × 8 / seconds / 1,000,000
```

- **下载**：测量窗口为"第一个响应字节 → 最后一个响应体字节"，统计客户端实际读到的字节数；
- **上传**：测量窗口为"请求体第一次被传输层读取 → 收到服务端确认"，分子使用服务端确认的字节数；
- **延迟**：HTTP RTT（请求到第一个响应字节），明确标注 `latency.type = http_rtt`，
  不冒充 ICMP Ping；jitter 为相邻样本差值的平均绝对值；
- **丢包**：`N/A`。没有真实丢包测试能力就不输出任何百分比。

完整推导、单位约定和误差来源见 [docs/benchmark.md](docs/benchmark.md)，
模块划分与扩展点见 [docs/architecture.md](docs/architecture.md)。

## 准确性说明

1. 使用单调时钟计时，避免系统时间跳变影响结果；
2. 所有速率都基于真实传输字节数与真实耗时，绝不使用随机数或模拟值；
3. 禁用 HTTP 压缩、禁用缓存，避免速率虚高；
4. 区分连接建立时间与数据传输时间，并记录测量窗口；
5. 支持配置传输时长与数据量，支持重复测试；
6. 上传结果必须通过服务端字节数确认，字节数不一致时报告异常而非正常结果；
7. **本地回环测速不代表公网速度**，CLI 会明确显示 `Local Loopback Test`；
8. 单个 HTTP 测速结果**不一定代表运营商接入带宽的理论最大值**，
   它同时受客户端、服务器、链路、并发连接数和服务器负载影响；
9. 网卡协商速率、运营商套餐带宽、服务器可用带宽与实际吞吐量是不同概念，
   本项目不会把它们混为一谈。

## 已知限制

- v0.1.0 仅支持单连接测速；多连接并发在 v0.2.0 规划中；
- 只有 HTTP RTT 一种延迟指标，没有 ICMP / TCP Connect / UDP 丢包测试；
- 速率窗口包含协议栈开销（尤其上传包含一次确认往返），结果偏保守；
- Windows 等平台的时钟分辨率会导致极短回环测量读数偏小甚至为 0；
- 没有 GUI、历史记录、CSV 导出和公网节点自动选择；
- 单次测试受服务器性能影响，不能直接等同于链路容量。

## 开发路线图

| 版本 | 内容 |
| --- | --- |
| v0.1.0（当前） | 项目初始化、CLI、本地测速服务端、HTTP RTT、单连接下载/上传、JSON 结果、单元测试 |
| v0.2.0 | 多连接并发、实时速率采样、时长配置、稳定性分析、jitter 统计 |
| v0.3.0 | 多节点管理、节点自动选择、公网节点支持、多次测速对比、历史记录 |
| v0.4.0 | 本地 Web UI、仪表盘、实时曲线、节点选择界面 |
| v0.5.0 | Wails 桌面客户端、Windows 安装包、深色/浅色主题、结果可视化 |
| v0.6.0 | Linux / macOS 桌面支持、网络接口状态显示、更完整的诊断能力 |
| 未来 | ICMP / UDP 网络质量测试、负载延迟、Bufferbloat 分析、持续监控 |

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

本地端到端验证流程：

```bash
# 终端 1
go run ./cmd/gospeed server --addr 127.0.0.1:8080

# 终端 2
curl http://127.0.0.1:8080/health
go run ./cmd/gospeed test --server http://127.0.0.1:8080 --duration 10s
```

## 开源许可证

本项目使用 [MIT License](LICENSE)。
