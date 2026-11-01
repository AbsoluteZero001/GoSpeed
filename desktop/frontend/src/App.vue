<script setup lang="ts">
import { Play, RefreshCcw, Square, TriangleAlert } from '@lucide/vue'
import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, ref } from 'vue'
import * as api from './bridge'
import { errorMessage } from './bridge'
import ControlPanel from './components/ControlPanel.vue'
import LatencyChart from './components/LatencyChart.vue'
import MetricsPanel from './components/MetricsPanel.vue'
import NDT7Panel from './components/NDT7Panel.vue'
import NoticeBanner from './components/NoticeBanner.vue'
import NodePanel from './components/NodePanel.vue'
import ProviderResultPanel from './components/ProviderResultPanel.vue'
import ResultPanel from './components/ResultPanel.vue'
import SpeedChart from './components/SpeedChart.vue'
import { isCloudflarePocEnabled } from './featureFlags'
import { providerErrorLabel, runStateLabel } from './format'
import type {
  AppInfo,
  FinishedEvent,
  MeasurementNotice,
  NDT7ConsentState,
  NodeCheckResult,
  NodeStatusView,
  NodeView,
  ProgressEvent,
  ProviderNodeView,
  StateEvent,
  TargetEvent,
  TestResult,
  UnifiedResult,
} from './types'

const appInfo = ref<AppInfo | null>(null)
// Build-time gate keeps the Cloudflare experiment out of the default artifact;
// the runtime flag stays as defence in depth for locally served builds.
const cloudflarePocEnabled =
  __GOSPEED_CF_POC_BUILD__ && isCloudflarePocEnabled(import.meta.env.VITE_CLOUDFLARE_SPEEDTEST_POC)
const CloudflarePocPanel = __GOSPEED_CF_POC_BUILD__
  ? defineAsyncComponent(() => import('./components/CloudflarePocPanel.vue'))
  : null
const nodes = ref<NodeView[]>([])
const nodeStatuses = ref<Record<string, NodeStatusView>>({})
const nodesPath = ref('')
const runtimeAvailable = ref(true)
const checking = ref(false)

const selectionMode = ref('auto')
const nodeId = ref('')
const connections = ref(1)
const durationMs = ref(10000)
/**
 * plannedDurationMs is the transfer window the *current* run was asked for.
 *
 * It is snapshotted when a run starts rather than read from the live input at
 * render time: the duration control stays editable while a measurement is in
 * flight, and a later edit must never rewrite what this run was planned to do.
 * The result panel needs it to contrast the real measured window against the
 * planned one when a node's byte ceiling ends the transfer early.
 */
const plannedDurationMs = ref<number | null>(null)
const sampleIntervalMs = ref(200)

const state = ref('idle')
const errorText = ref('')
const targetEvent = ref<TargetEvent | null>(null)
const targetNotice = ref<MeasurementNotice | null>(null)
const configNotice = ref<MeasurementNotice | null>(null)
const finished = ref<FinishedEvent | null>(null)
const result = ref<TestResult | null>(null)
// The provider independent verdict of the last run. It is present even for a
// cancelled or failed run, which is exactly when the status distinction matters.
const unified = ref<UnifiedResult | null>(null)

const downloadCurrent = ref<number | null>(null)
const downloadAverage = ref<number | null>(null)
const uploadCurrent = ref<number | null>(null)
const uploadAverage = ref<number | null>(null)
const pingMs = ref<number | null>(null)
const jitterMs = ref<number | null>(null)
const pingSamples = ref(0)
const bytes = ref(0)
const activeConnections = ref(0)
const elapsedMs = ref(0)
const progressFraction = ref<number | null>(null)
const remainingMs = ref<number | null>(null)

const downloadPoints = ref<[number, number][]>([])
const uploadPoints = ref<[number, number][]>([])
const latencyPoints = ref<[number, number][]>([])

// The M-Lab NDT7 path. The backend owns the consent, the single use run
// authorization and the daily limit; the frontend only shows them and asks.
const ndt7State = ref<NDT7ConsentState | null>(null)
const ndt7Nodes = ref<ProviderNodeView[]>([])
const ndt7Discovering = ref(false)
// ndt7Authorizing covers the whole gesture: the confirmation, the
// authorization and the start. Without it the panel would stay clickable
// between the authorization and the start, and a double click would look
// like two confirmations.
const ndt7Authorizing = ref(false)

