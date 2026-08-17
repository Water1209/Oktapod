import { expect, test, type Page } from '@playwright/test'

test.beforeEach(async ({ page }, testInfo) => {
  const suffix = `${Date.now().toString(36)}-${testInfo.testId.slice(-6)}`
  await page.goto('/')
  await expect(page.getByRole('heading', { name: '登录 Oktapod' })).toBeVisible()
  await page.getByRole('tab', { name: '注册' }).click()
  await page.getByRole('textbox', { name: '用户名' }).fill(`success.${suffix}`)
  await page.getByRole('textbox', { name: '显示名称' }).fill('Success Tester')
  await page.getByLabel('密码', { exact: true }).fill(`success-${suffix}-password`)
  await page.getByRole('button', { name: '创建并登录' }).click()
  await expect(page.getByText('模型服务已配置')).toBeVisible()
})

async function openProjectManagement(page: Page, view?: '文件' | '计划' | '智能体' | '用量') {
  if (await page.locator('.project-management-panel').count() === 0) {
    await page.getByRole('button', { name: '打开任务面板' }).click()
  }
  if (!view) return
  await page.locator('.project-management-panel > nav').getByRole('button', { name: /更多/ }).click()
  await page.locator('.panel-secondary-nav').getByRole('button', { name: view === '用量' ? '项目用量' : view, exact: true }).click()
}

async function sendTaskMessage(page: Page, content: string) {
  await page.locator('.conversation-composer').getByRole('combobox').first().click()
  await page.getByRole('option', { name: 'Task', exact: true }).click()
  await page.getByRole('textbox', { name: '消息' }).fill(content)
  await page.getByRole('button', { name: '发送' }).click()
}


async function openLinkedTask(page: Page) {
  await expect(page.locator('.task-capsule')).toBeVisible({ timeout: 15_000 })
  const openInspector = page.getByRole('button', { name: '打开任务面板', exact: true })
  if (await openInspector.isVisible()) await openInspector.click()
}

test('Reviewer 使用数据库中的准确 Provider 与 Model 配置', async ({ page }) => {
  await page.goto('/settings/governance')
  await expect(page.getByRole('heading', { name: '治理与审批' })).toBeVisible()
  const reviewer = page.locator('.governance-form')
  const provider = reviewer.getByRole('combobox', { name: 'Reviewer Provider' })
  const model = reviewer.getByRole('combobox', { name: 'Reviewer Model' })
  await provider.selectOption({ index: 1 })
  await expect(model.locator('option')).toHaveCount(2)
  await model.selectOption({ index: 1 })
  await reviewer.getByRole('checkbox', { name: '启用自动审查' }).check()
  await Promise.all([
    page.waitForResponse((response) => response.request().method() === 'PUT' && new URL(response.url()).pathname === '/api/v1/settings/ai-governance' && response.ok()),
    page.getByRole('button', { name: '保存治理策略' }).click(),
  ])
  await page.goto('/diagnostics')
  const autoReview = page.locator('.diagnostic-list > div').filter({ hasText: '自动审查' })
  await expect(autoReview.getByText('正常', { exact: true })).toBeVisible()
})

