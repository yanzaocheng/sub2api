import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import ConversationRecordsView from '../ConversationRecordsView.vue'

const { list, getThread, deleteThread, batchDelete, clear, getSettings, showError, showSuccess } = vi.hoisted(() => ({
  list: vi.fn(),
  getThread: vi.fn(),
  deleteThread: vi.fn(),
  batchDelete: vi.fn(),
  clear: vi.fn(),
  getSettings: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: { conversationRecords: { list, getThread, deleteThread, batchDelete, clear, getSettings } }
}))
vi.mock('@/stores', () => ({ useAppStore: () => ({ showError, showSuccess }) }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  // 带参数的文案把参数序列化进去，便于断言计数等插值。
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key)
  })
}))

const summary = (key: string, over: Record<string, unknown> = {}) => ({
  conversation_key: key,
  user_id: 7,
  user_email: `${key}@example.com`,
  api_key_name: 'my-key',
  endpoint: '/v1/messages',
  model: 'claude-sonnet-4-5',
  turns: 3,
  first_at: '2026-09-30T08:00:00Z',
  last_at: '2026-09-30T08:05:00Z',
  first_prompt: `first prompt of ${key}`,
  last_response: `last response of ${key}`,
  ...over
})

const turn = (id: number, over: Record<string, unknown> = {}) => ({
  id,
  created_at: '2026-09-30T08:00:00Z',
  request_id: `req-${id}`,
  conversation_key: 'alpha',
  user_email: 'alpha@example.com',
  api_key_name: 'my-key',
  endpoint: '/v1/messages',
  model: 'claude-sonnet-4-5',
  stream: true,
  duration_ms: 1500,
  client_ip: '203.0.113.5',
  user_agent: 'claude-cli',
  system_prompt: '',
  messages: [{ role: 'user', content: `question ${id}` }],
  response: `answer ${id}`,
  ...over
})

function mountView() {
  return mount(ConversationRecordsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
        },
        DataTable: {
          props: ['columns', 'data', 'selectedKeys'],
          emits: ['row-click', 'update:selected-keys'],
          template: `<div data-test="table">
            <div v-for="row in data" :key="row.conversation_key" data-test="row" @click="$emit('row-click', row)">
              <slot name="cell-last_at" :row="row" :value="row.last_at" />
              <slot name="cell-user" :row="row" :value="row.user_email" />
              <slot name="cell-model" :row="row" :value="row.model" />
              <slot name="cell-conversation" :row="row" :value="row.first_prompt" />
              <slot name="cell-turns" :row="row" :value="row.turns" />
              <slot name="cell-actions" :row="row" />
              <input type="checkbox" data-test="select" @click.stop="$emit('update:selected-keys', [...selectedKeys, row.conversation_key])" />
            </div>
            <slot v-if="!data.length" name="empty" />
          </div>`
        },
        BaseDialog: {
          props: ['show', 'title'],
          emits: ['close'],
          template: '<div v-if="show" :data-dialog="title"><slot /><slot name="footer" /></div>'
        },
        ConfirmDialog: {
          props: ['show', 'title', 'message'],
          emits: ['confirm', 'cancel'],
          template: `<div v-if="show" :data-confirm="title">
            <span data-test="confirm-message">{{ message }}</span>
            <button data-test="confirm" @click="$emit('confirm')">ok</button>
            <button data-test="cancel" @click="$emit('cancel')">no</button>
          </div>`
        },
        Pagination: {
          props: ['page', 'total'],
          emits: ['update:page'],
          template: '<button data-test="page" @click="$emit(\'update:page\', 2)">{{ page }}/{{ total }}</button>'
        },
        Select: {
          props: ['modelValue', 'options'],
          emits: ['update:modelValue'],
          template: `<select data-test="time-range" :value="modelValue" @change="$emit('update:modelValue', $event.target.value)">
            <option v-for="o in options" :key="o.value" :value="o.value">{{ o.label }}</option>
          </select>`
        },
        Icon: true,
        'router-link': { template: '<a data-test="link"><slot /></a>' }
      }
    }
  })
}

let wrapper: ReturnType<typeof mountView>

beforeEach(() => {
  vi.clearAllMocks()
  list.mockResolvedValue({ items: [summary('alpha'), summary('beta', { turns: 1 })], total: 2 })
  getSettings.mockResolvedValue({ enabled: true, retention_days: 0 })
  getThread.mockResolvedValue({ conversation_key: 'alpha', total: 1, records: [turn(1)] })
  deleteThread.mockResolvedValue({ deleted: 3 })
  batchDelete.mockResolvedValue({ deleted: 4 })
  clear.mockResolvedValue({ deleted: 9 })
})

afterEach(() => wrapper?.unmount())

