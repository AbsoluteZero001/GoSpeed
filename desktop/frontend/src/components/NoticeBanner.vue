<script setup lang="ts">
import { Info, TriangleAlert } from '@lucide/vue'
import { computed } from 'vue'
import type { MeasurementNotice } from '../types'

const props = defineProps<{
  notice: MeasurementNotice
}>()

// Loopback / loopback-only / no-nodes are cautions ("do not misread the
// numbers"); LAN, public and unknown paths are informational.
const tone = computed(() => {
  switch (props.notice.kind) {
    case 'loopback':
    case 'loopback-only':
    case 'no-nodes':
      return 'warn'
    case 'lan':
    case 'public':
      return 'info'
    default:
      return 'neutral'
  }
})

const icon = computed(() => (tone.value === 'warn' ? TriangleAlert : Info))
</script>

<template>
  <section class="banner notice" :class="tone" role="status">
    <component :is="icon" :size="16" />
    <span class="text">
      <span class="notice-head">
        <strong>{{ notice.title }}</strong>
        <span v-if="notice.transferLabel" class="pill">{{ notice.transferLabel }}</span>
      </span>
      <span class="notice-line">{{ notice.message }}</span>
      <span v-if="notice.disclaimer" class="notice-line dim">{{ notice.disclaimer }}</span>
    </span>
  </section>
</template>
