<script setup lang="ts">
import { Play, RefreshCcw, Square, TriangleAlert } from '@lucide/vue'
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import * as api from './bridge'
import { errorMessage } from './bridge'
import ControlPanel from './components/ControlPanel.vue'
import LatencyChart from './components/LatencyChart.vue'
import MetricsPanel from './components/MetricsPanel.vue'
import NoticeBanner from './components/NoticeBanner.vue'
import NodePanel from './components/NodePanel.vue'
import ResultPanel from './components/ResultPanel.vue'
import SpeedChart from './components/SpeedChart.vue'
import { runStateLabel } from './format'
import type {
  AppInfo,
  FinishedEvent,
  MeasurementNotice,
  NodeCheckResult,
  NodeStatusView,
  NodeView,
  ProgressEvent,
  StateEvent,
  TargetEvent,
  TestResult,
} from './types'

const appInfo = ref<AppInfo | null>(null)
const nodes = ref<NodeView[]>([])
const nodeStatuses = ref<Record<string, NodeStatusView>>({})
const nodesPath = ref('')
const runtimeAvailable = ref(true)
const checking = ref(false)

const selectionMode = ref('auto')
const nodeId = ref('')
const connections = ref(1)
const durationMs = ref(10000)
const sampleIntervalMs = ref(200)

const state = ref('idle')
const errorText = ref('')
const targetEvent = ref<TargetEvent | null>(null)
const targetNotice = ref<MeasurementNotice | null>(null)
const configNotice = ref<MeasurementNotice | null>(null)
const finished = ref<FinishedEvent | null>(null)
const result = ref<TestResult | null>(null)

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
    pingMs.value = event.elapsedMs
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

  const measured = event.result ?? null
  result.value = measured
  if (!measured) return

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
})

onBeforeUnmount(() => {
  unsubscribe?.()
  unsubscribe = null
})

async function startTest() {
  if (!canStart.value) return
  errorText.value = ''
  result.value = null
  finished.value = null
  targetEvent.value = null
  targetNotice.value = null
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
        <span class="version">v{{ appInfo?.version ?? '0.4.0' }}</span>
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
      </aside>

      <section class="content">
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

        <SpeedChart :download="downloadPoints" :upload="uploadPoints" />
        <LatencyChart :samples="latencyPoints" />
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