// chartNote explains why the live curve is empty for a run that reports its
// rate only at the end.
const chartNote = ref('')

let unsubscribe: (() => void) | null = null

const running = computed(() =>
  ['probing', 'preparing', 'latency_testing', 'download_testing', 'upload_testing', 'cancelling'].includes(state.value),
)

const canStart = computed(() => {
  if (running.value || checking.value || nodes.value.length === 0) return false
  if (selectionMode.value === 'auto') return nodes.value.some((node) => node.enabled)
  return nodeId.value !== ''
})

const configLabel = computed(() => {
  if (!appInfo.value) return ''
  if (appInfo.value.nodeConfigSource === 'builtin') return '内置本机节点（未找到配置文件）'
  return nodesPath.value || appInfo.value.nodeConfigPath
})

const configTitle = computed(() => {
  const paths = appInfo.value?.configSearchPaths ?? []
  return paths.length > 0 ? `查找顺序：\n${paths.join('\n')}` : ''
})

const stateClass = computed(() => {
  if (running.value) return 'running'
  if (state.value === 'completed') return 'completed'
  if (state.value === 'failed') return 'failed'
  if (state.value === 'cancelled') return 'cancelled'
  return 'idle'
})

const displayError = computed(() => errorText.value || '')

function resetLive() {
  downloadCurrent.value = null
  downloadAverage.value = null
  uploadCurrent.value = null
  uploadAverage.value = null
  pingMs.value = null
  jitterMs.value = null
  pingSamples.value = 0
  bytes.value = 0
  activeConnections.value = 0
  elapsedMs.value = 0
  progressFraction.value = null
  remainingMs.value = null
  downloadPoints.value = []
  uploadPoints.value = []
  latencyPoints.value = []
}

function applyNodeCheck(resultPayload: NodeCheckResult) {
  const updated: Record<string, NodeStatusView> = { ...nodeStatuses.value }
  for (const node of resultPayload.nodes) {
    updated[node.id] = node
  }
  nodeStatuses.value = updated
  if (resultPayload.path) nodesPath.value = resultPayload.path
}

function onState(event: StateEvent) {
  if (event.state) state.value = event.state
}

function onTarget(event: TargetEvent) {
  targetEvent.value = event
  targetNotice.value = event.notice ?? null
}

function onProgress(event: ProgressEvent) {
  if (event.state) state.value = event.state

  if (event.phase === 'download' || event.phase === 'upload') {
    if (event.stage === 'start') {
      // A new transfer phase opens a new measurement window.
      bytes.value = 0
      activeConnections.value = 0
      elapsedMs.value = 0
      progressFraction.value = null
      remainingMs.value = null
    }
    if (event.stage === 'progress') {
      elapsedMs.value = event.elapsedMs
      bytes.value = event.bytes
      activeConnections.value = event.activeConnections
      if (event.budgetFraction !== undefined) progressFraction.value = event.budgetFraction
      if (event.budgetRemainingMs !== undefined) remainingMs.value = event.budgetRemainingMs
    }
    if (event.phase === 'download') {
      downloadCurrent.value = event.instantMbps
      downloadAverage.value = event.mbps
      downloadPoints.value = [...downloadPoints.value, [event.elapsedMs / 1000, event.instantMbps]]
      return
    }
    uploadCurrent.value = event.instantMbps
    uploadAverage.value = event.mbps
    uploadPoints.value = [...uploadPoints.value, [event.elapsedMs / 1000, event.instantMbps]]
    return
  }
  if (event.phase === 'latency' && event.stage === 'progress') {
    // A zero sample means the round trip finished inside one tick of the
    // platform clock. The duration was never resolved, so the headline number
    // must read N/A instead of a measured 0.00 ms.
    pingMs.value = event.elapsedMs > 0 ? event.elapsedMs : null
    pingSamples.value = event.samples
    latencyPoints.value = [...latencyPoints.value, [event.samples, event.elapsedMs]]
  }
}

