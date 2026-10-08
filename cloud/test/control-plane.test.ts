import { test, beforeEach } from 'node:test'
import assert from 'node:assert/strict'
import { DatabaseSync } from 'node:sqlite'
import { readFileSync, readdirSync } from 'node:fs'
import { generateKeyPair, exportJWK, SignJWT } from 'jose'
import worker, { hash, randomToken } from '../src/index.ts'
import type { Env } from '../src/index.ts'

class Statement {
  db: DatabaseSync; sql: string; values: any[] = []
  constructor(db: DatabaseSync, sql: string) { this.db = db; this.sql = sql }
  bind(...values: any[]) { const stmt = new Statement(this.db, this.sql); stmt.values = values; return stmt }
  async first() { return this.db.prepare(this.sql).get(...this.values) || null }
  async all() { return { results: this.db.prepare(this.sql).all(...this.values) } }
  async run() { return { success: true, meta: this.db.prepare(this.sql).run(...this.values) } }
}
let db: DatabaseSync, env: Env, session: string, otherSession: string
const origin = 'https://readyrig.example'
const timestamp = () => Math.floor(Date.now() / 1000)
beforeEach(async () => {
  db?.close(); db = new DatabaseSync(':memory:')
  for (const file of readdirSync(new URL('../migrations/', import.meta.url)).filter(file => file.endsWith('.sql')).sort()) db.exec(readFileSync(new URL('../migrations/' + file, import.meta.url), 'utf8'))
  env = { PUBLIC_ORIGIN: origin, GOOGLE_CLIENT_ID: 'test-client', GOOGLE_CLIENT_SECRET: 'test-secret', ASSETS: { fetch: async () => new Response('website') }, DB: {
    prepare: (sql: string) => new Statement(db, sql),
    batch: async (statements: Statement[]) => { db.exec('BEGIN'); try { const results = statements.map(s => ({ success: true, meta: db.prepare(s.sql).run(...s.values) })); db.exec('COMMIT'); return results } catch (e) { db.exec('ROLLBACK'); throw e } }
  } } as unknown as Env
  session = randomToken(); otherSession = randomToken()
  for (const [id, token] of [['alice', session], ['bob', otherSession]]) {
    db.prepare('INSERT INTO users VALUES(?,?,?,?)').run(id, id + '@example.com', id, timestamp())
    db.prepare('INSERT INTO sessions VALUES(?,?,?)').run(await hash(token), id, timestamp() + 600)
  }
})
async function call(path: string, method = 'GET', input?: unknown, headers: Record<string, string> = {}) {
  const req = new Request(origin + path, { method, headers: { ...(input === undefined ? {} : { 'Content-Type': 'application/json' }), ...headers }, body: input === undefined ? undefined : JSON.stringify(input) })
  const response = await worker.fetch(req, env)
  const result = response.headers.get('content-type')?.includes('application/json') ? await response.json() as any : null
  return { response, result }
}
const owner = () => ({ Cookie: '__Host-readyrig_session=' + session, Origin: origin })
const secondOwner = () => ({ Cookie: '__Host-readyrig_session=' + otherSession, Origin: origin })
async function register() {
  const token = randomToken()
  const { result: pair } = await call('/api/agent/pair', 'POST', { name: 'Test Mac', platform: 'darwin', challenge: await hash(token) })
  assert.equal((await call('/api/pairings/' + pair.id, 'POST', {}, owner())).response.status, 200)
  const { result } = await call('/api/agent/pair/' + pair.id, 'POST', { verifier: token })
  assert.equal(result.device_id, pair.id)
  return { id: pair.id, token, headers: { Authorization: 'Bearer ' + token } }
}
const snapshot = { version: '0.5.0', platform: 'darwin', paused: false, enabled: { files: true }, tunnel: { state: 'stopped' } }

test('domain migration redirects browsers while existing device credentials keep working', async () => {
  const device = await register()
  const legacy = 'https://legacy.example'
  env.LEGACY_ORIGIN = legacy
  const browser = await worker.fetch(new Request(legacy + '/console?pair=' + device.id), env)
  assert.equal(browser.status, 308)
  assert.equal(browser.headers.get('location'), origin + '/console?pair=' + device.id)
  const heartbeat = await worker.fetch(new Request(legacy + '/api/agent/heartbeat', { method: 'POST', headers: { ...device.headers, 'Content-Type': 'application/json' }, body: JSON.stringify({ snapshot }) }), env)
  assert.equal(heartbeat.status, 200)
  assert.equal((await call('/api/devices', 'GET', undefined, owner())).result.devices[0].online, true)
  const pairing = await worker.fetch(new Request(legacy + '/api/agent/pair', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: 'Legacy Mac', platform: 'darwin', challenge: await hash(randomToken()) }) }), env)
  assert.equal(pairing.status, 201)
  for (const host of [legacy, 'https://unknown.example']) {
    const mutation = await worker.fetch(new Request(host + '/api/devices/' + device.id, { method: 'DELETE', headers: owner() }), env)
    assert.equal(mutation.status, 400)
  }
  assert.equal((await worker.fetch(new Request('https://unknown.example/api/health'), env)).status, 400)
  assert.equal((await call('/api/devices/' + device.id, 'DELETE', undefined, { ...owner(), Origin: legacy })).response.status, 403)
})

