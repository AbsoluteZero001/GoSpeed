# 更新日志

本项目遵循 [语义化版本](https://semver.org/lang/zh-CN/)。
GoSpeed 目前处于 **Beta（Developer Preview）** 阶段：正式注册的 Provider 只有
`gospeed-http`，且**没有**面向普通用户的官方公共测速节点。

## [Unreleased]

尚未发布的下一版改动。

### 新增

- **M-Lab NDT7 接入 Windows 桌面 GUI**（WAN-01-GUI-INTEGRATION）
  - `mlab-ndt7` Provider 注册进桌面版 Registry。注册不等于启用：未同意
    隐私告知前，Provider 拒绝节点发现、能力协商与任何测速，零网络流量
  - 三道独立的门，全部在后端强制：持久化同意（`ndt7-notice-v3`，版本变化
    后旧同意失效）、单次测速授权（绑定节点与方向，用后即焚）、每日 4 次
    上限（M-Lab AUP 建议值，按本机自然日持久化，重启与重载窗口都不清零）
  - 节点通过 M-Lab Locate v2 自动发现，该查询同样受同意门约束
  - 前端只接收不含 Endpoint / Token 的安全节点视图，用后端签发的不透明
    `Ref` 引用节点；测速时由后端 `endpointFor(ref)` 解析真实服务地址
  - GUI 展示完整告知、每日剩余次数、单次流量预算与逐次（下载 / 上传分别）
    授权确认；NDT7 无实时采样曲线时如实说明
  - 结果沿用统一 `FinalResult` 与 Provider 结果面板：TCP MinRTT 标注为
    `tcp_min_rtt`，Jitter 保持 N/A，超时 / 取消 / 预算触发分别展示
- `internal/provider/ndt7` 新增 `ConsentVersion()`：客户端需要知道当前告知
  文本版本，才能判断已保存的同意是否仍然有效

### 修复（提交前工程审计 WAN-01-GUI-AUDIT）

审计授权链路、每日上限、Token 生命周期、会话清理与 HTTP Provider 兼容性，
发现并修复 4 个缺陷（每个都有能复现问题的测试先行）：

- **节点发现不再与测速并发**：发现改为占用 GUI 会话，与测速/节点检查互斥；
  此前并发发现发布的节点会被该测速结束时的清理抹掉
- **撤销同意立即终止正在进行的 M-Lab 测速**：此前撤回授权后测速照常完成，
  数据仍在发布；现在只取消 M-Lab Provider 服务的任务，自建测速不受影响
- **同一测速的重复确认不再叠加授权**：桌面侧授权改为幂等，一次确认只买一次
  测速（此前连续确认两次会得到两张授权）
- **同意记录写失败时不开门**：改为先落盘再通知 Provider，界面与 Provider 不再
  出现"一个认为已同意、另一个认为未同意"的状态
- M-Lab 节点发现失败时给出中文说明（原来直接透出 Provider 的英文哨兵错误）

### 变更（提交前工程审计）

- `internal/provider/ndt7` 的 User-Agent 不再自带版本常量：改为引用
  `internal/version.Version`，并删除包内重复的第二份常量。User-Agent 现在是
  `GoSpeed/0.5.0-beta.1`，与 GUI、CLI、服务端一致。告知文本第 3 条使用的是
  `GoSpeed/<version>` 占位符，措辞未变，因此**告知版本不提升**，已存同意继续有效。
  历史实验 PoC 未改动
- `PRIVACY.md` §3.3 补充说明：每日次数限制是**客户端本地自律**，不是服务端
  不可绕过的配额，修改本机计数文件或系统时间可以绕过它
- 新增 `docs/WAN-01-GUI-ACCEPTANCE.md`：26 项 GUI 人工验收清单，
  每项含前置条件、操作步骤、预期结果、实际结果与状态；**全部标记为待人工执行**
- `.gitignore`：把新的验收清单加入 `docs/` 的白名单（该目录默认被忽略，
  只有明确列出的交付文档才会进入版本库）
- 审查修复（WAN-01-GUI-FIXES）：
  - **统一结果模型区分两种字节**：`Rate.ObservedBytes`（新增，实际观测字节，
    来自后端 socket 层计数，Provider 无法报告时为 null）与
    `Rate.TransferredBytes`（测量窗口覆盖的有效测速字节）；本次运行的
    `FinalResult.TransferredBytes` 改为"真正传输的字节"。预算耗尽但未形成
    测量窗口时，界面不再显示"传输 0 B"，而是说明"传输确实发生了，但没有
    形成有效测量窗口，因此没有发布速率"。自建 Provider 的呈现不变
  - **授权不再由桌面维护第二本账**：Provider 新增只读查询
    `HasRunAuthorization`，桌面直接向 Provider 确认"这次测速是否已确认"，
    从结构上消除了两本账分叉导致"幽灵授权"的可能
  - 会话记录归一化后的 Provider 名；告知中的流量数字（24/16/40 MiB）与实际
    预算常量绑定测试；`PRIVACY.md` 修正授权与每日名额的消耗时机表述；
    补齐 6 个文件的行尾换行
  - **预算未变**（24/16/40 MiB）、**`consentVersion` 未变**、速率未被伪造、
    授权与额度机制未被削弱
- 验收准备（WAN-01-GUI-ACCEPTANCE）：新增 `desktop/acceptance_test.go`，26 项验收
  测试与清单编号一一对应，全部离线（测量打在本地 NDT7 协议 mock 上）；新增前端
  `SpeedChart.spec.ts`（NDT7 不提供实时采样曲线的说明渲染）。
  `docs/WAN-01-GUI-ACCEPTANCE.md` 已更新为实测版：20 项 PASS、3 项 PASS_PARTIAL、
  1 项 BLOCKED（真实节点发现需公网，明令未执行），**公网额度消耗 0**

### 变更

- `PRIVACY.md` 1.2 → **1.3**：M-Lab 模块已内置于桌面版但在同意前完全惰性；
  补充 GUI 侧两道门的交互形态与本地保存内容的清除时机
- `README.md`：新增 M-Lab NDT7 公共测速说明；同步测试数量；修正上一个提交
  删除 `docs/architecture.md`、`docs/benchmark.md`、`docs/roadmap.md` 后残留的
  三处失效链接
- 桌面模块新增 `m-lab/locate`、`m-lab/ndt7-client-go` 依赖（随
  `internal/provider/ndt7` 引入，间接依赖）

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