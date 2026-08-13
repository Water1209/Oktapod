import { expect, test } from '@playwright/test'

test('用户注册后创建个人私有项目并可重新登录', async ({ page }, testInfo) => {
  const suffix = `${Date.now().toString(36)}-${testInfo.project.name}`
  const username = `architect.${suffix}`
  const password = `${crypto.randomUUID()}${crypto.randomUUID()}`
  const projectName = `个人项目 ${suffix}`

  await page.goto('/')
  await expect(page.getByRole('heading', { name: '登录 Oktapod' })).toBeVisible()
  await page.getByRole('tab', { name: '注册' }).click()
  await page.getByRole('textbox', { name: '用户名' }).fill(username)
  await page.getByRole('textbox', { name: '显示名称' }).fill('E2E Architect')
  await page.getByLabel('密码', { exact: true }).fill(password)
  await page.getByRole('button', { name: '创建并登录' }).click()

  await expect(page.getByRole('heading', { name: '工作台' })).toBeVisible()
  await expect(page.getByText('仅你可见', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '创建第一个项目' }).click()
  await page.getByRole('textbox', { name: '项目名称' }).fill(projectName)
  await page.getByRole('textbox', { name: '目标说明' }).fill('验证个人项目只属于当前账户')
  await page.getByRole('button', { name: '建立工作空间' }).click()

  await expect(page).toHaveURL(/\/projects\/[0-9a-f-]+$/)
  await expect(page.getByRole('heading', { name: projectName })).toBeVisible()
  await expect(page.getByRole('textbox', { name: '消息' })).toBeVisible()
  await expect(page.getByRole('button', { name: '打开任务面板' })).toBeVisible()
  if (testInfo.project.name === 'mobile') {
    await page.getByRole('button', { name: '打开导航' }).click()
  }
  await expect(page.getByRole('button', { name: new RegExp(projectName) })).toBeVisible()
  if (testInfo.project.name === 'desktop') {
    await page.getByRole('button', { name: '收起项目栏' }).click()
    await expect(page.getByRole('button', { name: '展开项目栏' })).toBeVisible()
    await page.getByRole('button', { name: '展开项目栏' }).click()
  }

  await page.getByRole('button', { name: /打开账号菜单/ }).click()
  await page.getByRole('menuitem', { name: '退出登录', exact: true }).click()
  await expect(page.getByRole('heading', { name: '登录 Oktapod' })).toBeVisible()
  await page.getByRole('textbox', { name: '用户名' }).fill(username)
  await page.getByLabel('密码', { exact: true }).fill(password)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByRole('heading', { name: '工作台' })).toBeVisible()
  await expect(page.getByRole('main').getByRole('link', { name: new RegExp(projectName) })).toBeVisible()
})
