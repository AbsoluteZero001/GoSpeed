# NET-01 网络诊断引擎 — 字段语义与已知限制

本文记录 `gospeed diagnose` 结果中每个字段的**确切含义**，以及它**不**声称什么。
面向的是会照着字段写结论的人，所以凡是可能被读成比实际更强的表述，都在这里写明。

结果 Schema 为 `netdiag/1`，`schema_version` 会随破坏性变更递增。

---

## 1. 范围（scope）语义

### 1.1 `network.scope` = 目标范围

**它描述被诊断的目标地址属于哪一类网络**，取值 `local` / `lan` / `remote` / `unknown`，
与 `internal/speedtest` 对测速目标使用的词汇表一致。

它**不**由本地源地址推导。这是一条曾经的缺陷：从 `192.168.1.111` 测量 `1.1.1.1`
时，旧实现按源地址判定而返回 `lan`，描述的其实是用户自己的局域网，而不是被测量的链路。

| 场景 | `scope` |
|---|---|
| 目标 `127.0.0.1` | `local` |
| 目标 `192.168.1.50`，源 `192.168.1.111` | `lan` |
| 目标 `1.1.1.1`，源 `192.168.1.111` | `remote` |

目标地址无法解析时为 `unknown`，而不是回退成 `lan`。

### 1.2 `network.local_scope` = 本地源地址范围

**它描述操作系统为本次测量选中的源地址**属于哪一类网络，取值同上。

新增于 Beta 之后的 NET-01，**为增量字段**：不删除、不改名既有字段，
忽略它的消费者不受影响。与 `scope` 分开，是因为「本机在哪一段」和
「被测对象在哪一段」是两个独立问题，合并成一个字段必然丢掉其中一个。

---

## 2. 默认网络接口与到目标的路由

两者是**不同的问题**，从不合并：

- `network.default_interface` —— 默认路由 `0.0.0.0/0` 所属的接口。
  Windows 上直接取自路由表记录（与网关同源，二者不可能描述不同网卡）；
  Linux/macOS 取自 UDP 路由探测。
- `network.route_to_target` —— 操作系统**实际**会为该目标选择的源地址与接口，
  通过 `net.DialUDP` 后读回 `LocalAddr()` 得到。**UDP connect 不发包**，
  纯路由查询。

在双网卡或 VPN 环境下二者会不同，此时只有 `route_to_target` 才能解释 traceroute 结果。
本机实测：默认接口 `WLAN`，到 `127.0.0.1` 的实际路由是 `Loopback Pseudo-Interface 1`。

`method` 字段说明该结论的来源，便于读者判断它是路由表查询而非抓包观察。

---

## 3. 延迟：三种口径互不换算

`latency.icmp` / `latency.tcp` / `latency.http` 是**三个独立测量**，从不平均、
从不互相替代：

| 方法 | 含义 | 不等于 |
|---|---|---|
| `icmp_echo_rtt` | ICMP Echo 请求往返 | TCP 或应用层延迟 |
| `tcp_connect_rtt` | TCP 三次握手完成时间 | 复用连接的 RTT |
| `http_rtt` | 发请求到收到首字节 | 上述两者 |

**明确禁止的推论：**

- ICMP 超时**不是** TCP 丢包率。本包结构上**没有** `packet_loss` 字段。
  ICMP 超时只说明「这个主机没有回��这个探测包」。
- 三种方法的差值本身不是故障信号（ICMP 由主机内核应答，TCP 需栈参与，HTTP 还经应用层），
  差值过大时会以 warning 明示，但不会被当成错误。

**零与未测到的区分：**

- 未测到 → 该字段为 `null`
- 延迟样本落在平台时钟一个 tick 内 → `N/A`
- 传输窗口短于一个 tick → 引擎**拒绝发布速率并报错**，不会给出 `0`
- 真实测得 0 → 显示 `0`，这是真实观测

**Jitter**：相邻样本差值绝对值的平均，与 `internal/speedtest` 同口径；
少于两个样本时为 `null`，因为没有可比对象。

---

## 4. Traceroute 是正向路径观测

`route` 记录的是探测包**向前**走过的路径。

- **不是往返路径。** 回程未被测量，也无法从本结果推断。
- **不假设每个 hop 都会回应。** 超时 hop 被**保留**（否则后续 index 全部错位），
  状态为 `timeout`。路由器普遍对 TTL expired 报文限速，**超时不是该 hop 故障的证据**。
- **同一 hop 可记录多个应答地址**，负载均衡不会被说成一个。
- `target_reached` 为真，仅当某一 hop 的应答地址等于目标的已解析地址。
  中间 hop 应答**不等于**到达目标。
- **Windows `<N ms` 上界不产出数值 RTT。** 该 hop 记为 `responding`（确实有回包），
  但不发布数值：报 `N` 高估、报 `0` 低估。这是诚实的代价。
- 主机名只接受两种形态：含点的全限定名，或紧邻其地址之前的单标签。
  traceroute 自己的提示文字（英文 `Request timed out.` / 中文 `请求超时。`）
  **绝不会**被当作 hop 身份。
- 不做反向 DNS 解析：解析会把 DNS 延迟混进延迟测量，且名字不是关于 hop 的证据。

---

## 5. ASN 归属不代表实际转发路径

`enrichment` 回答的是「**这个地址是谁注册的**」。

它**不是**「这个数据包经过了谁的路由器」。知道某一跳地址的归属，
无法推断流量实际经过了该运营商的网络 —— 中间可能存在任意的转发与隧道。

因此每条归属都自带说明字段，明确写出这不构成路径证据。

