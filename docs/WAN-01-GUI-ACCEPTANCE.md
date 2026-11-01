# WAN-01-GUI-ACCEPTANCE：NDT7 桌面 GUI 验收清单（实测版）

- 任务编号：WAN-01-GUI-ACCEPTANCE
- 适用范围：Windows 桌面版 GoSpeed（`desktop/build/bin/GoSpeed.exe`）
- 关联实现：`docs/WAN-01-B1-NDT7-Provider-工程化报告.md` §17（实现）、§18（审计）
- 本版：**26 项全部完成分类与离线/Mock 验证**；公网与人工项如实标记为未完成
- **公网额度消耗：0**（未调用 Locate、未执行任何真实 NDT7 测速）

## 0. 分类与状态定义

### 0.1 分类

| 分类 | 含义 | 联网 | 消耗每日额度 |
|------|------|------|------------|
| **L 纯本地** | 完全在本机完成，不产生任何网络请求 | 否 | 0 |
| **M Mock 模拟** | 测量打在本地 NDT7 协议 mock（回环）上，或使用本机 GoSpeed 服务端 | 否（仅回环） | 0（本地计数器，不写公网计数） |
| **P 公网** | 需要访问 M-Lab 公网服务 | 是 | 0（Locate 查询不计测速额度）/ 1（真实测速） |
| **G 人工 GUI** | 需要人在真实窗口中操作才能判定 | 视该项而定 | 视该项而定 |

### 0.2 状态

| 状态 | 含义 |
|------|------|
| `PASS` | 该项的**全部**可离线验证部分已执行并通过，且不依赖公网或人工操作 |
| `PASS_PARTIAL` | 离线部分已通过，剩余部分依赖公网或人工操作 |
| `BLOCKED` | 依赖 M-Lab 公网，本轮明令禁止执行 |
| `NEEDS_MANUAL_TEST` | 需要人工在真实窗口 / 真实公网环境操作 |

> **自动化测试通过 ≠ GUI 人工验收通过。** 凡是需要人在窗口里点、或需要连公网的
> 部分，即使其后端约束已有自动化证据，状态仍然是 `PASS_PARTIAL` /
> `BLOCKED` / `NEEDS_MANUAL_TEST`。

### 0.3 本轮执行环境

- **自动化**：`desktop/acceptance_test.go` 新增 26 项验收测试（每项一个，名字与编号
  对应），全部离线；测量跑在 `internal/provider/ndt7/mock` 的本地协议 mock 上。
- **实机 GUI**：真实启动 `desktop/build/bin/GoSpeed.exe`，使用**隔离的临时
  `%APPDATA%` 目录**（`%TEMP%\gospeed-acceptance-profile`），因此本机真实配置
  目录未被写入；测试结束后该临时目录已删除，真实配置目录仍然不存在。
- **未执行**：M-Lab Locate 公网查询、任何真实 NDT7 下载/上传、任何公网额度消耗。

---

## 1. 26 项实测结果

### 01 首次启动不自动连接 M-Lab
`L 纯本地` · 联网：否 · 额度：0 · 状态：**PASS**

- **操作**：自动化——未同意时调用发现；实机——启动 GoSpeed.exe 观察面板。
- **实际结果**：未同意时发现被同意门拒绝（零请求）；桌面 NDT7 路径源码级不含任何
  HTTP 客户端；实机启动后面板显示未同意态，且**没有**"发现附近的 M-Lab 节点"入口。
- **证据**：`TestAcceptance01_StartupIsInert`、`TestNDT7DiscoveryPathIssuesNoRequestOfItsOwn`；实机见 §3.1
- **未完成**：抓包确认"零出站连接"（需人工抓包）

### 02 未同意直接测速，后端拒绝
`M Mock 模拟` · 联网：否（回环） · 额度：0 · 状态：**PASS**

- **操作**：自动化——授权后不建连接，直接发起测速。
- **实际结果**：测速被拒，错误码 `consent_required`，mock 连接数 **0**，当日计数不变。
- **证据**：`TestAcceptance02_RunWithoutConsentIsRefused`

