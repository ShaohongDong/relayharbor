export interface Envelope<T> {
  data: T
  revision: string
}
export class APIError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message)
  }
}
export async function request<T>(
  token: string,
  path: string,
  method = 'GET',
  value?: unknown,
  revision?: string,
): Promise<Envelope<T>> {
  const controller = new AbortController()
  const timeout = setTimeout(() => controller.abort(), 180000)
  try {
    const headers: Record<string, string> = { Authorization: `Bearer ${token}` }
    if (value !== undefined) headers['Content-Type'] = 'application/json'
    if (revision) headers['If-Match'] = `"${revision}"`
    const response = await fetch(`/api/v1${path}`, {
      method,
      headers,
      body: value === undefined ? undefined : JSON.stringify(value),
      signal: controller.signal,
      cache: 'no-store',
      credentials: 'omit',
    })
    const body = await response.json()
    if (!response.ok)
      throw new APIError(
        response.status,
        body.error?.code ?? 'operation_failed',
        body.error?.message ?? '请求失败',
      )
    if (!body || typeof body.revision !== 'string' || !('data' in body))
      throw new Error('管理接口响应无效')
    return body as Envelope<T>
  } finally {
    clearTimeout(timeout)
  }
}
