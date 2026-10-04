import type { Env } from './index.ts'
import { now, randomToken, HTTPError, text } from './http.ts'

type QueuedCommand = { id: string; kind: string; payload: string; status: string }
type CommandHistory = QueuedCommand & { created_at: number; delivered_at: number | null; completed_at: number | null; error: string | null }

export function validateCommand(kind: unknown, payload: unknown): { kind: string; payload: Record<string, unknown> } {
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) throw new HTTPError(400, '无效的命令参数')
  const p = payload as Record<string, unknown>
  if (kind === 'tunnel.start' && (p.mode === 'quick' || p.mode === 'fixed')) return { kind, payload: { mode: p.mode } }
  if (kind === 'tunnel.stop') return { kind, payload: {} }
  // Relay can be switched off remotely but only started on the computer itself: it sends
  // tool data through this service, so the person at the computer must agree to it.
  if (kind === 'relay.stop') return { kind, payload: {} }
  if (kind === 'control.pause' && typeof p.paused === 'boolean') return { kind, payload: { paused: p.paused } }
  if (kind === 'capability.set' && typeof p.category === 'string' && ['files', 'terminal', 'computer', 'browser'].includes(String(p.category)) && typeof p.enabled === 'boolean') return { kind, payload: { category: p.category, enabled: p.enabled } }
  throw new HTTPError(400, '不支持的命令或配置')
}
export async function expireCommands(env: Env, id: string): Promise<void> {
  await env.DB.prepare("UPDATE commands SET status='expired',completed_at=?,error=CASE WHEN status='executing' THEN '设备未确认执行结果；不会重复执行' ELSE '命令已过期' END WHERE device_id=? AND ((status='queued' AND expires_at<=?) OR (status='executing' AND delivered_at<=?))").bind(now(), id, now(), now() - 90).run()
}
export async function commandHistory(env: Env, deviceID: string) {
  await expireCommands(env, deviceID)
  const { results } = await env.DB.prepare('SELECT id,kind,payload,status,created_at,delivered_at,completed_at,error FROM commands WHERE device_id=? ORDER BY created_at DESC,id DESC LIMIT 30').bind(deviceID).all<CommandHistory>()
  return { commands: results.map(c => ({ ...c, payload: JSON.parse(c.payload) })) }
}
export async function queueCommand(env: Env, deviceID: string, userID: string, input: Record<string, unknown>) {
  const cmd = validateCommand(input.kind, input.payload), requestID = text(input.request_id, 64), payload = JSON.stringify(cmd.payload)
  await expireCommands(env, deviceID)
  const previous = await env.DB.prepare('SELECT id,kind,payload,status FROM commands WHERE device_id=? AND request_id=?').bind(deviceID, requestID).first<QueuedCommand>()
  if (!previous) {
    // Keep the queue limit inside the insert, including concurrent submissions.
    await env.DB.prepare("INSERT OR IGNORE INTO commands(id,device_id,kind,payload,created_at,expires_at,request_id) SELECT ?,?,?,?,?,?,? WHERE EXISTS(SELECT 1 FROM devices WHERE id=? AND user_id=? AND revoked_at IS NULL) AND (SELECT COUNT(*) FROM commands WHERE device_id=? AND status IN ('queued','executing'))<20").bind(randomToken(), deviceID, cmd.kind, payload, now(), now() + 300, requestID, deviceID, userID, deviceID).run()
  }
  const result = previous || await env.DB.prepare('SELECT id,kind,payload,status FROM commands WHERE device_id=? AND request_id=?').bind(deviceID, requestID).first<QueuedCommand>()
  if (!result) {
    const bound = await env.DB.prepare('SELECT id FROM devices WHERE id=? AND user_id=? AND revoked_at IS NULL').bind(deviceID, userID).first()
    if (!bound) throw new HTTPError(409, '设备已解绑')
    throw new HTTPError(429, '待处理命令过多，请等待电脑执行')
  }
  if (result.kind !== cmd.kind || result.payload !== payload) throw new HTTPError(409, '请求编号已用于其他命令')
  return { ...result, payload: JSON.parse(result.payload) }
}
