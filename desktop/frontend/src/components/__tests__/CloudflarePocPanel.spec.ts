import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import CloudflarePocPanel from '../CloudflarePocPanel.vue'

describe('CloudflarePocPanel', () => {
  it('shows the isolated provider, budget and privacy notice without starting a test', () => {
    const wrapper = mount(CloudflarePocPanel)
    const text = wrapper.text()

    expect(text).toContain('Cloudflare Speedtest PoC')
    expect(text).toContain('@cloudflare/speedtest@1.14.1')
    expect(text).toContain('24.00 MiB 下载')
    expect(text).toContain('16.00 MiB 上传')
    expect(text).toContain('40.00 MiB')
    expect(text).toContain('测速请求会直接发送给 Cloudflare')
    expect(text).toContain('实际网络流量会高于应用层 Payload')
    expect(wrapper.find('[data-testid="cloudflare-start"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="cloudflare-cancel"]').exists()).toBe(false)
  })
})
