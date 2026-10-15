import { expect, mock, test } from 'claude-code/testing'

import { hatch } from '../hooks/pet'

const USAGE = {
  startedAt: 0,
  context: { window: 200_000 },
  rateLimits: [],
}

test('review: a successful tool result does not wait for the pet store write', async ($, on) => {
  const clock = mock.clock(on, { now: 1_000_000 })
  let stored: unknown = { ...hatch(1_000_000), xp: 20 }
  let toolRan = false
  let settled = false
  let answer: unknown

  on('store.get', { key: 'pet' }, () => ({ value: stored }) as never)
  on('store.set', { key: 'pet' }, async (_$, e) => {
    await clock.sleep(1_000)
    stored = e.value
    return { value: undefined } as never
  })
  on('session.start', () => ({ cwd: '/' }))
  on('session.usage', () => ({ value: USAGE }))
  on('tool.call', () => {
    toolRan = true
    return { result: 'ok' } as never
  })

  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  const pending = $.tool
    .call({ tool: 'Bash', input: { command: 'true' } } as never)
    .then(result => {
      answer = result
      settled = true
    })

  await clock.settle()
  expect(toolRan).toBe(true)
  expect(settled).toBe(true)

  await clock.advance(1_000)
  await pending
  expect((answer as { result: string }).result).toBe('ok')
})

test('review: completed-turn feed is durable when the session ends immediately', async ($, on) => {
  const clock = mock.clock(on, { now: 1_000_000 })
  let stored: unknown = { ...hatch(1_000_000), xp: 20 }
  let xpAtSessionEnd: number | undefined
  let delayNextPetRead = false

  on('store.get', { key: 'pet' }, () => ({ value: stored }) as never)
  on('store.set', { key: 'pet' }, (_$, e) => {
    stored = e.value
    return { value: undefined } as never
  })
  on('state.get', { plugin: 'bermudamon', key: 'pet' }, async (_$, e, next) => {
    if (delayNextPetRead) {
      delayNextPetRead = false
      await clock.sleep(1_000)
    }
    return next(e)
  })
  on('session.start', () => ({ cwd: '/' }))
  on('session.end', (_$, e) => {
    xpAtSessionEnd = (stored as { xp: number }).xp
    return { sessionId: e.sessionId }
  })
  on('session.usage', () => ({ value: USAGE }))
  on('turn.complete', () => ({ text: 'done' }))

  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  await clock.settle()
  delayNextPetRead = true
  await $.turn.complete({
    answer: 'done',
    durationMs: 1,
    isAborted: false,
    turnId: 'last-turn',
    reason: 'answer',
  })
  const ending = $.session.end({
    reason: 'prompt_input_exit',
    sessionId: 'ending',
    resume: { id: 'ending' },
  } as never)
  await clock.settle()
  await clock.advance(1_000)
  await ending

  expect(xpAtSessionEnd).toBe(23)
})

test('review evidence: non-answer turn completions do not feed', async ($, on) => {
  const clock = mock.clock(on, { now: 1_000_000 })
  let stored: unknown = { ...hatch(1_000_000), xp: 20 }

  on('store.get', { key: 'pet' }, () => ({ value: stored }) as never)
  on('store.set', { key: 'pet' }, (_$, e) => {
    stored = e.value
    return { value: undefined } as never
  })
  on('session.start', () => ({ cwd: '/' }))
  on('session.usage', () => ({ value: USAGE }))
  on('turn.complete', () => ({ text: '' }))

  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  await clock.settle()
  for (const reason of ['error', 'aborted', 'refusal'] as const) {
    await $.turn.complete({
      answer: '',
      durationMs: 1,
      isAborted: reason === 'aborted',
      turnId: reason,
      reason,
      ...(reason === 'refusal' ? { refusal: { category: 'policy', explanation: null } } : {}),
    } as never)
  }
  await clock.settle()

  expect((stored as { xp: number }).xp).toBe(20)
})

