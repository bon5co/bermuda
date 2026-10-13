import { expect, mock, test } from 'claude-code/testing'
import type { Engine } from 'claude-code/testing'
import type { On } from 'claude-code'

import { NUDGE_EVERY, TICK_FOOD, isChecklistCommand } from '../hooks/checklist'
import { FEED, hatch } from '../hooks/pet'

type ProcessResult = { exitCode: number; stdout: string; stderr: string }
type PageEntry = { name: string; kind: 'file'; size: number; mtimeMs: number; isLink: false }

const checklist = (title: string, done: number, total = 3, blocked = 0) =>
  [
    `# ${title}`,
    '<!-- bermuda-check: {"repo":"r","branch":"b"} -->',
    '',
    ...Array.from({ length: total - blocked }, (_, i) =>
      `- [${i < done ? 'x' : ' '}] ${title} story ${i + 1} <!-- bermuda-updated: 2026-10-13T05:00:16Z -->`,
    ),
    ...Array.from(
      { length: blocked },
      (_, i) => `- [ ] ${title} blocked ${i + 1} BLOCKED: Handler (admin)`,
    ),
    '',
  ].join('\n')

const ok = (stdout: string): ProcessResult => ({ exitCode: 0, stdout, stderr: '' })
const missing = (): ProcessResult => ({ exitCode: 1, stdout: '', stderr: 'no checklist' })

const setup = (
  on: On,
  options: {
    run: (argv: readonly string[]) => ProcessResult | Promise<ProcessResult>
    files?: () => PageEntry[]
    toolResult?: () => object
    petXp?: number
    showPage?: () => string
  },
) => {
  const clock = mock.clock(on, { now: 1_000_000 })
  const argvs: readonly string[][] = []
  let stored: unknown = { ...hatch(1_000_000), xp: options.petXp ?? 100 }
  const writes: number[] = []
  on('store.set', { key: 'pet' }, ($, e, next) => {
    stored = e.value
    writes.push((e.value as { xp: number }).xp)
    return next(e)
  })
  mock.store(on, { pet: stored })
  mock.env(on, { HOME: '/home/review2' })
  on('process.run', async (_$, e) => {
    ;(argvs as string[][]).push([...e.argv])
    if (e.argv.length === 3 && e.argv[2] === 'show') {
      const page = options.showPage?.() ?? 'cwd'
      return { value: ok(`/home/review2/.bermuda/memory/checklists/${page}.md\n`) } as never
    }
    return { value: await options.run(e.argv) } as never
  })
  on('fs.list', () => ({ value: options.files?.() ?? [] }) as never)
  on('session.start', () => ({ cwd: '/repo' }))
  on('session.end', (_$, e) => ({ sessionId: e.sessionId }))
  on('session.usage', () => ({
    value: { startedAt: 0, context: { window: 200_000 }, rateLimits: [] },
  }))
  on('turn.start', (_$, e) => ({ turnId: e.turnId }))
  on('turn.complete', () => ({ text: 'done' }))
  on('tool.call', () => (options.toolResult?.() ?? { result: 'ok' }) as never)
  on('ui.toast', () => ({ value: undefined }) as never)
  const prompts: (readonly string[] | undefined)[] = []
  on('prompt.submit', (_$, e) => (prompts.push(e.context), { text: e.text }) as never)
  return { clock, argvs, prompts, writes, stored: () => stored }
}

const start = ($: Engine) => $.session.start({ cwd: '/repo', surface: 'terminal', isInteractive: true })
const edit = { tool: 'Edit', file_path: '/repo/a.ts', old_string: 'a', new_string: 'b' } as never
const bash = (command: string) => ({ tool: 'Bash', command }) as never

