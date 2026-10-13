import type { Env } from './index.ts'
import { HTTPError } from './http.ts'

// Opt-in relay mode. A computer that cannot (or should not) use a Cloudflare tunnel
// keeps one outbound WebSocket open to its own RelayHub Durable Object. Cloud tool
// calls are forwarded over that socket and the reply is returned to the caller.
// Unlike a tunnel, tool arguments and results pass through this service in memory.
// Nothing is stored: the hub keeps only the pending call table and the socket.
const callTimeoutMs = 55_000
const maxMessage = 12 * 1024 * 1024

export type RelayCall = { tool: string; args: Record<string, unknown>; session: string; client: string }
export type RelayReply = { http_status: number; body: unknown }
export type UploadFrame = { method: string; token: string; header: Record<string, string>; data?: string }
type Pending = { resolve: (reply: RelayReply) => void; reject: (error: Error) => void; timer: ReturnType<typeof setTimeout> }
class RelayFailure extends Error { code: 'disconnected' | 'timeout' | 'send'; constructor(code: 'disconnected' | 'timeout' | 'send') { super(code); this.code = code } }

// One instance per computer (idFromName(device id)). Hibernatable sockets let an idle
// relay cost nothing; the pending table only matters while a call is awaiting its reply,
// and an awaiting request keeps the object in memory.
export class RelayHub {
  pending = new Map<string, Pending>()
  state: DurableObjectState
  env: Env
  constructor(state: DurableObjectState, env: Env) {
    this.state = state; this.env = env
    // Keep-alive pings are answered without waking the object.
    state.setWebSocketAutoResponse(new WebSocketRequestResponsePair('ping', 'pong'))
  }
  socket(): WebSocket | undefined { return this.state.getWebSockets()[0] }
  failAll(code: 'disconnected' | 'timeout' | 'send'): void {
    for (const [id, p] of this.pending) { clearTimeout(p.timer); this.pending.delete(id); p.reject(new RelayFailure(code)) }
  }
  async fetch(req: Request): Promise<Response> {
    const path = new URL(req.url).pathname
    if (req.headers.get('Upgrade')?.toLowerCase() === 'websocket') {
      // A new connection replaces the old one; calls on the old socket have an unknown outcome.
      for (const old of this.state.getWebSockets()) { try { old.close(4000, 'replaced') } catch { /* already closed */ } }
      this.failAll('disconnected')
      const pair = new WebSocketPair(), [client, server] = [pair[0], pair[1]]
      this.state.acceptWebSocket(server)
      return new Response(null, { status: 101, webSocket: client })
    }
    if (path === '/close') {
      for (const ws of this.state.getWebSockets()) { try { ws.close(4001, 'revoked') } catch { /* already closed */ } }
      this.failAll('disconnected')
      return Response.json({ ok: true })
    }
    if (path === '/call' && req.method === 'POST') {
      const call = await req.json() as RelayCall
      return this.send({ type: 'call', tool: call.tool, session: call.session, client: call.client, arguments: call.args })
    }
    if (path === '/upload' && req.method === 'POST') {
      const up = await req.json() as UploadFrame
      return this.send({ type: 'upload', method: up.method, token: up.token, header: up.header, data: up.data })
    }
    return new Response('Not found', { status: 404 })
  }
  // send forwards one frame to the computer and waits for its result frame.
  async send(frame: Record<string, unknown>): Promise<Response> {
    const ws = this.socket()
    if (!ws) return Response.json({ error: 'not_connected' }, { status: 409 })
    const id = crypto.randomUUID()
    try {
      const reply = await new Promise<RelayReply>((resolve, reject) => {
        const timer = setTimeout(() => {
          this.pending.delete(id)
          try { ws.send(JSON.stringify({ type: 'cancel', id })) } catch { /* the socket is gone */ }
          reject(new RelayFailure('timeout'))
        }, callTimeoutMs)
        this.pending.set(id, { resolve, reject, timer })
        try { ws.send(JSON.stringify({ ...frame, id })) } catch { clearTimeout(timer); this.pending.delete(id); reject(new RelayFailure('send')) }
      })
      return Response.json(reply)
    } catch (e) {
      if (!(e instanceof RelayFailure)) throw e
      // 'send' means nothing left this service; the other two may have reached the computer.
      return Response.json({ error: e.code }, { status: e.code === 'send' ? 409 : e.code === 'timeout' ? 504 : 502 })
    }
  }
  webSocketMessage(_ws: WebSocket, message: string | ArrayBuffer): void {
    if (typeof message !== 'string') return
    let frame: any
    try { frame = JSON.parse(message) } catch { return }
    if (!frame || frame.type !== 'result' || typeof frame.id !== 'string') return
    const p = this.pending.get(frame.id); if (!p) return
    clearTimeout(p.timer); this.pending.delete(frame.id)
    const status = Number.isInteger(frame.http_status) && frame.http_status >= 100 && frame.http_status < 600 ? frame.http_status : 502
    p.resolve(message.length > maxMessage ? { http_status: 502, body: { error: 'Computer response is too large; request a smaller result', status: 'error' } } : { http_status: status, body: frame.body })
  }
  disconnected(ws: WebSocket, code: number, reason: string): void {
    try { ws.close(code === 1005 || code === 1006 ? 1000 : code, reason.slice(0, 100)) } catch { /* already closed */ }
    if (!this.state.getWebSockets().some(s => s !== ws)) this.failAll('disconnected')
  }
  webSocketClose(ws: WebSocket, code: number, reason: string): void { this.disconnected(ws, code, reason) }
  webSocketError(ws: WebSocket): void { this.disconnected(ws, 1011, 'error') }
}

