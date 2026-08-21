import { describe, expect, it } from 'vitest'
import { hasLiveTasks, isLiveTaskStatus } from './task-status'

describe('task live status', () => {
  it('keeps polling only while a task can change without a new user action', () => {
    expect(isLiveTaskStatus('queued')).toBe(true)
    expect(isLiveTaskStatus('running')).toBe(true)
    expect(isLiveTaskStatus('waiting_approval')).toBe(true)
    expect(isLiveTaskStatus('waiting_input')).toBe(true)
    expect(isLiveTaskStatus('paused')).toBe(false)
    expect(isLiveTaskStatus('succeeded')).toBe(false)
  })

  it('detects a live task in a project result', () => {
    expect(hasLiveTasks([{ status: 'succeeded' }])).toBe(false)
    expect(hasLiveTasks([{ status: 'succeeded' }, { status: 'running' }])).toBe(true)
    expect(hasLiveTasks(undefined)).toBe(false)
  })
})
