export type ClaudeOAuthImportErrorCode =
  | 'empty'
  | 'invalidJson'
  | 'invalidObject'
  | 'missingAccessToken'
  | 'invalidRefreshToken'
  | 'invalidExpiry'
  | 'invalidScope'
  | 'expiredWithoutRefreshToken'

export class ClaudeOAuthImportError extends Error {
  constructor(public readonly code: ClaudeOAuthImportErrorCode) {
    super(code)
  }
}

export interface ClaudeOAuthCredentials extends Record<string, unknown> {
  access_token: string
  token_type: string
  refresh_token?: string
  expires_at?: number
  scope?: string
  org_uuid?: string
  account_uuid?: string
  email_address?: string
}

function isObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
}

/** Normalize one existing Claude OAuth credential without exchanging or logging tokens. */
export function parseClaudeOAuthCredentials(input: string, now = Date.now()): ClaudeOAuthCredentials {
  if (!input.trim()) throw new ClaudeOAuthImportError('empty')
  let parsed: unknown
  try {
    parsed = JSON.parse(input)
  } catch {
    throw new ClaudeOAuthImportError('invalidJson')
  }
  if (!isObject(parsed)) throw new ClaudeOAuthImportError('invalidObject')
  const source = parsed.claudeAiOauth ?? parsed.credentials ?? parsed.tokens ?? parsed
  if (!isObject(source)) throw new ClaudeOAuthImportError('invalidObject')

  const accessToken = source.access_token ?? source.accessToken
  if (typeof accessToken !== 'string' || !accessToken.trim() || /\s/.test(accessToken.trim())) {
    throw new ClaudeOAuthImportError('missingAccessToken')
  }
  const credentials: ClaudeOAuthCredentials = { access_token: accessToken.trim(), token_type: 'Bearer' }
  const refreshToken = source.refresh_token ?? source.refreshToken
  if (refreshToken != null && refreshToken !== '') {
    if (typeof refreshToken !== 'string' || /\s/.test(refreshToken.trim())) {
      throw new ClaudeOAuthImportError('invalidRefreshToken')
    }
    if (refreshToken.trim()) credentials.refresh_token = refreshToken.trim()
  }

  const expiry = source.expires_at ?? source.expiresAt
  if (expiry != null && expiry !== '') {
    let timestamp: number
    if (typeof expiry === 'number' || (typeof expiry === 'string' && /^\d+(\.\d+)?$/.test(expiry.trim()))) {
      timestamp = Number(expiry)
      if (timestamp >= 1e12) timestamp /= 1000 // Claude Code stores milliseconds.
    } else if (typeof expiry === 'string') {
      timestamp = Date.parse(expiry) / 1000
    } else {
      throw new ClaudeOAuthImportError('invalidExpiry')
    }
    if (!Number.isFinite(timestamp) || timestamp <= 0) throw new ClaudeOAuthImportError('invalidExpiry')
    credentials.expires_at = Math.floor(timestamp)
  } else if (source.expires_in != null) {
    const duration = source.expires_in
    if ((typeof duration !== 'number' && typeof duration !== 'string') || !Number.isFinite(Number(duration)) || Number(duration) <= 0) {
      throw new ClaudeOAuthImportError('invalidExpiry')
    }
    credentials.expires_at = Math.floor(now / 1000 + Number(duration))
  }
  if (credentials.expires_at != null && credentials.expires_at <= now / 1000 && !credentials.refresh_token) {
    throw new ClaudeOAuthImportError('expiredWithoutRefreshToken')
  }

  const scope = source.scope ?? source.scopes
  if (scope != null) {
    if (typeof scope === 'string') credentials.scope = scope.trim()
    else if (Array.isArray(scope) && scope.every(item => typeof item === 'string')) {
      credentials.scope = scope.map(item => item.trim()).filter(Boolean).join(' ')
    } else throw new ClaudeOAuthImportError('invalidScope')
  }

  for (const [key, alias] of [
    ['org_uuid', 'organizationUuid'], ['account_uuid', 'accountUuid'], ['email_address', 'email'],
    ['subscription_type', 'subscriptionType'], ['rate_limit_tier', 'rateLimitTier'], ['token_type', 'tokenType']
  ]) {
    const value = source[key] ?? source[alias]
    if (typeof value === 'string' && value.trim()) credentials[key] = value.trim()
  }
  return credentials
}
