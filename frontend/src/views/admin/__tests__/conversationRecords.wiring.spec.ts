import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const here = dirname(fileURLToPath(import.meta.url))
const read = (relative: string) => readFileSync(resolve(here, relative), 'utf8')

describe('conversation records wiring', () => {
  it('adds the sidebar entry between the operation log and system settings', () => {
    const sidebar = read('../../../components/layout/AppSidebar.vue')
    const auditLogs = sidebar.indexOf("path: '/admin/audit-logs'")
    const records = sidebar.indexOf("path: '/admin/conversation-records'")
    const settings = sidebar.indexOf("visible.push({ path: '/admin/settings'")
    expect(auditLogs).toBeGreaterThan(-1)
    expect(records).toBeGreaterThan(auditLogs)
    expect(settings).toBeGreaterThan(records)
    expect(sidebar).toMatch(/path: '\/admin\/conversation-records'[^\n]*label: t\('nav\.conversationRecords'\)/)
  })

  it('registers an admin-only route', () => {
    const router = read('../../../router/index.ts')
    const block = router.slice(router.indexOf("path: '/admin/conversation-records'"))
    const route = block.slice(0, block.indexOf('\n  },'))
    expect(route).toContain("name: 'AdminConversationRecords'")
    expect(route).toContain('requiresAuth: true')
    expect(route).toContain('requiresAdmin: true')
    expect(route).toContain("titleKey: 'admin.conversationRecords.title'")
  })

  it('mounts the settings card in the Feature Switches tab', () => {
    const settings = read('../SettingsView.vue')
    expect(settings).toContain('import ConversationRecordSettingsCard from')
    const featuresTab = settings.indexOf("v-show=\"activeTab === 'features'\"")
    const card = settings.indexOf('<ConversationRecordSettingsCard />')
    const nextTab = settings.indexOf("v-show=\"activeTab === 'payment'\"")
    expect(featuresTab).toBeGreaterThan(-1)
    expect(card).toBeGreaterThan(featuresTab)
    expect(card).toBeLessThan(nextTab)
  })
})
