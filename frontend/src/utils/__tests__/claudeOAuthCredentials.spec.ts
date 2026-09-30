import { describe, expect, it } from 'vitest'
import { ClaudeOAuthImportError, parseClaudeOAuthCredentials } from '../claudeOAuthCredentials'

const now = Date.UTC(2026, 9, 1)
const future = now / 1000 + 3600

describe('Claude OAuth credential import', () => {
  it('imports a token response without carrying unrelated configuration', () => {
    expect(parseClaudeOAuthCredentials(JSON.stringify({
      access_token: ' access-test ', refresh_token: 'refresh-test', expires_at: future,
      org_uuid: 'org-test', account_uuid: 'account-test', email_address: 'test@example.com',
      api_key: 'unrelated-secret', enable_tls_fingerprint: false
    }), now)).toEqual({
      access_token: 'access-test', refresh_token: 'refresh-test', expires_at: future,
      token_type: 'Bearer', org_uuid: 'org-test', account_uuid: 'account-test', email_address: 'test@example.com'
    })
  })

  it('imports Claude Code credentials and converts millisecond expiry to seconds', () => {
    const credentials = parseClaudeOAuthCredentials(JSON.stringify({ claudeAiOauth: {
      accessToken: 'access-test', refreshToken: 'refresh-test', expiresAt: future * 1000,
      scopes: ['user:inference', 'user:profile'], subscriptionType: 'max', rateLimitTier: 'default'
    } }), now)
    expect(credentials).toMatchObject({
      access_token: 'access-test', refresh_token: 'refresh-test', expires_at: future,
      scope: 'user:inference user:profile', subscription_type: 'max', rate_limit_tier: 'default'
    })
  })

  it.each(['credentials', 'tokens'])('accepts the %s wrapper', wrapper => {
    expect(parseClaudeOAuthCredentials(JSON.stringify({ [wrapper]: { access_token: 'test' } }), now).access_token).toBe('test')
  })

  it.each([future, String(future), future * 1000, String(future * 1000), new Date(future * 1000).toISOString()])(
    'normalizes expiration %s', expires_at => {
      expect(parseClaudeOAuthCredentials(JSON.stringify({ access_token: 'test', expires_at }), now).expires_at).toBe(future)
    }
  )

  it('converts expires_in and permits an expired token when it can be refreshed', () => {
    expect(parseClaudeOAuthCredentials('{"access_token":"test","expires_in":3600}', now).expires_at).toBe(future)
    expect(parseClaudeOAuthCredentials('{"access_token":"test","refresh_token":"refresh","expires_at":1}', now).refresh_token).toBe('refresh')
  })

  it.each([
    ['', 'empty'], ['{"access_token":"secret",', 'invalidJson'], ['[]', 'invalidObject'], ['null', 'invalidObject'],
    ['{"refresh_token":"refresh"}', 'missingAccessToken'], ['{"access_token":"  "}', 'missingAccessToken'],
    ['{"access_token":"a b"}', 'missingAccessToken'], ['{"access_token":"test","refresh_token":123}', 'invalidRefreshToken'],
    ['{"access_token":"test","expires_at":"bad-date"}', 'invalidExpiry'], ['{"access_token":"test","expires_in":true}', 'invalidExpiry'],
    ['{"access_token":"test","scopes":[123]}', 'invalidScope'], ['{"access_token":"test","expires_at":1}', 'expiredWithoutRefreshToken']
  ])('rejects invalid credentials with a safe error code (%s)', (input, code) => {
    try {
      parseClaudeOAuthCredentials(input, now)
      expect.unreachable('Expected import to fail')
    } catch (error) {
      expect(error).toBeInstanceOf(ClaudeOAuthImportError)
      expect((error as ClaudeOAuthImportError).code).toBe(code)
      expect((error as Error).message).not.toContain('secret')
    }
  })
})
