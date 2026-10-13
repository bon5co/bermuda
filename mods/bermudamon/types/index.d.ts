export type Pet = {
  xp: number
  fullness: number
  /** When `fullness` was last computed, in ms since the epoch. */
  at: number
  born: number
}

export type Limit = { kind: string; percentUsed: number; resetsAt?: string }

export type Pos = { x: number; dir: 1 | -1 }

/** An unticked, unblocked item and its number on the page, as `bermuda check tick` takes it. */
export type OpenItem = { n: number; text: string }

/** The bermuda checklist this session works against, as last read. */
export type Checklist = {
  /** The page's name, as bermuda takes it; null only when it could not be told. */
  name: string | null
  title: string
  done: number
  total: number
  blocked: number
  open: OpenItem[]
  /** The ticked items, to tell which ticks are new. */
  ticked: OpenItem[]
}

export type Tracking = {
  list: Checklist | null
  /** Files changed this session while no checklist was active. */
  isUntracked: boolean
}

declare module 'claude-code' {
  interface PluginState {
    bermudamon: {
      pet: Pet | null
      frame: number
      pos: Pos
      busy: number
      /** Index into FOODS of what the pet eats now. */
      snack: number
      tracking: Tracking
      limits: Limit[]
    }
  }
}
