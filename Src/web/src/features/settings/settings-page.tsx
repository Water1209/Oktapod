import { useEffect, useState } from 'react'
import { Link, Navigate, useParams } from '@tanstack/react-router'
import { Palette, SlidersHorizontal } from 'lucide-react'
import { applyPresentationPreferences, defaultPresentationPreferences, presentationPreferenceStorageKey, readPresentationPreferences, type DensityPreference, type ThemePreference } from '../../shared/presentation-preferences'

type Preferences = { theme: ThemePreference; density: DensityPreference }

export function SettingsPage() {
  const { section } = useParams({ from: '/settings/$section' })
  if (section !== 'appearance') return <Navigate to="/settings/$section" params={{ section: 'appearance' }} />
  return <AppearanceSettings />
}

function AppearanceSettings() {
  const [preferences, setPreferences] = useState<Preferences>(() => {
    try { return { ...defaultPresentationPreferences, ...readPresentationPreferences() } } catch { return defaultPresentationPreferences }
  })
  useEffect(() => {
    window.localStorage.setItem(presentationPreferenceStorageKey, JSON.stringify(preferences))
    applyPresentationPreferences(preferences)
  }, [preferences])
  return <SettingsLayout active="appearance">
    <div className="settings-group"><header><h2>外观</h2><p>这两项由浏览器保存，修改后立即作用于整个工作区。</p></header>
      <SettingRow label="主题" source="浏览器本地"><select aria-label="主题" value={preferences.theme} onChange={(event) => setPreferences((current) => ({ ...current, theme: event.target.value as ThemePreference }))}><option value="system">跟随系统</option><option value="light">浅色</option><option value="dark">深色</option></select></SettingRow>
      <SettingRow label="信息密度" source="浏览器本地"><select aria-label="信息密度" value={preferences.density} onChange={(event) => setPreferences((current) => ({ ...current, density: event.target.value as DensityPreference }))}><option value="compact">紧凑</option><option value="comfortable">舒适</option></select></SettingRow>
    </div>
  </SettingsLayout>
}

export function SettingsLayout({ active, children }: { active: 'providers' | 'appearance'; children: React.ReactNode }) {
  return <main className="settings-page">
    <header className="management-heading"><div><span className="eyebrow">设置</span><h1>设置</h1><p>这里只展示可以修改、并且会真实生效的配置。</p></div></header>
    <div className="settings-layout">
      <nav aria-label="设置分组">
        <Link className={active === 'providers' ? 'active' : ''} to="/settings/providers"><SlidersHorizontal size={15} />模型服务</Link>
        <Link className={active === 'appearance' ? 'active' : ''} to="/settings/$section" params={{ section: 'appearance' }}><Palette size={15} />外观</Link>
      </nav>
      <section className="settings-detail">{children}</section>
    </div>
  </main>
}

function SettingRow({ label, source, children }: { label: string; source: string; children: React.ReactNode }) {
  return <label className="setting-row"><span><strong>{label}</strong><small>来源：{source}</small></span><span>{children}</span></label>
}
