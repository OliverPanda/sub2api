import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import UpstreamBalancesPanel from '../UpstreamBalancesPanel.vue'

const mocks = vi.hoisted(() => ({
  getUpstreamBalances: vi.fn(),
  showError: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      getUpstreamBalances: mocks.getUpstreamBalances
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: mocks.showError })
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

enableAutoUnmount(afterEach)

const mountPanel = (show = false) => mount(UpstreamBalancesPanel, { props: { show } })

beforeEach(() => {
  mocks.getUpstreamBalances.mockReset().mockResolvedValue({
    items: [
      { account_id: 1, name: 'upstream-a', platform: 'openai', status: 'active', balance: 32.5, has_balance: true },
      { account_id: 2, name: 'upstream-b', platform: 'openai', status: 'active', has_balance: false }
    ],
    total: 32.5,
    has_total: true,
    probe_failed: 0
  })
  mocks.showError.mockReset()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('UpstreamBalancesPanel', () => {
  it('loads persisted balances only when the panel is shown', async () => {
    const wrapper = mountPanel()
    expect(mocks.getUpstreamBalances).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="upstream-balances-panel"]').exists()).toBe(false)

    await wrapper.setProps({ show: true })
    await flushPromises()

    expect(mocks.getUpstreamBalances).toHaveBeenCalledWith(1, 100)
    expect(wrapper.get('[data-testid="upstream-balances-panel"]').classes()).toContain('mt-4')
    expect(wrapper.get('[data-testid="upstream-balances-total"]').text()).toBe('$32.50')
    expect(wrapper.findAll('[data-testid="upstream-balance-item"]').map(item => item.text())).toEqual(['$32.50', 'admin.accounts.upstreamBalances.unknown'])
  })

  it('reloads the saved snapshot when refresh is clicked', async () => {
    const wrapper = mountPanel(true)
    await flushPromises()

    await wrapper.get('[data-testid="upstream-balances-refresh"]').trigger('click')
    await flushPromises()

    expect(mocks.getUpstreamBalances).toHaveBeenCalledTimes(2)
  })
})
