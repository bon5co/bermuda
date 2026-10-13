# Bermudamon

An inverted-triangle monster that lives in the Claude Code prompt footer. It is
a mod for Claude Code, shipped with [Bermuda](../../README.md).

- **Grows as you work.** Every tool call and finished answer feeds it; it
  evolves through seven stages (Egg to Mega) and hungers over time.
- **Shows your limits.** The 5-hour and weekly windows as two bars beside it.
- **Keeps your checklist in view.** Follows the `bermuda check` page the
  session works against, shows `done/total`, reminds the agent of open items
  on each prompt, and pays the pet for every item ticked (once per item, ever).
  Without a tracked page the slot reads `☐ --`; without the `bermuda` command on `PATH` it stays that way and
  the pet and bars still work.

## Install

```
/plugin marketplace add bon5co/bermuda
/plugin install bermudamon@bermuda
```

## Develop

```
claude plugin validate mods/bermudamon
claude plugin test mods/bermudamon
```

The sprites are pixel grids in `hooks/pet.ts`; `pack` turns them into the
quadrant blocks the footer draws. The tests hold every stage to the shape: top
row widest, narrowing to a point, mirror symmetric.
