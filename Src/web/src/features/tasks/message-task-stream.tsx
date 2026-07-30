import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { AlertTriangle, Check, ChevronDown, ChevronRight, FileText, ListTree, LoaderCircle, Puzzle, Search, X } from 'lucide-react'
import type { Artifact, Task } from '../../shared/api/client'
import { api } from '../../shared/api/client'
import { buildTaskTimeline, taskFailureInfo, type TaskTimelineItem } from '../tasks/task-progress-model'
import { useRunEvents } from '../runs/use-run-events'

// StepLine 是消息流内的可折叠单行步骤，关闭时仅显示一行标题。
function StepLine({ step }: { step: TaskTimelineItem }) {
  const [expanded, setExpanded] = useState(false)
  const isRunning = step.state === 'running'
  const isFailed = step.state === 'failed' || step.state === 'blocked'
  return <div className={`message-execution-step ${step.state}`}>
    <div className="step-header" onClick={() => setExpanded((value) => !value)}>
      {isRunning ? <LoaderCircle className="spin" size={12} /> :
        step.state === 'succeeded' ? <Check size={12} /> :
          isFailed ? <AlertTriangle size={12} /> : <ChevronRight size={12} />}
      <strong>{step.label}</strong>
      <small>{step.state === 'running' ? '执行中' : step.state === 'succeeded' ? '已完成' : step.state}</small>
      {expanded ? <ChevronDown size={11} /> : <ChevronRight size={11} />}
    </div>
    {expanded && (step.tool || step.detail) ? <div className="step-content">
      {step.tool ? <>
        {step.tool.input ? <div className="step-tool-input"><small>输入</small><pre>{step.tool.input}</pre></div> : null}
        {step.tool.result ? <div className="step-tool-result"><small>结果</small><p>{step.tool.result}</p></div> : null}
        {step.tool.output ? <div className="step-command-output"><small>输出</small><pre>{step.tool.output}</pre></div> : null}
      </> : null}
      {!step.tool && step.detail ? <p>{step.detail}</p> : null}
    </div> : null}
  </div>
}

