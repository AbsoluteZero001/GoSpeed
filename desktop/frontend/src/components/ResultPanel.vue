<script setup lang="ts">
import { computed } from 'vue'
import type { FinishedEvent, TargetEvent, TestResult, TransferResult, UploadResult } from '../types'
import {
  formatBytes,
  formatCount,
  formatFromNs,
  formatMbps,
  formatPercent,
  formatSecondsFromMs,
  formatSecondsFromNs,
  formatTimestamp,
  runStateLabel,
  scopeLabel,
  statusLabel,
} from '../format'

const props = defineProps<{
  result: TestResult | null
  finished: FinishedEvent | null
  target: TargetEvent | null
}>()

const incomplete = computed(() => props.finished !== null && props.finished.status !== 'completed')

function rowsOf(transfer: TransferResult | null | undefined): { label: string; value: string }[] {
  if (!transfer) return []
  const stats = transfer.statistics
  const rows = [
    { label: '速率', value: `${formatMbps(transfer.mbps)} Mbps` },
    { label: '速率（MB/s）', value: transfer.mb_per_second.toFixed(2) },
    { label: '传输字节', value: formatBytes(transfer.bytes) },
    { label: '测量窗口', value: formatSecondsFromNs(transfer.duration_ns) },
    { label: '窗口定义', value: transfer.measurement_window },
    { label: '停止原因', value: transfer.stop_reason },
    {
      label: '连接',
      value: `${formatCount(transfer.active_connections)} / ${formatCount(transfer.connections)} 生效，${formatCount(transfer.failed_connections)} 失败`,
    },
    { label: '采样数', value: formatCount(stats.samples) },
    { label: '采样均值', value: `${formatMbps(stats.mean_mbps)} Mbps` },
    { label: '采样中位数', value: `${formatMbps(stats.median_mbps)} Mbps` },
    { label: '采样最小 / 最大', value: `${formatMbps(stats.min_mbps)} / ${formatMbps(stats.max_mbps)} Mbps` },
    {
      label: '采样标准差',
      value: `${formatMbps(stats.stddev_mbps)} Mbps${stats.stddev_kind ? `（${stats.stddev_kind}）` : ''}`,
    },
    { label: '变异系数 CV', value: formatPercent(stats.cv_percent) },
  ]
  const upload = transfer as UploadResult
  if (upload.server_confirmed_bytes !== undefined) {
    rows.push({
      label: '服务端确认字节',
      value: `${formatBytes(upload.server_confirmed_bytes)}（客户端发送 ${formatBytes(transfer.bytes)}）`,
    })
  }
  return rows
}

const latencyRows = computed(() => {
  const latency = props.result?.latency
  if (!latency) return []
  const samples = latency.samples_ns ?? []
  const measurable = samples.filter((ns) => ns > 0)
  const belowResolution = samples.length > 0 && measurable.length === 0
  const duration = (ns: number) => (belowResolution || ns <= 0 ? 'N/A（低于平台时钟分辨率）' : `${formatFromNs(ns)} ms`)
  return [
    { label: '类型', value: `${latency.type}（HTTP 往返，非 ICMP）` },
    {
      label: '样本',
      value: `${formatCount(latency.successful_samples)} 成功 / ${formatCount(latency.attempts)} 尝试，${formatCount(latency.failed_samples)} 失败`,
    },
    { label: '最小', value: duration(latency.min_ns) },
    { label: '平均', value: duration(latency.average_ns) },
    { label: '最大', value: duration(latency.max_ns) },
    {
      label: '抖动 Jitter',
      value: latency.jitter_ns === null || belowResolution ? 'N/A（无法分辨，或样本少于 2 个）' : `${formatFromNs(latency.jitter_ns)} ms`,
    },
    {
      label: '丢包率',
      value: latency.packet_loss_percent === null ? 'N/A（未实现丢包测试，不使用 HTTP 失败率冒充）' : formatPercent(latency.packet_loss_percent),
    },
  ]
})