test('review2: readChecklist passes an explicit page before -raw, as the real CLI requires', async ($, on) => {
  const pages = {
    cwd: checklist('cwd task', 0),
    other: checklist('other task', 1),
  }
  const { argvs, clock } = setup(on, {
    // This reproduces the real CLI observed in this review: a page name after
    // -raw is ignored, while a page name before -raw is honored.
    run: argv => {
      const positional = argv[3] !== '-raw' ? argv[3] : undefined
      return ok(pages[(positional ?? 'cwd') as keyof typeof pages])
    },
    files: () => [
      { name: 'other.md', kind: 'file', size: 10, mtimeMs: clock.now(), isLink: false },
    ],
  })
  await start($)
  await $.tool.call(bash('bermuda check tick other 1'))
  await clock.settle()

  expect(argvs.at(-1)).toEqual(['bermuda', 'check', 'show', 'other', '-raw'])
})

test('review2: a parallel agent touching a newer page cannot steal the active checklist', async ($, on) => {
  const pages = {
    own: checklist('own task', 0),
    other: checklist('other agent task', 0),
  }
  const { clock, prompts } = setup(on, {
    // Honor the plugin's intended final argv here to isolate touchedSince.
    run: argv => ok(pages[(argv[3] === '-raw' ? 'own' : argv[3]) as keyof typeof pages]),
    files: () => [
      { name: 'own.md', kind: 'file', size: 10, mtimeMs: clock.now(), isLink: false },
      { name: 'other.md', kind: 'file', size: 10, mtimeMs: clock.now() + 1, isLink: false },
    ],
    showPage: () => 'own',
  })
  await start($)
  await $.tool.call(bash('bermuda check tick own 1'))
  await clock.settle()
  await $.prompt.submit({ text: 'continue' } as never)
  const note = prompts.at(-1)?.join('\n') ?? ''

  expect({ own: note.includes('"own task"'), other: note.includes('"other agent task"') }).toEqual({
    own: true,
    other: false,
  })
})

test('review2: showing an explicit page follows it even though show does not write', async ($, on) => {
  const pages = {
    cwd: checklist('cwd task', 0),
    'branch-abc shown-task': checklist('shown task', 0),
  }
  const { clock, prompts } = setup(on, {
    run: argv => ok(pages[(argv[3] === '-raw' ? 'cwd' : argv[3]) as keyof typeof pages]),
    // Both pages predate the call, exactly as a read-only show leaves them.
    files: () => [
      { name: 'cwd.md', kind: 'file', size: 10, mtimeMs: clock.now() - 1, isLink: false },
      {
        name: 'branch-abc shown-task.md',
        kind: 'file',
        size: 10,
        mtimeMs: clock.now() - 1,
        isLink: false,
      },
    ],
  })
  await start($)
  await $.tool.call(bash('bermuda check show shown-task'))
  await clock.settle()
  await $.prompt.submit({ text: 'continue' } as never)
  const note = prompts.at(-1)?.join('\n') ?? ''

  expect({ shown: note.includes('"shown task"'), cwd: note.includes('"cwd task"') }).toEqual({
    shown: true,
    cwd: false,
  })
})

test('review2: a pre-existing coarse-mtime page cannot steal an unnamed show', async ($, on) => {
  const pages = {
    cwd: checklist('cwd task', 0),
    'wrong-page': checklist('wrong task', 0),
  }
  let clock: ReturnType<typeof mock.clock>
  const harness = setup(on, {
    run: argv => ok(pages[(argv[3] === '-raw' ? 'cwd' : argv[3]) as keyof typeof pages]),
    files: () => [
      { name: 'cwd.md', kind: 'file', size: 10, mtimeMs: clock.now() - 1, isLink: false },
      { name: 'wrong-page.md', kind: 'file', size: 10, mtimeMs: clock.now(), isLink: false },
    ],
    toolResult: () => ({
      result: 'ok',
      text: '/home/review2/.bermuda/memory/checklists/cwd.md',
      isReadOnly: true,
    }),
  })
  clock = harness.clock
  await start($)
  await $.tool.call(bash('bermuda check show'))
  await clock.settle()
  await $.prompt.submit({ text: 'continue' } as never)
  const note = harness.prompts.at(-1)?.join('\n') ?? ''

  expect({ cwd: note.includes('"cwd task"'), wrong: note.includes('"wrong task"') }).toEqual({
    cwd: true,
    wrong: false,
  })
})

