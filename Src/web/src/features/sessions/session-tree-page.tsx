import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams, useSearch } from '@tanstack/react-router'
import { GitBranch, GitFork, MessageSquare, Network, PencilLine, Split, Tag } from 'lucide-react'
import { api, type SessionEntry } from '../../shared/api/client'
import { hasLiveTasks } from '../../shared/task-status'

type TreeSearch = { thread?: string }

export function SessionTreePage() {
  const { projectId } = useParams({ from: '/projects/$projectId/tree' })
  const search = useSearch({ from: '/projects/$projectId/tree' }) as TreeSearch
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const conversations = useQuery({ queryKey: ['conversations', projectId], queryFn: () => api.conversations(projectId) })
  const tasks = useQuery({ queryKey: ['tasks', projectId], queryFn: () => api.tasks(projectId), refetchInterval: (query) => hasLiveTasks(query.state.data?.items) ? 2_000 : false })
  const conversationId = search.thread ?? conversations.data?.items[0]?.id
  const tree = useQuery({ queryKey: ['session-tree', conversationId], queryFn: () => api.sessionEntries(conversationId!), enabled: Boolean(conversationId) })
  const [selectedId, setSelectedId] = useState<string>()
  const selected = tree.data?.items.find((entry) => entry.id === selectedId) ?? tree.data?.items.find((entry) => entry.id === tree.data?.active_leaf_entry_id) ?? tree.data?.items.at(-1)
  const activeRun = (tasks.data?.items ?? []).some((task) => task.conversation_id === conversationId && ['queued', 'running', 'waiting_approval', 'waiting_input'].includes(task.status))
  const depthById = useMemo(() => {
    const result = new Map<string, number>()
    for (const entry of tree.data?.items ?? []) {
      let depth = 0
      let parent = entry.parent_entry_id
      const seen = new Set<string>()
      while (parent && !seen.has(parent)) {
        seen.add(parent)
        depth++
        parent = tree.data?.items.find((item) => item.id === parent)?.parent_entry_id ?? ''
      }
      result.set(entry.id, depth)
    }
    return result
  }, [tree.data])
  const refresh = async () => {
    await queryClient.invalidateQueries({ queryKey: ['session-tree', conversationId] })
    await queryClient.invalidateQueries({ queryKey: ['conversations', projectId] })
  }
  const move = useMutation({ mutationFn: (entry: SessionEntry) => api.navigateSession(conversationId!, entry.id, tree.data?.active_leaf_entry_id ?? ''), onSuccess: async (result) => { await refresh(); if (result.composer_text) window.sessionStorage.setItem(`oktapod.composer.prefill.${conversationId}`, result.composer_text); await navigate({ to: '/projects/$projectId/threads/$threadId', params: { projectId, threadId: conversationId! } }) } })
  const fork = useMutation({ mutationFn: (entry: SessionEntry) => api.forkSession(conversationId!, entry.id, `${conversations.data?.items.find((item) => item.id === conversationId)?.title || '会话'} · 分支`), onSuccess: async (created) => { await refresh(); await navigate({ to: '/projects/$projectId/threads/$threadId', params: { projectId, threadId: created.id } }) } })
  const clone = useMutation({ mutationFn: () => api.cloneSession(conversationId!, `${conversations.data?.items.find((item) => item.id === conversationId)?.title || '会话'} · 副本`), onSuccess: async (created) => { await refresh(); await navigate({ to: '/projects/$projectId/threads/$threadId', params: { projectId, threadId: created.id } }) } })
  const label = useMutation({ mutationFn: ({ entry, value }: { entry: SessionEntry; value: string }) => api.labelSessionEntry(conversationId!, entry.id, value, tree.data?.active_leaf_entry_id ?? ''), onSuccess: refresh })

  return <main className="tree-page">
    <header className="tree-heading"><div><span className="eyebrow">项目 / 会话分支</span><h1>会话树</h1><p>历史不可变；可以切换当前分支，或从历史节点创建新的会话分支。</p></div><label><span>会话</span><select value={conversationId ?? ''} onChange={(event) => void navigate({ to: '/projects/$projectId/tree', params: { projectId }, search: { thread: event.target.value } })}>{conversations.data?.items.map((item) => <option value={item.id} key={item.id}>{item.title || '未命名会话'}</option>)}</select></label></header>
    {activeRun ? <div className="tree-busy"><span className="status-orb active" /><strong>当前会话正在运行</strong><span>请先结束或中止任务，再切换或创建分支。</span></div> : null}
    <div className="tree-workspace">
      <section className="tree-canvas" aria-label="Session Tree">
        {!conversationId ? <div className="detail-empty"><Network size={28} /><h2>还没有会话</h2><p>发送第一条消息后会建立可分支的会话树。</p></div> : null}
        {tree.data?.items.map((entry) => <button type="button" key={entry.id} className={`${entry.id === selected?.id ? 'selected' : ''} ${entry.id === tree.data.active_leaf_entry_id ? 'leaf' : ''}`} style={{ '--tree-depth': depthById.get(entry.id) ?? 0 } as React.CSSProperties} onClick={() => setSelectedId(entry.id)}>
          <span className="tree-rail"><i /></span>
          <span className="tree-node">{entry.kind === 'message' ? <MessageSquare size={12} /> : entry.kind === 'branch_summary' ? <Split size={12} /> : <GitBranch size={12} />}</span>
          <span><strong>{entry.label || entry.summary || stringPayload(entry.payload) || entry.kind}</strong><small>#{entry.entry_seq} · {new Date(entry.created_at).toLocaleString('zh-CN')}</small></span>
        </button>)}
      </section>
      <aside className="tree-preview">
        {selected ? <><header><span className={`tree-node ${selected.id === tree.data?.active_leaf_entry_id ? 'leaf' : ''}`}><GitBranch size={14} /></span><div><span className="eyebrow">节点 #{selected.entry_seq}</span><h2>{selected.label || selected.kind}</h2></div></header><dl><div><dt>类型</dt><dd>{selected.kind}</dd></div><div><dt>上下文</dt><dd>{selected.context_injection}</dd></div><div><dt>引用</dt><dd><code>{selected.ref_id || '—'}</code></dd></div><div><dt>结构版本</dt><dd>v{selected.schema_version}</dd></div></dl><section><h3>内容预览</h3><p>{selected.summary || stringPayload(selected.payload) || '该节点没有文本摘要。'}</p></section><footer><button type="button" disabled={activeRun || move.isPending} onClick={() => { const action = selected.kind === 'message' && typeof selected.payload.role === 'string' && selected.payload.role === 'user' ? '重新编辑此用户消息并移动当前分支？' : '从此节点继续？'; if (window.confirm(action)) move.mutate(selected) }}>{selected.payload.role === 'user' ? <PencilLine size={14} /> : <GitBranch size={14} />}{selected.payload.role === 'user' ? '重新编辑' : '从此继续'}</button><button type="button" disabled={activeRun || fork.isPending} onClick={() => fork.mutate(selected)}><GitFork size={14} />创建分支</button><button type="button" disabled={activeRun || label.isPending} onClick={() => { const value = window.prompt('输入标签名称', selected.label); if (value?.trim()) label.mutate({ entry: selected, value: value.trim() }) }}><Tag size={14} />添加标签</button><button type="button" disabled={activeRun || clone.isPending} onClick={() => clone.mutate()}><Split size={14} />复制当前路径</button></footer>{move.error || fork.error || clone.error || label.error ? <div className="form-error">{move.error?.message ?? fork.error?.message ?? clone.error?.message ?? label.error?.message}</div> : null}</> : <div className="detail-empty"><Network size={28} /><h2>选择一个节点</h2></div>}
      </aside>
    </div>
    {conversationId ? <Link className="tree-back-link" to="/projects/$projectId/threads/$threadId" params={{ projectId, threadId: conversationId }}>返回会话</Link> : null}
  </main>
}

function stringPayload(payload: Record<string, unknown>) {
  for (const key of ['content', 'text', 'summary']) {
    if (typeof payload[key] === 'string') return payload[key] as string
  }
  return ''
}
