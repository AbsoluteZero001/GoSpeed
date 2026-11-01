import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import NDT7Panel from '../NDT7Panel.vue'
import type { NDT7ConsentState, ProviderNodeView } from '../../types'

// The M-Lab panel is the only place the user can see what a public
// measurement publishes and confirm it. These tests pin the three mistakes
// it must never make: hiding the disclosure, letting a measurement start
// without its own confirmation, and showing anything that could carry the
// access token.

function consent(overrides: Partial<NDT7ConsentState> = {}): NDT7ConsentState {
  return {
    provider: 'mlab-ndt7',
    consentRequired: true,
    agreed: false,
    policyVersion: 'ndt7-notice-v3',
    notice: '1. Public-data destination. Every measurement runs against an M-Lab NDT7 server.',
    dailyLimit: 4,
    dailyUsed: 1,
    dailyRemaining: 3,
    dailyAvailable: true,
    budget: { downloadBytes: 25165824, uploadBytes: 16777216, totalBytes: 41943040 },
    nodeCount: 0,
    ...overrides,
  }
}

function node(overrides: Partial<ProviderNodeView> = {}): ProviderNodeView {
  return {
    ref: 'pnode-1',
    id: 'ndt-mlab2-hkg03.mlab-oti.measurement-lab.org',
    name: 'ndt-mlab2-hkg03.mlab-oti.measurement-lab.org (Hong Kong, HK)',
    host: 'ndt-mlab2-hkg03.mlab-oti.measurement-lab.org:443',
    servicePath: '/ndt/v7/download',
    scope: 'remote',
    provider: 'mlab-ndt7',
    ownership: 'third_party',
    discovery: 'service',
    ...overrides,
  }
}