function stub(env: Env, deviceID: string): DurableObjectStub {
  if (!env.RELAY) throw new HTTPError(503, 'Relay mode is not available on this service')
  return env.RELAY.get(env.RELAY.idFromName(deviceID))
}
// The computer's authenticated WebSocket upgrade is handed to its own hub.
export function relayConnect(env: Env, deviceID: string, req: Request): Promise<Response> { return stub(env, deviceID).fetch(req) }
export async function relayClose(env: Env, deviceID: string): Promise<void> {
  if (!env.RELAY) return
  try { await stub(env, deviceID).fetch('https://relay/close', { method: 'POST' }) } catch { /* best effort; the device credential is already revoked */ }
}
export async function relayCall(env: Env, deviceID: string, call: RelayCall): Promise<RelayReply> {
  let response: Response
  try { response = await stub(env, deviceID).fetch('https://relay/call', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(call) }) } catch (e) {
    if (e instanceof HTTPError) throw e
    throw new HTTPError(502, 'Relay connection failed. Execution may have occurred. Check local activity or an existing command session before retrying.')
  }
  if (response.ok) return await response.json() as RelayReply
  const code = ((await response.json().catch(() => ({}))) as { error?: string }).error
  if (code === 'timeout') throw new HTTPError(502, 'Computer did not answer through the relay in time. Execution may have occurred. Check local activity or an existing command session before retrying.')
  if (code === 'disconnected') throw new HTTPError(502, 'The relay connection dropped during the call. Execution may have occurred. Check local activity before retrying.')
  throw new HTTPError(409, 'Relay is not connected. Ask the user to turn on relay mode in ReadyRig on that computer; tool data then passes through ReadyRig Cloud.')
}

// Upload addresses of computers that receive files over the relay (receive_file with
// via=relay). The token in the path is the only credential: the computer answers only
// for an address it opened. A PATCH body is forwarded in frames of at most 1 MiB, each
// naming its offset, so a dropped frame is resent safely. File bytes pass through this
// service in memory and are never stored.
const uploadFrameBytes = 1 << 20
export const maxRelayUpload = 100 * 1024 * 1024
const uploadHeaders = ['Upload-Offset', 'Upload-Length', 'Upload-Complete', 'Content-Type', 'Allow']

