import { describe, expect, it } from 'vitest'
import type { TaskStatus } from '../../shared/api/client'
import { taskStreamPresentation } from './task-stream-presentation'

describe('taskStreamPresentation', () => {
  it.each([
    ['queued', '任务等待开始', 'running'],
    ['running', '校验 HTML', 'running'],
    ['waiting_approval', '任务等待审批', 'paused'],
    ['waiting_input', '任务等待输入', 'paused'],
    ['paused', '任务已暂停', 'paused'],
    ['blocked', '任务执行受阻', 'failed'],
    ['failed', '任务执行失败', 'failed'],
    ['canceled', '任务已取消', 'canceled'],
    ['succeeded', '任务已完成', 'succeeded'],
  ] satisfies Array<[TaskStatus, string, string]>)('maps %s without inferring success from an inactive stream', (status, label, tone) => {
    expect(taskStreamPresentation(status, status === 'running' ? '校验 HTML' : undefined)).toMatchObject({ label, tone })
  })

  it('keeps a specific terminal failure title', () => {
    expect(taskStreamPresentation('failed', undefined, '模型服务请求超时').label).toBe('模型服务请求超时')
  })
})
