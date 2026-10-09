import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import NoticeBanner from '../NoticeBanner.vue'
import type { MeasurementNotice } from '../../types'

const loopbackNotice: MeasurementNotice = {
  kind: 'loopback',
  scope: 'local',
  title: '本机回环性能测试',
  message: '目标地址是本机回环地址（127.0.0.1 / ::1 / localhost）：流量不经过网卡与互联网。',
  disclaimer: '结果仅代表本机回环吞吐量，不代表真实宽带速度。',
  transferLabel: '本机吞吐量',
}

describe('NoticeBanner', () => {
  it('shows the loopback caveat and labels the metrics as local throughput', () => {
    const wrapper = mount(NoticeBanner, { props: { notice: loopbackNotice } })
    const text = wrapper.text()
    expect(text).toContain('本机回环性能测试')
    expect(text).toContain('不代表真实宽带速度')
    expect(text).toContain('本机吞吐量')
    expect(wrapper.classes()).toContain('warn')
  })

  it('keeps the LAN wording and uses the informational style', () => {
    const wrapper = mount(NoticeBanner, {
      props: {
        notice: {
          kind: 'lan',
          scope: 'lan',
          title: '局域网测速',
          message: '目标地址是局域网地址：流量经过本机网卡与局域网链路。',
          disclaimer: '局域网测速，不代表互联网宽带速度。',
          transferLabel: '局域网吞吐量',
        },
      },
    })
    expect(wrapper.text()).toContain('不代表互联网宽带速度')
    expect(wrapper.text()).toContain('局域网吞吐量')
    expect(wrapper.classes()).toContain('info')
  })

  it('names the factors that influence a public target', () => {
    const wrapper = mount(NoticeBanner, {
      props: {
        notice: {
          kind: 'public',
          scope: 'remote',
          title: '公网目标测速',
          message: '目标地址是公网 IP 字面量；结果受到服务器带宽、路由、网络拥塞和测速配置影响。',
          disclaimer: '实际路径仍由路由与中间网络决定，地址分类不能证明流量经过了哪些网络。',
          transferLabel: '实测速率',
        },
      },
    })
    const text = wrapper.text()
    expect(text).toContain('公网目标测速')
    expect(text).toContain('服务器带宽、路由、网络拥塞和测速配置')
    expect(text).toContain('地址分类不能证明')
  })

  it('does not imply a public path for a host name target', () => {
    const wrapper = mount(NoticeBanner, {
      props: {
        notice: {
          kind: 'unknown',
          scope: 'unknown',
          title: '目标路径未知',
          message: '目标是域名或无法分类的地址：GoSpeed 不做 DNS 解析猜测，无法仅凭地址判断是否经过公网。',
          disclaimer: '结果只代表到该目标的实际可达路径。',
          transferLabel: '实测速率',
        },
      },
    })
    const text = wrapper.text()
    expect(text).toContain('无法仅凭地址判断是否经过公网')
    expect(wrapper.classes()).toContain('neutral')
  })
})
