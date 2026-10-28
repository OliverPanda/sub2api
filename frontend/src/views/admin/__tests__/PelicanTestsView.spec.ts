import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en'
import PelicanTestsView from '../PelicanTestsView.vue'

const mocks = vi.hoisted(() => ({
  getAllGroups: vi.fn(),
  getModelAllowlistCandidates: vi.fn(),
  listRuns: vi.fn(),
  createRun: vi.fn(),
  getRun: vi.fn(),
  deleteRun: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: {
      getAll: mocks.getAllGroups,
      getModelAllowlistCandidates: mocks.getModelAllowlistCandidates
    },
    pelicanTests: {
      listRuns: mocks.listRuns,
      createRun: mocks.createRun,
      getRun: mocks.getRun,
      deleteRun: mocks.deleteRun
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: mocks.showError,
    showSuccess: mocks.showSuccess
  })
}))

vi.mock('@/utils/sanitize', () => ({
  sanitizeSvg: (svg: string) => svg
}))

const i18n = createI18n({
  legacy: false,
  locale: 'en',
  messages: { en }
})

const SelectStub = {
  props: ['modelValue', 'options', 'disabled'],
  emits: ['update:modelValue'],
  template: `
    <select :value="String(modelValue ?? '')" :disabled="disabled">
      <option v-for="option in options" :key="String(option.value)" :value="String(option.value)">
        {{ option.label }}
      </option>
    </select>
  `
}

const runSummary = {
  id: 12,
  group_id: 1,
  group_name: 'Core Anthropic',
  model_id: 'claude-sonnet-4-6',
  prompt: 'Draw a pelican.',
  status: 'completed' as const,
  total_accounts: 2,
  success_count: 2,
  failed_count: 0,
  skipped_count: 0,
  error_message: '',
  created_by: 1,
  started_at: '2026-10-27T00:00:00Z',
  finished_at: '2026-10-27T00:00:03Z',
  created_at: '2026-10-27T00:00:00Z'
}

const runDetail = {
  ...runSummary,
  results: [
    {
      id: 8,
      run_id: 12,
      group_id: 1,
      account_id: 4,
      account_name: 'Primary Account',
      platform: 'anthropic',
      model_id: 'claude-sonnet-4-6',
      status: 'success' as const,
      raw_output: '<svg xmlns="http://www.w3.org/2000/svg"><circle cx="5" cy="5" r="5"/></svg>',
      svg_content: '<svg xmlns="http://www.w3.org/2000/svg"><circle cx="5" cy="5" r="5"/></svg>',
      error_message: '',
      latency_ms: 1234,
      started_at: '2026-10-27T00:00:00Z',
      finished_at: '2026-10-27T00:00:01Z',
      created_at: '2026-10-27T00:00:01Z'
    },
    {
      id: 9,
      run_id: 12,
      group_id: 1,
      account_id: 5,
      account_name: 'Image Account',
      platform: 'openai',
      model_id: 'gpt-image-2',
      status: 'success' as const,
      image_url: 'data:image/png;base64,iVBORw0KGgo=',
      error_message: '',
      latency_ms: 2345,
      started_at: '2026-10-27T00:00:02Z',
      finished_at: '2026-10-27T00:00:03Z',
      created_at: '2026-10-27T00:00:03Z'
    }
  ]
}

const mountView = async () => {
  const wrapper = mount(PelicanTestsView, {
    global: {
      plugins: [i18n],
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' },
        ConfirmDialog: true,
        LoadingSpinner: true,
        Select: SelectStub,
        Icon: true
      }
    }
  })
  await flushPromises()
  return wrapper
}

describe('PelicanTestsView', () => {
  beforeEach(() => {
    mocks.getAllGroups.mockReset().mockResolvedValue([
      {
        id: 1,
        name: 'Core Anthropic',
        platform: 'anthropic',
        active_account_count: 3
      }
    ])
    mocks.getModelAllowlistCandidates.mockReset().mockResolvedValue(['claude-sonnet-4-6'])
    mocks.listRuns.mockReset().mockResolvedValue([runSummary])
    mocks.getRun.mockReset().mockResolvedValue(runDetail)
    mocks.createRun.mockReset().mockResolvedValue({
      ...runSummary,
      id: 13,
      status: 'running',
      success_count: 0,
      finished_at: null
    })
    mocks.deleteRun.mockReset().mockResolvedValue(undefined)
    mocks.showError.mockReset()
    mocks.showSuccess.mockReset()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('loads history and renders the selected group result', async () => {
    const wrapper = await mountView()

    expect(mocks.listRuns).toHaveBeenCalledWith({ limit: 50 })
    expect(mocks.getRun).toHaveBeenCalledWith(12)
    expect(wrapper.text()).toContain('Core Anthropic')
    expect(wrapper.text()).toContain('Primary Account')
    expect(wrapper.text()).toContain('1.23 s')
    expect(wrapper.text()).toContain('Image Account')
    expect(wrapper.find('iframe').exists()).toBe(true)
    expect(wrapper.find('img').exists()).toBe(true)

    wrapper.unmount()
  })

  it('starts a test with the selected group, model and prompt', async () => {
    const wrapper = await mountView()
    const selects = wrapper.findAllComponents(SelectStub)

    selects[0].vm.$emit('update:modelValue', 1)
    await flushPromises()
    expect(mocks.getModelAllowlistCandidates).toHaveBeenCalledWith(1, 'anthropic')

    selects[1].vm.$emit('update:modelValue', 'claude-sonnet-4-6')
    await wrapper.get('#pelican-prompt').setValue('custom pelican prompt')
    const startButton = wrapper.get('[data-testid="pelican-start"]')
    await startButton.trigger('click')
    await flushPromises()

    expect(mocks.createRun).toHaveBeenCalledWith({
      group_id: 1,
      model_id: 'claude-sonnet-4-6',
      prompt: 'custom pelican prompt'
    })
    expect(mocks.showSuccess).toHaveBeenCalled()

    wrapper.unmount()
  })
})
