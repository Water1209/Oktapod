import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { ArrowRight, Eye, EyeOff, LockKeyhole } from 'lucide-react'
import { api } from '../../shared/api/client'
import brandWordmark from '../../shared/brand-wordmark.png'

export function LoginPage({ onAuthenticated, sessionExpired = false }: { onAuthenticated: () => Promise<void>; sessionExpired?: boolean }) {
  const [mode, setMode] = useState<'login' | 'register'>('login')
  const [username, setUsername] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [password, setPassword] = useState('')
  const [showPassword, setShowPassword] = useState(false)

  const authenticate = useMutation({
    mutationFn: () => mode === 'login'
      ? api.login({ username, password })
      : api.register({ username, display_name: displayName, password }),
    onSuccess: onAuthenticated,
  })

  const switchMode = (next: 'login' | 'register') => {
    setMode(next)
    authenticate.reset()
  }

  return (
    <main className="auth-layout">
      <section className="auth-identity" aria-label="Oktapod">
        <img className="auth-wordmark" src={brandWordmark} alt="Oktapod" />
        <span>Agent workspace</span>
      </section>

      <section className="auth-surface">
        <div className="auth-form-wrap">
          <div className="auth-kicker"><LockKeyhole size={14} /> 本地工作空间</div>
          <h1>{mode === 'login' ? '登录 Oktapod' : '创建账户'}</h1>
          <p>{mode === 'login' ? '继续你的个人项目。' : '新账户默认只有自己的私有项目。'}</p>
          {sessionExpired && mode === 'login' ? <div className="auth-session-notice" role="status">会话已过期。重新登录后会返回刚才的页面。</div> : null}

          <div className="auth-tabs" role="tablist" aria-label="账户操作">
            <button type="button" role="tab" aria-selected={mode === 'login'} onClick={() => switchMode('login')}>登录</button>
            <button type="button" role="tab" aria-selected={mode === 'register'} onClick={() => switchMode('register')}>注册</button>
          </div>

          <form onSubmit={(event) => { event.preventDefault(); authenticate.mutate() }}>
            <label className="auth-field">
              <span>用户名</span>
              <input autoFocus autoComplete="username" required minLength={3} maxLength={64} value={username} onChange={(event) => setUsername(event.target.value)} placeholder="your.name" />
            </label>
            {mode === 'register' ? (
              <label className="auth-field">
                <span>显示名称</span>
                <input autoComplete="name" required maxLength={80} value={displayName} onChange={(event) => setDisplayName(event.target.value)} placeholder="你的名字" />
              </label>
            ) : null}
            <div className="auth-field">
              <label htmlFor="auth-password">密码</label>
              <span className="password-input">
                <input id="auth-password" type={showPassword ? 'text' : 'password'} autoComplete={mode === 'login' ? 'current-password' : 'new-password'} required minLength={8} maxLength={256} value={password} onChange={(event) => setPassword(event.target.value)} placeholder="至少 8 个字符" />
                <button type="button" title={showPassword ? '隐藏密码' : '显示密码'} aria-label={showPassword ? '隐藏密码' : '显示密码'} onClick={() => setShowPassword((value) => !value)}>
                  {showPassword ? <EyeOff size={17} /> : <Eye size={17} />}
                </button>
              </span>
            </div>

            {authenticate.error ? <div className="auth-error" role="alert">{authenticate.error.message}</div> : null}

            <button className="auth-submit" type="submit" disabled={authenticate.isPending || !username.trim() || !password || (mode === 'register' && !displayName.trim())}>
              <span>{authenticate.isPending ? '处理中...' : mode === 'login' ? '登录' : '创建并登录'}</span>
              <ArrowRight size={17} />
            </button>
          </form>
        </div>
      </section>
    </main>
  )
}
