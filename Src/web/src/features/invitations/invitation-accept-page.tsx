import { useMutation } from '@tanstack/react-query'
import { Link, useNavigate, useSearch } from '@tanstack/react-router'
import { CheckCircle2, CircleAlert, LoaderCircle } from 'lucide-react'
import { api } from '../../shared/api/client'

export function InvitationAcceptPage() {
  const { token } = useSearch({ from: '/invitations/accept' })
  const navigate = useNavigate()
  const accept = useMutation({
    mutationFn: () => api.acceptProjectInvitation(token),
    onSuccess: async (invitation) => {
      await navigate({ to: '/projects/$projectId', params: { projectId: invitation.project_id } })
    },
  })

  if (!token) {
    return <div className="invite-accept-page"><CircleAlert size={34} /><h1>邀请链接无效</h1><p>链接缺少一次性邀请令牌，请向项目 Owner 重新获取邀请。</p><Link className="primary-button" to="/">返回项目首页</Link></div>
  }
  return <div className="invite-accept-page">
    {accept.isPending ? <><LoaderCircle className="spin" size={34} /><h1>正在加入项目…</h1><p>正在验证一次性邀请并建立安全会话。</p></> : null}
    {accept.isSuccess ? <><CheckCircle2 size={34} /><h1>已加入项目</h1><p>安全会话已建立，正在打开项目工作空间。</p></> : null}
    {accept.error ? <><CircleAlert size={34} /><h1>邀请无法接受</h1><p>{accept.error.message}</p><button className="primary-button" type="button" onClick={() => accept.mutate()}>重试</button></> : null}
    {!accept.isPending && !accept.isSuccess && !accept.error ? <><h1>加入团队项目</h1><p>此链接是一次性凭证，仅用于将当前浏览器加入邀请对应的项目。</p><button className="primary-button" type="button" onClick={() => accept.mutate()}>接受邀请</button></> : null}
  </div>
}