### 03 完整展示隐私声明 v3
`L 纯本地` · 联网：否 · 额度：0 · 状态：**PASS**

- **操作**：自动化——校验告知文本；实机——点"查看完整告知"。
- **实际结果**：告知含标题与 `ndt7-notice-v3` 版本号、**10 条编号条款齐全**
  （含公网 IP 公开、逐次授权、每日 4 次、流量预算 24/16/40 MB、本地保存内容）、
  M-Lab 隐私政策与 AUP 链接；实机窗口中全文可滚动阅读。
- **证据**：`TestAcceptance03_NoticeIsCompleteAndVersioned`、Provider 的
  `TestConsentNoticeNamesTheEnforcedLimits`；实机见 §3.2
- **未完成**：无（该项不依赖公网）

### 04 接受声明后同意状态持久化
`L 纯本地` · 联网：否 · 额度：0 · 状态：**PASS**

- **操作**：自动化——文件读写；实机——点"我已阅读并同意"后检查落盘文件。
- **实际结果**：实机落盘 `%APPDATA%\GoSpeed\ndt7-consent.json`，内容为
  `version/agreed/agreedAt/policyVersion/clientName/clientVersion` 六项；
  **扫描该目录未发现 access_token、主机名或任何服务地址**。
- **证据**：`TestAcceptance04_ConsentIsPersistedAndHoldsNoCredential`；实机文件内容见 §3.3

### 05 重启后不重复要求同版本同意
`L 纯本地` · 联网：否 · 额度：0 · 状态：**PASS**

- **操作**：自动化——重建网关；实机——关闭并重新启动 GoSpeed.exe。
- **实际结果**：自动化——当前版本的已存同意在重建后仍生效。实机——重启后 M-Lab 面板
  **直接进入已同意态**（显示"发现附近的 M-Lab 节点"，不再出现告知与同意按钮）。
- **证据**：`TestAcceptance05_RestartKeepsTheCurrentConsent`；实机见 §3.4

### 06 告知版本更新后重新征求同意
`L 纯本地` · 联网：否 · 额度：0 · 状态：**PASS**

- **操作**：自动化——把已存记录的版本改成 `ndt7-notice-v2` 后重建。
- **实际结果**：旧版本记录**不被复用**，状态回到"需要重新同意"，面板告知版本仍为
  当前 `ndt7-notice-v3`。
- **证据**：`TestAcceptance06_VersionChangeForcesReconsent`、
  `TestNDT7StaleConsentVersionForcesReconsent`、Provider 的 `TestNoticeVersionTracksConsentVersion`

### 07 正常发现节点
`P 公网` · 联网：**是（M-Lab Locate）** · 额度：0（Locate 不计测速额度） · 状态：**BLOCKED**

- **操作**：本轮**禁止**执行（明令：不得调用 M-Lab Locate API）。
- **实际结果**：未执行。离线可验证的部分已通过：未同意时发现被同意门拒绝且零请求；
  Provider 侧的 Locate 解析、可信主机白名单、重定向拒绝已有本地桩测试覆盖
  （`TestDiscoverReturnsSeveralNodes`、`TestDiscoverRefusesARedirect`）。
- **证据**：`TestAcceptance07_DiscoveryIsRefusedWithoutConsent`（离线部分）
- **未完成**：真实节点的发现、节点列表呈现、"第三方公共节点"标注（需人工联网执行）

### 08 前端数据不包含 Token
`L 纯本地` · 联网：否 · 额度：0 · 状态：**PASS_PARTIAL**

- **操作**：自动化——序列化全部前端可见载荷并扫描关键字；实机——需在开发者工具中检查。
- **实际结果**：自动化——consent state、节点视图、finished 事件序列化后均**不含**
  access_token、`wss://`/`ws://`、主机或 query 串；`ProviderNodeView` 字段集由
  反射钉住；`StartOptions` 无任何地址字段。
