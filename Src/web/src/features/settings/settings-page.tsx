import { useEffect, useState } from 'react'
import { Link, useRouterState } from '@tanstack/react-router'
import { Braces, Database, Keyboard, Palette, RefreshCcw, Shield, SlidersHorizontal, SquareTerminal } from 'lucide-react'

type Preferences = {
  theme: 'system' | 'light' | 'dark'
  density: 'comfortable' | 'compact'
  autoContinue: boolean
  retryLimit: number
  shell: string
  contextFiles: boolean
}

const defaults: Preferences = {
  theme: 'system',
  density: 'compact',
  autoContinue: false,
  retryLimit: 2,
  shell: 'system',
  contextFiles: true,
}

const sections = [
  ['appearance', '外观', Palette],
  ['context', '上下文', Braces],
  ['retry', '重试与续写', RefreshCcw],
  ['security', '安全', Shield],
  ['shell', 'Shell', SquareTerminal],
  ['shortcuts', '快捷键', Keyboard],
  ['data', '数据维护', Database],
] as const

export function SettingsPage() {
  const pathname = useRouterState({ select: (state) => state.location.pathname })
  const section = pathname.split('/').at(-1) || 'appearance'
  const [preferences, setPreferences] = useState<Preferences>(() => {
    try {
      return { ...defaults, ...JSON.parse(window.localStorage.getItem('oktapod.preferences.v1') || '{}') }
    } catch {
      return defaults
    }
  })
  useEffect(() => {
    window.localStorage.setItem('oktapod.preferences.v1', JSON.stringify(preferences))
    document.documentElement.dataset.theme = preferences.theme
    document.documentElement.dataset.density = preferences.density
  }, [preferences])
  const update = <K extends keyof Preferences>(key: K, value: Preferences[K]) => setPreferences((current) => ({ ...current, [key]: value }))

  return <main className="settings-page">
    <header className="management-heading"><div><span className="eyebrow">Settings</span><h1>设置</h1><p>每项配置都标明生效来源；浏览器偏好不会伪装成服务端策略。</p></div></header>
    <div className="settings-layout">
      <nav aria-label="设置分组">
        <Link to="/settings/providers"><SlidersHorizontal size={15}/>Provider</Link>
        {sections.map(([value, label, Icon]) => <Link key={value} className={section === value ? 'active' : ''} to="/settings/$section" params={{ section: value }}><Icon size={15}/>{label}</Link>)}
      </nav>
      <section className="settings-detail">
        {section === 'appearance' ? <SettingsGroup title="外观" description="立即作用于当前浏览器。">
          <SettingRow label="主题" source="浏览器本地"><select value={preferences.theme} onChange={(event) => update('theme', event.target.value as Preferences['theme'])}><option value="system">跟随系统</option><option value="light">浅色</option><option value="dark">深色</option></select></SettingRow>
          <SettingRow label="信息密度" source="浏览器本地"><select value={preferences.density} onChange={(event) => update('density', event.target.value as Preferences['density'])}><option value="compact">紧凑</option><option value="comfortable">舒适</option></select></SettingRow>
        </SettingsGroup> : null}
        {section === 'context' ? <SettingsGroup title="上下文" description="控制 Composer 默认行为；Run 启动后仍以冻结快照为准。">
          <SettingRow label="自动附带已选文件" source="浏览器默认"><input type="checkbox" checked={preferences.contextFiles} onChange={(event) => update('contextFiles', event.target.checked)}/></SettingRow>
          <SettingRow label="Compaction" source="服务端策略"><span>按实际 Context Tokens 自动触发</span></SettingRow>
        </SettingsGroup> : null}
        {section === 'retry' ? <SettingsGroup title="重试与续写" description="不会对未知副作用自动重试。">
          <SettingRow label="普通错误重试上限" source="浏览器建议"><input type="number" min={0} max={5} value={preferences.retryLimit} onChange={(event) => update('retryLimit', Number(event.target.value))}/></SettingRow>
          <SettingRow label="文本截断后自动续写" source="浏览器默认"><input type="checkbox" checked={preferences.autoContinue} onChange={(event) => update('autoContinue', event.target.checked)}/></SettingRow>
        </SettingsGroup> : null}
        {section === 'security' ? <SettingsGroup title="安全" description="静态路径、网络和命令黑名单不可由这里关闭。">
          <SettingRow label="默认权限" source="服务端 Policy"><span>最小权限 · 需要时审批</span></SettingRow>
          <SettingRow label="Full Access" source="发送框手动控制"><span>持续到手动切换，不绕过静态规则</span></SettingRow>
          <Link className="secondary-button compact" to="/approvals">管理审批与历史</Link>
        </SettingsGroup> : null}
        {section === 'shell' ? <SettingsGroup title="Shell" description="受控终端仍通过 Tool Policy 和审批。">
          <SettingRow label="首选 Shell" source="浏览器建议"><select value={preferences.shell} onChange={(event) => update('shell', event.target.value)}><option value="system">系统默认</option><option value="powershell">PowerShell</option><option value="bash">Bash</option></select></SettingRow>
        </SettingsGroup> : null}
        {section === 'shortcuts' ? <SettingsGroup title="快捷键" description="当前版本固定快捷键，后续可导出键位方案。"><ShortcutRows/></SettingsGroup> : null}
        {section === 'data' ? <SettingsGroup title="数据维护" description="备份、恢复与导入均是独立高风险流程。">
          <Link className="secondary-button" to="/import-export">导入与导出</Link>
          <Link className="secondary-button" to="/diagnostics">打开只读诊断</Link>
          <p className="settings-warning">恢复数据库必须使用运维 CLI 并在停机状态执行；网页不会把恢复按钮混入健康检查。</p>
        </SettingsGroup> : null}
      </section>
    </div>
  </main>
}

function SettingsGroup({ title, description, children }: { title: string; description: string; children: React.ReactNode }) {
  return <div className="settings-group"><header><h2>{title}</h2><p>{description}</p></header>{children}</div>
}

function SettingRow({ label, source, children }: { label: string; source: string; children: React.ReactNode }) {
  return <label className="setting-row"><span><strong>{label}</strong><small>来源：{source}</small></span><span>{children}</span></label>
}

function ShortcutRows() {
  return <dl className="shortcut-grid"><div><dt>发送 / Steering</dt><dd><kbd>Enter</kbd></dd></div><div><dt>换行</dt><dd><kbd>Shift</kbd> + <kbd>Enter</kbd></dd></div><div><dt>Follow-up</dt><dd><kbd>Alt</kbd> + <kbd>Enter</kbd></dd></div><div><dt>请求中止</dt><dd><kbd>Esc</kbd></dd></div></dl>
}
