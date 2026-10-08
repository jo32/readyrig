import { oauthRoute } from './mcp-oauth.ts'
import { manageClients } from './mcp-clients.ts'
import { cloudMCP } from './mcp.ts'
import { createRemoteJWKSet, jwtVerify } from 'jose'
import { now, randomToken, hash, HTTPError, json, body, text } from './http.ts'
import { computerAPI, issueDiscoveryToken } from './computer-discovery.ts'
import { expireCommands, commandHistory, queueCommand } from './computer-commands.ts'
import { relayClose, relayConnect } from './relay.ts'
export { validateCommand } from './computer-commands.ts'
export { RelayHub } from './relay.ts'
export { hash, randomToken } from './http.ts'

export interface Env {
  DB: D1Database
  ASSETS: Fetcher
  // Durable Object namespace for opt-in relay mode; without it the service has no relay.
  RELAY?: DurableObjectNamespace
  PUBLIC_ORIGIN: string
  LEGACY_ORIGIN?: string
  MCP_ALLOWED_TUNNEL_HOSTS?: string
  GOOGLE_CLIENT_ID: string
  GOOGLE_CLIENT_SECRET: string
}
export type User = { id: string; email: string; name: string }
type Device = { id: string; user_id: string; name: string; revoked_at: number | null }
type Pair = { id: string; challenge: string; name: string; platform: string; code: string; user_id: string | null; expires_at: number; claimed: number }
const jwks = createRemoteJWKSet(new URL('https://www.googleapis.com/oauth2/v3/certs'))
async function pkce(value: string): Promise<string> {
  const bytes = new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(value)))
  return btoa(String.fromCharCode(...bytes)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}
