import { expect, mock, test } from 'claude-code/testing'

// The test environment has timers; the hooks lib declares none.
declare const setTimeout: (run: () => void, ms: number) => unknown

const COMPUTERS = {
  computers: [
    { id: 'mac', name: 'my-mac', platform: 'darwin', online: true },
    { id: 'box', name: 'my-box', platform: 'linux', online: false },
  ],
}
const PROJECTS = {
  projects: [
    { id: 'p1', name: 'tvbox', path: '/Users/me/tvbox' },
    { id: 'p2', name: 'agent_workspace', path: '/Users/me/agent_workspace' },
  ],
}
// tvbox has both instruction files and skills in both folders; agent_workspace has nothing.
const FILES: Record<string, string> = {
  'p1:AGENTS.md': '# tvbox\n\nRun make test before you commit.\n',
  'p1:CLAUDE.md': '@AGENTS.md\n',
  'p1:.agents/skills/deploy/SKILL.md': '---\nname: deploy\ndescription: Ship it.\n---\n\n# Deploy\n',
  'p1:.agents/skills/linked/SKILL.md':
    '---\nname: linked\ndescription: >\n  A folded\n  description.\n---\n',
  'p1:.claude/skills/deploy/SKILL.md': '---\nname: deploy\ndescription: The other one.\n---\n',
  'p1:.claude/skills/review/SKILL.md': '---\nname: review\ndescription: "Reviews code."\n---\n',
}
const DIRS: Record<string, { name: string; directory: boolean; symlink: boolean }[]> = {
  'p1:.agents/skills': [
    { name: '.hidden', directory: true, symlink: false },
    { name: 'README.md', directory: false, symlink: false },
    { name: 'deploy', directory: true, symlink: false },
    { name: 'linked', directory: false, symlink: true },
  ],
  'p1:.claude/skills': [
    { name: 'deploy', directory: true, symlink: false },
    { name: 'review', directory: true, symlink: false },
  ],
}
const PANE = {
  title: 'ReadyRig projects',
  isFocused: true,
  bodyColumns: 80,
  placement: 'dock',
  scroll: { offset: 0, bodyRows: 30 },
  view: {},
} as const
const RUN_RP = { command: 'rp', args: '', origin: { kind: 'composer' }, presentation: { isFullscreen: true, columns: 120 } } as any
const RUN_DETACH = { ...RUN_RP, args: 'detach' }

function answer(body: unknown, isError = false) {
  const text = JSON.stringify(body)
  return { result: [{ type: 'text', text }], text, ...(isError ? { isError: true } : {}) } as any
}

// ReadyRig, the conversation and the prompt, faked beneath the mod.
// `gate` holds back AGENTS.md reads until it resolves.
function fakeWorld(on: any, gate?: Promise<void>) {
  const session = mock.session(on)
  const world = { sent: [] as string[], keepFrom: 0, notes: () => session.appended().map(r => textOf(r)) }
  on('tool.call', async ($: any, e: any) => {
    if (String(e.tool).endsWith('list_computers')) return answer(COMPUTERS)
    const args = e.arguments ?? {}
    const key = `${args.project}:${args.path}`
    if (e.tool_name === 'list_projects') return answer({ status: 'success', result: PROJECTS })
    if (e.tool_name === 'read_file' && key in FILES) {
      if (args.path === 'AGENTS.md' && gate) await gate
      return answer({ status: 'success', result: { content: FILES[key], truncated: false } })
    }
    if (e.tool_name === 'list_directory' && key in DIRS) {
      return answer({ status: 'success', result: { entries: DIRS[key] } })
    }
    return answer({ status: 'error', error: `no such file or directory: ${args.path}` }, true)
  })
  on('ui.open', () => ({ value: undefined }) as any)
  on('ui.close', () => ({ value: undefined }) as any)
  // What the model would read: the appended rows from `keepFrom` on (a compaction drops the rest).
  on('session.messages', () => ({
    value: world.notes().slice(world.keepFrom).map(text => ({ role: 'user', text, toolUses: [] })),
  }) as any)
  on('prompt.submit', ($: any, e: any) => {
    world.sent.push(e.text)
    return { text: e.text }
  })
  return world
}

function textOf(row: any): string {
  return row.message.content.map((b: any) => b.text ?? '').join('')
}

const submit = ($: any, text: string) =>
  $.prompt.submit({ text, wait: false, origin: { kind: 'composer' } } as any)

