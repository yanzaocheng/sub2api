/**
 * Display helpers for the conversation records page.
 *
 * Replies are stored as plain text. Tool calls the model made appear on their
 * own line as `[tool_use: Name] {...args}`; the thread view renders those
 * lines differently from the prose around them.
 */

export interface ResponseSegment {
  kind: 'text' | 'tool'
  /** Prose for `text`; the whole original line for `tool`. */
  text: string
  /** Tool name (tool segments only). */
  name?: string
  /** Truncated JSON arguments (tool segments only). */
  args?: string
}

const TOOL_LINE = /^\[tool_use: ([^\]]*)\](?: (.*))?$/

export function splitResponse(text: string): ResponseSegment[] {
  const segments: ResponseSegment[] = []
  let buffer: string[] = []
  const flush = () => {
    const prose = buffer.join('\n').trim()
    if (prose) segments.push({ kind: 'text', text: prose })
    buffer = []
  }
  for (const line of (text || '').split('\n')) {
    const match = TOOL_LINE.exec(line)
    if (match) {
      flush()
      segments.push({ kind: 'tool', text: line, name: match[1], args: match[2] || '' })
    } else {
      buffer.push(line)
    }
  }
  flush()
  return segments
}

export type RoleKind = 'user' | 'assistant' | 'tool' | 'system' | 'note'

/** Normalises a stored message role to one of the kinds the UI knows how to style. */
export function roleKind(role: string): RoleKind {
  switch (role) {
    case 'user':
    case 'assistant':
    case 'tool':
    case 'system':
    case 'note':
      return role
    default:
      return 'note'
  }
}

export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return '—'
  if (ms < 1000) return `${Math.round(ms)} ms`
  return `${(ms / 1000).toFixed(ms < 10000 ? 1 : 0)} s`
}

/** Short label for the protocol a request came in on, from its canonical endpoint. */
export function endpointLabel(endpoint: string): string {
  switch (endpoint) {
    case '/v1/messages':
      return 'Anthropic'
    case '/v1/chat/completions':
      return 'OpenAI Chat'
    case '/v1/responses':
      return 'OpenAI Responses'
    case '/v1beta/models':
      return 'Gemini'
    default:
      return endpoint || '—'
  }
}
