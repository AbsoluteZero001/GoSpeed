import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import ProviderResultPanel from '../ProviderResultPanel.vue'
import type { UnifiedResult } from '../../types'
import { measurementStatusLabel } from '../../format'

// RC-02: a transfer that ends at the byte ceiling the node advertised is a
// planned stop. The panel must say so, state the real window against the
// planned one, keep the measured rate, and never present the short run as a
// full duration measurement.

function limited(overrides: Partial<UnifiedResult> = {}): UnifiedResult {
  return {
    providerId: 'gospeed-http',
    serverId: 'loopback-1',
    status: 'completed',
    scope: 'local',
    ownership: 'self_hosted',
    discovery: 'configured',
    download: {
      mbps: 812.34,
      elapsedMs: 10000,
      transferredBytes: 1015427500,
      window: 'first_response_byte_to_last_body_byte',
      stopReason: 'duration_elapsed',
    },
    upload: {
      mbps: 4976.39,
      elapsedMs: 1400,
      transferredBytes: 870_000_000,
      window: 'request_body_write_start_to_server_confirmation',
      stopReason: 'server_limit_reached',
    },
    latency: { valueMs: 0.14, type: 'http_rtt', source: 'http request to first response byte' },
    jitter: { valueMs: 0.26, type: 'consecutive_diff', source: 'mean absolute difference' },
    elapsedMs: 11400,
    transferredBytes: 1885427500,
    quality: { completeness: 'complete', stability: 'degraded', trust: 'normal' },
    method: {
      protocol: 'gospeed-http',
      negotiatedCaps: {
        supported: { download: true, upload: true, latency: true, jitter: true },
        notes: {},
      },
      budget: { downloadBytes: 0, uploadBytes: 0, totalBytes: 0 },
      warnings: [
        'upload: the transfer stopped at the 536870912 byte ceiling the server advertised, before the requested duration elapsed',
      ],
    },
    ...overrides,
  }
}

const notice = '[data-testid="upload-limit-notice"]'

describe('upload limit notice', () => {
  it('states the real window, the planned window and the confirmed bytes', () => {
    const wrapper = mount(ProviderResultPanel, {
      props: { unified: limited(), plannedDurationMs: 10000 },
    })
    const text = wrapper.find(notice).text()
    // The real window, not the planned one.
    expect(text).toContain('1.4 s')
    // The planned window, so the two can be compared.
    expect(text).toContain('10.0 s')
    // The server confirmed bytes the node accepted before stopping.
    expect(text).toContain('829.70 MiB')
    expect(text).toContain('达到节点声明的字节上限')
  })

  it('keeps the measured upload rate on screen', () => {
    const wrapper = mount(ProviderResultPanel, {
      props: { unified: limited(), plannedDurationMs: 10000 },
    })
    // The rate is real for the window it reports and must survive untouched.
    expect(wrapper.text()).toContain('4976.39 Mbps')
  })

  it('never presents the shortened run as a full duration measurement', () => {
    const wrapper = mount(ProviderResultPanel, {
      props: { unified: limited(), plannedDurationMs: 10000 },
    })
    const text = wrapper.find(notice).text()
    expect(text).toContain('不代表完整')
    expect(text).toContain('计划内的正常停止')
    // The verdict itself must still read as a completed run. Asserting on the
    // status rather than on prose keeps this honest: the notice may talk about
    // what did not happen, the verdict must not be one of those things.
    expect(wrapper.find(notice).text()).not.toContain('413')
    expect(wrapper.text()).toContain('已完成')
    for (const row of ['failed', 'timeout', 'cancelled', 'budget_exceeded']) {
      expect(wrapper.text()).not.toContain(measurementStatusLabel(row))
    }
  })

  it('keeps the run completed and the warning visible', () => {
    const wrapper = mount(ProviderResultPanel, {
      props: { unified: limited(), plannedDurationMs: 10000 },
    })
    expect(wrapper.text()).toContain('已完成')
    // The backend warning is never hidden by the notice.
    expect(wrapper.text()).toContain('the transfer stopped at the 536870912 byte ceiling')
  })

  it('renders N/A for an unknown planned window instead of guessing one', () => {
    const wrapper = mount(ProviderResultPanel, { props: { unified: limited() } })
    const text = wrapper.find(notice).text()
    expect(text).toContain('N/A')
    // A guessed planned window would be a fabricated number.
    expect(text).not.toContain('原计划 Duration 10.0 s')
  })

  it('stays hidden when no direction stopped at a node ceiling', () => {
    const wrapper = mount(ProviderResultPanel, {
      props: {
        unified: limited({
          upload: {
            mbps: 204.5,
            elapsedMs: 10000,
            transferredBytes: 306750000,
            window: 'request_body_write_start_to_server_confirmation',
            stopReason: 'duration_elapsed',
          },
        }),
        plannedDurationMs: 10000,
      },
    })
    expect(wrapper.find(notice).exists()).toBe(false)
  })

  it('stays hidden when the upload was never measured', () => {
    const wrapper = mount(ProviderResultPanel, {
      props: { unified: limited({ upload: null }), plannedDurationMs: 10000 },
    })
    expect(wrapper.find(notice).exists()).toBe(false)
  })

  it('also covers a download that stopped at a node ceiling', () => {
    const wrapper = mount(ProviderResultPanel, {
      props: {
        unified: limited({
          download: {
            mbps: 9381.37,
            elapsedMs: 600,
            transferredBytes: 700_000_000,
            window: 'first_response_byte_to_last_body_byte',
            stopReason: 'server_limit_reached',
          },
          upload: {
            mbps: 204.5,
            elapsedMs: 10000,
            transferredBytes: 306750000,
            window: 'request_body_write_start_to_server_confirmation',
            stopReason: 'duration_elapsed',
          },
        }),
        plannedDurationMs: 10000,
      },
    })
    const text = wrapper.find(notice).text()
    expect(text).toContain('下载在节点声明的字节上限处提前结束')
    expect(text).toContain('0.6 s')
  })
})