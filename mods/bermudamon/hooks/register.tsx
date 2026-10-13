import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register } from 'claude-code'

import type { Checklist, Limit, OpenItem, Pet } from '../types'
import {
  NUDGE_EVERY,
  TICK_FOOD,
  checklistCalls,
  type CheckCall,
  isEdit,
  isMutating,
  LISTING_VERBS,
  noChecklistReminder,
  openItemsReminder,
  parseChecklist,
  staleReminder,
} from './checklist'
import {
  FEED,
  FOODS,
  STAGES,
  TRACK,
  feed,
  hatch,
  revive,
  meter,
  meterColor,
  petRows,
  stageIndex,
  wander,
  width,
} from './pet'

const pet = atom({ plugin: 'bermudamon', key: 'pet' } as const, null)
const frame = atom({ plugin: 'bermudamon', key: 'frame' } as const, 0)
const pos = atom({ plugin: 'bermudamon', key: 'pos' } as const, { x: 0, dir: 1 })
const busy = atom({ plugin: 'bermudamon', key: 'busy' } as const, 0)
const limits = atom({ plugin: 'bermudamon', key: 'limits' } as const, [])
const snack = atom({ plugin: 'bermudamon', key: 'snack' } as const, 0)
const tracking = atom({ plugin: 'bermudamon', key: 'tracking' } as const, { list: null, isUntracked: false })

const STORE_KEY = 'pet'
const TICK_MS = 500
/** Ticks between snacks while eating: a new one every 4 s. */
const SNACK_TICKS = 8

const pickSnack = ($: EngineInterface) => update($, snack, () => Math.floor(Math.random() * FOODS.length))

// One save at a time, each writing the pet as it stands when the save runs:
// parallel feeds can finish in any order and the store still ends on the latest.
let saving: Promise<void> = Promise.resolve()
const persist = ($: EngineInterface) => {
  saving = saving
    .then(async () => {
      const latest = await read($, pet)
      if (latest) await $.store.set(STORE_KEY, latest)
    })
    .catch(() => undefined)
}

// Feeds run off the tool's and the turn's path; session.end waits for them.
const pending = new Set<Promise<void>>()
const inBackground = (work: Promise<void>) => {
  const running: Promise<void> = work.catch(() => undefined).finally(() => pending.delete(running))
  pending.add(running)
}
const flush = async () => {
  while (pending.size > 0) await Promise.all([...pending])
  await saving
}

const eat = async ($: EngineInterface, food: { xp: number; fullness: number }) => {
  const now = await $.clock.now()
  let before: Pet | null = null
  const after = await update($, pet, current => {
    before = current
    return feed(current ?? hatch(now), food, now)
  })
  if (!after) return
  persist($)

  const was = before === null ? 0 : stageIndex((before as Pet).xp)
  const is = stageIndex(after.xp)
  if (is > was) $.ui.toast(`✨ Bermudamon digivolved into ${STAGES[is]!.name}!`)
}

// The bermuda checklist this session works against. The agent's own
// `bermuda check` calls name it (the page they touch); until one does, it is
// whatever the working directory resolves, as `bermuda check show` reads it.
let sessionList: string | null = null
let changesSinceCheck = 0
let currentTurn = ''
let remindedTurn: string | null = null
/** Bumped by every reset, so work begun before a /clear cannot write into the new conversation. */
let epoch = 0

/** A new conversation (start, /clear) starts with no page of its own and a zero count. */
const resetTracking = ($: EngineInterface) => {
  epoch += 1
  sessionList = null
  changesSinceCheck = 0
  remindedTurn = null
  return update($, tracking, () => ({ list: null, isUntracked: false }))
}

/** The most a prompt waits for a fresh checklist reading. */
const REFRESH_WAIT_MS = 250
/** Items already paid for, across sessions, so untick and tick again earns nothing. */
const REWARDED_KEY = 'rewarded'
const REWARDED_MAX = 2_000

const runBermuda = ($: EngineInterface, argv: readonly string[]) =>
  $.process.run(['bermuda', 'check', ...argv], { timeoutMs: 3_000 })