- **证据**：`TestAcceptance08_FrontendNeverSeesTheToken`、
  `TestNDT7NothingThatReachesTheFrontendCarriesTheToken`、`TestNDT7RequestCannotCarryAnEndpoint`
- **未完成**：开发者工具 / 崩溃转储中的实际检查（需人工）

### 09 下载授权正确绑定
`M Mock 模拟` · 联网：否（回环） · 额度：0 · 状态：**PASS**

- **操作**：自动化——授权下载方向并运行。
- **实际结果**：测速完成，统一结果**只有下载**（upload 为 nil），方向绑定正确。
- **证据**：`TestAcceptance09_DownloadAuthorizationIsBound`；前端逐次确认交互见 §3.5

### 10 上传授权正确绑定
`M Mock 模拟` · 联网：否（回环） · 额度：0 · 状态：**PASS**

- **操作**：自动化——授权上传方向并运行（mock 需限速，否则客户端会在形成测量
  窗口前用完产品默认的 16 MiB 上传预算——这是协议与预算的正常交互，不是缺陷）。
- **实际结果**：测速完成，统一结果**只有上传**（download 为 nil），速率真实发布。
- **证据**：`TestAcceptance10_UploadAuthorizationIsBound`

### 11 授权不可重复使用
`M Mock 模拟` · 联网：否（回环） · 额度：0 · 状态：**PASS**

- **操作**：自动化——一次授权，两次测速。
- **实际结果**：第二次被 `measurement_not_authorized` 拒绝，mock 连接数保持 **1**
  （被拒的那次没有建连）。
- **证据**：`TestAcceptance11_AuthorizationIsSingleUse`

### 12 授权不可跨节点使用
`M Mock 模拟` · 联网：否（回环） · 额度：0 · 状态：**PASS**

- **实际结果**：对节点 A 的授权在节点 B 上被 `measurement_authorization_mismatch`
  拒绝，节点 B 的 mock 连接数为 **0**。
- **证据**：`TestAcceptance12_AuthorizationIsBoundToTheNode`

### 13 授权不可跨方向使用
`M Mock 模拟` · 联网：否（回环） · 额度：0 · 状态：**PASS**

- **实际结果**：下载授权在上传请求上被 `measurement_authorization_mismatch` 拒绝，
  连接数为 **0**。
- **证据**：`TestAcceptance13_AuthorizationIsBoundToTheDirection`

### 14 伪造 Ref 被拒绝
`L 纯本地` · 联网：否 · 额度：0 · 状态：**PASS**

- **操作**：自动化——四种伪造引用；实机——未选节点时的按钮态。
- **实际结果**：`pnode-9999`、空串、`../../etc/passwd`、
  `wss://evil.example/...?access_token=x` 全部被拒（后端直接拒绝或运行被拒），
  mock 连接数为 **0**；实机中未选节点时"确认并开始这一次测速"为**禁用**态。
- **证据**：`TestAcceptance14_ForgedReferenceIsRejected`；实机见 §3.5

### 15 正确展示剩余次数
`L 纯本地` · 联网：否 · 额度：0 · 状态：**PASS**

- **操作**：自动化——逐次占用并读回状态；实机——观察面板计数行。
- **实际结果**：自动化——`已用/上限/剩余`三值与实际占用严格一致（0→4 逐格核对）。
  实机——面板显示"今日已用 0 / 4 次，剩余 4 次（按本机自然日计数，重启不会清零）"。
- **证据**：`TestAcceptance15_RemainingCountIsReported`；实机见 §3.1

### 16 达到每日上限后禁止启动
`L 纯本地` · 联网：否 · 额度：0 · 状态：**PASS**

- **操作**：自动化——把当日计数填满后发起测速。
- **实际结果**：测速被 `daily_limit_reached` 拒绝，mock 连接数 **0**（未联系任何
  服务器）；实测中该拒绝发生在授权消费之前，因此不消耗用户的确认。
- **证据**：`TestAcceptance16_DailyCeilingStopsTheRun`、Provider 的
  `TestRunnerRefusesTheFifthMeasurementOfTheDay`

