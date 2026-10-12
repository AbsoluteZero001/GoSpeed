<script setup lang="ts">
import { ShieldCheck, Waypoints } from '@lucide/vue'
import { computed } from 'vue'
import type { Rate, UnifiedResult } from '../types'
import {
  completenessLabel,
  discoveryLabel,
  formatBytes,
  formatMbps,
  formatMs,
  formatSecondsFromMs,
  jitterTypeLabel,
  measurementStatusLabel,
  metricTypeLabel,
  ownershipLabel,
  scopeLabel,
  scopeWarning,
  stabilityLabel,
  stopReasonLabel,
  trustLabel,
} from '../format'

// ProviderResultPanel renders the unified verdict of one run.
//
// Its job is to make three things impossible to misread:
//   - what produced the numbers (provider, ownership, scope),
//   - what each number is and how it was measured (metric type and source),
//   - what is missing (null stays N/A, never a measured zero).
//
// The panel renders whatever the backend reported. It never derives a scope
// from a rate, never fills a missing metric, and never turns an incomplete run
// into a successful looking one.

const props = defineProps<{ unified: UnifiedResult | null }>()

const supported = computed(() => props.unified?.method?.negotiatedCaps?.supported ?? {})
const capabilityNotes = computed(() => props.unified?.method?.negotiatedCaps?.notes ?? {})
const notes = computed(() => props.unified?.quality?.notes ?? [])
const warnings = computed(() => props.unified?.method?.warnings ?? [])

const scopeNotice = computed(() => {
  const unified = props.unified
  if (!unified) return null
  return scopeWarning(unified.scope, unified.ownership === 'self_hosted')
})

/** isSupported keeps a capability row honest: absent means unsupported. */
function isSupported(capability: string): boolean {
  return supported.value[capability] === true
}

/** rateRows renders one direction, or an explicit "not measured" statement. */
function rateRows(rate: Rate | null, label: string): { label: string; value: string }[] {
  if (!rate) {
    return [{ label, value: 'N/A（未测量）' }]
  }
  return [
    { label: `${label}速率`, value: `${formatMbps(rate.mbps)} Mbps` },
    { label: `${label}字节`, value: formatBytes(rate.transferredBytes) },
    { label: `${label}窗口`, value: formatSecondsFromMs(rate.elapsedMs) },
    { label: `${label}窗口定义`, value: rate.window || 'N/A' },
    { label: `${label}停止原因`, value: stopReasonLabel(rate.stopReason) },
  ]
}

const downloadRows = computed(() => rateRows(props.unified?.download ?? null, '下载'))
const uploadRows = computed(() => rateRows(props.unified?.upload ?? null, '上传'))

const identityRows = computed(() => {
  const unified = props.unified
  if (!unified) return []
  return [
    { label: 'Provider', value: unified.providerId || 'N/A' },
    { label: '测量协议', value: unified.method?.protocol || 'N/A' },
    { label: '节点', value: unified.serverId || 'N/A' },
    { label: '节点范围', value: scopeLabel(unified.scope) },
    { label: '归属', value: ownershipLabel(unified.ownership) },
    { label: '来源', value: discoveryLabel(unified.discovery) },
  ]
})

const statusRows = computed(() => {
  const unified = props.unified
  if (!unified) return []
  return [
    { label: '终态', value: measurementStatusLabel(unified.status) },
    { label: '证据完整性', value: completenessLabel(unified.quality?.completeness) },
    { label: '稳定性', value: stabilityLabel(unified.quality?.stability) },
    { label: '可信度', value: trustLabel(unified.quality?.trust) },
    { label: '总耗时', value: formatSecondsFromMs(unified.elapsedMs) },
    { label: '总传输', value: formatBytes(unified.transferredBytes) },
  ]
})
</script>

