import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import ProviderResultPanel from '../ProviderResultPanel.vue'
import type { UnifiedResult } from '../../types'

// The NDT7 budget stop is the one outcome where traffic moved but no rate
// can be published. The panel has to say both things: the traffic was real,
// and the speed was never measured.

function ndt7BudgetStop(): UnifiedResult {
  return {
    providerId: 'mlab-ndt7',
    serverId: 'ndt-mlab2-hkg03.mlab-oti.measurement-lab.org',
    status: 'budget_exceeded',
    scope: 'remote',
    ownership: 'third_party',
    discovery: 'service',
    download: null,
    upload: {
      mbps: null,
      elapsedMs: 0,
      transferredBytes: 0,
      observedBytes: 16777216,
      window: 'client_appinfo_first_to_last',
      stopReason: 'ndt7_budget_exceeded',
    },
    latency: null,
    jitter: null,
    elapsedMs: 46,
    transferredBytes: 16777216,
    errorCode: 'budget_exceeded',
    errorMessage: 'wire byte budget exceeded',
    quality: { completeness: 'partial', stability: 'degraded', trust: 'unknown' },
    method: {
      protocol: 'ndt7',
      negotiatedCaps: { supported: { download: true, upload: true, latency: true }, notes: {} },
      budget: { downloadBytes: 25165824, uploadBytes: 16777216, totalBytes: 41943040 },
      consentVersion: 'ndt7-notice-v3',
    },
  }
}

describe('ProviderResultPanel - the NDT7 budget stop', () => {
  it('says that no rate was published and why', () => {
    const wrapper = mount(ProviderResultPanel, { props: { unified: ndt7BudgetStop() } })
    const text = wrapper.text()
    expect(text).toContain('N/A（未形成有效测量窗口）')
    expect(text).toContain('没有发布速率')
  })

  it('shows the traffic that really moved instead of a zero byte count', () => {
    const wrapper = mount(ProviderResultPanel, { props: { unified: ndt7BudgetStop() } })
    const text = wrapper.text()
    expect(text).toContain('上传实际观测字节')
    expect(text).toContain('16.00 MiB')
    // The window byte count is meaningless without a window, so it must
    // not be shown as a measured zero.
    expect(text).not.toContain('上传字节0 B')
  })

  it('explains the budget stop with the observed traffic and the cap', () => {
    const wrapper = mount(ProviderResultPanel, { props: { unified: ndt7BudgetStop() } })
    const notice = wrapper.find('[data-testid="ndt7-budget-notice"]')
    expect(notice.exists()).toBe(true)
    expect(notice.text()).toContain('流量预算上限')
    expect(notice.text()).toContain('16.00 MiB')
  })

  it('keeps the ordinary rate table unchanged for a provider without an observed count', () => {
    const wrapper = mount(ProviderResultPanel, {
      props: {
        unified: {
          providerId: 'gospeed-http',
          serverId: 'loopback-1',
          status: 'completed',
          scope: 'local',
          download: {
            mbps: 812.34,
            elapsedMs: 1000,
            transferredBytes: 101542750,
            window: 'first_response_byte_to_last_body_byte',
            stopReason: 'duration_elapsed',
          },
          upload: null,
          latency: null,
          jitter: null,
          elapsedMs: 2400,
          transferredBytes: 101542750,
          quality: { completeness: 'complete', stability: 'normal', trust: 'verified' },
          method: {
            protocol: 'gospeed-http',
            negotiatedCaps: { supported: { download: true }, notes: {} },
            budget: { downloadBytes: 0, uploadBytes: 0, totalBytes: 0 },
          },
        } as unknown as UnifiedResult,
      },
    })
    const text = wrapper.text()
    expect(text).toContain('812.34 Mbps')
    expect(text).toContain('下载字节')
    // No socket level observation, so no such row appears.
    expect(text).not.toContain('实际观测字节')
    expect(wrapper.find('[data-testid="ndt7-budget-notice"]').exists()).toBe(false)
  })
})
