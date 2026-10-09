<script setup lang="ts">
import { RefreshCcw, Square } from '@lucide/vue'
import type { NodeStatusView, NodeView } from '../types'
import { formatMs, scopeLabel, statusLabel } from '../format'

const props = defineProps<{
  nodes: NodeView[]
  statuses: Record<string, NodeStatusView>
  selectedNodeId: string
  checking: boolean
  running: boolean
}>()

const emit = defineEmits<{
  (event: 'check'): void
  (event: 'cancelCheck'): void
  (event: 'select', nodeId: string): void
}>()

function rttLabel(node: NodeView): string {
  const status = props.statuses[node.id]
  if (!status) return '未探测'
  const latency = status.latencyMedianMs ?? status.httpRttMs
  if (latency === undefined) return status.status === 'healthy' ? '低于时钟分辨率' : 'N/A'
  return `${formatMs(latency)} ms`
}
</script>

<template>
  <section class="panel">
    <h2 class="panel-title">
      节点
      <span class="hint">{{ nodes.length }} 个</span>
      <span class="spacer" />
      <button
        v-if="!checking"
        type="button"
        class="btn small"
        :disabled="running || nodes.length === 0"
        title="对每个节点执行 /health + /capabilities + /ping 探测"
        @click="emit('check')"
      >
        <RefreshCcw :size="13" />
        检查
      </button>
      <button v-else type="button" class="btn small danger" @click="emit('cancelCheck')">
        <Square :size="13" />
        停止
      </button>
    </h2>

    <div v-if="nodes.length === 0" class="node-row disabled">没有配置任何节点</div>
    <div v-else class="node-list">
      <article
        v-for="node in nodes"
        :key="node.id"
        class="node-row"
        :class="{ selected: node.id === selectedNodeId, disabled: !node.enabled }"
        @click="emit('select', node.id)"
      >
        <div class="line1">
          <span class="name">{{ node.name }}</span>
          <span class="spacer" />
          <span v-if="node.local" class="pill local">本机</span>
          <span class="pill" :class="statuses[node.id]?.status ?? 'unknown'">
            {{ statusLabel(statuses[node.id]?.status) }}
          </span>
        </div>
        <div class="addr">{{ node.id }} · {{ node.baseUrl }}</div>
        <div class="addr">
          {{ scopeLabel(node.networkScope) }} · {{ node.enabled ? '已启用' : '已禁用' }} · RTT {{ rttLabel(node) }}
        </div>
        <div v-if="statuses[node.id]?.detail" class="detail">{{ statuses[node.id]?.detail }}</div>
      </article>
    </div>
  </section>
</template>
