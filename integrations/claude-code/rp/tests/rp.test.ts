import { expect, test } from 'claude-code/testing'

const COMPUTERS = {
  computers: [
    { id: 'mac', name: 'my-mac', platform: 'darwin', online: true },
    { id: 'box', name: 'my-box', platform: 'linux', online: false },
  ],
}
const PROJECTS = {
  result: {
    projects: [
      { id: 'p1', name: 'tvbox', path: '/Users/me/tvbox' },
      { id: 'p2', name: 'agent_workspace', path: '/Users/me/agent_workspace' },
    ],
  },
}
const PANE = { title: 'ReadyRig projects', isFocused: true, bodyColumns: 80, placement: 'dock' } as const

function fakeReadyRig(on: any) {
  on('tool.call', ($: any, e: any) => {
    const text = String(e.tool).endsWith('list_computers')
      ? JSON.stringify(COMPUTERS)
      : JSON.stringify(PROJECTS)
    return { result: [{ type: 'text', text }], text } as any
  })
  on('ui.open', () => ({ value: undefined }) as any)
  on('ui.close', () => ({ value: undefined }) as any)
  const sent: string[] = []
  on('prompt.submit', ($: any, e: any) => {
    sent.push(e.text)
    return { text: e.text }
  })
  return sent
}

const RUN_RP = { command: 'rp', args: '', origin: { kind: 'composer' }, presentation: { isFullscreen: true, columns: 120 } } as any

for (const surface of ['terminal', 'desktop'] as const) {
  test(`picks a project and appends it to the next prompt (${surface})`, async ($, on) => {
    const sent = fakeReadyRig(on)

    await $.command.run(RUN_RP)

    const ui = await $.ui.mount({ plugin: 'rp', surface, component: 'Pane', requestId: 'rp', props: PANE })
    expect(await ui.findAll({ text: 'offline' })).not.toHaveLength(0)
    await ui.press({ key: 'b-mac-p1' })

    await $.prompt.submit({ text: 'run the tests', wait: false, origin: { kind: 'composer' } } as any)
    await $.prompt.submit({ text: 'second prompt', wait: false, origin: { kind: 'composer' } } as any)

    expect(sent[0]).toContain('run the tests')
    expect(sent[0]).toContain('"tvbox" on machine "my-mac" (computer_id: mac)')
    expect(sent[0]).toContain('project: "p1"')
    expect(sent[0]).not.toContain('/Users/me/tvbox')
    expect(sent[1]).toBe('second prompt')
  })

  test(`search filters the list and Enter picks the first match (${surface})`, async ($, on) => {
    const sent = fakeReadyRig(on)

    await $.command.run(RUN_RP)

    const ui = await $.ui.mount({ plugin: 'rp', surface, component: 'Pane', requestId: 'rp', props: PANE })
    await ui.input({ key: 'search', text: 'AGENT', kind: 'change' })
    expect(await ui.find({ key: 'b-mac-p2' })).toBeDefined()
    expect(await ui.find({ key: 'b-mac-p1' })).toBeUndefined()
    expect(await ui.findAll({ text: 'offline' })).toHaveLength(0)

    await ui.input({ key: 'search', text: 'nothing here', kind: 'change' })
    expect(await ui.findAll({ text: /No projects match/ })).not.toHaveLength(0)

    await ui.input({ key: 'search', text: 'mac agent' })
    await $.prompt.submit({ text: 'go', wait: false, origin: { kind: 'composer' } } as any)
    expect(sent[0]).toContain('project: "p2"')
  })
}