function onFinished(event: FinishedEvent) {
  finished.value = event
  state.value = event.status
  checking.value = false
  if (event.notice) targetNotice.value = event.notice
  remainingMs.value = event.status === 'completed' ? 0 : remainingMs.value
  if (event.status === 'completed') progressFraction.value = 1

  unified.value = event.unified ?? null

  // The live curve only exists for a provider that samples while it
  // transfers. NDT7 reports one window at the end of the run, so the chart
  // says so instead of showing an empty plot that looks like a failure.
  chartNote.value =
    event.unified?.providerId === 'mlab-ndt7'
      ? 'NDT7 协议只在测量窗口结束时给出速率，本协议不提供实时采样曲线。'
      : ''

  // A refusal the provider issued before it opened a connection carries a
  // machine readable code. Its meaning is spelled out, because the raw code
  // alone does not tell the user what to do. The raw provider text is English
  // and terse, so the localized explanation replaces it in the result panel.
  const refusal = providerErrorLabel(event.unified?.errorCode)
  if (refusal) {
    if (!errorText.value) errorText.value = refusal
    finished.value = { ...event, error: refusal }
  }

  // The M-Lab session ended with the run: the node references and the
  // pending authorization are gone, so the panel has to reread the state.
  void refreshNDT7State()

  // A run that did not complete produced no final rate. Leaving the live cards
  // on the last progress value would present a partial or zero reading as the
  // measured speed, so they fall back to N/A exactly like the unified verdict
  // does. A completed run keeps the aggregate the engine published.
  if (event.status !== 'completed') {
    downloadCurrent.value = null
    downloadAverage.value = null
    uploadCurrent.value = null
    uploadAverage.value = null
  }

  const measured = event.result ?? null
  result.value = measured
  if (!measured) {
    // A provider without a native result (NDT7) reports its rate in the
    // unified verdict. The live cards fall back to it, so a completed run
    // does not show N/A next to a measured rate.
    const download = event.unified?.download
    const upload = event.unified?.upload
    if (download?.mbps != null) {
      downloadCurrent.value = download.mbps
      downloadAverage.value = download.mbps
    } else if (upload?.mbps != null) {
      uploadCurrent.value = upload.mbps
      uploadAverage.value = upload.mbps
    }
    return
  }

  const latency = measured.latency
  if (latency) {
    pingSamples.value = latency.successful_samples
    const samples = latency.samples_ns ?? []
    const measurable = samples.filter((ns) => ns > 0)
    if (samples.length > 0 && measurable.length === 0) {
      // Every sample was shorter than the platform clock resolution. That is
      // N/A, not a 0.00 ms measurement.
      pingMs.value = null
      jitterMs.value = null
    } else {
      pingMs.value = latency.average_ns / 1e6
      jitterMs.value = latency.jitter_ns === null ? null : latency.jitter_ns / 1e6
    }
    if (latency.samples_ns?.length) {
      latencyPoints.value = latency.samples_ns.map((ns, index) => [index + 1, ns / 1e6] as [number, number])
    }
  }
  if (measured.download) {
    downloadCurrent.value = measured.download.mbps
    downloadAverage.value = measured.download.mbps
  }
  if (measured.upload) {
    uploadCurrent.value = measured.upload.mbps
    uploadAverage.value = measured.upload.mbps
  }
}

function onNodes(event: NodeCheckResult) {
  applyNodeCheck(event)
}

onMounted(async () => {
  unsubscribe = api.subscribe({
    onState,
    onTarget,
    onProgress,
    onFinished,
    onNodes,
  })

  if (!api.hasRuntime()) {
    runtimeAvailable.value = false
    return
  }

  try {
    const [info, list, status] = await Promise.all([api.getAppInfo(), api.listNodes(), api.getStatus()])
    appInfo.value = info
    nodes.value = list.nodes
    nodesPath.value = list.path
    configNotice.value = list.notice ?? null
    connections.value = info.defaultConnections || connections.value
    durationMs.value = info.defaultDurationMs || durationMs.value
    sampleIntervalMs.value = info.defaultSampleIntervalMs || sampleIntervalMs.value
    const preferred = list.nodes.find((node) => node.enabled) ?? list.nodes[0]
    nodeId.value = preferred?.id ?? ''
    state.value = status.state || 'idle'
    checking.value = status.kind === 'check'
  } catch (error) {
    errorText.value = errorMessage(error)
  }

  await refreshNDT7State()
})

