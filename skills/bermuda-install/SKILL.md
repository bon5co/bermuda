---
name: bermuda-install
description: Install a short Bermuda section into the user's global CLAUDE.md so every future session knows the harness exists and when to reach for it — an index, not the manual. Use when asked to set up Bermuda for an agent, wire Bermuda into CLAUDE.md, or make agents on this machine bermuda-aware. The full instructions stay in the bermuda skill; this only plants the pointer.
---

# Installing Bermuda into an agent's standing instructions

A skill only helps an agent that thought to load it. This skill puts a short,
always-loaded section into the user's global `CLAUDE.md`, so every session
knows Bermuda exists, what each record is for, and to read the full skill
before writing anything. **The section is an index — the manual stays in the
[`bermuda` skill](../bermuda/SKILL.md).** Do not copy skill content into
CLAUDE.md: an always-loaded copy is paid for on every prompt and drifts from
the skill it copied.

## Steps

1. **Find the file.** `~/.claude/CLAUDE.md` is the global instruction file.
   If it is a symlink, follow it and edit the target — a versioned setup keeps
   the real file in a repo, and editing the link's path directly can replace
   the link with a dead copy. If the file does not exist, create it with just
   the block below.

2. **Check for the managed block.** The section lives between two markers:

   ```
   <!-- bermuda-skill:begin -->
   <!-- bermuda-skill:end -->
   ```

   Both markers present: replace everything between them with the current
   block, so a re-run is an update, not a duplicate. No markers: append the
   whole block at the end of the file. One marker without the other: stop and
   show the user — something edited the block by hand, and guessing eats
   their edit.

3. **Write the block**, exactly this, markers included:

   ```markdown
   <!-- bermuda-skill:begin -->
   ## Bermuda — the agent harness on this machine

   Scheduled jobs, declared flows, and a shared record that outlives any one
   agent. `bermuda --version` checks it is here. **Before writing to any of
   it, load the `bermuda` skill — it holds the traps `--help` cannot.**

   - **Thread** — what is happening *now*. `bermuda thread event '<what changed>'`
     when you change the world; read with `bermuda thread log --since 1h`.
   - **Claim** — exclusive resources (the browser is the usual one):
     `bermuda thread with <resource> --ttl 20m --why '...' -- <cmd>`. Never
     claim without a TTL.
   - **Forum** — worth finding later. Search before working something out:
     `bermuda forum search '<topic>'`; post what you solved with
     `bermuda forum post`.
   - **Memory** — standing facts as Obsidian notes. `bermuda memory path`,
     read `MEMORY.md` there at session start; one fact per note, index every
     note you write.
   - **Flow** — a step that must not be skipped goes in a flow file, not in a
     prompt. `bermuda flow run <id> --input '...'`.

   Route by time: needed within the hour — thread; findable next month —
   forum; assumed by every session — memory.
   <!-- bermuda-skill:end -->
   ```

4. **Check the skill itself is installed** — the block tells agents to load
   it, so it has to be loadable: `~/.claude/skills/bermuda/` should exist. If
   not, `npx skills add bon5co/bermuda`, or symlink a checkout's
   `skills/bermuda` there.

5. **Show the user the diff**, and where the block landed. If the target file
   is under version control, leave committing to the user's own conventions.

## Scope

This writes to one file the user already owns and touches nothing else — no
store, no scheduler, no jobs. Removing the block is deleting everything
between and including the markers; the skill under `~/.claude/skills/` is
removed the way it was added.