function base64(bytes: Uint8Array): string {
  let s = ''
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000))
  return btoa(s)
}
async function uploadFrame(env: Env, deviceID: string, frame: UploadFrame): Promise<RelayReply> {
  let response: Response
  try { response = await stub(env, deviceID).fetch('https://relay/upload', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(frame) }) } catch {
    return { http_status: 502, body: { text: 'the relay connection failed' } }
  }
  if (response.ok) return await response.json() as RelayReply
  const code = ((await response.json().catch(() => ({}))) as { error?: string }).error
  // 5xx makes send_file ask where things stand and retry.
  if (code === 'timeout') return { http_status: 504, body: { text: 'the receiving computer did not answer in time' } }
  if (code === 'disconnected') return { http_status: 502, body: { text: 'the relay connection dropped' } }
  return { http_status: 503, body: { text: 'the receiving computer is not connected' } }
}
function uploadResponse(reply: RelayReply, head: boolean): Response {
  const body = (reply.body && typeof reply.body === 'object' ? reply.body : {}) as { header?: Record<string, unknown>; text?: unknown }
  const headers = new Headers({ 'Cache-Control': 'no-store' })
  for (const key of uploadHeaders) { const value = body.header?.[key]; if (typeof value === 'string') headers.set(key, value) }
  const status = Number.isInteger(reply.http_status) && reply.http_status >= 200 && reply.http_status < 600 ? reply.http_status : 502
  const text = !head && status !== 204 && status !== 304 && typeof body.text === 'string' ? body.text : null
  return new Response(text, { status, headers })
}
export async function relayUpload(req: Request, env: Env, deviceID: string, token: string): Promise<Response> {
  if (!env.RELAY) return new Response('relay mode is not available on this service', { status: 503 })
  const method = req.method
  if (!['HEAD', 'GET', 'PATCH', 'DELETE'].includes(method)) return new Response(null, { status: 405, headers: { Allow: 'HEAD, GET, PATCH, DELETE' } })
  const device = await env.DB.prepare('SELECT id FROM devices WHERE id=? AND revoked_at IS NULL').bind(deviceID).first<{ id: string }>()
  if (!device) return new Response('404 page not found', { status: 404 })
  if (method !== 'PATCH') return uploadResponse(await uploadFrame(env, device.id, { method, token, header: {} }), method === 'HEAD')
  const offset = Number(req.headers.get('Upload-Offset') ?? NaN), length = Number(req.headers.get('Content-Length') ?? NaN)
  if (!Number.isSafeInteger(offset) || offset < 0) return new Response('Upload-Offset header required', { status: 400 })
  if (!Number.isSafeInteger(length) || length < 0) return new Response('Content-Length header required', { status: 411 })
  if (length > maxRelayUpload) return new Response('relay uploads are limited to 100 MB per request', { status: 413 })
  const sha = req.headers.get('Upload-SHA256') || ''
  const buffer = new Uint8Array(uploadFrameBytes)
  let sent = 0, filled = 0, reply: RelayReply | null = null
  const flush = async (last: boolean): Promise<boolean> => {
    const header: Record<string, string> = { 'Upload-Offset': String(offset + sent) }
    if (last && sha) header['Upload-SHA256'] = sha
    reply = await uploadFrame(env, device.id, { method: 'PATCH', token, header, data: base64(buffer.subarray(0, filled)) })
    sent += filled
    filled = 0
    return reply.http_status === 204
  }
  const reader = req.body?.getReader()
  while (reader) {
    const { done, value } = await reader.read()
    if (done) break
    if (sent + filled + value.length > length) { await reader.cancel(); return new Response('the body is longer than Content-Length', { status: 400 }) }
    for (let at = 0; at < value.length;) {
      const n = Math.min(value.length - at, buffer.length - filled)
      buffer.set(value.subarray(at, at + n), filled)
      filled += n
      at += n
      // Keep the final frame back: it carries the hash.
      if (filled === buffer.length && sent + filled < length && !(await flush(false))) { await reader.cancel(); return uploadResponse(reply!, false) }
    }
  }
  if (sent + filled !== length) return new Response('the body is shorter than Content-Length', { status: 400 })
  await flush(true)
  return uploadResponse(reply!, false)
}
