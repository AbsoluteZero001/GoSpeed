# GoSpeed（秒速）

[![CI](https://github.com/AbsoluteZero001/GoSpeed/actions/workflows/ci.yml/badge.svg)](https://github.com/AbsoluteZero001/GoSpeed/actions/workflows/ci.yml)

> 基于 Go 语言的跨平台网络测速与网络质量分析工具。
>
> A cross-platform network speed test and network quality analysis tool written in Go.

GoSpeed v0.5.0-beta.1 在 v0.4.0 的 Windows 桌面客户端基础上，完成了**统一
Provider 测量架构**：新增 `internal/provider` 抽象层与 Registry，桌面端所有测量
路径统一经过 Provider 调度，并把下载 / 上传 / Latency / Jitter 映射为一份统一的
结果模型（`provider.FinalResult`），在 GUI 中显式展示真实 Mbps、指标口径、Provider
来源与节点范围。测速内核仍是唯一的计算实现（`internal/speedtest`），所有结果坚持
同一原则：真实字节计数、明确测量窗口、绝不伪造指标。

> **v0.5.0 Beta 定位：Windows Developer Preview / Self-hosted Speedtest。**
>
> 目前正式注册的 Provider 只有 `gospeed-http`（GoSpeed 自有 HTTP 测速协议）。
> 项目**没有**面向普通用户的官方公共测速节点，也**不会**静默连接任何未经授权的
> 第三方公共服务。要测量真实宽带速度，请自行部署 GoSpeed 测速服务端并配置节点。
> M-Lab（实验性）与 Cloudflare（隔离实验）均不是默认 Provider。

> **隐私政策**：[PRIVACY.md](PRIVACY.md)。GoSpeed 自身不收集你的数据；
> 实验性 M-Lab NDT7 测速会在你明确同意后连接第三方（M-Lab）服务器，
> 你的公网 IP 将被 M-Lab 收集、公开发布并长期保留，且无法保证事后删除，
> 请在使用前阅读。

## 功能特性

### 已实现（v0.5.0-beta.1）

**统一 Provider 测量架构（v0.5.0 新增）**

- `internal/provider`：Provider 接口（`ID` / `Negotiate` / `Run`）、统一结果模型
  `FinalResult`、Provider Registry
- `gospeed-http`：目前唯一正式注册的 Provider，直接复用 `internal/speedtest` 引擎，
  不复制任何测速协议解析器或速率公式
- Capability 协商走 `GET /capabilities`：**真实支持**的指标才会被声明支持；
  未实现的指标不伪造、不乐观声明；旧节点按 legacy 规则继续兼容
- 统一结果模型如实区分：`completed` / `failed` / `cancelled` / `timeout` /
  `budget_exceeded` / `consent_required`；部分完成保留 Warning
- 速率单位统一为真实 Mbit/s；延迟标注为 **HTTP RTT**，不冒充 ICMP Ping；
  Jitter 使用连续样本差值口径，**RTTVar 不冒充 Jitter**
- 缺失指标使用 `null` / N/A，绝不显示为实测的 `0` 或 `0.00 Mbps`
- 取消（context cancel）与超时不被误判为正常完成；正常达到计划传输字节数
  **不会**被误报为 `budget_exceeded`
- Provider 名称、节点范围（Node Scope）、测量口径与结果质量在 GUI 中显式展示

**桌面 GUI（v0.4.0 起，v0.5.0 接入统一结果面板）**
- Wails v2.16.0（稳定线）+ Vue 3 + TypeScript + Vite + ECharts + Windows WebView2
- 一键开始测速；显示下载 / 上传实时与窗口平均速率
- 实时速率曲线（每个采样点来自引擎的实时采样）与 HTTP RTT 采样曲线
- Ping（HTTP RTT）与 Jitter：样本不足以分辨时显示 N/A，不显示 0
- 测速类型提示：本机回环（`127.0.0.1` / `::1` / `localhost`）显示“本机回环性能
  测试”，速率标注为“本机吞吐量”并说明不代表真实宽带速度；局域网显示“局域网
  测速，不代表互联网宽带速度”；公网字面地址显示“公网目标测速，结果受到服务器
  带宽、路由、网络拥塞和测速配置影响”；域名目标保持“路径未知”，不因地址分类
  就宣称流量经过公网；仅配置本机节点时明确提示尚未测得真实宽带速度
- 节点自动选择（复用 `nodes.CheckAll` + `nodes.SelectAuto`，展示候选排名与选择原因）
  与手动选择；节点健康表与能力协商信息
- 配置并发连接数（1..16）、测速时长、采样间隔
- 取消测速 / 取消节点检查：真实取消 `context`，等待引擎返回并释放连接后再结束任务
- 通过 Wails 事件推送状态、目标、进度与最终结果，前端**不轮询**
- 启动时不自动测速，也不启动任何监听端口的测速服务

**测速内核（v0.3.0 起）**

- 多节点管理：`nodes list / check / auto / add / remove / enable / disable`
- 节点配置 JSON：唯一 ID 校验、地址安全策略、**原子写入**（临时文件 + rename）
- 健康探测：DNS / TCP / TLS / HTTP 分段计时，有限重试（1..5 次），
  状态 `healthy` / `degraded` / `unavailable` / `unknown`
- 节点能力协商：`GET /capabilities`（协议版本、服务端版本、能力、限额）；
  旧服务端（404/405）自动识别为 legacy 并继续可用
- 自动选择：可用性优先、其次实测 HTTP RTT 中位数、最后稳定 ID 排序；
  记录选择方法与原因，并明确声明“延迟排序 ≠ 带宽排名”
- 手动选择：`gospeed test --node <id>`，也可直接使用 URL
- 真实远程 HTTP(S) 测速：默认校验证书，支持超时、取消与网络断开
- 服务端限额校验：超过服务端公布的连接数 / 时长 / 字节上限时直接报错，不静默裁剪
- 多次测速：`--repeat N`（1..10），每次保留独立 `Result`，另生成 `Summary`
  （平均 / 中位 / 最小 / 最大 / n-1 标准差 / CV，只统计已完成运行）
- 多连接并发下载 / 上传（1..16）、共享字节预算、统一测量窗口、逐连接报告
- 实时速率采样（默认 200 ms）与描述性统计
- 服务端并发保护：超过并发测速上限返回 503 + `Retry-After`；
  默认仍只监听 `127.0.0.1`
- 结构化 JSON 结果：字节数、测量窗口、采样、逐连接证据、能力与健康信息
- 单元测试、并发测试、`go vet`、`-race`、GitHub Actions CI

### 未实现（Planned，正在规划中）

以下能力**尚未实现**，请勿当作已有功能：

- 节点身份验证 / 授权（token、mTLS）
- 全局流量配额与单客户端配额（当前只有单请求限额与并发上限）
- 官方公共测速节点清单（公网节点必须由用户自行配置）
- ICMP Ping / UDP 丢包测试（丢包率显示 `N/A`，不使用 HTTP 失败率冒充）
- TCP Connect RTT、负载延迟（loaded latency）、Bufferbloat 分析
- 四分位（IQR）、多节点并行对比、历史记录与 CSV 导出、本地 Web UI

## 开发状态

| 项目 | 状态 |
| --- | --- |
| 版本 | v0.5.0-beta.1（Windows Developer Preview / Self-hosted Speedtest） |
| Go Modules | `module github.com/AbsoluteZero001/GoSpeed` |
| 本地验证环境 | `go1.27.2 windows/amd64`（`go.mod` 最低要求 Go 1.25） |
| 第三方依赖 | CLI 与服务端：无（仅标准库）；桌面端：Wails v2.16.0、Vue 3、ECharts（独立模块 `desktop/`）。Cloudflare 实验 SDK 不是运行时 Provider 依赖 |
| CI | Windows / Linux / macOS：`gofmt` 检查、`go vet`、`go test`、`-race`、跨平台构建；Windows 追加桌面模块 `go vet` / `go test` / `npm test` / `npm build` / `wails build` |
| 测试状态 | 根模块与桌面模块：`go vet ./...`、`go test ./...`、`go test -race ./...` 全部通过；前端：Vitest 9 文件 / 55 测试通过，`npm run build` 通过 |

## 技术栈

- **语言**：Go 标准库（`net`、`net/http`、`net/http/httptrace`、`context`、
  `sync/atomic`、`crypto/tls`、`encoding/json`、`io`、`time`）
- **CLI**：标准库 `flag`，无第三方 CLI 框架
- **服务端**：标准库 `net/http`，固定路由，无开放代理
- **桌面 GUI**：Wails v2.16.0（Go）+ Vue 3 + TypeScript + Vite + ECharts +
  Windows WebView2；位于独立模块 `desktop/`，通过 `replace` 复用 `internal/` 测速内核

## 项目结构

```text
GoSpeed/
├── cmd/gospeed/main.go
├── desktop/          # Wails 桌面客户端（独立 Go 模块，复用 internal/）
│   ├── main.go       # Wails 入口：不自动测速、不启动服务端
│   ├── app.go        # Wails binding（GetAppInfo / ListNodes / CheckNodes / StartTest / CancelTest）
│   ├── runner.go     # 目标解析、事件推送、取消与资源释放
│   ├── providerbridge.go  # Provider Registry 桥接：统一结果 -> GUI 视图模型
│   └── frontend/     # Vue 3 + TypeScript + Vite + ECharts
├── internal/
│   ├── cli/          # test / server / nodes 子命令与渲染
│   ├── config/       # CLI 与测试共享默认值
│   ├── nodes/
│   │   ├── node.go       # 节点模型与配置校验
│   │   ├── manager.go    # 内存节点管理（List/Add/Remove/Update/...）
│   │   ├── store.go      # 原子持久化
│   │   ├── security.go   # 回环 / 链路本地 / 云元数据地址策略
│   │   ├── probe.go      # 健康探测、分段计时、能力协商、延迟采样
│   │   └── select.go     # 自动选择策略与选择原因
│   ├── provider/     # Provider 接口、统一结果模型 FinalResult、Registry、gospeed-http 实现
│   ├── server/       # 测速服务端 + /capabilities + 并发保护
│   ├── speedtest/    # 测速引擎（延迟/下载/上传/采样/统计/汇总/能力协商）
│   └── version/      # 版本号与 User-Agent
├── configs/nodes.example.json
├── docs/{architecture,benchmark,roadmap}.md`n├── CHANGELOG.md
├── .github/workflows/ci.yml
├── go.mod
└── LICENSE（MIT）
```

## 环境要求

- Go 1.25 或更高版本（本地使用 go1.27.2 验证）
- Windows / Linux / macOS（纯标准库，无 CGO 依赖）
- 本地回环测试不需要额外服务；公网测速节点需要你自行部署或获得授权
- 桌面 GUI 额外需要：Wails CLI v2.16.0、Node.js 20.19+（本地使用 Node 24.15.0）、
  Windows WebView2 Runtime（Windows 10/11 通常已随 Edge 安装）

## 安装方式

```bash
go install github.com/AbsoluteZero001/GoSpeed/cmd/gospeed@latest
```

```powershell
# Windows (PowerShell)
go build -o bin\gospeed.exe .\cmd\gospeed
.\bin\gospeed.exe version
```

桌面 GUI（Windows，需要 Wails CLI v2.16.0 与 WebView2）：

```powershell
go install github.com/wailsapp/wails/v2/cmd/wails@v2.16.0
cd desktop
wails build
# 产物：desktop\build\bin\GoSpeed.exe（双击运行，启动时不会自动测速）
```

```bash
# Linux / macOS
go build -o bin/gospeed ./cmd/gospeed
./bin/gospeed version
```

## 快速开始

```bash
# 终端 1：启动本地测速服务端（默认 127.0.0.1:8080）
go run ./cmd/gospeed server

# 终端 2：对本地节点测速
go run ./cmd/gospeed test --connections 4 --duration 10s
```

## 节点配置

默认查找顺序：`configs/nodes.json`（本地配置）→ `configs/nodes.example.json`
（只读示例）→ 内置 `local` 节点。也可以显式指定：

```bash
gospeed nodes list --config configs/nodes.json
```

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
      "local": true,
      "provider": "GoSpeed",
      "description": "Loopback test server started with: gospeed server"
    },
    {
      "id": "private-cloud",
      "name": "Private Cloud Server",
      "base_url": "https://speed.example.com",
      "protocol": "https",
      "enabled": false,
      "provider": "Replace with your own provider"
    }
  ]
}
```

说明：

1. `speed.example.com` 只是**示例占位符**，不是可用的公共测速节点；
2. 回环地址必须显式写 `"local": true`，否则配置会被拒绝；
3. 链路本地地址与云元数据地址（`169.254.169.254`、`100.100.100.200` 等）
   会被**始终拒绝**，避免共享的节点清单把客户端变成内网探测器；
4. `configs/nodes.example.json` 只读，修改类命令会拒绝写入，请复制为
   `configs/nodes.json` 或使用 `--config` 指定自己的文件。

## 节点命令

```bash
# 列表
gospeed nodes list
gospeed nodes list --json

# 健康检查（DNS/TCP/TLS/HTTP 分段耗时 + 能力协商）
gospeed nodes check --timeout 3s --samples 3
gospeed nodes check --json

# 自动选择（探测 → 排序 → 输出选择原因）
gospeed nodes auto --samples 3
gospeed nodes auto --json

# 增删改
gospeed nodes add --id private-cloud --name "Private Cloud" --url https://speed.example.com --provider "My Provider"
gospeed nodes disable --config configs/nodes.json private-cloud
gospeed nodes enable  --config configs/nodes.json private-cloud
gospeed nodes remove  --config configs/nodes.json private-cloud
```

自动选择策略（`nodes auto` / `test --auto`）：

1. 只考虑本轮探测中 `healthy` / `degraded` 的启用节点；
2. `healthy` 优先于 `degraded`；
3. 同状态下比较 `/ping` 的 HTTP RTT 中位数，越小越靠前；
4. 仍并列时按节点 ID 升序，并在原因中说明是并列。

> 延迟排序只用于挑选候选节点，**不代表服务器带宽排名**；
> 需要比较吞吐量必须执行真实下载 / 上传测速（可用 `--repeat`）。

## 测速命令

```bash
gospeed test [flags]
```

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--server` | 第一个启用节点 | 节点 ID 或绝对 URL（如 `https://speed.example.com`） |
| `--node` | 空 | 按节点 ID 手动选择（等价于 `--server <id>`） |
| `--auto` | `false` | 先探测并自动选择节点，再执行测速 |
| `--config` | 自动查找 | 节点配置文件路径 |
| `--connections` | `1` | 并发连接数，1..16（端到端覆盖 1 / 4 / 8 / 16） |
| `--duration` | `10s` | 传输时长窗口 |
| `--timeout` | `30s` | 单阶段超时，必须大于 `--duration` |
| `--sample-interval` | `200ms` | 实时速率采样间隔 |
| `--max-bytes` | `0` | 阶段总字节预算（所有连接共享） |
| `--repeat` | `1` | 顺序重复测速次数，1..10 |
| `--capabilities` | `true` | 协商 `GET /capabilities` 并校验服务端限额 |
| `--latency-samples` | `5` | HTTP RTT 采样次数 |
| `--latency-interval` | `100ms` | 采样间隔 |
| `--warmup` | `true` | 正式测量前请求一次 `/health` |
| `--json` | `false` | 结果 JSON 输出到 stdout（进度输出到 stderr） |

示例：

```bash
# 手动选择节点
gospeed test --node local --connections 8 --duration 10s

# 自动选择节点 + 重复 3 次 + JSON 汇总
gospeed test --auto --repeat 3 --connections 4 --duration 10s --json

# 直接对远程 GoSpeed 服务端测速（证书默认正常校验）
gospeed test --server https://speed.example.com --connections 4 --duration 10s
```

`--repeat 1` 时 JSON 仍是单个 `Result`（v0.2.0 兼容）；
`--repeat > 1` 时输出 `{ "summary": {...}, "results": [...] }`。

## 本地测速服务端

```bash
go run ./cmd/gospeed server --addr 127.0.0.1:8080
```

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/health` | 健康检查（`{"status":"ok","service":"gospeed","version":"..."}`） |
| `GET` | `/ping` | 极简响应（204），用于 HTTP RTT |
| `GET` | `/capabilities` | 协议版本、服务端版本、能力与限额 |
| `GET` | `/download?bytes=N` 或 `?duration_ms=N` | 流式不可压缩随机数据 |
| `POST` | `/upload` | 流式接收并返回实际收到的字节数 |

| 服务端参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--addr` | `127.0.0.1:8080` | 监听地址；非回环地址会打印警告 |
| `--max-upload-bytes` | `1 GiB` | 单请求上传上限 |
| `--max-download-bytes` | `4 GiB` | 单请求下载上限 |
| `--max-download-duration` | `60s` | 单请求时长上限 |
| `--max-concurrent-tests` | `32` | 同时处理的 `/download` + `/upload` 上限，超出返回 503 |
| `--max-connections-per-test` | `16` | 通过 `/capabilities` 公布的每测试连接数上限 |
| `--read-timeout` / `--write-timeout` | `60s` / `75s` | HTTP 读写超时（写超时必须大于最大下载时长） |

## 公网节点：安全、隐私与费用

1. 公网节点必须**由你自行部署或获得授权**，GoSpeed 不附带任何公共节点清单；
2. 默认使用 HTTPS 并正常校验证书，**不会**跳过证书校验；
3. 客户端不会把节点配置变成通用代理：只请求上述固定端点；
4. 节点列表加载与保存时执行地址策略，阻止回环（未标记 local）、链路本地和
   云元数据地址；
5. **单次测速会消耗真实公网流量**：下载测速消耗服务器出方向流量，
   上传测速消耗入方向流量。多连接 + 重复测速会成倍放大流量与云费用；
6. 公开部署服务端前请自行增加身份验证、全局字节配额、单客户端配额、
   监控与日志；当前版本只提供单请求限额与并发上限，不要把它直接暴露到公网；
7. GoSpeed 不会上传你的配置或测速结果；结果只写入 stdout 或你自己保存的文件。

## 测速结果示例

以下数据来自 v0.3.0 在本机启动两个服务端 + 一个失效节点的真实运行
（2026-10-09）。回环数值只反映本机能力，**不代表公网带宽**。

### 节点健康检查

```text
Config: configs/nodes.json

ID             STATUS       HTTP RTT     DNS          TCP          TLS          CAPABILITIES   DETAIL
dead           unavailable  N/A          N/A          N/A          N/A          no             Get "http://127.0.0.1:18999/health": dial tcp ... connection refused
local-a        healthy      1.82 ms      N/A          N/A          N/A          yes (0.3.0)    3 sample(s) below clock resolution
local-b        healthy      2.53 ms      N/A          N/A          N/A          yes (0.3.0)    2 sample(s) below clock resolution
```

（`DNS` / `TCP` / `TLS` 为 N/A 表示该阶段在本次连接中未被观测到，
或低于平台时钟分辨率；不是 0。）

### 自动选择节点

```text
Selected: Local Server A (local-a)
Reason:   healthy; tied on measured median HTTP RTT 0.55 ms with another candidate; selected the lowest node ID (local-a)
Note:     Latency ranking only picks a candidate node; it does not rank server bandwidth or throughput.

RANK ID             STATUS       HTTP RTT (MEDIAN) REASON
1    local-a        healthy      0.55 ms          healthy, median HTTP RTT 0.55 ms, server 0.3.0
2    local-b        healthy      0.55 ms          healthy, median HTTP RTT 0.55 ms, server 0.3.0
```

### 重复测速汇总（3 轮，自动选择）

```text
GoSpeed v0.3.0 - 3 run(s)
Server:      Local Server A (local-a)
Address:     http://127.0.0.1:18090
Protocol:    http
Scope:       local
Connections: 1
Completed:   3, failed: 0, cancelled: 0

RUN  STATUS     DOWNLOAD           UPLOAD             RTT            WARNINGS
1    completed  14109.17 Mbps      1390.16 Mbps       0.00 ms        0
2    completed  20442.57 Mbps      1130.19 Mbps       0.39 ms        0
3    completed  30866.00 Mbps      5895.95 Mbps       0.10 ms        0

Download Mbps: 3 value(s), mean 21805.91, median 20442.57, min 14109.17, max 30866.00, stddev 8461.20 (sample_n_minus_1), cv 38.80%
Upload Mbps:   3 value(s), mean 2805.43, median 1390.16, min 1130.19, max 5895.95, stddev 2679.62 (sample_n_minus_1), cv 95.52%
Latency ms:    3 value(s), mean 0.16, median 0.10, min 0.00, max 0.39, stddev 0.20 (sample_n_minus_1), cv 123.38%
Jitter ms:     3 value(s), mean 0.25, median 0.25, min 0.00, max 0.49, stddev 0.25 (sample_n_minus_1), cv 99.07%
```

### JSON 汇总（截取 `--repeat 3` 的关键字段）

```json
{
  "summary": {
    "node_id": "local-a",
    "server_address": "http://127.0.0.1:18090",
    "protocol": "http",
    "network_scope": "local",
    "connections": 1,
    "runs": 3,
    "completed": 3,
    "failed": 0,
    "cancelled": 0,
    "download_mbps": {
      "samples": 3,
      "mean": 11502.650996172628,
      "median": 11757.395844283263,
      "min": 3584.2625192274827,
      "max": 19166.29462500714,
      "stddev": 7794.138973588821,
      "cv_percent": 67.75950149389239,
      "stddev_kind": "sample_n_minus_1"
    },
    "run_details": [
      {
        "index": 0,
        "test_id": "gs-0516a130caafd78a",
        "status": "completed",
        "download_bytes": 8388608,
        "upload_bytes": 8388608,
        "latency_average_ns": 169740,
        "jitter_ns": 349150,
        "connections": 1
      }
    ]
  },
  "results": [
    {
      "test_id": "gs-0516a130caafd78a",
      "status": "completed",
      "target": {
        "server_id": "local-a",
        "selection_method": "auto",
        "network_scope": "local",
        "capabilities": { "supported": true, "server_version": "0.3.0" }
      },
      "download": { "bytes": 8388608, "mbps": 3584.26251922748 },
      "upload": { "bytes": 8388608, "server_confirmed_bytes": 8388608 }
    }
  ]
}
```

### 服务端限额校验（真实运行）

服务端 B 通过 `/capabilities` 公布 `max_connections_per_test = 2`，
客户端请求 4 连接时：

```text
exit=1 status=failed
error=speedtest: request exceeds the server limit: server 0.3.0 allows at most 2 connections per test, requested 4
```

### 服务端并发保护（真实运行）

服务端 B 配置 `--max-concurrent-tests 4`：4 个 2 秒下载占用全部槽位时，
第 5 个请求返回 **503** 并带 `Retry-After: 1`；4 个慢请求全部正常返回 200。

## 测速原理与准确性

```text
Mbps = bytes × 8 / seconds / 1,000,000
```

| 场景 | 测量窗口 |
| --- | --- |
| 单连接下载 | 第一个响应字节到传输停止 |
| 多连接下载 | 首个连接的第一个响应字节到聚合停止 |
| 单连接上传 | 第一次写请求体到服务端确认读完 |
| 多连接上传 | 首个连接第一次写请求体到最后一个服务端确认读完 |

- 多连接速率 = 所有连接在同一共享窗口内的字节总和 / 窗口时长，
  绝不把各连接的时间或 Mbps 相加；
- 使用单调时钟；采样速率按真实观测窗口计算；
- 禁用压缩与缓存；上传必须通过服务端字节数确认；
- 不做任何补偿系数；部分连接失败会记录告警与有效连接数；
- **本地回环测速不代表公网速度**，CLI 明确显示 `Local Loopback Test`；
- 单个 HTTP 测速结果不一定代表运营商接入带宽的理论最大值；
- 测速值稳定不等于绝对准确；波动小只说明当前窗口内相对稳定。

完整口径见 [docs/benchmark.md](docs/benchmark.md)，
多节点架构与安全边界见 [docs/architecture.md](docs/architecture.md)。

## 已知限制

- 只有 HTTP(S) 测速，没有 ICMP / UDP / TCP Connect 测试；
- 节点自动选择只比较延迟，不代表带宽；多节点并行对比尚未实现；
- **没有官方公共测速节点**：唯一正式 Provider 是 `gospeed-http`，必须先自行部署
  GoSpeed 服务端或配置已授权节点，GUI 才会显示可用节点；
- M-Lab 为独立实验（需逐次明确同意），Cloudflare 为隔离实验，二者都不是默认 Provider；
- Latency 为 **HTTP RTT**，不是 ICMP Ping；Jitter 为连续样本差值，不是 RTTVar；
- 桌面 GUI 的原生 Wails 窗口交互（点击 / 曲线 / 取消）尚未在本仓库的自动化回归中
  覆盖，需人工按 [docs/GoSpeed_v0.5.0-B6_原生GUI人工验收清单.md](docs/GoSpeed_v0.5.0-B6_原生GUI人工验收清单.md) 验收；
- 服务端尚无身份验证、全局字节配额、单客户端配额与监控；
- 平台时钟分辨率会让极短回环测量读数为 0（如实显示，不编造）；
- 短阶段可能没有采样统计（N/A）；四分位、历史记录与 CSV 导出尚未实现；
- 桌面 GUI 目前仅支持 Windows（WebView2）；Linux / macOS 桌面端与本地 Web UI 尚未实现；
- 桌面端与 CLI 完全共用引擎与限额校验：高速链路超过服务端单请求字节上限时，
  会如实返回服务端的 413 错误，不做静默裁剪。

## 开发路线图

| 版本 | 内容 |
| --- | --- |
| v0.1.0 | 项目初始化、CLI、本地服务端、HTTP RTT、单连接测速 |
| v0.2.0 | 多连接（1..16）、共享预算与窗口、实时采样、描述性统计 |
| v0.3.0 | 多节点管理、健康探测、能力协商、自动选择、重复测速汇总、并发保护 |
| v0.4.0 | Windows 桌面 GUI（Wails + Vue 3 + ECharts）：一键测速、实时曲线、节点选择、并发/时长/采样配置、取消与统计展示 |
| v0.5.0-beta.1（当前） | 统一 Provider 架构（`internal/provider` + Registry）、统一结果模型与 GUI 结果面板、Node Scope / 指标口径展示、版本信息同步、Cloudflare 实验构建期隔离 |
| v0.6.0 | Linux / macOS 桌面支持、网络接口状态显示、诊断能力 |
| 未来 | ICMP / UDP 测试、负载延迟、Bufferbloat、持续监控 |

详细路线图与“尚未实现”清单见 [docs/roadmap.md](docs/roadmap.md)。

## 开发与测试

```bash
go mod tidy
gofmt -l .            # 期望无输出
go vet ./...
go test ./...
go test -race ./...   # Linux / 已安装 CGO 工具链的环境
go build ./cmd/gospeed
```

桌面模块（独立 Go module，不参与上面的 `./...`）：

```powershell
cd desktop
go vet ./...
go test ./...
go test -race ./...
cd frontend
npm install
npm run test
npm run build
```

Windows 桌面 EXE（需要 Wails CLI v2.16.0 与 WebView2 Runtime）：

```powershell
cd desktop
wails build
# 产物：desktop\build\bin\GoSpeed.exe
```

> Cloudflare 速度测试实验**默认不进入**构建产物：默认 `wails build` /
> `npm run build` 的输出中不含 `CloudflarePocPanel-*.js`，也不含
> `speed.cloudflare.com`；构建脚本会扫描产物并在发现泄漏时直接失败。

## 更新日志

版本变更记录见 [CHANGELOG.md](CHANGELOG.md)。

## 开源许可证

本项目使用 [MIT License](LICENSE)。
