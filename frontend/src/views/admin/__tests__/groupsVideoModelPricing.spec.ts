import { describe, expect, it } from 'vitest'

import {
  createVideoModelPricesForm,
  addVideoModelPrice,
  renameVideoModelPrice,
  removeVideoModelPrice,
  serializeVideoModelPrices,
  videoModelPriceFamilyRows,
  videoPriceResolutions
} from '../groupsVideoModelPricing'

describe('Grok video model pricing form', () => {
  it('provides editable rows for both canonical Grok video families', () => {
    const form = createVideoModelPricesForm()

    expect(videoModelPriceFamilyRows(form).map(({ key }) => key)).toEqual([
      'grok-imagine-video',
      'grok-imagine-video-1.5'
    ])
    expect(form['grok-imagine-video']['480p']).toBeNull()
    expect(form['grok-imagine-video-1.5']['1080p']).toBeNull()
  })

  it('serializes only finite non-negative prices and preserves future families', () => {
    const form = createVideoModelPricesForm({
      'grok-imagine-video-2': { '1080p': 0.4 }
    })
    form['grok-imagine-video']['480p'] = 0.05
    form['grok-imagine-video']['720p'] = ''
    form['grok-imagine-video-1.5']['1080p'] = -1

    expect(serializeVideoModelPrices(form)).toEqual({
      'grok-imagine-video': { '480p': 0.05 },
      'grok-imagine-video-2': { '1080p': 0.4 }
    })
  })

  it('round-trips unknown model families so editing does not discard them', () => {
    const form = createVideoModelPricesForm({
      'grok-imagine-video-2': { '480p': 0.2 }
    })

    expect(videoModelPriceFamilyRows(form).map(({ key }) => key)).toContain(
      'grok-imagine-video-2'
    )
    expect(serializeVideoModelPrices(form)).toMatchObject({
      'grok-imagine-video-2': { '480p': 0.2 }
    })
  })
})

describe('compatible video aliases', () => {
  it('adds arbitrary aliases without replacing existing prices', () => {
    const form = createVideoModelPricesForm()
    expect(addVideoModelPrice(form, ' Video-Veo ')).toBe(true)
    form['video-veo']['720p'] = 0.25
    expect(addVideoModelPrice(form, 'video-veo')).toBe(false)
    expect(addVideoModelPrice(form, '  ')).toBe(false)
    expect(serializeVideoModelPrices(form)).toEqual({ 'video-veo': { '720p': 0.25 } })
    expect(videoModelPriceFamilyRows(form).map(({ key }) => key)).toContain('video-veo')
  })
})

it('keeps MiniMax quality tiers separate from flat low-resolution prices', () => {
  const form = createVideoModelPricesForm({ 'video-minimax-h3': { '768p': 0.2, '2k': 0.4 } })
  expect(serializeVideoModelPrices(form)).toEqual({ 'video-minimax-h3': { '768p': 0.2, '2k': 0.4 } })
  expect(form['video-minimax-h3']['480p']).toBeNull()
})

it('offers the shared quality tiers for every compatible upstream model', () => {
  expect(videoPriceResolutions('openai').map(({ key }) => key)).toEqual(['480p', '720p', '768p', '1080p', '2k', '4k'])
})

describe('video model row editing', () => {
  it('renames a model while preserving its prices', () => {
    const form = createVideoModelPricesForm({ 'seedance2.5': { '480p': 0.44 } })

    expect(renameVideoModelPrice(form, 'seedance2.5', 'seedance-2.5')).toBe(true)
    expect(form['seedance-2.5']['480p']).toBe(0.44)
    expect(videoModelPriceFamilyRows(form).map(({ key }) => key)).toContain('seedance-2.5')
    expect(videoModelPriceFamilyRows(form).map(({ key }) => key)).not.toContain('seedance2.5')
  })

  it('rejects duplicate names and removes a row', () => {
    const form = createVideoModelPricesForm({ first: { '480p': 0.1 }, second: { '720p': 0.2 } })

    expect(renameVideoModelPrice(form, 'first', 'second')).toBe(false)
    expect(removeVideoModelPrice(form, 'first')).toBe(true)
    expect(videoModelPriceFamilyRows(form).map(({ key }) => key)).not.toContain('first')
    expect(form.second['720p']).toBe(0.2)
  })

  it('does not re-add a deleted default Grok row', () => {
    const form = createVideoModelPricesForm()

    expect(removeVideoModelPrice(form, 'grok-imagine-video')).toBe(true)
    expect(videoModelPriceFamilyRows(form).map(({ key }) => key)).not.toContain('grok-imagine-video')
    expect(serializeVideoModelPrices(form)).not.toHaveProperty('grok-imagine-video')
  })
})
