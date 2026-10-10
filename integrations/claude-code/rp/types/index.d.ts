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

declare module 'claude-code' {
  interface PluginState {
    rp: {
      machines: RpMachine[]
      isLoading: boolean
      error: string | null
      pick: RpPick | null
      query: string
    }
  }
}
