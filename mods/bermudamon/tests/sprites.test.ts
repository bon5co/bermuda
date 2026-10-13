import { expect, test } from 'claude-code/testing'

import { STAGES, TRACK, pack, width } from '../hooks/pet'

const span = (row: string): number => {
  const first = row.indexOf('#')
  return first === -1 ? 0 : row.lastIndexOf('#') - first + 1
}

test('pack turns pixels into quadrant blocks, two pixels per cell', async () => {
  expect(pack(['##..', '##..', '....', '....'])).toEqual(['█ ', '  '])
  expect(pack(['#.#.', '.#.#', '####', '....'])).toEqual(['▚▚', '▀▀'])
  expect(pack(['....', '....', '#..#', '####'])).toEqual(['  ', '▙▟'])
})

test('every stage is an inverted triangle: the top row is widest and each row below is no wider', async () => {
  for (const s of STAGES) {
    const spans = s.body.map(span)
    expect(spans[0]).toBe(s.body[0].length)
    for (let i = 1; i < spans.length; i++) expect(spans[i]!).toBeLessThanOrEqual(spans[i - 1]!)
    expect(spans[3]!).toBeLessThan(spans[0]!)
  }
})

test('every stage is mirror symmetric, so the monster faces front', async () => {
  for (const s of STAGES)
    for (const row of s.body) expect([...row].reverse().join('')).toBe(row)
})

test('stages grow wider as they evolve and every frame fits the track', async () => {
  const widths = STAGES.map(s => width(s.idle[0][0]))
  expect([...widths].sort((a, b) => a - b)).toEqual(widths)
  expect(new Set(widths).size).toBe(widths.length)
  for (const s of STAGES)
    for (const f of [...s.idle, ...s.munch]) {
      expect(f.length).toBe(2)
      expect(width(f[0])).toBe(width(f[1]))
      expect(width(f[0]) + 2).toBeLessThanOrEqual(TRACK)
    }
})

test('one eye, open in every frame: the tip and the mouth never touch the eye rows', async () => {
  for (const s of STAGES.slice(1)) {
    // A single centered hole in the second row: one gap, between lit pixels.
    expect(s.body[1].match(/#\.+#/g)).toHaveLength(1)
    for (const f of [...s.idle, ...s.munch]) expect(f[0]).toBe(pack(s.body)[0])
  }
})

test('walking wiggles the tip, munching opens the mouth', async () => {
  for (const s of STAGES) {
    expect(s.idle[0]).not.toEqual(s.idle[1])
    expect(s.idle[0][0]).toBe(s.idle[1][0])
  }
  for (const s of STAGES.slice(1)) expect(s.munch[0][1]).not.toBe(s.munch[1][1])
  // An egg has no mouth.
  expect(STAGES[0]!.munch).toEqual(STAGES[0]!.idle)
})
