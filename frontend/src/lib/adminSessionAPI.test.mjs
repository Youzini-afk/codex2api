import assert from 'node:assert/strict'
import test from 'node:test'
import { api, AdminAPIError } from '../api.ts'

test('session login preserves throttle errors without retrying authentication', async t => {
  const originalStorage = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: { getItem: () => null },
  })
  t.after(() => {
    if (originalStorage) Object.defineProperty(globalThis, 'localStorage', originalStorage)
    else delete globalThis.localStorage
  })
  let attempts = 0
  t.mock.method(globalThis, 'fetch', async (url, options) => {
    attempts++
    assert.equal(url, '/api/admin/auth/login')
    assert.equal(options.credentials, 'same-origin')
    assert.equal(options.cache, 'no-store')
    assert.deepEqual(JSON.parse(options.body), { secret: 'wrong-secret' })
    return new Response(JSON.stringify({ error: '认证失败次数过多，请稍后再试' }), { status: 429 })
  })
  await assert.rejects(api.loginAdminSession('wrong-secret'), error => {
    assert.ok(error instanceof AdminAPIError)
    assert.equal(error.status, 429)
    assert.equal(error.message, '认证失败次数过多，请稍后再试')
    return true
  })
  assert.equal(attempts, 1)
})
