import { describe, expect, it } from 'vitest'

import { endpointLabel, formatDuration, roleKind, splitResponse } from '../conversationRecordsFormat'

describe('splitResponse', () => {
  it('returns nothing for empty input', () => {
    expect(splitResponse('')).toEqual([])
    expect(splitResponse('   \n  ')).toEqual([])
  })

  it('keeps plain prose as one text segment', () => {
    expect(splitResponse('Hello\n\nworld')).toEqual([{ kind: 'text', text: 'Hello\n\nworld' }])
  })

  it('pulls tool calls out of the surrounding prose, in order', () => {
    const text = 'Let me look.\n[tool_use: Read] {"file_path":"/a.go"}\nDone.\n[tool_use: Bash] {"command":"ls"}'
    expect(splitResponse(text)).toEqual([
      { kind: 'text', text: 'Let me look.' },
      { kind: 'tool', text: '[tool_use: Read] {"file_path":"/a.go"}', name: 'Read', args: '{"file_path":"/a.go"}' },
      { kind: 'text', text: 'Done.' },
      { kind: 'tool', text: '[tool_use: Bash] {"command":"ls"}', name: 'Bash', args: '{"command":"ls"}' }
    ])
  })

  it('handles a tool call without arguments', () => {
    expect(splitResponse('[tool_use: Noop]')).toEqual([{ kind: 'tool', text: '[tool_use: Noop]', name: 'Noop', args: '' }])
  })

  it('does not mistake prose that merely mentions a tool for a call', () => {
    const text = 'I used [tool_use: Read] earlier'
    expect(splitResponse(text)).toEqual([{ kind: 'text', text }])
  })
})

describe('roleKind', () => {
  it('passes known roles through and folds the rest into note', () => {
    for (const role of ['user', 'assistant', 'tool', 'system', 'note'] as const) {
      expect(roleKind(role)).toBe(role)
    }
    expect(roleKind('developer')).toBe('note')
    expect(roleKind('')).toBe('note')
  })
})

describe('formatDuration', () => {
  it('formats milliseconds and seconds', () => {
    expect(formatDuration(0)).toBe('0 ms')
    expect(formatDuration(850)).toBe('850 ms')
    expect(formatDuration(1500)).toBe('1.5 s')
    expect(formatDuration(12345)).toBe('12 s')
    expect(formatDuration(-1)).toBe('—')
    expect(formatDuration(Number.NaN)).toBe('—')
  })
})

describe('endpointLabel', () => {
  it('names the protocol of each canonical endpoint', () => {
    expect(endpointLabel('/v1/messages')).toBe('Anthropic')
    expect(endpointLabel('/v1/chat/completions')).toBe('OpenAI Chat')
    expect(endpointLabel('/v1/responses')).toBe('OpenAI Responses')
    expect(endpointLabel('/v1beta/models')).toBe('Gemini')
    expect(endpointLabel('/custom')).toBe('/custom')
    expect(endpointLabel('')).toBe('—')
  })
})
