/**
 * Drove daemon REST 客户端。
 * 仅做 JSON 透传与错误归一化，不包含业务逻辑。
 */
import type { AgentStatus, EventRow, StartRequest } from './types'

const BASE = '/api/v1'

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const resp = await fetch(`${BASE}${path}`, {
    headers: { 'Content-Type': 'application/json' },
    ...init,
  })
  if (!resp.ok) {
    const body = (await resp.json().catch(() => ({}))) as { error?: string }
    throw new Error(body.error ?? `${resp.status} ${resp.statusText}`)
  }
  if (resp.status === 204) {
    return undefined as T
  }
  return (await resp.json()) as T
}

/** 列出全部会话。 */
export function listAgents(): Promise<AgentStatus[]> {
  return request<AgentStatus[]>('/agents')
}

/** 启动一个会话。 */
export function startAgent(req: StartRequest): Promise<AgentStatus> {
  return request<AgentStatus>('/agents', { method: 'POST', body: JSON.stringify(req) })
}

/** 停止一个会话。 */
export function stopAgent(id: string): Promise<void> {
  return request<void>(`/agents/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

/** 回放某会话的事件流。 */
export function replayAgent(id: string): Promise<EventRow[]> {
  return request<EventRow[]>(`/agents/${encodeURIComponent(id)}/events`)
}
