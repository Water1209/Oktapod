import { expect, test } from '@playwright/test'

test('Personal 用户能创建项目并看到缺少凭证的可恢复阻塞证据', async ({ page }) => {
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

  await page.getByRole('link', { name: '查看项目' }).click()
  await page.getByRole('button', { name: '新建项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`E2E 项目 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证 Personal 持久化任务闭环')
  await page.getByRole('button', { name: '建立工作空间' }).click()

  await expect(page).toHaveURL(/\/projects\/[0-9a-f-]+$/)
  await page.getByRole('button', { name: '打开任务面板' }).click()
  for (const tab of ['运行', '文件', '变更', '安全', '追踪']) {
    await expect(page.getByRole('button', { name: tab, exact: true })).toBeVisible()
  }
  await page.getByRole('button', { name: '文件', exact: true }).click()
  for (const section of ['工作区', '项目文件', '任务输出']) {
    await expect(page.getByRole('button', { name: section, exact: true })).toBeVisible()
  }
  await expect(page.getByText('工作区暂无文件')).toBeVisible()
  await page.getByRole('button', { name: '最大化任务面板' }).click()
  await expect(page.locator('.project-chat-workspace')).toHaveClass(/drawer-maximized/)
  expect(await page.locator('.project-management-panel').evaluate((panel) => panel.getBoundingClientRect().width)).toBeGreaterThan(500)
  await page.getByRole('button', { name: '还原任务面板' }).click()
  await expect(page.locator('.project-chat-workspace')).not.toHaveClass(/drawer-maximized/)
  await page.getByRole('button', { name: '更多' }).click()
  await expect(page.getByRole('button', { name: '能力', exact: true })).toHaveCount(0)
  await expect(page.getByRole('button', { name: '自动化', exact: true })).toHaveCount(0)
  await expect(page.getByRole('button', { name: '记忆', exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: '关闭任务面板' }).click()
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
})
