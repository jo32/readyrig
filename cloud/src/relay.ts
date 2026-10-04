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
      const call = await req.json() as RelayCall, ws = this.socket()
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
          try { ws.send(JSON.stringify({ type: 'call', id, tool: call.tool, session: call.session, client: call.client, arguments: call.args })) } catch { clearTimeout(timer); this.pending.delete(id); reject(new RelayFailure('send')) }
        })
        return Response.json(reply)
      } catch (e) {
        if (!(e instanceof RelayFailure)) throw e
        // 'send' means nothing left this service; the other two may have reached the computer.
        return Response.json({ error: e.code }, { status: e.code === 'send' ? 409 : e.code === 'timeout' ? 504 : 502 })
      }
    }
    return new Response('Not found', { status: 404 })
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
