# 更新日志

本项目遵循 [语义化版本](https://semver.org/lang/zh-CN/)。
GoSpeed 目前处于 **Beta（Developer Preview）** 阶段：正式注册的 Provider 只有
`gospeed-http`，且**没有**面向普通用户的官方公共测速节点。

## [Unreleased]

尚未发布的下一版改动。

---

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
- **Windows GUI 上传限额提示**（RC-02）
  - 上传以 `server_limit_reached` 结束时，速率表上方显示提示条：实际测量窗口、
    原计划 Duration、服务端已确认字节、停止原因
  - 明写「不代表完整的 N s 测速」；保留真实 Mbps，不把提前结束冒充完整测速，
    不误报失败，不隐藏 Warning
  - 原计划 Duration 由 GUI 在运行开始时快照，不读取事后的实时输入
  - 按停止原因触发，对下载方向同样生效
- `CHANGELOG.md`

### 修复

- **Duration 模式上传不再因超过服务端字节上限而失败**（RC-01）
  - 客户端协商到 `max_upload_bytes` 后，把它作为共享字节预算，到达即正常收尾，
    不再让服务端以 HTTP 413 中断一次已经完成的传输
  - 停止原因记为 `server_limit_reached`，整体状态仍为 `completed`：这是计划内的停止，
    不是失败，也不上报为取消
  - 实际测量窗口可能短于请求的 Duration；速率只对实际发生的窗口有效，并附 Warning
    说明该速率不描述完整请求窗口
  - 调用方自设的字节预算仍按调用方记账（`requested_bytes_reached`），不会被说成是
    节点的限制；真实的 HTTP 错误依然如实返回
- **Windows CI 测试稳定性**（RC-03-A）
  - 上传字节确认夹具改用仓库既有的 `measurableByteBudget`（16 MiB）。此前 2 MiB 的
    夹具在粗粒度 Windows 时钟上会让整个传输与确认落在同一个 tick 内而失败
  - GUI 线格式测试不再以「1 GiB 上限在 1 s 内跑不完」为前提，改为按实际确认字节数
    与上限的关系判定，机器快慢都不再影响结论
  - 零时长窗口不可测量的真实性约束保持不变：引擎仍然拒绝发布速率，不用伪造时间
    或 Mbps 填补
- RC-02 实现中发现并修正：提示文案误用 Markdown（Vue 模板不渲染）；
  文案出现无意义的 "HTTP 413" 字样（用户从未见过该错误）
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