- **仅对公网地址查询。** 私网 / 回环 / 链路本地 / CGNAT / 文档保留段全部过滤，
  不会把用户内网布局送到第三方服务。
- 带 1 小时缓存与请求间隔限速。
- **查询失败不影响任何测量结果**，只记入 `errors`。

---

## 6. 多网卡 / VPN 的已知限制

### 6.1 Windows 默认网关

Windows 上默认网关取自 `MIB_IPFORWARDTABLE`（`GetIpForwardTable`）：

- 表以 4 字节 `dwNumEntries` 开头，记录从偏移 4 开始，每条 56 字节；
  记录条数以 **`dwNumEntries` 为准**，不是缓冲区长度（API 返回的缓冲区大于表本身）
- 候选默认路由按 `dwForwardMetric1` 取最小者
- 每条记录经四重校验后才可能被采用：默认路由特征、掩码为连续前缀、
  接口号存在于标准库可见的适配器中、下一个跃点为可用单播地址

**限制**：v1 表只提供一个 `dwForwardMetric1`。它是**有效指标**
（Windows 已把接口指标合并进去），因此**不会**再加上接口指标——那会重复计数。

已在本机核实：`dwForwardMetric1 = 35`，而 `Get-NetRoute` 报告
`RouteMetric = 0` / `InterfaceMetric = 35`，只有「有效指标」这一种解释自洽。

**未能验证的部分**：本机只有**一条**默认路由，因此**多条默认路由之间的排序**
仅由确定性单元测试覆盖，未在真实双网卡或 VPN 机器上比对。
当存在多条可用默认路由时，结果的 `notes` 会明确写出选中了哪一条、
落选的是哪些、以及本包无法拆分 route/interface 两个指标，
以便与系统自带工具交叉核对。

### 6.2 `GetIpForwardTable2`：此前的调用协议有误，结论无效

v2 API（MIB_IPFORWARD_ROW2）能分别给出 RouteMetric 与 InterfaceMetric，
从根上消除 6.1 的限制。本轮曾试探过它，**但那次探测是无效的**，
其结果不能用来判断 v2 API 是否可用。

**此前探测的错误在于调用协议**，而不是 API 本身：

- v1 与 v2 的调用形状根本不同。v1 是
  `GetIpForwardTable(PMIB_IPFORWARDTABLE, PULONG pSize, BOOL)`
  —— 两段式，先传 NULL 表询问大小、期望 `STATUS_BUFFER_OVERFLOW`。
- v2 则是
  `GetIpForwardTable2(ADDRESS_FAMILY Family, PMIB_IPFORWARD_TABLE2 *Table)`
  —— 由调用方一次性分配整个表结构后传入，**没有大小探测阶段**。
  此前把 v1 的两段式协议套到 v2 上（并以一个无 `pSize` 出参的写法调用），
  调用协议本身就是错的。
- 由此产生的观测值（`ret = 0`、`size = 0xCCCCCCCC`、`NumEntries = 0`）
  只是错误协议下的未定义回读，**既不能证明 v2 可用，也不能证明其不可用**。

因此，关于 v2 的正确表述是：**状态未知，尚未验证**，而不是「API 不可用」。

**本轮的决定**：继续保留已经完整验证的 v1 实现，不再重构 Windows 路由模块。
6.1 中标注的多默认路由残留风险按已知限制接受。

**后续路径**：若将来要换用 v2（尤其是多网卡 / VPN 场景下需要区分
RouteMetric 与 InterfaceMetric 时），应作为一项独立的、有专门验收标准的
多网卡 / VPN 专项任务来做，前置条件是先独立验证 `MIB_IPFORWARD_TABLE2`
与 `MIB_IF_ROW2` 的布局，而不是靠单次输出反推。同族的 `GetBestRoute2`
要求调用方提供整个 MIB_IPFORWARD_ROW2 输出结构，成本同样不低。

本轮**不实现** v2。

---

## 7. 平台覆盖

| 平台 | ICMP | Traceroute | 网关 / DNS | 验证状态 |
|---|---|---|---|---|
| Windows | `ping -n -w` | `tracert -d -4 -h -w` | `GetIpForwardTable` + `GetAdaptersAddresses` | ✅ **实机验证**（全表 31/31 记录逐条校验） |
| Linux | `ping -n -c -W`(秒) | `traceroute -n -4 -m -w` | `/proc/net/route` + `/proc/resolv.conf` | ⚠️ **仅交叉编译，未实机执行** |
| macOS | `ping -n -c -W -t`(毫秒) | `traceroute -n -4 -m -w` | `netstat -rn` + `scutil --dns` | ⚠️ **仅交叉编译，未实机执行** |
| 其他 | — | — | `unknown` + 说明 | ⚠️ 仅交叉编译 |

**Linux 与 macOS 的解析路径存在与 Windows 同类的列偏移风险**，
只是尚未被发现——因为没有在真实系统上跑过。等效的全表逐条校验应在这两个平台上补做。

所有平台的 ICMP 与 traceroute 都调用**系统自带程序**，不引入第三方驱动或库；
根模块保持**零第三方依赖**。

---

## 8. 隐私

诊断结果**不会被上传**，代码中没有这样的路径。
唯一会对外通信的环节是显式的 `--asn`，它只发送**已过滤的公网地址**给第三方数据服务，
带缓存与限速，失败不影响任何测量结果。

---

## 9. 相关文件

- 实现：`internal/netdiag/`
- CLI：`internal/cli/diagnose.go`
- Windows 路由表布局与选择测试：`internal/netdiag/netinfo_windows_test.go`、`netinfo_windows_route_test.go`
- Windows 实机全表校验：`internal/netdiag/netinfo_windows_live_test.go`
