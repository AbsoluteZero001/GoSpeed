import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import MetricsPanel from '../MetricsPanel.vue'
import ProviderResultPanel from '../ProviderResultPanel.vue'
import type { UnifiedResult } from '../../types'

// The panel renders what the backend reported. These tests pin the three
// mistakes it must never make: inventing a metric, turning a missing value into
// a zero, and presenting a local or LAN run as an internet measurement.

function unified(overrides: Partial<UnifiedResult> = {}): UnifiedResult {
  return {
    providerId: 'gospeed-http',
    serverId: 'loopback-1',
    status: 'completed',
    scope: 'local',
    ownership: 'self_hosted',
    discovery: 'configured',
    download: {
      mbps: 812.34,
      elapsedMs: 1000,
      transferredBytes: 101542750,
      window: 'first_response_byte_to_last_body_byte',
      stopReason: 'duration_elapsed',
    },
    upload: {
      mbps: 204.5,
      elapsedMs: 1200,
      transferredBytes: 30675000,
      window: 'request_body_write_start_to_server_confirmation',
      stopReason: 'duration_elapsed',
    },
    latency: {
      valueMs: 0.42,
      type: 'http_rtt',
      source: 'http request to first response byte (GET /ping); not an icmp ping',
    },
    jitter: { valueMs: 0.08, type: 'consecutive_diff', source: 'mean absolute difference' },
    elapsedMs: 2400,
    transferredBytes: 132217750,
    quality: { completeness: 'complete', stability: 'normal', trust: 'verified' },
    method: {
      protocol: 'gospeed-http',
      negotiatedCaps: {
        supported: { download: true, upload: true, latency: true, jitter: true },
        notes: { latency: 'http request to first response byte (GET /ping); not an icmp ping' },
      },
      budget: { downloadBytes: 0, uploadBytes: 0, totalBytes: 0 },
    },
    ...overrides,
  }
}