test('binding requires owner confirmation and secret verifier; lost claim response is recoverable', async () => {
  const token = randomToken(), { result: pair } = await call('/api/agent/pair', 'POST', { name: 'Mac', platform: 'darwin', challenge: await hash(token) })
  assert.equal((await call('/api/agent/pair/' + pair.id, 'POST', { verifier: token })).result.pending, true)
  assert.equal((await call('/api/agent/pair/' + pair.id, 'POST', { verifier: randomToken() })).response.status, 410)
  assert.equal((await call('/api/pairings/' + pair.id)).response.status, 401)
  await call('/api/pairings/' + pair.id, 'POST', {}, owner())
  assert.equal((await call('/api/pairings/' + pair.id, 'POST', {}, secondOwner())).response.status, 404)
  const one = await call('/api/agent/pair/' + pair.id, 'POST', { verifier: token })
  const two = await call('/api/agent/pair/' + pair.id, 'POST', { verifier: token })
  assert.equal(one.result.device_id, two.result.device_id)
  assert.equal(one.result.email, 'alice@example.com')
  assert.equal(db.prepare('SELECT COUNT(*) AS n FROM devices').get()!.n, 1)
  assert.equal((await call('/api/devices', 'GET', undefined, secondOwner())).result.devices.length, 0)
})
test('heartbeats update presence, strip local secrets, and deliver commands in order once', async () => {
  const device = await register()
  const create = async (kind: string, payload: unknown) => (await call('/api/devices/' + device.id + '/commands', 'POST', { kind, payload, request_id: randomToken() }, owner())).result
  const first = await create('tunnel.start', { mode: 'quick' }), second = await create('tunnel.stop', {})
  const heartbeat = (results: unknown[] = []) => call('/api/agent/heartbeat', 'POST', { snapshot: { ...snapshot, workspace: '/private', token: 'private', tunnel: { state: 'stopped', logs: ['private'] } }, results }, device.headers)
  assert.equal((await heartbeat()).result.command.id, first.id)
  assert.equal((await heartbeat()).result.command, null)
  assert.equal((await heartbeat([{ id: first.id }])).result.command.id, second.id)
  await heartbeat([{ id: second.id, error: 'example failure' }])
  const listed = (await call('/api/devices', 'GET', undefined, owner())).result.devices[0]
  assert.equal(listed.online, true); assert.equal(listed.snapshot.workspace, undefined); assert.equal(listed.snapshot.tunnel.logs, undefined)
  assert.equal(db.prepare('SELECT status FROM commands WHERE id=?').get(first.id)!.status, 'completed')
  assert.equal(db.prepare('SELECT status FROM commands WHERE id=?').get(second.id)!.status, 'failed')
  db.prepare('UPDATE devices SET last_seen=? WHERE id=?').run(timestamp() - 61, device.id)
  assert.equal((await call('/api/devices', 'GET', undefined, owner())).result.devices[0].online, false)
})
test('a linked computer can rename itself and heartbeats report the new name', async () => {
  const device = await register()
  assert.equal((await call('/api/agent/rename', 'POST', { name: '  Studio Mac ' }, device.headers)).result.name, 'Studio Mac')
  assert.equal((await call('/api/agent/heartbeat', 'POST', { snapshot }, device.headers)).result.name, 'Studio Mac')
  assert.equal((await call('/api/agent/rename', 'POST', { name: ' ' }, device.headers)).response.status, 400)
  assert.equal((await call('/api/agent/rename', 'POST', { name: 'x' })).response.status, 401)
  assert.equal((await call('/api/agent/heartbeat', 'POST', { snapshot, name: 'Offline rename' }, device.headers)).result.name, 'Offline rename')
  assert.equal((await call('/api/devices', 'GET', undefined, owner())).result.devices[0].name, 'Offline rename')
  await call('/api/devices/' + device.id, 'PATCH', { name: 'From web' }, owner())
  assert.equal((await call('/api/agent/heartbeat', 'POST', { snapshot }, device.headers)).result.name, 'From web')
})
test('another account and cross-origin pages cannot manage devices', async () => {
  const device = await register(), path = '/api/devices/' + device.id + '/commands'
  assert.equal((await call(path, 'POST', { kind: 'tunnel.stop', payload: {}, request_id: randomToken() }, secondOwner())).response.status, 404)
  assert.equal((await call(path, 'GET', undefined, secondOwner())).response.status, 404)
  assert.equal((await call('/api/devices/' + device.id, 'DELETE', undefined, { ...owner(), Origin: 'https://evil.example' })).response.status, 403)
  assert.equal((await call('/api/devices', 'GET', undefined, device.headers)).response.status, 401)
})
test('overlapping heartbeats atomically deliver only one command', async () => {
  const device = await register()
  await call('/api/devices/' + device.id + '/commands', 'POST', { kind: 'tunnel.stop', payload: {}, request_id: randomToken() }, owner())
  const replies = await Promise.all(Array.from({ length: 4 }, () => call('/api/agent/heartbeat', 'POST', { snapshot }, device.headers)))
  assert.ok(replies.every(r => r.response.status === 200))
  assert.equal(replies.filter(r => r.result.command).length, 1)
})
test('command submission is idempotent and unknown operations are rejected', async () => {
  const device = await register(), path = '/api/devices/' + device.id + '/commands', request = { kind: 'tunnel.stop', payload: {}, request_id: randomToken() }
  const first = await call(path, 'POST', request, owner()), again = await call(path, 'POST', request, owner())
  assert.equal(first.result.id, again.result.id)
  assert.equal((await call(path, 'POST', { ...request, kind: 'tunnel.start', payload: { mode: 'quick' } }, owner())).response.status, 409)
  for (const [kind, payload] of [['shell.exec', {}], ['capability.set', { category: 'full_access', enabled: true }], ['control.pause', { paused: 'false' }], ['tunnel.start', { mode: 'unknown' }]]) assert.equal((await call(path, 'POST', { kind, payload, request_id: randomToken() }, owner())).response.status, 400)
})
test('expired commands are not executed or automatically redelivered', async () => {
  const device = await register(), path = '/api/devices/' + device.id + '/commands'
  const request = { kind: 'tunnel.stop', payload: {}, request_id: randomToken() }
  const { result: command } = await call(path, 'POST', request, owner())
  db.prepare('UPDATE commands SET expires_at=? WHERE id=?').run(timestamp() - 1, command.id)
  const beat = () => call('/api/agent/heartbeat', 'POST', { snapshot }, device.headers)
  assert.equal((await beat()).result.command, null)
  const { result: next } = await call(path, 'POST', { ...request, request_id: randomToken() }, owner())
  assert.equal((await beat()).result.command.id, next.id)
  db.prepare('UPDATE commands SET delivered_at=? WHERE id=?').run(timestamp() - 91, next.id)
  assert.equal((await beat()).result.command, null)
  assert.equal(db.prepare('SELECT status FROM commands WHERE id=?').get(next.id)!.status, 'expired')
})
test('revocation removes device access and queued commands; old pairing cannot resurrect it', async () => {
  const device = await register()
  await call('/api/devices/' + device.id + '/commands', 'POST', { kind: 'tunnel.stop', payload: {}, request_id: randomToken() }, owner())
  assert.equal((await call('/api/devices/' + device.id, 'DELETE', undefined, owner())).response.status, 200)
  assert.equal((await call('/api/agent/heartbeat', 'POST', { snapshot }, device.headers)).response.status, 401)
  assert.equal((await call('/api/agent/pair/' + device.id, 'POST', { verifier: device.token })).response.status, 410)
  assert.equal(db.prepare('SELECT status FROM commands').get()!.status, 'revoked')
})
test('receipts cannot acknowledge another computer command', async () => {
  const a = await register(), b = await register()
  const { result: command } = await call('/api/devices/' + a.id + '/commands', 'POST', { kind: 'tunnel.stop', payload: {}, request_id: randomToken() }, owner())
  await call('/api/agent/heartbeat', 'POST', { snapshot }, a.headers)
  await call('/api/agent/heartbeat', 'POST', { snapshot, results: [{ id: command.id }] }, b.headers)
  assert.equal(db.prepare('SELECT status FROM commands WHERE id=?').get(command.id)!.status, 'executing')
})
test('OAuth binds state to browser, verifies signed Google identity and nonce, and creates session', async () => {
  const { publicKey, privateKey } = await generateKeyPair('RS256'), jwk = await exportJWK(publicKey)
  Object.assign(jwk, { kid: 'test-google-key', alg: 'RS256', use: 'sig' })
  const start = await call('/auth/google?return_to=https://evil.example')
  const location = new URL(start.response.headers.get('Location')!)
  assert.equal(location.searchParams.get('code_challenge_method'), 'S256')
  const state = location.searchParams.get('state')!, flow = db.prepare('SELECT * FROM oauth_states WHERE id=?').get(state)!
  assert.equal((await call('/auth/callback?state=' + state + '&code=test')).response.status, 400)
  assert.ok(db.prepare('SELECT id FROM oauth_states WHERE id=?').get(state))
  let idToken = await new SignJWT({ email: 'google@example.com', email_verified: true, nonce: flow.nonce, name: 'Google User' }).setProtectedHeader({ alg: 'RS256', kid: 'test-google-key' }).setIssuer('https://accounts.google.com').setAudience(env.GOOGLE_CLIENT_ID).setSubject('google-user').setExpirationTime('1h').setIssuedAt().sign(privateKey)
  const originalFetch = globalThis.fetch
  globalThis.fetch = async (url: any) => String(url).includes('/token') ? Response.json({ id_token: idToken }) : Response.json({ keys: [jwk] })
  try {
    const completed = await call('/auth/callback?state=' + state + '&code=test', 'GET', undefined, { Cookie: start.response.headers.get('Set-Cookie')!.split(';')[0] })
    assert.equal(completed.response.status, 302); assert.equal(completed.response.headers.get('Location'), '/console')
    assert.ok(completed.response.headers.get('Set-Cookie')!.includes('HttpOnly'))
    assert.equal(db.prepare('SELECT email FROM users WHERE id=?').get('google-user')!.email, 'google@example.com')
    assert.equal((await call('/auth/callback?state=' + state + '&code=test', 'GET', undefined, { Cookie: start.response.headers.get('Set-Cookie')!.split(';')[0] })).response.status, 400)
    for (const invalid of ['nonce', 'audience', 'issuer', 'unverified-email']) {
      const begin = await call('/auth/google'), authURL = new URL(begin.response.headers.get('Location')!), nextState = authURL.searchParams.get('state')!
      const claims = db.prepare('SELECT * FROM oauth_states WHERE id=?').get(nextState)!
      idToken = await new SignJWT({ email: 'invalid@example.com', email_verified: invalid !== 'unverified-email', nonce: invalid === 'nonce' ? 'wrong-nonce' : claims.nonce }).setProtectedHeader({ alg: 'RS256', kid: 'test-google-key' }).setIssuer(invalid === 'issuer' ? 'https://wrong.example' : 'https://accounts.google.com').setAudience(invalid === 'audience' ? 'wrong-client' : env.GOOGLE_CLIENT_ID).setSubject('invalid-user').setExpirationTime('1h').sign(privateKey)
      const rejected = await call('/auth/callback?state=' + nextState + '&code=test', 'GET', undefined, { Cookie: begin.response.headers.get('Set-Cookie')!.split(';')[0] })
      assert.ok(rejected.response.status >= 400, invalid)
      assert.equal(rejected.response.headers.get('Set-Cookie'), null)
      assert.equal(db.prepare('SELECT id FROM users WHERE id=?').get('invalid-user'), undefined)
    }
  } finally { globalThis.fetch = originalFetch }
})
test('logout invalidates its session and malformed or oversized requests fail', async () => {
  assert.equal((await call('/api/logout', 'POST', {}, owner())).response.status, 200)
  assert.equal((await call('/api/me', 'GET', undefined, owner())).response.status, 401)
  assert.equal((await call('/api/agent/pair', 'POST', { challenge: 'a'.repeat(70000) })).response.status, 413)
  assert.equal((await call('/api/agent/pair', 'POST', { challenge: 'bad', name: 'Mac', platform: 'darwin' })).response.status, 400)
})

async function access(headers = owner()) {
  const { response, result } = await call('/api/discovery-token', 'POST', {}, headers)
  assert.equal(response.status, 201)
  return { ...result, headers: { Authorization: 'Bearer ' + result.token } }
}
const beat = (d: { headers: Record<string, string> }, next = snapshot, results: unknown[] = []) => call('/api/agent/heartbeat', 'POST', { snapshot: next, results }, d.headers)

