<script setup lang="ts">
import * as echarts from 'echarts/core'
import { LineChart } from 'echarts/charts'
import { GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'

echarts.use([LineChart, GridComponent, LegendComponent, TooltipComponent, CanvasRenderer])

const props = defineProps<{
  download: [number, number][]
  upload: [number, number][]
}>()

const container = ref<HTMLDivElement | null>(null)
let chart: ReturnType<typeof echarts.init> | null = null
let observer: ResizeObserver | null = null

function baseOption() {
  return {
    animation: false,
    grid: { left: 56, right: 16, top: 30, bottom: 30 },
    legend: {
      top: 0,
      right: 0,
      icon: 'roundRect',
      itemWidth: 12,
      itemHeight: 4,
      textStyle: { color: '#9aa2ad', fontSize: 12 },
    },
    tooltip: {
      trigger: 'axis',
      backgroundColor: '#1d2024',
      borderColor: '#3a4048',
      textStyle: { color: '#eceef1', fontSize: 12 },
    },
    xAxis: {
      type: 'value',
      min: 0,
      name: '秒',
      nameTextStyle: { color: '#9aa2ad', fontSize: 11 },
      axisLabel: { color: '#9aa2ad', fontSize: 11 },
      axisLine: { lineStyle: { color: '#3a4048' } },
      splitLine: { lineStyle: { color: '#22262c' } },
    },
    yAxis: {
      type: 'value',
      min: 0,
      name: 'Mbps',
      nameTextStyle: { color: '#9aa2ad', fontSize: 11 },
      axisLabel: { color: '#9aa2ad', fontSize: 11 },
      axisLine: { lineStyle: { color: '#3a4048' } },
      splitLine: { lineStyle: { color: '#22262c' } },
    },
    series: [
      {
        name: '下载',
        type: 'line',
        showSymbol: false,
        lineStyle: { width: 2, color: '#38bdf8' },
        itemStyle: { color: '#38bdf8' },
        data: [] as [number, number][],
      },
      {
        name: '上传',
        type: 'line',
        showSymbol: false,
        lineStyle: { width: 2, color: '#f59e0b' },
        itemStyle: { color: '#f59e0b' },
        data: [] as [number, number][],
      },
    ],
  }
}

function render() {
  if (!chart) return
  chart.setOption({
    series: [{ data: props.download }, { data: props.upload }],
  })
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
  () => [props.download, props.upload],
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
      实时速率
      <span class="hint">每个采样点的瞬时速率，来自引擎实时采样</span>
    </h2>
    <div ref="container" class="chart" />
  </section>
</template>