onBeforeUnmount(() => {
  unsubscribe?.()
  unsubscribe = null
})

async function startTest() {
  if (!canStart.value) return
  errorText.value = ''
  result.value = null
  unified.value = null
  finished.value = null
  targetEvent.value = null
  targetNotice.value = null
  plannedDurationMs.value = durationMs.value
  resetLive()
  state.value = 'preparing'
  try {
    const acknowledgement = await api.startTest({
      selectionMode: selectionMode.value,
      nodeId: nodeId.value,
      connections: connections.value,
      durationMs: durationMs.value,
      sampleIntervalMs: sampleIntervalMs.value,
    })
    if (!acknowledgement.started) {
      state.value = 'idle'
      errorText.value = acknowledgement.message || '测速请求被拒绝'
    }
  } catch (error) {
    state.value = 'idle'
    errorText.value = errorMessage(error)
  }
}

async function cancelTest() {
  errorText.value = ''
  try {
    await api.cancelTest()
  } catch (error) {
    errorText.value = errorMessage(error)
  }
}

// refreshNDT7State rereads the M-Lab gates. The panel is informational, so a
// failure here must never break a measurement that just finished.
async function refreshNDT7State() {
  try {
    const state = await api.getNDT7State()
    ndt7State.value = state
    if (state.nodeCount === 0) ndt7Nodes.value = []
  } catch {
    // Keep the last known state.
  }
}

async function setNDT7Consent(agreed: boolean) {
  errorText.value = ''
  try {
    ndt7State.value = await api.setNDT7Consent(agreed)
    if (!agreed) ndt7Nodes.value = []
  } catch (error) {
    errorText.value = errorMessage(error)
  }
}

async function discoverNDT7Nodes() {
  errorText.value = ''
  ndt7Discovering.value = true
  try {
    const result = await api.discoverNDT7Nodes()
    ndt7Nodes.value = result.nodes
    ndt7State.value = result.consent
  } catch (error) {
    errorText.value = errorMessage(error)
  } finally {
    ndt7Discovering.value = false
  }
}

// authorizeNDT7 asks the backend for the single use authorization of one
// measurement and starts it.
//
// The authorization is the backend's decision, not the frontend's: the panel
// has already shown the user what this run publishes and how much traffic it
// may use. The grant is consumed by this run and cannot be reused.
async function authorizeNDT7(payload: { ref: string; direction: string }) {
  errorText.value = ''
  ndt7Authorizing.value = true
  try {
    const grant = await api.authorizeNDT7Run(payload.ref, payload.direction)
    if (!grant.granted) {
      errorText.value = '本次测速未获得授权，请重新确认。'
      await refreshNDT7State()
      return
    }
    await startNDT7Test(payload)
  } catch (error) {
    errorText.value = errorMessage(error)
    await refreshNDT7State()
  } finally {
    ndt7Authorizing.value = false
  }
}

// startNDT7Test starts one M-Lab measurement.
//
// The engine's knobs are not sent: NDT7 measures one direction over a single
// WebSocket and applies its own traffic budget and timeout, so a duration or
// a connection count would describe something the protocol does not have.
async function startNDT7Test(payload: { ref: string; direction: string }) {
  result.value = null
  unified.value = null
  finished.value = null
  targetEvent.value = null
  targetNotice.value = null
  plannedDurationMs.value = null
  resetLive()
  state.value = 'preparing'
  try {
    const acknowledgement = await api.startTest({
      selectionMode: 'manual',
      nodeId: '',
      connections: 1,
      durationMs: 0,
      sampleIntervalMs: 0,
      provider: 'mlab-ndt7',
      providerNodeRef: payload.ref,
      direction: payload.direction,
    })
    if (!acknowledgement.started) {
      state.value = 'idle'
      errorText.value = acknowledgement.message || '测速请求被拒绝'
    }
  } catch (error) {
    state.value = 'idle'
    errorText.value = errorMessage(error)
  }
}