test('review2: an unnamed show after cd follows the other repository page', async ($, on) => {
  const pages = {
    cwd: checklist('cwd task', 0),
    'branch-deadbeef task-elsewhere': checklist('work elsewhere', 0),
  }
  let clock: ReturnType<typeof mock.clock>
  const harness = setup(on, {
    run: argv => ok(pages[(argv[3] === '-raw' ? 'cwd' : argv[3]) as keyof typeof pages]),
    files: () => [
      { name: 'cwd.md', kind: 'file', size: 10, mtimeMs: clock.now() - 1, isLink: false },
      {
        name: 'branch-deadbeef task-elsewhere.md',
        kind: 'file',
        size: 10,
        mtimeMs: clock.now() - 1,
        isLink: false,
      },
    ],
    toolResult: () => ({
      result: 'ok',
      text: '/home/review2/.bermuda/memory/checklists/branch-deadbeef task-elsewhere.md',
      isReadOnly: true,
    }),
  })
  clock = harness.clock
  await start($)
  await $.tool.call(bash('cd /tmp/review2-other && bermuda check show'))
  await clock.settle()
  await $.prompt.submit({ text: 'continue' } as never)
  const note = harness.prompts.at(-1)?.join('\n') ?? ''

  expect({ cwd: note.includes('"cwd task"'), other: note.includes('"work elsewhere"') }).toEqual({
    cwd: false,
    other: true,
  })
})

test('review2: an unnamed raw show after cd intentionally keeps the tracked page', async ($, on) => {
  const pages = {
    cwd: checklist('cwd task', 0),
    'branch-deadbeef task-elsewhere': checklist('work elsewhere', 0),
  }
  let clock: ReturnType<typeof mock.clock>
  const harness = setup(on, {
    run: argv => ok(pages[(argv[3] === '-raw' ? 'cwd' : argv[3]) as keyof typeof pages]),
    files: () => [
      { name: 'cwd.md', kind: 'file', size: 10, mtimeMs: clock.now() - 1, isLink: false },
      {
        name: 'branch-deadbeef task-elsewhere.md',
        kind: 'file',
        size: 10,
        mtimeMs: clock.now() - 1,
        isLink: false,
      },
    ],
    toolResult: () => ({ result: 'ok', text: checklist('work elsewhere', 0), isReadOnly: true }),
  })
  clock = harness.clock
  await start($)
  await $.tool.call(bash('cd /tmp/review2-other && bermuda check show -raw'))
  await clock.settle()
  await $.prompt.submit({ text: 'continue' } as never)
  const note = harness.prompts.at(-1)?.join('\n') ?? ''

  expect({ cwd: note.includes('"cwd task"'), other: note.includes('"work elsewhere"') }).toEqual({
    cwd: true,
    other: false,
  })
})

