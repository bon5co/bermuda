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

test('every stage narrows from its top row to its chin, and the top is the widest', async () => {
  for (const s of STAGES) {
    const spans = s.body.map(span)
    expect(spans[0]).toBe(Math.max(...spans))
    for (let i = 1; i < spans.length; i++) expect(spans[i]!).toBeLessThanOrEqual(spans[i - 1]!)
    expect(spans[3]!).toBeLessThan(spans[0]!)
  }
})

test('the top, the eye row and the chin are mirror symmetric, so it faces front', async () => {
  for (const s of STAGES)
    for (const row of [s.body[0], s.body[2], s.body[3]]) expect([...row].reverse().join('')).toBe(row)
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

test('two big eyes, each a 2x2 hole with a lit sparkle, open in every frame', async () => {
  for (const s of STAGES.slice(1)) {
    const [, sparkled, hollow] = s.body
    const holes = [...hollow.matchAll(/#(\.\.)#/g)]
    expect(holes).toHaveLength(2)
    for (const hole of holes) {
      const x = hole.index! + 1
      expect(sparkled.slice(x, x + 2)).toBe('.#')
    }
    // The shuffle and the mouth are on the chin; the eyes never change.
    for (const f of [...s.idle, ...s.munch]) expect(f[0]).toBe(pack(s.body)[0])
  }
  // An egg has no face.
  expect(STAGES[0]!.body[1]).not.toMatch(/#\.\.#/)
})

test('walking shuffles the chin, munching opens it', async () => {
  for (const s of STAGES) {
    expect(s.idle[0]).not.toEqual(s.idle[1])
    expect(s.idle[0][0]).toBe(s.idle[1][0])
  }
  for (const s of STAGES.slice(1)) expect(s.munch[0][1]).not.toBe(s.munch[1][1])
  // An egg has no mouth.
  expect(STAGES[0]!.munch).toEqual(STAGES[0]!.idle)
})
