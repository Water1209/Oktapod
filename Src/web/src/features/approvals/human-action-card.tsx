import { useState } from 'react'
import * as Dialog from '@radix-ui/react-dialog'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Check, Clock3, Gauge, Infinity as InfinityIcon, LockKeyhole, ShieldAlert, Square, X } from 'lucide-react'
import { api, type ApprovalScope, type HumanAction } from '../../shared/api/client'
import { mcpToolDisplayLabel } from '../../shared/mcp-display'

export function HumanActionCard({ action, onDecided, onBusy, onError, disabled = false }: { action: HumanAction; onDecided?: (result: HumanAction) => void; onBusy?: (busy: boolean) => void; onError?: (message: string) => void; disabled?: boolean }) {
  const queryClient = useQueryClient()
  const [structuredAnswers, setStructuredAnswers] = useState<Record<string, string | string[]>>({})
  const [projectConfirmOpen, setProjectConfirmOpen] = useState(false)
  const decision = useMutation({
    mutationFn: (input: {
      decision: 'approved' | 'rejected' | 'answered' | 'confirm_applied' | 'confirm_not_applied'
      scope?: ApprovalScope
      answer?: unknown
      confirmed?: boolean
    }) => api.decideHumanAction(action.id, { ...input, version: action.version }),
    onSuccess: async (result) => {
      setProjectConfirmOpen(false)
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['human-actions'] }),
        queryClient.invalidateQueries({ queryKey: ['run-plan', action.run_id] }),
        queryClient.invalidateQueries({ queryKey: ['run-events', action.run_id] }),
        queryClient.invalidateQueries({ queryKey: ['tasks', action.project_id] }),
        queryClient.invalidateQueries({ queryKey: ['conversation-messages', action.conversation_id] }),
      ])
      onBusy?.(false)
      onDecided?.(result)
    },
    onMutate: () => onBusy?.(true),
    onError: (error) => { onBusy?.(false); onError?.(error instanceof Error ? error.message : '审批未完成，请重试') },
  })
  const actionPending = decision.isPending || disabled

  const waiting = action.status === 'waiting_user'
  const frozen = action.request.frozen_params ?? action.request.params ?? action.request
  const impact = String(action.request.impact_scope ?? action.request.scope ?? '仅当前计划项')
  const toolName = String(action.request.tool_name ?? '')
  const approval = action.kind === 'tool_approval'
  const mcpApproval = approval && toolName.startsWith('mcp__')
  const privateWebFetch = approval && toolName === 'web_fetch' && action.request.approval_scope === 'run_origin'
  const persistentApprovalSupported = approval && !mcpApproval && !privateWebFetch
  const input = action.kind === 'input_request'
  const permission = action.kind === 'permission_escalation'
  const budget = action.kind === 'budget_request'
  const sideEffect = action.kind === 'side_effect_resolution'
  const questions = inputQuestions(action.request)
  const currentCalls = numericRequest(action.request.current_max_model_calls)
  const currentTokens = numericRequest(action.request.current_max_tokens)
  const recommendedCalls = numericRequest(action.request.recommended_max_model_calls ?? action.request.proposed_max_model_calls)
  const recommendedTokens = numericRequest(action.request.recommended_max_tokens ?? action.request.proposed_max_tokens)
  const addCalls = numericRequest(action.request.recommended_add_model_calls) || Math.max(0, recommendedCalls - currentCalls)
  const addTokens = numericRequest(action.request.recommended_add_tokens) || Math.max(0, recommendedTokens - currentTokens)
  const finishCalls = numericRequest(action.request.finish_add_model_calls) || 2
  const finishTokens = numericRequest(action.request.finish_add_tokens) || 131072

  const approve = (scope: ApprovalScope) => decision.mutate({ decision: 'approved', scope })

  return <article className="human-action-card" tabIndex={-1} aria-label={actionTitle(action)}>
    <header>
      <span className={`human-action-risk ${action.risk_level.toLowerCase()}`}><ShieldAlert size={13} />{riskLabel(action.risk_level)}</span>
      <span><strong>{actionTitle(action)}</strong><small><Clock3 size={12} />有效至 {formatTime(action.expires_at)}</small></span>
      <em>{statusLabel(action.status)}</em>
    </header>
    <p>{action.reason}</p>
    {waiting && mcpApproval ? <small className="human-action-error">这是已安装 MCP Server 的真实工具调用。可只批准当前参数一次，也可仅在本任务内允许后续调用同一个工具；不会授权整个 MCP、其他任务或项目。</small> : null}
    {privateWebFetch ? <div className="private-origin-approval"><ShieldAlert size={16} /><span><strong>受控私网读取</strong><small>只读 GET · 当前 Run · 最长 24 小时</small><code>{String(action.request.target_origin ?? '未知 origin')}</code>{action.request.matched_rule_id ? <small>匹配规则：{String(action.request.matched_rule_id)}</small> : null}<p>批准后，模型可读取此 origin 的其他路径，并可能把页面数据带入模型上下文；不会授权其他端口、协议或 Run。</p></span></div> : null}
	{!input && !budget ? <div className="human-action-frozen"><LockKeyhole size={14} /><span><small>{approval ? '冻结参数' : '请求内容'}</small><code>{formatRequest(toolName, frozen)}</code></span></div> : null}
    {budget ? <div className="human-action-budget-summary">
      <div><Gauge size={15} /><span><small>当前上限</small><strong>{formatCount(currentCalls)} 次 · {formatTokens(currentTokens)}</strong></span></div>
      <div><span aria-hidden="true">→</span><span><small>建议扩展后</small><strong>{formatCount(recommendedCalls)} 次 · {formatTokens(recommendedTokens)}</strong></span></div>
      <p>本次建议增加 <strong>{formatCount(addCalls)} 次模型调用</strong>和 <strong>{formatTokens(addTokens)}</strong>；只影响当前 Run。</p>
    </div> : null}
    <dl><div><dt>影响范围</dt><dd>{impact}</dd></div><div><dt>请求哈希</dt><dd><code>{action.request_hash.slice(0, 16)}</code></dd></div></dl>

    {waiting && input ? <InputRequestForm
      action={action}
      questions={questions}
      structuredAnswers={structuredAnswers}
      setStructuredAnswers={setStructuredAnswers}
      pending={actionPending}
      onSubmit={(value) => decision.mutate({ decision: 'answered', answer: value })}
    /> : null}

    {waiting && (approval || permission) ? <div className="human-action-actions">
      <button className="reject" disabled={actionPending} onClick={() => decision.mutate({ decision: 'rejected', scope: 'once' })}><X size={14} />拒绝</button>
      {privateWebFetch ? <button className="approve approve-origin" disabled={actionPending} onClick={() => approve('run_origin')}><Check size={14} />允许本任务访问此站点</button> : <button className="approve" disabled={actionPending} title="只授权当前冻结参数的一次调用" onClick={() => approve('once')}><Check size={14} />仅这一次</button>}
      {mcpApproval ? <button className="approve approve-session" disabled={actionPending} onClick={() => approve('run_tool')}><Check size={14} />本任务允许此工具</button> : null}
      {persistentApprovalSupported ? <button disabled={actionPending} title="本会话内匹配当前工具、精确参数和风险等级，不会放行其他命令" onClick={() => approve('conversation')}><Check size={14} />本会话同类动作</button> : null}
      {persistentApprovalSupported ? <button disabled={actionPending} title="项目内后续会话可复用相同工具、精确参数和风险等级的规则，有效 30 天" onClick={() => setProjectConfirmOpen(true)}><Check size={14} />本项目 30 天</button> : null}
    </div> : null}

    {waiting && budget ? <div className="human-action-actions budget-actions">
      <button className="reject" disabled={actionPending} title="终止当前 Run，保留已有文件和执行记录" onClick={() => decision.mutate({ decision: 'rejected', answer: 'stop' })}><Square size={12} />停止任务</button>
      <button disabled={actionPending} title={`仅增加 ${formatCount(finishCalls)} 次和 ${formatTokens(finishTokens)}，要求模型立即收尾`} onClick={() => decision.mutate({ decision: 'approved', answer: 'finish_existing' })}><Check size={14} />用现有结果收尾</button>
      <button className="approve" disabled={actionPending} onClick={() => decision.mutate({ decision: 'approved', answer: 'recommended' })}><Check size={14} />扩展 +{formatCount(addCalls)} 次 / +{formatTokens(addTokens)}</button>
      <button disabled={actionPending} title="仅当前 Run 取消模型调用和 Token 上限；风险动作仍受审批策略约束" onClick={() => decision.mutate({ decision: 'approved', answer: 'unlimited' })}><InfinityIcon size={14} />本次不限额</button>
    </div> : null}

    {waiting && sideEffect ? <div className="human-action-actions">
      <button className="reject" disabled={actionPending} onClick={() => decision.mutate({ decision: 'confirm_not_applied' })}>确认未生效</button>
      <button className="approve" disabled={actionPending} onClick={() => decision.mutate({ decision: 'confirm_applied' })}>确认已生效</button>
    </div> : null}

    {decision.error ? <small className="human-action-error" role="alert">{decision.error.message}</small> : null}

    <Dialog.Root open={projectConfirmOpen} onOpenChange={setProjectConfirmOpen}>
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <Dialog.Content className="dialog-content human-action-dialog" aria-describedby={`project-rule-description-${action.id}`}>
          <Dialog.Title>允许当前项目使用同类动作？</Dialog.Title>
          <Dialog.Description id={`project-rule-description-${action.id}`}>授权有效期为 30 天，只匹配当前工具、精确参数形状和风险等级。黑名单、破坏性 Git 与开放世界 MCP 不会被放行。</Dialog.Description>
          <div className="dialog-actions"><Dialog.Close asChild><button type="button">取消</button></Dialog.Close><button className="primary" type="button" disabled={actionPending} onClick={() => decision.mutate({ decision: 'approved', scope: 'project', confirmed: true })}>确认授权</button></div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  </article>
}

