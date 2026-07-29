import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { AlertTriangle, Check, ChevronDown, ChevronRight, FileText, LoaderCircle, Puzzle } from 'lucide-react'
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
  const hasExecutionEvidence = taskActive || Boolean(failure) || artifacts.length > 0 || timeline.some((step) => Boolean(step.tool))
  if (!hasExecutionEvidence) return null

  return <div className={`message-task-stream${taskActive ? ' active' : ''}`}>
    <div className="message-task-stream-head">
      <span className={`task-stream-status${taskActive ? ' running' : ''}${taskFailed ? ' failed' : ''}`}>
        {taskActive ? <LoaderCircle className="spin" size={12} /> : taskFailed ? <AlertTriangle size={12} /> : <Check size={12} />}
        {taskActive ? (runningStep?.label ?? '任务执行中') : failure ? failure.title : '任务已完成'}
      </span>
      <span className={`task-skill-summary${skillNames.length ? ' loaded' : ''}`} title={skillNames.length ? `已加载：${skillNames.join('、')}` : '本次 Run 未加载 Skill'}>
        <Puzzle size={11} />{skillNames.length ? skillNames.join('、') : '未加载 Skill'}
      </span>
      {artifacts.length > 0 ? <small>{artifacts.length} 个文件</small> : null}
    </div>
    {failure ? <div className="message-task-failure" role="alert">
      <AlertTriangle size={14} />
      <div><p>{failure.detail}</p><small>{failure.action}</small></div>
    </div> : null}
    <details className="message-task-detail">
      <summary>执行记录</summary>
      {timeline.length > 0 ? <div className="message-execution-steps">
        {timeline.map((step) => <StepLine key={step.id} step={step} />)}
      </div> : null}
    </details>
    {artifacts.length > 0 ? <div className="message-artifacts">
      <button type="button" className="message-artifacts-trigger" onClick={() => onRequestFileReview(artifacts, taskQuery.data!)}>
        <FileText size={14} /><span>📄 {artifacts.length} 个文件</span><small>在右侧面板打开</small>
      </button>
    </div> : null}
  </div>
}