const capabilityRows = computed(() => {
  const capabilities = props.result?.target.capabilities
  if (!capabilities) return []
  const rows = [
    { label: '状态', value: capabilities.supported ? '已协商' : capabilities.unsupported_endpoint ? '旧服务端（无 /capabilities）' : '未协商' },
  ]
  if (capabilities.server_version) rows.push({ label: '服务端版本', value: capabilities.server_version })
  if (capabilities.protocol_version) rows.push({ label: '协议版本', value: String(capabilities.protocol_version) })
  if (capabilities.capabilities?.length) rows.push({ label: '能力', value: capabilities.capabilities.join(', ') })
  const limits = capabilities.limits
  if (limits) {
    const parts: string[] = []
    if (limits.max_connections_per_test) parts.push(`连接 ≤ ${limits.max_connections_per_test}`)
    if (limits.max_duration_seconds) parts.push(`时长 ≤ ${limits.max_duration_seconds}s`)
    if (limits.max_download_bytes) parts.push(`下载 ≤ ${formatBytes(limits.max_download_bytes)}`)
    if (limits.max_upload_bytes) parts.push(`上传 ≤ ${formatBytes(limits.max_upload_bytes)}`)
    if (parts.length) rows.push({ label: '服务端限额', value: parts.join('，') })
  }
  if (capabilities.error) rows.push({ label: '说明', value: capabilities.error })
  return rows
})

const settingsRows = computed(() => {
  const settings = props.result?.settings
  if (!settings) return []
  return [
    { label: '阶段', value: settings.phases.join(' → ') },
    { label: '测速时长', value: formatSecondsFromNs(settings.duration_ns) },
    { label: '相位超时', value: formatSecondsFromNs(settings.timeout_ns) },
    { label: '并发连接数', value: formatCount(settings.connections) },
    { label: '采样间隔', value: `${(settings.sample_interval_ns / 1e6).toFixed(0)} ms` },
    { label: '延迟采样', value: `${formatCount(settings.latency_samples)} 次，间隔 ${(settings.latency_interval_ns / 1e6).toFixed(0)} ms` },
    { label: '字节预算', value: settings.max_bytes === 0 ? '无（仅按时长）' : formatBytes(settings.max_bytes) },
    { label: '预热', value: settings.warmup ? '已启用' : '未启用' },
  ]
})
</script>