for (const surface of ['terminal', 'desktop'] as const) {
  test(`attaching adds the project's instructions and skills once (${surface})`, async ($, on) => {
    const world = fakeWorld(on)

    await $.command.run(RUN_RP)
    const ui = await $.ui.mount({ plugin: 'rp', surface, component: 'Pane', requestId: 'rp', props: PANE })
    expect(await ui.findAll({ text: 'offline' })).not.toHaveLength(0)
    await ui.press({ key: 'b-mac-p1' })

    const notes = world.notes()
    expect(notes).toHaveLength(1)
    const note = notes[0]
    expect(note).toContain('[rp:attached mac/p1]')
    expect(note).toContain('"tvbox" on machine "my-mac" (computer_id: mac)')
    expect(note).toContain('project: "p1"')
    expect(note).toContain('<project_file path="AGENTS.md">\n# tvbox\n\nRun make test before you commit.\n</project_file>')
    // A CLAUDE.md that only imports AGENTS.md is left out.
    expect(note).not.toContain('<project_file path="CLAUDE.md">')
    expect(note).toContain('- deploy: Ship it. (.agents/skills/deploy/SKILL.md)')
    expect(note).toContain('- linked: A folded description. (.agents/skills/linked/SKILL.md)')
    expect(note).toContain('- review: Reviews code. (.claude/skills/review/SKILL.md)')
    expect(note).not.toContain('The other one.')
    expect(note).not.toContain('.hidden')
    expect(note).not.toContain('/Users/me/tvbox')

    // Prompts go out as typed, and the note is not repeated.
    await submit($, 'run the tests')
    await submit($, 'second prompt')
    expect(world.sent).toEqual(['run the tests', 'second prompt'])
    expect(world.notes()).toHaveLength(1)
  })

  test(`the pane lists each step while attaching (${surface})`, async ($, on) => {
    let open = () => {}
    const world = fakeWorld(on, new Promise<void>(resolve => (open = resolve)))

    await $.command.run(RUN_RP)
    const ui = await $.ui.mount({ plugin: 'rp', surface, component: 'Pane', requestId: 'rp', props: PANE })
    const pressing = ui.press({ key: 'b-mac-p1' })
    await new Promise<void>(resolve => setTimeout(resolve, 10))

    expect(await ui.findAll({ text: /Attaching tvbox/ })).not.toHaveLength(0)
    expect(await ui.findAll({ text: 'reading…' })).not.toHaveLength(0)
    expect(await ui.findAll({ text: 'deploy, linked' })).not.toHaveLength(0)
    expect(await ui.findAll({ text: 'Add to the conversation' })).not.toHaveLength(0)
    expect(world.notes()).toHaveLength(0)

    open()
    await pressing
    expect(world.notes()).toHaveLength(1)
  })

  test(`search filters the list and Enter attaches the first match (${surface})`, async ($, on) => {
    const world = fakeWorld(on)

    await $.command.run(RUN_RP)
    const ui = await $.ui.mount({ plugin: 'rp', surface, component: 'Pane', requestId: 'rp', props: PANE })
    await ui.input({ key: 'search', text: 'AGENT', kind: 'change' })
    expect(await ui.find({ key: 'b-mac-p2' })).toBeDefined()
    expect(await ui.find({ key: 'b-mac-p1' })).toBeUndefined()
    expect(await ui.findAll({ text: 'offline' })).toHaveLength(0)

    await ui.input({ key: 'search', text: 'nothing here', kind: 'change' })
    expect(await ui.findAll({ text: /No projects match/ })).not.toHaveLength(0)

    await ui.input({ key: 'search', text: 'mac agent' })
    const note = world.notes()[0]
    expect(note).toContain('project: "p2"')
    expect(note).toContain('The project has no AGENTS.md or CLAUDE.md at its root.')
    expect(note).toContain('The project has no skills in .agents/skills or .claude/skills.')
  })
}

test('the note comes back after a compaction drops it', async ($, on) => {
  const world = fakeWorld(on)
  await $.command.run(RUN_RP)
  const ui = await $.ui.mount({ plugin: 'rp', surface: 'terminal', component: 'Pane', requestId: 'rp', props: PANE })
  await ui.press({ key: 'b-mac-p1' })

  world.keepFrom = world.notes().length
  await submit($, 'after compaction')
  await submit($, 'and again')

  const notes = world.notes()
  expect(notes).toHaveLength(2)
  expect(notes[1]).toBe(notes[0])
  expect(world.sent).toEqual(['after compaction', 'and again'])
})

test('/rp detach tells the model and stops the note', async ($, on) => {
  const world = fakeWorld(on)
  expect(await $.command.run(RUN_DETACH)).toMatchObject({ text: 'No ReadyRig project is attached.' })

  await $.command.run(RUN_RP)
  const ui = await $.ui.mount({ plugin: 'rp', surface: 'terminal', component: 'Pane', requestId: 'rp', props: PANE })
  await ui.press({ key: 'b-mac-p1' })
  expect(await $.command.run(RUN_DETACH)).toMatchObject({ text: 'Detached tvbox @ my-mac.' })

  const notes = world.notes()
  expect(notes).toHaveLength(2)
  expect(notes[1]).toContain('[rp:detached mac/p1]')

  world.keepFrom = notes.length
  await submit($, 'unrelated work')
  expect(world.notes()).toHaveLength(2)
})
