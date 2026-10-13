import type { Limit, Pet, Pos } from '../types'

/** Two text rows of quadrant blocks, the Claude Code mascot's own style. */
export type Sprite = readonly [string, string]

export type Stage = {
  name: string
  xp: number
  /** The pixel art the frames derive from. */
  body: Body
  idle: readonly [Sprite, Sprite]
  munch: readonly [Sprite, Sprite]
}

/** Quadrant block for each combination of lit pixels: top-left 1, top-right 2, bottom-left 4, bottom-right 8. */
const QUADRANTS = [' ', '▘', '▝', '▀', '▖', '▌', '▞', '▛', '▗', '▚', '▐', '▜', '▄', '▙', '▟', '█'] as const

export type Body = readonly [string, string, string, string]

/** Four pixel rows ('#' lit, '.' dark) of an even width, packed two pixels per cell into two text rows. */
export const pack = (pixels: Body): Sprite => {
  const cell = (top: string, bottom: string, x: number) =>
    QUADRANTS[
      (top[x] === '#' ? 1 : 0) | (top[x + 1] === '#' ? 2 : 0) | (bottom[x] === '#' ? 4 : 0) | (bottom[x + 1] === '#' ? 8 : 0)
    ]!
  const row = (top: string, bottom: string) =>
    Array.from({ length: top.length / 2 }, (_, i) => cell(top, bottom, i * 2)).join('')
  return [row(pixels[0], pixels[1]), row(pixels[2], pixels[3])]
}

const put = (row: string, x: number, c: '#' | '.'): string => row.slice(0, x) + c + row.slice(x + 1)

/**
 * A square critter. Pixels are twice as tall as wide, so a body three rows
 * tall and as many pixels wide as it is tall again reads as a square. `top`
 * is the roof (its length is the sprite's width, its notches the ears), `eye`
 * the column of the left dot eye and left foot; the right ones mirror them.
 * The hands are single pixels on the outer columns: down by its side, or up.
 */
const critter = (top: string, eye: number) => {
  const far = top.length - 1
  const right = far - eye
  const trunk = Array.from({ length: top.length }, (_, x) => (x >= 1 && x < far ? '#' : '.')).join('')
  const eyes = put(put(trunk, eye, '.'), right, '.')
  const feet = put(put('.'.repeat(top.length), eye, '#'), right, '#')
  const hands = (row: string) => put(put(row, 0, '#'), far, '#')
  const mid = top.length / 2
  const down: Body = [top, eyes, hands(trunk), feet]
  const up: Body = [top, hands(eyes), trunk, feet]
  const chewing: Body = [top, up[1], put(put(trunk, mid - 1, '.'), mid, '.'), feet]
  return { down, up, chewing }
}

const stage = (name: string, xp: number, top: string, eye: number): Stage => {
  const { down, up, chewing } = critter(top, eye)
  return { name, xp, body: down, idle: [pack(down), pack(up)], munch: [pack(chewing), pack(down)] }
}

/** Wobbles in place: it cannot walk, eat or wave yet. */
const egg = (): Stage => {
  const body: Body = ['.####.', '.####.', '######', '..##..']
  const wobble: Body = [body[0], body[1], body[2], '...##.']
  const idle = [pack(body), pack(wobble)] as const
  return { name: 'Egg', xp: 0, body, idle, munch: idle }
}

/**
 * Evolution line: each stage unlocks at `xp`. A square critter with dot
 * eyes, little feet and tiny hands that wave while it walks and come up to
 * its mouth while it eats. It grows a little wider and earns ears and a
 * crown along the way. Every glyph is one column wide.
 */
export const STAGES: readonly Stage[] = [
  egg(),
  stage('Baby', 15, '.######.', 2),
  stage('In-Training', 60, '.##..##.', 2),
  stage('Rookie', 200, '.########.', 3),
  stage('Champion', 700, '.#.####.#.', 3),
  stage('Ultimate', 2500, '.##########.', 3),
  stage('Mega', 8000, '.#.######.#.', 3),
]

/** Columns the pet walks in, sprite included. */
export const TRACK = 18
/**
 * Snacks drawn on the bottom row beside the pet while it eats. Each is one
 * astral code point (two UTF-16 units) drawn two columns wide, so `length`
 * is its width in columns.
 */
export const FOODS = ['🍖', '🍕', '🍩', '🍪', '🍣', '🍔', '🌮', '🍙', '🧁', '🍎'] as const
const HOUR = 3_600_000
const DECAY_PER_HOUR = 5

