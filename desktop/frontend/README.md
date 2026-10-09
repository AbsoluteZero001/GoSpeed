# GoSpeed Desktop 前端

Vue 3 + TypeScript + Vite + ECharts。这里只负责渲染：所有测量数据都来自
Wails 事件（`gospeed:state` / `gospeed:target` / `gospeed:progress` /
`gospeed:finished` / `gospeed:nodes`），前端不轮询，也不生成任何测速数值。

```bash
npm install
npm run build   # vue-tsc 类型检查 + vite 打包到 dist/
npm run dev     # 仅用于界面调试；没有 Wails binding 时会提示未连接后端
```

`wailsjs/` 由 `wails build` 自动生成（Wails binding），不要手工修改。

结构：

```text
src/
├── App.vue                     # 页面状态机：订阅事件、驱动实时指标与图表
├── bridge.ts                   # 唯一的 Wails binding / 事件入口
├── types.ts                    # 后端事件与 speedtest.Result 的 TS 类型
├── format.ts                   # 数值格式化：未知值一律 N/A
└── components/
    ├── ControlPanel.vue        # 节点选择、并发、时长、采样间隔、开始/取消
    ├── NodePanel.vue           # 节点列表、健康状态、健康检查
    ├── MetricsPanel.vue        # 下载/上传/Ping/Jitter 与阶段进度
    ├── SpeedChart.vue          # 实时速率曲线（ECharts）
    ├── LatencyChart.vue        # HTTP RTT 采样曲线（ECharts）
    └── ResultPanel.vue         # 目标、延迟、下载/上传统计、连接明细、能力协商
```
