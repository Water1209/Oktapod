import { useQuery } from '@tanstack/react-query'
import { Navigate, useParams } from '@tanstack/react-router'
import { api } from '../../shared/api/client'

export function LegacyTaskResolver() {
  const { taskId } = useParams({ from: '/tasks/$taskId' })
  const task = useQuery({ queryKey: ['task', taskId], queryFn: () => api.task(taskId), retry: false })

  if (task.isLoading) return <div className="page-loading">正在定位任务所属会话…</div>
  if (task.error || !task.data) {
    return <div className="empty-panel"><h1>任务无法打开</h1><p>{task.error?.message ?? '任务不存在或当前不可见。'}</p></div>
  }

  return <Navigate
    replace
    to="/projects/$projectId/threads/$threadId"
    params={{ projectId: task.data.project_id, threadId: task.data.conversation_id }}
    search={{ inspector: 'run', event: task.data.run_id }}
  />
}
