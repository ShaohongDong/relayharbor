import { afterEach, describe, expect, it, vi } from 'vitest'
import { APIError, request } from './api'
afterEach(() => vi.unstubAllGlobals())
describe('API transport', () => {
  it('sends only bearer auth and a quoted revision, with a JSON body', async () => {
    const fetch = vi
      .fn()
      .mockResolvedValue({ ok: true, json: async () => ({ data: {}, revision: 'r2' }) })
    vi.stubGlobal('fetch', fetch)
    await request('memory-only-token', '/config', 'PUT', { activeProfile: 'office' }, 'r1')
    expect(fetch).toHaveBeenCalledTimes(1)
    const [url, init] = fetch.mock.calls[0]!
    expect(url).toBe('/api/v1/config')
    expect(init.headers['If-Match']).toBe('"r1"')
    expect(init.headers.Authorization).toBe('Bearer memory-only-token')
    expect(init.credentials).toBe('omit')
    expect(init.body).toBe('{"activeProfile":"office"}')
  })
  it('preserves revision conflict details and never retries mutations', async () => {
    const fetch = vi.fn().mockResolvedValue({
      ok: false,
      status: 409,
      json: async () => ({ error: { code: 'revision_conflict', message: 'stale' } }),
    })
    vi.stubGlobal('fetch', fetch)
    await expect(request('token', '/rollback', 'POST', {}, 'old')).rejects.toMatchObject({
      status: 409,
      code: 'revision_conflict',
    })
    expect(fetch).toHaveBeenCalledTimes(1)
  })
  it('exposes authentication failure for memory cleanup', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: false,
        status: 401,
        json: async () => ({ error: { code: 'unauthorized', message: 'invalid token' } }),
      }),
    )
    await expect(request('bad', '/status')).rejects.toBeInstanceOf(APIError)
  })
})
