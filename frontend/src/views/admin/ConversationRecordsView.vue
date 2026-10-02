<template>
  <AppLayout>
    <TablePageLayout>
      <!-- Filters -->
      <template #filters>
        <div class="card p-4 sm:p-6">
          <!-- 未开启保存时的提示：已保存的内容仍可查看与清理 -->
          <div
            v-if="recordingDisabled"
            class="mb-4 flex flex-wrap items-center justify-between gap-3 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 dark:border-amber-800 dark:bg-amber-900/20"
            data-testid="recording-disabled-banner"
          >
            <div class="flex items-start gap-2">
              <Icon name="infoCircle" size="md" class="mt-0.5 flex-shrink-0 text-amber-500" />
              <div class="text-sm text-amber-700 dark:text-amber-300">
                <div class="font-medium">{{ t('admin.conversationRecords.disabledBanner.title') }}</div>
                <div>{{ t('admin.conversationRecords.disabledBanner.description') }}</div>
              </div>
            </div>
            <router-link to="/admin/settings" class="btn btn-secondary btn-sm">
              {{ t('admin.conversationRecords.disabledBanner.action') }}
            </router-link>
          </div>

          <div class="flex flex-wrap items-end justify-between gap-4">
            <div class="flex flex-1 flex-wrap items-end gap-4">
              <div class="w-full sm:w-auto sm:min-w-[240px]">
                <label class="input-label">{{ t('admin.conversationRecords.filters.q') }}</label>
                <div class="relative">
                  <Icon
                    name="search"
                    size="md"
                    class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-gray-400"
                  />
                  <input
                    v-model.trim="filters.q"
                    type="text"
                    class="input pl-10"
                    data-testid="filter-q"
                    :placeholder="t('admin.conversationRecords.filters.qPlaceholder')"
                    @keyup.enter="search"
                  />
                </div>
              </div>

              <div class="w-full sm:w-auto sm:min-w-[200px]">
                <label class="input-label">{{ t('admin.conversationRecords.filters.user') }}</label>
                <input
                  v-model.trim="filters.user"
                  type="text"
                  class="input"
                  data-testid="filter-user"
                  :placeholder="t('admin.conversationRecords.filters.userPlaceholder')"
                  @keyup.enter="search"
                />
              </div>

              <div class="w-full sm:w-auto sm:min-w-[180px]">
                <label class="input-label">{{ t('admin.conversationRecords.filters.model') }}</label>
                <input
                  v-model.trim="filters.model"
                  type="text"
                  class="input"
                  data-testid="filter-model"
                  :placeholder="t('admin.conversationRecords.filters.modelPlaceholder')"
                  @keyup.enter="search"
                />
              </div>

              <div class="w-full sm:w-auto sm:min-w-[170px]">
                <label class="input-label">{{ t('admin.dashboard.timeRange') }}</label>
                <Select
                  :model-value="timeRange"
                  :options="timeRangeOptions"
                  @update:model-value="handleTimeRangeChange"
                />
              </div>
            </div>

            <div class="flex w-full flex-wrap items-center justify-end gap-3 sm:w-auto">
              <button type="button" class="btn btn-primary" :disabled="loading" data-testid="search" @click="search">
                {{ t('common.search') }}
              </button>
              <button type="button" class="btn btn-secondary" :disabled="loading" @click="resetFilters">
                {{ t('common.reset') }}
              </button>
              <button
                v-if="selectedKeys.length > 0"
                type="button"
                class="btn btn-danger"
                data-testid="delete-selected"
                @click="bulkDeleteVisible = true"
              >
                <Icon name="trash" size="sm" class="mr-1.5" />
                {{ t('admin.conversationRecords.deleteSelected', { count: selectedKeys.length }) }}
              </button>
              <button type="button" class="btn btn-danger" data-testid="clear-all" @click="clearVisible = true">
                {{ t('admin.conversationRecords.clearAll') }}
              </button>
            </div>
          </div>
        </div>
      </template>

      <!-- Table -->
      <template #table>
        <DataTable
          :columns="columns"
          :data="items"
          :loading="loading"
          row-key="conversation_key"
          selectable
          clickable-rows
          :selected-keys="selectedKeys"
          :selection-label="selectionLabel"
          @update:selected-keys="onSelectionChange"
          @row-click="(row: ConversationSummary) => openThread(row.conversation_key)"
        >
          <template #cell-last_at="{ row }">
            <div class="whitespace-nowrap">
              <div class="text-gray-700 dark:text-gray-200">{{ formatTime(row.last_at) }}</div>
              <div v-if="row.turns > 1" class="mt-0.5 text-xs text-gray-400">
                {{ t('admin.conversationRecords.startedAt', { time: formatTime(row.first_at) }) }}
              </div>
            </div>
          </template>

          <template #cell-user="{ row }">
            <div class="min-w-0 max-w-[220px]">
              <div class="truncate font-medium text-gray-900 dark:text-white" :title="row.user_email">
                {{ row.user_email || '—' }}
              </div>
              <div class="mt-0.5 truncate text-xs text-gray-400" :title="row.api_key_name">
                {{ row.api_key_name }}
              </div>
            </div>
          </template>

          <template #cell-model="{ row }">
            <div class="min-w-0 max-w-[200px]">
              <div class="truncate font-mono text-sm text-gray-800 dark:text-gray-200" :title="row.model">
                {{ row.model || '—' }}
              </div>
              <div class="mt-0.5 text-xs text-gray-400">{{ endpointLabel(row.endpoint) }}</div>
            </div>
          </template>

          <template #cell-conversation="{ row }">
            <div class="min-w-0 max-w-[260px] 2xl:max-w-md">
              <div class="line-clamp-2 break-words text-gray-900 dark:text-gray-100">
                {{ row.first_prompt || '—' }}
              </div>
              <div class="mt-1 line-clamp-1 break-words text-xs text-gray-400">
                {{ row.last_response }}
              </div>
            </div>
          </template>

          <template #cell-turns="{ value }">
            <span
              class="inline-flex min-w-[2rem] justify-center rounded-full bg-gray-100 px-2 py-0.5 text-xs font-semibold text-gray-700 dark:bg-dark-700 dark:text-gray-200"
            >
              {{ value }}
            </span>
          </template>

          <template #cell-actions="{ row }">
            <div class="flex items-center gap-3 whitespace-nowrap">
              <button
                type="button"
                class="inline-flex items-center gap-1 font-medium text-primary-600 transition-colors hover:text-primary-700 dark:text-primary-400 dark:hover:text-primary-300"
                data-testid="open-thread"
                @click.stop="openThread(row.conversation_key)"
              >
                <Icon name="eye" size="sm" />
                {{ t('admin.conversationRecords.view') }}
              </button>
              <button
                type="button"
                class="inline-flex items-center gap-1 font-medium text-red-600 transition-colors hover:text-red-700 dark:text-red-400 dark:hover:text-red-300"
                data-testid="delete-thread"
                @click.stop="deleteKey = row.conversation_key"
              >
                <Icon name="trash" size="sm" />
                {{ t('common.delete') }}
              </button>
            </div>
          </template>

          <template #empty>
            <div class="flex flex-col items-center py-8">
              <Icon name="chat" size="xl" class="mb-4 h-12 w-12 text-gray-300 dark:text-dark-600" />
              <p class="text-sm font-medium text-gray-500 dark:text-gray-400">
                {{ t('admin.conversationRecords.empty') }}
              </p>
            </div>
          </template>
        </DataTable>
      </template>

      <!-- Pagination -->
      <template #pagination>
        <Pagination
          v-if="total > 0"
          :total="total"
          :page="page"
          :page-size="pageSize"
          @update:page="onPageChange"
          @update:pageSize="onPageSizeChange"
        />
      </template>
    </TablePageLayout>

    <!-- Conversation detail -->
    <BaseDialog
      :show="threadVisible"
      :title="t('admin.conversationRecords.detail.title')"
      width="wide"
      :close-on-click-outside="true"
      @close="closeThread"
    >
      <div v-if="threadLoading" class="flex items-center justify-center py-16">
        <div class="flex flex-col items-center gap-3">
          <div class="h-8 w-8 animate-spin rounded-full border-b-2 border-primary-600"></div>
          <div class="text-sm font-medium text-gray-500 dark:text-gray-400">{{ t('common.loading') }}</div>
        </div>
      </div>

      <div v-else-if="thread && firstTurn" class="space-y-5 py-2" data-testid="thread">
        <!-- Who / when / which model -->
        <div class="rounded-2xl border border-gray-200 bg-gray-50/60 p-5 dark:border-dark-700 dark:bg-dark-900/60">
          <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
            <span class="break-all text-base font-semibold text-gray-900 dark:text-white">
              {{ firstTurn.user_email || '—' }}
            </span>
            <span v-if="firstTurn.api_key_name" class="text-sm text-gray-400">{{ firstTurn.api_key_name }}</span>
          </div>
          <div class="mt-2 flex flex-wrap items-center gap-x-5 gap-y-1.5 text-xs text-gray-500 dark:text-gray-400">
            <span class="inline-flex items-center gap-1.5">
              <Icon name="clock" size="xs" />
              {{ formatTime(firstTurn.created_at) }} – {{ formatTime(lastTurn?.created_at || firstTurn.created_at) }}
            </span>
            <span>{{ t('admin.conversationRecords.detail.turns', { count: thread.total }) }}</span>
            <span class="font-mono">{{ modelsUsed.join(' · ') }}</span>
            <span>{{ endpointLabel(firstTurn.endpoint) }}</span>
            <span v-if="firstTurn.client_ip" class="font-mono">{{ firstTurn.client_ip }}</span>
          </div>
          <div
            v-if="thread.total > thread.records.length"
            class="mt-3 rounded-lg bg-amber-50 px-3 py-2 text-xs text-amber-700 dark:bg-amber-900/20 dark:text-amber-300"
            data-testid="thread-truncated"
          >
            {{ t('admin.conversationRecords.detail.truncated', { shown: thread.records.length, total: thread.total }) }}
          </div>
        </div>

        <!-- System prompt -->
        <details v-if="systemPrompt" class="rounded-xl bg-gray-50 p-4 dark:bg-dark-900">
          <summary class="cursor-pointer text-xs font-bold uppercase tracking-wider text-gray-400">
            {{ t('admin.conversationRecords.detail.systemPrompt') }}
          </summary>
          <p class="mt-2 whitespace-pre-wrap break-words text-sm text-gray-600 dark:text-gray-300">{{ systemPrompt }}</p>
        </details>

        <!-- Turns, oldest first -->
        <div class="space-y-6">
          <section v-for="turn in thread.records" :key="turn.id" class="space-y-2" data-testid="turn">
            <div class="flex flex-wrap items-center gap-x-3 text-[11px] text-gray-400">
              <span>{{ formatTime(turn.created_at) }}</span>
              <span class="font-mono">{{ turn.model }}</span>
              <span>{{ formatDuration(turn.duration_ms) }}</span>
              <span v-if="turn.stream">{{ t('admin.conversationRecords.detail.stream') }}</span>
            </div>

            <div
              v-for="(message, index) in turn.messages"
              :key="`${turn.id}-${index}`"
              :class="bubbleClass(message.role)"
              data-testid="message"
            >
              <div v-if="roleKind(message.role) !== 'user'" :class="roleLabelClass(message.role)">
                {{ t(`admin.conversationRecords.roles.${roleKind(message.role)}`) }}
              </div>
              <div class="whitespace-pre-wrap break-words">{{ message.content }}</div>
            </div>

            <div :class="bubbleClass('assistant')" data-testid="response">
              <div :class="roleLabelClass('assistant')">{{ t('admin.conversationRecords.roles.assistant') }}</div>
              <template v-for="(segment, index) in splitResponse(turn.response)" :key="index">
                <div v-if="segment.kind === 'text'" class="whitespace-pre-wrap break-words">{{ segment.text }}</div>
                <div
                  v-else
                  class="my-1 break-all rounded-md bg-white/70 px-2 py-1 font-mono text-xs text-gray-600 dark:bg-dark-900/60 dark:text-gray-300"
                  data-testid="tool-call"
                >
                  <span class="font-semibold text-primary-600 dark:text-primary-400">{{ segment.name }}</span>
                  <span v-if="segment.args" class="ml-1 text-gray-400">{{ segment.args }}</span>
                </div>
              </template>
            </div>
          </section>
        </div>
      </div>

      <template #footer>
        <button
          v-if="thread"
          type="button"
          class="btn btn-danger mr-auto"
          data-testid="detail-delete"
          @click="deleteKey = thread.conversation_key"
        >
          {{ t('admin.conversationRecords.detail.delete') }}
        </button>
        <button type="button" class="btn btn-secondary" @click="closeThread">{{ t('common.close') }}</button>
      </template>
    </BaseDialog>

    <!-- Custom time range -->
    <BaseDialog
      :show="showCustomTimeRangeDialog"
      :title="t('admin.ops.timeRange.custom')"
      width="narrow"
      @close="handleCustomTimeRangeCancel"
    >
      <div class="space-y-4 py-2">
        <div>
          <label class="input-label">{{ t('admin.ops.customTimeRange.startTime') }}</label>
          <input v-model="customStartTimeInput" type="datetime-local" class="input" />
        </div>
        <div>
          <label class="input-label">{{ t('admin.ops.customTimeRange.endTime') }}</label>
          <input v-model="customEndTimeInput" type="datetime-local" class="input" />
        </div>
      </div>
      <template #footer>
        <button type="button" class="btn btn-secondary" @click="handleCustomTimeRangeCancel">
          {{ t('common.cancel') }}
        </button>
        <button
          type="button"
          class="btn btn-primary"
          :disabled="!customStartTimeInput || !customEndTimeInput"
          @click="handleCustomTimeRangeConfirm"
        >
          {{ t('common.confirm') }}
        </button>
      </template>
    </BaseDialog>

    <ConfirmDialog
      :show="deleteKey !== ''"
      :title="t('admin.conversationRecords.deleteConfirm.title')"
      :message="t('admin.conversationRecords.deleteConfirm.message')"
      :confirm-text="t('common.delete')"
      :cancel-text="t('common.cancel')"
      danger
      @confirm="confirmDeleteThread"
      @cancel="deleteKey = ''"
    />

    <ConfirmDialog
      :show="bulkDeleteVisible"
      :title="t('admin.conversationRecords.bulkDeleteConfirm.title')"
      :message="t('admin.conversationRecords.bulkDeleteConfirm.message', { count: selectedKeys.length })"
      :confirm-text="t('common.delete')"
      :cancel-text="t('common.cancel')"
      danger
      @confirm="confirmBulkDelete"
      @cancel="bulkDeleteVisible = false"
    />

    <ConfirmDialog
      :show="clearVisible"
      :title="t('admin.conversationRecords.clearConfirm.title')"
      :message="t('admin.conversationRecords.clearConfirm.message')"
      :confirm-text="t('admin.conversationRecords.clearAll')"
      :cancel-text="t('common.cancel')"
      danger
      @confirm="confirmClear"
      @cancel="clearVisible = false"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { ConversationSummary, ConversationThread } from '@/api/admin'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import type { Column } from '@/components/common/types'
