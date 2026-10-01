import { baseCompile } from '@intlify/message-compiler'
import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zh from '../locales/zh'

function collectMessages(value: unknown, prefix = ''): Array<[string, string]> {
  if (typeof value === 'string') return [[prefix, value]]
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return []
  return Object.entries(value as Record<string, unknown>).flatMap(([key, child]) =>
    collectMessages(child, prefix ? `${prefix}.${key}` : key)
  )
}

// Production builds compile messages with the JIT compiler and t() throws on a syntax error,
// which aborts rendering of the whole component. Literal braces must be written as {'{'} / {'}'}.
function findSyntaxErrors(messages: Record<string, unknown>): string[] {
  const failures: string[] = []
  for (const [key, message] of collectMessages(messages)) {
    try {
      baseCompile(message, {
        jit: true,
        onError: (error) => {
          throw error
        }
      })
    } catch (error) {
      failures.push(`${key}: ${(error as Error).message}`)
    }
  }
  return failures
}

describe('locale message syntax', () => {
  it.each([
    ['en', en],
    ['zh', zh]
  ])('compiles every %s message with the production JIT compiler', (_locale, messages) => {
    expect(findSyntaxErrors(messages as Record<string, unknown>)).toEqual([])
  })
})
