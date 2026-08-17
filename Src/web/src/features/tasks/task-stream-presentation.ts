import type { TaskStatus } from '../../shared/api/client'

export type TaskStreamTone = 'running' | 'paused' | 'failed' | 'canceled' | 'succeeded'

export interface TaskStreamPresentation {
  label: string
  tone: TaskStreamTone
  spinning: boolean
}

// The task record is the lifecycle authority. A stopped event stream only means
// that no more events are arriving; it must never be interpreted as success.
export function taskStreamPresentation(status: TaskStatus, runningStepLabel?: string, failureTitle?: string): TaskStreamPresentation {
  switch (status) {
    case 'queued':
      return { label: '任务等待开始', tone: 'running', spinning: true }
    case 'running':
      return { label: runningStepLabel || '任务执行中', tone: 'running', spinning: true }
    case 'waiting_approval':
      return { label: '任务等待审批', tone: 'paused', spinning: false }
    case 'waiting_input':
      return { label: '任务等待输入', tone: 'paused', spinning: false }
    case 'paused':
      return { label: '任务已暂停', tone: 'paused', spinning: false }
    case 'blocked':
      return { label: failureTitle || '任务执行受阻', tone: 'failed', spinning: false }
    case 'failed':
      return { label: failureTitle || '任务执行失败', tone: 'failed', spinning: false }
    case 'canceled':
      return { label: '任务已取消', tone: 'canceled', spinning: false }
    case 'succeeded':
      return { label: '任务已完成', tone: 'succeeded', spinning: false }
  }
}
