import { atom, memberOf, read, update } from 'claude-code'
import type { EngineInterface, Register } from 'claude-code'

import type {
  RpAttached,
  RpImage,
  RpLink,
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
const needsNote = atom({ plugin: 'rp', key: 'needsNote' } as const, false)
const query = atom({ plugin: 'rp', key: 'query' } as const, '')
const link = atom({ plugin: 'rp', key: 'link' } as const, null)
const isLinking = atom({ plugin: 'rp', key: 'isLinking' } as const, false)
// One per show_image call, by its tool_use_id: what its result row draws.
const images = atom({ plugin: 'rp', key: 'image' } as const, null)

// $.store key of the links, folder → project, kept across sessions.
const LINKS = 'links'
// While Claude Code starts, the ReadyRig MCP server may not be connected yet.
const CONNECT_TRIES = 15
const CONNECT_WAIT_MS = 2_000
// How long the first prompt waits for that attach.
const LINK_WAIT_MS = 8_000

// The AGENTS.md standard (agents.md) and the universal skills folder first,
// then Claude Code's own. A skill name found in both folders is listed once.
const INSTRUCTION_FILES = ['AGENTS.md', 'CLAUDE.md']
const SKILL_DIRS = ['.agents/skills', '.claude/skills']
// Past this the note says where to read the rest, so one huge file can't fill the context.
const INSTRUCTIONS_MAX = 40_000
const NOTE_STEP = 'note'

// show_image: a picture from the attached project, drawn in its tool row.
const SHOW_IMAGE = 'show_image'
const SHOW_IMAGE_TOOL = 'mcp__rp__show_image'
const IMAGE_EXT = /\.(png|jpe?g|gif|webp|heic|heif|tiff?|bmp)$/i
// read_file reads inside the project only, so the PNG copy passes through a
// folder there; it is removed as soon as it has been read.
const PREVIEW_DIR = '.rr-preview'
// The terminal's Image takes a PNG of at most 2 MiB.
const IMAGE_MAX_BYTES = 2 * 1024 * 1024
const IMAGE_MAX_SIDE = 1200
const IMAGE_MIN_SIDE = 200
// Pictures kept for redrawing rows; an older row draws its text alone.
const IMAGES_KEPT = 12
// A terminal cell is about twice as tall as it is wide.
const IMAGE_COLUMNS = 60
const IMAGE_ROWS = 30
const CELL_PX = 9

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
  return answerOf(await $.tool.call(args as any))
}