type InputOption = { id: string; label: string; description?: string; recommended?: boolean }
type InputQuestion = { id: string; header?: string; question: string; selection_mode?: 'single' | 'multiple'; options?: InputOption[]; allow_freeform?: boolean; required?: boolean }

function inputQuestions(request: Record<string, unknown>): InputQuestion[] {
  if (Array.isArray(request.questions)) return request.questions.filter((item): item is InputQuestion => Boolean(item && typeof item === 'object' && typeof (item as Record<string, unknown>).id === 'string' && typeof (item as Record<string, unknown>).question === 'string'))
  const prompt = typeof request.prompt === 'string' ? request.prompt : '请补充信息'
  const legacy = Array.isArray(request.choices) ? request.choices : []
  const options = legacy.map((item, index) => typeof item === 'string' ? ({ id: `option-${index + 1}`, label: item }) : item as InputOption)
  return [{ id: 'answer', header: options.length ? '选择' : '回答', question: prompt, selection_mode: options.length ? 'single' : undefined, options, allow_freeform: true, required: true }]
}

function InputRequestForm({ action, questions, structuredAnswers, setStructuredAnswers, pending, onSubmit }: {
  action: HumanAction
  questions: InputQuestion[]
  structuredAnswers: Record<string, string | string[]>
  setStructuredAnswers: (value: Record<string, string | string[]>) => void
  pending: boolean
  onSubmit: (value: unknown) => void
}) {
  const [freeform, setFreeform] = useState<Record<string, string>>({})
  const selectedValues = (id: string) => {
    const value = structuredAnswers[id]
    return Array.isArray(value) ? value : value ? [value] : []
  }
  const ready = questions.every((question) => !question.required || selectedValues(question.id).length > 0 || Boolean(freeform[question.id]?.trim()))
  const submission = () => Object.fromEntries(questions.map((question) => [question.id, { selected: selectedValues(question.id), text: freeform[question.id]?.trim() || '' }]))
  const update = (id: string, value: string | string[]) => setStructuredAnswers({ ...structuredAnswers, [id]: value })
  return <div className="human-action-answer">
    {questions.map((question) => <fieldset className="human-input-question" key={question.id}>
      <legend>{question.header || '需要你的选择'}</legend>
      <p>{question.question}</p>
      {question.options?.map((option) => {
        const selected = structuredAnswers[question.id]
        const checked = Array.isArray(selected) ? selected.includes(option.id) : selected === option.id
        return <label className="human-input-option" key={option.id}>
          <input type={question.selection_mode === 'multiple' ? 'checkbox' : 'radio'} name={`${action.id}-${question.id}`} checked={checked} onChange={() => {
            if (question.selection_mode === 'multiple') {
              const values = Array.isArray(selected) ? selected : []
              update(question.id, checked ? values.filter((value) => value !== option.id) : [...values, option.id])
            } else update(question.id, option.id)
          }} />
          <span><strong>{option.label}</strong>{option.recommended ? <em>推荐</em> : null}{option.description ? <small>{option.description}</small> : null}</span>
        </label>
      })}
      {question.allow_freeform || !question.options?.length ? <textarea aria-label={question.header || question.id} value={freeform[question.id] || ''} onChange={(event) => setFreeform({ ...freeform, [question.id]: event.target.value })} rows={3} placeholder={question.options?.length ? '补充说明（选填）' : '请输入回答'} /> : null}
    </fieldset>)}
    <button className="approve" disabled={pending || !ready} onClick={() => onSubmit(submission())}>提交并继续</button>
  </div>
}

function formatRequest(toolName: string, value: unknown) {
  if (toolName === 'web_fetch' && value && typeof value === 'object') {
    return String((value as Record<string, unknown>).url ?? 'invalid url')
  }
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
  if (tool === 'web_fetch') return '读取网页'
  if (action.kind === 'input_request') return action.title || '需要补充信息'
  if (action.kind === 'permission_escalation') return action.title || '申请运行权限'
  if (action.kind === 'budget_request') return action.title || '确认扩展本次运行预算'
  if (action.kind === 'side_effect_resolution') return action.title || '确认动作是否生效'
  if (tool.startsWith('mcp__')) return mcpToolDisplayLabel(tool, String(action.request.tool_display_name ?? action.title ?? ''))
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

function numericRequest(value: unknown) {
  const number = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(number) && number >= 0 ? Math.round(number) : 0
}

function formatCount(value: number) {
  return value.toLocaleString('zh-CN')
}

function formatTokens(value: number) {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(2)}M Token`
  if (value >= 1_000) return `${Math.round(value / 1_000)}K Token`
  return `${value.toLocaleString('zh-CN')} Token`
}