test('Personal 成功结果需要用户独立接受', async ({ page }) => {
  test.setTimeout(90_000)
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await expect(page.getByText('模型服务已配置')).toBeVisible()

  await page.getByRole('link', { name: '查看项目' }).click()
  await page.getByRole('button', { name: '新建项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`成功验收 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证执行成功和用户接受相互独立')
  await page.getByRole('button', { name: '建立工作空间' }).click()
  await openProjectManagement(page, '计划')
  await page.getByRole('textbox', { name: '待办标题' }).fill('独立项目计划')
  await Promise.all([
    page.waitForResponse((response) => response.request().method() === 'POST' && /\/api\/v1\/projects\/[^/]+\/plan-items$/.test(new URL(response.url()).pathname) && response.ok()),
    page.getByRole('button', { name: '新建待办' }).click(),
  ])
  await expect(page.getByText('独立项目计划')).toBeVisible()
  await Promise.all([
    page.waitForResponse((response) => response.request().method() === 'POST' && /\/api\/v1\/plan-items\/[^/]+\/status$/.test(new URL(response.url()).pathname) && response.ok()),
    page.getByRole('combobox', { name: '移动 独立项目计划' }).selectOption('in_progress'),
  ])
  await expect(page.locator('.plan-column').nth(1).getByText('独立项目计划')).toBeVisible()

  const goal = '返回 E2E 模型执行结果'
  await sendTaskMessage(page, goal)
  await openLinkedTask(page)

  await expect(page.locator('.task-capsule').getByText('已完成', { exact: true })).toBeVisible({ timeout: 15_000 })
  await page.getByRole('button', { name: '更多', exact: true }).click()
  await page.getByRole('button', { name: '用量', exact: true }).click()
  await expect(page.locator('.inspector-facts div').filter({ hasText: '调用' }).getByText('2', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '运行', exact: true }).click()
  await expect(page.getByRole('region', { name: '任务结果' }).getByText('E2E 模型执行成功。', { exact: true })).toBeVisible()
  await expect(page.locator('.artifact-publish-row').getByText('执行结果.md')).toBeVisible({ timeout: 15_000 })
  await page.getByRole('button', { name: '追踪', exact: true }).click()
  await expect(page.locator('.inspector-event-list article').first()).toBeVisible()

  await page.getByRole('button', { name: '运行', exact: true }).click()
  await page.getByRole('button', { name: '接受成果' }).click()
  await expect(page.getByRole('button', { name: '接受成果' })).toHaveCount(0)
  await expect(page.getByRole('region', { name: '任务结果' })).toContainText('已接受')

  const resultArtifact = page.locator('.artifact-publish-row').filter({ hasText: '执行结果.md' })
  await resultArtifact.getByRole('combobox', { name: '执行结果.md 保存范围' }).selectOption('project')
  await Promise.all([
    page.waitForResponse((response) => response.request().method() === 'POST' && /\/api\/v1\/artifacts\/[^/]+\/project-files$/.test(new URL(response.url()).pathname) && response.ok()),
    resultArtifact.getByRole('button', { name: '保存到项目资料' }).click(),
  ])
  await expect(resultArtifact.getByRole('button', { name: '已保存到项目资料' })).toBeVisible()
  await page.getByRole('button', { name: '文件', exact: true }).click()
  await page.getByRole('button', { name: '工作文件', exact: true }).click()
  const candidateOutputs = page.locator('.workspace-lifecycle-group.candidate_output')
  await expect(candidateOutputs).toContainText('待登记输出')
  await expect(candidateOutputs).toContainText('执行结果.md')
  await page.getByRole('button', { name: '项目资料', exact: true }).click()
  const markdownLink = page.locator('.asset-row .asset-name').getByRole('link', { name: '执行结果.md' })
  await expect(markdownLink).toHaveAttribute('href', /\/api\/v1\/assets\/.+\/preview/)
  const [previewPage] = await Promise.all([page.waitForEvent('popup'), markdownLink.click()])
  await expect(previewPage.locator('body')).toContainText('E2E 模型执行成功。')
  await previewPage.close()
  await page.getByRole('button', { name: '任务输出', exact: true }).click()
  await expect(page.locator('.files-inspector-list > div').filter({ hasText: '执行结果.md' }).getByRole('button', { name: '已保存到项目资料' })).toBeVisible()
  await page.getByRole('button', { name: '项目资料', exact: true }).click()
  const projectFiles = page.locator('.files-inspector .asset-surface')
  await projectFiles.getByTestId('asset-file-input').setInputFiles({ name: '验收资产.txt', mimeType: 'text/plain', buffer: Buffer.from('asset e2e content') })
  await expect(projectFiles.locator('.asset-row .asset-name').getByText('验收资产.txt')).toBeVisible()
  await projectFiles.getByRole('textbox', { name: '搜索资产' }).fill('验收资产')
  await expect(projectFiles.getByRole('link', { name: '下载 验收资产.txt' })).toHaveAttribute('href', /\/api\/v1\/assets\/.+\/content/)
})

test('Changes 可按文件查看并通过原 Thread 受治理提交所选路径', async ({ page }) => {
  test.setTimeout(90_000)
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('link', { name: '查看项目' }).click()
  await page.getByRole('button', { name: '新建项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`Changes 闭环 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证 Diff 查看与选择提交都进入受治理任务')
  await page.getByRole('button', { name: '建立工作空间' }).click()

  await sendTaskMessage(page, '返回 E2E 模型执行结果')
  await expect(page.locator('.message-task-stream').getByText('任务已完成', { exact: true })).toBeVisible({ timeout: 45_000 })
  await openLinkedTask(page)
  await page.getByRole('button', { name: '变更', exact: true }).click()

  await expect(page.locator('.changes-file-list details').filter({ hasText: '执行结果.md' })).toBeVisible()
  await expect(page.locator('.changes-file-list details').filter({ hasText: '执行摘要.md' })).toBeVisible()
  await page.getByRole('checkbox', { name: '选择 执行结果.md' }).check()
  await page.getByRole('textbox', { name: 'Git 提交信息' }).fill('Commit selected result')
  await page.getByRole('button', { name: '提交所选' }).click()

  const approval = page.locator('.human-action-card').filter({ hasText: '执行结果.md' })
  await expect(approval).toContainText('执行结果.md', { timeout: 15_000 })
  await approval.getByRole('button', { name: '批准一次' }).click()
  await expect(page.locator('.message-task-stream').last().getByText('任务已完成', { exact: true })).toBeVisible({ timeout: 45_000 })

  await page.getByRole('button', { name: '变更', exact: true }).click()
  await page.getByRole('button', { name: 'Git HEAD', exact: true }).click()
  await expect(page.locator('.changes-file-list details').filter({ hasText: '执行摘要.md' })).toBeVisible()
  await expect(page.getByRole('checkbox', { name: '选择 执行结果.md' })).toHaveCount(0)
})

test('任务执行流默认收敛并支持多 Markdown 文件审阅', async ({ page }) => {
  test.setTimeout(90_000)
  const consoleProblems: string[] = []
  page.on('console', (message) => {
    if (['error', 'warning'].includes(message.type())) {
      consoleProblems.push(`${message.type()}: ${message.text()}`)
    }
  })
  page.on('pageerror', (error) => consoleProblems.push(`pageerror: ${error.message}`))
  await page.setViewportSize({ width: 780, height: 831 })

  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('link', { name: '查看项目' }).click()
  await page.getByRole('button', { name: '新建项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`任务进度交互 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证 Codex 风格任务进度和文件审阅')
  await page.getByRole('button', { name: '建立工作空间' }).click()

  const goal = '返回 E2E 模型执行结果'
  await sendTaskMessage(page, goal)

  const taskStream = page.locator('.message-task-stream')
  await expect(taskStream.getByText('任务已完成', { exact: true })).toBeVisible({ timeout: 45_000 })
  const artifactsButton = taskStream.getByRole('button', { name: /2 个文件/ })
  await expect(artifactsButton).toBeVisible({ timeout: 15_000 })
  await artifactsButton.click()

  await expect(page.getByRole('button', { name: '文件', exact: true })).toHaveClass(/active/)
  await expect(page.getByRole('button', { name: '任务输出', exact: true })).toHaveClass(/active/)
  expect(await page.locator('.project-management-panel').evaluate((panel) => {
    const rect = panel.getBoundingClientRect()
    const topElement = document.elementFromPoint(rect.left + 8, rect.top + 75)
    return Boolean(topElement && panel.contains(topElement))
  })).toBe(true)
  const outputs = page.locator('.files-inspector-section').filter({ hasText: '任务输出' })
  const resultLink = outputs.getByRole('link', { name: '执行结果.md', exact: true })
  const summaryLink = outputs.getByRole('link', { name: '执行摘要.md', exact: true })
  await expect(resultLink).toBeVisible()
  await expect(summaryLink).toBeVisible()
  const [resultPreview] = await Promise.all([page.waitForEvent('popup'), resultLink.click()])
  await expect(resultPreview.locator('body')).toContainText('E2E 模型执行成功。')
  await resultPreview.close()
  const [summaryPreview] = await Promise.all([page.waitForEvent('popup'), summaryLink.click()])
  await expect(summaryPreview.locator('body')).toContainText('# 执行摘要')
  await expect(summaryPreview.locator('body')).toContainText('| 浏览器验收 | 通过 |')
  await summaryPreview.close()
  expect(await outputs.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  expect(consoleProblems).toEqual([])
})
test('L2 命令建立可撤销项目规则后只执行一次并由代码验收', async ({ page }) => {
  test.setTimeout(90_000)
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('link', { name: '查看项目' }).click()
  await page.getByRole('button', { name: '新建项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`审批验收 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证冻结审批和副作用收据')
  await page.getByRole('button', { name: '建立工作空间' }).click()
  await page.locator('.conversation-composer').getByRole('combobox').first().click()
  await page.getByRole('option', { name: 'Task', exact: true }).click()
  await page.getByRole('button', { name: '执行设置' }).click()
  await page.locator('.composer-advanced .inline-select').first().click()
  await page.getByRole('option', { name: '代码场景', exact: true }).click()
  const taskTitle = '受控工具审批验收：创建文件并运行 node --version'
  await page.getByRole('textbox', { name: '消息' }).fill(taskTitle)
  await page.getByRole('button', { name: '发送' }).click()

  const commandApproval = page.locator('.human-action-card').filter({ hasText: '运行工作区命令' })
  await expect(commandApproval).toBeVisible({ timeout: 15_000 })
  await expect(commandApproval).toContainText('"node" "--version"')
  await expect(commandApproval.getByRole('button', { name: '本项目允许 30 天' })).toBeVisible()
  await commandApproval.getByRole('button', { name: '本项目允许 30 天' }).click()
  await page.getByRole('dialog').getByRole('button', { name: '确认授权' }).click()
  await expect(commandApproval).toHaveCount(0)
  await expect(page.locator('.message-task-stream').getByText('任务已完成', { exact: true })).toBeVisible({ timeout: 45_000 })
  const openInspector = page.getByRole('button', { name: '打开任务面板', exact: true })
  if (await openInspector.isVisible()) await openInspector.click()
  await page.getByRole('button', { name: '追踪', exact: true }).click()
  const completedCommand = page.locator('.inspector-event-list article').filter({ has: page.getByText('tool.completed', { exact: true }) }).filter({ hasText: 'workspace_run_command' })
  await expect(completedCommand).toHaveCount(1)
  await page.getByRole('button', { name: '运行', exact: true }).click()
  await expect(page.getByRole('region', { name: '任务结果' }).getByText('E2E 模型执行成功。', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '更多', exact: true }).click()
  await page.getByRole('button', { name: '安全', exact: true }).click()
  await expect(page.locator('.inspector-event-list article').filter({ hasText: 'workspace_run_command' }).filter({ hasText: 'sha256:' })).toBeVisible()
  await page.getByRole('button', { name: '撤销', exact: true }).click()
  await expect(page.getByText('没有生效中的项目规则', { exact: true })).toBeVisible()
  await page.getByRole('link', { name: '审批', exact: true }).click()
  await page.getByRole('button', { name: '风险审批', exact: true }).click()
  await page.getByRole('button', { name: '已批准', exact: true }).click()
  await expect(page.locator('.approval-card').filter({ hasText: '运行工作区命令' }).filter({ hasText: '已批准' })).toBeVisible()
  await page.getByRole('button', { name: '处理记录', exact: true }).click()
  await expect(page.locator('.approval-history-list').getByText('当前项目已允许同类窄规则动作 30 天，可随时撤销')).toBeVisible()
})

test('完全访问在有效窗口内跳过命令审批并自动完成', async ({ page }) => {
  test.setTimeout(60_000)
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('link', { name: '查看项目' }).click()
  await page.getByRole('button', { name: '新建项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`完全访问 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证完全访问只跳过可审批动作')
  await page.getByRole('button', { name: '建立工作空间' }).click()
  const composer = page.locator('.conversation-composer')
  await composer.getByRole('combobox', { name: '运行模式' }).click()
  await page.getByRole('option', { name: 'Task', exact: true }).click()
  await composer.getByRole('combobox', { name: '权限模式' }).click()
  await page.getByRole('option', { name: '完全访问', exact: true }).click()
  await page.getByRole('dialog').getByRole('button', { name: '确认启用' }).click()
  await page.getByRole('button', { name: '执行设置' }).click()
  await page.locator('.composer-advanced .inline-select').first().click()
  await page.getByRole('option', { name: '代码场景', exact: true }).click()
  const taskTitle = '受控工具审批验收：完全访问自动执行 node --version'
  await page.getByRole('textbox', { name: '消息' }).fill(taskTitle)
  await page.getByRole('button', { name: '发送' }).click()

  await openLinkedTask(page)
  await expect(page.locator('.message-task-stream').getByText('任务已完成', { exact: true })).toBeVisible({ timeout: 45_000 })
  await expect(page.locator('.approval-card')).toHaveCount(0)
  await page.getByRole('button', { name: '追踪', exact: true }).click()
  await expect(page.locator('.inspector-event-list article').filter({ hasText: 'tool.completed' }).filter({ hasText: 'workspace_run_command' })).toHaveCount(1)
})

test('Personal 项目为创建者私有且不暴露成员管理入口', async ({ page }) => {
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('link', { name: '查看项目' }).click()
  await page.getByRole('button', { name: '新建项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`个人范围 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证个人项目仅创建者可见')
  await page.getByRole('button', { name: '建立工作空间' }).click()

  await expect(page.getByRole('button', { name: '项目成员', exact: true })).toHaveCount(0)
  await expect(page.getByText('个人私有')).toBeVisible()
})

test('资源中心统一提供 Skill 与 MCP 安装入口', async ({ page }) => {
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('link', { name: '查看项目' }).click()
  await page.getByRole('button', { name: '新建项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`资源中心 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证 Skill 和 MCP 的统一安装入口')
  await page.getByRole('button', { name: '建立工作空间' }).click()
  await page.getByRole('link', { name: '资源', exact: true }).click()
  await expect(page.getByRole('heading', { name: '资源中心' })).toBeVisible()
  await expect(page.getByRole('button', { name: '项目资源 0' })).toBeVisible()
  await page.getByRole('button', { name: 'MCP 0' }).click()
  await expect(page.getByText('当前项目没有 MCP', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '项目资源 0' })).toBeVisible()
  await page.getByRole('button', { name: /系统能力/ }).click()
  await expect(page.getByRole('button', { name: /^MCP/ })).toHaveCount(0)
  await page.getByRole('button', { name: '项目资源 0' }).click()
  await page.getByRole('button', { name: /添加资源/ }).click()
  const menu = page.getByRole('menu')
  await expect(menu.getByRole('button', { name: /通过对话创建 Skill/ })).toBeVisible()
  await expect(menu.getByRole('button', { name: /导入 Skill ZIP/ })).toBeVisible()
  await expect(menu.getByRole('button', { name: /添加 MCP/ })).toBeVisible()
  await menu.getByRole('button', { name: /导入 Skill ZIP/ }).click()
  await expect(page.getByRole('dialog').getByRole('heading', { name: '导入 Skill' })).toBeVisible()
  await expect(page.locator('input[type="file"][accept="application/zip,.zip"]')).toHaveCount(1)
})

test('MCP 缺失凭据可在资源中心重新写入数据库密钥库', async ({ page }) => {
  test.setTimeout(60_000)
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('link', { name: '查看项目' }).click()
  await page.getByRole('button', { name: '新建项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`MCP Vault ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证 MCP 凭据缺失和重新录入')
  await page.getByRole('button', { name: '建立工作空间' }).click()
  await page.getByRole('link', { name: '资源', exact: true }).click()
  await page.getByRole('button', { name: /添加资源/ }).click()
  await page.getByRole('menu').getByRole('button', { name: /添加 MCP/ }).click()
  const dialog = page.getByRole('dialog')
  const endpoint = 'http://127.0.0.1:9/mcp'
  await dialog.getByPlaceholder(/例如 Tavily/).fill(endpoint)
  await dialog.getByRole('button', { name: '解析' }).click()
  await expect(dialog.getByText('已解析')).toBeVisible()
  await dialog.locator('summary').getByText('高级配置', { exact: true }).click()
  const manifestEditor = dialog.locator('textarea')
  const definition = JSON.parse(await manifestEditor.inputValue())
  definition.auth = { type: 'bearer' }
  definition.secret_bindings = [{ name: 'credential', target: 'header:Authorization', required: true }]
  await manifestEditor.fill(JSON.stringify(definition, null, 2))
  await dialog.getByRole('button', { name: '校验配置' }).click()
  await dialog.getByRole('button', { name: '确认安装' }).click()
  await expect(page.getByRole('button', { name: '审核并启用' })).toBeVisible()
  await page.getByRole('button', { name: '审核并启用' }).click()
  await expect(page.getByText('凭据缺失', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '重新录入' }).click()
  const secret = `mcp-e2e-${suffix}-secret`
  await page.getByPlaceholder('输入新凭据').fill(secret)
  await page.getByRole('button', { name: '保存并测试连接' }).click()
  await expect(page.getByRole('status')).toContainText('MCP 凭据已安全保存')
  await expect(page.getByText('数据库密钥库 · 可用')).toBeVisible()
  await expect(page.locator('body')).not.toContainText(secret)
})

test('V1 Composer 只暴露 Auto、Ask 和 Task 运行模式', async ({ page }) => {
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('link', { name: '查看项目' }).click()
  await page.getByRole('button', { name: '新建项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`V1 运行模式 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证 V1 不暴露未验收的 Agent Team 入口')
  await page.getByRole('button', { name: '建立工作空间' }).click()
  await page.locator('.conversation-composer').getByRole('combobox', { name: '运行模式' }).click()
  await expect(page.getByRole('option', { name: 'Auto', exact: true })).toBeVisible()
  await expect(page.getByRole('option', { name: 'Ask', exact: true })).toBeVisible()
  await expect(page.getByRole('option', { name: 'Task', exact: true })).toBeVisible()
  await expect(page.getByRole('option', { name: 'Agent Team', exact: true })).toHaveCount(0)
})

test('SSE 首次断线后按游标重连且终态事件不丢', async ({ page }) => {
  let aborted = false
  await page.route('**/api/v1/runs/*/events/stream*', async (route) => {
    if (!aborted) {
      aborted = true
      await route.abort('failed')
      return
    }
    await route.continue()
  })
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('link', { name: '查看项目' }).click()
  await page.getByRole('button', { name: '新建项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`SSE 断线验收 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证事件流断线重连')
  await page.getByRole('button', { name: '建立工作空间' }).click()
  const goal = 'SSE 断线重连后显示完整终态事件'
  await sendTaskMessage(page, goal)
  await openLinkedTask(page)

  await expect(page.locator('.task-capsule').getByText('已完成', { exact: true })).toBeVisible({ timeout: 15_000 })
  await page.getByRole('button', { name: '追踪', exact: true }).click()
  await expect(page.locator('.inspector-event-list article').filter({ hasText: 'step.succeeded' })).toHaveCount(1)
  expect(aborted).toBe(true)
})

test('计划看板支持拖拽移动且不改变运行任务状态', async ({ page }) => {
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('link', { name: '查看项目' }).click()
  await page.getByRole('button', { name: '新建项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`拖拽计划 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证计划拖拽只改变 PlanItem')
  await page.getByRole('button', { name: '建立工作空间' }).click()
  await openProjectManagement(page, '计划')
  await page.getByRole('textbox', { name: '待办标题' }).fill('拖拽事项')
  await page.getByRole('button', { name: '新建待办' }).click()
  const card = page.locator('.plan-card').filter({ hasText: '拖拽事项' })
  await card.dragTo(page.locator('.plan-column').nth(1))
  await expect(page.getByRole('combobox', { name: '移动 拖拽事项' })).toHaveValue('in_progress')
})

test('Agent JSON 导入会预填表单并继续走服务端创建校验', async ({ page }) => {
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('link', { name: '查看项目' }).click()
  await page.getByRole('button', { name: '新建项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`Agent 导入 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证 Agent JSON 导入')
  await page.getByRole('button', { name: '建立工作空间' }).click()
  await openProjectManagement(page, '智能体')
  await page.locator('.agent-surface input[accept="application/json,.json"]').nth(0).setInputFiles({
    name: 'reviewer.agent.json',
    mimeType: 'application/json',
    buffer: Buffer.from(JSON.stringify({ name: 'Imported Reviewer', version: '1.0.0', instructions: 'Review structured evidence.', model: 'model-e2e' })),
  })
  await expect(page.getByRole('textbox', { name: 'Agent 名称' })).toHaveValue('Imported Reviewer')
  await expect(page.getByRole('textbox', { name: 'Agent 指令' })).toHaveValue('Review structured evidence.')
  await page.getByRole('button', { name: '保存 Agent' }).click()
  await expect(page.locator('.agent-surface .schedule-card').filter({ hasText: 'Imported Reviewer' })).toBeVisible()
})

test('V1 智能体面板只暴露 Agent 而不暴露 TeamDefinition', async ({ page }) => {
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('link', { name: '查看项目' }).click()
  await page.getByRole('button', { name: '新建项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`Agent 范围 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证 V1 智能体的可见范围')
  await page.getByRole('button', { name: '建立工作空间' }).click()
  await openProjectManagement(page, '智能体')
  await expect(page.getByRole('heading', { name: '版本化 Agent' })).toBeVisible()
  await expect(page.getByText('版本化 Agent Team', { exact: true })).toHaveCount(0)
  await expect(page.locator('.agent-surface input[accept="application/json,.json"]')).toHaveCount(1)
})

test('项目用量面板按权限展示聚合且不显示私密正文', async ({ page }) => {
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('link', { name: '查看项目' }).click()
  await page.getByRole('button', { name: '新建项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`用量聚合 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证项目级用量聚合')
  await page.getByRole('button', { name: '建立工作空间' }).click()
  await openProjectManagement(page, '用量')
  await expect(page.getByText('项目用量与成本')).toBeVisible()
  await expect(page.getByText(/不包含任务标题、提示词或私密正文/)).toBeVisible()
})

test('V1 不暴露未验收的自动化面板', async ({ page }) => {
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('link', { name: '查看项目' }).click()
  await page.getByRole('button', { name: '新建项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`计划草稿 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证对话创建计划草稿')
  await page.getByRole('button', { name: '建立工作空间' }).click()
  await openProjectManagement(page)
  await page.locator('.project-management-panel > nav').getByRole('button', { name: /更多/ }).click()
  await expect(page.locator('.panel-secondary-nav').getByRole('button', { name: '自动化' })).toHaveCount(0)
})

test('对话安装 MCP 返回可校验的资源草稿', async ({ page }) => {
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('link', { name: '查看项目' }).click()
  await page.getByRole('button', { name: '新建项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`MCP 对话安装 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证自然语言进入统一资源安装链')
  await page.getByRole('button', { name: '建立工作空间' }).click()
  await page.getByRole('textbox', { name: '消息' }).fill('请安装 https://example.com/mcp MCP')
  await page.getByRole('button', { name: '发送' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('heading', { name: '添加 MCP' })).toBeVisible()
  await expect(dialog.getByPlaceholder(/例如 Tavily/)).toHaveValue('https://example.com/mcp')
  await expect(dialog.getByText('远端 Streamable HTTP')).toBeVisible()
  await dialog.getByRole('button', { name: '校验配置' }).click()
  await expect(dialog.getByText('格式预检通过')).toBeVisible()
})

test('对话可解析为 Agent 草稿并预填版本化 Agent 表单', async ({ page }) => {
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('link', { name: '查看项目' }).click()
  await page.getByRole('button', { name: '新建项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`Agent 草稿 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证对话创建 Agent 草稿')
  await page.getByRole('button', { name: '建立工作空间' }).click()
  await page.getByRole('textbox', { name: '消息' }).fill('创建 Agent：证据审查员 | 1.0.0 | 审查来源完整性并输出风险 | model-e2e')
  await page.getByRole('button', { name: '发送' }).click()
  await expect(page.locator('.panel-secondary-nav').getByRole('button', { name: '智能体' })).toHaveClass(/active/)
  await expect(page.getByRole('textbox', { name: 'Agent 名称' })).toHaveValue('证据审查员')
  await expect(page.getByRole('textbox', { name: 'Agent 版本' })).toHaveValue('1.0.0')
  await expect(page.getByRole('textbox', { name: 'Agent 指令' })).toHaveValue('审查来源完整性并输出风险')
  await expect(page.getByRole('combobox', { name: 'Agent 模型' })).toHaveValue('model-e2e')
})

test('资源中心通过对话启动内置 Skill Creator', async ({ page }) => {
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('link', { name: '查看项目' }).click()
  await page.getByRole('button', { name: '新建项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`Skill Creator ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证内置 Skill Creator 从资源中心进入对话')
  await page.getByRole('button', { name: '建立工作空间' }).click()
  await page.getByRole('link', { name: '资源', exact: true }).click()
  await page.getByRole('button', { name: /添加资源/ }).click()
  await page.getByRole('menu').getByRole('button', { name: /通过对话创建 Skill/ }).click()
  await expect(page.getByRole('textbox', { name: '消息' })).toHaveValue(/请使用内置 Skill Creator 创建一个新 Skill/)
})
