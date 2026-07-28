import { expect, test, type Page } from '@playwright/test'

test.beforeEach(async ({ page }, testInfo) => {
  const suffix = `${Date.now().toString(36)}-${testInfo.testId.slice(-6)}`
  await page.goto('/')
  await expect(page.getByRole('heading', { name: '登录 Oktapod' })).toBeVisible()
  await page.getByRole('tab', { name: '注册' }).click()
  await page.getByRole('textbox', { name: '用户名' }).fill(`routes.${suffix}`)
  await page.getByRole('textbox', { name: '显示名称' }).fill('Route Tester')
  await page.getByLabel('密码', { exact: true }).fill(`routes-${suffix}-password`)
  await page.getByRole('button', { name: '创建并登录' }).click()
  await expect(page.getByText('模型服务已配置')).toBeVisible()
})

async function createProject(page: Page, name: string) {
  await page.getByRole('button', { name: '打开项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(name)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证 Codex 式工作台路由与响应式交互')
  await page.getByRole('button', { name: '建立工作空间' }).click()
  await expect(page).toHaveURL(/\/projects\/[0-9a-f-]+$/)
}

test('Provider 实例页不回填凭据并可显示连接测试结果', async ({ page }) => {
  await page.goto('/settings/providers')
  await expect(page.getByRole('heading', { name: '模型服务', exact: true })).toBeVisible()
  await page.getByRole('button', { name: '添加实例' }).click()
  await page.getByLabel('显示名称').fill('E2E Provider')
  await page.getByLabel('Endpoint').fill('http://127.0.0.1:19090/v1')
  await page.getByLabel('默认模型').fill('model-e2e')
  await page.getByLabel(/API Key/).fill('fake-e2e-key')
  await page.getByRole('button', { name: '保存实例' }).click()
  const providerList = page.getByRole('complementary', { name: 'Provider 实例' })
  await expect(providerList.getByRole('button', { name: /E2E Provider/ })).toBeVisible()
  await expect(page.getByText('已配置 · 留空保持不变')).toBeVisible()
  await expect(page.locator('input[type="password"]')).toHaveValue('')
  await page.getByRole('button', { name: '测试连接' }).click()
  await expect(page.locator('.test-result')).toContainText(/models|个模型|fresh|stale/, { timeout: 15_000 })
  await expect(page.locator('input[type="password"]')).toHaveValue('')
})

test('Session Tree 可预览、标记并从用户消息重新编辑', async ({ page }) => {
  const suffix = Date.now().toString(36)
  await createProject(page, `会话树 ${suffix}`)
  const content = `保留这条可分支消息 ${suffix}`
  await page.getByRole('textbox', { name: '消息' }).fill(content)
  await page.getByRole('button', { name: '发送' }).click()
  await expect(page.getByRole('main').getByText('E2E 模型执行成功。', { exact: true })).toBeVisible({ timeout: 15_000 })

  await page.getByRole('link', { name: 'Session Tree' }).click()
  await expect(page.getByRole('heading', { name: '会话树' })).toBeVisible()
  const userEntry = page.getByRole('region', { name: 'Session Tree' }).getByRole('button').filter({ hasText: content })
  await expect(userEntry).toBeVisible()
  await userEntry.click()
  await expect(page.getByRole('button', { name: '重新编辑' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Fork' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Clone 当前路径' })).toBeVisible()

  page.once('dialog', async (dialog) => dialog.accept(`关键节点 ${suffix}`))
  await page.getByRole('button', { name: 'Label' }).click()
  await expect(page.getByText(`关键节点 ${suffix}`, { exact: true }).first()).toBeVisible()

  page.once('dialog', async (dialog) => dialog.accept())
  await page.getByRole('button', { name: '重新编辑' }).click()
  await expect(page.getByRole('textbox', { name: '消息' })).toHaveValue(content)
})

test('首页与工作台在五档视口无横向溢出', async ({ page }) => {
  const viewports = [1440, 1280, 1024, 768, 390]
  for (const width of viewports) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
    await page.goto('/')
    await expect(page.getByRole('heading', { name: '工作台' })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), `home width ${width}`).toBe(true)
  }

  await page.setViewportSize({ width: 1440, height: 900 })
  await createProject(page, `响应式 ${Date.now().toString(36)}`)
  for (const width of viewports) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
    await expect(page.getByRole('textbox', { name: '消息' })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), `workbench width ${width}`).toBe(true)
  }
  await page.getByRole('button', { name: '打开检查器' }).click()
  await expect(page.locator('.project-management-panel')).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
})

