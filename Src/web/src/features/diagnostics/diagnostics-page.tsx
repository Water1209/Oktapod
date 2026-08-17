import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Activity, Database, HardDrive, KeyRound, RefreshCw, Radio, Server, ShieldCheck, Wrench } from 'lucide-react'
import { api } from '../../shared/api/client'

export function DiagnosticsPage() {
  const diagnostics = useQuery({ queryKey: ['diagnostics'], queryFn: api.diagnostics, refetchInterval: 5_000 })
  if (diagnostics.isLoading) return <div className="empty-state">正在读取诊断状态…</div>
  if (diagnostics.error || !diagnostics.data) return <div className="empty-state"><h2>诊断不可用</h2><p>{diagnostics.error?.message}</p><button onClick={() => void diagnostics.refetch()} type="button">重试</button></div>
  const item = diagnostics.data
  return <main className="management-page diagnostics-page">
    <header className="management-heading"><div><span className="eyebrow">System / Read only</span><h1>诊断</h1><p>只展示可安全共享的运行状态；不会返回凭据或原始 Provider Payload。</p></div><button className="secondary-button compact" type="button" onClick={() => void diagnostics.refetch()}><RefreshCw size={14} />刷新</button></header>
    <div className={`diagnostic-overview ${item.status}`}><Activity size={20} /><div><strong>{item.status === 'ok' ? '服务运行正常' : item.status === 'attention' ? '服务可访问，但需要完成配置' : '服务存在降级项'}</strong><span>Oktapod {item.version} · {item.profile}</span></div><em>{item.status.toUpperCase()}</em></div>
    <div className="diagnostic-list">
      <DiagnosticRow icon={<Database />} title="数据库" status={item.database.status} detail={`${item.database.name} · 持久化连接`} />
      <DiagnosticRow icon={<Server />} title="Provider" status={item.providers.active > 0 ? 'ok' : 'attention'} detail={`${item.providers.active}/${item.providers.total} 启用 · ${item.providers.stale_catalog} 个目录过期`} action={<Link to="/settings/providers">管理</Link>} />
      <DiagnosticRow icon={<Database />} title="模型价格" status={item.pricing.uncovered_models > 0 ? 'attention' : 'ok'} detail={`${item.pricing.version_count} 个价格版本 · ${item.pricing.uncovered_models} 个已启用模型尚未覆盖`} action={<Link to="/settings/$section" params={{ section: 'pricing' }}>配置价格</Link>} />
      <DiagnosticRow icon={<KeyRound />} title="数据库密钥库" status={item.vault.state === 'active' ? 'ok' : item.vault.state === 'corrupt' ? 'degraded' : 'attention'} detail={`${vaultStateText[item.vault.state]} · Key v${item.vault.key_version || '—'} · ${item.vault.secret_count} 条 Secret 密文`} action={<Link to="/settings/$section" params={{ section: 'vault' }}>管理</Link>} />
      <DiagnosticRow icon={<KeyRound />} title="凭据状态" status={item.credential_states.corrupt > 0 ? 'degraded' : item.credential_states.locked + item.credential_states.missing > 0 ? 'attention' : 'ok'} detail={`${item.credential_states.ready} 可用 · ${item.credential_states.missing} 缺失 · ${item.credential_states.locked} 锁定 · ${item.credential_states.corrupt} 损坏`} />
      {item.credential_issues.length ? <div className="diagnostic-issue-drawer"><strong>需要处理的凭据</strong><div>{item.credential_issues.map((issue) => <a href={issue.action_path} key={`${issue.kind}:${issue.resource_id}`}><span><b>{issue.name}</b><small>{credentialKindText[issue.kind]} · {credentialStateText[issue.state]}</small></span><em>前往修复</em></a>)}</div></div> : null}
      <DiagnosticRow icon={<Wrench />} title="Worker" status="ok" detail={`${item.worker.running_runs} 运行 · ${item.worker.queued_runs} 排队 · ${item.worker.active_leases} 活跃 lease`} />
      <DiagnosticRow icon={<ShieldCheck />} title="审批" status={item.worker.waiting_approvals > 0 ? 'attention' : 'ok'} detail={`${item.worker.waiting_approvals} 项等待处理`} />
      <DiagnosticRow icon={<ShieldCheck />} title="自动审查" status={item.auto_review.available ? 'ok' : 'attention'} detail={item.auto_review.reason} action={<Link to="/settings/$section" params={{ section: 'governance' }}>治理设置</Link>} />
      {item.storage ? <DiagnosticRow icon={<HardDrive />} title="本机存储" status="ok" detail={`数据根目录：${item.storage.data_root} · Workspace：${item.storage.workspace_root}`} /> : null}
      <DiagnosticRow icon={<Radio />} title="SSE" status="ok" detail={`${item.sse.active_connections} 个活动连接`} />
    </div>
    <section className="diagnostic-policy"><h2>安全与维护边界</h2><div><strong>日志</strong><span>{item.logs.detail}</span><em>{item.logs.redacted ? '脱敏策略已启用' : '需要检查'}</em></div><div><strong>备份与恢复</strong><span>{item.maintenance.detail}</span><em>独立高风险流程</em></div></section>
  </main>
}

function DiagnosticRow({ icon, title, status, detail, action }: { icon: React.ReactNode; title: string; status: string; detail: string; action?: React.ReactNode }) {
  return <div><span className={`diagnostic-icon ${status}`}>{icon}</span><span><strong>{title}</strong><small>{detail}</small></span>{action}<em className={status}>{status === 'ok' ? '正常' : status === 'attention' ? '需关注' : '降级'}</em></div>
}

const vaultStateText = { uninitialized: '尚未初始化', pending: '等待确认恢复口令', active: '已自动解锁', locked: '缺少本地 KEK', corrupt: '元数据或密文损坏' } as const
const credentialKindText = { provider: 'Provider', mcp: 'MCP', mcp_oauth: 'MCP OAuth' } as const
const credentialStateText = { missing: '凭据缺失', locked: '密钥库锁定', corrupt: '密文损坏' } as const
