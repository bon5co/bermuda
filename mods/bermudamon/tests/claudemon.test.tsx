import { expect, mock, test } from 'claude-code/testing'

import { FEED, FOODS, STAGES, TRACK, feed, fullnessAt, hatch, meter, petRows, revive, stageIndex, wander, width } from '../hooks/pet'

const FOOD_RE = new RegExp(FOODS.join('|'), 'u')

const LIMITS = [
  { kind: 'five_hour', percentUsed: 23.5 },
  { kind: 'seven_day', percentUsed: 81 },
]


test('stages climb with xp and every sprite fits the track', async () => {
  expect(stageIndex(0)).toBe(0)
  expect(stageIndex(14)).toBe(0)
  expect(stageIndex(15)).toBe(1)
  expect(stageIndex(8000)).toBe(STAGES.length - 1)
  for (const s of STAGES)
    for (const f of [...s.idle, ...s.munch]) {
      expect(f.length).toBe(2)
      expect(width(f[0])).toBe(width(f[1]))
      expect(width(f[0]) + 2).toBeLessThanOrEqual(TRACK)
    }
})

test('wander stays inside the track', async () => {
  let pos = { x: 0, dir: 1 as 1 | -1 }
  for (let i = 0; i < 500; i++) {
    pos = wander(pos, 9, (i * 0.37) % 1)
    expect(pos.x).toBeGreaterThanOrEqual(0)
    expect(pos.x).toBeLessThanOrEqual(9)
  }
})

test('idle rows move with x, munch rows show food, every row fills the track', async () => {
  const rookie = { ...hatch(0), xp: 200 }
  const at = (x: number) => petRows(rookie, { x, dir: 1 }, 0, false)
  expect(at(0)).not.toEqual(at(5))
  for (const busy of [false, true])
    for (const frame of [0, 1])
      for (const row of petRows(rookie, { x: 3, dir: 1 }, frame, busy))
        expect(row.pad + row.food.length + width(row.body)).toBe(TRACK)
  FOODS.forEach((food, i) => {
    expect(food.length).toBe(2)
    expect(petRows(rookie, { x: 0, dir: 1 }, 0, true, i)[1]!.food).toBe(food)
  })
  expect(petRows(rookie, { x: 0, dir: 1 }, 0, false)[1]!.food).toBe('')
  // An egg never eats.
  expect(petRows(hatch(0), { x: 0, dir: 1 }, 0, true)[1]!.food).toBe('')
})

test('meters show remaining = 100 - round(percentUsed), as /usage rounds', async () => {
  expect(meter(LIMITS, 'five_hour', '5h')).toMatchObject({ remaining: 76, bar: '▰▰▰▰▰▰▱▱' })
  expect(meter(LIMITS, 'seven_day', 'wk')).toMatchObject({ remaining: 19, bar: '▰▰▱▱▱▱▱▱' })
  expect(meter([], 'five_hour', '5h').remaining).toBe(null)
})

test('using Claude Code feeds it, persists it, and the footer draws it', async ($, on) => {
  const clock = mock.clock(on, { now: 1_000_000 })
  const writes: { key: string; value: unknown }[] = []
  on('store.set', { key: 'pet' }, ($, e, next) => (writes.push({ key: e.key, value: e.value }), next(e)))
  mock.store(on, { pet: { ...hatch(0), xp: 14, at: 1_000_000 } })
  const toasts: string[] = []
  on('ui.toast', (_$, e) => (toasts.push(String(e.text)), { value: undefined }) as never)
  on('session.start', () => ({ cwd: '/' }))
  on('session.usage', () => ({ value: { startedAt: 0, context: { window: 200_000 }, rateLimits: LIMITS } }))
  on('turn.start', (_$, e) => ({ turnId: e.turnId }))
  on('turn.complete', () => ({ text: 'done' }))
  on('tool.call', () => ({ result: 'ok' }) as never)

  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })

  // Busy: a turn starts and a tool runs.
  await $.turn.start({ text: 'hi', turnId: 't1' })
  await $.tool.call({ tool: 'Bash', input: { command: 'ls' } } as never)
  await clock.settle()
  await clock.advance(500)
  const busyUi = await $.ui.mount({ plugin: 'bermudamon', surface: 'terminal', component: 'SessionMode', props: { modes: [] } })
  expect(await busyUi.find({ type: 'Text', text: FOOD_RE })).toBeDefined()
  await busyUi.unmount()

  await $.turn.complete({ answer: 'x', durationMs: 1, isAborted: false, turnId: 't1', reason: 'answer' } as never)
  // Feeding runs in the background, off the turn's path.
  await clock.settle()

  // 14 + tool 1 + turn 3 = 18 xp: hatched from Egg to Baby.
  const saved = writes.filter(w => w.key === 'pet').at(-1)?.value as { xp: number }
  expect(saved.xp).toBe(14 + FEED.tool.xp + FEED.turn.xp)
  expect(toasts).toEqual(['✨ Bermudamon digivolved into Baby!'])

  const ui = await $.ui.mount({ plugin: 'bermudamon', surface: 'terminal', component: 'SessionMode', props: { modes: ['focus'] } })
  expect(await ui.find({ type: 'Text', text: new RegExp(STAGES[1]!.idle[0][0].replace(/[|\\{}()[\]^$+*?.]/g, '\\$&')) })).toBeDefined()
  expect(await ui.find({ type: 'Text', text: /76%/ })).toBeDefined()
  expect(await ui.find({ type: 'Text', text: /19%/ })).toBeDefined()
  expect(await ui.find({ type: 'Text', text: /focus/ })).toBeDefined()
  expect(await ui.find({ type: 'Text', text: FOOD_RE })).toBeUndefined()
  await ui.unmount()
})