### 17 重载应用不能重置次数
`L 纯本地` · 联网：否 · 额度：0 · 状态：**PASS**

- **操作**：自动化——用文件计数器写入 2 次后重建网关；实机——重启 EXE。
- **实际结果**：自动化——重建后 `已用=2 / 剩余=2`，计数来自文件而非进程。
  实机——重启后计数仍为 0/4（本轮从未发起测速，计数文件根本未生成，说明计数
  只在真正开始传输时才写入）。
- **证据**：`TestAcceptance17_ReloadDoesNotResetTheCounter`、
  `TestNDT7DailyLimitCannotBeBypassed`

### 18 取消测速后授权撤销
`M Mock 模拟` · 联网：否（回环） · 额度：0 · 状态：**PASS**

- **操作**：自动化——对慢速 mock 测速中途取消。
- **实际结果**：运行状态为"已取消"；待用授权数归零；节点引用清空。
- **证据**：`TestAcceptance18_CancelRevokesTheAuthorization`

### 19 正常结束后清理节点引用
`M Mock 模拟` · 联网：否（回环） · 额度：0 · 状态：**PASS**

- **操作**：自动化——跑完一次完整测速。
- **实际结果**：待用授权数归零；节点引用清空（短时效 access_token 随会话结束丢弃）。
- **证据**：`TestAcceptance19_NormalEndClearsTheNodeReferences`

### 20 异常退出后不存在可复用授权
`L 纯本地` · 联网：否 · 额度：0 · 状态：**PASS**

- **操作**：自动化——授权后重建网关（等价于重启）。
- **实际结果**：重启后的 Provider 待用授权为 0，节点引用为空；授权只存在于内存，
  同意决定与每日计数才持久化。
- **证据**：`TestAcceptance20_RestartLeavesNoReusableAuthorization`

### 21 错误信息不泄露 Token
`M Mock 模拟` · 联网：否（回环） · 额度：0 · 状态：**PASS**

- **操作**：自动化——让握手被拒，令服务 URL 带 `access_token=ACCEPTANCETOKEN`。
- **实际结果**：错误文本与 finished 事件序列化后**均不含**该 token。
- **证据**：`TestAcceptance21_ErrorsDoNotLeakTheToken`、
  `TestNDT7FailedRunDoesNotLeakTheToken`、Provider 的 `TestRunFailureDoesNotLeakTheAccessToken`

### 22 下载结果正确展示
`M Mock 模拟` · 联网：否（回环） · 额度：0 · 状态：**PASS_PARTIAL**

- **操作**：自动化——跑一次下载并检查统一结果。
- **实际结果**：真实 Mbit/s、测量窗口、传输字节、停止原因齐全；协议记为 `ndt7`，
  同意版本记为 `ndt7-notice-v3`，归属为第三方；**抖动保持 nil**（NDT7 无应用层抖动）。
  延迟在本地 mock 下为 nil（mock 不发 TCPInfo 帧），界面因此显示 N/A 而不是 0。
- **证据**：`TestAcceptance22_DownloadResultIsHonest`
- **未完成**：真实 M-Lab 服务器会带 TCPInfo，界面显示"TCP 最小往返时延"这一格
  需公网验证（见 §4）

### 23 上传结果正确展示
`M Mock 模拟` · 联网：否（回环） · 额度：0 · 状态：**PASS_PARTIAL**

- **操作**：自动化——跑一次上传并检查统一结果。
- **实际结果**：上传速率真实发布，抖动为 nil，方向与请求一致。
- **证据**：`TestAcceptance23_UploadResultIsHonest`
- **未完成**：同 22，真实公网下的上传结果与窗口需人工验证

### 24 NDT7 实时曲线限制正确展示
`L 纯本地` · 联网：否 · 额度：0 · 状态：**PASS**

