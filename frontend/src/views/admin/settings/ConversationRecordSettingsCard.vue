<template>
  <div class="card" data-testid="conversation-record-settings">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <div class="flex items-center gap-2">
        <Icon name="chat" size="md" class="text-primary-500" />
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t('admin.conversationRecords.settings.title') }}
        </h2>
      </div>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
        {{ t('admin.conversationRecords.settings.description') }}
      </p>
      <p class="mt-1.5 text-xs">
        <router-link
          to="/admin/conversation-records"
          class="inline-flex items-center gap-1 text-primary-600 hover:underline dark:text-primary-400"
        >
          {{ t('admin.conversationRecords.settings.viewLink') }}
          <span aria-hidden="true">→</span>
        </router-link>
      </p>
    </div>

    <div class="space-y-5 p-6">
      <div v-if="loading" class="flex items-center gap-2 text-gray-500">
        <div class="h-4 w-4 animate-spin rounded-full border-b-2 border-primary-600"></div>
        {{ t('common.loading') }}
      </div>

      <div
        v-else-if="loadError"
        class="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-red-200 bg-red-50 p-4 dark:border-red-800 dark:bg-red-900/20"
        data-testid="conversation-record-load-error"
      >
        <p class="text-sm text-red-700 dark:text-red-300">{{ loadError }}</p>
        <button type="button" class="btn btn-secondary btn-sm" data-testid="conversation-record-retry" @click="load">
          {{ t('admin.conversationRecords.settings.retry') }}
        </button>
      </div>

      <template v-else>
        <div
          class="rounded-lg border border-amber-200 bg-amber-50 p-4 dark:border-amber-800 dark:bg-amber-900/20"
        >
          <div class="flex items-start">
            <Icon name="infoCircle" size="md" class="mt-0.5 flex-shrink-0 text-amber-500" />
            <p class="ml-3 text-sm text-amber-700 dark:text-amber-300">
              {{ t('admin.conversationRecords.settings.privacyNote') }}
            </p>
          </div>
        </div>

        <div class="flex items-center justify-between gap-4">
          <div>
            <label class="font-medium text-gray-900 dark:text-white">
              {{ t('admin.conversationRecords.settings.enabled') }}
            </label>
            <p class="text-sm text-gray-500 dark:text-gray-400">
              {{ t('admin.conversationRecords.settings.enabledHint') }}
            </p>
          </div>
          <Toggle v-model="form.enabled" data-testid="conversation-record-enabled" />
        </div>

        <div>
          <label class="input-label" for="conversation-record-retention">
            {{ t('admin.conversationRecords.settings.retentionDays') }}
          </label>
          <div class="relative max-w-xs">
            <input
              id="conversation-record-retention"
              v-model.number="form.retention_days"
              type="number"
              min="0"
              :max="MAX_RETENTION_DAYS"
              step="1"
              class="input pr-14"
              data-testid="conversation-record-retention"
            />
            <span class="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-sm text-gray-400">
              {{ t('admin.conversationRecords.settings.daysUnit') }}
            </span>
          </div>
          <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.conversationRecords.settings.retentionDaysHint') }}
          </p>
        </div>

        <div class="flex items-center justify-end gap-3 border-t border-gray-100 pt-4 dark:border-dark-700">
          <!-- 此处的修改要点本卡片的「保存」才生效，与页面其他设置项不同，容易误以为已随页面保存。 -->
          <span
            v-if="dirty"
            class="text-xs text-amber-600 dark:text-amber-400"
            data-testid="conversation-record-unsaved"
          >
            {{ t('admin.conversationRecords.settings.unsaved') }}
          </span>
          <button
            type="button"
            class="btn btn-primary btn-sm"
            data-testid="conversation-record-save"
            :disabled="saving"
            @click="save"
          >
            {{ saving ? t('common.saving') : t('common.save') }}
          </button>
        </div>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api'
import Icon from '@/components/icons/Icon.vue'
import Toggle from '@/components/common/Toggle.vue'
import { useAppStore } from '@/stores'

// 与后端 conversationRetentionDaysMax 保持一致。
const MAX_RETENTION_DAYS = 3650

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(true)
// 读取失败时不展示表单：此时的默认值并非真实配置，保存会把已开启的记录悄悄关掉。
const loadError = ref('')
const saving = ref(false)
const form = reactive({ enabled: false, retention_days: 0 })
// 最近一次从服务端读到 / 保存成功的值，用来判断表单是否有未保存的修改。
const saved = reactive({ enabled: false, retention_days: 0 })
const dirty = computed(() => form.enabled !== saved.enabled || (Number(form.retention_days) || 0) !== saved.retention_days)

function applySettings(settings: { enabled: boolean; retention_days: number }) {
  form.enabled = saved.enabled = settings.enabled
  form.retention_days = saved.retention_days = settings.retention_days
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    applySettings(await adminAPI.conversationRecords.getSettings())
  } catch (err: any) {
    loadError.value = err?.message || t('admin.conversationRecords.settings.loadFailed')
  } finally {
    loading.value = false
  }
}

async function save() {
  // v-model.number 在输入框清空时给出空字符串，按「永久保留」处理。
  const days = Number(form.retention_days) || 0
  if (!Number.isInteger(days) || days < 0 || days > MAX_RETENTION_DAYS) {
    appStore.showError(t('admin.conversationRecords.settings.retentionInvalid', { max: MAX_RETENTION_DAYS }))
    return
  }
  saving.value = true
  try {
    const updated = await adminAPI.conversationRecords.updateSettings({
      enabled: form.enabled,
      retention_days: days
    })
    applySettings(updated)
    appStore.showSuccess(t('admin.conversationRecords.settings.saved'))
  } catch (err: any) {
    appStore.showError(err?.message || t('admin.conversationRecords.settings.saveFailed'))
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>