describe('NDT7Panel', () => {
  it('asks for the consent and shows the full disclosure before anything else', async () => {
    const wrapper = mount(NDT7Panel, { props: { state: consent(), nodes: [], busy: false } })
    expect(wrapper.find('[data-testid="ndt7-consent-block"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="ndt7-discover"]').exists()).toBe(false)

    await wrapper.find('[data-testid="ndt7-notice-toggle"]').trigger('click')
    const notice = wrapper.find('[data-testid="ndt7-notice"]')
    expect(notice.exists()).toBe(true)
    expect(notice.text()).toContain('Public-data destination')
    expect(wrapper.text()).toContain('ndt7-notice-v3')
  })

  it('reports the agreement and the refusal to the backend', async () => {
    const wrapper = mount(NDT7Panel, { props: { state: consent(), nodes: [], busy: false } })
    await wrapper.find('[data-testid="ndt7-consent-agree"]').trigger('click')
    await wrapper.find('[data-testid="ndt7-consent-refuse"]').trigger('click')
    expect(wrapper.emitted('consent')).toEqual([[true], [false]])
  })

  it('shows today s counter and refuses to start once the ceiling is reached', () => {
    const wrapper = mount(NDT7Panel, {
      props: {
        state: consent({ agreed: true, consentRequired: false, dailyUsed: 4, dailyRemaining: 0 }),
        nodes: [node()],
        busy: false,
      },
    })
    expect(wrapper.find('[data-testid="ndt7-daily"]').text()).toContain('剩余')
    expect(wrapper.find('[data-testid="ndt7-limit-reached"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="ndt7-start"]').attributes('disabled')).toBeDefined()
  })

  it('marks a discovered node as a third party public node', () => {
    const wrapper = mount(NDT7Panel, {
      props: { state: consent({ agreed: true, consentRequired: false }), nodes: [node()], busy: false },
    })
    const row = wrapper.find('[data-testid="ndt7-node"]')
    expect(row.text()).toContain('第三方公共节点')
    expect(row.text()).toContain('M-Lab Locate 自动发现')
  })

  it('never renders an endpoint or a credential', () => {
    const wrapper = mount(NDT7Panel, {
      props: { state: consent({ agreed: true, consentRequired: false }), nodes: [node()], busy: false },
    })
    const text = wrapper.text()
    for (const forbidden of ['access_token', 'wss://', 'http://', 'endpoint']) {
      expect(text).not.toContain(forbidden)
    }
  })

  it('requires a per run confirmation before it authorizes the measurement', async () => {
    const wrapper = mount(NDT7Panel, {
      props: { state: consent({ agreed: true, consentRequired: false }), nodes: [node()], busy: false },
    })
    await wrapper.find('[data-testid="ndt7-node"]').trigger('click')
    // Opening the confirmation is not yet an authorization.
    await wrapper.find('[data-testid="ndt7-start"]').trigger('click')
    expect(wrapper.emitted('authorize')).toBeUndefined()
    const confirmation = wrapper.find('[data-testid="ndt7-confirm"]')
    expect(confirmation.exists()).toBe(true)
    expect(confirmation.text()).toContain('用后即失效')
    expect(confirmation.text()).toContain('公网 IP')

    await wrapper.find('[data-testid="ndt7-confirm-run"]').trigger('click')
    expect(wrapper.emitted('authorize')).toEqual([[{ ref: 'pnode-1', direction: 'download' }]])
  })

  it('authorizes the direction the user picked', async () => {
    const wrapper = mount(NDT7Panel, {
      props: { state: consent({ agreed: true, consentRequired: false }), nodes: [node()], busy: false },
    })
    await wrapper.find('[data-testid="ndt7-node"]').trigger('click')
    await wrapper.find('[data-testid="ndt7-direction-upload"]').trigger('click')
    await wrapper.find('[data-testid="ndt7-start"]').trigger('click')
    await wrapper.find('[data-testid="ndt7-confirm-run"]').trigger('click')
    expect(wrapper.emitted('authorize')).toEqual([[{ ref: 'pnode-1', direction: 'upload' }]])
  })

  it('drops the authorization when the user cancels the confirmation', async () => {
    const wrapper = mount(NDT7Panel, {
      props: { state: consent({ agreed: true, consentRequired: false }), nodes: [node()], busy: false },
    })
    await wrapper.find('[data-testid="ndt7-node"]').trigger('click')
    await wrapper.find('[data-testid="ndt7-start"]').trigger('click')
    await wrapper.find('[data-testid="ndt7-confirm-cancel"]').trigger('click')
    expect(wrapper.emitted('revoke')).toHaveLength(1)
    expect(wrapper.emitted('authorize')).toBeUndefined()
  })

  it('cannot start without a selected node', () => {
    const wrapper = mount(NDT7Panel, {
      props: { state: consent({ agreed: true, consentRequired: false }), nodes: [node()], busy: false },
    })
    expect(wrapper.find('[data-testid="ndt7-start"]').attributes('disabled')).toBeDefined()
  })

  it('cannot be triggered twice while the gesture is in flight', async () => {
    // The confirmation, the authorization and the start are one gesture.
    // While it is in flight the panel must not offer it again, or a double
    // click would look like two confirmations.
    const wrapper = mount(NDT7Panel, {
      props: { state: consent({ agreed: true, consentRequired: false }), nodes: [node()], busy: true },
    })
    expect(wrapper.find('[data-testid="ndt7-start"]').attributes('disabled')).toBeDefined()
    await wrapper.find('[data-testid="ndt7-node"]').trigger('click')
    // The confirmation never opens, so there is nothing to confirm twice.
    expect(wrapper.find('[data-testid="ndt7-confirm"]').exists()).toBe(false)
    expect(wrapper.emitted('authorize')).toBeUndefined()
  })

  it('says why the counter is unusable instead of showing a number', () => {
    const wrapper = mount(NDT7Panel, {
      props: {
        state: consent({
          agreed: true,
          consentRequired: false,
          dailyAvailable: false,
          dailyError: 'the counter file is corrupt',
        }),
        nodes: [],
        busy: false,
      },
    })
    expect(wrapper.find('[data-testid="ndt7-daily-unavailable"]').text()).toContain('the counter file is corrupt')
  })
})
