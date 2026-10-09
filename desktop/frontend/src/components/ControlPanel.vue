<script setup lang="ts">
import { Play, Square } from '@lucide/vue'
import { computed } from 'vue'
import type { AppInfo, NodeView } from '../types'

const props = defineProps<{
  appInfo: AppInfo | null
  nodes: NodeView[]
  running: boolean
  canStart: boolean
}>()

const selectionMode = defineModel<string>('selectionMode', { required: true })
const nodeId = defineModel<string>('nodeId', { required: true })
const connections = defineModel<number>('connections', { required: true })
const durationMs = defineModel<number>('durationMs', { required: true })
const sampleIntervalMs = defineModel<number>('sampleIntervalMs', { required: true })

const emit = defineEmits<{
  (event: 'start'): void
  (event: 'cancel'): void
}>()

const durationOptions = [
  { value: 3000, label: '3 秒' },
  { value: 5000, label: '5 秒' },
  { value: 10000, label: '10 秒' },
  { value: 15000, label: '15 秒' },
  { value: 30000, label: '30 秒' },
  { value: 60000, label: '60 秒' },
]

const sampleIntervalOptions = [
  { value: 100, label: '100 ms' },
  { value: 200, label: '200 ms' },
  { value: 250, label: '250 ms' },
  { value: 500, label: '500 ms' },
  { value: 1000, label: '1 s' },
]

const connectionPresets = [1, 4, 8, 16]

const maxConnections = computed(() => props.appInfo?.maxConnections ?? 16)
const selectableNodes = computed(() => props.nodes.filter((node) => node.enabled))

function clampConnections(value: number) {
  if (!Number.isFinite(value)) return
  connections.value = Math.min(Math.max(Math.round(value), 1), maxConnections.value)
}
</script>

<template>
  <section class="panel">
    <h2 class="panel-title">测速配置</h2>

    <div class="field">
      <span class="field-label">节点选择</span>
      <div class="segmented">
        <button
          type="button"
          :class="{ active: selectionMode === 'auto' }"
          :disabled="running"
          @click="selectionMode = 'auto'"
        >
          自动选择
        </button>
        <button
          type="button"
          :class="{ active: selectionMode === 'manual' }"
          :disabled="running"
          @click="selectionMode = 'manual'"
        >
          手动选择
        </button>
      </div>
    </div>

    <div v-if="selectionMode === 'manual'" class="field">
      <span class="field-label">测速节点（仅列出已启用节点）</span>
      <select v-model="nodeId" :disabled="running || selectableNodes.length === 0">
        <option v-for="node in selectableNodes" :key="node.id" :value="node.id">
          {{ node.name }}（{{ node.id }}）
        </option>
      </select>
    </div>

    <div class="field">
      <span class="field-label">并发连接数</span>
      <input
        v-model.number="connections"
        type="number"
        min="1"
        :max="maxConnections"
        step="1"
        :disabled="running"
        @change="clampConnections(connections)"
      />
      <div class="chip-row">
        <button
          v-for="preset in connectionPresets"
          :key="preset"
          type="button"
          class="chip"
          :class="{ active: connections === preset }"
          :disabled="running || preset > maxConnections"
          @click="connections = preset"
        >
          {{ preset }}
        </button>
      </div>
    </div>

    <div class="field">
      <span class="field-label">测速时长（每个传输阶段）</span>
      <select v-model.number="durationMs" :disabled="running">
        <option v-for="option in durationOptions" :key="option.value" :value="option.value">
          {{ option.label }}
        </option>
      </select>
    </div>

    <div class="field">
      <span class="field-label">采样间隔</span>
      <select v-model.number="sampleIntervalMs" :disabled="running">
        <option v-for="option in sampleIntervalOptions" :key="option.value" :value="option.value">
          {{ option.label }}
        </option>
      </select>
    </div>

    <div class="field">
      <button
        v-if="!running"
        type="button"
        class="btn primary block"
        :disabled="!canStart"
        @click="emit('start')"
      >
        <Play :size="14" />
        开始测速
      </button>
      <button v-else type="button" class="btn danger block" @click="emit('cancel')">
        <Square :size="14" />
        取消测速
      </button>
    </div>
  </section>
</template>
