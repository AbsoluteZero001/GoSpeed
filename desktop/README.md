# GoSpeed Desktop（Wails + Vue 3）

Windows 桌面客户端。它只是 `internal/speedtest` 测速引擎的界面层：
不包含任何测速公式、字节预算、计时或并发逻辑，所有测量、采样与统计都来自 CLI
使用的同一套引擎。

## 环境要求

- Go 1.25 或更高（`desktop/go.mod` 要求 1.25.0；本地使用 go1.27.2 验证）
- Node.js 20.19+ / 22.12+（本地使用 Node v24.15.0、npm 11.12.1 验证）
- Wails CLI v2.16.0（Wails v2 稳定线）：`go install github.com/wailsapp/wails/v2/cmd/wails@v2.16.0`
- Windows WebView2 Runtime（Windows 10/11 通常已随 Edge 安装；本机验证版本 154.0.4258.62）

## 构建

```powershell
# 在仓库根目录
go install github.com/wailsapp/wails/v2/cmd/wails@v2.16.0
cd desktop
wails build            # 前端 + Go 一起构建
```

产物：`desktop/build/bin/GoSpeed.exe`，可直接双击运行。

## Cloudflare Speedtest 隔离 PoC

Cloudflare 实验入口默认关闭。只有在前端构建时显式设置以下变量，界面才会动态加载
`@cloudflare/speedtest@1.14.1` 实验面板：

```powershell
cd desktop
$env:VITE_CLOUDFLARE_SPEEDTEST_POC = "true"
wails build
Remove-Item Env:VITE_CLOUDFLARE_SPEEDTEST_POC
```

实验面板不会自动开始测速。每次点击开始都会向 Cloudflare 公共端点发送真实的公网
下载、上传和延迟请求，应用层配置为下载 24 MiB、上传 16 MiB，合计 40 MiB；
TLS/TCP/IP 开销、重传和 SDK 的有限 HTTP 429 重试可能使实际网络流量更高。

PoC 运行期间会在内存中记录请求级诊断信息，并在测速完成后提供脱敏 JSON 导出。
记录内容仅包含请求类型、方法、状态、错误类型和耗时等元数据，不记录公网 IP、
完整 URL、Authorization、Cookie 或请求/响应正文。遥测不会新增网络请求。

P0-F 实验保护默认限制单请求 20 秒、整次运行 90 秒。超时会产生独立的 timeout
或 partial 诊断，不会自动重试，也不会把不完整结果显示为完整成功。

只验证后端（不需要 Wails CLI；需要先构建一次前端，
因为 Go 入口会嵌入 `frontend/dist`）：

```powershell
cd desktop
cd frontend; npm ci; npm run build; cd ..
go vet ./...
go test ./...
```

前端单独构建（类型检查 + Vite 打包）：

```powershell
cd desktop/frontend
npm install
npm run build
npm test        # Vitest：目标类型提示与指标标注的渲染测试
```

## 运行行为

- 启动时**不会**自动测速，也**不会**启动任何监听端口的测速服务端；
  它只读取节点配置并显示界面。
- 节点配置查找顺序：当前工作目录、EXE 所在目录下的
  `configs/nodes.json` → `configs/nodes.example.json`，都没有时回退到内置
  `local` 节点（`http://127.0.0.1:8080`）。
- 目标类型提示按地址分类显示，措辞由后端 `desktop/notice.go` 统一生成：
  本机回环（`127.0.0.1` / `::1` / `localhost`）标记为“本机回环性能测试”，
  速率指标标注“本机吞吐量”并说明“不代表真实宽带速度”；局域网节点显示
  “局域网测速，不代表互联网宽带速度”；公网字面地址显示“公网目标测速，结果
  受到服务器带宽、路由、网络拥塞和测速配置影响”；域名目标保持“路径未知”，
  不因为地址字符串就宣称流量走了公网。仅配置本机节点时，界面明确提示还没有
  测得真实宽带速度。
- 测速需要你自己启动服务端：`gospeed server --addr 127.0.0.1:8080`。
- 测速进度通过 Wails 事件推送（`gospeed:state` / `gospeed:target` /
  `gospeed:progress` / `gospeed:finished` / `gospeed:nodes`），前端不轮询状态。
- 取消测速或取消节点检查会真实取消 `context`；关闭窗口时会先取消并等待引擎
  返回，再退出进程。

## 已知行为

- 本机回环的 HTTP RTT 可能低于平台时钟分辨率，界面按项目约定显示
  `N/A（低于平台时钟分辨率）`，而不是 `0.00 ms`。
- 单请求上限由服务端公布：GoSpeed 服务端默认限制单请求上传 1 GiB、下载 4 GiB。
  若高速链路在时长窗口内超过该上限，上传阶段会收到服务端真实的 `413`，
  界面原样显示该错误，不会裁剪用户请求，也不会伪造结果。
- 服务端公布的连接数 / 时长 / 字节限额超过时，引擎在开始前直接报错（不静默裁剪）。

## 目录

```text
desktop/
├── main.go            # Wails 应用入口（不启动测速、不启动服务端）
├── app.go             # Wails binding：GetAppInfo/ListNodes/CheckNodes/StartTest/CancelTest
├── runner.go          # 任务编排：目标解析、事件推送、取消与资源释放
├── types.go           # 事件与 binding 载荷
├── config.go          # 节点配置查找（复用 internal/nodes）
├── nodes.go           # 节点视图与探测结果转换
├── wails.json         # Wails 工程配置（输出 GoSpeed.exe）
└── frontend/          # Vue 3 + TypeScript + Vite + ECharts
```
