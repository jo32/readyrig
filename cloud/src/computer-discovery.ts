import type { Env, User } from './index.ts'
import { now, randomToken, hash, HTTPError, json, body } from './http.ts'
import { agentPrompt } from './agent-prompt.ts'
import { commandHistory, queueCommand } from './computer-commands.ts'
import { relayCall } from './relay.ts'

type ComputerCredential = { user_id: string }
type Computer = { id: string; name: string; platform: string; last_seen: number; snapshot: string }

async function authenticate(req: Request, env: Env): Promise<ComputerCredential> {
  // Browser and device credentials never authenticate cloud computer requests.
  if (req.headers.has('Origin') && req.headers.get('Origin') !== env.PUBLIC_ORIGIN) throw new HTTPError(403, '请求来源无效')
  const match = /^Bearer (rr_links_[A-Za-z0-9_-]{43})$/.exec(req.headers.get('Authorization') || '')
  const token = match && await env.DB.prepare('SELECT s.user_id FROM discovery_tokens t JOIN sessions s ON s.token_hash=t.session_hash WHERE t.token_hash=? AND s.expires_at>?').bind(await hash(match[1]), now()).first<ComputerCredential>()
  if (!token) throw new HTTPError(401, '云端凭证已失效，请重新登录并复制云端 Prompt')
  return token
}
export async function computers(env: Env, token: ComputerCredential): Promise<Computer[]> {
  return (await env.DB.prepare('SELECT d.id,d.name,d.platform,d.last_seen,d.snapshot FROM devices d WHERE d.user_id=? AND d.revoked_at IS NULL ORDER BY d.created_at DESC,d.id LIMIT 100').bind(token.user_id).all<Computer>()).results
}
export async function computer(env: Env, token: ComputerCredential, id: string): Promise<Computer> {
  const d = await env.DB.prepare('SELECT d.id,d.name,d.platform,d.last_seen,d.snapshot FROM devices d WHERE d.id=? AND d.user_id=? AND d.revoked_at IS NULL').bind(id, token.user_id).first<Computer>()
  if (!d) throw new HTTPError(404, '找不到已授权的电脑')
  return d
}
export function summary(d: Computer) {
  const snapshot = JSON.parse(d.snapshot), online = d.last_seen > now() - 60
  const tunnel = snapshot.tunnel || {}
  let links: { gateway: string; mcp: string; console: string } | null = null
  if (online && tunnel.state === 'ready' && typeof tunnel.gateway === 'string') {
    try {
      const url = new URL(tunnel.gateway)
      if (url.protocol === 'https:' && !url.username && !url.password && !url.search && !url.hash && /^\/[A-Za-z0-9]{8}$/.test(url.pathname)) {
        const gateway = url.toString()
        links = { gateway, mcp: gateway + '/mcp', console: gateway + '/app/' }
      }
    } catch { /* A missing or invalid link is reported as unavailable. */ }
  }
  // Relay is opt-in on the computer. 'connected' means tool calls can be forwarded over
  // the computer's WebSocket (through this service) when no tunnel link is usable.
  const relay = snapshot.relay || {}, relayStates = ['off', 'standby', 'connecting', 'connected', 'error']
  return { id: d.id, name: d.name, platform: d.platform, last_seen: d.last_seen, online, version: snapshot.version || '', paused: snapshot.paused === true, enabled: snapshot.enabled || {}, connection: { state: online ? tunnel.state || 'stopped' : 'offline', mode: tunnel.mode || null }, links, relay: { state: online && relayStates.includes(relay.state) ? relay.state as string : 'off' }, privacy: { enabled: snapshot.privacy?.enabled === true } }
}
export async function issueDiscoveryToken(env: Env, owner: User, sessionHash: string): Promise<Response> {
  const session = await env.DB.prepare('SELECT expires_at FROM sessions WHERE token_hash=? AND user_id=? AND expires_at>?').bind(sessionHash, owner.id, now()).first<{ expires_at: number }>()
  if (!session) throw new HTTPError(401, '请先登录 Google')
  const token = 'rr_links_' + randomToken()
  // Only store a hash. This credential grants computer discovery and controls, never login management.
  await env.DB.prepare('INSERT INTO discovery_tokens(token_hash,session_hash) VALUES(?,?)').bind(await hash(token), sessionHash).run()
  return json({ token, expires_at: session.expires_at }, 201)
}
// Tool calls with the cloud credential go only over the relay, which the user turned on
// locally for this. A computer with a tunnel link is called at that link directly, so its
// tool data never passes through this service. The reply is the computer's own response.
async function relayTool(req: Request, env: Env, d: Computer, name: string): Promise<Response> {
  const device = summary(d)
  if (!device.online) throw new HTTPError(409, '电脑离线，请让用户打开 ReadyRig 并保持电脑唤醒')
  if (device.paused) throw new HTTPError(423, '电脑控制已暂停，只有用户授权时才可恢复')
  if (device.relay.state !== 'connected') throw new HTTPError(409, device.links ? '这台电脑没有开启云端转发，请直接调用 links.gateway 上的工具' : '这台电脑没有可用的连接。请让用户在 ReadyRig 中开启云端转发；只有用户同意时才用 tunnel.start 开启公网分享')
  const args = await body(req, 2 * 1024 * 1024)
  const session = 'cloud-api-' + (/^[A-Za-z0-9_.:-]{1,64}$/.exec(req.headers.get('X-Session-ID') || '')?.[0] || 'default')
  const reply = await relayCall(env, d.id, { tool: name, args, session, client: 'ReadyRig Cloud API' })
  return json(reply.body, reply.http_status >= 200 && reply.http_status < 600 ? reply.http_status : 502)
}
export async function computerAPI(req: Request, env: Env): Promise<Response> {
  const url = new URL(req.url), path = url.pathname
  // The public prompt contains only a placeholder credential and no devices.
  if (path === '/api/v1/prompts' && req.method === 'GET') return json({ prompts: [{ name: 'find_computer_links', description: 'Discover and control your computers, then connect directly', content: agentPrompt(env.PUBLIC_ORIGIN, '<READYRIG_TOKEN>', url.searchParams.get('lang') || 'zh-CN') }] })
  const token = await authenticate(req, env)
  if (path === '/api/v1/computers' && req.method === 'GET') return json({ computers: (await computers(env, token)).map(d => summary(d)) })
  const match = /^\/api\/v1\/computers\/([A-Za-z0-9_-]{43})(\/commands|\/tools\/([A-Za-z][A-Za-z0-9_]{0,127}))?$/.exec(path)
  if (match?.[3] && req.method === 'POST') return relayTool(req, env, await computer(env, token, match[1]), match[3])
  if (match && !match[3] && (req.method === 'GET' || match[2] && req.method === 'POST')) {
    const device = await computer(env, token, match[1])
    if (match[2] && req.method === 'GET') return json(await commandHistory(env, device.id))
    if (match[2] && req.method === 'POST') return json(await queueCommand(env, device.id, token.user_id, await body(req)), 202)
    return json({ computer: summary(device) })
  }
  throw new HTTPError(404, '找不到接口')
}
