import type {
  AddAccountRequest,
  AdminErrorResponse,
  APIKeysResponse,
  AccountsResponse,
  CreateAccountResponse,
  CreateAPIKeyResponse,
  HealthResponse,
  MessageResponse,
  StatsResponse,
  SystemSettings,
  UsageLogsResponse,
  UsageStats,
} from './types'

const BASE = '/api/admin'

function extractAdminErrorMessage(body: string, status: number): string {
  if (!body.trim()) {
    return `HTTP ${status}`
  }

  try {
    const parsed = JSON.parse(body) as Partial<AdminErrorResponse>
    if (typeof parsed.error === 'string' && parsed.error.trim()) {
      return parsed.error
    }
  } catch {
    // ignore JSON parse error and fall back to raw text
  }

  return body
}

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers)
  if (options.body !== undefined && options.body !== null && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }

  const res = await fetch(BASE + path, {
    ...options,
    headers,
  })

  if (!res.ok) {
    const body = await res.text()
    throw new Error(extractAdminErrorMessage(body, res.status))
  }

  return (await res.json()) as T
}

export const api = {
  getStats: () => request<StatsResponse>('/stats'),
  getAccounts: () => request<AccountsResponse>('/accounts'),
  addAccount: (data: AddAccountRequest) =>
    request<CreateAccountResponse>('/accounts', { method: 'POST', body: JSON.stringify(data) }),
  deleteAccount: (id: number) =>
    request<MessageResponse>(`/accounts/${id}`, { method: 'DELETE' }),
  refreshAccount: (id: number) =>
    request<MessageResponse>(`/accounts/${id}/refresh`, { method: 'POST' }),
  getHealth: () => request<HealthResponse>('/health'),
  getUsageStats: () => request<UsageStats>('/usage/stats'),
  getUsageLogs: (limit = 50) => request<UsageLogsResponse>(`/usage/logs?limit=${limit}`),
  getAPIKeys: () => request<APIKeysResponse>('/keys'),
  createAPIKey: (name: string, key?: string) =>
    request<CreateAPIKeyResponse>('/keys', {
      method: 'POST',
      body: JSON.stringify({ name, ...(key ? { key } : {}) }),
    }),
  deleteAPIKey: (id: number) =>
    request<MessageResponse>(`/keys/${id}`, { method: 'DELETE' }),
  clearUsageLogs: () =>
    request<MessageResponse>('/usage/logs', { method: 'DELETE' }),
  getSettings: () => request<SystemSettings>('/settings'),
  updateSettings: (data: Partial<SystemSettings>) =>
    request<SystemSettings>('/settings', { method: 'PUT', body: JSON.stringify(data) }),
  getModels: () => request<{ models: string[] }>('/models'),
  batchTestAccounts: () =>
    request<{ total: number; success: number; failed: number; banned: number; rate_limited: number }>('/accounts/batch-test', { method: 'POST' }),
  cleanBanned: () =>
    request<{ message: string; cleaned: number }>('/accounts/clean-banned', { method: 'POST' }),
  cleanRateLimited: () =>
    request<{ message: string; cleaned: number }>('/accounts/clean-rate-limited', { method: 'POST' }),
}
