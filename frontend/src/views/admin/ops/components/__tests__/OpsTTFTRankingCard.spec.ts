import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import OpsTTFTRankingCard from '../OpsTTFTRankingCard.vue'

const mockGetTTFTRanking = vi.fn()

vi.mock('@/api/admin/ops', () => ({
  opsAPI: { getTTFTRanking: (...args: any[]) => mockGetTTFTRanking(...args) },
}))

vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))

const SelectStub = defineComponent({
  props: { modelValue: { type: [String, Number], default: '' }, options: { type: Array, default: () => [] } },
  emits: ['update:modelValue'],
  template: '<div class="select-stub" />',
})

const EmptyStateStub = defineComponent({
  props: { title: { type: String, default: '' }, description: { type: String, default: '' } },
  template: '<div class="empty-state">{{ title }}|{{ description }}</div>',
})

const rows = [
  { id: 1, name: 'fast-group', platform: 'openai', request_count: 40, sample_count: 40, avg_ms: 800, p50_ms: 700, p95_ms: 1500, p99_ms: 2200, max_ms: 3000 },
  { id: 2, name: 'slow-group', platform: 'openai', request_count: 5, sample_count: 5, avg_ms: 8000, p50_ms: 8000, p95_ms: 12000, p99_ms: 16000, max_ms: 17000 },
]

describe('OpsTTFTRankingCard', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('ranks groups by P99 descending by default and supports ascending sort', async () => {
    mockGetTTFTRanking.mockResolvedValue({ dimension: 'group', items: rows })
    const wrapper = mount(OpsTTFTRankingCard, {
      props: { refreshToken: 0 },
      global: { stubs: { Select: SelectStub, EmptyState: EmptyStateStub } },
    })
    await flushPromises()

    const getNames = () => wrapper.findAll('tbody tr').map(row => row.find('td').text().split('#')[0].trim())
    expect(getNames()).toEqual(['slow-group', 'fast-group'])
    expect(wrapper.findAll('tbody tr')[0].text()).toContain('admin.ops.ttftRanking.lowSample')

    const p99Header = wrapper.findAll('thead button').find(button => button.text().includes('p99_ms'))
    expect(p99Header).toBeDefined()
    await p99Header!.trigger('click')
    expect(getNames()).toEqual(['fast-group', 'slow-group'])
    expect(mockGetTTFTRanking).toHaveBeenCalledWith({ time_range: '1h', dimension: 'group' })
  })
})
