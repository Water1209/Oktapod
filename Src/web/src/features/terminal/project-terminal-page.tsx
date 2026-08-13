import { useState } from 'react'
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { AlertTriangle, Ban, CornerDownLeft, MessageSquarePlus, ShieldCheck, SquareTerminal } from 'lucide-react'
import { api, type Task } from '../../shared/api/client'

type TerminalEntry = {
  command: string
  task: Task
}

export function ProjectTerminalPage() {
  const { projectId } = useParams({ from: '/projects/$projectId/terminal' })
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const project = useQuery({ queryKey: ['project', projectId], queryFn: () => api.project(projectId) })
  const [command, setCommand] = useState('')
  const [history, setHistory] = useState<TerminalEntry[]>([])
  const taskQueries = useQueries({
    queries: history.map((entry) => ({
      queryKey: ['task', entry.task.id],
      queryFn: () => api.task(entry.task.id),
      refetchInterval: (query: { state: { data?: Task } }) => query.state.data && ['succeeded', 'failed', 'blocked', 'canceled'].includes(query.state.data.status) ? false : 1_000,
    })),
  })
  const cancel = useMutation({
    mutationFn: (taskId: string) => api.controlTask(taskId, 'cancel'),
    onSuccess: (task) => queryClient.setQueryData(['task', task.id], task),
  })
  const execute = useMutation({
    mutationFn: (value: string) => api.createTask(projectId, {
      title: `Terminal · ${value.slice(0, 80)}`,
      goal: `Execute this exact workspace command through the controlled command tool and report stdout, stderr, exit code, and changed files:\n\n${value}`,
      acceptance: 'The command is policy-checked, approved when required, executed at most once, and its result is auditable.',
      mode: 'single_agent',
      visibility: 'private',
      permission_mode: 'default',
      scene: 'code',
    }),
    onSuccess: async (task, value) => {
      setHistory((current) => [...current, { command: value, task }])
      setCommand('')
      await queryClient.invalidateQueries({ queryKey: ['tasks', projectId] })
    },
  })

  return <main className="terminal-page">
    <header className="management-heading">
      <div><span className="eyebrow">Project / Controlled terminal</span><h1>受控终端</h1><p>{project.data?.name || '项目'} · 命令通过 Tool Policy、审批、前置状态与副作用收据执行。</p></div>
      <Link className="secondary-button compact" to="/projects/$projectId" params={{ projectId }}>返回会话</Link>
    </header>
    <div className="terminal-safety"><ShieldCheck size={17}/><span><strong>不是裸 Shell</strong><small>提交后会创建可恢复任务；静态黑名单、工作区边界和完全访问上限仍然生效。</small></span></div>
    <section className="terminal-surface" aria-label="终端会话">
      <div className="terminal-output">
        <div className="terminal-welcome"><SquareTerminal size={22}/><span>输入一条工作区命令。命令不会写入浏览器草稿或本地历史。</span></div>
        {history.map((entry, index) => {
          const task = taskQueries[index]?.data ?? entry.task
          const terminal = ['succeeded', 'failed', 'blocked', 'canceled'].includes(task.status)
          return <article key={entry.task.id}>
          <div><span>$</span><code>{entry.command}</code></div>
          <p>受控任务 · {task.status}{task.error_code ? ` · ${task.error_code}` : ''}</p>
          {task.result_summary ? <pre className="terminal-result">{task.result_summary}</pre> : null}
          <div className="terminal-entry-actions">
            <Link to="/projects/$projectId/threads/$threadId" params={{ projectId, threadId: task.conversation_id }} search={{ inspector: 'run', event: task.run_id }}>打开执行流与审批</Link>
            {!terminal ? <button type="button" disabled={cancel.isPending} onClick={() => cancel.mutate(task.id)}><Ban size={13}/>取消</button> : null}
            {terminal ? <button type="button" onClick={() => {
              window.sessionStorage.setItem(`oktapod.composer.prefill.${task.conversation_id}`, '请基于刚才的受控终端任务结果继续分析，并说明下一步建议。')
              void navigate({ to: '/projects/$projectId/threads/$threadId', params: { projectId, threadId: task.conversation_id } })
            }}><MessageSquarePlus size={13}/>加入下一次模型上下文</button> : null}
          </div>
        </article>
        })}
        {execute.error ? <div className="terminal-error"><AlertTriangle size={15}/>{execute.error.message}</div> : null}
      </div>
      <form className="terminal-command" onSubmit={(event) => { event.preventDefault(); const value = command.trim(); if (value) execute.mutate(value) }}>
        <span>$</span>
        <input aria-label="终端命令" autoComplete="off" spellCheck={false} value={command} onChange={(event) => setCommand(event.target.value)} placeholder="例如：pnpm test" />
        <button type="submit" disabled={!command.trim() || execute.isPending}>{execute.isPending ? '提交中…' : <><CornerDownLeft size={15}/>受控执行</>}</button>
      </form>
    </section>
  </main>
}