test('review2: a refused ambiguous name cannot switch the active page', async ($, on) => {
  const alpha = 'branch-9848671268a1a166d2f352a0 review2-ambiguous-fadcad9-alpha'
  const beta = 'branch-51037f59b2142131b4d8b262 review2-ambiguous-fadcad9-beta'
  const pages = {
    cwd: checklist('cwd task', 0),
    [alpha]: checklist('alpha task', 0),
    [beta]: checklist('beta task', 0),
  }
  let clock: ReturnType<typeof mock.clock>
  const harness = setup(on, {
    run: argv => ok(pages[(argv[3] === '-raw' ? 'cwd' : argv[3]) as keyof typeof pages]),
    files: () => [
      { name: 'cwd.md', kind: 'file', size: 10, mtimeMs: clock.now() - 2, isLink: false },
      { name: `${alpha}.md`, kind: 'file', size: 10, mtimeMs: clock.now() - 1, isLink: false },
      { name: `${beta}.md`, kind: 'file', size: 10, mtimeMs: clock.now(), isLink: false },
    ],
    toolResult: () => ({
      result: 'ambiguous',
      text: `2 checklists match: ${beta}, ${alpha}`,
      isError: true,
    }),
  })
  clock = harness.clock
  await start($)
  await $.tool.call(bash('bermuda check show review2-ambiguous-fadcad9'))
  await clock.settle()
  await $.prompt.submit({ text: 'continue' } as never)
  const note = harness.prompts.at(-1)?.join('\n') ?? ''

  expect({ cwd: note.includes('"cwd task"'), alpha: note.includes('"alpha task"'), beta: note.includes('"beta task"') }).toEqual({
    cwd: true,
    alpha: false,
    beta: false,
  })
})

test('review2: command detection distinguishes supported execution from mentions', async () => {
  expect({
    grep: isChecklistCommand("grep 'bermuda check' README.md"),
    unrelated: isChecklistCommand('echo bermuda; git check-ignore file'),
    alias: isChecklistCommand('bm check tick 1'),
    continued: isChecklistCommand('bermuda \\\n check tick 1'),
    fullPath: isChecklistCommand('/Users/u/.local/bin/bermuda check tick 1'),
  }).toEqual({ grep: false, unrelated: false, alias: false, continued: true, fullPath: true })
})

test('review2: mentioning bermuda check does not reset the stale-call counter', async ($, on) => {
  const page = checklist('active task', 0)
  const { prompts } = setup(on, {
    run: () => ok(page),
    files: () => [],
    toolResult: () => ({ result: 'ok', isReadOnly: true }),
  })
  await start($)
  await $.turn.start({ text: 'go', turnId: 'review2-false-positive' })
  for (let i = 0; i < NUDGE_EVERY - 1; i++) await $.tool.call(edit)
  await $.tool.call(bash("grep 'bermuda check' README.md"))
  const result = await $.tool.call(edit)
  await $.prompt.submit({ text: 'continue' } as never)

  expect(result.context?.join('\n')).toContain('file-changing calls since')
  expect(prompts.at(-1)?.join('\n')).toContain('active task')
})

test('review2: out-of-scope custom MCP writers do not count as file changes', async ($, on) => {
  const page = checklist('active task', 0)
  setup(on, { run: () => ok(page) })
  await start($)
  await $.turn.start({ text: 'go', turnId: 'review2-custom-writer' })
  const calls = []
  for (let i = 0; i < NUDGE_EVERY; i++) {
    calls.push(
      await $.tool.call({
        tool: 'mcp__filesystem__write_file',
        path: `/repo/${i}.txt`,
        content: String(i),
      } as never),
    )
  }

  expect(calls.at(-1)?.context).toBeUndefined()
})

test('review2: prompt submission waits only the intended 250 ms for a slow refresh', async ($, on) => {
  let runs = 0
  let clock: ReturnType<typeof mock.clock>
  const harness = setup(on, {
    run: async () => {
      runs += 1
      if (runs > 1) await clock.sleep(5_000)
      return ok(checklist('slow task', 0))
    },
  })
  clock = harness.clock
  await start($)

  let settled = false
  const pending = $.prompt.submit({ text: 'hello' } as never).then(result => {
    settled = true
    return result
  })
  await clock.settle()
  const settledBeforeRefresh = settled
  await clock.advance(249)
  const settledAt249ms = settled
  await clock.advance(1)
  const result = await pending

  expect({ settledBeforeRefresh, settledAt249ms, text: result.text }).toEqual({
    settledBeforeRefresh: false,
    settledAt249ms: false,
    text: 'hello',
  })
})