test('review: reordered parallel pet writes never regress persisted xp', async ($, on) => {
  const clock = mock.clock(on, { now: 1_000_000 })
  let stored: unknown = { ...hatch(1_000_000), xp: 13 }
  const writes: number[] = []
  const toasts: string[] = []

  on('store.get', { key: 'pet' }, () => ({ value: stored }) as never)
  on('store.set', { key: 'pet' }, async (_$, e) => {
    const xp = (e.value as { xp: number }).xp
    if (xp === 14) await clock.sleep(1_000)
    stored = e.value
    writes.push(xp)
    return { value: undefined } as never
  })
  on('ui.toast', (_$, e) => (toasts.push(String(e.text)), { value: undefined }) as never)
  on('session.start', () => ({ cwd: '/' }))
  on('session.usage', () => ({ value: USAGE }))
  on('tool.call', () => ({ result: 'ok' }) as never)

  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })

  const first = $.tool.call({ tool: 'Bash', input: { command: 'first' } } as never)
  await clock.settle()
  const second = $.tool.call({ tool: 'Bash', input: { command: 'second' } } as never)
  await second

  await clock.advance(1_000)
  await clock.settle()
  await first

  expect(writes).toEqual([...writes].sort((a, b) => a - b))
  expect(writes.at(-1)).toBe(15)
  expect(toasts).toEqual([])
  expect((stored as { xp: number }).xp).toBe(15)
})

test('review: a delayed earlier feed never moves the decay timestamp backward', async ($, on) => {
  const clock = mock.clock(on, { now: 1_000_000 })
  let stored: unknown = { ...hatch(1_000_000), xp: 20 }
  let delayNextPetRead = false

  on('store.get', { key: 'pet' }, () => ({ value: stored }) as never)
  on('store.set', { key: 'pet' }, (_$, e) => {
    stored = e.value
    return { value: undefined } as never
  })
  on('state.get', { plugin: 'bermudamon', key: 'pet' }, async (_$, e, next) => {
    if (delayNextPetRead) {
      delayNextPetRead = false
      await clock.sleep(1_000)
    }
    return next(e)
  })
  on('session.start', () => ({ cwd: '/' }))
  on('session.usage', () => ({ value: USAGE }))
  on('tool.call', () => ({ result: 'ok' }) as never)

  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  await clock.settle()

  delayNextPetRead = true
  await $.tool.call({ tool: 'Bash', input: { command: 'earlier-but-delayed' } } as never)
  await clock.settle()
  await clock.advance(100)

  await $.tool.call({ tool: 'Bash', input: { command: 'later-but-fast' } } as never)
  await clock.settle()
  await clock.advance(900)
  await clock.settle()

  expect((stored as { at: number }).at).toBe(1_000_100)
})

test('review: a failed evolution write loses nothing and shows no toast', async ($, on) => {
  const clock = mock.clock(on, { now: 1_000_000 })
  let stored: unknown = { ...hatch(1_000_000), xp: 14 }
  const toasts: string[] = []

  on('store.get', { key: 'pet' }, () => ({ value: stored }) as never)
  on('store.set', { key: 'pet' }, (_$, e) => {
    if ((e.value as { xp: number }).xp === 15) return { deny: 'injected store failure' } as never
    stored = e.value
    return { value: undefined } as never
  })
  on('ui.toast', (_$, e) => (toasts.push(String(e.text)), { value: undefined }) as never)
  on('session.start', () => ({ cwd: '/' }))
  on('session.usage', () => ({ value: USAGE }))
  on('tool.call', () => ({ result: 'ok' }) as never)

  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  await $.tool.call({ tool: 'Bash', input: { command: 'cross-threshold' } } as never)
  await clock.settle()
  await $.tool.call({ tool: 'Bash', input: { command: 'persist-next-feed' } } as never)
  await clock.settle()

  expect((stored as { xp: number }).xp).toBe(16)
  expect(toasts).toEqual([])
})

test('review: an old-shape stored pet is replaced with a valid hatch', async ($, on) => {
  const clock = mock.clock(on, { now: 1_000_000 })
  let stored: unknown = { xp: 15 }

  on('store.get', { key: 'pet' }, () => ({ value: stored }) as never)
  on('store.set', { key: 'pet' }, (_$, e) => {
    stored = JSON.parse(JSON.stringify(e.value))
    return { value: undefined } as never
  })
  on('session.start', () => ({ cwd: '/' }))
  on('session.usage', () => ({ value: USAGE }))
  on('tool.call', () => ({ result: 'ok' }) as never)

  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  await $.tool.call({ tool: 'Bash', input: { command: 'true' } } as never)
  await clock.settle()
  const pet = stored as { xp: number; fullness: number; at: number; born: number }

  expect({
    finiteXp: Number.isFinite(pet.xp),
    finiteFullness: Number.isFinite(pet.fullness),
    finiteAt: Number.isFinite(pet.at),
    finiteBorn: Number.isFinite(pet.born),
  }).toEqual({
    finiteXp: true,
    finiteFullness: true,
    finiteAt: true,
    finiteBorn: true,
  })
})
