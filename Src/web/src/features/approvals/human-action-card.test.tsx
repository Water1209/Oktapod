// @vitest-environment jsdom
import { fireEvent, render, screen, waitFor, cleanup } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { api, type HumanAction } from '../../shared/api/client'
import { HumanActionCard } from './human-action-card'

afterEach(() => { cleanup(); vi.restoreAllMocks() })

function setup() {
  const decide = vi.spyOn(api, 'decideHumanAction').mockResolvedValue({} as HumanAction)
  const action = { id: 'input-test', version: 1, kind: 'input_request', status: 'waiting_user', risk_level: 'L0', request_hash: 'test', expires_at: '', request: { prompt: '是否继续？', choices: ['继续', '暂停'] } } as unknown as HumanAction
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><HumanActionCard action={action} /></QueryClientProvider>)
  return decide
}

it('submits a selected legacy option with an empty optional text field', async () => {
  const decide = setup()
  expect((screen.getByText('提交并继续') as HTMLButtonElement).disabled).toBe(true)
  fireEvent.click(screen.getByRole('radio', { name: '继续' }))
  fireEvent.click(screen.getByText('提交并继续'))
  await waitFor(() => expect(decide).toHaveBeenCalledWith('input-test', expect.objectContaining({ answer: { answer: { selected: ['option-1'], text: '' } } })))
})

it('keeps the selected option when optional text is supplied', async () => {
  const decide = setup()
  fireEvent.click(screen.getByRole('radio', { name: '继续' }))
  fireEvent.change(screen.getByRole('textbox'), { target: { value: '只做基础玩法' } })
  fireEvent.click(screen.getByText('提交并继续'))
  await waitFor(() => expect(decide).toHaveBeenCalledWith('input-test', expect.objectContaining({ answer: { answer: { selected: ['option-1'], text: '只做基础玩法' } } })))
})

it('allows a freeform answer without forcing an option', async () => {
  const decide = setup()
  fireEvent.change(screen.getByRole('textbox'), { target: { value: '请先调整方案' } })
  fireEvent.click(screen.getByText('提交并继续'))
  await waitFor(() => expect(decide).toHaveBeenCalledWith('input-test', expect.objectContaining({ answer: { answer: { selected: [], text: '请先调整方案' } } })))
})