test('public prompt discovery has placeholder credentials and working cloud routes in both languages', async () => {
  for (const lang of ['zh-CN', 'en']) {
    const { response, result } = await call('/api/v1/prompts?lang=' + lang)
    assert.equal(response.status, 200)
    assert.equal(response.headers.get('Cache-Control'), 'no-store')
    assert.ok(result.prompts[0].content.includes('<READYRIG_TOKEN>'))
    for (const path of ['/api/v1/computers', '/commands', 'capability.set', 'terminal', 'request_id', '/api/v1/tools/help', '/api/v1/tools/list_projects', 'links.gateway', 'links.mcp']) assert.ok(result.prompts[0].content.includes(path))
    assert.ok(!JSON.stringify(result).includes('alice'))
    assert.ok(!JSON.stringify(result).includes('/api/agent/'))
  }
})
test('cloud credentials are hashed, follow the signed-in account and cannot manage its login', async () => {
  const a = await register(), other = await register()
  db.prepare('UPDATE devices SET user_id=? WHERE id=?').run('bob', other.id)
  const token = await access()
  assert.match(token.token, /^rr_links_[A-Za-z0-9_-]{43}$/)
  const stored = db.prepare('SELECT * FROM discovery_tokens').get()!
  assert.equal(stored.token_hash, await hash(token.token))
  assert.equal(stored.session_hash, await hash(session))
  assert.equal(token.expires_at, db.prepare('SELECT expires_at FROM sessions WHERE token_hash=?').get(await hash(session))!.expires_at)
  assert.ok(!JSON.stringify(stored).includes(token.token))
  assert.notEqual(token.token, session)
  await beat(a)
  const listed = (await call('/api/v1/computers', 'GET', undefined, token.headers)).result.computers
  assert.deepEqual(listed.map((d: any) => d.id), [a.id]); assert.equal(listed[0].online, true)
  const b = await register()
  const updated = (await call('/api/v1/computers', 'GET', undefined, token.headers)).result.computers
  assert.deepEqual(new Set(updated.map((d: any) => d.id)), new Set([a.id, b.id]))
  assert.equal((await call('/api/v1/computers/' + b.id, 'GET', undefined, token.headers)).response.status, 200)
  assert.equal((await call('/api/v1/computers/' + other.id, 'GET', undefined, token.headers)).response.status, 404)
  const bobToken = await access(secondOwner())
  assert.deepEqual((await call('/api/v1/computers', 'GET', undefined, bobToken.headers)).result.computers.map((d: any) => d.id), [other.id])
  for (const route of ['/api/me', '/api/devices']) assert.equal((await call(route, 'GET', undefined, token.headers)).response.status, 401)
  assert.equal((await call('/api/discovery-token', 'POST', {}, { ...token.headers, Origin: origin })).response.status, 401)
  assert.equal((await call('/api/devices/' + a.id + '/commands', 'POST', { kind: 'capability.set', payload: { category: 'terminal', enabled: true }, request_id: randomToken() }, { ...token.headers, Origin: origin })).response.status, 401)
  for (const headers of [owner(), a.headers, { Authorization: 'Bearer ' + session }, {}]) assert.equal((await call('/api/v1/computers', 'GET', undefined, headers)).response.status, 401)
  assert.equal((await call('/api/v1/computers', 'GET', undefined, { ...token.headers, Origin: 'https://evil.example' })).response.status, 403)
  for (const [method, path] of [['GET', '/api/tokens'], ['POST', '/api/tokens'], ['DELETE', '/api/tokens/' + randomToken()]]) assert.equal((await call(path, method, method === 'GET' ? undefined : {}, owner())).response.status, 404)
})
test('credential issuance requires a same-origin login session without a management form', async () => {
  assert.equal((await call('/api/discovery-token', 'POST', {})).response.status, 403)
  assert.equal((await call('/api/discovery-token', 'POST', {}, { Origin: origin })).response.status, 401)
  assert.equal((await call('/api/discovery-token', 'POST', {}, { ...owner(), Origin: 'https://evil.example' })).response.status, 403)
  assert.equal((await call('/api/discovery-token', 'POST', [], owner())).response.status, 400)
  assert.equal((await call('/api/discovery-token', 'POST', { oversized: 'a'.repeat(70000) }, owner())).response.status, 413)
  assert.equal(db.prepare('SELECT COUNT(*) AS n FROM discovery_tokens').get()!.n, 0)
  // Issuance works before binding; there is no selected-device grant to maintain.
  const token = await access()
  assert.deepEqual((await call('/api/v1/computers', 'GET', undefined, token.headers)).result.computers, [])
})
test('authorized link discovery returns complete public URLs without changing the computer', async () => {
  const d = await register(), token = await access(), path = '/api/v1/computers/' + d.id
  const ready = { ...snapshot, tunnel: { state: 'ready', mode: 'quick', gateway: 'https://fixture.trycloudflare.com/AbC123xy', token: 'private-device-secret' } }
  await beat(d, ready as any)
  const listed = await call(path, 'GET', undefined, token.headers)
  assert.deepEqual(listed.result.computer.links, { gateway: ready.tunnel.gateway, mcp: ready.tunnel.gateway + '/mcp', console: ready.tunnel.gateway + '/app/' })
  assert.equal(listed.result.computer.connection.mode, 'quick')
  assert.ok(!JSON.stringify(listed.result).includes('private-device-secret'))
  for (const route of ['/connect', '/tools/help']) assert.equal((await call(path + route, 'POST', {}, token.headers)).response.status, 404)
  assert.equal(db.prepare('SELECT COUNT(*) AS n FROM commands').get()!.n, 0)
})
test('discovery hides unavailable links and refreshes temporary links after rotation', async () => {
  const d = await register(), token = await access(), path = '/api/v1/computers/' + d.id
  for (const state of ['stopped', 'connecting', 'failed']) {
    await beat(d, { ...snapshot, tunnel: { state, gateway: 'https://fixture.trycloudflare.com/AbC123xy' } } as any)
    assert.equal((await call(path, 'GET', undefined, token.headers)).result.computer.links, null)
  }
  for (const accessPath of ['AbC123xy', 'XyZ987ab']) {
    const gateway = 'https://fixture.trycloudflare.com/' + accessPath
    await beat(d, { ...snapshot, tunnel: { state: 'ready', gateway } } as any)
    assert.equal((await call(path, 'GET', undefined, token.headers)).result.computer.links.gateway, gateway)
  }
  db.prepare('UPDATE devices SET last_seen=? WHERE id=?').run(timestamp() - 61, d.id)
  const offline = (await call(path, 'GET', undefined, token.headers)).result.computer
  assert.equal(offline.online, false); assert.equal(offline.links, null)
  assert.equal(offline.connection.state, 'offline')
  for (const gateway of ['http://fixture.trycloudflare.com/AbC123xy', 'https://user:pass@fixture.trycloudflare.com/AbC123xy', 'https://fixture.trycloudflare.com/AbC123xy?secret=private', 'https://fixture.trycloudflare.com/invalid/path', 'bad-url']) {
    await beat(d, { ...snapshot, tunnel: { state: 'ready', gateway } } as any)
    assert.equal((await call(path, 'GET', undefined, token.headers)).result.computer.links, null)
  }
})
test('logout and session expiry stop discovery without changing public links or device credentials', async () => {
  const d = await register(), primary = await access(), path = '/api/v1/computers/' + d.id
  const ready = { ...snapshot, tunnel: { state: 'ready', gateway: 'https://fixture.trycloudflare.com/AbC123xy' } }
  await beat(d, ready as any)
  for (const kind of ['logout', 'expire']) {
    const browser = randomToken(), sessionHash = await hash(browser)
    db.prepare('INSERT INTO sessions VALUES(?,?,?)').run(sessionHash, 'alice', timestamp() + 600)
    const headers = { Cookie: '__Host-readyrig_session=' + browser, Origin: origin }
    const token = await access(headers)
    if (kind === 'logout') {
      assert.equal((await call('/api/logout', 'POST', {}, headers)).response.status, 200)
      assert.equal(db.prepare('SELECT * FROM discovery_tokens WHERE session_hash=?').get(sessionHash), undefined)
    } else db.prepare('UPDATE sessions SET expires_at=? WHERE token_hash=?').run(timestamp() - 1, sessionHash)
    assert.equal((await call(path, 'GET', undefined, token.headers)).response.status, 401)
    assert.equal((await call('/api/v1/computers', 'GET', undefined, token.headers)).response.status, 401)
    const controls = path + '/commands'
    assert.equal((await call(controls, 'GET', undefined, token.headers)).response.status, 401)
    assert.equal((await call(controls, 'POST', { kind: 'tunnel.stop', payload: {}, request_id: randomToken() }, token.headers)).response.status, 401)
    assert.equal((await call(path, 'GET', undefined, primary.headers)).response.status, 200)
    const heartbeat = await beat(d, ready as any)
    assert.equal(heartbeat.response.status, 200); assert.equal(heartbeat.result.command, null)
    assert.equal(JSON.parse(db.prepare('SELECT snapshot FROM devices WHERE id=?').get(d.id)!.snapshot as string).tunnel.gateway, ready.tunnel.gateway)
    assert.equal(db.prepare('SELECT COUNT(*) AS n FROM commands').get()!.n, 0)
  }
})
test('unbinding removes an authorized device and prevents old credentials from discovering its links', async () => {
  const d = await register(), token = await access()
  await call('/api/devices/' + d.id, 'DELETE', undefined, owner())
  assert.equal((await call('/api/v1/computers', 'GET', undefined, token.headers)).result.computers.length, 0)
  assert.equal((await call('/api/v1/computers/' + d.id, 'GET', undefined, token.headers)).response.status, 404)
})

