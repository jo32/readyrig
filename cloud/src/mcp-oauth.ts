import type { Env, User } from './index.ts'
import { hash, HTTPError, json, now, randomToken } from './http.ts'

import { authenticateClient, registerClient, mcpScope as scope, mcpResource as resource } from './mcp-clients.ts'
import type { Client } from './mcp-clients.ts'
type Authorization = { id: string; client_id: string; redirect_uri: string; state: string; challenge: string; expires_at: number }
const redirect = (location: string) => new Response(null, { status: 302, headers: { Location: location, 'Cache-Control': 'no-store' } })
const escape = (s: string) => s.replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!)
export async function challenge(verifier: string): Promise<string> {
  return btoa(String.fromCharCode(...new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(verifier))))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}
function oauthError(error: string, status = 400) { return json({ error }, status) }

function unknownClient(req: Request): Response {
  const message = 'ReadyRig does not recognize this app registration. Return to your app and reconnect ReadyRig to register it again.'
  const headers = { 'Cache-Control': 'no-store', Vary: 'Accept', 'Referrer-Policy': 'no-referrer' }
  if (req.headers.get('Accept')?.includes('text/html')) {
    return new Response(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Reconnect ReadyRig</title><main><h1>Reconnect ReadyRig</h1><p>${message}</p><p lang="zh-CN">ReadyRig 无法识别此应用注册信息。请返回原应用，重新连接 ReadyRig 以再次注册。</p></main></html>`, {
      status: 400, headers: { ...headers, 'Content-Type': 'text/html; charset=utf-8' },
    })
  }
  return Response.json({ error: 'invalid_request', error_description: message }, { status: 400, headers })
}

export async function oauthRoute(req: Request, env: Env, getUser: () => Promise<User>, sessionHash: () => Promise<string>): Promise<Response | null> {
  const url = new URL(req.url), path = url.pathname
  if (req.method === 'GET' && ['/.well-known/oauth-protected-resource', '/.well-known/oauth-protected-resource/mcp'].includes(path)) {
    return json({ resource: resource(env), authorization_servers: [env.PUBLIC_ORIGIN], scopes_supported: [scope], bearer_methods_supported: ['header'] })
  }
  if (path === '/oauth/register' && req.method === 'POST') return registerClient(req, env)
  if (path === '/.well-known/oauth-authorization-server' && req.method === 'GET') return json({
    issuer: env.PUBLIC_ORIGIN, authorization_endpoint: env.PUBLIC_ORIGIN + '/oauth/authorize', token_endpoint: env.PUBLIC_ORIGIN + '/oauth/token',
    registration_endpoint: env.PUBLIC_ORIGIN + '/oauth/register', revocation_endpoint: env.PUBLIC_ORIGIN + '/oauth/revoke',
    response_types_supported: ['code'], grant_types_supported: ['authorization_code', 'refresh_token'],
    token_endpoint_auth_methods_supported: ['none', 'client_secret_post', 'client_secret_basic'], revocation_endpoint_auth_methods_supported: ['none', 'client_secret_post', 'client_secret_basic'], code_challenge_methods_supported: ['S256'], scopes_supported: [scope],
  })
  if (path === '/oauth/authorize' && req.method === 'GET') {
    const p = url.searchParams
    for (const key of new Set(p.keys())) if (p.getAll(key).length !== 1) return oauthError('invalid_request')
    const client = await env.DB.prepare('SELECT * FROM mcp_clients WHERE id=?').bind(p.get('client_id') || '').first<Client>()
    if (!client) return p.get('client_id') ? unknownClient(req) : oauthError('invalid_request')
    if (!JSON.parse(client.redirect_uris).includes(p.get('redirect_uri'))) return oauthError('invalid_request')
    if (p.get('response_type') !== 'code') return oauthError('unsupported_response_type')
    if (p.get('resource') !== resource(env)) return oauthError('invalid_target')
    if (p.has('scope') && p.get('scope') !== scope) return oauthError('invalid_scope')
    if (p.get('code_challenge_method') !== 'S256' || !/^[A-Za-z0-9_-]{43}$/.test(p.get('code_challenge') || '') || (p.get('state') || '').length > 8192) return oauthError('invalid_request')
    const id = randomToken()
    // Bound outstanding unauthenticated requests, including concurrent inserts.
    await env.DB.prepare('INSERT INTO mcp_requests(id,client_id,redirect_uri,state,challenge,expires_at) SELECT ?,?,?,?,?,? WHERE (SELECT COUNT(*) FROM mcp_requests WHERE client_id=? AND expires_at>?)<20').bind(id, client.id, p.get('redirect_uri'), p.get('state') || '', p.get('code_challenge'), now() + 600, client.id, now()).run()
    if (!await env.DB.prepare('SELECT id FROM mcp_requests WHERE id=?').bind(id).first()) return oauthError('temporarily_unavailable', 429)
    return redirect('/oauth/consent?request=' + id)
  }
  if (path === '/oauth/consent' && ['GET', 'POST'].includes(req.method)) {
    const id = url.searchParams.get('request') || ''
    if (!/^[A-Za-z0-9_-]{43}$/.test(id)) return oauthError('invalid_request')
    const flow = await env.DB.prepare('SELECT * FROM mcp_requests WHERE id=? AND expires_at>?').bind(id, now()).first<Authorization>()
    if (!flow) return oauthError('invalid_request')
    let owner: User
    try { owner = await getUser() } catch (e) {
      if (req.method === 'GET' && e instanceof HTTPError && e.status === 401) return redirect('/auth/google?return_to=' + encodeURIComponent('/oauth/consent?request=' + id))
      throw e
    }
    const client = await env.DB.prepare('SELECT * FROM mcp_clients WHERE id=? AND (user_id=? OR user_id IS NULL)').bind(flow.client_id, owner.id).first<Client>()
    if (!client) return oauthError('access_denied', 403)
    if (req.method === 'GET') {
      const csrf = randomToken()
      await env.DB.prepare('UPDATE mcp_requests SET csrf_hash=?,session_hash=? WHERE id=?').bind(await hash(csrf), await sessionHash(), id).run()
      // Native form POSTs need a same-origin referrer policy to retain Origin.
      // Consent submits only to this origin; the response starts a fresh callback navigation.
      return new Response(`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>ReadyRig · MCP 授权 / Authorization</title><style>body{font:16px system-ui;max-width:640px;margin:10vh auto;padding:24px;line-height:1.7}button{padding:12px 24px;margin:12px 12px 0 0}code{overflow-wrap:anywhere}</style><h1>授权连接 ReadyRig</h1><p>Authorize <strong>${escape(client.name)}</strong> · ${escape(owner.email)}</p>${client.user_id === null ? '<p>应用名称由客户端提供，未经 ReadyRig 验证。The app name is supplied by the client and is not verified by ReadyRig.</p>' : ''}<p>允许此应用查看本账号的电脑和公网连接、开关公网分享、调整工具权限、暂停或恢复控制。此应用也可通过云端调用电脑已开启的文件、终端、浏览器及桌面工具，调用参数和结果经 ReadyRig 云端转发。</p><p>Allow this app to discover your computers and public links, manage sharing and tool permissions, and pause or resume control. This app can also call enabled file, terminal, browser and desktop tools through ReadyRig Cloud, which relays their arguments and results.</p><p>授权持续到撤销或 30 天后到期，退出网页登录不会撤销。Authorization lasts until revoked or expires after 30 days; signing out does not revoke it.</p><p>回调地址 / Redirect URL: <code>${escape(flow.redirect_uri)}</code></p><form method="post" action="/oauth/consent?request=${id}"><input type="hidden" name="csrf" value="${csrf}"><button type="submit" name="decision" value="allow">允许 / Allow</button><button type="submit" name="decision" value="deny">取消 / Cancel</button></form></html>`, { headers: { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store', 'Referrer-Policy': 'same-origin', 'Content-Security-Policy': `default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'` } })
    }
    if (req.headers.get('Origin') !== env.PUBLIC_ORIGIN) return oauthError('access_denied', 403)
    const form = await readForm(req)
    const consumed = await env.DB.prepare('DELETE FROM mcp_requests WHERE id=? AND csrf_hash=? AND session_hash=? AND expires_at>? RETURNING id').bind(id, await hash(form.get('csrf') || ''), await sessionHash(), now()).first()
    if (!consumed) return oauthError('access_denied', 403)
    const target = new URL(flow.redirect_uri)
    if (flow.state) target.searchParams.set('state', flow.state)
    if (form.get('decision') !== 'allow') target.searchParams.set('error', 'access_denied')
    else {
      const code = randomToken()
      await env.DB.prepare('INSERT INTO mcp_codes(code_hash,client_id,user_id,redirect_uri,challenge,expires_at) VALUES(?,?,?,?,?,?)').bind(await hash(code), client.id, owner.id, flow.redirect_uri, flow.challenge, now() + 120).run()
      target.searchParams.set('code', code)
    }
    // A form's CSP can apply to the client's entire redirect chain in Chrome.
    // Start a fresh navigation after consuming consent, keeping form-action self.
    const destination = escape(target.toString())
    return new Response(`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta http-equiv="refresh" content="0;url=${destination}"><title>ReadyRig · 连接中</title><p>正在返回客户端… Returning to your app…</p><a href="${destination}">继续 / Continue</a></html>`, { headers: { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store', 'Referrer-Policy': 'no-referrer', 'Content-Security-Policy': "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'" } })
  }
  if (['/oauth/token', '/oauth/revoke'].includes(path) && req.method === 'POST') {
    const p = await readForm(req)
    const client = await authenticateClient(req, env, p)
    if (!client) return oauthError('invalid_client', 401)
    if (path === '/oauth/revoke') {
      const tokenHash = await hash(p.get('token') || '')
      await env.DB.prepare('DELETE FROM mcp_grants WHERE client_id=? AND (access_hash=? OR refresh_hash=?)').bind(client.id, tokenHash, tokenHash).run()
      return json({})
    }
    // Spark's OpenAuth client sends the resource on the token endpoint URL.
    // Accept either RFC 8707 location, rejecting duplicate or conflicting values.
    const queryResource = url.searchParams.get('resource')
    if (url.searchParams.getAll('resource').length > 1 || p.has('resource') && queryResource !== null && p.get('resource') !== queryResource || (p.get('resource') ?? queryResource) !== resource(env)) return oauthError('invalid_target')
    if (p.has('scope') && p.get('scope') !== scope) return oauthError('invalid_scope')
    const access = randomToken(), refresh = randomToken(), accessHash = await hash(access), refreshHash = await hash(refresh)
    if (p.get('grant_type') === 'authorization_code') {
      const verifier = p.get('code_verifier') || ''
      if (!/^[A-Za-z0-9._~-]{43,128}$/.test(verifier)) return oauthError('invalid_grant')
      const code = await env.DB.prepare('DELETE FROM mcp_codes WHERE code_hash=? AND client_id=? AND redirect_uri=? AND challenge=? AND expires_at>? RETURNING user_id').bind(await hash(p.get('code') || ''), client.id, p.get('redirect_uri') || '', await challenge(verifier), now()).first<{ user_id: string }>()
      if (!code) return oauthError('invalid_grant')
      await env.DB.prepare('INSERT INTO mcp_grants(id,client_id,user_id,access_hash,refresh_hash,access_expires_at,refresh_expires_at) VALUES(?,?,?,?,?,?,?)').bind(randomToken(), client.id, code.user_id, accessHash, refreshHash, now() + 3600, now() + 30 * 86400).run()
    } else if (p.get('grant_type') === 'refresh_token') {
      const previousHash = await hash(p.get('refresh_token') || '')
      await env.DB.batch([
        env.DB.prepare('INSERT OR IGNORE INTO mcp_refresh_used(token_hash,grant_id) SELECT refresh_hash,id FROM mcp_grants WHERE refresh_hash=? AND client_id=? AND refresh_expires_at>?').bind(previousHash, client.id, now()),
        env.DB.prepare('UPDATE mcp_grants SET access_hash=?,refresh_hash=?,access_expires_at=? WHERE refresh_hash=? AND client_id=? AND refresh_expires_at>?').bind(accessHash, refreshHash, now() + 3600, previousHash, client.id, now()),
      ])
      const grant = await env.DB.prepare('SELECT id FROM mcp_grants WHERE refresh_hash=? AND client_id=?').bind(refreshHash, client.id).first()
      if (!grant) {
        // A used refresh token signals possible theft; revoke the whole family.
        await env.DB.prepare('DELETE FROM mcp_grants WHERE client_id=? AND id IN (SELECT grant_id FROM mcp_refresh_used WHERE token_hash=?)').bind(client.id, previousHash).run()
        return oauthError('invalid_grant')
      }
    } else return oauthError('unsupported_grant_type')
    return json({ access_token: access, token_type: 'Bearer', expires_in: 3600, refresh_token: refresh, scope })
  }
  if (path.startsWith('/oauth/') || path.startsWith('/.well-known/oauth-')) return oauthError('invalid_request', 404)
  return null
}
async function readForm(req: Request): Promise<URLSearchParams> {
  if (!req.headers.get('Content-Type')?.startsWith('application/x-www-form-urlencoded')) throw new HTTPError(415, 'Use application/x-www-form-urlencoded')
  // Reuse the bounded body reader before decoding form data.
  const reader = req.body?.getReader(); if (!reader) throw new HTTPError(400, 'Missing form')
  let size = 0, value = ''; const decoder = new TextDecoder()
  while (true) { const part = await reader.read(); if (part.done) break; size += part.value.length; if (size > 16384) { await reader.cancel(); throw new HTTPError(413, 'Form too large') }; value += decoder.decode(part.value, { stream: true }) }
  const form = new URLSearchParams(value + decoder.decode())
  for (const key of new Set(form.keys())) if (form.getAll(key).length !== 1) throw new HTTPError(400, 'Duplicate form parameter')
  return form
}
export async function mcpIdentity(req: Request, env: Env): Promise<{ user_id: string; grant_id: string } | null> {
  const match = /^Bearer ([A-Za-z0-9_-]{43})$/.exec(req.headers.get('Authorization') || '')
  if (!match) return null
  return env.DB.prepare('SELECT g.user_id,g.id AS grant_id FROM mcp_grants g JOIN mcp_clients c ON c.id=g.client_id WHERE g.access_hash=? AND g.access_expires_at>? AND g.refresh_expires_at>?').bind(await hash(match[1]), now(), now()).first<{ user_id: string; grant_id: string }>()
}
