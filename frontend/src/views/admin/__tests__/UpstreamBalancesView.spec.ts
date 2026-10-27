import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import UpstreamBalancesView from '../UpstreamBalancesView.vue'

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
  useAppStore: () => ({
    showError: mocks.showError
  })
}))

const i18n = createI18n({
  legacy: false,
  locale: 'en',
  messages: {
    en: {
      nav: { upstreamBalances: 'Upstream Balances' },
      admin: {
        accounts: {
          upstreamBalances: {
            title: 'Upstream Balances Overview',
            total: 'Total balance (A+B+C)',
            refresh: 'Refresh',
            refreshing: 'Refreshing...',
            loadFailed: 'Load failed',
            noData: 'No data',
            snapshotHint: 'Snapshot hint',
            probeFailedHint: '{count} accounts failed probe',
            partialHint: 'Partial hint',
            receivedAt: 'Updated: {value}',
            balanceColumn: 'Balance',
            receivedAtTitle: 'Updated',
            unknown: 'Unknown'
          },
          table: { account: 'Account', platform: 'Platform', status: 'Status' },
          status: { active: 'Active', inactive: 'Disabled' }
        }
      }
    }
  }
})

const mountView = () =>
  mount(UpstreamBalancesView, {
    global: {
      plugins: [i18n],
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot /></div>' }
      }
    }
  })

describe('UpstreamBalancesView', () => {
  beforeEach(() => {
    mocks.getUpstreamBalances.mockReset().mockResolvedValue({
      items: [
        { account_id: 1, name: 'upstream-a', platform: 'openai', status: 'active', balance: 32.5, has_balance: true, received_at: '2026-10-27T00:00:00Z' },
        { account_id: 2, name: 'upstream-b', platform: 'openai', status: 'inactive', has_balance: false }
      ],
      total: 32.5,
      has_total: true,
      probe_failed: 0
    })
  })

  it('loads balances on mount and renders overview and table', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(mocks.getUpstreamBalances).toHaveBeenCalledWith(1, 100)
    expect(wrapper.get('[data-testid="upstream-balances-total"]').text()).toBe('$32.50')
    const items = wrapper.findAll('[data-testid="upstream-balance-item"]')
    expect(items.map((i) => i.text())).toEqual(['$32.50', 'admin.accounts.upstreamBalances.unknown'])
    expect(wrapper.text()).toContain('upstream-a')
    expect(wrapper.text()).toContain('$32.50')
    wrapper.unmount()
  })

  it('reloads when refresh button is clicked', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-testid="upstream-balances-refresh"]').trigger('click')
    await flushPromises()

    expect(mocks.getUpstreamBalances).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('shows error when load fails', async () => {
    mocks.getUpstreamBalances.mockRejectedValue(new Error('boom'))
    const wrapper = mountView()
    await flushPromises()

    expect(mocks.showError).toHaveBeenCalled()
    wrapper.unmount()
  })
})
