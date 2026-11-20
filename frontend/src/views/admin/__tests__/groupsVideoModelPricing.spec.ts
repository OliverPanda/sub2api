import { describe, expect, it } from 'vitest'

import {
  createVideoModelPricesForm,
  addVideoModelPrice,
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
  expect(videoPriceResolutions('openai').map(({ key }) => key)).toEqual(['480p', '720p', '768p', '1080p', '2k'])
})