test('项目不暴露伪终端入口，命令从对话进入受控工具链', async ({ page }) => {
  await createProject(page, `受控终端 ${Date.now().toString(36)}`)
  await expect(page.getByRole('link', { name: 'Terminal' })).toHaveCount(0)
  await expect(page.getByRole('textbox', { name: '消息' })).toBeVisible()
})

test('设置显示生效来源并能切换显式主题', async ({ page }) => {
  await page.goto('/settings/appearance')
  await expect(page.getByRole('heading', { name: '设置', exact: true })).toBeVisible()
  await expect(page.getByText('来源：浏览器本地').first()).toBeVisible()
  await page.getByRole('combobox').first().selectOption('dark')
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
  await page.getByRole('link', { name: '数据维护' }).click()
  await expect(page.getByRole('link', { name: '导入与导出' })).toBeVisible()
})

test('导入导出先预检并拒绝包含敏感字段的包', async ({ page }) => {
  await createProject(page, `迁移页面 ${Date.now().toString(36)}`)
  await page.goto('/import-export')
  await expect(page.getByRole('heading', { name: '导入与导出' })).toBeVisible()
  const download = page.waitForEvent('download')
  await page.getByRole('button', { name: '下载脱敏导出包' }).click()
  await expect((await download).suggestedFilename()).toMatch(/oktapod-export\.json$/)
  await page.getByRole('combobox', { name: '导出格式' }).selectOption('jsonl')
  const jsonlDownload = page.waitForEvent('download')
  await page.getByRole('button', { name: '下载脱敏导出包' }).click()
  await expect((await jsonlDownload).suggestedFilename()).toMatch(/oktapod-export\.jsonl$/)
  await page.getByRole('combobox', { name: '导出格式' }).selectOption('html')
  const htmlDownload = page.waitForEvent('download')
  await page.getByRole('button', { name: '下载脱敏导出包' }).click()
  await expect((await htmlDownload).suggestedFilename()).toMatch(/oktapod-export\.html$/)

  const fileInput = page.locator('.import-drop input[type="file"]')
  await fileInput.setInputFiles({
    name: 'unsafe.json',
    mimeType: 'application/json',
    buffer: Buffer.from(JSON.stringify({ project: { name: 'unsafe', scene: 'work' }, conversations: [], api_key: 'must-not-import' })),
  })
  await expect(page.getByText(/检测到敏感字段：api_key/)).toBeVisible()

  const conversationId = crypto.randomUUID()
  await fileInput.setInputFiles({
    name: 'safe.jsonl',
    mimeType: 'application/x-ndjson',
    buffer: Buffer.from([
      JSON.stringify({ type: 'oktapod_archive', version: 1, exported_at: new Date().toISOString(), project: { name: 'Safe import', description: 'portable', scene: 'work' } }),
      JSON.stringify({ type: 'oktapod_conversation', conversation_id: conversationId, title: 'Thread' }),
      JSON.stringify({ type: 'oktapod_message', conversation_id: conversationId, role: 'user', content: 'hello', created_at: new Date().toISOString() }),
    ].join('\n')),
  })
  await expect(page.getByText('Safe import', { exact: true })).toBeVisible()
  await expect(page.getByText('未发现', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '导入为个人项目' }).click()
  await expect(page).toHaveURL(/\/projects\/[0-9a-f-]+/)
  await expect(page.getByRole('link', { name: /Thread hello/ })).toBeVisible()
  await page.getByRole('link', { name: 'Session Tree' }).click()
  await expect(page.getByRole('region', { name: 'Session Tree' }).getByRole('button').filter({ hasText: 'hello' })).toBeVisible()
})
