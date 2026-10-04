// An isolated integration fixture: only local D1, a generated test account, and
// a harmless connector process. This never opens a real public tunnel.
import { spawn, execFileSync } from 'node:child_process'
import { createServer } from 'node:http'
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { createHash, randomBytes, randomUUID } from 'node:crypto'
import assert from 'node:assert/strict'

const root = resolve(import.meta.dirname, '..'), cloud = join(root, 'cloud')
const dir = mkdtempSync(join(tmpdir(), 'readyrig-cloud-test-')), children = []
const workerOrigin = 'http://localhost:18787', appOrigin = 'http://127.0.0.1:17431'
const session = randomBytes(32).toString('base64url'), sessionHash = createHash('sha256').update(session).digest('hex')
const discoverySession = randomBytes(32).toString('base64url'), discoverySessionHash = createHash('sha256').update(discoverySession).digest('hex')
const mcpToken = randomBytes(32).toString('base64url'), sha256 = value => createHash('sha256').update(value).digest('hex')
const wrangler = join(cloud, 'node_modules/wrangler/bin/wrangler.js'), persist = join(dir, 'worker')
let appKey, helper, deviceID
const delay = ms => new Promise(resolve => setTimeout(resolve, ms))
async function waitFor(fn, label, timeout = 45000) {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) { try { const result = await fn(); if (result) return result } catch {} await delay(300) }
  throw new Error('Timed out: ' + label)
}
function start(command, args, options = {}) {
  const child = spawn(command, args, { cwd: root, detached: process.platform !== 'win32', stdio: ['ignore', 'pipe', 'pipe'], ...options }); children.push(child); return child
}
function wranglerSync(args) { return execFileSync(process.execPath, [wrangler, ...args], { cwd: cloud, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }) }
async function request(origin, path, method = 'GET', data, headers = {}) {
  const response = await fetch(origin + path, { method, headers: { ...headers, ...(data === undefined ? {} : { 'Content-Type': 'application/json' }) }, body: data === undefined ? undefined : JSON.stringify(data) })
  const result = await response.json()
  assert.ok(response.ok, `HTTP ${response.status}: ${result.error || path}`); return result
}
const owner = { Cookie: 'readyrig_session=' + session, Origin: workerOrigin }
let localCookie
const local = (path, method = 'GET', data) => request(appOrigin, path, method, data, { Cookie: localCookie, Origin: appOrigin })
const remote = (path, method = 'GET', data) => request(workerOrigin, path, method, data, owner)
try {
  wranglerSync(['d1', 'migrations', 'apply', 'DB', '--local', '--persist-to', persist])
  const seed = `INSERT INTO users VALUES('integration-user','integration@example.invalid','Integration Test',unixepoch()); INSERT INTO sessions VALUES('${sessionHash}','integration-user',unixepoch()+3600); INSERT INTO sessions VALUES('${discoverySessionHash}','integration-user',unixepoch()+3600); INSERT INTO mcp_clients(id,user_id,name,redirect_uris,created_at) VALUES('integration-client','integration-user','Integration MCP','[]',unixepoch()); INSERT INTO mcp_grants VALUES('integration-grant','integration-client','integration-user','${sha256(mcpToken)}','${sha256('unused-' + mcpToken)}',unixepoch()+3600,unixepoch()+3600);`
  const seedPath = join(dir, 'seed.sql'); writeFileSync(seedPath, seed, { mode: 0o600 })
  wranglerSync(['d1', 'execute', 'DB', '--local', '--persist-to', persist, '--file', seedPath])
  const worker = start(process.execPath, [wrangler, 'dev', '--port', '18787', '--local-upstream', 'localhost:18787', '--persist-to', persist, '--var', 'PUBLIC_ORIGIN:' + workerOrigin], { cwd: cloud })
  let workerLog = ''; worker.stdout.on('data', data => { workerLog += data }); worker.stderr.on('data', data => { workerLog += data })
  await waitFor(() => request(workerOrigin, '/api/health'), 'Worker startup').catch(error => { throw new Error(error.message + '\n' + workerLog) })
  const binary = join(dir, 'readyrig'); execFileSync('go', ['build', '-tags', 'nogui', '-o', binary, './cmd/adapter'], { cwd: root, stdio: 'pipe' })
  const connector = join(dir, 'connector'); writeFileSync(connector, "#!/bin/sh\necho 'https://readyrig-integration-fixture.trycloudflare.com'\necho 'INF Registered tunnel connection'\nexec sleep 600\n", { mode: 0o700 })
  mkdirSync(join(dir, 'workspace'))
  const app = start(binary, ['web', '--foreground', '--workspace', join(dir, 'workspace'), '--data-dir', join(dir, 'data'), '--ui', '127.0.0.1:17431', '--gateway', '127.0.0.1:17432', '--cloud-url', workerOrigin, '--cloudflared', connector, '--no-chrome', '--no-update'])
  app.stdout.on('data', data => { const match = String(data).match(/#key=([a-f0-9]+)/); if (match) appKey = match[1] })
  await waitFor(() => appKey, 'App startup')
  const login = await fetch(appOrigin + '/api/login', { method: 'POST', headers: { 'Content-Type': 'application/json', Origin: appOrigin }, body: JSON.stringify({ key: appKey }) })
  assert.equal(login.status, 200); localCookie = login.headers.get('set-cookie').split(';')[0]
  const pair = await local('/api/cloud/login', 'POST', { url: workerOrigin, name: 'Integration Mac' })
  const pairID = new URL(pair.login_url).searchParams.get('pair')
  assert.equal((await remote('/api/pairings/' + pairID)).code, pair.code)
  await remote('/api/pairings/' + pairID, 'POST', {})
  await waitFor(async () => { const status = await local('/api/cloud'); deviceID = status.device_id; return status.state === 'online' }, 'Device registration')
  assert.equal((await remote('/api/devices')).devices[0].online, true)
  console.log('PASS: app login → device registration → real D1 heartbeat')
  const controller = await remote('/api/discovery-token', 'POST', {})
  const controlHeaders = { Authorization: 'Bearer ' + controller.token }
  const control = (path, method = 'GET', data) => request(workerOrigin, path, method, data, controlHeaders)
  const commandPath = '/api/v1/computers/' + deviceID + '/commands'
  async function command(kind, payload, check) {
    const input = { kind, payload, request_id: randomUUID() }
    const cmd = await control(commandPath, 'POST', input)
    assert.equal(cmd.status, 'queued')
    assert.equal((await control(commandPath, 'POST', input)).id, cmd.id)
    const browserHistory = await remote('/api/devices/' + deviceID + '/commands')
    assert.ok(browserHistory.commands.some(c => c.id === cmd.id))
    const receipt = await waitFor(async () => { const history = await control(commandPath); const result = history.commands.find(c => c.id === cmd.id); return result && ['completed', 'failed', 'expired', 'revoked'].includes(result.status) ? result : null }, kind)
    assert.equal(receipt.status, 'completed', receipt.error || receipt.status)
    await check(await local('/api/state'))
    console.log('PASS: Bearer ' + kind + ' → app execution → shared cloud receipt')
  }
  await command('tunnel.start', { mode: 'quick' }, state => assert.equal(state.tunnel.state, 'ready'))
  const discoveryOwner = { Cookie: 'readyrig_session=' + discoverySession, Origin: workerOrigin }
  const grant = await request(workerOrigin, '/api/discovery-token', 'POST', {}, discoveryOwner)
  const agentHeaders = { Authorization: 'Bearer ' + grant.token }
  const agent = (path, method = 'GET', data) => request(workerOrigin, path, method, data, agentHeaders)
  const computers = await agent('/api/v1/computers')
  assert.deepEqual(computers.computers.map(c => c.id), [deviceID])
  const linked = await waitFor(async () => { const result = await agent('/api/v1/computers/' + deviceID); return result.computer.links && result.computer }, 'Agent link discovery')
  assert.equal(linked.links.mcp, linked.links.gateway + '/mcp')
  // The fixture connector never opens a public tunnel. Reach the same gateway
  // locally to verify direct computer calls need no Bearer token.
  const accessPath = new URL(linked.links.gateway).pathname
  const direct = (tool = 'help', args = {}) => request('http://127.0.0.1:17432', accessPath + '/api/v1/tools/' + tool, 'POST', args)
  assert.ok((await direct()).result.tools.length > 0)
  await request(workerOrigin, '/api/logout', 'POST', {}, discoveryOwner)
  assert.equal((await fetch(workerOrigin + '/api/v1/computers', { headers: agentHeaders })).status, 401)
  assert.equal((await fetch(workerOrigin + commandPath, { method: 'POST', headers: { ...agentHeaders, 'Content-Type': 'application/json' }, body: JSON.stringify({ kind: 'tunnel.stop', payload: {}, request_id: randomUUID() }) })).status, 401)
  assert.ok((await direct()).result.tools.length > 0)
  console.log('PASS: logout stops Bearer queries and controls while leaving the public link usable')
  await command('capability.set', { category: 'terminal', enabled: true }, state => assert.equal(state.enabled.terminal, true))
  const shellResult = await direct('exec_command', { command: "printf 'readyrig-shell-control-fixture'", yield_time_ms: 1000 })
  assert.ok(JSON.stringify(shellResult.result).includes('readyrig-shell-control-fixture'))
  console.log('PASS: enabling shell over the Bearer API permits a direct shell call without token')
  await command('control.pause', { paused: true }, state => assert.equal(state.paused, true))
  await command('control.pause', { paused: false }, state => assert.equal(state.paused, false))
  await command('tunnel.stop', {}, state => assert.equal(state.tunnel.state, 'stopped'))
  await remote('/api/devices/' + deviceID, 'PATCH', { name: 'Renamed Integration Mac' })
  await waitFor(async () => (await local('/api/cloud')).name === 'Renamed Integration Mac', 'Rename heartbeat')
  console.log('PASS: device rename reflected in app')
  // Relay mode: the real RelayHub Durable Object, the real WebSocket client and the real tools.
  // The tunnel is stopped here, so these calls can only succeed through the relay.
  const mcp = async (name, args) => {
    const response = await fetch(workerOrigin + '/mcp', { method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + mcpToken }, body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name, arguments: { computer_id: deviceID, ...args } } }) })
    assert.equal(response.status, 200); return (await response.json()).result
  }
  const relayOf = async () => (await local('/api/cloud')).relay
  assert.deepEqual(await relayOf(), { enabled: false, state: 'off', message: '' })
  const noRelay = await mcp('list_computer_tools', {})
  assert.equal(noRelay.isError, true); assert.match(noRelay.content[0].text, /relay mode/)
  const refused = await fetch(appOrigin + '/api/cloud/relay', { method: 'POST', headers: { 'Content-Type': 'application/json', Cookie: localCookie, Origin: appOrigin }, body: JSON.stringify({ enabled: true }) })
  assert.equal(refused.status, 400); assert.equal((await relayOf()).enabled, false)
  await local('/api/cloud/relay', 'POST', { enabled: true, acknowledged: true })
  await waitFor(async () => (await relayOf()).state === 'connected', 'Relay WebSocket connection')
  await waitFor(async () => (await remote('/api/devices')).devices[0].snapshot.relay?.state === 'connected', 'Relay state heartbeat')
  const relayed = await mcp('list_computer_tools', {})
  assert.equal(relayed.isError, false); assert.ok(JSON.parse(relayed.content[0].text).result.tools.length > 0)
  const shell = await mcp('call_computer_tool', { tool_name: 'exec_command', arguments: { command: "printf 'readyrig-relay-fixture'", yield_time_ms: 1000 } })
  assert.equal(shell.isError, false); assert.ok(shell.content[0].text.includes('readyrig-relay-fixture'))
  // A large payload crosses the socket intact (the tool-argument direction carries file contents).
  const big = 'readyrig-relay-payload-'.repeat(40000).slice(0, 900000)
  const written = await mcp('call_computer_tool', { tool_name: 'write_file', arguments: { path: 'relay-big.txt', content: big } })
  assert.equal(written.isError, false, written.content[0].text)
  assert.equal(readFileSync(join(dir, 'workspace', 'relay-big.txt'), 'utf8'), big)
  assert.ok((await local('/api/state')).sessions.some(s => String(s.session || s.id || JSON.stringify(s)).includes('cloud-integration-grant')))
  console.log('PASS: relay mode (opt-in) → MCP tool calls over WebSocket with the tunnel stopped')
  // The cloud can turn relay off but has no command that turns it on.
  const startRelay = await fetch(workerOrigin + commandPath, { method: 'POST', headers: { ...controlHeaders, 'Content-Type': 'application/json' }, body: JSON.stringify({ kind: 'relay.start', payload: { enabled: true }, request_id: randomUUID() }) })
  assert.equal(startRelay.status, 400)
  await command('relay.stop', {}, state => assert.equal(state.cloud.relay.enabled, false))
  await waitFor(async () => (await relayOf()).state === 'off', 'Relay turned off')
  const stopped = await mcp('list_computer_tools', {})
  assert.equal(stopped.isError, true)
  console.log('PASS: relay.stop from the cloud closes the socket; relay cannot be enabled remotely')
  await local('/api/cloud/relay', 'POST', { enabled: true, acknowledged: true })
  await waitFor(async () => (await relayOf()).state === 'connected', 'Relay reconnection')
  // The tunnel is the main path: once it works the relay drops its connection and stands by,
  // and it comes back when the tunnel stops.
  await command('tunnel.start', { mode: 'quick' }, state => assert.equal(state.tunnel.state, 'ready'))
  await waitFor(async () => (await relayOf()).state === 'standby', 'Relay standby while the tunnel works')
  assert.equal((await relayOf()).enabled, true)
  await command('tunnel.stop', {}, state => assert.equal(state.tunnel.state, 'stopped'))
  await waitFor(async () => (await relayOf()).state === 'connected', 'Relay reconnects when the tunnel stops')
  console.log('PASS: relay stands by while the tunnel works and reconnects when it stops')
  helper = createServer((req, res) => {
    if (req.url === '/app') { res.writeHead(302, { Location: appOrigin + '/#key=' + appKey }); res.end(); return }
    res.writeHead(302, { 'Set-Cookie': `readyrig_session=${session}; Path=/; HttpOnly; SameSite=Lax; Max-Age=3600`, Location: workerOrigin + '/console?lang=zh-CN' }); res.end()
  }).listen(18789, 'localhost')
  console.log('Browser fixtures: http://localhost:18789/cloud and http://localhost:18789/app')
  if (process.argv.includes('--keep')) { console.log('Fixtures kept for visual verification. Stop to clean up.'); await new Promise(resolve => { process.once('SIGINT', resolve); process.once('SIGTERM', resolve) }) }
  await remote('/api/devices/' + deviceID, 'DELETE')
  await waitFor(async () => (await local('/api/cloud')).state === 'revoked', 'Credential revocation')
  await waitFor(async () => (await relayOf()).state !== 'connected', 'Relay socket closed on unbinding')
  console.log('PASS: unbinding revokes device credentials and closes the relay socket')
} finally {
  helper?.close()
  for (const child of children.reverse()) { try { if (process.platform === 'win32') child.kill('SIGTERM'); else process.kill(-child.pid, 'SIGTERM') } catch {} }
  await delay(1000)
  rmSync(dir, { recursive: true, force: true })
}
