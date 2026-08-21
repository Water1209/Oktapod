import type { TaskStatus } from './api/client'

// These states can change without another user action while a task is live.
// Paused and terminal states are refreshed by their explicit action or mutation.
export const LIVE_TASK_STATUSES: ReadonlySet<TaskStatus> = new Set([
  'queued',
  'running',
  'waiting_approval',
  'waiting_input',
])

export function isLiveTaskStatus(status: TaskStatus) {
  return LIVE_TASK_STATUSES.has(status)
}

export function hasLiveTasks(tasks: ReadonlyArray<{ status: TaskStatus }> | undefined) {
  return Boolean(tasks?.some((task) => isLiveTaskStatus(task.status)))
}
