import { useEffect } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Outlet } from '@tanstack/react-router'
import { LoginPage } from '../features/auth/login-page'
import { ApiProblem, api } from '../shared/api/client'
import { AppShell } from './shell'

export function AuthenticatedRoot() {
  const bootstrap = useQuery({ queryKey: ['bootstrap'], queryFn: api.bootstrap, retry: false })
  useEffect(() => {
    if (bootstrap.data) window.sessionStorage.setItem('oktapod.authenticated', '1')
  }, [bootstrap.data])

  if (bootstrap.isLoading) return <div className="app-loading"><div className="auth-mark">O</div><span>正在打开工作空间...</span></div>
  if (bootstrap.error instanceof ApiProblem && bootstrap.error.problem.status === 401) {
    return <LoginPage sessionExpired={window.sessionStorage.getItem('oktapod.authenticated') === '1'} onAuthenticated={async () => { window.location.replace('/') }} />
  }
  if (bootstrap.error || !bootstrap.data) {
    return <div className="app-loading error"><strong>无法连接 Oktapod</strong><span>{bootstrap.error?.message ?? '服务暂不可用'}</span><button type="button" onClick={() => void bootstrap.refetch()}>重试</button></div>
  }
  return <AppShell bootstrap={bootstrap.data}><Outlet /></AppShell>
}
