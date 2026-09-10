import { expect, test, type Page } from '@playwright/test'
import { assignTestInstanceOwner } from './instance-owner'

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
  if (['模型服务不回填凭据，并用启用列表控制项目可选模型', '设置显示生效来源并能切换显式主题'].includes(testInfo.title)) {
    assignTestInstanceOwner(`routes.${suffix}`)
  }
})

async function createProject(page: Page, name: string) {
  await page.getByRole('link', { name: '查看项目' }).click()
  await page.getByRole('button', { name: '新建项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(name)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证 Codex 式工作台路由与响应式交互')
  await page.getByRole('button', { name: '建立工作空间' }).click()
  await expect(page).toHaveURL(/\/projects\/[0-9a-f-]+$/)
}

test('发送已受理后断流保留附件和新草稿，原键重试不重复创建任务', async ({ page }) => {
  await createProject(page, `断流恢复 ${Date.now().toString(36)}`)
  const projectId = new URL(page.url()).pathname.split('/')[2]!
  const composer = page.getByRole('textbox', { name: '消息', exact: true })
  await page.locator('.conversation-composer input[type="file"]').setInputFiles({ name: 'sentinel.txt', mimeType: 'text/plain', buffer: Buffer.from('isolated attachment sentinel') })
  await expect(page.locator('.composer-attachment.ready')).toContainText('sentinel.txt')
  const keys: string[] = []
  let first = true
  await page.route(/\/api\/v1\/projects\/[^/]+\/turns(?:\/stream)?$/, async (route) => {
    keys.push(route.request().headers()['idempotency-key'] ?? '')
    if (first) {
      first = false
      const accepted = await route.fetch()
      expect(accepted.ok()).toBe(true)
      await route.abort('connectionclosed')
    } else await route.continue()
  })
  await composer.fill('返回 E2E 模型执行结果')
  await page.getByRole('button', { name: '发送', exact: true }).click()
  const failed = page.getByRole('alert').filter({ hasText: '发送未完成' })
  await expect(failed).toContainText('sentinel.txt')
  await composer.fill('新的草稿必须保留')
  await expect(failed.getByRole('button', { name: '恢复到输入框' })).toBeDisabled()
  await failed.getByRole('button', { name: '重试原消息' }).click()
  await expect(failed).toHaveCount(0)
  await expect(composer).toHaveValue('新的草稿必须保留')
  expect(keys).toHaveLength(2)
  expect(keys[0]).not.toBe('')
  expect(keys[1]).toBe(keys[0])
  const tasks = await page.request.get(`/api/v1/projects/${projectId}/tasks`)
  expect(tasks.ok()).toBe(true)
  expect((await tasks.json()).items).toHaveLength(1)
})

test('请求前断网可恢复文本和附件，已有草稿不会被覆盖', async ({ page }) => {
  await createProject(page, `离线恢复 ${Date.now().toString(36)}`)
  const composer = page.getByRole('textbox', { name: '消息', exact: true })
  await page.locator('.conversation-composer input[type="file"]').setInputFiles({ name: 'offline.txt', mimeType: 'text/plain', buffer: Buffer.from('isolated offline attachment') })
  await expect(page.locator('.composer-attachment.ready')).toContainText('offline.txt')
  await page.route(/\/api\/v1\/projects\/[^/]+\/turns(?:\/stream)?$/, (route) => route.abort('internetdisconnected'))
  await composer.fill('需要保留的原消息')
  await page.getByRole('button', { name: '发送', exact: true }).click()
  const failed = page.getByRole('alert').filter({ hasText: '发送未完成' })
  await expect(failed).toContainText('offline.txt')
  await composer.fill('后来输入的内容')
  await expect(failed.getByRole('button', { name: '恢复到输入框' })).toBeDisabled()
  await composer.clear()
  await failed.getByRole('button', { name: '恢复到输入框' }).click()
  await expect(composer).toHaveValue('需要保留的原消息')
  await expect(page.locator('.composer-attachment.ready')).toContainText('offline.txt')
  await expect(failed).toHaveCount(0)
})

test('同项目不同会话独立执行，一方等待审批不阻塞另一方', async ({ page }) => {
  test.setTimeout(60_000)
  await createProject(page, `会话并发 ${Date.now().toString(36)}`)
  await page.locator('.conversation-composer').getByRole('combobox').first().click()
  await page.getByRole('option', { name: 'Task', exact: true }).click()
  await page.getByRole('textbox', { name: '消息' }).fill('受控工具审批验收：创建文件并运行 node --version')
  await page.getByRole('button', { name: '发送', exact: true }).click()
  await expect(page.locator('.human-action-card')).toBeVisible({ timeout: 15_000 })
  const approvalURL = page.url()
  await page.getByRole('link', { name: /新建会话/ }).click()
  await page.getByRole('textbox', { name: '消息' }).fill('返回 E2E 模型执行结果')
  await page.getByRole('button', { name: '发送', exact: true }).click()
  await expect(page.locator('.message-task-stream').getByText('任务已完成', { exact: true })).toBeVisible({ timeout: 30_000 })
  expect(page.url()).not.toBe(approvalURL)
  await expect(page.getByRole('region', { name: '会话消息排队' })).toHaveCount(0)
  await page.goto(approvalURL)
  await expect(page.locator('.human-action-card')).toBeVisible()
  await page.getByRole('button', { name: '停止任务', exact: true }).click()
})

test('模型服务不回填凭据，并用启用列表控制项目可选模型', async ({ page }) => {
  await page.goto('/settings/providers')
  await expect(page.getByRole('heading', { name: '模型服务', exact: true })).toBeVisible()
  await page.getByRole('button', { name: '添加实例' }).click()
  await page.getByLabel('显示名称').fill('E2E Provider')
  await page.getByLabel('Endpoint').fill('http://127.0.0.1:19090/v1')
  await page.getByLabel(/API Key/).fill('fake-e2e-key')
  await page.getByLabel('状态').selectOption('active')
  await page.getByRole('textbox', { name: '手动模型 ID' }).fill('model-e2e')
  await page.getByRole('button', { name: '添加', exact: true }).click()
  await page.getByRole('combobox', { name: '默认模型' }).selectOption('model-e2e')
  await page.getByRole('button', { name: '保存所有更改' }).click()
  const providerList = page.getByRole('complementary', { name: '模型服务实例' })
  await expect(providerList.getByRole('button', { name: /E2E Provider/ })).toBeVisible()
  await expect(page.getByText('已配置 · 留空保持不变')).toBeVisible()
  await expect(page.locator('input[type="password"]')).toHaveValue('')
  await expect(page.getByRole('status')).toContainText(/保存并验证成功.*1 个模型/s)
  await expect(page.locator('.provider-save-bar')).toContainText('1 个模型')
  await expect(page.locator('input[type="password"]')).toHaveValue('')
})

test('项目标题菜单可以在个人项目之间切换', async ({ page }) => {
  const suffix = Date.now().toString(36)
  const firstName = `切换甲 ${suffix}`
  const secondName = `切换乙 ${suffix}`
  await createProject(page, firstName)
  const firstURL = page.url()

  await page.getByRole('link', { name: '项目', exact: true }).click()
  await page.getByRole('button', { name: '新建项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(secondName)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证项目切换菜单')
  await page.getByRole('button', { name: '建立工作空间' }).click()

  await page.getByRole('button', { name: new RegExp(secondName) }).click()
  const switcher = page.getByRole('menu', { name: '切换项目' })
  await expect(switcher).toBeVisible()
  await switcher.getByRole('menuitem', { name: new RegExp(firstName) }).click()
  await expect(page).toHaveURL(firstURL)
  await expect(page.getByRole('button', { name: new RegExp(firstName) })).toBeVisible()
})

test('Session Tree 可预览、标记并从用户消息重新编辑', async ({ page }) => {
  const suffix = Date.now().toString(36)
  await createProject(page, `会话树 ${suffix}`)
  const content = `保留这条可分支消息 ${suffix}`
  await page.getByRole('textbox', { name: '消息' }).fill(content)
  await page.getByRole('button', { name: '发送' }).click()
  await expect(page.getByRole('main').getByText('E2E 模型执行成功。', { exact: true })).toBeVisible({ timeout: 15_000 })

  const activeThread = page.locator('.thread-nav-row.active')
  await activeThread.hover()
  await activeThread.getByRole('button', { name: /管理/ }).click()
  await page.getByRole('link', { name: '查看会话分支' }).click()
  await expect(page.getByRole('heading', { name: '会话树' })).toBeVisible()
  const userEntry = page.getByRole('region', { name: 'Session Tree' }).getByRole('button').filter({ hasText: content })
  await expect(userEntry).toBeVisible()
  await userEntry.click()
  await expect(page.getByRole('button', { name: '重新编辑' })).toBeVisible()
  await expect(page.getByRole('button', { name: '创建分支' })).toBeVisible()
  await expect(page.getByRole('button', { name: '复制当前路径' })).toBeVisible()

  page.once('dialog', async (dialog) => dialog.accept(`关键节点 ${suffix}`))
  await page.getByRole('button', { name: '添加标签' }).click()
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
  await page.getByRole('button', { name: '打开任务面板' }).click()
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
  await expect(page.getByRole('heading', { name: '配置中心', exact: true })).toBeVisible()
  await expect(page.getByText(/来源：数据库 \+ 当前设备/).first()).toBeVisible()
  await page.getByRole('combobox', { name: '主题' }).selectOption('dark')
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
  await expect.poll(() => page.evaluate(() => getComputedStyle(document.body).color)).toBe('rgb(238, 241, 246)')
  await page.locator('.settings-detail').evaluate((element) => element.insertAdjacentHTML('beforeend', '<div data-theme-fixture class="message-body"><div class="assistant-markdown"><table><thead><tr><th>文件路径</th></tr></thead><tbody><tr><td><a href="#fixture">.research/report.md</a></td></tr></tbody></table></div><div class="task-capsule"><button class="task-capsule-head"><span class="task-capsule-current">已完成</span></button></div></div>'))
  const darkFixture = await page.locator('[data-theme-fixture]').evaluate((fixture) => {
    const style = (selector: string) => {
      const computed = getComputedStyle(fixture.querySelector(selector)!)
      return { color: computed.color, background: computed.backgroundColor }
    }
    return { cell: style('td'), link: style('a'), task: style('.task-capsule-head') }
  })
  expect(darkFixture.cell.color).toBe('rgb(238, 241, 246)')
  expect(darkFixture.link.color).toBe('rgb(138, 167, 255)')
  expect(darkFixture.task.background).toBe('rgb(23, 26, 34)')
  await page.goto('/settings/providers')
  await expect(page.getByRole('link', { name: /模型服务/ })).toBeVisible()
  await expect(page.getByRole('link', { name: /治理与审批/ })).toBeVisible()
  await expect(page.getByRole('link', { name: '外观与偏好' })).toBeVisible()
  await expect(page.locator('.settings-layout > nav a')).toHaveCount(7)
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
  const providerTheme = await page.evaluate(() => ({
    body: getComputedStyle(document.body).backgroundColor,
    sidebar: getComputedStyle(document.querySelector('.project-sidebar')!).backgroundColor,
    list: getComputedStyle(document.querySelector('.master-list')!).backgroundColor,
    action: getComputedStyle(document.querySelector('.primary-button')!).backgroundColor,
  }))
  expect(providerTheme.body).toBe('rgb(17, 19, 26)')
  expect(providerTheme.sidebar).not.toBe('rgb(240, 242, 245)')
  expect(providerTheme.list).not.toBe('rgb(247, 248, 250)')
  expect(providerTheme.action).toBe('rgb(36, 85, 230)')
  await page.getByRole('link', { name: '外观与偏好' }).click()
  await page.getByRole('combobox', { name: '主题' }).selectOption('light')
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')
  await page.getByRole('link', { name: '项目', exact: true }).click()
  await expect(page).toHaveURL('/projects')
  await expect(page.getByRole('heading', { name: '全部项目' })).toBeVisible()
  await page.getByRole('link', { name: '设置', exact: true }).click()
  await expect(page).toHaveURL('/settings/appearance')
  await page.goto('/settings/data')
  await expect(page.getByRole('heading', { name: '数据与备份' })).toBeVisible()
  await page.getByRole('link', { name: '打开项目交换' }).click()
  await expect(page).toHaveURL('/import-export')
  await expect(page.getByRole('heading', { name: '项目交换' })).toBeVisible()
})

test('导入导出先预检并拒绝包含敏感字段的包', async ({ page }) => {
  await createProject(page, `迁移页面 ${Date.now().toString(36)}`)
  await page.goto('/import-export')
  await expect(page.getByRole('heading', { name: '项目交换' })).toBeVisible()
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
  const activeThread = page.locator('.thread-nav-row.active')
  await activeThread.hover()
  await activeThread.getByRole('button', { name: /管理/ }).click()
  await page.getByRole('link', { name: '查看会话分支' }).click()
  await expect(page.getByRole('region', { name: 'Session Tree' }).getByRole('button').filter({ hasText: 'hello' })).toBeVisible()
})
