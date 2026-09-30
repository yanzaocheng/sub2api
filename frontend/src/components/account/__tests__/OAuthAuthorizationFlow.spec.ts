import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import OAuthAuthorizationFlow from '../OAuthAuthorizationFlow.vue'

vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn() }) }))

function mountFlow(extra = {}) {
  return mount(OAuthAuthorizationFlow, {
    props: { addMethod: 'oauth', platform: 'anthropic', showOauthCredentialsOption: true, ...extra },
    global: { stubs: { Icon: true } }
  })
}

describe('Claude OAuth credential authorization option', () => {
  it('shows a third method and submits the pasted JSON without generating a URL', async () => {
    const wrapper = mountFlow()
    expect(wrapper.findAll('input[type="radio"]')).toHaveLength(3)
    await wrapper.get('input[value="oauth_credentials"]').setValue()
    const input = wrapper.get('#claude-oauth-credentials')
    const button = wrapper.findAll('button').find(item => item.text().includes('credentialsImportButton'))!
    expect(button.attributes('disabled')).toBeDefined()
    await input.setValue('  {"access_token":"test","refresh_token":"refresh"}  ')
    await button.trigger('click')
    expect(wrapper.emitted('import-oauth-credentials')).toEqual([['{"access_token":"test","refresh_token":"refresh"}']])
    expect(wrapper.emitted('generate-url')).toBeUndefined()
    expect(wrapper.text()).not.toContain('admin.accounts.oauth.step1GenerateUrl')
    wrapper.unmount()
  })

  it('displays import errors and clears credentials when reset', async () => {
    const wrapper = mountFlow({ error: 'Invalid credentials' })
    await wrapper.get('input[value="oauth_credentials"]').setValue()
    await wrapper.get('#claude-oauth-credentials').setValue('{"access_token":"test"}')
    expect(wrapper.get('[role="alert"]').text()).toBe('Invalid credentials')
    wrapper.vm.reset()
    await wrapper.vm.$nextTick()
    await wrapper.get('input[value="oauth_credentials"]').setValue()
    expect((wrapper.get('#claude-oauth-credentials').element as HTMLTextAreaElement).value).toBe('')
    await wrapper.setProps({ loading: true })
    expect(wrapper.get('#claude-oauth-credentials').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('keeps the credential option opt-in for other flows', () => {
    const wrapper = mountFlow({ showOauthCredentialsOption: false })
    expect(wrapper.find('input[value="oauth_credentials"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
