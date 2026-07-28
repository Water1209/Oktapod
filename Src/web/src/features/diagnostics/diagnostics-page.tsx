import { useQuery } from '@tanstack/react-query'
import { Activity, Database, RefreshCw, Radio, Server, ShieldCheck, Wrench } from 'lucide-react'
import { api } from '../../shared/api/client'

export function DiagnosticsPage() {
  const diagnostics = useQuery({ queryKey: ['diagnostics'], queryFn: api.diagnostics, refetchInterval: 5_000 })
  if (diagnostics.isLoading) return <div className="empty-state">正在读取诊断状态…</div>
  if (diagnostics.error || !diagnostics.data) return <div className="empty-state"><h2>诊断不可用</h2><p>{diagnostics.error?.message}</p><button onClick={() => void diagnostics.refetch()} type="button">重试</button></div>
  const item = diagnostics.data
  return <main className="management-page diagnostics-page">
    <header className="management-heading"><div><span className="eyebrow">System / Read only</span><h1>诊断</h1><p>只展示可安全共享的运行状态；不会返回凭据或原始 Provider Payload。</p></div><button className="secondary-button compact" type="button" onClick={() => void diagnostics.refetch()}><RefreshCw size={14} />刷新</button></header>
    <div className={`diagnostic-overview ${item.status}`}><Activity size={20} /><div><strong>{item.status === 'ok' ? '服务运行正常' : '服务存在降级项'}</strong><span>Oktapod {item.version} · {item.profile}</span></div><em>{item.status.toUpperCase()}</em></div>
    <div className="diagnostic-list">
      <DiagnosticRow icon={<Database />} title="数据库" status={item.database.status} detail={`${item.database.name} · 持久化连接`} />
      <DiagnosticRow icon={<Server />} title="Provider" status={item.providers.active > 0 ? 'ok' : 'attention'} detail={`${item.providers.active}/${item.providers.total} 启用 · ${item.providers.stale_catalog} 个目录过期`} />
      <DiagnosticRow icon={<Wrench />} title="Worker" status="ok" detail={`${item.worker.running_runs} 运行 · ${item.worker.queued_runs} 排队 · ${item.worker.active_leases} 活跃 lease`} />
      <DiagnosticRow icon={<ShieldCheck />} title="审批" status={item.worker.waiting_approvals > 0 ? 'attention' : 'ok'} detail={`${item.worker.waiting_approvals} 项等待处理`} />
      <DiagnosticRow icon={<Radio />} title="SSE" status="ok" detail={`${item.sse.active_connections} 个活动连接`} />
    </div>
    <section className="diagnostic-policy"><h2>安全与维护边界</h2><div><strong>日志</strong><span>{item.logs.detail}</span><em>{item.logs.redacted ? '脱敏策略已启用' : '需要检查'}</em></div><div><strong>备份与恢复</strong><span>{item.maintenance.detail}</span><em>独立高风险流程</em></div></section>
  </main>
}

function DiagnosticRow({ icon, title, status, detail }: { icon: React.ReactNode; title: string; status: string; detail: string }) {
  return <div><span className={`diagnostic-icon ${status}`}>{icon}</span><span><strong>{title}</strong><small>{detail}</small></span><em className={status}>{status === 'ok' ? '正常' : status === 'attention' ? '需关注' : '降级'}</em></div>
}