function cookie(req: Request, key: string): string { return req.headers.get('Cookie')?.split(';').map(v => v.trim()).find(v => v.startsWith(key + '='))?.slice(key.length + 1) || '' }
function cookieName(env: Env, kind: string): string { return (env.PUBLIC_ORIGIN.startsWith('https:') ? '__Host-' : '') + 'readyrig_' + kind }
function setCookie(env: Env, kind: string, token: string, maxAge: number): string {
  return `${cookieName(env, kind)}=${token}; Path=/; HttpOnly; SameSite=Lax; Max-Age=${maxAge}${env.PUBLIC_ORIGIN.startsWith('https:') ? '; Secure' : ''}`
}
function redirect(path: string, cookies: string[] = []): Response {
  const headers = new Headers({ Location: path, 'Cache-Control': 'no-store' }); cookies.forEach(c => headers.append('Set-Cookie', c))
  return new Response(null, { status: 302, headers })
}
async function user(req: Request, env: Env): Promise<User> {
  const token = cookie(req, cookieName(env, 'session'))
  const result = token && await env.DB.prepare('SELECT u.id,u.email,u.name FROM sessions s JOIN users u ON s.user_id=u.id WHERE s.token_hash=? AND s.expires_at>?').bind(await hash(token), now()).first<User>()
  if (!result) throw new HTTPError(401, '请先登录 Google'); return result
}
function sameOrigin(req: Request, env: Env): void { if (req.headers.get('Origin') !== env.PUBLIC_ORIGIN) throw new HTTPError(403, '请求来源无效') }
async function agent(req: Request, env: Env): Promise<Device> {
  const match = /^Bearer ([A-Za-z0-9_-]{43})$/.exec(req.headers.get('Authorization') || '')
  const device = match && await env.DB.prepare('SELECT id,user_id,name,revoked_at FROM devices WHERE token_hash=? AND revoked_at IS NULL').bind(await hash(match[1])).first<Device>()
  if (!device) throw new HTTPError(401, '设备凭证已失效'); return device
}
async function owned(env: Env, id: string, owner: User): Promise<Device> {
  const device = await env.DB.prepare('SELECT id,user_id,name,revoked_at FROM devices WHERE id=? AND user_id=? AND revoked_at IS NULL').bind(id, owner.id).first<Device>()
  if (!device) throw new HTTPError(404, '找不到这台电脑'); return device
}
// Upload only the defined status fields; local logs, paths, and credentials are excluded.
export function sanitizeSnapshot(raw: unknown): Record<string, unknown> {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) throw new HTTPError(400, '无效的设备状态')
  const v = raw as Record<string, any>, enabled: Record<string, boolean> = {}
  for (const key of ['files', 'terminal', 'computer', 'browser']) enabled[key] = v.enabled?.[key] === true
  const t = v.tunnel || {}, tunnel: Record<string, unknown> = {}
  for (const key of ['mode', 'state', 'message', 'url', 'gateway', 'console', 'mcp']) if (typeof t[key] === 'string' && t[key].length <= 2048) tunnel[key] = t[key]
  const r = v.relay || {}, relay: Record<string, unknown> = {}
  for (const key of ['state', 'message']) if (typeof r[key] === 'string' && r[key].length <= 512) relay[key] = r[key]
  return { version: typeof v.version === 'string' ? v.version.slice(0, 64) : '', platform: typeof v.platform === 'string' ? v.platform.slice(0, 32) : '', paused: v.paused === true, enabled, tunnel, relay, privacy: { enabled: v.privacy?.enabled === true } }
}
async function route(req: Request, env: Env): Promise<Response> {
  const url = new URL(req.url), path = url.pathname
  if (url.origin !== env.PUBLIC_ORIGIN) {
    const legacy = !!env.LEGACY_ORIGIN && url.origin === env.LEGACY_ORIGIN
    // Keep existing device credentials working on the original deployment URL.
    // Browser sessions and OAuth always use the canonical domain.
    if (legacy && (req.method === 'GET' || req.method === 'HEAD')) return new Response(null, { status: 308, headers: { Location: env.PUBLIC_ORIGIN + path + url.search, 'Cache-Control': 'no-store' } })
    if (!(legacy && req.method === 'POST' && path.startsWith('/api/agent/')) && (path.startsWith('/api/') || path.startsWith('/auth/') || path.startsWith('/oauth/') || path.startsWith('/.well-known/') || path === '/mcp')) throw new HTTPError(400, '服务地址与 PUBLIC_ORIGIN 不一致')
  }
  if (path === '/mcp') return cloudMCP(req, env)
  const oauth = await oauthRoute(req, env, () => user(req, env), () => hash(cookie(req, cookieName(env, 'session'))))
  if (oauth) return oauth
  if (path === '/api/health' && req.method === 'GET') return json({ ok: true, google_configured: !!(env.GOOGLE_CLIENT_ID && env.GOOGLE_CLIENT_SECRET) })
  if (path === '/auth/google' && req.method === 'GET') {
    if (!env.GOOGLE_CLIENT_ID || !env.GOOGLE_CLIENT_SECRET) throw new HTTPError(503, '管理员尚未配置 Google 登录')
    const state = randomToken(), browser = randomToken(), verifier = randomToken(), nonce = randomToken()
    const requested = url.searchParams.get('return_to') || '/console'
    const returnTo = /^(?:\/console(?:\?pair=[A-Za-z0-9_-]{43})?|\/oauth\/consent\?request=[A-Za-z0-9_-]{43})$/.test(requested) ? requested : '/console'
    await env.DB.prepare('INSERT INTO oauth_states(id,browser_hash,verifier,nonce,return_to,expires_at) VALUES(?,?,?,?,?,?)').bind(state, await hash(browser), verifier, nonce, returnTo, now() + 600).run()
    const google = new URL('https://accounts.google.com/o/oauth2/v2/auth')
    google.search = new URLSearchParams({ client_id: env.GOOGLE_CLIENT_ID, redirect_uri: env.PUBLIC_ORIGIN + '/auth/callback', response_type: 'code', scope: 'openid email profile', state, nonce, code_challenge: await pkce(verifier), code_challenge_method: 'S256', prompt: 'select_account' }).toString()
    return redirect(google.toString(), [setCookie(env, 'oauth', browser, 600)])
  }
  if (path === '/auth/callback' && req.method === 'GET') {
    const state = url.searchParams.get('state'), browser = cookie(req, cookieName(env, 'oauth'))
    const flow = state && browser && await env.DB.prepare('DELETE FROM oauth_states WHERE id=? AND browser_hash=? AND expires_at>? RETURNING *').bind(state, await hash(browser), now()).first<{ verifier: string; nonce: string; return_to: string }>()
    if (!flow) throw new HTTPError(400, '登录已过期或验证失败，请重新登录')
    if (!url.searchParams.get('code')) return redirect('/console?error=google_cancelled', [setCookie(env, 'oauth', '', 0)])
    const response = await fetch('https://oauth2.googleapis.com/token', { method: 'POST', body: new URLSearchParams({ code: url.searchParams.get('code')!, client_id: env.GOOGLE_CLIENT_ID, client_secret: env.GOOGLE_CLIENT_SECRET, redirect_uri: env.PUBLIC_ORIGIN + '/auth/callback', grant_type: 'authorization_code', code_verifier: flow.verifier }), signal: AbortSignal.timeout(10000) })
    if (!response.ok) throw new HTTPError(502, 'Google 登录验证失败，请重试')
    const tokens = await response.json() as { id_token?: string }
    if (!tokens.id_token) throw new HTTPError(502, 'Google 未返回身份凭证')
    const { payload } = await jwtVerify(tokens.id_token, jwks, { issuer: ['https://accounts.google.com', 'accounts.google.com'], audience: env.GOOGLE_CLIENT_ID, algorithms: ['RS256'] })
    if (!payload.sub || payload.nonce !== flow.nonce || payload.email_verified !== true || typeof payload.email !== 'string') throw new HTTPError(403, 'Google 身份验证失败')
    const session = randomToken()
    await env.DB.batch([
      env.DB.prepare('INSERT INTO users(id,email,name,created_at) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET email=excluded.email,name=excluded.name').bind(payload.sub, payload.email, typeof payload.name === 'string' ? payload.name : payload.email, now()),
      env.DB.prepare('INSERT INTO sessions(token_hash,user_id,expires_at) VALUES(?,?,?)').bind(await hash(session), payload.sub, now() + 7 * 86400)
    ])
    return redirect(flow.return_to, [setCookie(env, 'session', session, 7 * 86400), setCookie(env, 'oauth', '', 0)])
  }
  if (path.startsWith('/api/v1/')) return computerAPI(req, env)
  if (path === '/api/agent/relay' && req.method === 'GET') {
    // Opt-in relay: the computer holds this WebSocket open so tool calls need no tunnel.
    const device = await agent(req, env)
    if (!env.RELAY) throw new HTTPError(503, '此云端服务未开启云端转发')
    if (req.headers.get('Upgrade')?.toLowerCase() !== 'websocket') throw new HTTPError(426, '需要 WebSocket 连接')
    return relayConnect(env, device.id, req)
  }
  if (path.startsWith('/api/agent/') && req.method === 'POST') {
    const input = await body(req)
    if (path === '/api/agent/pair') {
      const challenge = text(input.challenge, 64); if (!/^[a-f0-9]{64}$/.test(challenge)) throw new HTTPError(400, '无效的绑定验证')
      const count = await env.DB.prepare('SELECT COUNT(*) AS n FROM pairings WHERE expires_at>?').bind(now()).first<{ n: number }>()
      if ((count?.n || 0) > 1000) throw new HTTPError(429, '绑定请求过多，请稍后重试')
      const id = randomToken(), code = String(crypto.getRandomValues(new Uint32Array(1))[0] % 1000000).padStart(6, '0')
      await env.DB.prepare('INSERT INTO pairings(id,challenge,name,platform,code,expires_at) VALUES(?,?,?,?,?,?)').bind(id, challenge, text(input.name), text(input.platform, 32), code, now() + 600).run()
      return json({ id, code }, 201)
    }
    const pairMatch = /^\/api\/agent\/pair\/([A-Za-z0-9_-]{43})$/.exec(path)
    if (pairMatch) {
      const verifier = text(input.verifier, 43); if (!/^[A-Za-z0-9_-]{43}$/.test(verifier)) throw new HTTPError(400, '无效的设备验证')
      const challenge = await hash(verifier)
      const pair = await env.DB.prepare('SELECT * FROM pairings WHERE id=? AND challenge=? AND expires_at>?').bind(pairMatch[1], challenge, now()).first<Pair>()
      if (!pair) throw new HTTPError(410, '绑定已过期'); if (!pair.user_id) return json({ pending: true })
      await env.DB.batch([
        env.DB.prepare('INSERT OR IGNORE INTO devices(id,user_id,token_hash,name,platform,created_at) SELECT id,user_id,challenge,name,platform,? FROM pairings WHERE id=? AND challenge=? AND user_id IS NOT NULL AND expires_at>? AND claimed=0').bind(now(), pair.id, challenge, now()),
        env.DB.prepare('UPDATE pairings SET claimed=1 WHERE id=? AND challenge=? AND EXISTS(SELECT 1 FROM devices WHERE id=pairings.id AND token_hash=? AND revoked_at IS NULL)').bind(pair.id, challenge, challenge)
      ])
      const registered = await env.DB.prepare('SELECT d.id,u.email FROM devices d JOIN users u ON d.user_id=u.id WHERE d.id=? AND d.token_hash=? AND d.revoked_at IS NULL').bind(pair.id, challenge).first<{ id: string; email: string }>()
      if (!registered) throw new HTTPError(410, '设备绑定已撤销')
      return json({ device_id: registered.id, email: registered.email })
    }
    const device = await agent(req, env)
    if (path === '/api/agent/disconnect') {
      await revoke(env, device.id); return json({ ok: true })
    }
    if (path === '/api/agent/rename') {
      const name = text(input.name)
      await env.DB.prepare('UPDATE devices SET name=? WHERE id=? AND revoked_at IS NULL').bind(name, device.id).run(); return json({ name })
    }
    if (path === '/api/agent/heartbeat') {
      const snapshot = sanitizeSnapshot(input.snapshot), results = input.results || []
      // A rename made in the app while offline arrives with the next heartbeat.
      const name = input.name === undefined ? device.name : text(input.name)
      if (!Array.isArray(results) || results.length > 8) throw new HTTPError(400, '无效的命令回执')
      const updates = results.map((r: any) => {
        if (!r || typeof r.id !== 'string' || (r.error !== undefined && typeof r.error !== 'string')) throw new HTTPError(400, '无效的命令回执')
        return env.DB.prepare("UPDATE commands SET status=?,completed_at=?,error=? WHERE id=? AND device_id=? AND status IN ('executing','expired')").bind(r.error ? 'failed' : 'completed', now(), r.error ? r.error.slice(0, 1024) : null, r.id, device.id)
      })
      await env.DB.batch([env.DB.prepare('UPDATE devices SET last_seen=?,snapshot=?,name=? WHERE id=? AND revoked_at IS NULL').bind(now(), JSON.stringify(snapshot), name, device.id), ...updates])
      await expireCommands(env, device.id)
      // Atomic claim prevents two overlapping heartbeats from receiving the same
      // command. Timed-out execution is never retried automatically.
      const command = await env.DB.prepare("UPDATE commands SET status='executing',delivered_at=? WHERE id=(SELECT id FROM commands WHERE device_id=? AND status='queued' AND expires_at>? ORDER BY rowid LIMIT 1) AND status='queued' AND EXISTS(SELECT 1 FROM devices WHERE id=? AND revoked_at IS NULL) AND NOT EXISTS(SELECT 1 FROM commands WHERE device_id=? AND status='executing') RETURNING id,kind,payload").bind(now(), device.id, now(), device.id, device.id).first<{ id: string; kind: string; payload: string }>()
      return json({ name, command: command ? { ...command, payload: JSON.parse(command.payload) } : null })
    }
    throw new HTTPError(404, '找不到接口')
  }
  if (path.startsWith('/api/')) {
    if (req.method !== 'GET') sameOrigin(req, env)
    const owner = await user(req, env)
    if (path.startsWith('/api/mcp/')) return manageClients(req, env, owner)
    if (path === '/api/discovery-token' && req.method === 'POST') {
      await body(req)
      return issueDiscoveryToken(env, owner, await hash(cookie(req, cookieName(env, 'session'))))
    }
    if (path === '/api/me' && req.method === 'GET') return json({ user: owner })
    if (path === '/api/logout' && req.method === 'POST') {
      await env.DB.prepare('DELETE FROM sessions WHERE token_hash=?').bind(await hash(cookie(req, cookieName(env, 'session')))).run()
      const response = json({ ok: true }); response.headers.append('Set-Cookie', setCookie(env, 'session', '', 0)); return response
    }
    const pairing = /^\/api\/pairings\/([A-Za-z0-9_-]{43})$/.exec(path)
    if (pairing) {
      const pair = await env.DB.prepare('SELECT * FROM pairings WHERE id=? AND expires_at>? AND claimed=0').bind(pairing[1], now()).first<Pair>()
      if (!pair || (pair.user_id && pair.user_id !== owner.id)) throw new HTTPError(404, '绑定请求已过期或已完成')
      if (req.method === 'GET') return json({ name: pair.name, platform: pair.platform, code: pair.code, approved: pair.user_id === owner.id })
      if (req.method === 'POST') {
        await env.DB.prepare('UPDATE pairings SET user_id=? WHERE id=? AND expires_at>? AND claimed=0 AND (user_id IS NULL OR user_id=?)').bind(owner.id, pair.id, now(), owner.id).run()
        return json({ ok: true })
      }
    }
    if (path === '/api/devices' && req.method === 'GET') {
      const { results } = await env.DB.prepare('SELECT id,name,platform,created_at,last_seen,snapshot FROM devices WHERE user_id=? AND revoked_at IS NULL ORDER BY created_at DESC LIMIT 100').bind(owner.id).all<{ snapshot: string; last_seen: number }>()
      return json({ devices: results.map(d => ({ ...d, snapshot: JSON.parse(d.snapshot), online: d.last_seen > now() - 60 })) })
    }
    const match = /^\/api\/devices\/([A-Za-z0-9_-]{43})(\/commands)?$/.exec(path)
    if (match) {
      const device = await owned(env, match[1], owner)
      if (match[2] && req.method === 'GET') return json(await commandHistory(env, device.id))
      if (match[2] && req.method === 'POST') return json(await queueCommand(env, device.id, owner.id, await body(req), 'console'), 202)
      if (!match[2] && req.method === 'PATCH') { const input = await body(req); await env.DB.prepare('UPDATE devices SET name=? WHERE id=? AND revoked_at IS NULL').bind(text(input.name), device.id).run(); return json({ ok: true }) }
      if (!match[2] && req.method === 'DELETE') { await revoke(env, device.id); return json({ ok: true }) }
    }
    throw new HTTPError(404, '找不到接口')
  }
  if (path.startsWith('/auth/')) throw new HTTPError(404, '找不到接口')
  return env.ASSETS.fetch(req)
}
async function revoke(env: Env, id: string): Promise<void> {
  await env.DB.batch([
    env.DB.prepare('UPDATE devices SET revoked_at=?,snapshot=\'{}\' WHERE id=?').bind(now(), id),
    env.DB.prepare("UPDATE commands SET status='revoked',completed_at=?,error='设备已解绑' WHERE device_id=? AND status IN ('queued','executing')").bind(now(), id)
  ])
  await relayClose(env, id)
}
export default {
  async fetch(req: Request, env: Env): Promise<Response> {
    let response: Response
    try { response = await route(req, env) } catch (e) { response = e instanceof HTTPError ? json({ error: e.message }, e.status) : json({ error: '服务暂时不可用，请重试' }, 500) }
    // A WebSocket upgrade carries its socket on the Response; rebuilding it would drop that.
    if (response.status === 101) return response
    const headers = new Headers(response.headers)
    // ReadyRig installs its own beacon; prevent the zone from injecting a second one.
    if (headers.get('Content-Type')?.includes('text/html')) headers.set('Cache-Control', [headers.get('Cache-Control'), 'no-transform'].filter(Boolean).join(', '))
    headers.set('X-Content-Type-Options', 'nosniff')
    if (!headers.has('Referrer-Policy')) headers.set('Referrer-Policy', 'no-referrer')
    headers.set('X-Frame-Options', 'DENY')
    const path = new URL(req.url).pathname
    if (path.startsWith('/oauth/') || path === '/mcp') console.info('mcp_http', JSON.stringify({ path, method: req.method, status: response.status }))
    if (!headers.has('Content-Security-Policy') && (path.startsWith('/api/') || path.startsWith('/auth/') || path.startsWith('/oauth/') || path.startsWith('/.well-known/') || path === '/mcp')) headers.set('Content-Security-Policy', "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; font-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
    return new Response(response.body, { status: response.status, headers })
  },
  async scheduled(_event: ScheduledEvent, env: Env): Promise<void> {
    await env.DB.batch([
      env.DB.prepare('DELETE FROM mcp_registration_limits WHERE expires_at<?').bind(now()),
      env.DB.prepare('DELETE FROM mcp_clients WHERE user_id IS NULL AND created_at<? AND NOT EXISTS(SELECT 1 FROM mcp_grants WHERE client_id=mcp_clients.id AND refresh_expires_at>?)').bind(now() - 86400, now()),
      env.DB.prepare('DELETE FROM mcp_requests WHERE expires_at<?').bind(now()),
      env.DB.prepare('DELETE FROM mcp_codes WHERE expires_at<?').bind(now()),
      env.DB.prepare('DELETE FROM mcp_grants WHERE refresh_expires_at<?').bind(now()),
      env.DB.prepare('DELETE FROM sessions WHERE expires_at<?').bind(now()),
      env.DB.prepare('DELETE FROM oauth_states WHERE expires_at<?').bind(now()),
      env.DB.prepare('DELETE FROM pairings WHERE expires_at<?').bind(now()),
      env.DB.prepare('DELETE FROM commands WHERE created_at<?').bind(now() - 30 * 86400)
    ])
  }
}
