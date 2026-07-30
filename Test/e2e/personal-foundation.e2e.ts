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

  await page.getByRole('button', { name: '打开项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(`E2E 项目 ${suffix}`)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证 Personal 持久化任务闭环')
  await page.getByRole('button', { name: '建立工作空间' }).click()

  await expect(page).toHaveURL(/\/projects\/[0-9a-f-]+$/)
  await page.getByRole('button', { name: '打开检查器' }).click()
  await page.getByRole('button', { name: '更多' }).click()
  await page.getByRole('button', { name: '能力', exact: true }).click()
  await expect(page.getByRole('heading', { name: '能力中心' })).toBeVisible()
  await page.getByText('导入能力', { exact: true }).click()
  const scopePicker = page.locator('.capability-scope-picker')
  await expect(scopePicker.getByRole('button', { name: /全局/ })).toHaveClass(/active/)
  await expect(scopePicker.getByText('默认 · 所有项目可加载')).toBeVisible()
  await expect(page.getByText('这个范围还没有能力')).toBeVisible()
  await page.getByRole('button', { name: '关闭检查器' }).click()
  await expect(page.getByRole('button', { name: '安装能力附件' })).toBeVisible()

  await page.getByRole('combobox').first().click()
  await page.getByRole('option', { name: 'Task', exact: true }).click()
  const taskTitle = '输出一条 E2E 验收结果'
  await page.getByRole('textbox', { name: '消息' }).fill(taskTitle)
  await page.getByRole('button', { name: '发送' }).click()
  const taskStream = page.locator('.message-task-stream')
  await expect(taskStream).toContainText('数据库中没有可用的模型配置', { timeout: 15_000 })
  await expect(taskStream).toContainText('当前工作空间尚未保存模型配置')
})
