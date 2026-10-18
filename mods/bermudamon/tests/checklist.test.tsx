import { expect, mock, test } from 'claude-code/testing'
import type { Engine } from 'claude-code/testing'
import type { On } from 'claude-code'

import { NUDGE_EVERY, TICK_FOOD, checklistCalls, parseChecklist } from '../hooks/checklist'
import { FEED, hatch } from '../hooks/pet'

const page = (ticked: number) =>
  [
    '# ship the thing',
    '<!-- bermuda-check: {"repo":"r","branch":"b"} -->',
    '',
    ...[1, 2, 3, 4].map(
      n => `- [${n <= ticked ? 'x' : ' '}] story ${n} <!-- bermuda-updated: 2026-10-13T05:00:16Z -->`,
    ),
    '- [ ] story 5 — BLOCKED: Handler (org admin)',
    '',
  ].join('\n')

const LIMITS = [{ kind: 'five_hour', percentUsed: 10 }]

/** The world beneath the plugin: a session, a clock, a pet, and bermuda answering `show -raw` with `pages()`. */
type Page = { name: string; ageMs: number }

const world = (on: On, pages: () => string | null, written: () => readonly Page[] = () => [{ name: 'ship-the-thing', ageMs: 0 }]) => {
  const clock = mock.clock(on, { now: 1_000_000 })
  // Above the mocked store, so it sees every pet write.
  const writes: number[] = []
  on('store.set', { key: 'pet' }, ($, e, next) => (writes.push((e.value as { xp: number }).xp), next(e)))
  mock.store(on, { pet: { ...hatch(1_000_000), xp: 100 } })
  mock.env(on, { HOME: '/home/u' })
  const argvs: string[][] = []
  on('process.run', (_$, e) => {
    argvs.push([...e.argv])
    const raw = pages()
    if (raw === null) return { value: { exitCode: 1, stdout: '', stderr: 'no checklist' } } as never
    // Plain `show` prints the cwd page's path; with -raw, its bytes.
    const stdout = e.argv.includes('-raw') ? raw : '/home/u/.bermuda/memory/checklists/ship-the-thing.md\n# ship the thing\n'
    return { value: { exitCode: 0, stdout, stderr: '' } } as never
  })
  on('fs.list', () => ({
    value: written().map(page => ({ name: `${page.name}.md`, kind: 'file', size: 10, mtimeMs: clock.now() - page.ageMs, isLink: false })),
  }) as never)
  on('session.start', () => ({ cwd: '/' }))
  on('session.usage', () => ({ value: { startedAt: 0, context: { window: 200_000 }, rateLimits: LIMITS } }))
  on('turn.start', (_$, e) => ({ turnId: e.turnId }))
  on('turn.complete', () => ({ text: 'done' }))
  on('tool.call', () => ({ result: 'ok' }) as never)
  on('ui.toast', () => ({ value: undefined }) as never)
  const prompts: (readonly string[] | undefined)[] = []
  on('prompt.submit', (_$, e) => (prompts.push(e.context), { text: e.text }) as never)
  return { clock, argvs, prompts, writes }
}

const start = async ($: Engine) =>
  $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })

const edit = { tool: 'Edit', file_path: '/w/a.ts', old_string: 'a', new_string: 'b' } as never
const bash = (command: string) => ({ tool: 'Bash', command }) as never

test('parses a page: numbers, ticks, blocked items, comments stripped', async () => {
  const list = parseChecklist(page(1), null)!
  expect(list).toMatchObject({ title: 'ship the thing', done: 1, total: 5, blocked: 1 })
  expect(list.ticked).toEqual([{ n: 1, text: 'story 1' }])
  expect(list.open).toEqual([
    { n: 2, text: 'story 2' },
    { n: 3, text: 'story 3' },
    { n: 4, text: 'story 4' },
  ])
  expect(parseChecklist('no title here', null)).toBe(null)
})

test('an edit with no checklist gets one reminder per turn, and the footer says so', async ($, on) => {
  world(on, () => null)
  await start($)
  await $.turn.start({ text: 'go', turnId: 't1' })

  const first = await $.tool.call(edit)
  expect(first.context?.join('\n')).toContain('no bermuda checklist tracks this work')
  const second = await $.tool.call(edit)
  expect(second.context).toBeUndefined()

  await $.turn.start({ text: 'again', turnId: 't2' })
  const third = await $.tool.call(edit)
  expect(third.context?.join('\n')).toContain('bermuda check new')

  const ui = await $.ui.mount({ plugin: 'bermudamon', surface: 'terminal', component: 'SessionMode', props: { modes: [] } })
  expect(await ui.find({ type: 'Text', text: /no list/ })).toBeDefined()
  await ui.unmount()
})

test('a read-only or non-edit call with no checklist stays quiet', async ($, on) => {
  world(on, () => null)
  await start($)
  await $.turn.start({ text: 'go', turnId: 't1' })
  expect((await $.tool.call(bash('npm test'))).context).toBeUndefined()
})

test('every prompt carries the open items with their numbers', async ($, on) => {
  const { prompts } = world(on, () => page(1))
  await start($)
  await $.prompt.submit({ text: 'next step' } as never)
  const note = prompts.at(-1)?.join('\n') ?? ''
  expect(note).toContain('"ship the thing" is 1/5 done, 1 blocked')
  expect(note).toContain('2. [ ] story 2')
  expect(note).toContain('4. [ ] story 4')
  expect(note).not.toContain('story 5')
  expect(note).toContain('bermuda check tick "ship-the-thing" <n>')
})

