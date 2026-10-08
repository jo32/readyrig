import type { Env } from './index.ts'
import { body, HTTPError, json } from './http.ts'
import { computerTool } from './mcp-computer-tools.ts'
import { mcpIdentity } from './mcp-oauth.ts'
import { computer, computers, summary } from './computer-discovery.ts'
import { commandHistory, queueCommand } from './computer-commands.ts'

const idSchema = { type: 'string', pattern: '^[A-Za-z0-9_-]{43}$', description: 'Computer ID from list_computers' }
const tools = [
  { name: 'list_computer_tools', description: 'Discover enabled file, terminal, browser and desktop tools on a computer. By default returns a compact list (names and one-line descriptions; mutating/parallel shown only when true, enabled/available only when false); then pass names to get the exact argument schemas of just the tools you need, or compact=false for every schema. category narrows the list. Call this before call_computer_tool. Requires the computer online with public sharing ready or relay mode connected.', inputSchema: { type: 'object', properties: { computer_id: idSchema, names: { type: 'array', items: { type: 'string' }, maxItems: 40, description: 'Tool names to return full schemas for; unknown names come back in unknown' }, compact: { type: 'boolean', description: 'Default true unless names is given; false returns every full schema (large)' }, category: { type: 'string', description: 'Only list tools in this category, for example files, terminal, computer, browser or safari' } }, required: ['computer_id'], additionalProperties: false }, annotations: { readOnlyHint: true, destructiveHint: false, openWorldHint: true } },
  { name: 'call_computer_tool', description: 'Run an enabled tool on an owned computer through ReadyRig Cloud. Use list_computer_tools first for exact tool names and arguments. Local folder, capability, pause and system permissions are enforced by the computer. Requires online public sharing, or relay mode connected when no tunnel is usable. Calls are not retried; after a timeout, inspect activity before repeating writes. For long-running exec_command use a short yield_time_ms, then call write_stdin with the returned session_id.', inputSchema: { type: 'object', properties: { computer_id: idSchema, tool_name: { type: 'string', pattern: '^[A-Za-z][A-Za-z0-9_]{0,127}$' }, arguments: { type: 'object', additionalProperties: true, description: 'Arguments matching the tool schema returned by list_computer_tools' } }, required: ['computer_id', 'tool_name', 'arguments'], additionalProperties: false }, annotations: { readOnlyHint: false, destructiveHint: true, openWorldHint: true } },
  { name: 'list_computers', description: 'List your ReadyRig computers, online status, permissions and available direct connection URLs.', inputSchema: { type: 'object', properties: {}, additionalProperties: false } },
  { name: 'get_computer', description: 'Get current status and direct connection URLs for one computer.', inputSchema: { type: 'object', properties: { computer_id: idSchema }, required: ['computer_id'], additionalProperties: false } },
  { name: 'computer_commands', description: 'Check whether a queued computer setting change completed. Do not blindly retry uncertain actions.', inputSchema: { type: 'object', properties: { computer_id: idSchema }, required: ['computer_id'], additionalProperties: false } },
  { name: 'control_computer', description: 'Queue a sharing, tool-permission or pause change on your computer. Delivery usually takes 15 seconds. Check computer_commands for completion. Local folder and Full Access permissions cannot be changed here.', inputSchema: { type: 'object', properties: { computer_id: idSchema, request_id: { type: 'string', minLength: 1, maxLength: 64, description: 'Unique operation ID; reuse only for an identical retry' }, kind: { type: 'string', enum: ['tunnel.start', 'tunnel.stop', 'relay.stop', 'privacy.set', 'control.pause', 'capability.set'] }, payload: { type: 'object', description: 'tunnel.start: {mode:"quick"|"fixed"}; tunnel.stop: {}; relay.stop: {} (turns relay mode off; it can only be turned on at the computer); privacy.set: {enabled:true} (hides local paths from agents; only the user can turn it off); control.pause: {paused:boolean}; capability.set: {category:"files"|"terminal"|"computer"|"browser",enabled:boolean}' } }, required: ['computer_id', 'request_id', 'kind', 'payload'], additionalProperties: false } },
]
export async function cloudMCP(req: Request, env: Env): Promise<Response> {
  if (req.headers.has('Origin') && req.headers.get('Origin') !== env.PUBLIC_ORIGIN) throw new HTTPError(403, 'Invalid origin')
  const identity = await mcpIdentity(req, env)
  if (!identity) {
    const response = json({ error: 'unauthorized' }, 401)
    response.headers.set('WWW-Authenticate', `Bearer resource_metadata="${env.PUBLIC_ORIGIN}/.well-known/oauth-protected-resource/mcp", scope="computers:control"`)
    return response
  }
  if (req.method !== 'POST') return new Response(null, { status: 405, headers: { Allow: 'POST' } })
  if (req.headers.has('MCP-Protocol-Version') && !['2025-03-26', '2025-06-18', '2025-11-25'].includes(req.headers.get('MCP-Protocol-Version')!)) return json({ error: 'Unsupported protocol version' }, 400)
  let msg: Record<string, unknown>
  try { msg = await body(req, 2 * 1024 * 1024) } catch (e) { if (e instanceof HTTPError && e.status === 400) return json({ jsonrpc: '2.0', id: null, error: { code: -32700, message: 'Parse error' } }, 400); throw e }
  const id = msg.id ?? null
  const error = (code: number, message: string) => json({ jsonrpc: '2.0', id, error: { code, message } })
  const result = (result: unknown) => json({ jsonrpc: '2.0', id, result })
  if (msg.jsonrpc !== '2.0' || typeof msg.method !== 'string' || (msg.id !== undefined && typeof msg.id !== 'string' && typeof msg.id !== 'number')) return error(-32600, 'Invalid request')
  if (msg.params !== undefined && (!msg.params || typeof msg.params !== 'object' || Array.isArray(msg.params))) return error(-32602, 'Invalid params')
  if (msg.id === undefined) return new Response(null, { status: 202 })
  const params = (msg.params || {}) as Record<string, unknown>
  if (msg.method === 'initialize') return result({ protocolVersion: ['2025-03-26', '2025-06-18', '2025-11-25'].includes(String(params.protocolVersion)) ? params.protocolVersion : '2025-06-18', capabilities: { tools: {} }, serverInfo: { name: 'readyrig-cloud', version: '1.0.0' }, instructions: 'Use list_computers to select an owned computer. If sharing is stopped and the user authorized access, control_computer can start a quick tunnel; poll get_computer until ready. A computer whose relay.state is connected can be used without a tunnel; the user turned that on locally and tool data then passes through ReadyRig Cloud. Use list_computer_tools to discover exact tool names and schemas, then call_computer_tool to execute file, terminal, browser or desktop work through this MCP connection. Tool arguments and results are relayed by ReadyRig Cloud. Stay within user-authorized tasks. Never bypass local folder, capability, pause or system permissions. Always get a fresh screenshot before coordinate actions. Terminal commands run on the computer without an OS sandbox. On network timeouts, execution is uncertain; do not automatically repeat writes. With privacy mode on, results show local paths as ${RR_*} tokens; use them unchanged in arguments.' })
  if (msg.method === 'ping') return result({})
  if (msg.method === 'tools/list') return result({ tools })
  if (msg.method !== 'tools/call') return error(-32601, 'Method not found')
  const tool = tools.find(t => t.name === params.name)
  if (!tool) return error(-32602, 'Unknown tool')
  const args = params.arguments === undefined ? {} : params.arguments
  if (!args || typeof args !== 'object' || Array.isArray(args)) return error(-32602, 'Invalid arguments')
  const input = args as Record<string, unknown>
  if (Object.keys(input).some(key => !Object.hasOwn(tool.inputSchema.properties, key))) return error(-32602, 'Unknown argument')
  if (params.name !== 'list_computers' && (typeof input.computer_id !== 'string' || !/^[A-Za-z0-9_-]{43}$/.test(input.computer_id))) return error(-32602, 'Invalid computer_id')
  try {
    if (params.name === 'list_computer_tools') {
      const { names, compact, category } = input
      if ((names !== undefined && (!Array.isArray(names) || names.length > 40 || names.some(n => typeof n !== 'string'))) || (compact !== undefined && typeof compact !== 'boolean') || (category !== undefined && typeof category !== 'string')) return error(-32602, 'Invalid arguments')
      // Compact by default: the full catalogue is tens of kilobytes and agents use a handful of tools.
      const help: Record<string, unknown> = {}
      if (Array.isArray(names) && names.length) help.names = names
      else if (compact !== false) { help.compact = true; help.slim = true }
      if (category) help.category = category
      // Apps older than these options reject them as unknown arguments. help is read-only, so
      // retry with what an older app understands: the plain compact list, then the full list.
      const attempts: Record<string, unknown>[] = [help]
      if (help.slim) attempts.push({ compact: true })
      if (Object.keys(help).length) attempts.push({})
      let listed!: Awaited<ReturnType<typeof computerTool>>
      for (const attempt of attempts) {
        listed = await computerTool(env, identity, input.computer_id as string, 'help', attempt)
        if (!listed.isError || !/unknown argument/i.test(JSON.stringify(listed.content))) break
      }
      return result(listed)
    }
    if (params.name === 'call_computer_tool') {
      if (typeof input.tool_name !== 'string' || !input.arguments || typeof input.arguments !== 'object' || Array.isArray(input.arguments)) return error(-32602, 'Provide tool_name and arguments object')
      return result(await computerTool(env, identity, input.computer_id as string, input.tool_name, input.arguments as Record<string, unknown>))
    }
    let output: unknown
    if (params.name === 'list_computers') output = { computers: (await computers(env, identity)).map(summary) }
    else {
      const device = await computer(env, identity, input.computer_id as string)
      output = params.name === 'get_computer' ? { computer: summary(device) } : params.name === 'computer_commands' ? await commandHistory(env, device.id) : await queueCommand(env, device.id, identity.user_id, input)
    }
    return result({ content: [{ type: 'text', text: JSON.stringify(output) }], isError: false })
  } catch (e) { if (!(e instanceof HTTPError)) throw e; return result({ content: [{ type: 'text', text: e.message }], isError: true }) }
}