<template>
  <section v-if="unified" class="panel" data-testid="provider-result-panel">
    <h2 class="panel-title">
      <ShieldCheck :size="15" /> 统一测量结论
      <span class="pill">{{ measurementStatusLabel(unified.status) }}</span>
      <span class="spacer" />
      <span class="hint">{{ unified.providerId }} · {{ scopeLabel(unified.scope) }}</span>
    </h2>

    <div v-if="scopeNotice" class="banner" style="margin-bottom: 10px">
      <span class="text">{{ scopeNotice }}</span>
    </div>

    <div v-if="unified.status !== 'completed'" class="banner warn" style="margin-bottom: 10px">
      <span class="text">
        本次测量未完成（{{ measurementStatusLabel(unified.status) }}）。以下仅显示已经真实观测到的部分证据，
        不代表完整测速结果。
        <template v-if="unified.errorCode"> 错误码：{{ unified.errorCode }}</template>
      </span>
    </div>

    <div class="provider-grid">
      <table class="data">
        <thead>
          <tr><th>Provider 与节点</th><th /></tr>
        </thead>
        <tbody>
          <tr v-for="row in identityRows" :key="row.label">
            <th>{{ row.label }}</th>
            <td>{{ row.value }}</td>
          </tr>
        </tbody>
      </table>

      <table class="data">
        <thead>
          <tr><th>状态与质量</th><th /></tr>
        </thead>
        <tbody>
          <tr v-for="row in statusRows" :key="row.label">
            <th>{{ row.label }}</th>
            <td>{{ row.value }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <table class="data" style="margin-top: 10px">
      <thead>
        <tr><th>速率（真实 Mbit/s）</th><th /></tr>
      </thead>
      <tbody>
        <tr v-for="row in downloadRows" :key="row.label">
          <th>{{ row.label }}</th>
          <td>{{ row.value }}</td>
        </tr>
        <tr v-for="row in uploadRows" :key="row.label">
          <th>{{ row.label }}</th>
          <td>{{ row.value }}</td>
        </tr>
      </tbody>
    </table>

    <table class="data" style="margin-top: 10px">
      <thead>
        <tr><th>延迟与抖动口径</th><th /></tr>
      </thead>
      <tbody>
        <tr>
          <th>延迟值</th>
          <td>{{ unified.latency === null ? 'N/A（未测量）' : `${formatMs(unified.latency.valueMs)} ms` }}</td>
        </tr>
        <tr>
          <th>延迟类型</th>
          <td>{{ unified.latency ? metricTypeLabel(unified.latency.type) : 'N/A' }}</td>
        </tr>
        <tr>
          <th>延迟来源</th>
          <td>{{ unified.latency?.source ?? 'N/A' }}</td>
        </tr>
        <tr>
          <th>抖动值</th>
          <td>{{ unified.jitter === null ? 'N/A（未测量，或样本少于 2 个）' : `${formatMs(unified.jitter.valueMs)} ms` }}</td>
        </tr>
        <tr>
          <th>抖动类型</th>
          <td>{{ unified.jitter ? jitterTypeLabel(unified.jitter.type) : 'N/A' }}</td>
        </tr>
        <tr>
          <th>抖动来源</th>
          <td>{{ unified.jitter?.source ?? 'N/A' }}</td>
        </tr>
      </tbody>
    </table>

    <table class="data" style="margin-top: 10px">
      <thead>
        <tr><th>已协商能力</th><th /></tr>
      </thead>
      <tbody>
        <tr v-for="capability in ['download', 'upload', 'latency', 'jitter', 'packet_loss', 'loaded_latency']" :key="capability">
          <th>{{ capability }}</th>
          <td>
            {{ isSupported(capability) ? '已协商' : '未协商' }}
            <span v-if="capabilityNotes[capability]" class="hint"> · {{ capabilityNotes[capability] }}</span>
          </td>
        </tr>
      </tbody>
    </table>

    <div v-if="notes.length" class="banner" style="margin-top: 10px">
      <span class="text">
        <template v-for="(note, index) in notes" :key="index">{{ note }}<br /></template>
      </span>
    </div>

    <div v-if="warnings.length" class="banner warn" style="margin-top: 10px">
      <span class="text">
        <template v-for="(warning, index) in warnings" :key="index">{{ warning }}<br /></template>
      </span>
    </div>
  </section>
</template>

<style scoped>
.provider-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 10px;
}

@media (max-width: 900px) {
  .provider-grid {
    grid-template-columns: 1fr;
  }
}
</style>