import Pagination from '@/components/common/Pagination.vue'
import Select from '@/components/common/Select.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores'
import { endpointLabel, formatDuration, roleKind, splitResponse } from './conversationRecordsFormat'

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(false)
const items = ref<ConversationSummary[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const selectedKeys = ref<string[]>([])

const filters = reactive({ q: '', user: '', model: '' })

// 保存开关状态，仅用于在未开启时给出提示；读取失败不影响列表使用。
const recordingEnabled = ref<boolean | null>(null)
const recordingDisabled = computed(() => recordingEnabled.value === false)

async function loadRecordingState() {
  try {
    recordingEnabled.value = (await adminAPI.conversationRecords.getSettings()).enabled
  } catch {
    recordingEnabled.value = null
  }
}

// ---- 时间范围：预设窗口 + 自定义起止（与操作日志页一致）----
const timeRange = ref('')
const customStartTime = ref('')
const customEndTime = ref('')
const showCustomTimeRangeDialog = ref(false)
const customStartTimeInput = ref('')
const customEndTimeInput = ref('')

const TIME_RANGE_MINUTES: Record<string, number> = {
  '30m': 30,
  '1h': 60,
  '6h': 6 * 60,
  '24h': 24 * 60,
  '7d': 7 * 24 * 60,
  '30d': 30 * 24 * 60
}

const timeRangeOptions = computed(() => [
  { value: '', label: t('admin.conversationRecords.filters.all') },
  { value: '30m', label: t('admin.ops.timeRange.30m') },
  { value: '1h', label: t('admin.ops.timeRange.1h') },
  { value: '6h', label: t('admin.ops.timeRange.6h') },
  { value: '24h', label: t('admin.ops.timeRange.24h') },
  { value: '7d', label: t('admin.ops.timeRange.7d') },
  { value: '30d', label: t('admin.ops.timeRange.30d') },
  {
    value: 'custom',
    label:
      timeRange.value === 'custom' && customStartTime.value && customEndTime.value
        ? `${t('admin.ops.timeRange.custom')} (${formatCustomTimeRangeLabel(customStartTime.value, customEndTime.value)})`
        : t('admin.ops.timeRange.custom')
  }
])

function formatCustomTimeRangeLabel(startTime: string, endTime: string): string {
  const fmt = (raw: string) => {
    const d = new Date(raw)
    if (Number.isNaN(d.getTime())) return raw
    const pad = (n: number) => String(n).padStart(2, '0')
    return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
  }
  return `${fmt(startTime)} ~ ${fmt(endTime)}`
}

function toDatetimeLocal(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function handleTimeRangeChange(val: string | number | boolean | null) {
  const value = String(val ?? '')
  if (value === 'custom') {
    const now = new Date()
    customStartTimeInput.value = customStartTime.value || toDatetimeLocal(new Date(now.getTime() - 60 * 60 * 1000))
    customEndTimeInput.value = customEndTime.value || toDatetimeLocal(now)
    showCustomTimeRangeDialog.value = true
    return
  }
  timeRange.value = value
  search()
}

function handleCustomTimeRangeConfirm() {
  if (!customStartTimeInput.value || !customEndTimeInput.value) return
  customStartTime.value = customStartTimeInput.value
  customEndTime.value = customEndTimeInput.value
  timeRange.value = 'custom'
  showCustomTimeRangeDialog.value = false
  search()
}

function handleCustomTimeRangeCancel() {
  showCustomTimeRangeDialog.value = false
}

function toRFC3339(local: string): string | undefined {
  if (!local) return undefined
  const d = new Date(local)
  if (Number.isNaN(d.getTime())) return undefined
  return d.toISOString()
}

function buildTimeRangeQuery(): { start_time?: string; end_time?: string } {
  if (timeRange.value === 'custom') {
    return { start_time: toRFC3339(customStartTime.value), end_time: toRFC3339(customEndTime.value) }
  }
  const minutes = TIME_RANGE_MINUTES[timeRange.value]
  if (!minutes) return {}
  return { start_time: new Date(Date.now() - minutes * 60 * 1000).toISOString() }
}

// ---- 列表 ----
const columns = computed<Column[]>(() => [
  { key: 'last_at', label: t('admin.conversationRecords.columns.lastActive') },
  { key: 'user', label: t('admin.conversationRecords.columns.user') },
  { key: 'model', label: t('admin.conversationRecords.columns.model') },
  { key: 'turns', label: t('admin.conversationRecords.columns.turns') },
  { key: 'conversation', label: t('admin.conversationRecords.columns.conversation') },
  { key: 'actions', label: t('common.actions') }
])

function selectionLabel(row: ConversationSummary): string {
  return t('admin.conversationRecords.selectRow', { user: row.user_email })
}

function onSelectionChange(keys: Array<string | number>) {
  selectedKeys.value = keys.map(String)
}

function buildQuery() {
  return {
    page: page.value,
    page_size: pageSize.value,
    q: filters.q || undefined,
    user: filters.user || undefined,
    model: filters.model || undefined,
    ...buildTimeRangeQuery()
  }
}

async function fetchList() {
  loading.value = true
  try {
    const res = await adminAPI.conversationRecords.list(buildQuery())
    items.value = res.items
    total.value = res.total
  } catch (err: any) {
    appStore.showError(err?.message || t('admin.conversationRecords.loadFailed'))
  } finally {
    loading.value = false
  }
}

function search() {
  page.value = 1
  selectedKeys.value = []
  fetchList()
}

function resetFilters() {
  filters.q = ''
  filters.user = ''
  filters.model = ''
  timeRange.value = ''
  customStartTime.value = ''
  customEndTime.value = ''
  search()
}

function onPageChange(p: number) {
  page.value = p
  fetchList()
}

function onPageSizeChange(ps: number) {
  pageSize.value = ps
  page.value = 1
  fetchList()
}

// ---- 对话详情 ----
const threadVisible = ref(false)
const threadLoading = ref(false)
const thread = ref<ConversationThread | null>(null)

const firstTurn = computed(() => thread.value?.records[0] ?? null)
const lastTurn = computed(() => {
  const records = thread.value?.records ?? []
  return records.length ? records[records.length - 1] : null
})
const modelsUsed = computed(() => {
  const seen: string[] = []
  for (const turn of thread.value?.records ?? []) {
    if (turn.model && !seen.includes(turn.model)) seen.push(turn.model)
  }
  return seen
})
// 系统提示词通常每轮相同，取第一个非空的即可。
const systemPrompt = computed(() => thread.value?.records.find((turn) => turn.system_prompt)?.system_prompt ?? '')

async function openThread(key: string) {
  threadVisible.value = true
  threadLoading.value = true
  thread.value = null
  try {
    thread.value = await adminAPI.conversationRecords.getThread(key)
  } catch (err: any) {
    appStore.showError(err?.message || t('admin.conversationRecords.loadFailed'))
    threadVisible.value = false
  } finally {
    threadLoading.value = false
  }
}

function closeThread() {
  threadVisible.value = false
}

function bubbleClass(role: string): string {
  const base = 'rounded-2xl px-4 py-2.5 text-sm leading-relaxed '
  switch (roleKind(role)) {
    case 'user':
      return base + 'ml-auto w-fit max-w-[85%] rounded-tr-sm bg-primary-50 text-gray-900 dark:bg-primary-900/20 dark:text-gray-100'
    case 'assistant':
      return base + 'mr-auto w-fit max-w-[90%] rounded-tl-sm bg-gray-100 text-gray-900 dark:bg-dark-700 dark:text-gray-100'
    case 'tool':
      return base + 'mr-auto w-fit max-w-[90%] border border-dashed border-gray-300 font-mono text-xs text-gray-500 dark:border-dark-600 dark:text-gray-400'
    default:
      return base + 'mx-auto w-fit max-w-[90%] bg-gray-50 text-xs text-gray-500 dark:bg-dark-900 dark:text-gray-400'
  }
}

function roleLabelClass(role: string): string {
  const base = 'mb-0.5 text-[10px] font-bold uppercase tracking-wider '
  return base + (roleKind(role) === 'assistant' ? 'text-primary-600 dark:text-primary-400' : 'text-gray-400')
}

// ---- 删除 ----
const deleteKey = ref('')
const bulkDeleteVisible = ref(false)
const clearVisible = ref(false)

async function confirmDeleteThread() {
  const key = deleteKey.value
  deleteKey.value = ''
  if (!key) return
  try {
    const res = await adminAPI.conversationRecords.deleteThread(key)
    appStore.showSuccess(t('admin.conversationRecords.deleted', { count: res.deleted }))
    if (thread.value?.conversation_key === key) closeThread()
    selectedKeys.value = selectedKeys.value.filter((k) => k !== key)
    await fetchList()
  } catch (err: any) {
    appStore.showError(err?.message || t('admin.conversationRecords.deleteFailed'))
  }
}

async function confirmBulkDelete() {
  const keys = [...selectedKeys.value]
  bulkDeleteVisible.value = false
  if (keys.length === 0) return
  try {
    const res = await adminAPI.conversationRecords.batchDelete(keys)
    appStore.showSuccess(t('admin.conversationRecords.deleted', { count: res.deleted }))
    selectedKeys.value = []
    await fetchList()
  } catch (err: any) {
    appStore.showError(err?.message || t('admin.conversationRecords.deleteFailed'))
  }
}

async function confirmClear() {
  clearVisible.value = false
  try {
    const res = await adminAPI.conversationRecords.clear()
    appStore.showSuccess(t('admin.conversationRecords.deleted', { count: res.deleted }))
    selectedKeys.value = []
    page.value = 1
    await fetchList()
  } catch (err: any) {
    appStore.showError(err?.message || t('admin.conversationRecords.deleteFailed'))
  }
}

function formatTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString()
}

onMounted(() => {
  loadRecordingState()
  fetchList()
})
</script>