test('a stored pet of an old or damaged shape is repaired, never trusted', async () => {
  expect(revive({ xp: 15 }, 7)).toEqual({ xp: 15, fullness: 60, at: 7, born: 7 })
  expect(revive({ xp: 'x', fullness: Infinity, at: null }, 7)).toEqual(hatch(7))
  expect(revive({ xp: 5, fullness: 250, at: 3, born: 1 }, 7)).toEqual({ xp: 5, fullness: 100, at: 3, born: 1 })
  expect(revive(undefined, 7)).toEqual(hatch(7))
  const now = 1_800_000_000_000
  expect(revive({ xp: 1, fullness: 60, at: -1, born: -1 }, now)).toEqual({ xp: 1, fullness: 60, at: now, born: now })
  expect(revive({ xp: 1, fullness: 60, at: now + 5, born: now + 5 }, now)).toEqual({ xp: 1, fullness: 60, at: now, born: now })
})

test('parallel feeds with a slow save still leave the store on the latest pet', async ($, on) => {
  const clock = mock.clock(on, { now: 1_000_000 })
  let stored: unknown = { ...hatch(1_000_000), xp: 13 }
  const writes: number[] = []
  on('store.get', { key: 'pet' }, () => ({ value: stored }) as never)
  on('store.set', { key: 'pet' }, async (_$, e) => {
    const xp = (e.value as { xp: number }).xp
    if (xp === 14) await clock.sleep(1_000)
    stored = e.value
    writes.push(xp)
    return { value: undefined } as never
  })
  on('ui.toast', () => ({ value: undefined }) as never)
  on('session.start', () => ({ cwd: '/' }))
  on('session.usage', () => ({ value: { startedAt: 0, context: { window: 200_000 }, rateLimits: LIMITS } }))
  on('tool.call', () => ({ result: 'ok' }) as never)

  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  await clock.settle()
  const first = $.tool.call({ tool: 'Bash', input: { command: 'first' } } as never)
  await clock.settle()
  const second = $.tool.call({ tool: 'Bash', input: { command: 'second' } } as never)
  await Promise.all([first, second])
  await clock.advance(1_000)
  await clock.settle()

  expect((stored as { xp: number }).xp).toBe(15)
  expect(writes.at(-1)).toBe(15)
  // Saves never go backwards.
  expect([...writes].sort((a, b) => a - b)).toEqual(writes)
})

test('a late feed with an earlier clock never moves the pet back in time', async () => {
  const later = feed({ ...hatch(0), fullness: 50, at: 1_000_100 }, FEED.tool, 1_000_000)
  expect(later.at).toBe(1_000_100)
  expect(fullnessAt(later, 1_000_100)).toBe(50 + FEED.tool.fullness)
})

test('a slow animation write makes later ticks skip, never stack', async ($, on) => {
  const clock = mock.clock(on, { now: 1_000_000 })
  mock.store(on, { pet: { ...hatch(1_000_000), xp: 20 } })
  let inFlight = 0
  let peak = 0
  on('state.set', { key: 'frame' }, async ($, e, next) => {
    inFlight += 1
    peak = Math.max(peak, inFlight)
    await clock.sleep(1_200)
    const result = await next(e)
    inFlight -= 1
    return result
  })
  on('session.start', () => ({ cwd: '/' }))
  on('session.usage', () => ({ value: { startedAt: 0, context: { window: 200_000 }, rateLimits: LIMITS } }))
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  for (let i = 0; i < 12; i++) await clock.advance(250)
  expect(peak).toBe(1)
})

test('session.end lands a completed turn whose feed is still running', async ($, on) => {
  const clock = mock.clock(on, { now: 1_000_000 })
  let stored: unknown = { ...hatch(1_000_000), xp: 20 }
  on('store.get', { key: 'pet' }, () => ({ value: stored }) as never)
  on('store.set', { key: 'pet' }, (_$, e) => ((stored = e.value), { value: undefined }) as never)
  let isSlow = false
  on('state.get', { key: 'pet' }, async ($, e, next) => {
    if (isSlow) await clock.sleep(1_000)
    return next(e)
  })
  on('session.start', () => ({ cwd: '/' }))
  on('session.usage', () => ({ value: { startedAt: 0, context: { window: 200_000 }, rateLimits: LIMITS } }))
  on('turn.start', (_$, e) => ({ turnId: e.turnId }))
  on('turn.complete', () => ({ text: 'done' }))
  // What the store holds when the end reaches the engine: the moment the process may exit.
  let atEnd = -1
  on('session.end', () => ((atEnd = (stored as { xp: number }).xp), { sessionId: 's' }) as never)
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  await clock.settle()

  isSlow = true
  await $.turn.start({ text: 'hi', turnId: 't1' })
  await $.turn.complete({ answer: 'x', durationMs: 1, isAborted: false, turnId: 't1', reason: 'answer' } as never)
  const ending = $.session.end({ reason: 'prompt_input_exit' } as never)
  for (let i = 0; i < 8; i++) await clock.advance(500)
  await ending

  expect(atEnd).toBe(20 + FEED.turn.xp)
})

test('the eyes stay open in every frame from Baby on', async () => {
  for (const stage of STAGES.slice(1))
    for (const [head] of [...stage.idle, ...stage.munch]) {
      expect(head).toContain('▛')
      expect(head).toContain('▜')
    }
})
