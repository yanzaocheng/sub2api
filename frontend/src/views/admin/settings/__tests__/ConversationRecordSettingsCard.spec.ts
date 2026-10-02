import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import ConversationRecordSettingsCard from '../ConversationRecordSettingsCard.vue'

const { getSettings, updateSettings, showError, showSuccess } = vi.hoisted(() => ({
  getSettings: vi.fn(),
  updateSettings: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn()
}))

vi.mock('@/api', () => ({
  adminAPI: { conversationRecords: { getSettings, updateSettings } }
}))
vi.mock('@/stores', () => ({ useAppStore: () => ({ showError, showSuccess }) }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key)
  })
}))

function mountCard() {
  return mount(ConversationRecordSettingsCard, {
    global: {
      stubs: {
        Icon: true,
        'router-link': { template: '<a data-test="link"><slot /></a>' },
        Toggle: {
          props: ['modelValue'],
          emits: ['update:modelValue'],
          template: '<button data-test="toggle" :data-on="modelValue" @click="$emit(\'update:modelValue\', !modelValue)" />'
        }
      }
    }
  })
}

let wrapper: ReturnType<typeof mountCard>

beforeEach(() => {
  vi.clearAllMocks()
  getSettings.mockResolvedValue({ enabled: false, retention_days: 0 })
  updateSettings.mockImplementation(async (s: unknown) => s)
})

afterEach(() => wrapper?.unmount())

describe('ConversationRecordSettingsCard', () => {
  it('loads the saved settings', async () => {
    getSettings.mockResolvedValue({ enabled: true, retention_days: 30 })
    wrapper = mountCard()
    expect(wrapper.text()).toContain('common.loading')
    await flushPromises()

    expect(wrapper.get('[data-test="toggle"]').attributes('data-on')).toBe('true')
    expect((wrapper.get('[data-testid="conversation-record-retention"]').element as HTMLInputElement).value).toBe('30')
  })

  it('starts switched off when nothing was configured', async () => {
    wrapper = mountCard()
    await flushPromises()
    expect(wrapper.get('[data-test="toggle"]').attributes('data-on')).toBe('false')
  })

  it('warns that content is stored in plain text and links to the records page', async () => {
    wrapper = mountCard()
    await flushPromises()
    expect(wrapper.text()).toContain('admin.conversationRecords.settings.privacyNote')
    expect(wrapper.find('[data-test="link"]').exists()).toBe(true)
  })

  it('saves the switch and the retention days', async () => {
    wrapper = mountCard()
    await flushPromises()

    await wrapper.get('[data-test="toggle"]').trigger('click')
    await wrapper.get('[data-testid="conversation-record-retention"]').setValue('14')
    await wrapper.get('[data-testid="conversation-record-save"]').trigger('click')
    await flushPromises()

    expect(updateSettings).toHaveBeenCalledWith({ enabled: true, retention_days: 14 })
    expect(showSuccess).toHaveBeenCalledWith('admin.conversationRecords.settings.saved')
  })

  // 本卡片的开关要点自己的「保存」才生效，与页面其他设置项不同；有改动时必须有明显提示。
  it('flags unsaved changes until they are saved', async () => {
    wrapper = mountCard()
    await flushPromises()
    expect(wrapper.find('[data-testid="conversation-record-unsaved"]').exists()).toBe(false)

    await wrapper.get('[data-test="toggle"]').trigger('click')
    expect(wrapper.find('[data-testid="conversation-record-unsaved"]').exists()).toBe(true)

    // 改回原值后不再算未保存。
    await wrapper.get('[data-test="toggle"]').trigger('click')
    expect(wrapper.find('[data-testid="conversation-record-unsaved"]').exists()).toBe(false)

    await wrapper.get('[data-testid="conversation-record-retention"]').setValue('7')
    expect(wrapper.find('[data-testid="conversation-record-unsaved"]').exists()).toBe(true)
    await wrapper.get('[data-testid="conversation-record-save"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="conversation-record-unsaved"]').exists()).toBe(false)
  })

  it('treats an emptied retention field as keep-forever', async () => {
    wrapper = mountCard()
    await flushPromises()
    await wrapper.get('[data-testid="conversation-record-retention"]').setValue('')
    await wrapper.get('[data-testid="conversation-record-save"]').trigger('click')
    await flushPromises()
    expect(updateSettings).toHaveBeenCalledWith({ enabled: false, retention_days: 0 })
  })

  it.each(['-1', '3651', '1.5'])('refuses an invalid retention of %s without calling the API', async (value) => {
    wrapper = mountCard()
    await flushPromises()
    await wrapper.get('[data-testid="conversation-record-retention"]').setValue(value)
    await wrapper.get('[data-testid="conversation-record-save"]').trigger('click')
    await flushPromises()

    expect(updateSettings).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith('admin.conversationRecords.settings.retentionInvalid:{"max":3650}')
  })

  it('reports a failed save', async () => {
    updateSettings.mockRejectedValue(new Error('server said no'))
    wrapper = mountCard()
    await flushPromises()
    await wrapper.get('[data-testid="conversation-record-save"]').trigger('click')
    await flushPromises()
    expect(showError).toHaveBeenCalledWith('server said no')
    expect(showSuccess).not.toHaveBeenCalled()
  })

  // 读取失败时的默认值并非真实配置：若仍给出可保存的表单，管理员一点保存就会把已开启的记录悄悄关掉。
  it('does not offer an editable form when the settings cannot be loaded', async () => {
    getSettings.mockRejectedValue(new Error('offline'))
    wrapper = mountCard()
    await flushPromises()

    expect(wrapper.get('[data-testid="conversation-record-load-error"]').text()).toContain('offline')
    expect(wrapper.find('[data-testid="conversation-record-save"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="toggle"]').exists()).toBe(false)
    // 加载失败不弹 toast，避免干扰宿主页面。
    expect(showError).not.toHaveBeenCalled()
  })

  it('recovers when the retry succeeds', async () => {
    getSettings.mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce({ enabled: true, retention_days: 5 })
    wrapper = mountCard()
    await flushPromises()

    await wrapper.get('[data-testid="conversation-record-retry"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="conversation-record-load-error"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="toggle"]').attributes('data-on')).toBe('true')
    expect((wrapper.get('[data-testid="conversation-record-retention"]').element as HTMLInputElement).value).toBe('5')
  })
})
