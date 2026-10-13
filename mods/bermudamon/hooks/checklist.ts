import type { Checklist, OpenItem } from '../types'

/** File-changing tool calls with no `bermuda check` call before the agent is reminded. */
export const NUDGE_EVERY = 15
/** Open items listed in a reminder; the rest are counted. */
const LISTED = 8
const ITEM_CHARS = 140

export const TICK_FOOD = { xp: 10, fullness: 5 } as const

const EDIT_TOOLS = new Set(['Edit', 'MultiEdit', 'Write', 'NotebookEdit'])

/** A call that edits files: what starts work that needs a checklist. */
export const isEdit = (tool: string): boolean => EDIT_TOOLS.has(tool)

/** A call that changes the work: an edit, or a Bash command core did not hold read-only. */
export const isMutating = (tool: string, isReadOnly: boolean | undefined): boolean =>
  isEdit(tool) || (tool === 'Bash' && isReadOnly !== true)

/** `bermuda check` verbs that print the board's pages rather than work on one. */
export const LISTING_VERBS: ReadonlySet<string> = new Set(['ls', 'list'])

export type CheckCall = { verb: string; args: string[] }

/** Shell words and the operators between commands, quotes and backslashes honoured. */
const shellWords = (command: string): string[] => {
  const words: string[] = []
  let word: string | null = null
  let quote: '"' | "'" | null = null
  const push = () => {
    if (word !== null) words.push(word)
    word = null
  }
  for (let i = 0; i < command.length; i++) {
    const ch = command[i]!
    if (quote !== null) {
      if (ch === quote) quote = null
      else if (ch === '\\' && quote === '"' && i + 1 < command.length) word = (word ?? '') + command[++i]
      else word = (word ?? '') + ch
    } else if (ch === '"' || ch === "'") {
      quote = ch
      word = word ?? ''
    } else if (ch === '\\' && i + 1 < command.length) {
      const nextCh = command[++i]!
      if (nextCh !== '\n') word = (word ?? '') + nextCh
    } else if (ch === ' ' || ch === '\t') push()
    else if (ch === '\n' || ch === ';' || ch === '|' || ch === '&') {
      push()
      words.push(';')
    } else word = (word ?? '') + ch
  }
  push()
  return words
}

/**
 * The `bermuda check` calls a shell command runs, verb and arguments. A
 * mention is not a call: only a command whose program is `bermuda` (by name or
 * path, after any leading VAR=value) followed by `check` counts.
 */
export const checklistCalls = (command: string): CheckCall[] => {
  const segments: string[][] = [[]]
  for (const word of shellWords(command)) {
    if (word === ';') segments.push([])
    else segments.at(-1)!.push(word)
  }
  return segments.flatMap(words => {
    while (words.length > 0 && /^[A-Za-z_][A-Za-z0-9_]*=/.test(words[0]!)) words.shift()
    const [program, noun, verb, ...rest] = words
    if (program === undefined || !/(^|\/)bermuda$/.test(program) || noun !== 'check' || !verb) return []
    return [{ verb, args: rest.filter(arg => !arg.startsWith('-')) }]
  })
}

export const isChecklistCommand = (command: string): boolean => checklistCalls(command).length > 0

/** A checklist page as `bermuda check show -raw` prints it. */
export const parseChecklist = (raw: string, name: string | null): Checklist | null => {
  const lines = raw.split('\n')
  const title = lines.find(l => l.startsWith('# '))?.slice(2).trim()
  if (title === undefined) return null
  let n = 0
  let done = 0
  let blocked = 0
  const open: OpenItem[] = []
  const ticked: OpenItem[] = []
  for (const line of lines) {
    const item = /^- \[([ xX])\] (.*)$/.exec(line)
    if (!item) continue
    n += 1
    const text = item[2]!.replace(/\s*<!--.*?-->\s*/g, ' ').trim()
    if (item[1] !== ' ') {
      done += 1
      ticked.push({ n, text })
    } else if (/\bBLOCKED:/.test(text)) blocked += 1
    else open.push({ n, text })
  }
  return { name, title, done, total: done + blocked + open.length, blocked, open, ticked }
}

const cut = (text: string) => (text.length > ITEM_CHARS ? `${text.slice(0, ITEM_CHARS - 1)}…` : text)

const tickHint = (list: Checklist) =>
  list.name === null
    ? 'bermuda check tick <n>'
    : `bermuda check tick "${list.name}" <n>`

export const noChecklistReminder = (): string =>
  [
    'bermudamon: files are changing but no bermuda checklist tracks this work in this session.',
    'Before going further, write what the work delivers as provable one-line user stories:',
    '`bermuda check new "<task>"`, then one `bermuda check add "<checklist>" "<story>"` per story.',
  ].join('\n')

export const openItemsReminder = (list: Checklist): string => {
  const listed = list.open.slice(0, LISTED).map(item => `${item.n}. [ ] ${cut(item.text)}`)
  const more = list.open.length > LISTED ? [`… and ${list.open.length - LISTED} more open`] : []
  return [
    `bermudamon: bermuda checklist "${list.title}" is ${list.done}/${list.total} done${list.blocked > 0 ? `, ${list.blocked} blocked` : ''}. Open:`,
    ...listed,
    ...more,
    `Tick an item the moment its yes is observed (a run, a row, an output): \`${tickHint(list)}\`. Add stories learned mid-task.`,
  ].join('\n')
}

export const staleReminder = (list: Checklist, calls: number): string =>
  [
    `bermudamon: ${calls} file-changing calls since the bermuda checklist "${list.title}" was last touched, ${list.open.length} item${list.open.length === 1 ? '' : 's'} still open.`,
    `If any is now proven, tick it: \`${tickHint(list)}\`. If the work changed shape, add or block items.`,
  ].join('\n')
