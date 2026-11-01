import { mount } from '@vue/test-utils'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import SpeedChart from '../SpeedChart.vue'

// The NDT7 path reports one measurement window at the end of a run, so there
// are no samples to draw. The chart has to say that instead of showing an
// empty plot that reads like a failed measurement.
//
// The chart draws with ECharts, which needs a real canvas. jsdom has none,
// so the renderer is stubbed: what is under test here is the note the
// component renders, not the drawing itself.

vi.mock('echarts/core', () => ({
  init: () => ({
    setOption: () => {},
    resize: () => {},
    dispose: () => {},
  }),
  use: () => {},
}))

beforeAll(() => {
  class ResizeObserverStub {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  }
  ;(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = ResizeObserverStub
})

describe('SpeedChart', () => {
  it('shows the note that explains an empty curve', () => {
    const wrapper = mount(SpeedChart, {
      props: {
        download: [],
        upload: [],
        note: 'NDT7 协议只在测量窗口结束时给出速率，本协议不提供实时采样曲线。',
      },
    })
    expect(wrapper.text()).toContain('不提供实时采样曲线')
  })

  it('shows no note when the provider does sample while it transfers', () => {
    const wrapper = mount(SpeedChart, { props: { download: [[0, 1]], upload: [] } })
    expect(wrapper.text()).not.toContain('不提供实时采样曲线')
  })
})
