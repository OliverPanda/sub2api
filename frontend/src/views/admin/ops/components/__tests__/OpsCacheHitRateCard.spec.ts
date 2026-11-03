import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import OpsCacheHitRateCard from '../OpsCacheHitRateCard.vue'

vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))

describe('OpsCacheHitRateCard', () => {
  it('shows token-weighted hit rate and token composition', () => {
    const wrapper = mount(OpsCacheHitRateCard, {
      props: {
        loading: false,
        cacheStats: {
          input_tokens: 300,
          cache_read_tokens: 600,
          cache_creation_tokens: 100,
          hit_rate: 0.6,
        },
      },
      global: { stubs: { HelpTooltip: true } },
    })

    expect(wrapper.text()).toContain('60.0%')
    expect(wrapper.text()).toContain('600 / 1,000')
    expect(wrapper.text()).toContain('admin.ops.cacheStats.read')
    expect(wrapper.text()).toContain('admin.ops.cacheStats.write')
    expect(wrapper.text()).toContain('admin.ops.cacheStats.uncached')
    expect(wrapper.findAll('.h-full')[0].attributes('style')).toContain('60%')
    expect(wrapper.findAll('.h-full')[1].attributes('style')).toContain('10%')
    expect(wrapper.findAll('.h-full')[2].attributes('style')).toContain('30%')
  })

  it('shows the empty state when the selected window has no input tokens', () => {
    const wrapper = mount(OpsCacheHitRateCard, {
      props: {
        loading: false,
        cacheStats: {
          input_tokens: 0,
          cache_read_tokens: 0,
          cache_creation_tokens: 0,
          hit_rate: null,
        },
      },
      global: { stubs: { HelpTooltip: true } },
    })

    expect(wrapper.text()).toContain('admin.ops.cacheStats.empty')
    expect(wrapper.text()).not.toContain('NaN%')
  })
})