test('review2: a refresh beyond the intended bound uses the last reading', async ($, on) => {
  let done = 0
  let runs = 0
  let clock: ReturnType<typeof mock.clock>
  const harness = setup(on, {
    run: async () => {
      runs += 1
      const snapshot = checklist('bounded task', done, 2)
      if (runs > 1) await clock.sleep(1_000)
      return ok(snapshot)
    },
  })
  clock = harness.clock
  await start($)

  done = 1
  const prompt = $.prompt.submit({ text: 'continue' } as never)
  await clock.settle()
  await clock.advance(250)
  await prompt
  const note = harness.prompts.at(-1)?.join('\n') ?? ''

  expect(note).toContain('1. [ ] bounded task story 1')
  expect(note).toContain('2. [ ] bounded task story 2')
})

test('review2: the prompt carries items current at submission time', async ($, on) => {
  let done = 0
  const harness = setup(on, { run: () => ok(checklist('changing task', done, 2)) })
  await start($)

  done = 1
  await $.prompt.submit({ text: 'continue' } as never)
  const note = harness.prompts.at(-1)?.join('\n') ?? ''

  expect(note).not.toContain('1. [ ] changing task story 1')
  expect(note).toContain('2. [ ] changing task story 2')
})

test('review2: an older timed-out refresh cannot overwrite a newer reading', async ($, on) => {
  let done = 0
  let runs = 0
  let clock: ReturnType<typeof mock.clock>
  const harness = setup(on, {
    run: async () => {
      runs += 1
      const snapshot = checklist('ordered task', done, 2)
      if (runs === 2 || runs === 4) await clock.sleep(1_000)
      return ok(snapshot)
    },
  })
  clock = harness.clock
  await start($)

  const first = $.prompt.submit({ text: 'first' } as never)
  await clock.settle()
  await clock.advance(250)
  await first

  done = 1
  await $.prompt.submit({ text: 'second' } as never)
  await clock.advance(750)

  const third = $.prompt.submit({ text: 'third' } as never)
  await clock.settle()
  await clock.advance(250)
  await third
  const note = harness.prompts.at(-1)?.join('\n') ?? ''

  expect(note).not.toContain('1. [ ] ordered task story 1')
  expect(note).toContain('2. [ ] ordered task story 2')
})

test('review2: a checklist tool result is not held behind the follow-up refresh', async ($, on) => {
  let runs = 0
  let clock: ReturnType<typeof mock.clock>
  const harness = setup(on, {
    run: async () => {
      runs += 1
      if (runs > 1) await clock.sleep(5_000)
      return ok(checklist('slow task', 0))
    },
    files: () => [],
    toolResult: () => ({ ref: 17, result: 'exact result', text: 'exact text', context: ['core note'] }),
  })
  clock = harness.clock
  await start($)

  let settled = false
  const pending = $.tool.call(bash('bermuda check show')).then(result => {
    settled = true
    return result
  })
  await clock.settle()
  const settledBeforeRefresh = settled
  await clock.advance(5_000)
  const result = await pending

  expect({ settledBeforeRefresh, ref: result.ref, result: result.result, text: result.text }).toEqual({
    settledBeforeRefresh: true,
    ref: 17,
    result: 'exact result',
    text: 'exact text',
  })
})

test('review2: adding reminder context preserves every core tool-result field', async ($, on) => {
  const { clock } = setup(on, {
    run: () => missing(),
    toolResult: () => ({ ref: 23, result: 'exact result', text: 'exact text', context: ['core note'] }),
  })
  await start($)
  await $.turn.start({ text: 'go', turnId: 'review2-core-result' })
  const result = await $.tool.call(edit)
  await clock.settle()

  expect(result).toMatchObject({ ref: 23, result: 'exact result', text: 'exact text' })
  expect(result.context).toEqual(['core note', expect.stringContaining('no bermuda checklist')])
})