test('Bearer computer controls share the browser queue and require an owned bound computer', async () => {
  const d = await register(), other = await register(), token = await access()
  db.prepare('UPDATE devices SET user_id=? WHERE id=?').run('bob', other.id)
  const path = '/api/v1/computers/' + d.id + '/commands'
  const input = { kind: 'capability.set', payload: { category: 'terminal', enabled: true }, request_id: randomToken() }
  for (const headers of [{}, owner(), d.headers]) assert.equal((await call(path, 'POST', input, headers)).response.status, 401)
  assert.equal((await call(path, 'POST', input, { ...token.headers, Origin: 'https://evil.example' })).response.status, 403)
  for (const method of ['GET', 'POST']) assert.equal((await call('/api/v1/computers/' + other.id + '/commands', method, method === 'POST' ? input : undefined, token.headers)).response.status, 404)
  const queued = await call(path, 'POST', input, token.headers)
  assert.equal(queued.response.status, 202); assert.equal(queued.result.status, 'queued')
  const retry = await call('/api/devices/' + d.id + '/commands', 'POST', input, owner())
  assert.equal(retry.response.status, 202); assert.equal(retry.result.id, queued.result.id)
  const delivered = await beat(d)
  assert.deepEqual(delivered.result.command, { id: queued.result.id, kind: input.kind, payload: input.payload })
  assert.equal((await beat(d)).result.command, null)
  await beat(d, { ...snapshot, enabled: { ...snapshot.enabled, terminal: true } }, [{ id: queued.result.id }])
  const history = (await call(path, 'GET', undefined, token.headers)).result.commands
  assert.equal(history[0].id, queued.result.id); assert.equal(history[0].status, 'completed')
  assert.equal((await call('/api/v1/computers/' + d.id, 'GET', undefined, token.headers)).result.computer.enabled.terminal, true)
  await call('/api/devices/' + d.id, 'DELETE', undefined, owner())
  assert.equal((await call(path, 'POST', { ...input, request_id: randomToken() }, token.headers)).response.status, 404)
  assert.equal((await call(path, 'GET', undefined, token.headers)).response.status, 404)
})
test('Bearer controls accept defined toggles and report execution failures and expiry', async () => {
  const d = await register(), token = await access(), path = '/api/v1/computers/' + d.id + '/commands'
  const actions = [
    ['capability.set', { category: 'terminal', enabled: true }],
    ['capability.set', { category: 'terminal', enabled: false }],
    ['capability.set', { category: 'files', enabled: true }],
    ['capability.set', { category: 'browser', enabled: true }],
    ['capability.set', { category: 'computer', enabled: true }],
    ['control.pause', { paused: true }], ['control.pause', { paused: false }],
    ['tunnel.start', { mode: 'quick' }], ['tunnel.start', { mode: 'fixed' }], ['tunnel.stop', {}]
  ]
  for (const [kind, payload] of actions) {
    const { response, result } = await call(path, 'POST', { kind, payload, request_id: randomToken() }, token.headers)
    assert.equal(response.status, 202)
    assert.equal((await beat(d)).result.command.id, result.id)
    await beat(d, snapshot, [{ id: result.id }])
  }
  const failed = await call(path, 'POST', { kind: 'tunnel.start', payload: { mode: 'fixed' }, request_id: randomToken() }, token.headers)
  await beat(d)
  await beat(d, snapshot, [{ id: failed.result.id, error: 'Fixed tunnel is not configured' }])
  const expired = await call(path, 'POST', { kind: 'tunnel.stop', payload: {}, request_id: randomToken() }, token.headers)
  db.prepare('UPDATE commands SET expires_at=? WHERE id=?').run(timestamp() - 1, expired.result.id)
  const history = (await call(path, 'GET', undefined, token.headers)).result.commands
  assert.equal(history.find((c: any) => c.id === failed.result.id).status, 'failed')
  assert.equal(history.find((c: any) => c.id === failed.result.id).error, 'Fixed tunnel is not configured')
  assert.equal(history.find((c: any) => c.id === expired.result.id).status, 'expired')
  assert.equal((await beat(d)).result.command, null)
})
test('Bearer controls validate bodies and preserve retries even at the shared queue limit', async () => {
  const d = await register(), token = await access(), path = '/api/v1/computers/' + d.id + '/commands'
  for (const [kind, payload] of [['shell.exec', {}], ['capability.set', { category: 'full_access', enabled: true }], ['capability.set', { category: ['terminal'], enabled: true }], ['capability.set', { category: 'terminal', enabled: 'true' }], ['control.pause', { paused: 'false' }], ['tunnel.start', { mode: 'unknown' }], ['tunnel.stop', []]]) {
    assert.equal((await call(path, 'POST', { kind, payload, request_id: randomToken() }, token.headers)).response.status, 400)
  }
  for (const request_id of [undefined, '', 'a'.repeat(65)]) assert.equal((await call(path, 'POST', { kind: 'tunnel.stop', payload: {}, request_id }, token.headers)).response.status, 400)
  assert.equal((await call(path, 'POST', { oversized: 'a'.repeat(70000) }, token.headers)).response.status, 413)
  assert.equal(db.prepare('SELECT COUNT(*) AS n FROM commands').get()!.n, 0)
  const input = { kind: 'tunnel.stop', payload: {}, request_id: randomToken() }
  const first = await call(path, 'POST', input, token.headers)
  const concurrent = await Promise.all(Array.from({ length: 23 }, (_, index) => call(index % 2 ? '/api/devices/' + d.id + '/commands' : path, 'POST', { ...input, request_id: randomToken() }, index % 2 ? owner() : token.headers)))
  assert.equal(concurrent.filter(r => r.response.status === 202).length, 19)
  assert.equal(concurrent.filter(r => r.response.status === 429).length, 4)
  assert.equal(db.prepare('SELECT COUNT(*) AS n FROM commands').get()!.n, 20)
  const retry = await call(path, 'POST', input, token.headers)
  assert.equal(retry.response.status, 202); assert.equal(retry.result.id, first.result.id)
  assert.equal((await call(path, 'POST', { ...input, kind: 'control.pause', payload: { paused: true } }, token.headers)).response.status, 409)
  await call('/api/logout', 'POST', {}, owner())
  assert.equal((await call(path, 'POST', input, token.headers)).response.status, 401)
  assert.equal((await call(path, 'GET', undefined, token.headers)).response.status, 401)
})

// OAuth clients are account-owned and separate from both Google login and device credentials.
const callback = 'https://client.example/oauth/callback'
async function newMCPClient() {
  const { response, result } = await call('/api/mcp/clients', 'POST', { name: 'Test MCP', redirect_uris: [callback] }, owner())
  assert.equal(response.status, 201)
  return result as { client_id: string; client_secret: string }
}
async function form(path: string, input: Record<string, string>, headers: Record<string, string> = {}) {
  const response = await worker.fetch(new Request(origin + path, { method: 'POST', headers: { 'Content-Type': 'application/x-www-form-urlencoded', ...headers }, body: new URLSearchParams(input) }), env)
  return { response, result: response.headers.get('content-type')?.includes('application/json') ? await response.json() as any : null }
}
async function startMCP(clientID: string, overrides: Record<string, string> = {}) {
  const verifier = randomToken()
  const { challenge } = await import('../src/mcp-oauth.ts')
  const params = new URLSearchParams({ client_id: clientID, redirect_uri: callback, response_type: 'code', resource: origin + '/mcp', scope: 'computers:control', state: 'original-state', code_challenge: await challenge(verifier), code_challenge_method: 'S256', ...overrides })
  return { ...(await call('/oauth/authorize?' + params)), verifier }
}
async function mcpCode(clientID: string, who = owner()) {
  const { response, verifier } = await startMCP(clientID)
  assert.equal(response.status, 302)
  const path = response.headers.get('location')!
  const consent = await worker.fetch(new Request(origin + path, { headers: who }), env)
  assert.equal(consent.status, 200)
  assert.equal(consent.headers.get('Referrer-Policy'), 'same-origin')
  assert.ok(consent.headers.get('Content-Security-Policy')!.includes("form-action 'self'"))
  const csrf = /name="csrf" value="([^"]+)"/.exec(await consent.text())![1]
  const approved = await form(path, { csrf, decision: 'allow' }, who)
  assert.equal(approved.response.status, 200)
  const target = new URL(/href="([^"]+)"/.exec(await approved.response.text())![1].replaceAll('&amp;', '&'))
  assert.equal(target.origin + target.pathname, callback)
  assert.equal(target.searchParams.get('state'), 'original-state')
  return { code: target.searchParams.get('code')!, code_verifier: verifier }
}
async function mcpToken(client: Record<string, string>, who = owner()) {
  const code = await mcpCode(client.client_id, who)
  const { response, result } = await form('/oauth/token', { ...client, ...code, grant_type: 'authorization_code', redirect_uri: callback, resource: origin + '/mcp' })
  assert.equal(response.status, 200)
  return result as { access_token: string; refresh_token: string }
}
const rpc = (token: string, method: string, params: unknown = {}) => call('/mcp', 'POST', { jsonrpc: '2.0', id: 1, method, params }, { Authorization: 'Bearer ' + token })

test('MCP discovery advertises OAuth and rejects unrelated credentials and hosts', async () => {
  const response = (await call('/mcp')).response
  assert.equal(response.status, 401)
  assert.equal(response.headers.get('WWW-Authenticate'), `Bearer resource_metadata="${origin}/.well-known/oauth-protected-resource/mcp", scope="computers:control"`)
  for (const path of ['/.well-known/oauth-protected-resource', '/.well-known/oauth-protected-resource/mcp']) {
    const { result } = await call(path)
    assert.equal(result.resource, origin + '/mcp'); assert.deepEqual(result.authorization_servers, [origin])
  }
  const metadata = (await call('/.well-known/oauth-authorization-server')).result
  assert.equal(metadata.authorization_endpoint, origin + '/oauth/authorize')
  assert.deepEqual(metadata.code_challenge_methods_supported, ['S256'])
  const device = await register()
  const discovery = (await call('/api/discovery-token', 'POST', {}, owner())).result.token
  for (const token of [session, device.token, discovery]) assert.equal((await rpc(token, 'tools/list')).response.status, 401)
  for (const path of ['/mcp', '/oauth/token', '/.well-known/oauth-authorization-server']) assert.equal((await worker.fetch(new Request('https://evil.example' + path), env)).status, 400)
})

