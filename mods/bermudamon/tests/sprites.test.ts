import { expect, test } from 'claude-code/testing'

import { STAGES, TRACK, pack, width } from '../hooks/pet'

test('pack turns pixels into quadrant blocks, two pixels per cell', async () => {
  expect(pack(['##..', '##..', '....', '....'])).toEqual(['█ ', '  '])
  expect(pack(['#.#.', '.#.#', '####', '....'])).toEqual(['▚▚', '▀▀'])
  expect(pack(['....', '....', '#..#', '####'])).toEqual(['  ', '▙▟'])
})

test('every stage is square: a pixel is twice as tall as wide, so width <= 1.5 x (rows x 2)', async () => {
  for (const s of STAGES) {
    const ratio = s.body[0].length / (s.body.length * 2)
    expect(ratio).toBeGreaterThanOrEqual(0.7)
    expect(ratio).toBeLessThanOrEqual(1.5)
  }
})

test('every stage is mirror symmetric, so it faces front', async () => {
  for (const s of STAGES)
    for (const row of s.body) expect([...row].reverse().join('')).toBe(row)
})

test('stages never shrink, stay inside the track, and each looks different', async () => {
  const widths = STAGES.map(s => width(s.idle[0][0]))
  expect([...widths].sort((a, b) => a - b)).toEqual(widths)
  expect(new Set(STAGES.map(s => s.body.join('/'))).size).toBe(STAGES.length)
  for (const s of STAGES)
    for (const f of [...s.idle, ...s.munch]) {
      expect(f.length).toBe(2)
      expect(width(f[0])).toBe(width(f[1]))
      expect(width(f[0]) + 2).toBeLessThanOrEqual(TRACK)
    }
})

test('dot eyes, two little feet, tiny hands by its sides', async () => {
  for (const s of STAGES.slice(1)) {
    const [, eyes, trunk, feet] = s.body
    // Two single-pixel eyes, and two single-pixel feet.
    expect(eyes.match(/#\.#/g)).toHaveLength(2)
    expect(feet.match(/#/g)).toHaveLength(2)
    // Hands: one pixel out past the trunk on each side, low by default.
    expect(trunk.startsWith('##') && trunk.endsWith('##')).toBe(true)
    expect(eyes[0]).toBe('.')
    expect(eyes.at(-1)).toBe('.')
  }
})

test('the hands wave while walking and come to the mouth while eating', async () => {
  for (const s of STAGES.slice(1)) {
    // Walking: the hands swap between the eye row and the trunk row.
    expect(s.idle[0][0]).not.toBe(s.idle[1][0])
    expect(s.idle[0][1]).not.toBe(s.idle[1][1])
    // Eating: the first frame has an open mouth, the second is the resting body.
    expect(s.munch[0][1]).not.toBe(s.idle[0][1])
    expect(s.munch[1]).toEqual(s.idle[0])
  }
  // An egg cannot wave or eat, only wobble.
  expect(STAGES[0]!.idle[0]).not.toEqual(STAGES[0]!.idle[1])
  expect(STAGES[0]!.munch).toEqual(STAGES[0]!.idle)
})
