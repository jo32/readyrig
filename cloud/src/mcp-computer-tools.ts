import type { Env } from './index.ts'
import { HTTPError } from './http.ts'
import { computer, summary } from './computer-discovery.ts'
import { relayCall } from './relay.ts'

type Identity = { user_id: string; grant_id?: string }
type ToolResult = { content: Record<string, unknown>[]; isError: boolean; structuredContent?: unknown }
const maxResponseBytes = 8 * 1024 * 1024
const imageTypes = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp'])

// Only use the freshly authenticated owner's heartbeat URL. Arbitrary tool
// arguments can never select a URL, header, redirect or management endpoint.
export function permittedGateway(gateway: string, env: Env): boolean {
  const url = new URL(gateway)
  const quickTunnel = /^[a-z0-9]+(?:-[a-z0-9]+)*\.trycloudflare\.com$/.test(url.hostname)
  const fixedHosts = (env.MCP_ALLOWED_TUNNEL_HOSTS || '').split(',').map(h => h.trim().toLowerCase()).filter(Boolean)
  return url.protocol === 'https:' && !url.port && !url.username && !url.password && !url.search && !url.hash && /^\/[A-Za-z0-9]{8}$/.test(url.pathname) && (quickTunnel || fixedHosts.includes(url.hostname))
}
async function readResult(response: Response): Promise<unknown> {
  if (Number(response.headers.get('content-length') || 0) > maxResponseBytes) { await response.body?.cancel(); throw new HTTPError(502, 'Computer response is too large; request a smaller result') }
  const reader = response.body?.getReader()
  if (!reader) throw new HTTPError(502, 'Computer returned an empty response')
  let size = 0, value = ''; const decoder = new TextDecoder()
  while (true) {
    const chunk = await reader.read(); if (chunk.done) break
    size += chunk.value.length
    if (size > maxResponseBytes) { await reader.cancel(); throw new HTTPError(502, 'Computer response is too large; request a smaller result') }
    value += decoder.decode(chunk.value, { stream: true })
  }
  try { return JSON.parse(value + decoder.decode()) } catch { throw new HTTPError(502, 'Computer returned an invalid response') }
}
function object(value: unknown): value is Record<string, unknown> { return !!value && typeof value === 'object' && !Array.isArray(value) }

type Raw = { ok: boolean; output: Record<string, unknown> }

// A Cloudflare tunnel whose connector is gone answers 530 from its edge: the request never
// reached the computer, so with a connected relay it is safe to send it there instead.
async function viaGateway(gateway: string, session: string, name: string, args: Record<string, unknown>, fallback: boolean): Promise<Raw | null> {
  const headers = { 'Content-Type': 'application/json', 'X-Session-ID': session, 'X-Client-Name': 'ReadyRig Cloud MCP' }
  let response: Response, output: unknown
  try {
    response = await fetch(gateway + '/api/v1/tools/' + name, { method: 'POST', headers, body: JSON.stringify(args), redirect: 'manual', signal: AbortSignal.timeout(55000) })
    if (response.status >= 300 && response.status < 400) { await response.body?.cancel(); throw new HTTPError(502, 'Computer URL redirected. Wait for a fresh heartbeat; this call was not retried.') }
    if (response.status === 530 && fallback) { await response.body?.cancel(); return null }
    output = await readResult(response)
  } catch (error) {
    if (error instanceof HTTPError) throw error
    // The tool may already have executed. Never replay a mutating request.
    throw new HTTPError(502, 'Computer connection failed or timed out. Execution may have occurred. Check local activity or an existing command session before retrying.')
  }
  if (!object(output)) throw new HTTPError(502, 'Computer returned an invalid tool response')
  return { ok: response.ok, output }
}

// Runs a tool and returns the computer's own response. The tunnel comes first. The opt-in
// relay (connected only while the tunnel is not working) is used when there is no usable
// tunnel link, or when the tunnel edge reports 530. Any other tunnel failure is never replayed
// over the relay, because the call may already have run.
async function computerToolRaw(env: Env, identity: Identity, deviceID: string, name: string, args: Record<string, unknown>): Promise<Raw> {
  if (!/^[A-Za-z][A-Za-z0-9_]{0,127}$/.test(name)) throw new HTTPError(400, 'Invalid computer tool name')
  const device = summary(await computer(env, identity, deviceID))
  if (!device.online) throw new HTTPError(409, 'Computer is offline. Open ReadyRig and keep the computer awake.')
  if (device.paused) throw new HTTPError(423, 'Computer control is paused. Resume control only if authorized by the user.')
  const relayReady = device.relay.state === 'connected', session = 'cloud-' + (identity.grant_id || 'api')
  const tunnelUsable = !!device.links && permittedGateway(device.links.gateway, env)
  if (!tunnelUsable && !relayReady) {
    if (!device.links) throw new HTTPError(409, 'Public sharing is not ready. Start a quick tunnel with control_computer and wait for a ready connection. If a tunnel is not possible, the user can turn on relay mode in ReadyRig on that computer (tool data then passes through ReadyRig Cloud).')
    throw new HTTPError(403, 'This tunnel host is not enabled for cloud tool calls. Use a quick tunnel, or ask the service administrator to allow this fixed hostname.')
  }
  const raw = tunnelUsable ? await viaGateway(device.links!.gateway, session, name, args, relayReady) : null
  if (raw) return raw
  const reply = await relayCall(env, device.id, { tool: name, args, session, client: 'ReadyRig Cloud MCP' })
  if (!object(reply.body)) throw new HTTPError(502, 'Computer returned an invalid tool response')
  return { ok: reply.http_status >= 200 && reply.http_status < 300, output: reply.body }
}
export async function computerTool(env: Env, identity: Identity, deviceID: string, name: string, args: Record<string, unknown>): Promise<ToolResult> {
  const { ok, output } = await computerToolRaw(env, identity, deviceID, name, args)
  const failed = !ok || !!output.error || ['denied', 'error'].includes(String(output.status))
  const value = output.result
  // Chrome tools already contain MCP text/image/structured result blocks.
  if (object(value) && Array.isArray(value.content)) {
    return { content: value.content, ...(value.structuredContent !== undefined ? { structuredContent: value.structuredContent } : {}), isError: failed || value.isError === true }
  }
  const content: Record<string, unknown>[] = []
  // read_file returns images beside the result. Send them as MCP image blocks, not as
  // base64 inside the JSON text; anything that is not a well-formed image stays there.
  if (Array.isArray(output.images)) {
    const rest = output.images.filter(image => {
      if (!object(image) || typeof image.mimeType !== 'string' || !imageTypes.has(image.mimeType) || typeof image.data !== 'string' || !image.data) return true
      content.push({ type: 'image', mimeType: image.mimeType, data: image.data })
      return false
    })
    if (rest.length) output.images = rest
    else delete output.images
  }
  if (object(value) && typeof value.screenshot === 'string' && value.screenshot.startsWith('data:image/jpeg;base64,')) {
    content.push({ type: 'image', mimeType: 'image/jpeg', data: value.screenshot.slice('data:image/jpeg;base64,'.length) })
    delete value.screenshot
  }
  content.push({ type: 'text', text: JSON.stringify(output) })
  return { content, isError: failed }
}