describe('ProviderResultPanel', () => {
  it('renders the real rates, window and stop reason', () => {
    const wrapper = mount(ProviderResultPanel, { props: { unified: unified() } })
    const text = wrapper.text()
    expect(text).toContain('812.34 Mbps')
    expect(text).toContain('204.50 Mbps')
    expect(text).toContain('first_response_byte_to_last_body_byte')
    expect(text).toContain('测量窗口结束')
  })

  it('states the measurement method of latency and jitter instead of implying a ping', () => {
    const wrapper = mount(ProviderResultPanel, { props: { unified: unified() } })
    const text = wrapper.text()
    expect(text).toContain('0.42 ms')
    expect(text).toContain('HTTP 往返时延（非 ICMP Ping）')
    expect(text).toContain('相邻样本平均绝对差')
    // The panel must never call it an ICMP ping.
    expect(text).not.toContain('ICMP Ping 时延')
  })

  it('shows provider, ownership and scope', () => {
    const wrapper = mount(ProviderResultPanel, { props: { unified: unified() } })
    const text = wrapper.text()
    expect(text).toContain('gospeed-http')
    expect(text).toContain('本机回环')
    expect(text).toContain('自托管')
    expect(text).toContain('用户配置')
  })

  it('never presents a loopback run as an internet bandwidth measurement', () => {
    const wrapper = mount(ProviderResultPanel, { props: { unified: unified() } })
    const text = wrapper.text()
    expect(text).toContain('本机回环测速')
    expect(text).toContain('不代表宽带速度')
  })

  it('marks a LAN run as not internet bandwidth', () => {
    const wrapper = mount(ProviderResultPanel, { props: { unified: unified({ scope: 'lan' }) } })
    const text = wrapper.text()
    expect(text).toContain('局域网测速')
    expect(text).toContain('不代表互联网宽带速度')
  })

  it('never presents a self hosted remote node as an official one', () => {
    const wrapper = mount(ProviderResultPanel, {
      props: { unified: unified({ scope: 'remote', ownership: 'self_hosted' }) },
    })
    const text = wrapper.text()
    expect(text).toContain('不是官方测速节点')
  })

  it('keeps a missing rate as N/A instead of a measured zero', () => {
    const wrapper = mount(ProviderResultPanel, {
      props: { unified: unified({ upload: null, download: { ...unified().download!, mbps: null } }) },
    })
    const text = wrapper.text()
    expect(text).toContain('N/A（未测量）')
    expect(text).toContain('N/A Mbps')
  })

  it('keeps a missing jitter as N/A and explains why', () => {
    const wrapper = mount(ProviderResultPanel, { props: { unified: unified({ jitter: null }) } })
    const text = wrapper.text()
    expect(text).toContain('N/A（未测量，或样本少于 2 个）')
    expect(text).not.toContain('0.00 ms')
  })

  it('marks a cancelled run as incomplete and shows the error code', () => {
    const wrapper = mount(ProviderResultPanel, {
      props: {
        unified: unified({
          status: 'cancelled',
          errorCode: 'cancelled',
          errorMessage: 'context canceled',
          download: null,
          upload: null,
          latency: null,
          jitter: null,
          transferredBytes: 0,
          quality: { completeness: 'incomplete', stability: 'unknown', trust: 'unknown' },
        }),
      },
    })
    const text = wrapper.text()
    expect(text).toContain('已取消')
    expect(text).toContain('本次测量未完成')
    expect(text).toContain('错误码：cancelled')
    expect(text).toContain('不完整')
  })

  it('marks a timeout run with its own status', () => {
    const wrapper = mount(ProviderResultPanel, {
      props: {
        unified: unified({
          status: 'timeout',
          errorCode: 'timeout',
          quality: { completeness: 'incomplete', stability: 'unknown', trust: 'unknown' },
        }),
      },
    })
    expect(wrapper.text()).toContain('超时')
  })

  it('distinguishes a partial run from a complete one', () => {
    const wrapper = mount(ProviderResultPanel, {
      props: {
        unified: unified({
          status: 'failed',
          upload: null,
          quality: { completeness: 'partial', stability: 'degraded', trust: 'unknown' },
        }),
      },
    })
    const text = wrapper.text()
    expect(text).toContain('部分完成')
    expect(text).toContain('降级')
  })

  it('surfaces warnings and quality notes', () => {
    const wrapper = mount(ProviderResultPanel, {
      props: {
        unified: unified({
          method: {
            ...unified().method,
            warnings: ['download: 3 of 4 connections failed'],
          },
          quality: { completeness: 'complete', stability: 'degraded', trust: 'normal', notes: ['node scope: local'] },
        }),
      },
    })
    const text = wrapper.text()
    expect(text).toContain('download: 3 of 4 connections failed')
    expect(text).toContain('node scope: local')
  })

  it('shows packet loss and loaded latency as never negotiated', () => {
    const wrapper = mount(ProviderResultPanel, { props: { unified: unified() } })
    const text = wrapper.text()
    // Both capabilities are absent from the negotiated set, so both must be
    // reported as not negotiated rather than silently omitted.
    const notNegotiated = text.match(/未协商/g) ?? []
    expect(notNegotiated.length).toBe(2)
    expect(text).toContain('packet_loss未协商')
    expect(text).toContain('loaded_latency未协商')
  })

  it('lets the live metric cards fall back to N/A instead of a zero reading', () => {
    // A cancelled or failed run nulls the live rate refs. MetricsPanel must
    // render that as N/A, never as a measured 0.00 Mbps.
    const metrics = mount(MetricsPanel, {
      props: {
        state: 'cancelled',
        downloadCurrent: null,
        downloadAverage: null,
        uploadCurrent: null,
        uploadAverage: null,
        pingMs: null,
        pingSamples: 5,
        jitterMs: null,
        bytes: 0,
        activeConnections: 0,
        elapsedMs: 0,
        progressFraction: null,
        remainingMs: null,
        transferLabel: '本机吞吐量',
      },
    })
    const text = metrics.text()
    expect(text).toContain('N/A')
    expect(text).not.toContain('0.00 Mbps')
  })

  it('renders nothing when there is no unified result', () => {
    const wrapper = mount(ProviderResultPanel, { props: { unified: null } })
    expect(wrapper.find('[data-testid="provider-result-panel"]').exists()).toBe(false)
  })
})