describe('ConversationRecordsView list', () => {
  it('loads the first page on mount and shows one row per conversation', async () => {
    wrapper = mountView()
    await flushPromises()

    expect(list).toHaveBeenCalledWith(expect.objectContaining({ page: 1, page_size: 20 }))
    const rows = wrapper.findAll('[data-test="row"]')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('alpha@example.com')
    expect(rows[0].text()).toContain('my-key')
    expect(rows[0].text()).toContain('claude-sonnet-4-5')
    expect(rows[0].text()).toContain('Anthropic')
    expect(rows[0].text()).toContain('first prompt of alpha')
    expect(rows[0].text()).toContain('last response of alpha')
    expect(rows[0].text()).toContain('3')
    // 多轮对话额外显示开始时间，单轮不显示。
    expect(rows[0].text()).toContain('admin.conversationRecords.startedAt')
    expect(rows[1].text()).not.toContain('admin.conversationRecords.startedAt')
  })

  it('shows the empty state when nothing has been recorded', async () => {
    list.mockResolvedValue({ items: [], total: 0 })
    wrapper = mountView()
    await flushPromises()
    expect(wrapper.text()).toContain('admin.conversationRecords.empty')
  })

  it('reports a load failure', async () => {
    list.mockRejectedValue(new Error('boom'))
    wrapper = mountView()
    await flushPromises()
    expect(showError).toHaveBeenCalledWith('boom')
  })

  it('sends the filters to the API and goes back to page one when searching', async () => {
    wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-testid="filter-q"]').setValue('kubernetes')
    await wrapper.get('[data-testid="filter-user"]').setValue('alice')
    await wrapper.get('[data-testid="filter-model"]').setValue('opus')
    await wrapper.get('[data-testid="search"]').trigger('click')
    await flushPromises()

    expect(list).toHaveBeenLastCalledWith(
      expect.objectContaining({ page: 1, q: 'kubernetes', user: 'alice', model: 'opus' })
    )
  })

  it('sends a time window for the preset ranges', async () => {
    wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="time-range"]').setValue('24h')
    await flushPromises()

    const query = list.mock.lastCall![0]
    expect(query.start_time).toBeTypeOf('string')
    expect(query.end_time).toBeUndefined()
    const age = Date.now() - new Date(query.start_time).getTime()
    expect(Math.abs(age - 24 * 60 * 60 * 1000)).toBeLessThan(60 * 1000)
  })

  it('paginates', async () => {
    wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-test="page"]').trigger('click')
    await flushPromises()
    expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ page: 2 }))
  })

  it('clears the filters on reset', async () => {
    wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="filter-q"]').setValue('x')
    const reset = wrapper.findAll('button').find((b) => b.text() === 'common.reset')!
    await reset.trigger('click')
    await flushPromises()

    const query = list.mock.lastCall![0]
    expect(query.q).toBeUndefined()
    expect((wrapper.get('[data-testid="filter-q"]').element as HTMLInputElement).value).toBe('')
  })
})

describe('ConversationRecordsView recording banner', () => {
  it('warns when recording is switched off and links to the settings page', async () => {
    getSettings.mockResolvedValue({ enabled: false, retention_days: 0 })
    wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('[data-testid="recording-disabled-banner"]').exists()).toBe(true)
  })

  it('stays quiet when recording is on', async () => {
    wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('[data-testid="recording-disabled-banner"]').exists()).toBe(false)
  })

  it('still works when the settings cannot be read', async () => {
    getSettings.mockRejectedValue(new Error('nope'))
    wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('[data-testid="recording-disabled-banner"]').exists()).toBe(false)
    expect(wrapper.findAll('[data-test="row"]')).toHaveLength(2)
  })
})