async function checkNodes() {
  if (checking.value || running.value) return
  errorText.value = ''
  checking.value = true
  state.value = 'checking'
  try {
    const outcome = await api.checkNodes()
    applyNodeCheck(outcome)
  } catch (error) {
    errorText.value = errorMessage(error)
  } finally {
    checking.value = false
    if (state.value === 'checking') state.value = 'idle'
  }
}

async function cancelCheck() {
  try {
    await api.cancelNodeCheck()
  } catch (error) {
    errorText.value = errorMessage(error)
  }
}

function selectNode(id: string) {
  if (running.value) return
  nodeId.value = id
  selectionMode.value = 'manual'
}
</script>

<template>
  <div class="app">
    <header class="topbar">
      <div class="brand">
        GoSpeed
        <span class="version">v{{ appInfo?.version ?? '0.5.0-beta.1' }}</span>
      </div>
      <span class="state-pill" :class="stateClass">
        <span class="dot" />
        {{ runStateLabel(state) }}
      </span>
      <span class="config-info" :title="configTitle">节点配置：{{ configLabel || '加载中…' }}</span>
      <span class="spacer" />
      <button
        type="button"
        class="btn"
        :disabled="running || checking || nodes.length === 0"
        title="对每个节点执行健康探测与能力协商"
        @click="checkNodes"
      >
        <RefreshCcw :size="14" />
        检查节点
      </button>
      <button v-if="!running" type="button" class="btn primary" :disabled="!canStart" @click="startTest">
        <Play :size="14" />
        开始测速
      </button>
      <button v-else type="button" class="btn danger" @click="cancelTest">
        <Square :size="14" />
        取消测速
      </button>
    </header>

    <main class="layout">
      <aside class="sidebar">
        <ControlPanel
          v-model:selection-mode="selectionMode"
          v-model:node-id="nodeId"
          v-model:connections="connections"
          v-model:duration-ms="durationMs"
          v-model:sample-interval-ms="sampleIntervalMs"
          :app-info="appInfo"
          :nodes="nodes"
          :running="running"
          :can-start="canStart"
          @start="startTest"
          @cancel="cancelTest"
        />
        <NoticeBanner v-if="configNotice" :notice="configNotice" />
        <NodePanel
          :nodes="nodes"
          :statuses="nodeStatuses"
          :selected-node-id="nodeId"
          :checking="checking"
          :running="running"
          @check="checkNodes"
          @cancel-check="cancelCheck"
          @select="selectNode"
        />
        <NDT7Panel
          :state="ndt7State"
          :nodes="ndt7Nodes"
          :busy="running || ndt7Discovering || ndt7Authorizing"
          @consent="setNDT7Consent"
          @discover="discoverNDT7Nodes"
          @authorize="authorizeNDT7"
          @revoke="api.revokeNDT7Authorization()"
        />
      </aside>

      <section class="content">
        <CloudflarePocPanel v-if="cloudflarePocEnabled" />
        <div v-if="!runtimeAvailable" class="banner">
          <TriangleAlert :size="16" />
          <span class="text">未连接到 GoSpeed 后端：请通过 Wails 桌面程序打开本界面，浏览器页面不会进行任何测速。</span>
        </div>
        <div v-if="displayError" class="banner">
          <TriangleAlert :size="16" />
          <span class="text">{{ displayError }}</span>
        </div>
        <NoticeBanner v-if="targetNotice" :notice="targetNotice" />

        <MetricsPanel
          :state="state"
          :download-current="downloadCurrent"
          :download-average="downloadAverage"
          :upload-current="uploadCurrent"
          :upload-average="uploadAverage"
          :ping-ms="pingMs"
          :ping-samples="pingSamples"
          :jitter-ms="jitterMs"
          :bytes="bytes"
          :active-connections="activeConnections"
          :elapsed-ms="elapsedMs"
          :progress-fraction="progressFraction"
          :remaining-ms="remainingMs"
          :transfer-label="targetNotice?.transferLabel ?? null"
        />

        <SpeedChart :download="downloadPoints" :upload="uploadPoints" :note="chartNote" />
        <LatencyChart :samples="latencyPoints" />
        <ProviderResultPanel :unified="unified" :planned-duration-ms="plannedDurationMs" />
        <ResultPanel
          :result="result"
          :finished="finished"
          :target="targetEvent"
          :notice="targetNotice"
        />
      </section>
    </main>
  </div>
</template>
