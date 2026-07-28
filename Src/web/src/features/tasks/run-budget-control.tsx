import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Gauge, RotateCw, ShieldCheck } from 'lucide-react'
import { api, type RunUsage, type TaskStatus } from '../../shared/api/client'
import { recommendedRunBudget } from './run-budget'

export function RunBudgetControl({ taskId, runId, taskStatus, usage, compact = false }: { taskId: string; runId: string; taskStatus: TaskStatus; usage: RunUsage; compact?: boolean }) {
  const queryClient = useQueryClient()
  const suggested = recommendedRunBudget(usage)
  const [unlimited, setUnlimited] = useState(usage.budget_max_calls === 0 && usage.budget_max_tokens === 0)
  const [maxCalls, setMaxCalls] = useState<number | null>(null)
  const [maxTokens, setMaxTokens] = useState<number | null>(null)
  const targetCalls = maxCalls ?? suggested.maxCalls
  const targetTokens = maxTokens ?? suggested.maxTokens
  const usedCalls = usage.model_calls + usage.reserved_calls
  const usedTokens = usage.input_tokens + usage.output_tokens + usage.reasoning_tokens + usage.reserved_tokens
  const callPercent = Math.min(100, Math.round(usedCalls / Math.max(1, usage.budget_max_calls) * 100))
  const tokenPercent = Math.min(100, Math.round(usedTokens / Math.max(1, usage.budget_max_tokens) * 100))
  const recoverable = taskStatus === 'blocked' || taskStatus === 'failed'
  const update = useMutation({
    mutationFn: async () => {
      const updated = await api.updateRunBudget(runId, { max_model_calls: unlimited ? 0 : targetCalls, max_tokens: unlimited ? 0 : targetTokens, approve: true })
      if (recoverable) {
        try {
          await api.controlTask(taskId, 'retry')
        } catch (error) {
          throw new Error(`配额已更新，但任务未能自动重试：${error instanceof Error ? error.message : '请打开任务手动重试'}`, { cause: error })
        }
      }
      return updated
    },
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['inbox'] }),
        queryClient.invalidateQueries({ queryKey: ['task', taskId] }),
        queryClient.invalidateQueries({ queryKey: ['run-usage', runId] }),
        queryClient.invalidateQueries({ queryKey: ['run-events', runId] }),
      ])
    },
  })

  return <section className={`run-budget-control ${compact ? 'compact' : ''}`} aria-label="调整本次运行配额">
    <header><span className="budget-control-icon"><Gauge size={16} /></span><span><strong>资源不足，执行已停在安全检查点</strong><small>这里只调整本次 Run，不会改变其他项目或后续任务的默认值。</small></span></header>
    <div className="budget-usage-grid">
      <BudgetUsage label="模型调用" value={`${usedCalls.toLocaleString()} / ${usage.budget_max_calls.toLocaleString()} 次`} percent={callPercent} />
      <BudgetUsage label="Token" value={`${usedTokens.toLocaleString()} / ${usage.budget_max_tokens.toLocaleString()}`} percent={tokenPercent} />
    </div>
    <form onSubmit={(event) => { event.preventDefault(); update.mutate() }}>
      <div className="budget-control-mode"><button type="button" className={unlimited ? 'active' : ''} aria-pressed={unlimited} onClick={() => setUnlimited(true)}>不限额</button><button type="button" className={!unlimited ? 'active' : ''} aria-pressed={!unlimited} onClick={() => setUnlimited(false)}>自定义上限</button></div>
      {!unlimited ? <><label><span>新的模型调用上限</span><input aria-label="新的模型调用上限" type="number" min={Math.max(1, usedCalls)} max={1_000} value={targetCalls} onChange={(event) => setMaxCalls(Number(event.target.value))} /></label>
      <label><span>新的 Token 上限</span><input aria-label="新的 Token 上限" type="number" min={Math.max(1, usedTokens)} max={10_000_000} step={1_000} value={targetTokens} onChange={(event) => setMaxTokens(Number(event.target.value))} /></label></> : <div className="budget-unlimited-note"><strong>解除本次 Run 的资源上限</strong><small>任务会从当前检查点继续，直到完成或遇到其他安全边界。</small></div>}
      <button className="budget-control-submit" type="submit" disabled={update.isPending || !unlimited && (targetCalls < usedCalls || targetTokens < usedTokens)}>{recoverable ? <RotateCw size={14} /> : <ShieldCheck size={14} />}{update.isPending ? '正在更新' : unlimited ? recoverable ? '设为不限额并重试' : '设为不限额' : recoverable ? '批准增加配额并重试' : '批准更新配额'}</button>
    </form>
    <p>{unlimited ? '不限额是默认方式；权限审批和安全规则仍然有效。' : '建议值已根据当前用量留出继续执行空间；你可以在批准前修改。'}</p>
    {update.error ? <small className="budget-control-error" role="alert">{update.error.message}</small> : null}
  </section>
}

function BudgetUsage({ label, value, percent }: { label: string; value: string; percent: number }) {
  return <div><span><small>{label}</small><strong>{value}</strong></span><i><b style={{ width: `${percent}%` }} /></i></div>
}
