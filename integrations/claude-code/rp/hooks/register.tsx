import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register } from 'claude-code'

import type {
  RpAttached,
  RpMachine,
  RpPick,
  RpProgress,
  RpProject,
  RpSkill,
  RpStep,
} from '../types'

const PANE = 'rp'
const machines = atom({ plugin: 'rp', key: 'machines' } as const, [])
const isLoading = atom({ plugin: 'rp', key: 'isLoading' } as const, false)
const error = atom({ plugin: 'rp', key: 'error' } as const, null)
const attached = atom({ plugin: 'rp', key: 'attached' } as const, null)
const progress = atom({ plugin: 'rp', key: 'progress' } as const, null)
const query = atom({ plugin: 'rp', key: 'query' } as const, '')

// The AGENTS.md standard (agents.md) and the universal skills folder first,
// then Claude Code's own. A skill name found in both folders is listed once.
const INSTRUCTION_FILES = ['AGENTS.md', 'CLAUDE.md']
const SKILL_DIRS = ['.agents/skills', '.claude/skills']
// Past this the note says where to read the rest, so one huge file can't fill the context.
const INSTRUCTIONS_MAX = 40_000
const NOTE_STEP = 'note'

type ProjectFile = { path: string; text: string; isTruncated: boolean }

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

// One tool of a ReadyRig computer; a missing file and the like reject.
async function callComputer(
  $: EngineInterface,
  machineId: string,
  toolName: string,
  args: Record<string, unknown>,
) {
  const out = await callTool($, {
    tool: 'mcp__readyrig__call_computer_tool',
    computer_id: machineId,
    tool_name: toolName,
    arguments: args,
  })
  if (out.status === 'error' || out.error) throw new Error(String(out.error || 'tool failed'))
  return out.result ?? {}
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
    const out = await callComputer($, c.id, 'list_projects', {})
    const projects: RpProject[] = (out.projects ?? []).map((p: any) => ({
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

// A project file's text, or null when it is missing or unreadable.
async function readText(
  $: EngineInterface,
  p: RpPick,
  path: string,
  limit?: number,
): Promise<ProjectFile | null> {
  try {
    const out = await callComputer($, p.machineId, 'read_file', {
      project: p.project.id,
      path,
      ...(limit ? { limit } : {}),
    })
    if (typeof out.content !== 'string') return null
    return { path, text: out.content, isTruncated: Boolean(out.truncated) }
  } catch {
    return null
  }
}

// The name and description lines of a SKILL.md's YAML front matter. Enough
// YAML for skills: plain or quoted values, and folded or indented ones.
export function frontMatter(text: string): Record<string, string> {
  const lines = text.split(/\r?\n/)
  if (lines[0]?.trim() !== '---') return {}
  const out: Record<string, string> = {}
  let key: string | null = null
  for (const line of lines.slice(1)) {
    if (line.trim() === '---') break
    const m = /^([A-Za-z_][\w-]*):\s*(.*)$/.exec(line)
    if (m) {
      const [, name = '', raw = ''] = m
      const value = raw.trim()
      key = name
      out[key] = /^[>|][+-]?$/.test(value) ? '' : value.replace(/^(["'])(.*)\1$/, '$2')
    } else if (key && /^\s/.test(line)) {
      out[key] = `${out[key]} ${line.trim()}`.trim()
    }
  }
  return out
}

function plural(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? '' : 's'}`
}

// The pane's list of steps, and the count on the status line, as they run.
async function setStep($: EngineInterface, id: string, patch: Partial<RpStep>) {
  const next = await update($, progress, cur =>
    cur && { ...cur, steps: cur.steps.map(s => (s.id === id ? { ...s, ...patch } : s)) },
  )
  if (next) showProgress($, next)
}

function showProgress($: EngineInterface, p: RpProgress) {
  const finished = p.steps.filter(s => s.state !== 'waiting' && s.state !== 'running').length
  $.ui.status(`rp: attaching ${p.projectName} @ ${p.machineName}… ${finished}/${p.steps.length}`)
}

// AGENTS.md or CLAUDE.md at the project's root. A CLAUDE.md that only
// imports AGENTS.md (`@AGENTS.md`) adds nothing, so it counts as none.
async function loadInstruction($: EngineInterface, p: RpPick, path: string) {
  const file = await readText($, p, path)
  const isEmpty =
    !file ||
    file.text.trim() === '' ||
    (path === 'CLAUDE.md' && /^(\s*@(\.\/)?AGENTS\.md\s*)+$/.test(file.text))
  if (isEmpty) {
    await setStep($, path, { state: 'none', detail: file ? 'only imports AGENTS.md' : 'none' })
    return null
  }
  const lines = file.text.split('\n').length
  await setStep($, path, { state: 'done', detail: plural(lines, 'line') })
  return file
}

// Each folder (or link to one) in a skills folder that holds a SKILL.md.
async function loadSkills($: EngineInterface, p: RpPick, dir: string): Promise<RpSkill[]> {
  let entries: any[]
  try {
    const out = await callComputer($, p.machineId, 'list_directory', {
      project: p.project.id,
      path: dir,
    })
    entries = out.entries ?? []
  } catch {
    await setStep($, dir, { state: 'none', detail: 'none' })
    return []
  }
  const folders = entries
    .filter(e => (e.directory || e.symlink) && !String(e.name).startsWith('.'))
    .map(e => String(e.name))
  await setStep($, dir, { detail: `reading ${plural(folders.length, 'skill')}` })
  const skills = await Promise.all(
    folders.map(async (folder): Promise<RpSkill | null> => {
      const path = `${dir}/${folder}/SKILL.md`
      const file = await readText($, p, path, 40)
      if (!file) return null
      const fm = frontMatter(file.text)
      return { name: fm.name || folder, description: fm.description || '', path }
    }),
  )
  const found = skills.filter((s): s is RpSkill => s !== null)
  await setStep($, dir, {
    state: found.length > 0 ? 'done' : 'none',
    detail: found.length > 0 ? found.map(s => s.name).join(', ') : 'none',
  })
  return found
}

function markerOf(kind: 'attached' | 'detached', p: RpPick): string {
  return `[rp:${kind} ${p.machineId}/${p.project.id}]`
}

// The folder's path stays out of the note: it can name the user, and the
// project argument is all the tools need. Relative paths resolve inside it.
function attachNote(p: RpPick, files: ProjectFile[], skills: RpSkill[]): string {
  const parts = [
    markerOf('attached', p),
    `The person attached the ReadyRig project "${p.project.name}" on machine ` +
      `"${p.machineName}" (computer_id: ${p.machineId}) to this session, in place of ` +
      `any project attached before. Until they detach it, work in this project ` +
      `unless they say otherwise: call mcp__readyrig__call_computer_tool with ` +
      `computer_id "${p.machineId}", pass project: "${p.project.id}" in the tool's ` +
      `arguments, and use paths relative to the project.`,
    '## Project instructions',
  ]
  const names = INSTRUCTION_FILES.join(' or ')
  if (files.length > 0) {
    parts.push(
      `From ${files.map(f => f.path).join(' and ')} at the project's root. Follow ` +
        `them for work in this project. A subfolder may hold its own ${names}: ` +
        `before changing files in a subfolder, look for one; the file nearest to ` +
        `what you change wins, and what the person asks wins over all of them. ` +
        `A line \`@path\` imports that file: read it with read_file when it matters.`,
    )
    for (const f of files) {
      const isCut = f.isTruncated || f.text.length > INSTRUCTIONS_MAX
      parts.push(
        `<project_file path="${f.path}">\n${f.text.slice(0, INSTRUCTIONS_MAX).trimEnd()}\n` +
          (isCut ? `[cut short here: read the rest with read_file]\n` : '') +
          `</project_file>`,
      )
    }
  } else {
    parts.push(
      `The project has no ${names} at its root. A subfolder may hold one: ` +
        `before changing files in a subfolder, look for one and follow it.`,
    )
  }
  parts.push('## Project skills')
  if (skills.length > 0) {
    parts.push(
      `From ${SKILL_DIRS.join(' and ')}. When a task matches a skill's ` +
        `description, read that whole SKILL.md with read_file on this computer ` +
        `and project before you start, then follow it. Paths a skill names are ` +
        `relative to its folder.`,
      skills
        .map(s => `- ${s.name}: ${s.description || '(no description)'} (${s.path})`)
        .join('\n'),
    )
  } else {
    parts.push(`The project has no skills in ${SKILL_DIRS.join(' or ')}.`)
  }
  return parts.join('\n\n')
}

function detachNote(p: RpPick): string {
  return (
    `${markerOf('detached', p)}\n` +
    `The person detached the ReadyRig project "${p.project.name}" on machine ` +
    `"${p.machineName}". Its instructions and skills no longer apply, and the ` +
    `session has no attached project: don't work in it unless they ask.`
  )
}

async function appendNote($: EngineInterface, text: string) {
  const out = await $.session.append({
    message: { type: 'user', content: [{ type: 'text', text }] },
  })
  if (out.deny) throw new Error(out.deny)
}

// Whether the conversation still holds this attachment's note, the latest
// rp note of all. A compaction or /clear can drop it.
function hasNote(rows: readonly { role: string; text: string }[], a: RpAttached): boolean {
  for (const row of [...rows].reverse()) {
    if (row.role !== 'user') continue
    if (row.text.includes('[rp:attached ') || row.text.includes('[rp:detached ')) {
      return row.text.includes(markerOf('attached', a))
    }
  }
  return false
}

function summary(a: Pick<RpAttached, 'instructionFiles' | 'skills'>): string {
  return [...a.instructionFiles, plural(a.skills.length, 'skill')].join(' · ')
}

function showStatus($: EngineInterface, a: RpAttached | null) {
  $.ui.status(a ? `rp: ${a.project.name} @ ${a.machineName} · ${summary(a)}` : undefined)
}

// Reads the project's instructions and skills with the pane listing each
// step, then hands them to the model in one note at the end of the conversation.
async function attach($: EngineInterface, p: RpPick) {
  const start: RpProgress = {
    machineName: p.machineName,
    projectName: p.project.name,
    steps: [
      ...INSTRUCTION_FILES.map(id => ({ id, label: id, state: 'running' as const })),
      ...SKILL_DIRS.map(id => ({ id, label: id, state: 'running' as const })),
      { id: NOTE_STEP, label: 'Add to the conversation', state: 'waiting' },
    ],
  }
  await update($, progress, () => start)
  showProgress($, start)
  try {
    const [files, skillLists] = await Promise.all([
      Promise.all(INSTRUCTION_FILES.map(path => loadInstruction($, p, path))),
      Promise.all(SKILL_DIRS.map(dir => loadSkills($, p, dir))),
    ])
    const instructions = files.filter((f): f is ProjectFile => f !== null)
    const skills: RpSkill[] = []
    for (const s of skillLists.flat()) {
      if (!skills.some(k => k.name === s.name)) skills.push(s)
    }

    await setStep($, NOTE_STEP, { state: 'running' })
    const note = attachNote(p, instructions, skills)
    await appendNote($, note)
    await setStep($, NOTE_STEP, { state: 'done' })

    const a: RpAttached = { ...p, instructionFiles: instructions.map(f => f.path), skills, note }
    await update($, attached, () => a)
    await update($, progress, () => null)
    showStatus($, a)
    await $.ui.close({ id: PANE })
    $.ui.toast(`Attached ${p.project.name} @ ${p.machineName}: ${summary(a)}`)
  } catch (err) {
    const message = String((err as Error).message ?? err)
    await update($, progress, cur => cur && { ...cur, error: message })
    await setStep($, NOTE_STEP, { state: 'failed' })
    showStatus($, await read($, attached))
  }
}

async function detach($: EngineInterface): Promise<string> {
  const a = await read($, attached)
  if (!a) return 'No ReadyRig project is attached.'
  await update($, attached, () => null)
  showStatus($, null)
  await appendNote($, detachNote(a))
  return `Detached ${a.project.name} @ ${a.machineName}.`
}

const STEP_ICON: Record<RpStep['state'], string> = {
  waiting: '·',
  running: '…',
  done: '✓',
  none: '–',
  failed: '✗',
}

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    await $.command.register({
      name: 'rp',
      description: 'Attach a ReadyRig project (its AGENTS.md, CLAUDE.md and skills) to this session',
      argumentHint: '[detach]',
    })
    showStatus($, await read($, attached))

    return next(e)
  })

  on('command.run', { command: 'rp' }, async ($, e) => {
    const arg = e.args.trim()
    if (arg === 'detach' || arg === 'clear') {
      await $.ui.close({ id: PANE })
      return { text: await detach($) }
    }

    await update($, query, () => '')
    await update($, progress, () => null)
    await $.ui.open({
      id: PANE,
      title: 'ReadyRig projects',
      focus: true,
      closeOnEscape: true,
    })
    await load($)

    return { text: 'Pick a ReadyRig project to attach in the pane (Esc to cancel).' }
  })

  // The note is appended once, at the end of the conversation, so the cached
  // prompt before it stays valid. Put it back if a compaction or /clear lost it.
  on('prompt.submit', async ($, e, next) => {
    const a = await read($, attached)
    if (a && !e.text.trimStart().startsWith('/')) {
      const rows = await $.session.messages()
      if (Array.isArray(rows) && !hasNote(rows, a)) await appendNote($, a.note)
    }
    return next(e)
  }).catch(($, e, next) => next(e))

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const elements = $.ui.resolve(e)
    const { Box, Text, Button } = elements
    // The mobile app draws no text field yet: it shows the whole list.
    const Input = 'Input' in elements ? elements.Input : null
    const list = await read($, machines)
    const loading = await read($, isLoading)
    const failed = await read($, error)
    const current = await read($, attached)
    const running = await read($, progress)
    const search = await read($, query)
    const shown = filterMachines(list, search)

    if (running) {
      const labelWidth = Math.max(...running.steps.map(s => s.label.length)) + 2
      return (
        <Box flexDirection="column">
          <Box marginBottom={1}>
            <Text bold>
              Attaching {running.projectName} <Text dimColor>@ {running.machineName}</Text>
            </Text>
          </Box>
          {running.steps.map(s => (
            <Box key={`s-${s.id}`} flexDirection="row" paddingLeft={2}>
              <Box width={2} flexShrink={0}>
                <Text color={s.state === 'failed' ? 'red' : s.state === 'done' ? 'green' : undefined}>
                  {STEP_ICON[s.state]}
                </Text>
              </Box>
              <Box width={labelWidth} flexShrink={0}>
                <Text dimColor={s.state === 'waiting'}>{s.label}</Text>
              </Box>
              <Box flexGrow={1} flexShrink={1}>
                <Text dimColor wrap="wrap">
                  {s.detail ?? (s.state === 'running' ? 'reading…' : '')}
                </Text>
              </Box>
            </Box>
          ))}
          {running.error && (
            <Box marginTop={1}>
              <Text color="red">Could not attach: {running.error} · Esc to close</Text>
            </Box>
          )}
        </Box>
      )
    }

    const choose = (m: RpMachine, project: RpProject) =>
      attach($, { machineId: m.id, machineName: m.name, project })

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
              submitLabel="attach first"
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
                    <Text>{current?.machineId === m.id && current.project.id === p.id ? '●' : ' '}</Text>
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
          <Box flexDirection="row">
            <Text dimColor>Attached: {current.project.name} @ {current.machineName} · </Text>
            <Button
              key="detach"
              plain
              label="detach"
              onPress={async () => {
                await detach($)
                await $.ui.close({ id: PANE })
              }}
            />
          </Box>
        )}
      </Box>
    )
  })
}
