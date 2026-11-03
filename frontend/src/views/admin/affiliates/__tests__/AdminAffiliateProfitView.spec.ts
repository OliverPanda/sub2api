import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import AdminAffiliateProfitView from '../AdminAffiliateProfitView.vue'

const { getProfitReport } = vi.hoisted(() => ({
  getProfitReport: vi.fn(),
}))

vi.mock('@/api/admin/affiliates', () => {
  const affiliatesAPI = { getProfitReport }
  return { affiliatesAPI, default: affiliatesAPI }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn() }),
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

describe('AdminAffiliateProfitView', () => {
  beforeEach(() => {
    getProfitReport.mockReset()
    getProfitReport.mockResolvedValue({
      items: [],
      total: 0,
      page: 1,
      page_size: 50,
      share_rate_percent: 30,
      totals: {
        invitee_count: 4,
        request_count: 21,
        revenue: 12.5,
        estimated_cost: 8,
        gross_profit: 4.5,
        share_amount: 1.35,
      },
    })
  })

  it('loads invite usage profit totals using the selected share rate', async () => {
    const wrapper = mount(AdminAffiliateProfitView, {
      global: {
        stubs: {
          AppLayout: { template: '<main><slot /></main>' },
          TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
          DataTable: { template: '<div />' },
          Pagination: true,
          Icon: true,
        },
      },
    })
    await flushPromises()

    expect(getProfitReport).toHaveBeenCalledWith(expect.objectContaining({
      page: 1,
      page_size: 50,
      share_rate_percent: 30,
      start_at: expect.stringMatching(/^\d{4}-\d{2}-\d{2}$/),
      end_at: expect.stringMatching(/^\d{4}-\d{2}-\d{2}$/),
    }))
    expect(wrapper.text()).toContain('$12.5000')
    expect(wrapper.text()).toContain('$8.0000')
    expect(wrapper.text()).toContain('$4.5000')
    expect(wrapper.text()).toContain('36.00%')
    expect(wrapper.text()).toContain('$1.3500')
  })
})