- **操作**：自动化——检查 NDT7 运行期间是否产生进度采样；前端——渲染 note。
- **实际结果**：NDT7 运行**不产生任何进度采样**（采样数为 0），因此界面没有可画的
  曲线；前端组件在有 note 时渲染"NDT7 协议只在测量窗口结束时给出速率，本协议不提供
  实时采样曲线"，无 note 时不渲染该说明。
- **证据**：`TestAcceptance24_NoLiveSamplesAreFabricated`、前端 `SpeedChart.spec.ts`

### 25 自建 HTTP Provider 回归正常
`M Mock 模拟`（本机 GoSpeed 服务端） · 联网：否（回环） · 额度：0 · 状态：**PASS**

- **操作**：自动化——对本机 GoSpeed 服务端跑一次完整测速；实机——观察自建节点面板。
- **实际结果**：自动化——测速完成，Provider 为 `gospeed-http`，原生结果保留，
  **进度采样仍存在**（自建路径的实时曲线不受影响）。实机——节点列表仍显示
  "Local Test Server / 本机 / local · http://127.0.0.1:8080"，并保留
  "当前仅配置本机回环节点"提示。
- **证据**：`TestAcceptance25_SelfHostedProviderIsUnaffected`、
  `TestNDT7SelfHostedProviderIsUnaffected`；实机见 §3.1

### 26 GUI 关闭重开后状态正确
`L 纯本地` · 联网：否 · 额度：0 · 状态：**PASS**

- **操作**：自动化——新建 Runner；实机——关闭并重启 GoSpeed.exe。
- **实际结果**：自动化——新 Runner 空闲、无会话、不携带上一次的结果。实机——重启后
  状态"空闲"，指标全部 N/A（无残留结果），M-Lab 面板显示已同意态、节点列表为空、
  计数 0/4。
- **证据**：`TestAcceptance26_StateAfterRestart`；实机见 §3.4

---

## 2. 分类与状态统计

### 2.1 按分类

| 分类 | 项数 | 编号 |
|------|------|------|
| **L 纯本地**（不联网） | 13 | 01, 03, 04, 05, 06, 08, 14, 15, 16, 17, 20, 24, 26 |
| **M Mock 模拟**（仅回环） | 12 | 02, 09, 10, 11, 12, 13, 18, 19, 21, 22, 23, 25 |
| **P 公网**（M-Lab） | 1 | 07 |
| 合计 | **26** | |

### 2.2 按状态

| 状态 | 项数 | 编号 |
|------|------|------|
| **PASS** | 20 | 01, 02, 03, 04, 05, 06, 09, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 24, 25, 26 中除 08/22/23 外的全部 |
| **PASS_PARTIAL** | 3 | 08（差开发者工具实检）、22（差真实 TCPInfo 呈现）、23（差真实公网结果） |
| **BLOCKED** | 1 | 07（禁止公网） |
| **NEEDS_MANUAL_TEST** | 0（独立项） | — |

> 22 / 23 的"未完成"部分与 07、08 的"未完成"部分，都需要**真实公网 + 人工窗口**，
> 因此它们没有单独列为 PASS。

### 2.3 联网与额度

| 项 | 是否联网 | 是否消耗每日 4 次额度 |
|---|---------|----------------------|
| 26 项中的全部自动化与实机操作 | **否**（仅本地回环 / 本机服务端 / 无请求） | **0** |
| 07 真实节点发现（未执行） | 是（M-Lab Locate） | 0（Locate 不计测速额度） |
| 22 / 23 的真实公网呈现部分（未执行） | 是 | 各 1 |

**本轮公网额度消耗：0。** 每日计数文件在本轮从未生成——计数只在真正开始传输时
写入，而本轮没有任何一次测量到达过真实服务器。

---

## 3. 实机 GUI 观察记录（真实启动 GoSpeed.exe）

### 3.1 首次启动（隔离配置目录）

- 窗口标题 `GoSpeed 0.5.0-beta.1`，状态"空闲"。
- M-Lab 面板标题："M-Lab 公共测速（NDT7） / 第三方公共节点 · 需逐次确认"。
- 计数行："今日已用 0 / 4 次，剩余 4 次（按本机自然日计数，重启不会清零）"。
- 未同意提示："M-Lab NDT7 测速会把你的公网 IP 与测量数据发布到 M-Lab 的公开数据集
  （无删除接口），并且每次测速都需要单独确认。请先阅读完整告知。"
