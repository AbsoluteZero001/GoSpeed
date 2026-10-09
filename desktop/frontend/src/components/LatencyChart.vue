<script setup lang="ts">
import * as echarts from 'echarts/core'
import { LineChart } from 'echarts/charts'
import { GridComponent, TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'

echarts.use([LineChart, GridComponent, TooltipComponent, CanvasRenderer])

const props = defineProps<{
  samples: [number, number][]
}>()

const container = ref<HTMLDivElement | null>(null)
let chart: ReturnType<typeof echarts.init> | null = null
let observer: ResizeObserver | null = null

function baseOption() {
  return {
    animation: false,
    grid: { left: 56, right: 16, top: 16, bottom: 28 },
    tooltip: {
      trigger: 'axis',
      backgroundColor: '#1d2024',
      borderColor: '#3a4048',
      textStyle: { color: '#eceef1', fontSize: 12 },
    },
    xAxis: {
      type: 'value',
      min: 1,
      name: '样本',
      nameTextStyle: { color: '#9aa2ad', fontSize: 11 },
      axisLabel: { color: '#9aa2ad', fontSize: 11 },
      axisLine: { lineStyle: { color: '#3a4048' } },
      splitLine: { lineStyle: { color: '#22262c' } },
      minInterval: 1,
    },
    yAxis: {
      type: 'value',
      min: 0,
      name: 'ms',
      nameTextStyle: { color: '#9aa2ad', fontSize: 11 },
      axisLabel: { color: '#9aa2ad', fontSize: 11 },
      axisLine: { lineStyle: { color: '#3a4048' } },
      splitLine: { lineStyle: { color: '#22262c' } },
    },
    series: [
      {
        name: 'HTTP RTT',
        type: 'line',
        symbolSize: 5,
        lineStyle: { width: 1.5, color: '#34d399' },
        itemStyle: { color: '#34d399' },
        data: [] as [number, number][],
      },
    ],
  }
}

function render() {
  if (!chart) return
  chart.setOption({ series: [{ data: props.samples }] })
}

onMounted(() => {
  if (!container.value) return
  chart = echarts.init(container.value)
  chart.setOption(baseOption())
  render()
  observer = new ResizeObserver(() => chart?.resize())
  observer.observe(container.value)
})

watch(
  () => props.samples,
  () => render(),
)

onBeforeUnmount(() => {
  observer?.disconnect()
  observer = null
  chart?.dispose()
  chart = null
})
</script>

<template>
  <section class="panel">
    <h2 class="panel-title">
      HTTP RTT 采样
      <span class="hint">测量的是 HTTP 往返时间，不是 ICMP Ping</span>
    </h2>
    <div ref="container" class="chart small" />
  </section>
</template>