function answerOf(ran: any) {
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

// Claude Code puts the plugin's name before its status line itself.
function showProgress($: EngineInterface, p: RpProgress) {
  const finished = p.steps.filter(s => s.state !== 'waiting' && s.state !== 'running').length
  $.ui.status(`attaching ${p.projectName} @ ${p.machineName}… ${finished}/${p.steps.length}`)
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
function attachNote(
  p: RpPick,
  files: ProjectFile[],
  skills: RpSkill[],
  isLinked: boolean,
): string {
  const who = isLinked
    ? `The person linked this session's folder to the ReadyRig project ` +
      `"${p.project.name}" on machine "${p.machineName}" (computer_id: ` +
      `${p.machineId}), so it was attached when the session started`
    : `The person attached the ReadyRig project "${p.project.name}" on machine ` +
      `"${p.machineName}" (computer_id: ${p.machineId}) to this session`
  const parts = [
    markerOf('attached', p),
    `${who}, in place of any project attached before. Until they detach it, work in this project ` +
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

function summary(a: Pick<RpAttached, 'instructionFiles' | 'skills'>): string {
  return [...a.instructionFiles, plural(a.skills.length, 'skill')].join(' · ')
}

function isSame(a: RpPick | null, b: RpPick | null): boolean {
  return Boolean(a && b && a.machineId === b.machineId && a.project.id === b.project.id)
}

async function showStatus($: EngineInterface) {
  const a = await read($, attached)
  const linked = isSame(a, await read($, link)) ? ' · linked' : ''
  $.ui.status(a ? `${a.project.name} @ ${a.machineName} · ${summary(a)}${linked}` : undefined)
}

// Reads the project's instructions and skills with the pane listing each
// step, then hands them to the model in one note at the end of the conversation.
async function attach(
  $: EngineInterface,
  p: RpPick,
  isLinked = false,
): Promise<RpAttached | null> {
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
    const note = attachNote(p, instructions, skills, isLinked)
    await appendNote($, note)
    await setStep($, NOTE_STEP, { state: 'done' })

    const a: RpAttached = { ...p, instructionFiles: instructions.map(f => f.path), skills, note }
    await update($, attached, () => a)
    await update($, needsNote, () => false)
    await update($, progress, () => null)
    await showStatus($)
    await $.ui.close({ id: PANE })
    $.ui.toast(
      `Attached ${p.project.name} @ ${p.machineName}${isLinked ? ' (linked)' : ''}: ${summary(a)}`,
    )
    return a
  } catch (err) {
    const message = String((err as Error).message ?? err)
    await update($, progress, cur => cur && { ...cur, error: message })
    await setStep($, NOTE_STEP, { state: 'failed' })
    await showStatus($)
    return null
  }
}

async function detach($: EngineInterface): Promise<string> {
  const a = await read($, attached)
  if (!a) return 'No ReadyRig project is attached.'
  await update($, attached, () => null)
  await update($, needsNote, () => false)
  await showStatus($)
  await appendNote($, detachNote(a))
  const l = await read($, link)
  const stays = isSame(a, l) ? ` This folder stays linked to it: /rp unlink stops attaching it.` : ''
  return `Detached ${a.project.name} @ ${a.machineName}.${stays}`
}

function slashed(path: string): string {
  return /[\\/]$/.test(path) ? path : `${path}/`
}

async function readLinks($: EngineInterface): Promise<Record<string, RpPick>> {
  const value = await $.store.get(LINKS)
  return value && typeof value === 'object' ? (value as Record<string, RpPick>) : {}
}

// The link of the session's project root, or of the nearest folder above it.
async function findLink($: EngineInterface): Promise<RpLink | null> {
  const root = await $.session.root()
  const links = await readLinks($)
  const folder = Object.keys(links)
    .filter(f => root === f || slashed(root).startsWith(slashed(f)) || root.startsWith(`${f}\\`))
    .sort((a, b) => b.length - a.length)[0]
  const p = folder ? links[folder] : undefined
  return folder && p ? { ...p, folder } : null
}

// Links the session's project root to `p`, in place of a link it had.
async function linkHere($: EngineInterface, p: RpPick): Promise<string> {
  const folder = await $.session.root()
  const links = await readLinks($)
  const pick: RpPick = { machineId: p.machineId, machineName: p.machineName, project: p.project }
  await $.store.set(LINKS, { ...links, [folder]: pick })
  await update($, link, () => ({ ...pick, folder }))
  await showStatus($)
  return (
    `Linked ${folder} to ${p.project.name} @ ${p.machineName}: a session started ` +
    `in this folder, or in one below it, attaches it.`
  )
}

async function unlink($: EngineInterface): Promise<string> {
  const l = await findLink($)
  if (!l) return 'This folder is not linked to a ReadyRig project.'
  const { [l.folder]: _, ...rest } = await readLinks($)
  await $.store.set(LINKS, rest)
  // A link of a folder above this one may apply now.
  await update($, link, () => null)
  const above = await findLink($)
  await update($, link, () => above)
  await showStatus($)
  const a = await read($, attached)
  const stays = isSame(a, l) ? ' It stays attached to this session: /rp detach detaches it.' : ''
  return `Unlinked ${l.folder} from ${l.project.name} @ ${l.machineName}.${stays}`
}

// The machine of a link, with its projects. Until the ReadyRig MCP server
// connects, the session has no such tool and the call rejects: try again. A
// denied call or an error answer is final.
async function linkedMachine($: EngineInterface, l: RpLink): Promise<RpMachine> {
  let ran: any
  for (let tries = 1; !ran; tries++) {
    try {
      ran = await $.tool.call({ tool: 'mcp__readyrig__list_computers' } as any)
    } catch (err) {
      if (tries >= CONNECT_TRIES) throw err
      await $.clock.sleep(CONNECT_WAIT_MS)
    }
  }
  const c = (answerOf(ran).computers ?? []).find((c: any) => c.id === l.machineId)
  if (!c) return { ...unknownMachine(l), error: 'not one of your ReadyRig computers' }
  return loadMachine($, c)
}

function unknownMachine(l: RpLink): RpMachine {
  return { id: l.machineId, name: l.machineName, platform: '', online: false, projects: [] }
}

// A session started in a linked folder attaches the linked project. A reload
// of the mod leaves an attached project as it is.
async function attachLinked($: EngineInterface) {
  const l = await findLink($)
  await update($, link, () => l)
  if (!l || (await read($, attached)) || (await read($, progress))) return showStatus($)
  const failed = (why: string) => {
    $.ui.status(undefined)
    $.ui.toast(`Could not attach ${l.project.name} @ ${l.machineName}, linked to this folder: ${why}`)
  }
  $.ui.status(`attaching ${l.project.name} @ ${l.machineName} (linked)…`)
  let m: RpMachine
  try {
    m = await linkedMachine($, l)
  } catch (err) {
    return failed(String((err as Error).message ?? err))
  }
  if (m.error) return failed(m.error)
  const project = m.projects.find(p => p.id === l.project.id)
  if (!project) return failed(`the machine has no project ${l.project.name} any more`)
  const a = await attach($, { machineId: m.id, machineName: m.name, project }, true)
  if (!a) failed((await read($, progress))?.error ?? 'unknown error')
  await update($, progress, () => null)
}

export function shellQuote(text: string): string {
  return `'${text.replace(/'/g, `'\\''`)}'`
}

// Why show_image refuses a path, or null. The PNG copy is made by a shell
// command, so the path is held inside the project as read_file holds its own.
export function imagePathError(path: string): string | null {
  if (!path.trim()) return 'give the path of an image file'
  if (/^[/~$\\]|^[A-Za-z]:/.test(path)) return 'use a path relative to the project'
  if (path.split(/[\\/]/).includes('..')) return 'the path must stay inside the project'
  if (!IMAGE_EXT.test(path)) return 'not an image file (png, jpg, gif, webp, heic, tiff or bmp)'
  return null
}

// On macOS, sips writes a PNG of the picture to `out`, at most IMAGE_MAX_SIDE
// a side and never enlarged, smaller until it fits IMAGE_MAX_BYTES, and prints
// `ok <width> <height>`. Without sips it prints `no-sips` and writes nothing.
export function convertScript(path: string, out: string): string {
  const src = shellQuote(`./${path}`)
  const dst = shellQuote(out)
  return [
    'set -e',
    'command -v sips >/dev/null 2>&1 || { echo no-sips; exit 0; }',
    `mkdir -p ${PREVIEW_DIR}`,
    `big=$(sips -g pixelWidth -g pixelHeight ${src} | awk '/pixel(Width|Height)/ { if ($2 > m) m = $2 } END { print m + 0 }')`,
    `m=${IMAGE_MAX_SIDE}`,
    'if [ "$big" -gt 0 ] && [ "$big" -lt "$m" ]; then m=$big; fi',
    'while :; do',
    `  sips -s format png -Z "$m" ${src} --out ${dst} >/dev/null`,
    `  if [ "$(stat -f %z ${dst})" -le ${IMAGE_MAX_BYTES} ] || [ "$m" -le ${IMAGE_MIN_SIDE} ]; then break; fi`,
    '  m=$((m * 7 / 10))',
    'done',
    `sips -g pixelWidth -g pixelHeight ${dst} | awk '/pixelWidth/ { w = $2 } /pixelHeight/ { h = $2 } END { print "ok", w, h }'`,
  ].join('\n')
}

// A PNG's size, from the IHDR chunk at the start of its base64.
export function pngSize(base64: string): { width: number; height: number } | null {
  const head = atob(base64.slice(0, 32))
  if (head.slice(1, 4) !== 'PNG' || head.length < 24) return null
  const at = (i: number) =>
    ((head.charCodeAt(i) << 24) | (head.charCodeAt(i + 1) << 16) | (head.charCodeAt(i + 2) << 8) | head.charCodeAt(i + 3)) >>> 0
  return { width: at(16), height: at(20) }
}

// The terminal cells a picture is drawn over, its shape kept.
export function imageCells(width: number, height: number): { columns: number; rows: number } {
  let columns = Math.max(4, Math.min(IMAGE_COLUMNS, Math.round(width / CELL_PX)))
  let rows = Math.max(2, Math.round((columns * height) / width / 2))
  if (rows > IMAGE_ROWS) {
    columns = Math.max(4, Math.round((columns * IMAGE_ROWS) / rows))
    rows = IMAGE_ROWS
  }
  return { columns, rows }
}

// A shell command on a ReadyRig computer; a non-zero exit rejects with its stderr.
async function runCommand($: EngineInterface, machineId: string, project: string, command: string) {
  const out = await callComputer($, machineId, 'exec_command', {
    project,
    command,
    yield_time_ms: 10_000,
  })
  if (out.running) throw new Error('the command is taking too long')
  if (out.exit_code) throw new Error(String(out.stderr || out.stdout || `exit ${out.exit_code}`).trim())
  return String(out.stdout ?? '')
}

// read_file answers a picture as an MCP image block beside its JSON text.
async function readPng($: EngineInterface, machineId: string, project: string, path: string) {
  const ran: any = await $.tool.call({
    tool: 'mcp__readyrig__call_computer_tool',
    computer_id: machineId,
    tool_name: 'read_file',
    arguments: { project, path },
  } as any)
  const out = answerOf(ran)
  if (out.status === 'error' || out.error) throw new Error(String(out.error || 'tool failed'))
  const blocks: any[] = Array.isArray(ran.result) ? ran.result : (ran.result?.content ?? [])
  const block = blocks.find(b => b?.type === 'image')
  const data = block?.data ?? block?.source?.data
  if (typeof data !== 'string' || !data) throw new Error('ReadyRig sent no picture')
  if (!pngSize(data)) throw new Error('ReadyRig sent a picture that is not a PNG')
  return data
}

// The picture as a PNG the terminal can draw: converted on macOS, read as it
// is elsewhere when it is a PNG already. The copy is removed in every case.
async function fetchImage($: EngineInterface, p: RpPick, path: string, id: string): Promise<RpImage> {
  const project = p.project.id
  const copy = `${PREVIEW_DIR}/${id.replace(/[^\w-]/g, '')}.png`
  let png: string
  try {
    const said = (await runCommand($, p.machineId, project, convertScript(path, copy))).trim()
    if (said === 'no-sips') {
      if (!/\.png$/i.test(path)) throw new Error('only PNG files can be shown from a computer without macOS sips')
      png = await readPng($, p.machineId, project, path)
    } else {
      png = await readPng($, p.machineId, project, copy)
    }
  } finally {
    await runCommand($, p.machineId, project, `rm -f ${shellQuote(copy)}; rmdir ${PREVIEW_DIR} 2>/dev/null || true`).catch(() => {})
  }
  if (png.length * 0.75 > IMAGE_MAX_BYTES) throw new Error('the picture is larger than 2 MiB')
  const size = pngSize(png)!
  return { ...size, png, path, machineId: p.machineId, machineName: p.machineName, projectId: project }
}

async function openInPreview($: EngineInterface, img: RpImage): Promise<string | null> {
  try {
    await runCommand($, img.machineId, img.projectId, `open -a Preview ${shellQuote(`./${img.path}`)}`)
    return null
  } catch (err) {
    return `Could not open ${img.path} in Preview on ${img.machineName}: ${String((err as Error).message ?? err)}`
  }
}

const STEP_ICON: Record<RpStep['state'], string> = {
  waiting: '·',
  running: '…',
  done: '✓',
  none: '–',
  failed: '✗',
}

export const register: Register = on => {
  // Attaching a linked project runs while Claude Code starts; the first prompt
  // waits for it, so the model has the note before it answers.
  let linking: Promise<void> | null = null
  // The show_image calls whose pictures are kept, oldest first.
  const shown: string[] = []

  on('session.start', async ($, e, next) => {
    await $.command.register({
      name: 'rp',
      description: 'Attach a ReadyRig project (its AGENTS.md, CLAUDE.md and skills) to this session',
      argumentHint: '[detach | link | unlink]',
    })
    await $.tool.register({
      name: SHOW_IMAGE,
      description:
        'Show the person an image file from the ReadyRig project attached with /rp: it is drawn ' +
        'in their terminal (Ghostty, kitty) with an "Open in Preview" button, and where the ' +
        'terminal draws no pictures (tmux, herdr) the button alone. Use it when they ask to ' +
        'see an image or a screenshot, or when showing one helps. You do not see the picture: ' +
        'read it with read_file when you need to look at it yourself.',
      inputSchema: {
        type: 'object',
        properties: {
          path: { type: 'string', description: 'The image file, relative to the attached project' },
          open_in_preview: {
            type: 'boolean',
            description: 'Also open it in Preview on that Mac, as when they ask to open it',
          },
        },
        required: ['path'],
        additionalProperties: false,
      },
      isDeferred: false,
    })
    linking = attachLinked($).catch(() => {})

    return next(e)
  })

  on('tool.call', { tool: SHOW_IMAGE_TOOL }, async ($, e) => {
    const input = e as unknown as { path?: unknown; open_in_preview?: unknown }
    const path = typeof input.path === 'string' ? input.path.replace(/^\.\//, '') : ''
    const a = await read($, attached)
    if (!a) return { deny: 'No ReadyRig project is attached: the person attaches one with /rp.' }
    const why = imagePathError(path)
    if (why) return { deny: `Cannot show ${path || 'that'}: ${why}.` }
    let img: RpImage
    try {
      img = await fetchImage($, a, path, e.tool_use_id)
    } catch (err) {
      return { deny: `Cannot show ${path}: ${String((err as Error).message ?? err)}` }
    }
    await update($, memberOf(images, { requestId: e.tool_use_id }), () => img)
    shown.push(e.tool_use_id)
    for (const old of shown.splice(0, Math.max(0, shown.length - IMAGES_KEPT))) {
      await update($, memberOf(images, { requestId: old }), () => null)
    }
    const failed = input.open_in_preview === true ? await openInPreview($, img) : null
    const opened = input.open_in_preview === true && !failed ? ' and opened it in Preview' : ''
    return {
      result:
        `Showed ${path} (${img.width}×${img.height} PNG) from ${a.project.name} @ ${a.machineName} ` +
        `to the person${opened}.${failed ? ` ${failed}` : ''}`,
    }
  }).catch(($, e) => ({ deny: `Cannot show the image: it took too long or failed.` }))

  on('command.run', { command: 'rp' }, async ($, e) => {
    const arg = e.args.trim()
    if (arg === 'detach' || arg === 'clear') {
      await $.ui.close({ id: PANE })
      return { text: await detach($) }
    }
    if (arg === 'unlink') return { text: await unlink($) }
    const current = await read($, attached)
    if (arg === 'link' && current) return { text: await linkHere($, current) }

    const isLink = arg === 'link'
    await update($, query, () => '')
    await update($, progress, () => null)
    await update($, isLinking, () => isLink)
    await $.ui.open({
      id: PANE,
      title: isLink ? 'Link a ReadyRig project to this folder' : 'ReadyRig projects',
      focus: true,
      closeOnEscape: true,
    })
    await load($)

    return {
      text: isLink
        ? 'Pick a ReadyRig project to attach and link to this folder in the pane (Esc to cancel).'
        : 'Pick a ReadyRig project to attach in the pane (Esc to cancel).',
    }
  })

  // The note is appended once, at the end of the conversation, so the cached
  // prompt before it stays valid. A compaction of the main conversation or a
  // /clear drops it: the next prompt puts it back.
  on('session.compact', async ($, e, next) => {
    const out = await next(e)
    if (!e.agentId && e.trigger !== 'precompute' && out.messages) {
      await update($, needsNote, () => true)
    }
    return out
  })

  on('session.end', async ($, e, next) => {
    if (e.reason === 'clear') await update($, needsNote, () => true)
    return next(e)
  })

  on('prompt.submit', async ($, e, next) => {
    const isCommand = e.text.trimStart().startsWith('/')
    // Held under the hook's 10 s budget; a slower attach adds its note when done.
    if (linking && !isCommand) {
      await Promise.race([linking, $.clock.sleep(LINK_WAIT_MS)])
      linking = null
    }
    const a = await read($, attached)
    if (a && (await read($, needsNote)) && !isCommand) {
      await appendNote($, a.note)
      await update($, needsNote, () => false)
    }
    return next(e)
  }).catch(($, e, next) => next(e))

  // The picture over the row's cells where the terminal draws pictures, its
  // dim path elsewhere, and a button that opens it in Preview on the Mac.
  on('ui.render', { component: 'ToolResult', props: { tool: SHOW_IMAGE_TOOL } }, async ($, e, next) => {
    if (e.props.isErrored) return next(e)
    const img = await read($, memberOf(images, e))
    if (!img) return next(e)
    const elements = $.ui.resolve(e)
    const { Box, Text, Button } = elements
    const Image = 'Image' in elements ? elements.Image : null
    const cells = imageCells(img.width, img.height)
    return (
      <Box flexDirection="column">
        {Image && (
          <Image
            source={{ png: img.png }}
            columns={cells.columns}
            rows={cells.rows}
            alt={`[${img.path}]`}
          />
        )}
        <Box flexDirection="row" flexWrap="wrap">
          <Text dimColor>
            {img.path} · {img.width}×{img.height} · {img.machineName} ·{' '}
          </Text>
          <Button
            key="preview"
            plain
            label="Open in Preview"
            onPress={async () => {
              const failed = await openInPreview($, img)
              if (failed) $.ui.toast(failed)
            }}
          />
        </Box>
      </Box>
    )
  })

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
    const linked = await read($, link)
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

    // From /rp link, the project picked is linked once it is attached.
    const choose = async (m: RpMachine, project: RpProject) => {
      const a = await attach($, { machineId: m.id, machineName: m.name, project })
      if (a && (await read($, isLinking))) {
        await update($, isLinking, () => false)
        $.ui.toast(await linkHere($, a))
      }
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
                    <Text>{isSame(current, { machineId: m.id, machineName: m.name, project: p }) ? '●' : ' '}</Text>
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
          <Box flexDirection="row" flexWrap="wrap">
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
            {!isSame(current, linked) && (
              <Box key="link-row" flexDirection="row">
                <Text dimColor> · </Text>
                <Button
                  key="link"
                  plain
                  label="link to this folder"
                  onPress={async () => $.ui.toast(await linkHere($, current))}
                />
              </Box>
            )}
          </Box>
        )}
        {linked && (
          <Box flexDirection="row" flexWrap="wrap">
            <Text dimColor>
              Linked: {linked.folder} → {linked.project.name} @ {linked.machineName} ·{' '}
            </Text>
            <Button
              key="unlink"
              plain
              label="unlink"
              onPress={async () => $.ui.toast(await unlink($))}
            />
          </Box>
        )}
      </Box>
    )
  })
}
