import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register } from 'claude-code'

import type { RpMachine, RpPick, RpProject } from '../types'

const PANE = 'rp'
const machines = atom({ plugin: 'rp', key: 'machines' } as const, [])
const isLoading = atom({ plugin: 'rp', key: 'isLoading' } as const, false)
const error = atom({ plugin: 'rp', key: 'error' } as const, null)
const pick = atom({ plugin: 'rp', key: 'pick' } as const, null)
const query = atom({ plugin: 'rp', key: 'query' } as const, '')

// The ReadyRig MCP tools answer JSON text; pull the object out of it.
function parseJson(text: string | undefined): any {
  if (!text) throw new Error('empty answer')
  const start = text.indexOf('{')
  const end = text.lastIndexOf('}')
  if (start < 0 || end < start) throw new Error(text.slice(0, 200))
  return JSON.parse(text.slice(start, end + 1))
}

async function callTool($: EngineInterface, args: Record<string, unknown>) {
  const ran: any = await $.tool.call(args as any)
  if (ran.deny) throw new Error(ran.deny)
  if (ran.isError) throw new Error(ran.text ?? 'tool failed')
  return parseJson(ran.text)
}

async function loadMachine($: EngineInterface, c: any): Promise<RpMachine> {
  const machine: RpMachine = {
    id: c.id,
    name: c.name,
    platform: c.platform,
    online: Boolean(c.online),
    projects: [],
  }
  if (!machine.online) return { ...machine, error: 'offline' }
  try {
    const out = await callTool($, {
      tool: 'mcp__readyrig__call_computer_tool',
      computer_id: c.id,
      tool_name: 'list_projects',
      arguments: {},
    })
    const projects: RpProject[] = (out.result?.projects ?? []).map((p: any) => ({
      id: String(p.id),
      name: String(p.name),
      path: String(p.path),
    }))
    return { ...machine, projects }
  } catch (err) {
    return { ...machine, error: String((err as Error).message ?? err) }
  }
}

async function load($: EngineInterface) {
  await update($, isLoading, () => true)
  await update($, error, () => null)
  try {
    const out = await callTool($, { tool: 'mcp__readyrig__list_computers' })
    const list = await Promise.all(
      (out.computers ?? []).map((c: any) => loadMachine($, c)),
    )
    await update($, machines, () => list)
  } catch (err) {
    await update($, error, () => String((err as Error).message ?? err))
  }
  await update($, isLoading, () => false)
}

// Every word of the search must appear in the project's name or path, or in
// its machine's name. With a search, machines left without a match are hidden.
function filterMachines(list: RpMachine[], text: string): RpMachine[] {
  const words = text.toLowerCase().split(/\s+/).filter(Boolean)
  if (words.length === 0) return list
  return list
    .map(m => ({
      ...m,
      projects: m.projects.filter(p => {
        const hay = `${p.name} ${p.path} ${m.name}`.toLowerCase()
        return words.every(w => hay.includes(w))
      }),
    }))
    .filter(m => m.projects.length > 0)
}

// The folder's path stays out of the prompt: it can name the user, and the
// project argument is all the tools need. Relative paths resolve inside it.
function contextFor(p: RpPick): string {
  return (
    `[ReadyRig project: "${p.project.name}" on machine "${p.machineName}" ` +
    `(computer_id: ${p.machineId}). Use this machine for this request: pass ` +
    `project: "${p.project.id}" to its tools and use paths relative to the project.]`
  )
}