test('review2: one checklist item can award tick XP only once per session', async ($, on) => {
  let done = 1
  let clock: ReturnType<typeof mock.clock>
  const harness = setup(on, {
    run: () => ok(checklist('farm', done, 1)),
    files: () => [{ name: 'farm.md', kind: 'file', size: 10, mtimeMs: clock.now(), isLink: false }],
  })
  clock = harness.clock
  await start($)
  await clock.settle()

  for (const nextDone of [0, 1, 0, 1]) {
    done = nextDone
    await $.tool.call(bash(nextDone === 0 ? 'bermuda check untick farm 1' : 'bermuda check tick farm 1'))
    await clock.settle()
  }

  const expected = 100 + 4 * FEED.tool.xp + TICK_FOOD.xp
  expect((harness.stored() as { xp: number }).xp).toBe(expected)
})

test('review2: a read-only show cannot claim XP for another agent tick', async ($, on) => {
  let done = 1
  let clock: ReturnType<typeof mock.clock>
  const harness = setup(on, {
    run: () => ok(checklist('shared', done, 2)),
    files: () => [{ name: 'shared.md', kind: 'file', size: 10, mtimeMs: clock.now() - 1, isLink: false }],
  })
  clock = harness.clock
  await start($)
  await clock.settle()

  done = 2
  await $.tool.call(bash('bermuda check show shared'))
  await clock.settle()

  expect((harness.stored() as { xp: number }).xp).toBe(100 + FEED.tool.xp)
})

test('review2: one local tick does not claim a concurrent agent tick', async ($, on) => {
  let done = 0
  let clock: ReturnType<typeof mock.clock>
  const harness = setup(on, {
    run: () => ok(checklist('active', done, 2)),
    files: () => [{ name: 'active.md', kind: 'file', size: 10, mtimeMs: clock.now(), isLink: false }],
  })
  clock = harness.clock
  await start($)
  await $.tool.call(bash('bermuda check show active'))
  await clock.settle()

  done = 2
  await $.tool.call(bash('bermuda check tick active 1'))
  await clock.settle()

  expect((harness.stored() as { xp: number }).xp).toBe(100 + 2 * FEED.tool.xp + TICK_FOOD.xp)
})

test('review2: switching between same-title pages cannot manufacture tick XP', async ($, on) => {
  const pages = {
    cwd: checklist('same title', 0, 3),
    other: checklist('same title', 3, 3),
  }
  let clock: ReturnType<typeof mock.clock>
  const harness = setup(on, {
    run: argv => ok(pages[(argv[3] === '-raw' ? 'cwd' : argv[3]) as keyof typeof pages]),
    files: () => [{ name: 'other.md', kind: 'file', size: 10, mtimeMs: clock.now(), isLink: false }],
  })
  clock = harness.clock
  await start($)
  await clock.settle()

  await $.tool.call(bash('bermuda check tick other 3'))
  await clock.settle()

  expect((harness.stored() as { xp: number }).xp).toBe(100 + FEED.tool.xp)
})

test('review2: a tick that reveals a different-title page establishes a no-pay baseline', async ($, on) => {
  const pages = {
    cwd: checklist('first title', 0, 1),
    other: checklist('second title', 1, 1),
  }
  let clock: ReturnType<typeof mock.clock>
  const harness = setup(on, {
    run: argv => ok(pages[(argv[3] === '-raw' ? 'cwd' : argv[3]) as keyof typeof pages]),
    files: () => [{ name: 'other.md', kind: 'file', size: 10, mtimeMs: clock.now(), isLink: false }],
  })
  clock = harness.clock
  await start($)
  await clock.settle()

  await $.tool.call(bash('bermuda check tick other 1'))
  await clock.settle()

  expect((harness.stored() as { xp: number }).xp).toBe(100 + FEED.tool.xp)
})

