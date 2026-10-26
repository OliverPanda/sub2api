import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import OpsGroupQualityRanking from '../OpsGroupQualityRanking.vue'

const mockGetGroupQualityRanking = vi.fn()

vi.mock('@/api/admin/ops', () => ({
  opsAPI: {
    getGroupQualityRanking: (...args: any[]) => mockGetGroupQualityRanking(...args)
  }
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, any>) => {
        if (key === 'admin.ops.groupQuality.insufficient') return `insufficient:${params?.count}`
        if (key === 'admin.ops.groupQuality.status.insufficient') return 'status:insufficient'
        if (key === 'admin.ops.groupQuality.showAll') return `showAll:${params?.count}`
        if (key === 'admin.ops.groupQuality.description') return `description:${params?.sample}`
        return key
      }
    })
  }
})

const EmptyStateStub = defineComponent({
  name: 'EmptyState',
  props: { title: { type: String, default: '' } },
  template: '<div class="empty-state">{{ title }}</div>'
})

const sampleResponse = {
  start_time: '2026-10-21T00:00:00Z',
  end_time: '2026-10-21T01:00:00Z',
  platform: '',
  min_sample: 20,
  insufficient_sample_count: 1,
  items: [
    {
      group_id: 12,
      group_name: 'unstable-relay',
      platform: 'openai',
      request_count: 40,
      success_count: 31,
      error_count_sla: 9,
      success_rate: 0.775,
      upstream_error_rate: 0.2,
      rate_limit_rate: 0.05,
      p95_duration_ms: 2300,
      p95_ttft_ms: 850,
      sample_count: 40,
      sample_sufficient: true
    },
    {
      group_id: 13,
      group_name: 'new-group',
      platform: 'anthropic',
      request_count: 5,
      success_count: 5,
      error_count_sla: 0,
      success_rate: 1,
      upstream_error_rate: 0,
      rate_limit_rate: 0,
      p95_duration_ms: null,
      p95_ttft_ms: null,
      sample_count: 5,
      sample_sufficient: false
    }
  ]
}

describe('OpsGroupQualityRanking', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('loads the ranking with SLA filters and renders the worst group first', async () => {
    mockGetGroupQualityRanking.mockResolvedValue(sampleResponse)

    const wrapper = mount(OpsGroupQualityRanking, {
      props: {
        platformFilter: 'openai',
        groupIdFilter: 12,
        timeRange: '6h',
        queryMode: 'raw',
        refreshToken: 3,
        minSample: 50
      },
      global: {
        stubs: {
          EmptyState: EmptyStateStub,
          HelpTooltip: true
        }
      }
    })
    await flushPromises()

    expect(mockGetGroupQualityRanking).toHaveBeenCalledTimes(1)
    expect(mockGetGroupQualityRanking).toHaveBeenCalledWith(
      {
        time_range: '6h',
        platform: 'openai',
        group_id: 12,
        mode: 'raw',
        min_sample: 50
      },
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    )

    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('unstable-relay')
    expect(rows[0].text()).toContain('77.50%')
    expect(rows[0].text()).toContain('2.30s')
    expect(rows[1].text()).toContain('new-group')
    expect(rows[1].text()).toContain('status:insufficient')
    expect(wrapper.text()).toContain('insufficient:1')
  })

  it('uses custom times and emits the selected group for dashboard drilldown', async () => {
    mockGetGroupQualityRanking.mockResolvedValue(sampleResponse)

    const wrapper = mount(OpsGroupQualityRanking, {
      props: {
        timeRange: 'custom',
        customStartTime: '2026-10-20T00:00:00Z',
        customEndTime: '2026-10-21T00:00:00Z',
        refreshToken: 0
      },
      global: {
        stubs: {
          EmptyState: EmptyStateStub,
          HelpTooltip: true
        }
      }
    })
    await flushPromises()

    expect(mockGetGroupQualityRanking).toHaveBeenCalledWith(
      expect.objectContaining({
        start_time: '2026-10-20T00:00:00Z',
        end_time: '2026-10-21T00:00:00Z',
        min_sample: 20
      }),
      expect.any(Object)
    )

    await wrapper.findAll('tbody tr')[0].find('button').trigger('click')
    expect(wrapper.emitted('selectGroup')).toEqual([[12]])
  })

  it('refreshes when the shared dashboard refresh token changes', async () => {
    mockGetGroupQualityRanking.mockResolvedValue(sampleResponse)

    const wrapper = mount(OpsGroupQualityRanking, {
      props: { refreshToken: 0 },
      global: {
        stubs: {
          EmptyState: EmptyStateStub,
          HelpTooltip: true
        }
      }
    })
    await flushPromises()
    expect(mockGetGroupQualityRanking).toHaveBeenCalledTimes(1)

    await wrapper.setProps({ refreshToken: 1 })
    await flushPromises()
    expect(mockGetGroupQualityRanking).toHaveBeenCalledTimes(2)
  })
})
