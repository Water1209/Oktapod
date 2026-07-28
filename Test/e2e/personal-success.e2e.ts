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

async function openProjectManagement(page: Page, view?: '文件' | '能力' | '计划' | '自动化' | '智能体' | '用量') {
  if (await page.locator('.project-management-panel').count() === 0) {
    await page.getByRole('button', { name: '打开检查器' }).click()
  }
  if (!view) return
  await page.locator('.project-management-panel > nav').getByRole('button', { name: /更多/ }).click()
  await page.locator('.panel-secondary-nav').getByRole('button', { name: view === '用量' ? '项目用量' : view, exact: true }).click()
}

async function sendTaskMessage(page: Page, content: string, mode: '任务' | 'Agent Team' = '任务') {
  await page.locator('.conversation-composer').getByRole('combobox').first().click()
  await page.getByRole('option', { name: mode === '任务' ? 'Task' : mode, exact: true }).click()
  await page.getByRole('textbox', { name: '消息' }).fill(content)
  await page.getByRole('button', { name: '发送' }).click()
}


async function openLinkedTask(page: Page) {
  const link = page.getByRole('link', { name: '查看任务详情' }).last()
  await expect(link).toBeVisible({ timeout: 15_000 })
  await link.click()
  const openInspector = page.getByRole('button', { name: '打开检查器', exact: true })
  if (await openInspector.isVisible()) await openInspector.click()
}
test('Personal 成功结果需要用户独立接受', async ({ page }) => {
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await expect(page.getByText('模型服务已配置')).toBeVisible()

  await page.getByRole('button', { name: '打开项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`成功验收 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证执行成功和用户接受相互独立')
  await page.getByRole('button', { name: '建立工作空间' }).click()
  await openProjectManagement(page, '计划')
  await page.getByRole('textbox', { name: '待办标题' }).fill('独立项目计划')
  await page.getByRole('button', { name: '新建待办' }).click()
  await expect(page.getByText('独立项目计划')).toBeVisible()
  await page.getByRole('combobox', { name: '移动 独立项目计划' }).selectOption('in_progress')
  await expect(page.locator('.plan-column').nth(1).getByText('独立项目计划')).toBeVisible()

  const goal = '返回 E2E 模型执行结果'
  await sendTaskMessage(page, goal)
  await openLinkedTask(page)

  await expect(page.locator('.message-task-stream').getByText('任务已完成', { exact: true })).toBeVisible({ timeout: 15_000 })
  await page.getByRole('button', { name: 'Usage', exact: true }).click()
  await expect(page.locator('.inspector-facts div').filter({ hasText: '调用' }).getByText('2', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Run', exact: true }).click()
  await expect(page.getByRole('region', { name: '任务结果' }).getByText('E2E 模型执行成功。', { exact: true })).toBeVisible()
  await expect(page.locator('.artifact-publish-row').getByText('执行结果.md')).toBeVisible({ timeout: 15_000 })
  await page.getByRole('button', { name: 'Trace', exact: true }).click()
  await expect(page.locator('.inspector-event-list article').first()).toBeVisible()

  await page.getByRole('button', { name: 'Run', exact: true }).click()
  await page.getByRole('button', { name: '接受成果' }).click()
  await expect(page.getByRole('button', { name: '接受成果' })).toHaveCount(0)
  await expect(page.getByRole('region', { name: '任务结果' })).toContainText('已接受')

  const resultArtifact = page.locator('.artifact-publish-row').filter({ hasText: '执行结果.md' })
  await resultArtifact.getByRole('combobox', { name: '执行结果.md 发布可见性' }).selectOption('project')
  await resultArtifact.getByRole('button', { name: '发布到资产' }).click()
  await expect(resultArtifact.getByRole('button', { name: '已发布' })).toBeVisible()
  await openProjectManagement(page, '文件')
  const markdownLink = page.locator('.asset-row .asset-name').getByRole('link', { name: '执行结果.md' })
  await expect(markdownLink).toHaveAttribute('href', /\/api\/v1\/assets\/.+\/preview/)
  const [previewPage] = await Promise.all([page.waitForEvent('popup'), markdownLink.click()])
  await expect(previewPage.locator('body')).toContainText('E2E 模型执行成功。')
  await previewPage.close()
  await expect(page.getByText(/5\.00 GiB/)).toBeVisible()
  await page.getByRole('textbox', { name: '新文件夹名称' }).fill('E2E 资料')
  await page.getByRole('button', { name: '新建文件夹' }).click()
  await page.getByRole('button', { name: 'E2E 资料', exact: true }).click()
  await page.getByTestId('asset-file-input').setInputFiles({ name: '验收资产.txt', mimeType: 'text/plain', buffer: Buffer.from('asset e2e content') })
  await expect(page.locator('.asset-row .asset-name').getByText('验收资产.txt')).toBeVisible()
  await page.getByRole('textbox', { name: '搜索资产' }).fill('验收资产')
  await expect(page.getByRole('link', { name: '下载 验收资产.txt' })).toHaveAttribute('href', /\/api\/v1\/assets\/.+\/content/)
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
  await page.getByRole('button', { name: '打开项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`任务进度交互 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证 Codex 风格任务进度和文件审阅')
  await page.getByRole('button', { name: '建立工作空间' }).click()

  const goal = '返回 E2E 模型执行结果'
  await sendTaskMessage(page, goal)

  const taskStream = page.locator('.message-task-stream')
  await expect(taskStream.getByText('执行计划', { exact: true })).toBeVisible({ timeout: 45_000 })
  await expect(taskStream.locator('.message-execution-steps')).toContainText('任务已创建')
  const artifactsButton = taskStream.getByRole('button', { name: /2 个文件/ })
  await expect(artifactsButton).toBeVisible({ timeout: 15_000 })
  await artifactsButton.click()

  const review = page.getByRole('complementary', { name: /审阅任务文件/ })
  await expect(review).toBeVisible()
  await expect(review).toContainText(`2 个文件 · ${goal}`)
  await expect(review.getByRole('button', { name: /执行结果\.md/ })).toBeVisible()
  await expect(review.getByRole('button', { name: /执行摘要\.md/ })).toBeVisible()
  await expect(review.locator('.file-review-panel-content')).toContainText('E2E 模型执行成功。')

  await review.getByRole('button', { name: /执行摘要\.md/ }).click()
  await expect(review.getByRole('heading', { name: '执行摘要' })).toBeVisible()
  await expect(review.getByRole('table')).toContainText('浏览器验收')
  await expect(review.getByRole('table')).toContainText('通过')
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  expect(consoleProblems).toEqual([])
})
test('L2 命令建立可撤销项目规则后只执行一次并由代码验收', async ({ page }) => {
  test.setTimeout(90_000)
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('button', { name: '打开项目' }).click()
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
  await page.getByRole('link', { name: '查看任务详情' }).filter({ hasText: '查看任务详情' }).last().click()

  await expect(page.locator('.message-task-stream').getByText('任务已完成', { exact: true })).toBeVisible({ timeout: 45_000 })
  const openInspector = page.getByRole('button', { name: '打开检查器', exact: true })
  if (await openInspector.isVisible()) await openInspector.click()
  await page.getByRole('button', { name: 'Trace', exact: true }).click()
  const completedCommand = page.locator('.inspector-event-list article').filter({ has: page.getByText('tool.completed', { exact: true }) }).filter({ hasText: 'workspace_run_command' })
  await expect(completedCommand).toHaveCount(1)
  await page.getByRole('button', { name: 'Run', exact: true }).click()
  await expect(page.getByRole('region', { name: '任务结果' }).getByText('E2E 模型执行成功。', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '更多', exact: true }).click()
  await page.getByRole('button', { name: 'Security', exact: true }).click()
  await expect(page.locator('.inspector-event-list article').filter({ hasText: 'workspace_run_command' }).filter({ hasText: 'sha256:' })).toBeVisible()
  await page.getByRole('button', { name: '撤销', exact: true }).click()
  await expect(page.getByText('没有生效中的项目规则', { exact: true })).toBeVisible()
  await page.getByRole('link', { name: '审批', exact: true }).click()
  await page.getByRole('button', { name: '已批准', exact: true }).click()
  await page.locator('.approval-history summary').click()
  await expect(page.locator('.approval-history').getByText('当前项目已允许同类窄规则动作 30 天，可随时撤销')).toBeVisible()
})

test('完全访问在有效窗口内跳过命令审批并自动完成', async ({ page }) => {
  test.setTimeout(60_000)
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('button', { name: '打开项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`完全访问 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证完全访问只跳过可审批动作')
  await page.getByRole('button', { name: '建立工作空间' }).click()
  const composer = page.locator('.conversation-composer')
  await composer.getByRole('combobox').nth(0).click()
  await page.getByRole('option', { name: 'Task', exact: true }).click()
  await composer.getByRole('combobox').nth(1).click()
  await page.getByRole('option', { name: 'Full Access · 30 min', exact: true }).click()
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
  await page.getByRole('button', { name: 'Trace', exact: true }).click()
  await expect(page.locator('.inspector-event-list article').filter({ hasText: 'tool.completed' }).filter({ hasText: 'workspace_run_command' })).toHaveCount(1)
})

test('Personal 项目为创建者私有且不暴露成员管理入口', async ({ page }) => {
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('button', { name: '打开项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`个人范围 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证个人项目仅创建者可见')
  await page.getByRole('button', { name: '建立工作空间' }).click()

  await expect(page.getByRole('button', { name: '项目成员', exact: true })).toHaveCount(0)
  await expect(page.getByText('个人私有')).toBeVisible()
})

test('能力 manifest 可从文件导入、隔离并导出', async ({ page }) => {
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('button', { name: '打开项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`能力迁移 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证能力 manifest 的导入预检和导出迁移')
  await page.getByRole('button', { name: '建立工作空间' }).click()
  await openProjectManagement(page, '能力')
  await page.locator('.capability-surface .create-disclosure > summary').click()
  await page.locator('.capability-surface .capability-scope-picker button:has-text("当前项目")').click()
  await page.locator('.capability-surface input[type="file"]').setInputFiles({
    name: 'research.skill.json',
    mimeType: 'application/json',
    buffer: Buffer.from(JSON.stringify({ kind: 'skill', name: 'Research Skill', version: '1.2.0', manifest_json: JSON.stringify({ description: 'safe skill', permissions: ['read'] }) })),
  })
  await expect(page.getByText('自动识别')).toBeVisible()
  await expect(page.getByText('Research Skill')).toBeVisible()
  await expect(page.getByText(/SKILL · v1\.2\.0/)).toBeVisible()
  await page.getByRole('button', { name: '导入并预检' }).click()
  const card = page.locator('.schedule-card').filter({ hasText: 'Research Skill' })
  await expect(card.getByText(/待审查/)).toBeVisible()
  const download = page.waitForEvent('download')
  await card.getByRole('button', { name: '导出' }).click()
  await expect((await download).suggestedFilename()).toMatch(/Research-Skill\.manifest\.json/)
})

test('Agent Team 任务展示协作室、成员状态与群聊投影', async ({ page }) => {
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('button', { name: '打开项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`Agent Team 验收 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证多 Agent 协作任务')
  await page.getByRole('button', { name: '建立工作空间' }).click()

  const goal = '请由研究者和构建者协作完成一份简短交付说明'
  await sendTaskMessage(page, goal, 'Agent Team')
  await openLinkedTask(page)

  await expect(page.getByRole('region', { name: 'Agent Team 协作室' })).toBeVisible({ timeout: 15_000 })
  await expect(page.getByText(/协调者|研究者|构建者/).first()).toBeVisible()
  await expect(page.getByText(/group cursor/)).toBeVisible()
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
  await page.getByRole('button', { name: '打开项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`SSE 断线验收 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证事件流断线重连')
  await page.getByRole('button', { name: '建立工作空间' }).click()
  const goal = '验证 SSE 断线后仍能看到完整终态'
  await sendTaskMessage(page, goal)
  await openLinkedTask(page)

  await expect(page.locator('.message-task-stream').getByText('任务已完成', { exact: true })).toBeVisible({ timeout: 15_000 })
  await page.getByRole('button', { name: 'Trace', exact: true }).click()
  await expect(page.locator('.inspector-event-list article').filter({ hasText: 'step.succeeded' })).toHaveCount(1)
  expect(aborted).toBe(true)
})

test('计划看板支持拖拽移动且不改变运行任务状态', async ({ page }) => {
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('button', { name: '打开项目' }).click()
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
  await page.getByRole('button', { name: '打开项目' }).click()
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

test('TeamDefinition JSON 导入会预填角色并继续走服务端校验', async ({ page }) => {
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('button', { name: '打开项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`Team 导入 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证 TeamDefinition JSON 导入')
  await page.getByRole('button', { name: '建立工作空间' }).click()
  await openProjectManagement(page, '智能体')
  await page.locator('.agent-surface input[accept="application/json,.json"]').nth(1).setInputFiles({
    name: 'research-team.json',
    mimeType: 'application/json',
    buffer: Buffer.from(JSON.stringify({ name: 'Imported Research Team', version: '1.0.0', roles: [{ role: 'researcher', name: 'Researcher' }, { role: 'builder', name: 'Builder' }] })),
  })
  await expect(page.getByRole('textbox', { name: 'Team 名称' })).toHaveValue('Imported Research Team')
  await expect(page.getByRole('textbox', { name: 'Team 角色 JSON' })).toHaveValue(/researcher/)
  await page.getByRole('button', { name: '保存 TeamDefinition' }).click()
  await expect(page.locator('.agent-surface .schedule-card').filter({ hasText: 'Imported Research Team' })).toBeVisible()
})

test('项目用量面板按权限展示聚合且不显示私密正文', async ({ page }) => {
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('button', { name: '打开项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`用量聚合 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证项目级用量聚合')
  await page.getByRole('button', { name: '建立工作空间' }).click()
  await openProjectManagement(page, '用量')
  await expect(page.getByText('项目用量与成本')).toBeVisible()
  await expect(page.getByText(/不包含任务标题、提示词或私密正文/)).toBeVisible()
})

test('对话可解析为计划草稿并要求用户在自动化面板确认', async ({ page }) => {
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('button', { name: '打开项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`计划草稿 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证对话创建计划草稿')
  await page.getByRole('button', { name: '建立工作空间' }).click()
  await page.getByRole('textbox', { name: '消息' }).fill('创建计划：日报 | 0 9 * * * | Asia/Shanghai | 生成当天日报')
  await page.getByRole('button', { name: '发送' }).click()
  await expect(page.locator('.panel-secondary-nav').getByRole('button', { name: '自动化' })).toHaveClass(/active/)
  await expect(page.getByRole('textbox', { name: '计划名称' })).toHaveValue('日报')
  await expect(page.getByRole('textbox', { name: 'Cron 表达式' })).toHaveValue('0 9 * * *')
  await expect(page.getByRole('textbox', { name: '计划目标' })).toHaveValue('生成当天日报')
})

test('对话可解析为能力草稿并要求用户在能力面板确认', async ({ page }) => {
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('button', { name: '打开项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`能力草稿 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证对话创建能力草稿')
  await page.getByRole('button', { name: '建立工作空间' }).click()
  await page.getByRole('textbox', { name: '消息' }).fill('创建 Skill：研究证据 | 1.0.0 | 整理研究证据并输出来源')
  await page.getByRole('button', { name: '发送' }).click()
  await expect(page.locator('.panel-secondary-nav').getByRole('button', { name: '能力' })).toHaveClass(/active/)
  await expect(page.getByText('自动识别')).toBeVisible()
  await expect(page.getByText('研究证据')).toBeVisible()
  await expect(page.getByText(/SKILL · v1\.0\.0/)).toBeVisible()
})

test('对话可解析为 Agent 草稿并预填版本化 Agent 表单', async ({ page }) => {
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('button', { name: '打开项目' }).click()
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

test('对话可解析为 TeamDefinition 草稿并预填角色 JSON', async ({ page }) => {
  const suffix = Date.now().toString(36)
  await page.goto('/')
  await page.getByRole('button', { name: '打开项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`Team 草稿 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证对话创建 TeamDefinition 草稿')
  await page.getByRole('button', { name: '建立工作空间' }).click()
  await page.getByRole('textbox', { name: '消息' }).fill('创建 Agent Team：研究交付队 | 1.0.0 | [{"role":"researcher","name":"研究员"},{"role":"builder","name":"构建者"}]')
  await page.getByRole('button', { name: '发送' }).click()
  await expect(page.locator('.panel-secondary-nav').getByRole('button', { name: '智能体' })).toHaveClass(/active/)
  await expect(page.getByRole('textbox', { name: 'Team 名称' })).toHaveValue('研究交付队')
  await expect(page.getByRole('textbox', { name: 'Team 版本' })).toHaveValue('1.0.0')
  await expect(page.getByRole('textbox', { name: 'Team 角色 JSON' })).toHaveValue(/研究员/)
  await expect(page.getByRole('textbox', { name: 'Team 角色 JSON' })).toHaveValue(/构建者/)
})
