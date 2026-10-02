import { beforeEach, describe, expect, it, vi } from 'vitest'

const client = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn(), post: vi.fn(), delete: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: client }))
vi.mock('../client', () => ({ apiClient: client }))

import conversationRecordsAPI from '../admin/conversationRecords'

describe('conversation records admin API', () => {
  beforeEach(() => Object.values(client).forEach((mock) => mock.mockReset()))

  it('lists conversations with the query as URL params', async () => {
    client.get.mockResolvedValue({ data: { items: [], total: 0 } })
    const result = await conversationRecordsAPI.list({ page: 2, page_size: 50, q: 'hello', user: 'alice' })

    expect(client.get).toHaveBeenCalledWith('/admin/conversation-records', {
      params: { page: 2, page_size: 50, q: 'hello', user: 'alice' }
    })
    expect(result).toEqual({ items: [], total: 0 })
  })

  it('reads and deletes one conversation by its key, escaping it for the URL', async () => {
    client.get.mockResolvedValue({ data: { conversation_key: 'a/b', total: 0, records: [] } })
    await conversationRecordsAPI.getThread('a/b c')
    expect(client.get).toHaveBeenCalledWith('/admin/conversation-records/threads/a%2Fb%20c')

    client.delete.mockResolvedValue({ data: { deleted: 2 } })
    await expect(conversationRecordsAPI.deleteThread('abc')).resolves.toEqual({ deleted: 2 })
    expect(client.delete).toHaveBeenCalledWith('/admin/conversation-records/threads/abc')
  })

  it('batch deletes and clears', async () => {
    client.post.mockResolvedValue({ data: { deleted: 5 } })
    await expect(conversationRecordsAPI.batchDelete(['a', 'b'])).resolves.toEqual({ deleted: 5 })
    expect(client.post).toHaveBeenCalledWith('/admin/conversation-records/batch-delete', { keys: ['a', 'b'] })

    await conversationRecordsAPI.clear()
    expect(client.post).toHaveBeenLastCalledWith('/admin/conversation-records/clear')
  })

  it('reads and writes the settings', async () => {
    client.get.mockResolvedValue({ data: { enabled: true, retention_days: 7 } })
    await expect(conversationRecordsAPI.getSettings()).resolves.toEqual({ enabled: true, retention_days: 7 })
    expect(client.get).toHaveBeenCalledWith('/admin/conversation-records/settings')

    client.put.mockResolvedValue({ data: { enabled: false, retention_days: 0 } })
    await conversationRecordsAPI.updateSettings({ enabled: false, retention_days: 0 })
    expect(client.put).toHaveBeenCalledWith('/admin/conversation-records/settings', { enabled: false, retention_days: 0 })
  })
})