test('review2: each duplicate-text checklist item earns XP when newly ticked', async ($, on) => {
  const duplicateRaw = (done: number) => [
    '# duplicate page',
    '<!-- bermuda-check: {"repo":"r","branch":"b"} -->',
    '',
    `${done >= 1 ? '- [x]' : '- [ ]'} repeated item`,
    `${done >= 2 ? '- [x]' : '- [ ]'} repeated item`,
    '',
  ].join('\n')
  let done = 0
  let clock: ReturnType<typeof mock.clock>
  const harness = setup(on, {
    run: () => ok(duplicateRaw(done)),
    files: () => [
      { name: 'duplicate-page.md', kind: 'file', size: 10, mtimeMs: clock.now(), isLink: false },
    ],
  })
  clock = harness.clock
  await start($)
  await $.tool.call(bash('bermuda check show duplicate-page'))
  await clock.settle()

  done = 1
  await $.tool.call(bash('bermuda check tick duplicate-page 1'))
  await clock.settle()
  done = 2
  await $.tool.call(bash('bermuda check tick duplicate-page 2'))
  await clock.settle()

  expect((harness.stored() as { xp: number }).xp).toBe(100 + 3 * FEED.tool.xp + 2 * TICK_FOOD.xp)
})

test('review2: the bounded reward ledger intentionally evicts its oldest key', async ($, on) => {
  const total = 2_001
  const ticked = new Set<number>()
  const raw = () => [
    '# bulk page',
    '<!-- bermuda-check: {"repo":"r","branch":"b"} -->',
    '',
    ...Array.from({ length: total }, (_, i) => {
      const n = i + 1
      return `- [${ticked.has(n) ? 'x' : ' '}] item ${n}`
    }),
    '',
  ].join('\n')
  let clock: ReturnType<typeof mock.clock>
  const harness = setup(on, {
    run: () => ok(raw()),
    files: () => [{ name: 'bulk.md', kind: 'file', size: 10, mtimeMs: clock.now(), isLink: false }],
  })
  clock = harness.clock
  await start($)
  await $.tool.call(bash('bermuda check show bulk'))
  await clock.settle()

  for (let n = 1; n <= total; n++) ticked.add(n)
  const allTicks = Array.from({ length: total }, (_, i) => `bermuda check tick bulk ${i + 1}`).join('; ')
  await $.tool.call(bash(allTicks))
  await clock.settle()
  ticked.delete(1)
  await $.tool.call(bash('bermuda check untick bulk 1'))
  await clock.settle()
  ticked.add(1)
  await $.tool.call(bash('bermuda check tick bulk 1'))
  await clock.settle()

  expect((harness.stored() as { xp: number }).xp).toBe(100 + 4 * FEED.tool.xp + (total + 1) * TICK_FOOD.xp)
})

test('review2: clear does not carry an old page and call count into the new conversation', async ($, on) => {
  let cwd = 'first'
  let clock: ReturnType<typeof mock.clock>
  const pages = {
    first: checklist('first task', 0),
    named: checklist('named old task', 1),
    fresh: checklist('fresh task', 0),
  }
  const harness = setup(on, {
    run: argv => ok(pages[(argv[3] === '-raw' ? cwd : argv[3]) as keyof typeof pages]),
    files: () => [{ name: 'named.md', kind: 'file', size: 10, mtimeMs: clock.now(), isLink: false }],
    showPage: () => cwd,
  })
  clock = harness.clock
  await start($)
  await $.tool.call(bash('bermuda check tick named 1'))
  for (let i = 0; i < NUDGE_EVERY - 1; i++) await $.tool.call(edit)
  await clock.settle()

  await $.session.end({
    reason: 'clear',
    sessionId: 'old',
    resume: { id: 'old' },
  } as never)
  cwd = 'fresh'
  await $.turn.start({ text: 'new conversation', turnId: 'review2-after-clear' })
  const firstPrompt = await $.prompt.submit({ text: 'continue' } as never)
  await clock.settle()
  await $.prompt.submit({ text: 'continue again' } as never)
  const firstEdit = await $.tool.call(edit)

  const note = harness.prompts.at(-1)?.join('\n') ?? ''
  expect({
    promptFresh: note.includes('"fresh task"'),
    promptOld: note.includes('"named old task"'),
    firstPromptText: firstPrompt.text,
    staleOnFirstEdit: firstEdit.context?.join('\n').includes('file-changing calls since') ?? false,
  }).toEqual({ promptFresh: true, promptOld: false, firstPromptText: 'continue', staleOnFirstEdit: false })
})

