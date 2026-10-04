type LoginBootstrapResult =
  | { kind: 'ready' }
  | { kind: 'reload' }
  | { kind: 'failed'; message: string }

export async function bootstrapLogin(): Promise<LoginBootstrapResult> {
  if (window.location.pathname !== '/login') {
    return { kind: 'ready' }
  }

  const code = window.location.hash.slice(1)
  window.history.replaceState(null, '', '/')
  if (code === '') {
    return { kind: 'failed', message: '登录链接缺少一次性凭据。请重新运行 drove web。' }
  }

  try {
    const response = await fetch('/api/v1/auth/login', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ code }),
    })
    if (!response.ok) {
      return {
        kind: 'failed',
        message: await responseError(response),
      }
    }
    return { kind: 'reload' }
  } catch (error) {
    return {
      kind: 'failed',
      message: error instanceof Error ? error.message : String(error),
    }
  }
}

async function responseError(response: Response): Promise<string> {
  const fallback = `登录失败（${response.status}）`
  try {
    const value: unknown = await response.json()
    if (
      typeof value === 'object' &&
      value !== null &&
      'error' in value &&
      typeof value.error === 'string'
    ) {
      return value.error
    }
  } catch {
    return fallback
  }
  return fallback
}