<template>
  <section v-if="target" class="panel">
    <h2 class="panel-title">
      本次目标
      <span class="pill" :class="target.target.local ? 'local' : ''">
        {{ scopeLabel(target.target.networkScope) }}
      </span>
    </h2>
    <table class="data">
      <tbody>
        <tr>
          <th>节点</th>
          <td>{{ target.target.name }}（{{ target.target.id }}）</td>
        </tr>
        <tr>
          <th>地址</th>
          <td>{{ target.target.baseUrl }}</td>
        </tr>
        <tr>
          <th>选择方式</th>
          <td>
            {{ target.selectionMethod }}
            <template v-if="target.target.healthStatus"> · 健康状态 {{ statusLabel(target.target.healthStatus) }}</template>
            <template v-if="target.target.healthLatencyMs !== undefined">
              · 最近延迟 {{ target.target.healthLatencyMs.toFixed(2) }} ms
            </template>
          </td>
        </tr>
        <tr v-if="target.selectionReason">
          <th>选择原因</th>
          <td>{{ target.selectionReason }}</td>
        </tr>
        <tr v-if="target.selectionNote">
          <th>说明</th>
          <td>{{ target.selectionNote }}</td>
        </tr>
      </tbody>
    </table>

    <table v-if="target.candidates?.length" class="data" style="margin-top: 10px">
      <thead>
        <tr>
          <th class="num">排名</th>
          <th>节点</th>
          <th>状态</th>
          <th class="num">中位 RTT</th>
          <th>原因</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="candidate in target.candidates" :key="candidate.nodeId">
          <td class="num">{{ candidate.rank }}</td>
          <td>{{ candidate.nodeName }}（{{ candidate.nodeId }}）</td>
          <td>{{ statusLabel(candidate.status) }}</td>
          <td class="num">{{ candidate.latencyMs === undefined ? 'N/A' : `${candidate.latencyMs.toFixed(2)} ms` }}</td>
          <td>{{ candidate.reason }}</td>
        </tr>
      </tbody>
    </table>
  </section>

  <template v-if="result || finished">
    <div v-if="incomplete && finished?.error" class="banner">
      <strong>{{ runStateLabel(finished.status) }}</strong>
      <span class="text">{{ finished.error }}</span>
    </div>

    <section v-if="result" class="panel">
      <h2 class="panel-title">
        测速结果
        <span class="pill">{{ runStateLabel(result.status) }}</span>
        <span class="spacer" />
        <span class="hint">{{ result.test_id }} · {{ formatTimestamp(result.timestamp) }} · 用时 {{ formatSecondsFromMs(finished?.durationMs ?? null) }}</span>
      </h2>
      <div v-if="incomplete" class="banner warn" style="margin-bottom: 10px">
        <span class="text">测速未完成：以下为已经真实观测到的部分证据，不代表完整测速结果。</span>
      </div>
      <div v-if="result.error_message" class="banner" style="margin-bottom: 10px">
        <span class="text">{{ result.error_message }}</span>
      </div>
      <div v-if="result.target.local" class="banner warn" style="margin-bottom: 10px">
        <span class="text">本机回环测试：结果只反映本机能力，不代表公网带宽。</span>
      </div>

      <table class="data">
        <thead>
          <tr>
            <th>延迟</th>
            <th />
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in latencyRows" :key="row.label">
            <th>{{ row.label }}</th>
            <td>{{ row.value }}</td>
          </tr>
          <tr v-if="latencyRows.length === 0">
            <td colspan="2">未测量延迟</td>
          </tr>
        </tbody>
      </table>

      <table class="data" style="margin-top: 10px">
        <thead>
          <tr>
            <th>下载</th>
            <th />
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in rowsOf(result.download)" :key="row.label">
            <th>{{ row.label }}</th>
            <td>{{ row.value }}</td>
          </tr>
          <tr v-if="!result.download">
            <td colspan="2">未测量下载</td>
          </tr>
        </tbody>
      </table>

      <table class="data" style="margin-top: 10px">
        <thead>
          <tr>
            <th>上传</th>
            <th />
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in rowsOf(result.upload)" :key="row.label">
            <th>{{ row.label }}</th>
            <td>{{ row.value }}</td>
          </tr>
          <tr v-if="!result.upload">
            <td colspan="2">未测量上传</td>
          </tr>
        </tbody>
      </table>

      <table v-if="result.download?.connection_reports?.length" class="data" style="margin-top: 10px">
        <thead>
          <tr>
            <th class="num">连接</th>
            <th>状态</th>
            <th class="num">下载字节</th>
            <th class="num">连接窗口</th>
            <th>错误</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="report in result.download.connection_reports" :key="`d-${report.index}`">
            <td class="num">{{ report.index + 1 }}</td>
            <td>{{ report.state }}</td>
            <td class="num">{{ formatBytes(report.bytes) }}</td>
            <td class="num">{{ formatSecondsFromNs(report.duration_ns) }}</td>
            <td>{{ report.error ?? '' }}</td>
          </tr>
        </tbody>
      </table>

      <table v-if="capabilityRows.length" class="data" style="margin-top: 10px">
        <thead>
          <tr>
            <th>能力协商</th>
            <th />
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in capabilityRows" :key="row.label">
            <th>{{ row.label }}</th>
            <td>{{ row.value }}</td>
          </tr>
        </tbody>
      </table>

      <table v-if="settingsRows.length" class="data" style="margin-top: 10px">
        <thead>
          <tr>
            <th>执行参数</th>
            <th />
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in settingsRows" :key="row.label">
            <th>{{ row.label }}</th>
            <td>{{ row.value }}</td>
          </tr>
        </tbody>
      </table>

      <div v-if="result.warnings?.length" class="banner warn" style="margin-top: 10px">
        <span class="text">
          <template v-for="(warning, index) in result.warnings" :key="index">
            {{ warning }}<br />
          </template>
        </span>
      </div>
    </section>
  </template>
</template>
