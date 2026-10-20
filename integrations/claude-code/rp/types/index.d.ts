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

// A folder on this computer linked to a ReadyRig project: a session started
// in it, or in a folder below it, attaches the project.
export type RpLink = RpPick & { folder: string }

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

// A picture show_image drew: a PNG of at most 2 MiB, base64, and where it is.
export type RpImage = {
  png: string
  width: number
  height: number
  path: string
  machineId: string
  machineName: string
  projectId: string
}

declare module 'claude-code' {
  interface PluginState {
    rp: {
      machines: RpMachine[]
      isLoading: boolean
      error: string | null
      attached: RpAttached | null
      // The conversation lost the attached project's note (a compaction, /clear).
      needsNote: boolean
      progress: RpProgress | null
      query: string
      // The link of the session's folder, or of the nearest folder above it.
      link: RpLink | null
      // The pane was opened by /rp link: the project picked is linked too.
      isLinking: boolean
      // By show_image call (tool_use_id); null once the picture is let go.
      image: StateFamily<RpImage | null>
    }
  }
}
