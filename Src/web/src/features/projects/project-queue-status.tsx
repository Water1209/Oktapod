import { useEffect } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { api } from '../../shared/api/client'

export function ProjectQueueStatus({ projectId, conversationId }: { projectId: string; conversationId?: string }) {
  const client = useQueryClient()
  const queue = useQuery({ queryKey: ['project-admission', conversationId], enabled: Boolean(conversationId), queryFn: async () => {
    const [pending, starting] = await Promise.all([api.conversationQueue(conversationId!, 'pending'), api.conversationQueue(conversationId!, 'claimed')])
    return { items: [...pending.items, ...starting.items] }
  }, refetchInterval: (query) => query.state.data?.items.length ? 2000 : false })
  const tasks = useQuery({ queryKey: ['tasks', projectId], queryFn: () => api.tasks(projectId) })
  const blocker = tasks.data?.items.find((task) => task.conversation_id === conversationId && ['queued', 'running', 'waiting_approval', 'waiting_input', 'paused', 'blocked'].includes(task.status))
  const cancel = useMutation({ mutationFn: (id: string) => api.cancelConversationQueueItem(conversationId!, id), onSuccess: async () => { await client.invalidateQueries({ queryKey: ['project-admission', conversationId] }) } })
  useEffect(() => {
    if (queue.dataUpdatedAt) void client.invalidateQueries({ queryKey: ['tasks', projectId] })
  }, [client, projectId, queue.dataUpdatedAt])
  if (!queue.data?.items.length) return null
  return <section className="task-capsule" aria-label="会话消息排队"><strong>此会话有 {queue.data.items.length} 条消息等待执行</strong>{blocker ? <p>正在等待「{blocker.title}」结束。<Link to="/projects/$projectId" params={{ projectId }} search={{ conversation: blocker.conversation_id }}>打开阻塞任务，处理审批、恢复或取消</Link></p> : <p>正在准备启动；页面关闭后仍会保留排队消息。</p>}{queue.data.items.map((item) => <p key={item.id}>{item.content} {item.status === 'pending' ? <button type="button" disabled={cancel.isPending} onClick={() => cancel.mutate(item.id)}>撤回</button> : <span>正在启动</span>}</p>)}{cancel.error ? <p role="alert">{cancel.error.message}</p> : null}</section>
}
