// Draws the prompt footer at several terminal widths from the mod's own code
// and prints it as JSON rows of coloured cells. render.py turns that into the
// README screenshot. Run: bun screenshots/footer.ts | uv run screenshots/render.py out.png
import { fit, hatch, meter, petRows, stageIndex } from '../hooks/pet'

type Cell = { t: string; c: string }
const text = (s: string, c: string): Cell[] => [...s].map(t => ({ t, c }))
const pad = (cells: Cell[], n: number): Cell[] => [...cells, ...text(' '.repeat(Math.max(0, n - cells.length)), 'dim')]

const pet = { ...hatch(0), xp: 200 }
const limits = [
  { kind: 'five_hour', percentUsed: 24 },
  { kind: 'seven_day', percentUsed: 81 },
] as never
const color = (r: number | null) => (r === null ? 'dim' : r > 50 ? 'success' : r > 20 ? 'warning' : 'error')

const footer = (columns: number): Cell[][] => {
  const show = fit(columns, 0)
  const parts: Cell[][][] = []
  if (show.list) parts.push([text('☑ 2/3', 'warning'), text(' ', 'dim')])
  if (show.meters) {
    const rows = [meter(limits, 'five_hour', '5h'), meter(limits, 'seven_day', 'wk')].map(m => [
      ...text(`${m.label} `, 'dim'),
      ...text(m.bar, color(m.remaining)),
      ...text(`${m.remaining}%`.padStart(5), 'dim'),
    ])
    parts.push(rows)
  }
  if (show.pet) parts.push(petRows(pet, { x: 6, dir: 1 }, 1, false).map(r => [...text(' '.repeat(r.pad), 'dim'), ...text(r.body, 'claude')]))
  const right: Cell[][] = [[], []]
  parts.forEach((p, i) => {
    const w = Math.max(...p.map(r => r.length))
    for (let y = 0; y < 2; y++) right[y]!.push(...pad(p[y] ?? [], w), ...(i < parts.length - 1 ? text(' ', 'dim') : []))
  })
  const left = [text('  ? for shortcuts', 'dim'), []]
  return [0, 1].map(y => [...pad(left[y]!, columns - right[y]!.length), ...right[y]!])
}

const panels = [100, 80, 60, 40].map(columns => ({ columns, rows: footer(columns) }))
console.log(JSON.stringify({ stage: stageIndex(pet.xp), panels }))
