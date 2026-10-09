import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import MetricsPanel from '../MetricsPanel.vue'

function baseProps(overrides: Record<string, unknown> = {}) {
  return {
    state: 'download_testing',
    downloadCurrent: 123.45,
    downloadAverage: 120,
    uploadCurrent: null,
    uploadAverage: null,
    pingMs: null,
    pingSamples: 5,
    jitterMs: null,
    bytes: 1024,
    activeConnections: 1,
    elapsedMs: 1000,
    progressFraction: 0.1,
    remainingMs: 9000,
    transferLabel: null,
    ...overrides,
  }
}

describe('MetricsPanel', () => {
  it('marks download and upload as local throughput when the target is loopback', () => {
    const wrapper = mount(MetricsPanel, { props: baseProps({ transferLabel: '本机吞吐量' }) })
    const qualifiers = wrapper.findAll('.qualifier')
    expect(qualifiers).toHaveLength(2)
    expect(qualifiers[0].text()).toBe('本机吞吐量')
    expect(qualifiers[1].text()).toBe('本机吞吐量')
  })

  it('shows no qualifier when no notice applies', () => {
    const wrapper = mount(MetricsPanel, { props: baseProps() })
    expect(wrapper.findAll('.qualifier')).toHaveLength(0)
  })

  it('renders the measured numbers verbatim', () => {
    const wrapper = mount(MetricsPanel, { props: baseProps() })
    const text = wrapper.text()
    expect(text).toContain('123.45')
    expect(text).toContain('窗口平均 120.00')
    expect(text).toContain('下载测试')
  })
})
