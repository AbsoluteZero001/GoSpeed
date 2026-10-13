# 更新日志

本项目遵循 [语义化版本](https://semver.org/lang/zh-CN/)。
GoSpeed 目前处于 **Beta（Developer Preview）** 阶段：正式注册的 Provider 只有
`gospeed-http`，且**没有**面向普通用户的官方公共测速节点。

## [Unreleased]

尚未发布的下一版改动。

### 修复

- **纯 Duration 模式下的上传字节上限协调**（RC-01）
  - 此前在未设置字节预算时，高速链路会在服务端 `max_upload_bytes` 处收到
    `HTTP 413 Request Entity Too Large`，整个上传阶段记为失败，一个本已完成的
    传输被丢弃
  - 现在引擎会把 `GET /capabilities` 声明的上限作为上传阶段的额外停止条件，
    传输在该处正常结束，服务端确认每一个字节
  - 停止原因新增 `server_limit_reached`，并在 `warnings` 中说明窗口短于请求，
    以及如何覆盖完整窗口
  - 结果为 `completed`：`Completeness=Complete`、`Stability=degraded`，
    `Trust` 保持保守的 `normal`
  - 调用方**显式**请求的字节预算若超过服务端上限，仍然在传输前被拒绝
    （`ErrServerLimitExceeded`），不会静默裁剪
  - 下载侧行为不变：`/download` 的字节上限只在客户端声明 `bytes=N` 时生效，
    Duration 模式下服务端不做强制

---

尚无已发布的 v0.5.0 正式版本。以下条目为 `v0.5.0-beta.1` 候选内容。

## [0.5.0-beta.1] - 未发布

### 新增

- **统一 Provider 测量架构**（`internal/provider`）
  - Provider 接口：`ID` / `Negotiate(ctx, node)` / `Run(ctx, request)`，
    全部取消操作基于 `context.Context`
  - 统一结果模型 `FinalResult`，区分
    `completed` / `failed` / `cancelled` / `timeout` / `budget_exceeded` /
    `consent_required`
  - Provider Registry 与 `desktop/providerbridge.go` 桥接层
- **`gospeed-http` Provider**：直接复用 `internal/speedtest` 引擎，
  不复制测速协议解析器、不重复实现速率公式
- **Windows GUI 结果面板**：展示真实 Mbps、Latency / Jitter 口径、Provider 来源、
  节点范围（Node Scope）与结果质量；正确处理 N/A、取消、超时、错误与部分完成
- `CHANGELOG.md`

### 修复

- 修复 Windows 桌面 CI 作业失败（`Desktop GUI (windows)`）
- 修复 URL 凭据脱敏未覆盖查询串与片段（`#fragment`）的问题
- 修复 Windows EXE 版本信息：`VS_FIXEDFILEINFO.ProductVersion` 之前恒为
  `0.0.0.0`（`info.json` 缺少 `fixed.product_version`），并补充缺失的
  `FileVersion` 字符串键；版本号由 `0.4.0` 同步到 `0.5.0-beta.1`

### 变更

- Cloudflare 速度测试实验改为**构建期隔离**：默认 `wails build` /
  `npm run build` 产物不再包含 `CloudflarePocPanel-*.js` 与
  `speed.cloudflare.com`；构建脚本会扫描产物并在发现泄漏时失败。
  该实验仍不是默认 Provider，也未注册到 Registry
- Latency 统一标注为 **HTTP RTT**，不冒充 ICMP Ping；
  Jitter 统一为连续样本差值口径，RTTVar 不再冒充 Jitter
- 速率单位统一为真实 Mbit/s，消除 kbit/s 与 Mbps 混用
- 版本号同步点：`internal/version`、CLI JSON 保持向后兼容

### 已知限制

- 没有官方公共测速节点；需自行部署 GoSpeed 服务端或配置已授权节点
- 原生 Wails 窗口的交互式验收仍需人工执行（见人工验收清单）
- M-Lab 为独立实验功能（需逐次明确同意），不随默认构建分发

## [0.4.0]

- Windows 桌面 GUI（Wails v2.16.0 + Vue 3 + TypeScript + ECharts + WebView2）：
  一键测速、实时速率曲线、HTTP RTT 采样曲线、节点自动与手动选择、
  并发 / 时长 / 采样间隔配置、可取消任务、完整统计展示

## [0.3.0]

- 多节点管理、健康探测、Capability 协商、自动选择、重复测速汇总、
  服务端并发保护

## [0.2.0]

- 多连接并发下载 / 上传（1..16）、共享字节预算与统一测量窗口、
  实时速率采样与描述性统计

## [0.1.0]

- 项目初始化、CLI、本地 HTTP 测速服务端、HTTP RTT、单连接测速