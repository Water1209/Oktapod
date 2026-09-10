import { expect, test } from '@playwright/test'
import { writeFileSync } from 'node:fs'
import { resolve } from 'node:path'

const restartCheck = process.env.OKTAPOD_E2E_RESTART_CHECK === '1'
const statePath = process.env.OKTAPOD_E2E_STATE_PATH
const dataRoot = process.env.OKTAPOD_E2E_DATA_ROOT
if (restartCheck && statePath) test.use({ storageState: statePath })

test('Personal 用户能创建项目并看到缺少凭证的可恢复阻塞证据', async ({ page }) => {
  test.skip(restartCheck)
  test.setTimeout(60_000)
  const suffix = Date.now().toString(36)
  const username = `foundation.${suffix}`
  await page.goto('/')

  await expect(page.getByRole('heading', { name: '登录 Oktapod' })).toBeVisible()
  await page.getByRole('tab', { name: '注册' }).click()
  await page.getByRole('textbox', { name: '用户名' }).fill(username)
  await page.getByRole('textbox', { name: '显示名称' }).fill('Foundation Tester')
  await page.getByLabel('密码', { exact: true }).fill(`foundation-${suffix}-password`)
  await page.getByRole('button', { name: '创建并登录' }).click()

  await expect(page.getByRole('heading', { name: '工作台' })).toBeVisible()

  await page.goto('/settings/vault')
  await expect(page.getByRole('heading', { name: '凭据与密钥库' })).toBeVisible()
  await page.getByRole('button', { name: '生成恢复口令' }).click()
  const recoveryCode = (await page.locator('.recovery-code code').textContent())?.trim()
  expect(recoveryCode).toBeTruthy()
  await page.getByLabel('重新输入恢复口令').fill(recoveryCode!)
  await page.getByRole('button', { name: '确认并启用 Vault' }).click()
  await expect(page.locator('.vault-seal').getByRole('heading', { name: '已解锁' })).toBeVisible()
  await page.goto('/diagnostics')
  await expect(page.getByText(/已自动解锁/)).toBeVisible()

  await page.goto('/')
  await page.getByRole('link', { name: '查看项目' }).click()
  await page.getByRole('button', { name: '新建项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`E2E 项目 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证 Personal 持久化任务闭环')
  await page.getByRole('button', { name: '建立工作空间' }).click()

  await expect(page).toHaveURL(/\/projects\/[0-9a-f-]+$/)
  const projectId = page.url().match(/\/projects\/([0-9a-f-]+)/)?.[1]
  expect(projectId).toBeTruthy()
  await page.getByRole('button', { name: '打开任务面板' }).click()
  for (const tab of ['文件', '变更', '用量', '项目', '追踪']) {
    await expect(page.getByRole('button', { name: tab, exact: true })).toBeVisible()
  }
  await page.getByRole('button', { name: '文件', exact: true }).click()
  for (const section of ['工作文件', '项目资料', '任务输出']) {
    await expect(page.getByRole('button', { name: section, exact: true })).toBeVisible()
  }
  await expect(page.getByText('这个目录暂无内容')).toBeVisible()
  if (!dataRoot) throw new Error('OKTAPOD_E2E_DATA_ROOT is required')
  const workspace = resolve(dataRoot, 'workspaces', projectId!)
  writeFileSync(resolve(workspace, 'preview.md'), '# 面板内 Markdown 预览\n\n保持当前任务上下文。\n', 'utf8')
  writeFileSync(resolve(workspace, 'preview.html'), '<!doctype html><html><head><title>Preview</title><style>body{font-family:sans-serif;padding:32px}</style></head><body><main><h1>安全 HTML 预览</h1><p>脚本保持停用。</p></main><script>document.body.dataset.scriptRan="true"</script></body></html>', 'utf8')
  writeFileSync(resolve(workspace, 'sample.py'), 'def answer():\n    return 42\n', 'utf8')
  writeFileSync(resolve(workspace, 'archive.zip'), 'unsupported fixture', 'utf8')
  await page.getByRole('button', { name: '刷新工作文件' }).click()
  await expect(page.getByRole('button', { name: 'preview.md', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '预览 preview.html', exact: true })).toBeVisible()

  await page.getByRole('button', { name: '最大化侧边面板' }).click()
  await expect(page.locator('.project-chat-workspace')).toHaveClass(/drawer-maximized/)
  const maximizedLayout = await page.locator('.project-chat-workspace').evaluate((workspace) => {
    const workspaceBounds = workspace.getBoundingClientRect()
    const conversation = workspace.querySelector('.conversation-main')!.getBoundingClientRect()
    const panel = workspace.querySelector('.project-management-panel')!.getBoundingClientRect()
    return {
      workspaceTop: workspaceBounds.top,
      workspaceRight: workspaceBounds.right,
      workspaceBottom: workspaceBounds.bottom,
      workspaceLeft: workspaceBounds.left,
      conversationWidth: conversation.width,
      panelTop: panel.top,
      panelRight: panel.right,
      panelBottom: panel.bottom,
      panelLeft: panel.left,
      overlap: conversation.right - panel.left,
    }
  })
  expect(Math.abs(maximizedLayout.workspaceTop - maximizedLayout.panelTop)).toBeLessThanOrEqual(1)
  expect(Math.abs(maximizedLayout.workspaceRight - maximizedLayout.panelRight)).toBeLessThanOrEqual(1)
  expect(Math.abs(maximizedLayout.workspaceBottom - maximizedLayout.panelBottom)).toBeLessThanOrEqual(1)
  expect(Math.abs(maximizedLayout.workspaceLeft - maximizedLayout.panelLeft)).toBeLessThanOrEqual(1)
  expect(maximizedLayout.overlap).toBeGreaterThanOrEqual(maximizedLayout.conversationWidth - 1)
  await page.getByRole('button', { name: '收起项目栏' }).click()
  const collapsedLayout = await page.locator('.codex-shell').evaluate((shell) => {
    const rail = shell.querySelector('.global-rail')!.getBoundingClientRect()
    const main = shell.querySelector('.codex-main')!.getBoundingClientRect()
    const panel = shell.querySelector('.project-management-panel')!.getBoundingClientRect()
    const shellBounds = shell.getBoundingClientRect()
    return {
      shellTop: shellBounds.top,
      shellRight: shellBounds.right,
      shellBottom: shellBounds.bottom,
      railRight: rail.right,
      mainTop: main.top,
      mainRight: main.right,
      mainBottom: main.bottom,
      mainLeft: main.left,
      panelTop: panel.top,
      panelRight: panel.right,
      panelBottom: panel.bottom,
      panelLeft: panel.left,
    }
  })
  expect(Math.abs(collapsedLayout.shellTop - collapsedLayout.mainTop)).toBeLessThanOrEqual(1)
  expect(Math.abs(collapsedLayout.shellRight - collapsedLayout.mainRight)).toBeLessThanOrEqual(1)
  expect(Math.abs(collapsedLayout.shellBottom - collapsedLayout.mainBottom)).toBeLessThanOrEqual(1)
  expect(Math.abs(collapsedLayout.railRight - collapsedLayout.mainLeft)).toBeLessThanOrEqual(1)
  expect(Math.abs(collapsedLayout.mainTop - collapsedLayout.panelTop)).toBeLessThanOrEqual(1)
  expect(Math.abs(collapsedLayout.mainRight - collapsedLayout.panelRight)).toBeLessThanOrEqual(1)
  expect(Math.abs(collapsedLayout.mainBottom - collapsedLayout.panelBottom)).toBeLessThanOrEqual(1)
  expect(Math.abs(collapsedLayout.mainLeft - collapsedLayout.panelLeft)).toBeLessThanOrEqual(1)
  await page.getByRole('button', { name: '展开项目栏' }).click()

  await page.getByRole('button', { name: 'preview.md', exact: true }).click()
  await expect(page.getByRole('heading', { name: '面板内 Markdown 预览' })).toBeVisible()
  await expect(page.getByText('保持当前任务上下文。')).toBeVisible()
  await page.getByRole('button', { name: '原文', exact: true }).click()
  await expect(page.getByRole('region', { name: 'Markdown 原文' })).toContainText('# 面板内 Markdown 预览')
  await page.getByRole('button', { name: '预览', exact: true }).click()
  await page.getByRole('button', { name: '返回文件' }).click()
  await page.getByRole('button', { name: '预览 preview.html', exact: true }).click()
  const htmlPreview = page.frameLocator('iframe[title="HTML 预览：preview.html"]')
  await expect(htmlPreview.getByRole('heading', { name: '安全 HTML 预览' })).toBeVisible()
  await expect(page.locator('iframe[title="HTML 预览：preview.html"]')).toHaveAttribute('sandbox', 'allow-scripts')
  await expect(htmlPreview.locator('body')).toHaveAttribute('data-script-ran', 'true')
  await page.getByRole('button', { name: '源码', exact: true }).click()
  await expect(page.getByRole('region', { name: 'HTML 源码' })).toContainText('<!doctype html>')
  await page.getByRole('button', { name: '返回文件' }).click()
  await page.getByRole('button', { name: 'sample.py', exact: true }).click()
  await expect(page.locator('.file-preview-code')).toContainText('return 42')
  await page.getByRole('button', { name: '返回文件' }).click()
  const popupPromise = page.waitForEvent('popup')
  await page.getByRole('button', { name: 'archive.zip', exact: true }).click()
  const unsupportedPage = await popupPromise
  await expect(unsupportedPage).toHaveURL(/workspace\/files\/content\?path=archive\.zip/)
  await unsupportedPage.close()

  await page.getByRole('button', { name: '还原侧边面板' }).click()
  await expect(page.locator('.project-chat-workspace')).not.toHaveClass(/drawer-maximized/)
  await expect(page.getByRole('button', { name: '能力', exact: true })).toHaveCount(0)
  await expect(page.getByRole('button', { name: '自动化', exact: true })).toHaveCount(0)
  await expect(page.getByRole('button', { name: '记忆', exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: '关闭侧边面板' }).click()
  await expect(page.getByRole('button', { name: '添加附件' })).toBeVisible()
  await expect(page.getByRole('button', { name: '安装能力附件' })).toHaveCount(0)
  await expect(page.getByRole('combobox', { name: '模型' })).toBeVisible()

  await page.getByRole('combobox').first().click()
  await page.getByRole('option', { name: 'Task', exact: true }).click()
  const taskTitle = '输出一条 E2E 验收结果'
  await page.getByRole('textbox', { name: '消息' }).fill(taskTitle)
  await page.getByRole('button', { name: '发送' }).click()
  const taskStream = page.locator('.message-task-stream')
  await expect(taskStream).toContainText('数据库中没有可用的模型配置', { timeout: 15_000 })
  await expect(taskStream).toContainText('当前工作空间尚未保存模型配置')
  if (statePath) await page.context().storageState({ path: statePath })
})

test('Vault 在服务重启后使用本地 KEK 自动解锁', async ({ page }) => {
  test.skip(!restartCheck)
  await page.goto('/settings/vault')
  await expect(page.getByRole('heading', { name: '凭据与密钥库' })).toBeVisible()
  await expect(page.locator('.vault-seal').getByRole('heading', { name: '已解锁' })).toBeVisible()
  await page.goto('/diagnostics')
  await expect(page.getByText(/已自动解锁/)).toBeVisible()
})