test('review2: clear cannot be undone by an in-flight checklist refresh', async ($, on) => {
  let runs = 0
  let clock: ReturnType<typeof mock.clock>
  const pages = {
    cwd: checklist('cwd task', 0),
    old: checklist('old task', 1),
  }
  const harness = setup(on, {
    run: async argv => {
      runs += 1
      if (runs > 1) await clock.sleep(1_000)
      return ok(pages[(argv[3] === '-raw' ? 'cwd' : argv[3]) as keyof typeof pages])
    },
    files: () => [{ name: 'old.md', kind: 'file', size: 10, mtimeMs: clock.now(), isLink: false }],
  })
  clock = harness.clock
  await start($)

  await $.tool.call(bash('bermuda check tick old 1'))
  const ending = $.session.end({
    reason: 'clear',
    sessionId: 'old-session',
    resume: { id: 'old-session' },
  } as never)
  await clock.settle()
  await clock.advance(1_000)
  await ending
  const firstPrompt = $.prompt.submit({ text: 'new conversation' } as never)
  await clock.settle()
  await clock.advance(250)
  await firstPrompt
  await clock.advance(750)
  const secondPrompt = $.prompt.submit({ text: 'new conversation again' } as never)
  await clock.settle()
  await clock.advance(250)
  await secondPrompt
  const note = harness.prompts.at(-1)?.join('\n') ?? ''

  expect(note).toContain('"cwd task"')
  expect(note).not.toContain('"old task"')
})

test('review2: footer validates with a list on terminal and desktop', async ($, on) => {
  setup(on, { run: () => ok(checklist('footer', 1, 3, 1)) })
  await start($)

  for (const surface of ['terminal', 'desktop'] as const) {
    const ui = await $.ui.mount({
      plugin: 'bermudamon',
      surface,
      component: 'SessionMode',
      props: { modes: [] },
    })
    expect(await ui.find({ type: 'Text', text: /1\/3/ })).toBeDefined()
    expect(await ui.find({ type: 'Text', text: /⚑ 1/ })).toBeDefined()
    await ui.unmount()
  }
})

test('review2: footer validates as untracked on terminal and desktop', async ($, on) => {
  setup(on, { run: () => missing() })
  await start($)
  await $.turn.start({ text: 'go', turnId: 'review2-footer-untracked' })
  await $.tool.call(edit)

  for (const surface of ['terminal', 'desktop'] as const) {
    const ui = await $.ui.mount({
      plugin: 'bermudamon',
      surface,
      component: 'SessionMode',
      props: { modes: [] },
    })
    expect(await ui.find({ type: 'Text', text: /no list/ })).toBeDefined()
    await ui.unmount()
  }
})

test('review2: footer holds an empty checklist slot when no page is tracked, on terminal and desktop', async ($, on) => {
  setup(on, { run: () => missing() })
  await start($)

  for (const surface of ['terminal', 'desktop'] as const) {
    const ui = await $.ui.mount({
      plugin: 'bermudamon',
      surface,
      component: 'SessionMode',
      props: { modes: [] },
    })
    expect(await ui.find({ type: 'Text', text: /☑|no list/ })).toBeUndefined()
    expect(await ui.find({ type: 'Text', text: /☐ --/ })).toBeDefined()
    await ui.unmount()
  }
})
