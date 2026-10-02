/**
 * Admin conversation records API.
 *
 * When enabled in System Settings, the gateway stores each chat turn (the new
 * user input and the model reply). Turns that belong to the same conversation
 * share a `conversation_key`, so the list shows one row per conversation and
 * the thread endpoint returns every turn of one conversation.
 */

import { apiClient } from '../client'
import type { PaginatedResponse } from '@/types'

export interface ConversationMessage {
  /** user | assistant | system | tool | note */
  role: string
  content: string
}

/** One row of the list: a whole conversation. */
export interface ConversationSummary {
  conversation_key: string
  user_id?: number
  user_email: string
  api_key_name: string
  endpoint: string
  /** Model of the most recent turn. */
  model: string
  turns: number
  first_at: string
  last_at: string
  first_prompt: string
  last_response: string
}

/** One recorded turn (a single gateway request). */
export interface ConversationTurn {
  id: number
  created_at: string
  request_id: string
  conversation_key: string
  user_id?: number
  user_email: string
  api_key_id?: number
  api_key_name: string
  group_id?: number
  endpoint: string
  model: string
  stream: boolean
  duration_ms: number
  client_ip: string
  user_agent: string
  system_prompt: string
  /** The new input of this turn only; earlier history lives in earlier turns. */
  messages: ConversationMessage[]
  response: string
}

export interface ConversationThread {
  conversation_key: string
  /** Total turns of the conversation; `records` holds only the most recent ones. */
  total: number
  records: ConversationTurn[]
}

export interface ConversationQuery {
  page?: number
  page_size?: number
  /** Fuzzy match on conversation content. */
  q?: string
  /** Fuzzy match on user email. */
  user?: string
  /** Fuzzy match on model name. */
  model?: string
  user_id?: number
  start_time?: string
  end_time?: string
}

export interface ConversationRecordSettings {
  enabled: boolean
  /** Days to keep records; 0 keeps them forever. */
  retention_days: number
}

export type ConversationListResponse = PaginatedResponse<ConversationSummary>

export async function list(params: ConversationQuery): Promise<ConversationListResponse> {
  const { data } = await apiClient.get('/admin/conversation-records', { params })
  return data
}

export async function getThread(key: string): Promise<ConversationThread> {
  const { data } = await apiClient.get(`/admin/conversation-records/threads/${encodeURIComponent(key)}`)
  return data
}

export async function deleteThread(key: string): Promise<{ deleted: number }> {
  const { data } = await apiClient.delete(`/admin/conversation-records/threads/${encodeURIComponent(key)}`)
  return data
}

export async function batchDelete(keys: string[]): Promise<{ deleted: number }> {
  const { data } = await apiClient.post('/admin/conversation-records/batch-delete', { keys })
  return data
}

export async function clear(): Promise<{ deleted: number }> {
  const { data } = await apiClient.post('/admin/conversation-records/clear')
  return data
}

export async function getSettings(): Promise<ConversationRecordSettings> {
  const { data } = await apiClient.get('/admin/conversation-records/settings')
  return data
}

export async function updateSettings(
  settings: ConversationRecordSettings
): Promise<ConversationRecordSettings> {
  const { data } = await apiClient.put('/admin/conversation-records/settings', settings)
  return data
}

export const conversationRecordsAPI = {
  list,
  getThread,
  deleteThread,
  batchDelete,
  clear,
  getSettings,
  updateSettings
}

export default conversationRecordsAPI