test('MCP registration requires same-origin login, exact safe callbacks, and only returns secret once', async () => {
  const input = { name: 'Client', redirect_uris: [callback] }
  assert.equal((await call('/api/mcp/clients', 'POST', input)).response.status, 403)
  assert.equal((await call('/api/mcp/clients', 'POST', input, { ...owner(), Origin: 'https://evil.example' })).response.status, 403)
  for (const uri of ['http://remote.example/cb', 'https://example.com/#frag', 'https://user:pass@example.com/cb', 'javascript:alert(1)', 'https://example.com/*', 'invalid']) assert.equal((await call('/api/mcp/clients', 'POST', { ...input, redirect_uris: [uri] }, owner())).response.status, 400)
  const client = await newMCPClient()
  assert.equal(db.prepare('SELECT secret_hash FROM mcp_clients WHERE id=?').get(client.client_id)!.secret_hash, await hash(client.client_secret))
  const listed = (await call('/api/mcp/clients', 'GET', undefined, owner())).result
  assert.equal(listed.clients.length, 1); assert.ok(!JSON.stringify(listed).includes(client.client_secret)); assert.equal(listed.clients[0].secret_hash, undefined)
  assert.equal((await call('/api/mcp/clients', 'GET', undefined, secondOwner())).result.clients.length, 0)
  await call('/api/mcp/clients/' + client.client_id, 'DELETE', undefined, secondOwner())
  assert.ok(db.prepare('SELECT id FROM mcp_clients WHERE id=?').get(client.client_id))
})

test('OAuth requires PKCE, audience, exact redirect and explicit account-bound consent with CSRF', async () => {
  const client = await newMCPClient()
  for (const overrides of [{ redirect_uri: callback + '/other' }, { code_challenge_method: 'plain' }, { resource: 'https://evil.example/mcp' }, { response_type: 'token' }, { scope: 'admin' }]) {
    const response = (await startMCP(client.client_id, overrides)).response
    assert.equal(response.status, 400); assert.equal(response.headers.get('location'), null)
  }
  const start = await startMCP(client.client_id), path = start.response.headers.get('location')!
  const loggedOut = (await call(path)).response
  assert.ok(loggedOut.headers.get('location')!.startsWith('/auth/google?return_to='))
  const google = (await call(loggedOut.headers.get('location')!)).response
  const googleState = new URL(google.headers.get('location')!).searchParams.get('state')!
  assert.equal(db.prepare('SELECT return_to FROM oauth_states WHERE id=?').get(googleState)!.return_to, path)
  assert.equal((await call(path, 'GET', undefined, secondOwner())).response.status, 403)
  const consent = await worker.fetch(new Request(origin + path, { headers: owner() }), env)
  const csrf = /name="csrf" value="([^"]+)"/.exec(await consent.text())![1]
  assert.equal((await form(path, { csrf: 'wrong', decision: 'allow' }, owner())).response.status, 403)
  assert.equal((await form(path, { csrf, decision: 'allow' }, { ...owner(), Origin: 'https://evil.example' })).response.status, 403)
  assert.equal(db.prepare('SELECT COUNT(*) AS n FROM mcp_codes').get()!.n, 0)
  const denied = await form(path, { csrf, decision: 'deny' }, owner())
  assert.equal(new URL(/href="([^"]+)"/.exec(await denied.response.text())![1].replaceAll('&amp;', '&')).searchParams.get('error'), 'access_denied')
  assert.equal((await form(path, { csrf, decision: 'allow' }, owner())).response.status, 400)
})

test('OAuth code exchange binds secret, verifier, callback and resource, and consumes code once', async () => {
  const client = await newMCPClient(), code = await mcpCode(client.client_id)
  const input = { ...client, ...code, grant_type: 'authorization_code', redirect_uri: callback, resource: origin + '/mcp' }
  for (const overrides of [{ client_secret: 'wrong' }, { code_verifier: randomToken() }, { redirect_uri: callback + '/' }, { resource: origin + '/other' }]) assert.notEqual((await form('/oauth/token', { ...input, ...overrides })).response.status, 200)
  const { client_id, client_secret, ...basicInput } = input
  const results = await Promise.all([form('/oauth/token', basicInput, { Authorization: 'Basic ' + btoa(client_id + ':' + client_secret) }), form('/oauth/token', input)])
  assert.equal(results.filter(r => r.response.status === 200).length, 1)
  assert.equal((await form('/oauth/token', input)).result.error, 'invalid_grant')
  const tokens = results.find(r => r.response.status === 200)!.result
  const grant = db.prepare('SELECT * FROM mcp_grants').get()!
  assert.equal(grant.access_hash, await hash(tokens.access_token)); assert.equal(grant.refresh_hash, await hash(tokens.refresh_token))
  const next = await mcpCode(client.client_id)
  db.prepare('UPDATE mcp_codes SET expires_at=?').run(timestamp() - 1)
  assert.equal((await form('/oauth/token', { ...input, ...next })).result.error, 'invalid_grant')
})

test('OAuth MCP lists only owned computers and shares validated idempotent controls', async () => {
  const device = await register(), client = await newMCPClient(), tokens = await mcpToken(client)
  await call('/api/agent/heartbeat', 'POST', { snapshot }, device.headers)
  const init = (await rpc(tokens.access_token, 'initialize', { protocolVersion: '2025-06-18' })).result
  assert.equal(init.result.protocolVersion, '2025-06-18')
  assert.equal((await rpc(tokens.access_token, 'tools/list')).result.result.tools.length, 6)
  const listed = (await rpc(tokens.access_token, 'tools/call', { name: 'list_computers' })).result
  assert.equal(JSON.parse(listed.result.content[0].text).computers[0].id, device.id)
  db.prepare('UPDATE devices SET user_id=? WHERE id=?').run('bob', device.id)
  const forbidden = (await rpc(tokens.access_token, 'tools/call', { name: 'get_computer', arguments: { computer_id: device.id } })).result
  assert.equal(forbidden.result.isError, true)
  db.prepare('UPDATE devices SET user_id=? WHERE id=?').run('alice', device.id)
  const args = { computer_id: device.id, kind: 'control.pause', payload: { paused: true }, request_id: randomToken() }
  const run = () => rpc(tokens.access_token, 'tools/call', { name: 'control_computer', arguments: args })
  const first = (await run()).result, second = (await run()).result
  assert.equal(first.result.isError, false); assert.deepEqual(first, second)
  assert.equal((await call('/api/agent/heartbeat', 'POST', { snapshot }, device.headers)).result.command.kind, 'control.pause')
  const invalid = (await rpc(tokens.access_token, 'tools/call', { name: 'control_computer', arguments: { ...args, kind: 'shell.exec' } })).result
  assert.equal(invalid.result.isError, true)
  assert.equal((await call('/api/devices', 'GET', undefined, { Authorization: 'Bearer ' + tokens.access_token })).response.status, 401)
  assert.equal((await call('/mcp', 'POST', {}, { Authorization: 'Bearer ' + tokens.access_token, Origin: 'https://evil.example' })).response.status, 403)
  assert.equal((await rpc(tokens.access_token, 'tools/call', { name: 'list_computers', arguments: { unexpected: true } })).result.error.code, -32602)
})

test('MCP token expiry, refresh rotation, logout persistence and client revocation', async () => {
  const client = await newMCPClient(), tokens = await mcpToken(client)
  db.prepare('UPDATE mcp_grants SET access_expires_at=?').run(timestamp() - 1)
  assert.equal((await rpc(tokens.access_token, 'ping')).response.status, 401)
  const input = { ...client, grant_type: 'refresh_token', refresh_token: tokens.refresh_token, resource: origin + '/mcp' }
  const refreshed = await form('/oauth/token', input)
  assert.equal(refreshed.response.status, 200)
  assert.equal((await rpc(tokens.access_token, 'ping')).response.status, 401)
  assert.equal((await rpc(refreshed.result.access_token, 'ping')).response.status, 200)
  // Persistent authorization does not depend on the login session.
  await call('/api/logout', 'POST', {}, owner())
  assert.equal((await rpc(refreshed.result.access_token, 'ping')).response.status, 200)
  db.prepare('INSERT INTO sessions VALUES(?,?,?)').run(await hash(session), 'alice', timestamp() + 600)
  await call('/api/mcp/clients/' + client.client_id, 'DELETE', undefined, owner())
  assert.equal((await rpc(refreshed.result.access_token, 'ping')).response.status, 401)
  assert.equal((await form('/oauth/token', { ...input, refresh_token: refreshed.result.refresh_token })).response.status, 401)
})

