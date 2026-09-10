import { describe, expect, it } from 'vitest'
import { AuthLifecycle } from './auth-lifecycle'

describe('authenticated workspace lifetime', () => {
  it('aborts outstanding requests and invalidates consumers only once', () => {
    const lifetime = new AuthLifecycle()
    let calls = 0
    const unsubscribe = lifetime.subscribe(() => { calls++; expect(lifetime.signal.aborted).toBe(true) })
    lifetime.invalidate()
    lifetime.invalidate()
    expect(calls).toBe(1)
    expect(lifetime.snapshot()).toBe(true)
    unsubscribe()
    const nextDocument = new AuthLifecycle()
    expect(nextDocument.snapshot()).toBe(false)
    expect(nextDocument.signal.aborted).toBe(false)
  })
})