/** A page by name, or the cwd's page with its name told (plain `show` prints its path). */
const readChecklist = async ($: EngineInterface, name: string | null): Promise<Checklist | null> => {
  let page = name
  if (page === null) {
    const shown = await runBermuda($, ['show'])
    const path = shown.exitCode === 0 ? shown.stdout.split('\n')[0]!.trim() : ''
    if (!path.endsWith('.md')) return null
    page = path.slice(path.lastIndexOf('/') + 1, -'.md'.length)
  }
  // The name goes before -raw: after it, bermuda ignores the name and shows the cwd's page.
  const { exitCode, stdout } = await runBermuda($, ['show', page, '-raw'])
  return exitCode === 0 ? parseChecklist(stdout, page) : null
}

/** Readings are numbered so a slow one landing late never rewinds a newer one. */
let readings = 0
let shownReading = 0

/** Reads the page again; a reset since the read began, or a newer reading already shown, wins. */
const refresh = async ($: EngineInterface): Promise<Checklist | null> => {
  const began = epoch
  const reading = ++readings
  const list = await readChecklist($, sessionList).catch(() => null)
  if (began !== epoch || reading < shownReading) return null
  shownReading = reading
  await update($, tracking, t => ({ list, isUntracked: list === null && t.isUntracked }))
  return list
}

/**
 * The checklist page a `bermuda check` call worked on. Other agents write
 * pages too, so a page counts when the call names it, in full or by the part
 * bermuda resolves (`show shown-task` for `branch-abc shown-task`), or its
 * output does (`new` prints its path, `add` its name); unnamed, when it is the
 * only page written during the call.
 */
const workedOn = async ($: EngineInterface, since: number, calls: readonly CheckCall[], output: string): Promise<string | null> => {
  const home = await $.env.get('HOME')
  if (!home) return null
  const pages = (await $.fs.list(`${home}/.bermuda/memory/checklists`).catch(() => []))
    .filter(page => page.kind === 'file' && page.name.endsWith('.md'))
    .sort((a, b) => b.mtimeMs - a.mtimeMs)
  const name = (page: { name: string }) => page.name.slice(0, -'.md'.length)
  const words = calls.flatMap(call => call.args).filter(word => !/^\d+$/.test(word) && word.length >= 3)
  // A fragment names a page only when it matches one, as bermuda itself refuses two.
  const byFragment = words
    .map(word => pages.map(name).filter(page => page.includes(word)))
    .find(matches => matches.length === 1)?.[0]
  const named = pages.map(name).find(page => output.includes(page)) ?? byFragment
  if (named !== undefined) return named
  const written = pages.filter(page => page.mtimeMs >= since)
  return written.length === 1 ? name(written[0]!) : null
}

/**
 * Pays for items this call's ticks named (by number or text prefix) that went
 * from open to ticked on the page it worked on, each once ever. A tick another
 * agent made in the same moment is seen, not paid.
 */
const rewardTicks = async (
  $: EngineInterface,
  before: Checklist | null,
  after: Checklist | null,
  ticks: readonly CheckCall[],
) => {
  // Only the same page, by name: a first look at a page is a baseline, never a payday.
  if (!before || !after || before.name === null || before.name !== after.name) return
  // bermuda reads a tick by arity: `tick 3` names an item, `tick <page> 3` a page then an item.
  const targets = ticks.flatMap(call => call.args.slice(-1))
  const numbers = new Set(targets.filter(arg => /^\d+$/.test(arg)).map(Number))
  const prefixes = targets.filter(arg => !/^\d+$/.test(arg))
  const isNamed = (item: OpenItem) => numbers.has(item.n) || prefixes.some(prefix => item.text.startsWith(prefix))
  const key = (item: OpenItem) => `${after.name}\u0000${item.n}\u0000${item.text}`
  const was = new Set(before.ticked.map(key))
  const fresh = after.ticked.filter(isNamed).map(key).filter(k => !was.has(k))
  if (fresh.length === 0) return
  const stored = await $.store.get(REWARDED_KEY)
  const paid = Array.isArray(stored) ? stored.filter((k): k is string => typeof k === 'string') : []
  const unpaid = fresh.filter(k => !paid.includes(k))
  if (unpaid.length === 0) return
  await $.store.set(REWARDED_KEY, [...paid, ...unpaid].slice(-REWARDED_MAX))
  await eat($, { xp: TICK_FOOD.xp * unpaid.length, fullness: TICK_FOOD.fullness * unpaid.length })
}