describe('ConversationRecordsView thread detail', () => {
  it('opens a conversation when its row is clicked and renders every turn', async () => {
    getThread.mockResolvedValue({
      conversation_key: 'alpha',
      total: 2,
      records: [
        turn(1, { system_prompt: 'You are terse.', messages: [{ role: 'user', content: 'fix the bug' }], response: 'Looking.\n[tool_use: Read] {"file_path":"/a.go"}' }),
        turn(2, {
          model: 'claude-opus-4-1',
          messages: [{ role: 'tool', content: 'package main' }, { role: 'user', content: 'thanks' }],
          response: 'You are welcome.'
        })
      ]
    })
    wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="row"]').trigger('click')
    await flushPromises()

    expect(getThread).toHaveBeenCalledWith('alpha')
    const dialog = wrapper.get('[data-testid="thread"]')
    expect(dialog.text()).toContain('alpha@example.com')
    expect(dialog.text()).toContain('You are terse.')

    expect(wrapper.findAll('[data-testid="turn"]')).toHaveLength(2)
    const messages = wrapper.findAll('[data-testid="message"]').map((m) => m.text())
    expect(messages).toHaveLength(3)
    expect(messages[0]).toContain('fix the bug')
    expect(messages[1]).toContain('package main')
    expect(messages[1]).toContain('admin.conversationRecords.roles.tool')
    expect(messages[2]).toContain('thanks')

    const responses = wrapper.findAll('[data-testid="response"]')
    expect(responses[0].text()).toContain('Looking.')
    // 工具调用单独成块，名称与参数分开展示。
    const toolCall = responses[0].get('[data-testid="tool-call"]')
    expect(toolCall.text()).toContain('Read')
    expect(toolCall.text()).toContain('{"file_path":"/a.go"}')
    expect(responses[1].text()).toContain('You are welcome.')
    // 两轮用到的模型都列出。
    expect(dialog.text()).toContain('claude-sonnet-4-5 · claude-opus-4-1')
  })

  it('tells the admin when only the latest turns are shown', async () => {
    getThread.mockResolvedValue({ conversation_key: 'alpha', total: 900, records: [turn(1)] })
    wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-test="row"]').trigger('click')
    await flushPromises()

    const notice = wrapper.get('[data-testid="thread-truncated"]')
    expect(notice.text()).toContain('"shown":1')
    expect(notice.text()).toContain('"total":900')
  })

  it('renders content as text, never as HTML', async () => {
    getThread.mockResolvedValue({
      conversation_key: 'alpha',
      total: 1,
      records: [turn(1, { messages: [{ role: 'user', content: '<img src=x onerror=alert(1)>' }], response: '<script>alert(1)</script>' })]
    })
    wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-test="row"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.find('script').exists()).toBe(false)
    expect(wrapper.get('[data-testid="thread"]').text()).toContain('<img src=x onerror=alert(1)>')
  })

  it('closes and reports an error when the conversation cannot be loaded', async () => {
    getThread.mockRejectedValue(new Error('gone'))
    wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-test="row"]').trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('gone')
    expect(wrapper.find('[data-testid="thread"]').exists()).toBe(false)
  })

  it('deletes the open conversation from the detail footer', async () => {
    wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-test="row"]').trigger('click')
    await flushPromises()

    await wrapper.get('[data-testid="detail-delete"]').trigger('click')
    await wrapper.get('[data-test="confirm"]').trigger('click')
    await flushPromises()

    expect(deleteThread).toHaveBeenCalledWith('alpha')
    expect(wrapper.find('[data-testid="thread"]').exists()).toBe(false)
    expect(list).toHaveBeenCalledTimes(2)
  })
})

describe('ConversationRecordsView deleting', () => {
  it('deletes one conversation only after confirmation', async () => {
    wrapper = mountView()
    await flushPromises()

    await wrapper.findAll('[data-testid="delete-thread"]')[1].trigger('click')
    expect(wrapper.get('[data-test="confirm"]').exists()).toBe(true)
    await wrapper.get('[data-test="cancel"]').trigger('click')
    expect(deleteThread).not.toHaveBeenCalled()

    await wrapper.findAll('[data-testid="delete-thread"]')[1].trigger('click')
    await wrapper.get('[data-test="confirm"]').trigger('click')
    await flushPromises()

    expect(deleteThread).toHaveBeenCalledWith('beta')
    expect(showSuccess).toHaveBeenCalledWith('admin.conversationRecords.deleted:{"count":3}')
    expect(list).toHaveBeenCalledTimes(2)
    // 行内删除按钮不能同时触发打开详情。
    expect(getThread).not.toHaveBeenCalled()
  })

  it('deletes the selected conversations in one request', async () => {
    wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('[data-testid="delete-selected"]').exists()).toBe(false)

    const checks = wrapper.findAll('[data-test="select"]')
    await checks[0].trigger('click')
    await checks[1].trigger('click')
    const button = wrapper.get('[data-testid="delete-selected"]')
    expect(button.text()).toContain('"count":2')

    await button.trigger('click')
    expect(wrapper.get('[data-test="confirm-message"]').text()).toContain('"count":2')
    await wrapper.get('[data-test="confirm"]').trigger('click')
    await flushPromises()

    expect(batchDelete).toHaveBeenCalledWith(['alpha', 'beta'])
    expect(wrapper.find('[data-testid="delete-selected"]').exists()).toBe(false)
  })

  it('clears everything after confirmation', async () => {
    wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-testid="clear-all"]').trigger('click')
    expect(clear).not.toHaveBeenCalled()
    await wrapper.get('[data-test="confirm"]').trigger('click')
    await flushPromises()

    expect(clear).toHaveBeenCalledTimes(1)
    expect(showSuccess).toHaveBeenCalledWith('admin.conversationRecords.deleted:{"count":9}')
  })

  it('reports a failed delete and keeps the list', async () => {
    deleteThread.mockRejectedValue(new Error('denied'))
    wrapper = mountView()
    await flushPromises()

    await wrapper.findAll('[data-testid="delete-thread"]')[0].trigger('click')
    await wrapper.get('[data-test="confirm"]').trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('denied')
    expect(wrapper.findAll('[data-test="row"]')).toHaveLength(2)
  })
})
