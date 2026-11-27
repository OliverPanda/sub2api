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
            quotaColumn: 'Used / limit',
            unlimited: 'Unlimited',
            groupsColumn: 'Groups',
            receivedAtTitle: 'Updated',
            upstreamColumn: 'Upstream',
            accountsColumn: 'Probed/Accounts',
            accountsCount: '{count} accounts',
            probe: { ok: 'OK', partial: 'Partial', failed: 'Failed', unsupported: 'Unsupported', unknown: 'Unknown' },
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
        { base_url: 'https://upstream-a.example', groups: ['alpha', 'beta'], balance: 32.5, quota: 100, quota_used: 67.5, has_balance: true },
        { base_url: 'https://upstream-b.example', groups: [], has_balance: false }
      ]
    })
  })

  it('loads balances on mount and renders overview and table', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(mocks.getUpstreamBalances).toHaveBeenCalledWith()
    expect(wrapper.text()).toContain('alpha, beta')
    expect(wrapper.text()).toContain('Unknown')
    expect(wrapper.text()).toContain('https://upstream-a.example')
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