/** After the agent's own `bermuda check` call: follow the page it worked on and pay for its ticks. */
const afterChecklistCall = async ($: EngineInterface, startedAt: number, calls: readonly CheckCall[], output: string) => {
  const began = epoch
  const before = (await read($, tracking)).list
  const page = await workedOn($, startedAt, calls, output)
  if (began !== epoch) return
  if (page !== null) sessionList = page
  const after = await refresh($)
  if (began !== epoch) return
  const ticks = calls.filter(call => call.verb === 'tick')
  if (ticks.length > 0) await rewardTicks($, before, after, ticks)
}

/** After a tool ran: follow the checklist, and say what the agent should hear. */
const follow = async (
  $: EngineInterface,
  call: { tool: string; command: string | null; output: string; isError: boolean; isReadOnly?: true },
  startedAt: number,
): Promise<string | null> => {
  const calls = call.command === null ? [] : checklistCalls(call.command)
  if (calls.length > 0) {
    // A refused call (an ambiguous name, a bad number) changed nothing to follow.
    if (call.isError) return null
    changesSinceCheck = 0
    // A listing names every page on the board: none of them is this session's.
    const worked = calls.filter(c => !LISTING_VERBS.has(c.verb))
    // Off the result's path: the agent gets its tool result at once.
    if (worked.length > 0) inBackground(afterChecklistCall($, startedAt, worked, call.output))
    return null
  }
  if (!isMutating(call.tool, call.isReadOnly)) return null
  changesSinceCheck += 1
  const { list } = await read($, tracking)
  if (list === null) {
    if (!isEdit(call.tool)) return null
    await update($, tracking, t => ({ ...t, isUntracked: true }))
    if (remindedTurn === currentTurn) return null
    remindedTurn = currentTurn
    return noChecklistReminder()
  }
  if (list.open.length > 0 && changesSinceCheck >= NUDGE_EVERY) {
    changesSinceCheck = 0
    return staleReminder(list, NUDGE_EVERY)
  }
  return null
}

