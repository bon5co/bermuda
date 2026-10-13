import { expect, mock, test } from 'claude-code/testing'

import { feed, fullnessAt, hatch, revive } from '../hooks/pet'

const LIMITS = [
  { kind: 'five_hour', percentUsed: 23.5 },
  { kind: 'seven_day', percentUsed: 81 },
]

const USAGE = {
  startedAt: 0,
  context: { window: 200_000 },
  rateLimits: LIMITS,
}

test('review: main completion reasons and agent completions keep busy balanced', async ($, on) => {
  mock.clock(on, { now: 1_000_000 })
  mock.store(on, { pet: { ...hatch(1_000_000), xp: 200 } })
  const busyWrites: number[] = []
  on('session.start', () => ({ cwd: '/' }))
  on('session.usage', () => ({ value: USAGE }))
  on('turn.start', (_$, e) => ({ turnId: e.turnId }))
  on('turn.complete', () => ({ text: 'done' }))
  on('state.set', { plugin: 'bermudamon', key: 'busy' }, (_$, e, next) => {
    busyWrites.push(e.value as number)
    return next(e)
  })

  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  expect(busyWrites.at(-1)).toBe(0)

  await $.turn.start({ text: 'main', turnId: 'main-error' })
  expect(busyWrites.at(-1)).toBe(1)
  const beforeAgent = busyWrites.length
  await $.turn.complete({
    answer: 'agent',
    durationMs: 1,
    isAborted: false,
    turnId: 'agent-1',
    agentId: 'agent-1',
    reason: 'answer',
  } as never)
  expect(busyWrites.length).toBe(beforeAgent)
  expect(busyWrites.at(-1)).toBe(1)

  await $.turn.complete({
    answer: '',
    durationMs: 1,
    isAborted: false,
    turnId: 'main-error',
    reason: 'error',
  })
  expect(busyWrites.at(-1)).toBe(0)

  await $.turn.start({ text: '', turnId: 'continuation' })
  expect(busyWrites.at(-1)).toBe(1)
  await $.turn.complete({
    answer: '',
    durationMs: 1,
    isAborted: true,
    turnId: 'continuation',
    reason: 'aborted',
  })
  expect(busyWrites.at(-1)).toBe(0)

  await $.turn.start({ text: 'refuse', turnId: 'refusal' })
  await $.turn.complete({
    answer: '',
    durationMs: 1,
    isAborted: false,
    turnId: 'refusal',
    reason: 'refusal',
    refusal: { category: 'policy', explanation: null },
  })
  expect(busyWrites.at(-1)).toBe(0)
})

test('review: SessionMode validates on terminal and desktop and keeps mode labels', async ($, on) => {
  mock.clock(on, { now: 1_000_000 })
  mock.store(on, { pet: { ...hatch(1_000_000), xp: 200 } })
  on('session.start', () => ({ cwd: '/' }))
  on('session.usage', () => ({ value: USAGE }))

  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })

  for (const surface of ['terminal', 'desktop'] as const) {
    const ui = await $.ui.mount({
      plugin: 'bermudamon',
      surface,
      component: 'SessionMode',
      props: { modes: ['focus', 'memory paused'] },
    })
    expect(await ui.find({ type: 'Text', text: /focus & memory paused/ })).toBeDefined()
    expect(await ui.find({ type: 'Text', text: /76%/ })).toBeDefined()
    expect(await ui.find({ type: 'Text', text: /19%/ })).toBeDefined()
    await ui.unmount()
  }
})

test('review: the clear transition leaves one existing animation timer', async ($, on) => {
  const clock = mock.clock(on, { now: 1_000_000 })
  mock.store(on, { pet: hatch(1_000_000) })
  const frameWrites: number[] = []
  on('session.start', () => ({ cwd: '/' }))
  on('session.usage', () => ({ value: USAGE }))
  on('session.end', (_$, e) => ({ sessionId: e.sessionId }))
  on('state.set', { plugin: 'bermudamon', key: 'frame' }, (_$, e, next) => {
    frameWrites.push(e.value as number)
    return next(e)
  })

  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  expect(frameWrites).toEqual([])
  await clock.advance(500)
  expect(frameWrites).toEqual([1])

  await $.session.end({
    reason: 'clear',
    sessionId: 'old',
    resume: { id: 'old' },
  } as never)
  await clock.advance(500)
  expect(frameWrites).toEqual([1, 2])
})

test('review: animation ticks do not overlap when a state write is slow', async ($, on) => {
  const clock = mock.clock(on, { now: 1_000_000 })
  mock.store(on, { pet: hatch(1_000_000) })
  let inFlight = 0
  let maxInFlight = 0

  on('session.start', () => ({ cwd: '/' }))
  on('session.usage', () => ({ value: USAGE }))
  on('state.set', { plugin: 'bermudamon', key: 'frame' }, async (_$, e, next) => {
    inFlight += 1
    maxInFlight = Math.max(maxInFlight, inFlight)
    await clock.sleep(1_000)
    const result = await next(e)
    inFlight -= 1
    return result
  })

  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  await clock.advance(500)
  expect(inFlight).toBe(1)
  await clock.advance(500)

  expect(maxInFlight).toBe(1)
})

test('review: decay is linear, clamps at zero, and feed applies after decay', async () => {
  const pet = { ...hatch(0), fullness: 60 }
  expect(fullnessAt(pet, 3_600_000)).toBe(55)
  expect(fullnessAt(pet, 100 * 3_600_000)).toBe(0)
  expect(fullnessAt(pet, -1)).toBe(60)
  expect(feed(pet, { xp: 3, fullness: 5 }, 3_600_000)).toEqual({
    xp: 3,
    fullness: 60,
    at: 3_600_000,
    born: 0,
  })
})

test('review: revive rejects impossible finite timestamps', async () => {
  const now = 1_800_000_000_000
  const revived = revive({ xp: 5, fullness: 60, at: -1, born: -1 }, now)

  expect(revived.at).toBe(now)
  expect(revived.born).toBe(now)
  expect(fullnessAt(revived, now)).toBe(60)
})
