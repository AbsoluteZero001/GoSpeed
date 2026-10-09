<script setup lang="ts">
import { Activity, Download, Gauge, Upload, Waves } from '@lucide/vue'
import { computed } from 'vue'
import {
  formatBytes,
  formatCount,
  formatMbps,
  formatMs,
  formatSecondsFromMs,
  runStateLabel,
} from '../format'

const props = defineProps<{
  state: string
  downloadCurrent: number | null
  downloadAverage: number | null
  uploadCurrent: number | null
  uploadAverage: number | null
  pingMs: number | null
  pingSamples: number
  jitterMs: number | null
  bytes: number
  activeConnections: number
  elapsedMs: number
  progressFraction: number | null
  remainingMs: number | null
  transferLabel?: string | null
}>()

const progressWidth = computed(() => {
  if (props.progressFraction === null) return '0%'
  const clamped = Math.min(Math.max(props.progressFraction, 0), 1)
  return `${(clamped * 100).toFixed(1)}%`
})
</script>

<template>
  <section class="metrics">
    <article class="metric download">
      <header>
        <Download :size="14" /> 下载
        <span v-if="transferLabel" class="qualifier">{{ transferLabel }}</span>
      </header>
      <div class="value">{{ formatMbps(downloadCurrent) }}<span class="unit">Mbps</span></div>
      <footer>窗口平均 {{ formatMbps(downloadAverage) }}</footer>
    </article>
    <article class="metric upload">
      <header>
        <Upload :size="14" /> 上传
        <span v-if="transferLabel" class="qualifier">{{ transferLabel }}</span>
      </header>
      <div class="value">{{ formatMbps(uploadCurrent) }}<span class="unit">Mbps</span></div>
      <footer>窗口平均 {{ formatMbps(uploadAverage) }}</footer>
    </article>
    <article class="metric ping">
      <header><Activity :size="14" /> Ping（HTTP RTT）</header>
      <div class="value">{{ formatMs(pingMs) }}<span class="unit">ms</span></div>
      <footer>
        {{ formatCount(pingSamples) }} 个样本<template v-if="pingMs === null && pingSamples > 0">（低于时钟分辨率）</template>
      </footer>
    </article>
    <article class="metric jitter">
      <header><Waves :size="14" /> 抖动 Jitter</header>
      <div class="value">{{ formatMs(jitterMs) }}<span class="unit">ms</span></div>
      <footer>相邻样本平均绝对差</footer>
    </article>
  </section>

  <section class="panel runbar">
    <Gauge :size="15" />
    <span class="fact">{{ runStateLabel(state) }}</span>
    <div class="progress">
      <div class="bar" :style="{ width: progressWidth }" />
    </div>
    <span class="fact">剩余 {{ formatSecondsFromMs(remainingMs) }}</span>
    <span class="fact">{{ activeConnections }} 连接</span>
    <span class="fact">已传输 {{ formatBytes(bytes) }}</span>
    <span class="fact">已用时 {{ formatSecondsFromMs(elapsedMs) }}</span>
  </section>
</template>