function showStatus($: EngineInterface, p: RpPick | null) {
  $.ui.status(p ? `rp: ${p.project.name} @ ${p.machineName}` : undefined)
}

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    await $.command.register({
      name: 'rp',
      description: 'Pick a ReadyRig project (grouped by machine) for your next prompt',
      argumentHint: '[clear]',
    })
    showStatus($, await read($, pick))

    return next(e)
  })

  on('command.run', { command: 'rp' }, async ($, e) => {
    if (e.args.trim() === 'clear') {
      await update($, pick, () => null)
      showStatus($, null)
      await $.ui.close({ id: PANE })

      return { text: 'ReadyRig project cleared.' }
    }

    await update($, query, () => '')
    await $.ui.open({
      id: PANE,
      title: 'ReadyRig projects',
      focus: true,
      closeOnEscape: true,
    })
    await load($)

    return { text: 'Pick a ReadyRig project in the pane (Esc to cancel).' }
  })

  // One shot: the picked project rides on the next prompt, then is cleared.
  on('prompt.submit', async ($, e, next) => {
    const picked = await read($, pick)
    if (!picked || e.text.trimStart().startsWith('/')) return next(e)

    await update($, pick, () => null)
    showStatus($, null)

    return next({ ...e, text: `${e.text}\n\n${contextFor(picked)}` })
  }).catch(($, e, next) => next(e))

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const elements = $.ui.resolve(e)
    const { Box, Text, Button } = elements
    // The mobile app draws no text field yet: it shows the whole list.
    const Input = 'Input' in elements ? elements.Input : null
    const list = await read($, machines)
    const loading = await read($, isLoading)
    const failed = await read($, error)
    const current = await read($, pick)
    const search = await read($, query)
    const shown = filterMachines(list, search)

    const choose = async (m: RpMachine, project: RpProject) => {
      const p: RpPick = { machineId: m.id, machineName: m.name, project }
      await update($, pick, () => p)
      showStatus($, p)
      $.ui.toast(`Next prompt will use ${project.name} @ ${m.name}`)
      await $.ui.close({ id: PANE })
    }

    // Enter in the search box picks the first project that matches.
    const chooseFirst = async (text: string) => {
      const m = filterMachines(await read($, machines), text)[0]
      const p = m?.projects[0]
      if (m && p) await choose(m, p)
    }

    // One name column for every machine, so the paths line up.
    const nameWidth = Math.max(0, ...list.flatMap(m => m.projects.map(p => p.name.length)))
    // Too narrow for name and path side by side: put the path on its own line.
    const isStacked = (e.props.bodyColumns ?? 80) - (2 + 2 + nameWidth + 2) < 20

    return (
      <Box flexDirection="column">
        {Input && (
          <Box marginBottom={1}>
            <Input
              key="search"
              label="Search: "
              placeholder="project, path or machine"
              value={search}
              submitLabel="pick first"
              autoFocus
              onInput={value => update($, query, () => value)}
              onSubmit={value => chooseFirst(value)}
            />
          </Box>
        )}
        {loading && <Text dimColor>Loading ReadyRig machines…</Text>}
        {failed && <Text color="red">Could not list machines: {failed}</Text>}
        {!loading && !failed && list.length === 0 && (
          <Text dimColor>No ReadyRig machines found.</Text>
        )}
        {!loading && list.length > 0 && shown.length === 0 && (
          <Text dimColor>No projects match "{search}".</Text>
        )}
        {shown.map(m => (
          <Box key={`m-${m.id}`} flexDirection="column" marginBottom={1}>
            <Text bold>
              {m.name} <Text dimColor>({m.platform}{m.online ? '' : ', offline'})</Text>
            </Text>
            {m.error && (
              <Box paddingLeft={2}>
                <Text dimColor>{m.error}</Text>
              </Box>
            )}
            {!m.error && m.projects.length === 0 && (
              <Box paddingLeft={2}>
                <Text dimColor>no projects</Text>
              </Box>
            )}
            {m.projects.map(p => (
              <Box
                key={`p-${m.id}-${p.id}`}
                flexDirection={isStacked ? 'column' : 'row'}
                paddingLeft={2}
              >
                <Box flexDirection="row" flexShrink={0}>
                  <Box width={2} flexShrink={0}>
                    <Text>{current?.project.id === p.id ? '●' : ' '}</Text>
                  </Box>
                  <Box width={isStacked ? undefined : nameWidth + 2} flexShrink={0}>
                    <Button
                      key={`b-${m.id}-${p.id}`}
                      plain
                      label={p.name}
                      onPress={() => choose(m, p)}
                    />
                  </Box>
                </Box>
                <Box flexGrow={1} flexShrink={1} paddingLeft={isStacked ? 4 : 0}>
                  <Text dimColor wrap="wrap">{p.path}</Text>
                </Box>
              </Box>
            ))}
          </Box>
        ))}
        {current && (
          <Text dimColor>Picked: {current.project.name} @ {current.machineName} · /rp clear to drop it</Text>
        )}
      </Box>
    )
  })
}
