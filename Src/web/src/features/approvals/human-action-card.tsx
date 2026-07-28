import { useState } from 'react'
import * as Dialog from '@radix-ui/react-dialog'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Check, Clock3, LockKeyhole, ShieldAlert, X } from 'lucide-react'
import { api, type ApprovalScope, type HumanAction } from '../../shared/api/client'

export function HumanActionCard({ action }: { action: HumanAction }) {
  const queryClient = useQueryClient()
  const [answer, setAnswer] = useState('')
  const [projectConfirmOpen, setProjectConfirmOpen] = useState(false)
  const decision = useMutation({
    mutationFn: (input: {
      decision: 'approved' | 'rejected' | 'answered' | 'confirm_applied' | 'confirm_not_applied'
      scope?: ApprovalScope
      answer?: unknown
      confirmed?: boolean
    }) => api.decideHumanAction(action.id, { ...input, version: action.version }),
    onSuccess: async () => {
      setProjectConfirmOpen(false)
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['human-actions'] }),
        queryClient.invalidateQueries({ queryKey: ['run-plan', action.run_id] }),
        queryClient.invalidateQueries({ queryKey: ['run-events', action.run_id] }),
        queryClient.invalidateQueries({ queryKey: ['tasks', action.project_id] }),
        queryClient.invalidateQueries({ queryKey: ['conversation-messages', action.conversation_id] }),
      ])
    },
  })

  const waiting = action.status === 'waiting_user'
  const frozen = action.request.frozen_params ?? action.request.params ?? action.request
  const impact = String(action.request.impact_scope ?? action.request.scope ?? '仅当前计划项')
  const toolName = String(action.request.tool_name ?? '')
  const approval = action.kind === 'tool_approval'
  const input = action.kind === 'input_request'
  const permission = action.kind === 'permission_escalation'
  const budget = action.kind === 'budget_request'
  const sideEffect = action.kind === 'side_effect_resolution'

  const approve = (scope: ApprovalScope) => decision.mutate({ decision: 'approved', scope })

  return <article className="human-action-card" tabIndex={-1} aria-label={actionTitle(action)}>
    <header>
      <span className={`human-action-risk ${action.risk_level.toLowerCase()}`}><ShieldAlert size={13} />{riskLabel(action.risk_level)}</span>
      <span><strong>{actionTitle(action)}</strong><small><Clock3 size={12} />有效至 {formatTime(action.expires_at)}</small></span>
      <em>{statusLabel(action.status)}</em>
    </header>
    <p>{action.reason}</p>
    {!input ? <div className="human-action-frozen"><LockKeyhole size={14} /><span><small>{approval ? '冻结参数' : '请求内容'}</small><code>{formatRequest(toolName, frozen)}</code></span></div> : null}
    <dl><div><dt>影响范围</dt><dd>{impact}</dd></div><div><dt>请求哈希</dt><dd><code>{action.request_hash.slice(0, 16)}</code></dd></div></dl>

    {waiting && input ? <div className="human-action-answer">
      <label htmlFor={`human-action-answer-${action.id}`}>补充信息</label>
      <textarea id={`human-action-answer-${action.id}`} value={answer} onChange={(event) => setAnswer(event.target.value)} rows={3} autoFocus />
      <button className="approve" disabled={decision.isPending || !answer.trim()} onClick={() => decision.mutate({ decision: 'answered', answer: answer.trim() })}>提交并继续</button>
    </div> : null}

    {waiting && (approval || permission || budget) ? <div className="human-action-actions">
      <button className="reject" disabled={decision.isPending} onClick={() => decision.mutate({ decision: 'rejected', scope: 'once' })}><X size={14} />拒绝</button>
      <button className="approve" disabled={decision.isPending} onClick={() => approve('once')}><Check size={14} />{budget ? '批准扩展并继续' : '批准一次'}</button>
      {approval ? <button disabled={decision.isPending} onClick={() => approve('conversation')}><Check size={14} />本会话允许</button> : null}
      {approval ? <button disabled={decision.isPending} onClick={() => setProjectConfirmOpen(true)}><Check size={14} />本项目允许 30 天</button> : null}
    </div> : null}

    {waiting && sideEffect ? <div className="human-action-actions">
      <button className="reject" disabled={decision.isPending} onClick={() => decision.mutate({ decision: 'confirm_not_applied' })}>确认未生效</button>
      <button className="approve" disabled={decision.isPending} onClick={() => decision.mutate({ decision: 'confirm_applied' })}>确认已生效</button>
    </div> : null}

    {decision.error ? <small className="human-action-error" role="alert">{decision.error.message}</small> : null}

    <Dialog.Root open={projectConfirmOpen} onOpenChange={setProjectConfirmOpen}>
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <Dialog.Content className="dialog-content human-action-dialog" aria-describedby={`project-rule-description-${action.id}`}>
          <Dialog.Title>允许当前项目使用同类动作？</Dialog.Title>
          <Dialog.Description id={`project-rule-description-${action.id}`}>授权有效期为 30 天，只匹配当前工具、精确参数形状和风险等级。黑名单、破坏性 Git 与开放世界 MCP 不会被放行。</Dialog.Description>
          <div className="dialog-actions"><Dialog.Close asChild><button type="button">取消</button></Dialog.Close><button className="primary" type="button" disabled={decision.isPending} onClick={() => decision.mutate({ decision: 'approved', scope: 'project', confirmed: true })}>确认授权</button></div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  </article>
}

function formatRequest(toolName: string, value: unknown) {
  if (toolName === 'workspace_run_command' && value && typeof value === 'object') {
    const params = value as Record<string, unknown>
    const program = typeof params.program === 'string' ? params.program : ''
    const args = Array.isArray(params.args) ? params.args.map(String) : []
    return [program, ...args].filter(Boolean).map((part) => JSON.stringify(part)).join(' ')
  }
  return typeof value === 'string' ? value : JSON.stringify(value, null, 2)
}

function actionTitle(action: HumanAction) {
  const tool = String(action.request.tool_name ?? action.title)
  if (tool === 'workspace_run_command') return '运行工作区命令'
  if (tool === 'workspace_write_file') return '写入工作区文件'
  if (action.kind === 'input_request') return action.title || '需要补充信息'
  if (action.kind === 'permission_escalation') return action.title || '申请运行权限'
  if (action.kind === 'budget_request') return action.title || '确认扩展本次运行预算'
  if (action.kind === 'side_effect_resolution') return action.title || '确认动作是否生效'
  return action.title
}

function riskLabel(value: string) {
  return value ? `${value.toUpperCase()} 风险` : '需确认'
}

function statusLabel(value: HumanAction['status']) {
  if (value === 'reviewing') return '自动审查中'
  if (value === 'waiting_user') return '等待你处理'
  if (value === 'approved') return '已批准'
  if (value === 'rejected') return '已拒绝'
  if (value === 'answered') return '已回答'
  if (value === 'expired') return '已过期'
  return '已失效'
}

function formatTime(value: string) {
  const time = new Date(value)
  return Number.isNaN(time.getTime()) ? '未设置' : time.toLocaleString('zh-CN')
}