test('a finished checklist adds nothing to the prompt', async ($, on) => {
  const { prompts } = world(on, () => page(5).replace('- [ ] story 5 — BLOCKED: Handler (org admin)', '- [x] story 5'))
  await start($)
  await $.prompt.submit({ text: 'next' } as never)
  expect(prompts.at(-1)).toBeUndefined()
})

test(`${NUDGE_EVERY} changing calls without a bermuda check call bring one reminder`, async ($, on) => {
  world(on, () => page(1))
  await start($)
  await $.turn.start({ text: 'go', turnId: 't1' })
  const notes: (string | undefined)[] = []
  for (let i = 0; i < NUDGE_EVERY * 2; i++) notes.push((await $.tool.call(edit)).context?.join('\n'))
  const reminded = notes.flatMap((note, i) => (note?.includes('file-changing calls since') ? [i + 1] : []))
  expect(reminded).toEqual([NUDGE_EVERY, NUDGE_EVERY * 2])
})

test('a bermuda check call resets the count, follows the page it wrote, and a tick feeds the pet', async ($, on) => {
  let ticked = 1
  const { clock, argvs, writes } = world(on, () => page(ticked))
  await start($)
  await $.turn.start({ text: 'go', turnId: 't1' })
  for (let i = 0; i < NUDGE_EVERY - 1; i++) await $.tool.call(edit)

  ticked = 3
  await $.tool.call(bash('bermuda check tick "ship-the-thing" 2 && bermuda check tick "ship-the-thing" 3'))
  await clock.settle()
  // It now reads the page the call wrote, by name.
  expect(argvs.at(-1)).toEqual(['bermuda', 'check', 'show', 'ship-the-thing', '-raw'])
  // The count started over: the next edit is not the reminder.
  expect((await $.tool.call(edit)).context).toBeUndefined()
  await clock.settle()

  const tools = NUDGE_EVERY - 1 + 1 + 1
  expect(writes.at(-1)).toBe(100 + tools * FEED.tool.xp + 2 * TICK_FOOD.xp)

  const ui = await $.ui.mount({ plugin: 'bermudamon', surface: 'terminal', component: 'SessionMode', props: { modes: [] } })
  expect(await ui.find({ type: 'Text', text: /3\/5/ })).toBeDefined()
  const flag = await ui.find({ type: 'Text', text: /⚑ 1/ })
  expect(flag).toBeDefined()
  // blocked items wait on someone: red, not dimmed, so the eye finds them
  expect(JSON.stringify(flag)).toContain('"error"')
  await ui.unmount()
})

test("a subagent's calls get no reminders", async ($, on) => {
  world(on, () => null)
  await start($)
  await $.turn.start({ text: 'go', turnId: 't1' })
  const sub = await $.tool.call({ ...(edit as object), agentId: 'a1' } as never)
  expect(sub.context).toBeUndefined()
})

test("another agent's page written in the same moment never takes this session over", async ($, on) => {
  let written: Page[] = [{ name: 'mine', ageMs: 0 }]
  const { argvs, clock } = world(on, () => page(1), () => written)
  await start($)
  await $.turn.start({ text: 'go', turnId: 't1' })

  // Named in the command: ours wins even though another page is newer.
  written = [{ name: 'theirs', ageMs: -1 }, { name: 'mine', ageMs: 0 }]
  await $.tool.call(bash('bermuda check tick "mine" 2'))
  await clock.settle()
  expect(argvs.at(-1)).toEqual(['bermuda', 'check', 'show', 'mine', '-raw'])

  // Unnamed and two pages written: keep what we had.
  written = [{ name: 'theirs', ageMs: -1 }, { name: 'other', ageMs: 0 }]
  await $.tool.call(bash('bermuda check tick 3'))
  await clock.settle()
  expect(argvs.at(-1)).toEqual(['bermuda', 'check', 'show', 'mine', '-raw'])

  // Unnamed and only one page written in the window: that is ours.
  written = [{ name: 'fresh', ageMs: 0 }, { name: 'old', ageMs: 60_000 }]
  await $.tool.call(bash('bermuda check tick 1'))
  await clock.settle()
  expect(argvs.at(-1)).toEqual(['bermuda', 'check', 'show', 'fresh', '-raw'])
})

test("a tick is paid for the items it named, not for another agent's tick seen at the same time", async ($, on) => {
  let ticked = 1
  const { clock, writes } = world(on, () => page(ticked))
  await start($)
  await clock.settle()
  const xp = () => writes.at(-1)!

  const startXp = xp()
  // This call ticks item 2; another agent ticked items 3 and 4 meanwhile.
  ticked = 4
  await $.tool.call(bash('bermuda check tick "ship-the-thing" 2'))
  await clock.settle()
  expect(xp()).toBe(startXp + FEED.tool.xp + TICK_FOOD.xp)
})

test('calls are parsed like a shell: quotes, paths, prefixes, mentions', async () => {
  expect(checklistCalls('bermuda check tick "branch-abc shown-task" 2')).toEqual([{ verb: 'tick', args: ['branch-abc shown-task', '2'] }])
  expect(checklistCalls("X=1 ~/.local/bin/bermuda check add 'my list' 'a story; with semi'")).toEqual([
    { verb: 'add', args: ['my list', 'a story; with semi'] },
  ])
  expect(checklistCalls('cd w && bermuda \\\n  check tick 3 | cat')).toEqual([{ verb: 'tick', args: ['3'] }])
  expect(checklistCalls('echo bermuda check tick 1; grep "bermuda check" notes.md')).toEqual([])
})