test('Spark automatic registration and query-resource token flow work without manual credentials', async () => {
  const metadata = (await call('/.well-known/oauth-authorization-server')).result
  assert.equal(metadata.registration_endpoint, origin + '/oauth/register')
  const registration = await call('/oauth/register', 'POST', { client_name: 'Google', redirect_uris: [callback, 'https://oauth-redirect.googleusercontent.com/r/test-project'], token_endpoint_auth_method: 'none', grant_types: ['authorization_code', 'refresh_token'], response_types: ['code'] })
  assert.equal(registration.response.status, 201)
  assert.equal(registration.result.client_secret, undefined)
  const clientID = registration.result.client_id
  assert.equal(db.prepare('SELECT user_id FROM mcp_clients WHERE id=?').get(clientID)!.user_id, null)
  assert.equal((await call('/api/mcp/clients', 'GET', undefined, owner())).result.clients.length, 0)
  const code = await mcpCode(clientID)
  const input = { client_id: clientID, ...code, redirect_uri: callback, grant_type: 'authorization_code' }
  assert.equal((await form('/oauth/token?resource=' + encodeURIComponent(origin + '/other'), input)).result.error, 'invalid_target')
  assert.equal((await form('/oauth/token?resource=' + encodeURIComponent(origin + '/mcp'), { ...input, resource: origin + '/other' })).result.error, 'invalid_target')
  const exchange = await form('/oauth/token?resource=' + encodeURIComponent(origin + '/mcp'), input)
  assert.equal(exchange.response.status, 200)
  assert.equal((await rpc(exchange.result.access_token, 'initialize', { protocolVersion: '2025-11-25' })).result.result.protocolVersion, '2025-11-25')
  const listed = (await call('/api/mcp/clients', 'GET', undefined, owner())).result.clients
  assert.equal(listed.length, 1); assert.equal(listed[0].automatic, true)
  const refresh = await form('/oauth/token?resource=' + encodeURIComponent(origin + '/mcp'), { client_id: clientID, grant_type: 'refresh_token', refresh_token: exchange.result.refresh_token })
  assert.equal(refresh.response.status, 200)
  const replay = await form('/oauth/token?resource=' + encodeURIComponent(origin + '/mcp'), { client_id: clientID, grant_type: 'refresh_token', refresh_token: exchange.result.refresh_token })
  assert.equal(replay.result.error, 'invalid_grant')
  assert.equal((await rpc(refresh.result.access_token, 'ping')).response.status, 401)
  assert.equal((await form('/oauth/token', { client_id: clientID, grant_type: 'refresh_token', refresh_token: refresh.result.refresh_token, resource: origin + '/mcp' })).result.error, 'invalid_grant')
})

test('automatic clients isolate account grants; revocation does not disconnect other users', async () => {
  const registration = (await call('/oauth/register', 'POST', { client_name: 'Google', redirect_uris: [callback], token_endpoint_auth_method: 'none' })).result
  const client = { client_id: registration.client_id }
  const alice = await mcpToken(client), bob = await mcpToken(client, secondOwner())
  assert.equal((await call('/api/mcp/clients', 'GET', undefined, secondOwner())).result.clients.length, 1)
  await call('/api/mcp/clients/' + client.client_id, 'DELETE', undefined, owner())
  assert.equal((await rpc(alice.access_token, 'ping')).response.status, 401)
  assert.equal((await rpc(bob.access_token, 'ping')).response.status, 200)
  assert.equal((await call('/api/mcp/clients', 'GET', undefined, owner())).result.clients.length, 0)
  assert.equal((await form('/oauth/revoke', { ...client, token: bob.refresh_token })).response.status, 200)
  assert.equal((await rpc(bob.access_token, 'ping')).response.status, 401)
  assert.equal((await form('/oauth/revoke', { ...client, token: 'unknown' })).response.status, 200)
})

test('DCR validates redirects, limits abuse and honors negotiated secret authentication', async () => {
  for (const input of [{ redirect_uris: ['https://example.com/#'] }, { redirect_uris: [callback], token_endpoint_auth_method: 'client_secret_jwt' }, { redirect_uris: [callback], grant_types: ['client_credentials'] }, { redirect_uris: [callback], response_types: ['token'] }]) assert.equal((await call('/oauth/register', 'POST', input)).response.status, 400)
  const client = (await call('/oauth/register', 'POST', { client_name: 'Google', redirect_uris: [callback] })).result
  assert.equal(client.token_endpoint_auth_method, 'client_secret_basic')
  assert.ok(client.client_secret)
  const code = await mcpCode(client.client_id)
  const input = { ...code, redirect_uri: callback, grant_type: 'authorization_code', resource: origin + '/mcp' }
  assert.equal((await form('/oauth/token', { ...input, client_id: client.client_id, client_secret: client.client_secret })).response.status, 401)
  const auth = { Authorization: 'Basic ' + btoa(client.client_id + ':' + client.client_secret) }
  assert.equal((await form('/oauth/token', input, auth)).response.status, 200)
  const postClient = (await call('/oauth/register', 'POST', { client_name: 'Google', redirect_uris: [callback], token_endpoint_auth_method: 'client_secret_post' })).result
  assert.ok((await mcpToken({ client_id: postClient.client_id, client_secret: postClient.client_secret })).access_token)
  for (let i = 0; i < 18; i++) assert.equal((await call('/oauth/register', 'POST', { redirect_uris: [callback] })).response.status, 201)
  assert.equal((await call('/oauth/register', 'POST', { redirect_uris: [callback] })).response.status, 429)
})

test('cloud MCP relays enabled tools only to the current owned approved tunnel, preserving images and errors', async () => {
  const device = await register(), client = await newMCPClient(), tokens = await mcpToken(client)
  const ready = { ...snapshot, tunnel: { state: 'ready', gateway: 'https://owned-computer.trycloudflare.com/AB12cd34' } }
  await call('/api/agent/heartbeat', 'POST', { snapshot: ready }, device.headers)
  const originalFetch = globalThis.fetch
  let requests: { url: string; init: RequestInit }[] = [], response: unknown = { call_id: 'example', result: { tools: [{ name: 'read_file' }] }, status: 'ok', error: '' }, status = 200
  globalThis.fetch = async (url, init) => { requests.push({ url: String(url), init: init! }); return Response.json(response, { status }) }
  const run = (name: string, args: Record<string, unknown>) => rpc(tokens.access_token, 'tools/call', { name, arguments: { computer_id: device.id, ...args } })
  try {
    const listed = (await run('list_computer_tools', {})).result.result
    assert.equal(listed.isError, false)
    assert.equal(requests[0].url, ready.tunnel.gateway + '/api/v1/tools/help')
    const headers = new Headers(requests[0].init.headers)
    assert.equal(headers.has('Authorization'), false); assert.equal(headers.has('Cookie'), false)
    assert.ok(headers.get('X-Session-ID')!.startsWith('cloud-'))
    assert.equal(requests[0].init.redirect, 'manual')
    assert.deepEqual(JSON.parse(String(requests[0].init.body)), { compact: true, slim: true })
    await run('list_computer_tools', { names: ['read_file', 'glob'], category: 'files' })
    assert.deepEqual(JSON.parse(String(requests.at(-1)!.init.body)), { names: ['read_file', 'glob'], category: 'files' })
    await run('list_computer_tools', { compact: false })
    assert.deepEqual(JSON.parse(String(requests.at(-1)!.init.body)), {})
    const before = requests.length
    for (const bad of [{ names: 'read_file' }, { names: [1] }, { compact: 'yes' }, { category: 5 }]) assert.equal((await run('list_computer_tools', bad)).result.error?.code, -32602)
    assert.equal(requests.length, before)
    // An app that predates names/compact/slim rejects them; the call degrades to what it supports.
    const upToDate = globalThis.fetch
    const bodies: Record<string, unknown>[] = []
    globalThis.fetch = async (url, init) => {
      const body = JSON.parse(String(init!.body)); bodies.push(body)
      return body.slim || body.names ? Response.json({ error: 'unknown argument: ' + (body.slim ? 'slim' : 'names'), status: 'error' }, { status: 422 }) : Response.json({ call_id: 'old', result: { tools: [] }, status: 'success' })
    }
    assert.equal((await run('list_computer_tools', {})).result.result.isError, false)
    assert.deepEqual(bodies, [{ compact: true, slim: true }, { compact: true }])
    bodies.length = 0
    assert.equal((await run('list_computer_tools', { names: ['read_file'] })).result.result.isError, false)
    assert.deepEqual(bodies, [{ names: ['read_file'] }, {}])
    globalThis.fetch = upToDate
    response = { call_id: 'screenshot', result: { screenshot: 'data:image/jpeg;base64,dGVzdA==', width: 10 }, status: 'ok' }
    const screenshot = (await run('call_computer_tool', { tool_name: 'computer_screenshot', arguments: {} })).result.result
    assert.equal(screenshot.content[0].type, 'image'); assert.equal(screenshot.content[0].data, 'dGVzdA==')
    assert.equal(JSON.parse(screenshot.content[1].text).result.screenshot, undefined)
    // read_file images arrive as MCP image blocks, in order, and leave the JSON text.
    response = { call_id: 'read', result: { resolved_path: '/p/a.png', size: 4, mime: 'image/png', image: true }, images: [{ mimeType: 'image/png', data: 'cG5n' }, { mimeType: 'image/webp', data: 'd2VicA==' }], status: 'success', error: '' }
    const read = (await run('call_computer_tool', { tool_name: 'read_file', arguments: { path: 'a.png' } })).result.result
    assert.equal(read.isError, false)
    assert.deepEqual(read.content.slice(0, 2), [{ type: 'image', mimeType: 'image/png', data: 'cG5n' }, { type: 'image', mimeType: 'image/webp', data: 'd2VicA==' }])
    assert.equal(read.content.length, 3)
    const readText = JSON.parse(read.content[2].text)
    assert.equal(readText.images, undefined); assert.equal(readText.result.resolved_path, '/p/a.png'); assert.ok(!read.content[2].text.includes('cG5n'))
    // Malformed or unsupported entries are not turned into images and stay visible in the text.
    response = { call_id: 'odd', result: {}, images: [{ mimeType: 'image/jpeg', data: 'anBn' }, { mimeType: 'image/svg+xml', data: 'c3Zn' }, { mimeType: 'image/png', data: 5 }, { mimeType: 'image/png', data: '' }, 'x', null], status: 'success' }
    const odd = (await run('call_computer_tool', { tool_name: 'read_file', arguments: { path: 'b' } })).result.result
    assert.deepEqual(odd.content.filter((c: any) => c.type === 'image'), [{ type: 'image', mimeType: 'image/jpeg', data: 'anBn' }])
    assert.deepEqual(JSON.parse(odd.content.at(-1).text).images, [{ mimeType: 'image/svg+xml', data: 'c3Zn' }, { mimeType: 'image/png', data: 5 }, { mimeType: 'image/png', data: '' }, 'x', null])
    // A result with both kinds keeps every image; one with no images is unchanged text.
    response = { call_id: 'both', result: { screenshot: 'data:image/jpeg;base64,c2hvdA==' }, images: [{ mimeType: 'image/gif', data: 'Z2lm' }], status: 'success' }
    const both = (await run('call_computer_tool', { tool_name: 'read_file', arguments: { path: 'c.gif' } })).result.result
    assert.deepEqual(both.content.map((c: any) => c.type + ':' + (c.data || '')), ['image:Z2lm', 'image:c2hvdA==', 'text:'])
    response = { call_id: 'text', result: { content: '     1\thello' }, status: 'success' }
    const text = (await run('call_computer_tool', { tool_name: 'read_file', arguments: { path: 'a.txt' } })).result.result
    assert.equal(text.content.length, 1); assert.equal(JSON.parse(text.content[0].text).result.content, '     1\thello')
    response = { result: { content: [{ type: 'image', mimeType: 'image/png', data: 'dGVzdA==' }], structuredContent: { node: 12 } }, status: 'ok' }
    const chrome = (await run('call_computer_tool', { tool_name: 'chrome_take_screenshot', arguments: {} })).result.result
    assert.equal(chrome.content[0].mimeType, 'image/png'); assert.equal(chrome.structuredContent.node, 12)
    status = 423; response = { error: 'Capability disabled', status: 'denied' }
    assert.equal((await run('call_computer_tool', { tool_name: 'exec_command', arguments: { command: 'pwd' } })).result.result.isError, true)
    let previous = requests.length
    await run('call_computer_tool', { tool_name: '../api/logout', arguments: {} })
    assert.equal(requests.length, previous)
    for (const invalid of [{ ...ready, paused: true }, { ...ready, tunnel: { state: 'stopped' } }, { ...ready, tunnel: { state: 'ready', gateway: 'https://127.0.0.1/AB12cd34' } }, { ...ready, tunnel: { state: 'ready', gateway: 'https://evil.example/AB12cd34' } }]) {
      await call('/api/agent/heartbeat', 'POST', { snapshot: invalid }, device.headers)
      assert.equal((await run('list_computer_tools', {})).result.result.isError, true)
      assert.equal(requests.length, previous)
    }
    await call('/api/agent/heartbeat', 'POST', { snapshot: ready }, device.headers)
    globalThis.fetch = async () => { requests.push({ url: 'failed', init: {} }); throw new Error('timeout') }
    const timedOut = (await run('call_computer_tool', { tool_name: 'write_file', arguments: { path: 'example', content: 'test' } })).result.result
    assert.equal(timedOut.isError, true); assert.match(timedOut.content[0].text, /may have occurred/)
    assert.equal(requests.length, previous + 1)
    globalThis.fetch = async () => new Response(null, { status: 302, headers: { Location: 'https://evil.example/' } })
    assert.match((await run('list_computer_tools', {})).result.result.content[0].text, /redirected/)
    db.prepare('UPDATE devices SET user_id=? WHERE id=?').run('bob', device.id)
    assert.equal((await run('list_computer_tools', {})).result.result.isError, true)
  } finally { globalThis.fetch = originalFetch }
})