const setLimits = ($: EngineInterface, next: readonly Limit[]) =>
  update($, limits, () => next.map(({ kind, percentUsed, resetsAt }) => ({ kind, percentUsed, resetsAt })))

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    const result = await next(e)
    const now = await $.clock.now()
    const loaded = revive(await $.store.get(STORE_KEY), now)
    await update($, pet, () => loaded)
    persist($)
    await update($, busy, () => 0)
    await setLimits($, (await $.session.usage()).rateLimits)
    await resetTracking($)
    await refresh($).catch(() => null)

    // The timer does not wait for its callback, so a slow tick makes the next
    // one skip instead of stacking writes behind it.
    let isTicking = false
    $.clock.every(TICK_MS, () => {
      if (isTicking) return
      isTicking = true
      void (async () => {
        const tick = await update($, frame, f => f + 1)
        if ((await read($, busy)) > 0) {
          if (tick % SNACK_TICKS === 0) await pickSnack($)
          return
        }
        const current = await read($, pet)
        const stage = STAGES[stageIndex(current?.xp ?? 0)]!
        await update($, pos, p => wander(p, TRACK - width(stage.idle[0][0]), Math.random()))
      })()
        .catch(() => undefined)
        .finally(() => {
          isTicking = false
        })
    })

    return result
  })

  // Exit, /clear and reload: land the last feeds before the session goes,
  // inside the short bound session.end shares.
  on('session.end', async ($, e, next) => {
    // /clear starts a new conversation with no session.start: it gets a clean slate.
    if (e.reason === 'clear') await resetTracking($).catch(() => undefined)
    const room = Math.max(0, next.budget.remainingMs - 250)
    await Promise.race([flush(), $.clock.sleep(room, { signal: next.signal })]).catch(() => undefined)
    return next(e)
  })

  on('session.measure', async ($, e, next) => {
    if (e.changed.includes('rateLimits')) await setLimits($, e.rateLimits)
    return next(e)
  })

  // Every prompt carries the open items, so the agent never works from memory of them.
  // A fresh reading takes about 70 ms; a slow or hung bermuda holds a prompt
  // REFRESH_WAIT_MS at most, then the last reading stands and the refresh lands later.
  on('prompt.submit', async ($, e, next) => {
    const reading = refresh($)
    inBackground(reading.then(() => undefined))
    const fresh = await Promise.race([reading, $.clock.sleep(REFRESH_WAIT_MS).then(() => undefined)]).catch(() => undefined)
    const list = fresh === undefined ? (await read($, tracking)).list : fresh
    if (!list || list.open.length === 0) return next(e)
    return next({ ...e, context: [...(e.context ?? []), openItemsReminder(list)] })
  })

  on('turn.start', async ($, e, next) => {
    currentTurn = e.turnId
    await update($, busy, n => n + 1)
    await pickSnack($)
    return next(e)
  })

  // turn.start carries no agentId, so only the main thread's turns count as busy.
  on('turn.complete', async ($, e, next) => {
    if (e.agentId !== undefined) return next(e)
    await update($, busy, n => Math.max(0, n - 1))
    // Fed in the background so the turn's end never waits on the pet.
    if (e.reason === 'answer') inBackground(eat($, FEED.turn))
    return next(e)
  })

  // Feeding never gates or delays a tool: eat in the background once it ran.
  // The main agent's calls also keep the checklist in step, and a reminder
  // rides on the result where the agent reads it.
  on('tool.call', async ($, e, next) => {
    const startedAt = await $.clock.now()
    const ran = await next(e)
    if (ran.deny !== undefined) return ran
    inBackground(eat($, FEED.tool))
    if (e.agentId !== undefined) return ran
    const command = e.tool === 'Bash' ? e.command : null
    const call = { tool: e.tool, command, output: ran.text ?? '', isError: ran.isError === true, isReadOnly: ran.isReadOnly }
    const note = await follow($, call, startedAt).catch(() => null)
    return note === null ? ran : { ...ran, context: [...(ran.context ?? []), note] }
  }).catch((_$, e, next) => next(e))

  // The right end of the prompt footer: the terminal's lower-right corner.
  // Columns two rows tall: the checklist, the usage bars, then the pet.
  on('ui.render', { component: 'SessionMode' }, async ($, e, next) => {
    const current = await read($, pet)
    if (!current) return next(e)

    const [tick, at, working, windows, food, { list, isUntracked }] = await Promise.all([
      read($, frame),
      read($, pos),
      read($, busy),
      read($, limits),
      read($, snack),
      read($, tracking),
    ])
    const { Box, Text } = $.ui.resolve(e)
    const meters = [meter(windows, 'five_hour', '5h'), meter(windows, 'seven_day', 'wk')]
    const rows = petRows(current, at, tick, working > 0, food)

    return (
      <Box alignItems="flex-end">
        {e.props.modes.length > 0 ? <Text dimColor>{e.props.modes.join(' & ')}  </Text> : null}
        {list !== null ? (
          <Box flexDirection="column" marginRight={1}>
            <Text color={list.open.length === 0 ? 'success' : 'warning'}>
              ☑ {list.done}/{list.total}
            </Text>
            <Text dimColor>{list.blocked > 0 ? `⚑ ${list.blocked}` : ' '}</Text>
          </Box>
        ) : isUntracked ? (
          <Box flexDirection="column" marginRight={1}>
            <Text color="warning">☐ no list</Text>
            <Text> </Text>
          </Box>
        ) : (
          <Box flexDirection="column" marginRight={1}>
            <Text dimColor>☐ --</Text>
            <Text> </Text>
          </Box>
        )}
        <Box flexDirection="column" marginRight={1}>
          {meters.map(m => (
            <Box key={m.label}>
              <Text dimColor>{m.label} </Text>
              <Text color={meterColor(m.remaining)}>{m.bar}</Text>
              <Text dimColor>{(m.remaining === null ? '--' : `${m.remaining}%`).padStart(5)}</Text>
            </Box>
          ))}
        </Box>
        <Box flexDirection="column">
          {rows.map((row, i) => (
            <Box key={`row${i}`}>
              <Text>{' '.repeat(row.pad)}</Text>
              <Text color="warning">{row.food}</Text>
              <Text color="claude">{row.body}</Text>
            </Box>
          ))}
        </Box>
      </Box>
    )
  })
}
