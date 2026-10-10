export type RpProject = { id: string; name: string; path: string }

export type RpMachine = {
  id: string
  name: string
  platform: string
  online: boolean
  projects: RpProject[]
  error?: string
}

export type RpPick = {
  machineId: string
  machineName: string
  project: RpProject
}

// A skill from .agents/skills or .claude/skills; path is project-relative.
export type RpSkill = { name: string; description: string; path: string }

// The project attached to the session, with the note the model was handed.
export type RpAttached = RpPick & {
  instructionFiles: string[]
  skills: RpSkill[]
  note: string
}

// One step of attaching, as the pane lists it while it runs.
export type RpStep = {
  id: string
  label: string
  state: 'waiting' | 'running' | 'done' | 'none' | 'failed'
  detail?: string
}

export type RpProgress = {
  machineName: string
  projectName: string
  steps: RpStep[]
  error?: string
}

declare module 'claude-code' {
  interface PluginState {
    rp: {
      machines: RpMachine[]
      isLoading: boolean
      error: string | null
      attached: RpAttached | null
      progress: RpProgress | null
      query: string
    }
  }
}
