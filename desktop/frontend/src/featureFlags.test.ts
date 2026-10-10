import { describe, expect, it } from 'vitest'
import { isCloudflarePocEnabled } from './featureFlags'

describe('isCloudflarePocEnabled', () => {
  it('is disabled by default and for false-like values', () => {
    expect(isCloudflarePocEnabled(undefined)).toBe(false)
    expect(isCloudflarePocEnabled('')).toBe(false)
    expect(isCloudflarePocEnabled('false')).toBe(false)
    expect(isCloudflarePocEnabled('0')).toBe(false)
  })

  it('accepts explicit true values', () => {
    expect(isCloudflarePocEnabled('true')).toBe(true)
    expect(isCloudflarePocEnabled(' TRUE ')).toBe(true)
    expect(isCloudflarePocEnabled('1')).toBe(true)
  })
})