export const FEED = {
  tool: { xp: 1, fullness: 2 },
  turn: { xp: 3, fullness: 5 },
} as const

export const stageIndex = (xp: number): number => {
  let i = 0
  STAGES.forEach((s, n) => {
    if (xp >= s.xp) i = n
  })
  return i
}

export const width = (s: string): number => [...s].length

export const hatch = (now: number): Pet => ({ xp: 0, fullness: 60, at: now, born: now })

const finite = (value: unknown): value is number => typeof value === 'number' && Number.isFinite(value)

/** A real moment no later than now: a negative or future time is damage. */
const isPast = (value: unknown, now: number): value is number => finite(value) && value > 0 && value <= now

/** A stored value as a Pet: fields of an older or damaged shape are repaired, never trusted. */
export const revive = (value: unknown, now: number): Pet => {
  const fresh = hatch(now)
  if (typeof value !== 'object' || value === null) return fresh
  const v = value as Record<string, unknown>
  return {
    xp: finite(v.xp) && v.xp >= 0 ? Math.floor(v.xp) : fresh.xp,
    fullness: finite(v.fullness) ? Math.max(0, Math.min(100, v.fullness)) : fresh.fullness,
    at: isPast(v.at, now) ? v.at : now,
    born: isPast(v.born, now) ? v.born : now,
  }
}

/** Fullness after decay, computed lazily from the last write. */
export const fullnessAt = (pet: Pet, now: number): number =>
  Math.max(0, pet.fullness - (DECAY_PER_HOUR * Math.max(0, now - pet.at)) / HOUR)

/** A feed sampled its clock before a later one landed: time never runs backwards. */
export const feed = (pet: Pet, food: { xp: number; fullness: number }, now: number): Pet => {
  const at = Math.max(now, pet.at)
  return {
    ...pet,
    xp: pet.xp + food.xp,
    fullness: Math.min(100, fullnessAt(pet, at) + food.fullness),
    at,
  }
}

/** One wander step: mostly walk, sometimes turn or rest, bounce off the ends. */
export const wander = (pos: Pos, maxX: number, roll: number): Pos => {
  if (maxX <= 0) return { x: 0, dir: pos.dir }
  let dir = pos.dir
  if (roll < 0.1) dir = dir === 1 ? -1 : 1
  else if (roll >= 0.7) return { x: Math.min(pos.x, maxX), dir }
  let x = pos.x + dir
  if (x > maxX) {
    x = maxX - 1
    dir = -1
  } else if (x < 0) {
    x = 1
    dir = 1
  }
  return { x: Math.max(0, Math.min(x, maxX)), dir }
}

export type Row = { pad: number; food: string; body: string }

/** The pet's two rows inside the track: walking at `x`, or at the right end eating. */
export const petRows = (pet: Pet, pos: Pos, frame: number, isBusy: boolean, snack = 0): Row[] => {
  const index = stageIndex(pet.xp)
  const stage = STAGES[index]!
  // An egg cannot eat or walk: it wobbles in the corner.
  const isEating = isBusy && index > 0
  const sprite = (isEating ? stage.munch : stage.idle)[frame % 2]!
  const w = width(sprite[0])
  const food = isEating ? FOODS[snack % FOODS.length]! : ''
  const x = isEating || index === 0 ? TRACK - w - food.length : Math.min(pos.x, Math.max(0, TRACK - w))
  return sprite.map((body, i) => ({
    pad: x,
    food: i === sprite.length - 1 ? food : ' '.repeat(food.length),
    body: body + ' '.repeat(Math.max(0, TRACK - x - food.length - w)),
  }))
}

export const CELLS = 8

export type Meter = { label: string; remaining: number | null; bar: string }

/** Remaining share of a window as a bar; null when the engine has no reading. */
export const meter = (limits: readonly Limit[], kind: string, label: string): Meter => {
  const limit = limits.find(l => l.kind === kind)
  if (!limit) return { label, remaining: null, bar: '▱'.repeat(CELLS) }
  // Round the used share first, as /usage does, so both screens agree.
  const remaining = Math.max(0, Math.min(100, 100 - Math.round(limit.percentUsed)))
  const full = Math.round((remaining / 100) * CELLS)
  return { label, remaining, bar: '▰'.repeat(full) + '▱'.repeat(CELLS - full) }
}

export const meterColor = (remaining: number | null): 'success' | 'warning' | 'error' | 'inactive' =>
  remaining === null ? 'inactive' : remaining > 50 ? 'success' : remaining > 20 ? 'warning' : 'error'