// MessageTaskStream 只保留历史执行证据。活动计划与人工动作统一位于 Composer 上方。
export function MessageTaskStream({ taskId, runId, active, onRequestFileReview }: {
  taskId: string
  runId: string
  active?: boolean
  onRequestFileReview: (artifacts: Artifact[], task: Task) => void
}) {
  const [detailsOpen, setDetailsOpen] = useState(false)
  const [searchOpen, setSearchOpen] = useState(false)
  const [query, setQuery] = useState('')
  const taskQuery = useQuery({ queryKey: ['task', taskId], queryFn: () => api.task(taskId), enabled: !!taskId, refetchInterval: active ? 1_000 : false })
  const eventsQuery = useRunEvents(runId, Boolean(active))
  const refetchEvents = eventsQuery.refetch
  const refetchTask = taskQuery.refetch
  useEffect(() => {
    // A task can finish before the active observer's two-second REST safety
    // poll. Load one authoritative terminal snapshot so the inline audit does
    // not collapse to only the final result.
    if (!active && runId) {
      void refetchEvents()
      void refetchTask()
    }
  }, [active, refetchEvents, refetchTask, runId])
  const artifactsQuery = useQuery({ queryKey: ['run-artifacts', runId], queryFn: () => api.runArtifacts(runId), refetchInterval: active ? 1000 : false, enabled: !!runId })
  if (!taskQuery.data) return null
  const liveEvents = eventsQuery.data.items
  const timeline = buildTaskTimeline(taskQuery.data, liveEvents)
  const failure = taskFailureInfo(taskQuery.data)
  const artifacts = artifactsQuery.data?.items ?? []
  const runningStep = timeline.find((step) => step.state === 'running')
  const taskActive = active && ['queued', 'running', 'waiting_approval', 'waiting_input', 'paused'].includes(taskQuery.data.status)
  const taskFailed = taskQuery.data.status === 'failed' || taskQuery.data.status === 'blocked'
  const skillNames = Array.from(new Set(liveEvents.flatMap((event) => {
    if (event.event_type !== 'skill.loaded' || !Array.isArray(event.payload.skill_names)) return []
    return event.payload.skill_names.filter((name): name is string => typeof name === 'string' && Boolean(name.trim()))
  })))
  const agentName = liveEvents.reduce((name, event) => {
    if (name || event.event_type !== 'run.created') return name
    return typeof event.payload.agent_name === 'string' && event.payload.agent_name.trim() ? event.payload.agent_name.trim() : ''
  }, '') || 'Oktapod'
  const normalizedQuery = query.trim().toLocaleLowerCase('zh-CN')
  const visibleTimeline = normalizedQuery
    ? timeline.filter((step) => [step.label, step.detail, step.tool?.input, step.tool?.result, step.tool?.output]
      .some((value) => value?.toLocaleLowerCase('zh-CN').includes(normalizedQuery)))
    : timeline
  const hasExecutionEvidence = taskActive || Boolean(failure) || artifacts.length > 0 || timeline.some((step) => Boolean(step.tool))
  if (!hasExecutionEvidence) return null

  return <>
    <div className="thread-task-agent"><span aria-hidden="true">{agentName.slice(0, 1).toUpperCase()}</span><strong>{agentName}</strong></div>
    <div className={`message-task-stream${taskActive ? ' active' : ''}`}>
      <div className="message-task-stream-head">
      <span className={`task-stream-status${taskActive ? ' running' : ''}${taskFailed ? ' failed' : ''}`}>
        {taskActive ? <LoaderCircle className="spin" size={12} /> : taskFailed ? <AlertTriangle size={12} /> : <Check size={12} />}
        {taskActive ? (runningStep?.label ?? '任务执行中') : failure ? failure.title : '任务已完成'}
      </span>
      <span className={`task-skill-summary${skillNames.length ? ' loaded' : ''}`} title={skillNames.length ? `已加载：${skillNames.join('、')}` : '本次 Run 未加载 Skill'}>
        <Puzzle size={11} />{skillNames.length ? skillNames.join('、') : '未加载 Skill'}
      </span>
      {artifacts.length > 0 ? <small>{artifacts.length} 个文件</small> : null}
        <span className="message-task-stream-actions">
          <button type="button" className={searchOpen ? 'active' : ''} aria-label="搜索执行记录" title="搜索执行记录" onClick={() => { setSearchOpen((value) => !value); setDetailsOpen(true) }}><Search size={13} /></button>
          <button type="button" className={detailsOpen ? 'active' : ''} aria-label={detailsOpen ? '收起执行记录' : '展开执行记录'} title={detailsOpen ? '收起执行记录' : '展开执行记录'} onClick={() => setDetailsOpen((value) => !value)}><ListTree size={14} /></button>
        </span>
      </div>
      {searchOpen ? <label className="message-task-search"><Search size={12} /><input autoFocus value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索步骤、输入或输出" /><button type="button" aria-label="关闭搜索" onClick={() => { setQuery(''); setSearchOpen(false) }}><X size={12} /></button></label> : null}
      {failure ? <div className="message-task-failure" role="alert">
        <AlertTriangle size={14} />
        <div><p>{failure.detail}</p><small>{failure.action}</small></div>
      </div> : null}
      {detailsOpen ? <div className="message-task-detail">
        {visibleTimeline.length > 0 ? <div className="message-execution-steps">
          {visibleTimeline.map((step) => <StepLine key={step.id} step={step} />)}
        </div> : <p className="message-task-search-empty">{normalizedQuery ? '没有匹配的执行记录' : '暂无执行记录'}</p>}
      </div> : null}
      {artifacts.length > 0 ? <div className="message-artifacts">
        <button type="button" className="message-artifacts-trigger" onClick={() => onRequestFileReview(artifacts, taskQuery.data!)}>
          <FileText size={14} /><span>{artifacts.length} 个文件</span><small>在右侧面板打开</small>
        </button>
      </div> : null}
    </div>
  </>
}
