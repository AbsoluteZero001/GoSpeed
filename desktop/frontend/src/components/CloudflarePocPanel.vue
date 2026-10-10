<script setup lang="ts">
import { Cloud, Play, Square, TriangleAlert } from '@lucide/vue'
import { computed, onBeforeUnmount, ref } from 'vue'
import { formatBytes, formatMbps, formatMs, formatSecondsFromMs } from '../format'
import {
  CLOUDFLARE_POC_PROFILE,
  CLOUDFLARE_PROVIDER_ID,
  CLOUDFLARE_SDK_VERSION,
  CloudflareSpeedTestProvider,
  type CloudflareProgress,
  type CloudflareProviderResult,
} from '../providers/cloudflareSpeedtest'

type PocState = 'idle' | 'running' | 'cancelling' | 'completed' | 'failed' | 'cancelled'

const state = ref<PocState>('idle')
const progress = ref<CloudflareProgress | null>(null)
const result = ref<CloudflareProviderResult | null>(null)
const errorText = ref('')

const provider = new CloudflareSpeedTestProvider({
  onProgress: (event) => {
    progress.value = event
    if (event.stage === 'failed') state.value = 'failed'
    else if (event.stage === 'cancelled') state.value = 'cancelled'
    else if (event.stage === 'completed') state.value = 'completed'
    else if (state.value !== 'cancelling') state.value = 'running'
  },
})

const running = computed(() => state.value === 'running' || state.value === 'cancelling')

const phaseLabel = computed(() => {
  switch (progress.value?.phase) {
    case 'latency':
      return 'Latency'
    case 'download':
      return 'Download'
    case 'upload':
      return 'Upload'
    default:
      return state.value === 'idle' ? '未开始' : '准备中'
  }
})

const stateLabel = computed(() => {
  switch (state.value) {
    case 'running':
      return '测速中'
    case 'cancelling':
      return '正在取消'
    case 'completed':
      return '已完成'
    case 'failed':
      return '失败'
    case 'cancelled':
      return '已取消'
    default:
      return '未开始'
  }
})

const payloadFraction = computed(() => {
  const total = progress.value?.configuredPayloadBytes ?? CLOUDFLARE_POC_PROFILE.totalPayloadBytes
  if (total <= 0) return 0
  return Math.min(1, (progress.value?.completedPayloadBytes ?? 0) / total)
})

const payloadWidth = computed(() => `${(payloadFraction.value * 100).toFixed(1)}%`)
const downloadMbps = computed(() => progress.value?.downloadMbps ?? result.value?.downloadMbps ?? null)
const uploadMbps = computed(() => progress.value?.uploadMbps ?? result.value?.uploadMbps ?? null)
const latencyMs = computed(() => progress.value?.latencyMs ?? result.value?.latencyMs ?? null)
const jitterMs = computed(() => progress.value?.jitterMs ?? result.value?.jitterMs ?? null)

async function start() {
  if (running.value) return
  state.value = 'running'
  progress.value = null
  result.value = null
  errorText.value = ''
  try {
    const finalResult = await provider.start()
    if (finalResult.status === 'busy') return
    result.value = finalResult
    state.value = finalResult.status
    errorText.value = finalResult.error ?? ''
  } catch (error) {
    state.value = 'failed'
    errorText.value = error instanceof Error ? error.message : String(error)
  }
}

function cancel() {
  if (!running.value) return
  state.value = 'cancelling'
  provider.cancel('用户取消 Cloudflare PoC')
}

onBeforeUnmount(() => {
  provider.dispose()
})
</script>

<template>
  <section class="panel cloudflare-poc" data-testid="cloudflare-poc-panel">
    <h2 class="panel-title">
      <Cloud :size="15" />
      Cloudflare Speedtest PoC
      <span class="pill">隔离实验</span>
      <span class="spacer" />
      <span class="hint">{{ stateLabel }}</span>
    </h2>

    <div class="banner warn poc-notice">
      <TriangleAlert :size="16" />
      <span class="text">
        测速请求会直接发送给 Cloudflare，Cloudflare 可以观察到连接公网 IP、请求时间等网络信息。测试会消耗真实流量，实际网络流量会高于应用层 Payload。
      </span>
    </div>

    <div class="poc-meta">
      <span>Provider: Cloudflare Speedtest</span>
      <span>SDK: @cloudflare/speedtest@{{ CLOUDFLARE_SDK_VERSION }}</span>
      <span>Provider ID: {{ CLOUDFLARE_PROVIDER_ID }}</span>
      <span>HTTP 429 时 SDK 最多重试 3 次，可能增加实际流量</span>
      <span>
        Payload: {{ formatBytes(CLOUDFLARE_POC_PROFILE.downloadPayloadBytes) }} 下载 +
        {{ formatBytes(CLOUDFLARE_POC_PROFILE.uploadPayloadBytes) }} 上传 =
        {{ formatBytes(CLOUDFLARE_POC_PROFILE.totalPayloadBytes) }}
      </span>
    </div>

    <section class="metrics poc-metrics">
      <article class="metric download">
        <header>下载 Mbps</header>
        <div class="value">{{ formatMbps(downloadMbps) }}</div>
      </article>
      <article class="metric upload">
        <header>上传 Mbps</header>
        <div class="value">{{ formatMbps(uploadMbps) }}</div>
      </article>
      <article class="metric ping">
        <header>Latency</header>
        <div class="value">{{ formatMs(latencyMs) }}<span class="unit">ms</span></div>
        <footer>Provider TTFB，非 ICMP Ping</footer>
      </article>
      <article class="metric jitter">
        <header>Jitter</header>
        <div class="value">{{ formatMs(jitterMs) }}<span class="unit">ms</span></div>
        <footer>相邻延迟样本平均绝对差</footer>
      </article>
    </section>

    <section class="runbar poc-runbar">
      <span class="fact">阶段 {{ phaseLabel }}</span>
      <div class="progress">
        <div class="bar" :style="{ width: payloadWidth }" />
      </div>
      <span class="fact">
        {{ formatBytes(progress?.completedPayloadBytes ?? 0) }} /
        {{ formatBytes(progress?.configuredPayloadBytes ?? CLOUDFLARE_POC_PROFILE.totalPayloadBytes) }}
      </span>
      <span class="fact">{{ formatSecondsFromMs(progress?.elapsedMs ?? null) }}</span>
    </section>

    <p class="poc-limit">
      SDK 每完成一个请求更新一次，不提供字节级实时回调。取消会调用 SDK pause 并请求中止当前请求；本地测试确认不会继续发起新请求，但真实 WebView2 在途连接是否已完全终止仍需单独验证。
    </p>

    <div v-if="errorText" class="banner poc-error">
      <TriangleAlert :size="16" />
      <span class="text">{{ errorText }}</span>
    </div>

    <div class="poc-actions">
      <button v-if="!running" type="button" class="btn primary" data-testid="cloudflare-start" @click="start">
        <Play :size="14" />
        开始 Cloudflare 测速
      </button>
      <button v-else type="button" class="btn danger" data-testid="cloudflare-cancel" @click="cancel">
        <Square :size="14" />
        取消 Cloudflare 测速
      </button>
    </div>
  </section>
</template>