// A stand-in for the RelayHub Durable Object namespace. It records what the Worker sends
// and answers /status and /call like the real hub does; it cannot model WebSockets.
function fakeRelay(options: { connected?: boolean; reply?: { http_status: number; body: unknown } | { fail: number; error: string } } = {}) {
  const state = { connected: options.connected ?? true, calls: [] as any[], connects: [] as Request[], closed: [] as string[], names: [] as string[], reply: options.reply ?? { http_status: 200, body: { call_id: 'relay', result: { tools: [] }, status: 'success', error: '' } } }
  env.RELAY = { idFromName: (name: string) => { state.names.push(name); return name }, get: (id: string) => ({ fetch: async (input: RequestInfo, init?: RequestInit) => {
    const req = new Request(input, init), path = new URL(req.url).pathname
    if (req.headers.get('Upgrade') === 'websocket') { state.connects.push(req); return new Response(null, { status: 200, headers: { 'X-Relay-Device': id } }) }
    if (path === '/status') return Response.json({ connected: state.connected })
    if (path === '/close') { state.closed.push(id); return Response.json({ ok: true }) }
    state.calls.push(await req.json())
    if (!state.connected) return Response.json({ error: 'not_connected' }, { status: 409 })
    const reply = state.reply as any
    return 'fail' in reply ? Response.json({ error: reply.error }, { status: reply.fail }) : Response.json(reply)
  } }) } as unknown as DurableObjectNamespace
  return state
}
const relaySnapshot = (state: string, tunnel: Record<string, unknown> = { state: 'stopped' }) => ({ ...snapshot, tunnel, relay: { state, message: 'private-detail', token: 'private-device-secret' } })

test('relay state in a heartbeat is sanitized and reported as relay.state only while online', async () => {
  const d = await register(), token = await access(), path = '/api/v1/computers/' + d.id
  const none = (await call(path, 'GET', undefined, token.headers)).result.computer
  assert.deepEqual(none.relay, { state: 'off' })
  await beat(d, relaySnapshot('connected') as any)
  const stored = JSON.parse(db.prepare('SELECT snapshot FROM devices WHERE id=?').get(d.id)!.snapshot as string)
  assert.deepEqual(stored.relay, { state: 'connected', message: 'private-detail' })
  const online = (await call(path, 'GET', undefined, token.headers)).result.computer
  assert.deepEqual(online.relay, { state: 'connected' }); assert.equal(online.links, null)
  assert.ok(!JSON.stringify(online).includes('private'))
  await beat(d, relaySnapshot('bogus') as any)
  assert.deepEqual((await call(path, 'GET', undefined, token.headers)).result.computer.relay, { state: 'off' })
  await beat(d, relaySnapshot('connected') as any)
  db.prepare('UPDATE devices SET last_seen=? WHERE id=?').run(timestamp() - 61, d.id)
  assert.deepEqual((await call(path, 'GET', undefined, token.headers)).result.computer.relay, { state: 'off' })
})

test('relay can be switched off remotely but never switched on remotely', async () => {
  const d = await register(), token = await access(), path = '/api/v1/computers/' + d.id + '/commands'
  const stop = await call(path, 'POST', { kind: 'relay.stop', payload: {}, request_id: randomToken() }, token.headers)
  assert.equal(stop.response.status, 202); assert.equal(stop.result.kind, 'relay.stop')
  for (const kind of ['relay.start', 'relay.enable', 'relay.set']) assert.equal((await call(path, 'POST', { kind, payload: { enabled: true }, request_id: randomToken() }, token.headers)).response.status, 400)
  assert.equal((await beat(d)).result.command.kind, 'relay.stop')
})

test('the relay socket needs a device credential, an upgrade request and a configured hub', async () => {
  const d = await register(), other = await register(), relay = fakeRelay()
  const connect = (headers: Record<string, string>) => worker.fetch(new Request(origin + '/api/agent/relay', { headers }), env)
  assert.equal((await connect({ Upgrade: 'websocket' })).status, 401)
  assert.equal((await connect({ Upgrade: 'websocket', Authorization: 'Bearer ' + session })).status, 401)
  assert.equal((await connect({ Authorization: d.headers.Authorization })).status, 426)
  assert.equal((await call('/api/agent/relay', 'POST', {}, d.headers)).response.status, 404)
  assert.equal(relay.connects.length, 0)
  const ok = await connect({ Upgrade: 'websocket', Authorization: d.headers.Authorization })
  assert.equal(ok.headers.get('X-Relay-Device'), d.id); assert.deepEqual(relay.names, [d.id])
  assert.equal((await connect({ Upgrade: 'websocket', Authorization: other.headers.Authorization })).headers.get('X-Relay-Device'), other.id)
  delete env.RELAY
  assert.equal((await connect({ Upgrade: 'websocket', Authorization: d.headers.Authorization })).status, 503)
  // A revoked computer loses both its credential and its open socket.
  const live = fakeRelay()
  assert.equal((await call('/api/devices/' + d.id, 'DELETE', undefined, owner())).response.status, 200)
  assert.deepEqual(live.closed, [d.id])
  assert.equal((await connect({ Upgrade: 'websocket', Authorization: d.headers.Authorization })).status, 401)
})

