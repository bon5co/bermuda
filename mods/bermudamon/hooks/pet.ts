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

const nudge = (row: string, by: 1 | -1): string => (by === 1 ? '.' + row.slice(0, -1) : row.slice(1) + '.')
/** Walking shuffles the chin one pixel either way. */
const withTip = (body: Body, by: 1 | -1): Body => [body[0], body[1], body[2], nudge(body[3], by)]
/** Eating: the middle of the chin opens. */
const withMouth = (body: Body): Body => {
  const mid = body[3].length / 2
  return [body[0], body[1], body[2], body[3].slice(0, mid - 1) + '..' + body[3].slice(mid + 1)]
}

/**
 * A round-cheeked face: a wide top, two big eyes and a narrow chin. Each eye
 * is a 2x2 hole whose upper-right pixel stays lit, the sparkle that makes it
 * look alive. `inset` is the body's margin on each side below the top row and
 * `eye` the column of the left eye; the right eye mirrors it.
 */
const face = (top: string, inset: number, eye: number, chin: string): Body => {
  const width = top.length
  const body = (sparkle: boolean) =>
    Array.from({ length: width }, (_, x) => {
      if (x < inset || x >= width - inset) return '.'
      const right = width - 2 - eye
      const inEye = (x === eye || x === eye + 1 || x === right || x === right + 1) && !(sparkle && (x === eye + 1 || x === right + 1))
      return inEye ? '.' : '#'
    }).join('')
  return [top, body(true), body(false), chin]
}

const stage = (name: string, xp: number, body: Body, canEat = true): Stage => {
  const idle = [pack(withTip(body, -1)), pack(withTip(body, 1))] as const
  return { name, xp, body, idle, munch: canEat ? [pack(withMouth(body)), pack(body)] : idle }
}

/**
 * Evolution line: each stage unlocks at `xp`. A cute inverted-trapezoid
 * critter that grows from a seed to a big-eyed mascot: a wide top (ears
 * notched in from Rookie on), two sparkling eyes and a narrow chin. Walking
 * shuffles the chin, munching opens it. Every glyph is one column wide.
 */
export const STAGES: readonly Stage[] = [
  stage('Egg', 0, ['######', '.####.', '.####.', '..##..'], false),
  stage('Baby', 15, face('.########.', 1, 2, '...####...')),
  stage('In-Training', 60, face('.##########.', 1, 3, '....####....')),
  stage('Rookie', 200, face('##.########.##', 1, 3, '....######....')),
  stage('Champion', 700, face('##..########..##', 1, 4, '.....######.....')),
  stage('Ultimate', 2500, face('###..########..###', 1, 4, '......######......')),
  stage('Mega', 8000, face('####..##########..####', 1, 5, '.......########.......')),
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
