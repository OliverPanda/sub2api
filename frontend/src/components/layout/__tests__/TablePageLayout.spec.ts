import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import TablePageLayout from '../TablePageLayout.vue'

const componentPath = resolve(dirname(fileURLToPath(import.meta.url)), '../TablePageLayout.vue')
const componentSource = readFileSync(componentPath, 'utf8')

describe('TablePageLayout responsive table scrolling', () => {
  it('renders default slot content above the named layout sections', () => {
    const wrapper = mount(TablePageLayout, {
      slots: {
        default: '<div data-testid="page-overview">Overview</div>',
        filters: '<div data-testid="filters">Filters</div>',
        table: '<div data-testid="table">Table</div>',
      },
    })

    const html = wrapper.html()
    expect(wrapper.get('[data-testid="page-overview"]').text()).toBe('Overview')
    expect(html.indexOf('page-overview')).toBeLessThan(html.indexOf('data-testid="filters"'))
    expect(html.indexOf('page-overview')).toBeLessThan(html.indexOf('data-testid="table"'))
  })

  it('does not disable the table horizontal scroll container in mobile mode', () => {
    const tableWrapperBlocks = Array.from(
      componentSource.matchAll(/([^{}]*:deep\(\.table-wrapper\)[^{}]*)\{([^{}]*)\}/g)
    )

    expect(tableWrapperBlocks.length).toBeGreaterThan(0)

    const baseBlock = tableWrapperBlocks.find(([selector]) => !selector.includes('.mobile-mode'))
    const mobileBlocks = tableWrapperBlocks.filter(([selector]) => selector.includes('.mobile-mode'))

    expect(baseBlock?.[2]).toContain('overflow-x-auto')
    expect(mobileBlocks.every(([, , declarations]) => !declarations.includes('overflow-visible'))).toBe(
      true
    )
  })
})