test('cloud MCP uses the relay when no tunnel is usable and says so when it cannot', async () => {
  const device = await register(), client = await newMCPClient(), tokens = await mcpToken(client), relay = fakeRelay()
  const run = (name: string, args: Record<string, unknown>) => rpc(tokens.access_token, 'tools/call', { name, arguments: { computer_id: device.id, ...args } })
  const originalFetch = globalThis.fetch
  globalThis.fetch = async () => { throw new Error('the relay must not use the network') }
  try {
    await beat(device, relaySnapshot('off') as any)
    let blocked = (await run('list_computer_tools', {})).result.result
    assert.equal(blocked.isError, true); assert.match(blocked.content[0].text, /relay mode/)
    assert.equal(relay.calls.length, 0)
    await beat(device, relaySnapshot('connected') as any)
    const listed = (await run('list_computer_tools', {})).result.result
    assert.equal(listed.isError, false)
    assert.deepEqual(relay.calls[0], { tool: 'help', args: { compact: true, slim: true }, session: relay.calls[0].session, client: 'ReadyRig Cloud MCP' })
    assert.ok(relay.calls[0].session.startsWith('cloud-'))
    assert.deepEqual(relay.names.at(-1), device.id)
    relay.reply = { http_status: 200, body: { call_id: 's', result: { screenshot: 'data:image/jpeg;base64,dGVzdA==' }, status: 'success' } }
    const shot = (await run('call_computer_tool', { tool_name: 'computer_screenshot', arguments: { frame: 1 } })).result.result
    assert.equal(shot.content[0].type, 'image'); assert.equal(shot.content[0].data, 'dGVzdA==')
    assert.deepEqual(relay.calls.at(-1).args, { frame: 1 })
    relay.reply = { http_status: 200, body: { call_id: 'r', result: { resolved_path: '/p/a.png', image: true }, images: [{ mimeType: 'image/png', data: 'cG5n' }], status: 'success' } }
    const read = (await run('call_computer_tool', { tool_name: 'read_file', arguments: { path: 'a.png' } })).result.result
    assert.deepEqual(read.content[0], { type: 'image', mimeType: 'image/png', data: 'cG5n' }); assert.equal(JSON.parse(read.content[1].text).images, undefined)
    // A failed call with images still reports the error and the image.
    relay.reply = { http_status: 422, body: { call_id: 'e', result: null, images: [{ mimeType: 'image/png', data: 'cG5n' }], error: 'partial', status: 'error' } }
    const partial = (await run('call_computer_tool', { tool_name: 'read_file', arguments: { path: 'a.png' } })).result.result
    assert.equal(partial.isError, true); assert.equal(partial.content[0].type, 'image'); assert.equal(JSON.parse(partial.content[1].text).error, 'partial')
    relay.reply = { http_status: 423, body: { error: 'Capability disabled', status: 'denied' } }
    assert.equal((await run('call_computer_tool', { tool_name: 'exec_command', arguments: { command: 'pwd' } })).result.result.isError, true)
    const before = relay.calls.length
    await run('call_computer_tool', { tool_name: '../api/logout', arguments: {} })
    assert.equal(relay.calls.length, before)
    for (const [fail, error, expected] of [[504, 'timeout', /may have occurred/], [502, 'disconnected', /dropped during the call/], [409, 'not_connected', /not connected/]] as const) {
      relay.reply = { fail, error }
      const failed = (await run('call_computer_tool', { tool_name: 'write_file', arguments: { path: 'x', content: 'y' } })).result.result
      assert.equal(failed.isError, true); assert.match(failed.content[0].text, expected)
    }
    // A paused or offline computer is refused before anything is sent to the hub.
    const sent = relay.calls.length
    await beat(device, { ...relaySnapshot('connected'), paused: true } as any)
    assert.equal((await run('list_computer_tools', {})).result.result.isError, true)
    db.prepare('UPDATE devices SET last_seen=? WHERE id=?').run(timestamp() - 61, device.id)
    assert.equal((await run('list_computer_tools', {})).result.result.isError, true)
    assert.equal(relay.calls.length, sent)
    // Another account cannot reach a computer's relay.
    db.prepare('UPDATE devices SET last_seen=?,user_id=? WHERE id=?').run(timestamp(), 'bob', device.id)
    assert.equal((await run('list_computer_tools', {})).result.result.isError, true)
    assert.equal(relay.calls.length, sent)
    // Without a configured hub the service reports the relay as unavailable.
    db.prepare('UPDATE devices SET user_id=? WHERE id=?').run('alice', device.id)
    await beat(device, relaySnapshot('connected') as any)
    delete env.RELAY
    assert.match((await run('list_computer_tools', {})).result.result.content[0].text, /not available/)
  } finally { globalThis.fetch = originalFetch }
})

test('a usable tunnel stays preferred; only an unreachable tunnel (530) falls back to the relay', async () => {
  const device = await register(), client = await newMCPClient(), tokens = await mcpToken(client), relay = fakeRelay()
  const gateway = 'https://owned-computer.trycloudflare.com/AB12cd34'
  const run = (name: string, args: Record<string, unknown>) => rpc(tokens.access_token, 'tools/call', { name, arguments: { computer_id: device.id, ...args } })
  const originalFetch = globalThis.fetch
  let gatewayCalls = 0, reply = () => Response.json({ call_id: 'tunnel', result: { ok: true }, status: 'success' })
  globalThis.fetch = async () => { gatewayCalls++; return reply() }
  try {
    await beat(device, relaySnapshot('connected', { state: 'ready', gateway }) as any)
    const direct = (await run('list_computer_tools', {})).result.result
    assert.equal(direct.isError, false); assert.match(direct.content[0].text, /tunnel/)
    assert.equal(gatewayCalls, 1); assert.equal(relay.calls.length, 0)
    // The edge says the connector is gone: the request never reached the computer.
    reply = () => new Response('<html>Error 1033</html>', { status: 530, headers: { 'Content-Type': 'text/html' } })
    const fallback = (await run('list_computer_tools', {})).result.result
    assert.equal(fallback.isError, false); assert.equal(gatewayCalls, 2); assert.equal(relay.calls.length, 1)
    // Other tunnel failures are never replayed over the relay; the call may have run.
    for (const failure of [() => new Response('bad gateway', { status: 502 }), () => { throw new Error('timeout') }]) {
      reply = failure as any
      const failed = (await run('call_computer_tool', { tool_name: 'write_file', arguments: {} })).result.result
      assert.equal(failed.isError, true)
    }
    assert.equal(relay.calls.length, 1)
    // Without a connected relay a 530 stays an error.
    await beat(device, relaySnapshot('off', { state: 'ready', gateway }) as any)
    reply = () => new Response('<html>Error 1033</html>', { status: 530 })
    assert.equal((await run('list_computer_tools', {})).result.result.isError, true)
    assert.equal(relay.calls.length, 1)
    // A tunnel host that is not approved falls back to a connected relay instead of failing.
    await beat(device, relaySnapshot('connected', { state: 'ready', gateway: 'https://evil.example/AB12cd34' }) as any)
    const before = gatewayCalls
    assert.equal((await run('list_computer_tools', {})).result.result.isError, false)
    assert.equal(gatewayCalls, before); assert.equal(relay.calls.length, 2)
  } finally { globalThis.fetch = originalFetch }
})

test('a standby relay is reported but never used; the tunnel stays the only path while it works', async () => {
  const device = await register(), client = await newMCPClient(), tokens = await mcpToken(client), relay = fakeRelay(), token = await access()
  const gateway = 'https://owned-computer.trycloudflare.com/AB12cd34'
  const run = (name: string, args: Record<string, unknown>) => rpc(tokens.access_token, 'tools/call', { name, arguments: { computer_id: device.id, ...args } })
  const originalFetch = globalThis.fetch
  let gatewayCalls = 0
  globalThis.fetch = async () => { gatewayCalls++; return Response.json({ call_id: 'tunnel', result: { ok: true }, status: 'success' }) }
  try {
    await beat(device, relaySnapshot('standby', { state: 'ready', gateway }) as any)
    assert.deepEqual((await call('/api/v1/computers/' + device.id, 'GET', undefined, token.headers)).result.computer.relay, { state: 'standby' })
    assert.equal((await run('list_computer_tools', {})).result.result.isError, false)
    assert.equal(gatewayCalls, 1); assert.equal(relay.calls.length, 0)
    // Standby holds no socket, so with no tunnel link there is nothing to relay through.
    await beat(device, relaySnapshot('standby') as any)
    const none = (await run('list_computer_tools', {})).result.result
    assert.equal(none.isError, true); assert.equal(relay.calls.length, 0); assert.equal(gatewayCalls, 1)
  } finally { globalThis.fetch = originalFetch }
})

test('relay.stop is accepted for a standby relay', async () => {
  const d = await register(), token = await access()
  await beat(d, relaySnapshot('standby') as any)
  const stop = await call('/api/v1/computers/' + d.id + '/commands', 'POST', { kind: 'relay.stop', payload: {}, request_id: randomToken() }, token.headers)
  assert.equal(stop.response.status, 202)
})

test('privacy.set: the console turns it on and off, agents can only turn it on', async () => {
  const d = await register(), token = await access()
  const send = (path: string, enabled: boolean, headers: Record<string, string>) => call(path, 'POST', { kind: 'privacy.set', payload: { enabled }, request_id: randomToken() }, headers)
  const fromConsole = (enabled: boolean) => send('/api/devices/' + d.id + '/commands', enabled, owner())
  const fromAgent = (enabled: boolean) => send('/api/v1/computers/' + d.id + '/commands', enabled, token.headers)
  assert.equal((await fromAgent(true)).response.status, 202)
  assert.equal((await fromAgent(false)).response.status, 403)
  assert.equal((await fromConsole(false)).response.status, 202)
  assert.equal((await fromConsole(true)).response.status, 202)
  await beat(d, { ...relaySnapshot('off'), privacy: { enabled: true } } as any)
  assert.deepEqual((await call('/api/v1/computers/' + d.id, 'GET', undefined, token.headers)).result.computer.privacy, { enabled: true })
})