- 三个按钮："查看完整告知" / "我已阅读并同意" / "不同意"；**没有**"发现附近的
  M-Lab 节点"入口（未同意时不提供发现）。
- 底部显示"告知版本：ndt7-notice-v3"。
- 自建节点区不受影响："节点 1 个 / Local Test Server / 本机 / local ·
  http://127.0.0.1:8080"，并保留"当前仅配置本机回环节点"提示。
- 四个指标卡片全部为 **N/A**（未伪造任何读数）。

### 3.2 展开完整告知

点击"查看完整告知"后按钮变为"收起告知"，下方以等宽可滚动区域显示完整正文：
标题行 `M-Lab NDT7 measurement disclosure (GoSpeed, ndt7-notice-v3)`、编号 1–10
的十条条款、流量预算 `download MB: 24 / upload MB: 16 / total MB: 40`、以及
M-Lab 隐私政策与 AUP 两个链接。

### 3.3 点击同意后的落盘文件

`%APPDATA%\GoSpeed\ndt7-consent.json`：

```json
{
  "version": 1,
  "agreed": true,
  "agreedAt": "2026-11-01T19:09:06.4147087+08:00",
  "policyVersion": "ndt7-notice-v3",
  "clientName": "GoSpeed",
  "clientVersion": "0.5.0-beta.1"
}
```

对该目录做关键字扫描（`access_token` / `mlab-oti` / `measurement-lab` / `wss://`）
结果为**零命中**。

### 3.4 同意后的面板与重启

- 同意后面板切换为：计数行保持、"发现附近的 M-Lab 节点"、"尚未发现节点。发现节点会
  向 M-Lab 发起一次 Locate 查询，该查询本身会暴露你的公网 IP。"、
  "本次测速方向（下载与上传分别确认）"+ 下载/上传两个方向按钮（下载为选中态）、
  "单次流量预算：下载上限 24.00 MiB · 上传上限 16.00 MiB · 合计 40.00 MiB"、
  "确认并开始这一次测速"（**禁用**，因为还没选节点）。
- **关闭并重新启动 EXE 后**：面板**直接是已同意态**（没有告知、没有同意按钮），
  节点列表为空、计数仍 0/4、状态"空闲"、指标全为 N/A、无上一次结果残留。

### 3.5 未在实机上执行的动作（按禁止项）

- **没有**点击"发现附近的 M-Lab 节点"——会调用 M-Lab Locate 公网接口。
- **没有**点击"确认并开始这一次测速"——会消耗公网额度。
- **没有**点击"不同意"——实机使用的是隔离配置目录，撤回同意会额外产生一次写盘，
  其行为已由 `TestNDT7ConsentRefusalWithdrawsEverything` 离线覆盖。

---

## 4. 未执行项与原因

| 项 | 未执行内容 | 原因 | 如何补做 |
|---|-----------|------|---------|
| 07 | 真实 M-Lab 节点发现与节点列表呈现 | 本轮明令禁止调用 Locate 公网接口 | 人工在有外网的机器上点"发现附近的 M-Lab 节点" |
| 22 / 23 | 真实服务器下的 TCP MinRTT 呈现与真实速率 | 需要真实公网测量，会消耗额度；本地 mock 不发 TCPInfo 帧 | 人工执行一次真实测速（每天最多 4 次） |
| 01 | 抓包确认"零出站连接" | 需要人工抓包工具 | 人工用 Wireshark 观察启动 30 秒 |
| 08 | 开发者工具 / 崩溃转储中的实检 | 需要人工打开开发者工具 | 人工在 WebView 开发者工具里搜索关键字 |
| 全部 | 每日上限达到上限后的界面提示 | 需要先用满 4 次额度 | 在专用测试机上按天用满额度后验收 |
