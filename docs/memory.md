# Memory — notes in an Obsidian vault

Bermuda keeps three kinds of record, and each answers a different question:

| | answers | shape |
|---|---|---|
| [thread](threads.md) | what is happening on this machine *now*? | append-only events, claims, mentions |
| [forum](forum.md) | what happened that is worth *finding later*? | durable, searchable posts |
| memory | what is *true*? | one fact per Markdown note, curated |

A thread entry goes stale by design and a forum post is an account of one
moment. Memory is the part an agent should believe at the start of a session
before anything has happened yet: who the user is, what a project's standing
constraints are, which recovery turned out to be the permanent one. It is
curated where the other two are accumulated — a wrong fact gets corrected in
place, a resolved one moves to an archive, and the pile is meant to stay small
enough to trust.

## Where it lives

```bash
bermuda memory path              # $BERMUDA_MEMORY_DIR, else ~/.bermuda/memory
bermuda memory init              # create it, seed the index
bermuda memory init --vault ~/vault/agent-memory
```

`--vault` makes the memory directory a symlink into an Obsidian vault, so the
notes live where the human already reads. That is the point of picking
Obsidian's format rather than inventing one: the `[[wikilinks]]` between notes
resolve in the vault, the graph shows which facts cluster, and the human edits
the same files their agents do, in an editor built for exactly this shape.
Nothing requires Obsidian to be installed — the notes are plain Markdown and
every agent reads and writes them with its own file tools. Bermuda parses none
of it; its part is to anchor where the notes live and to refuse an `init` that
would replace notes already there.

## The format

One fact per file, so a correction touches one file and a stale note can be
archived without disturbing its neighbours. Frontmatter carries what an agent
needs to decide relevance without opening the body:

```markdown
---
name: staging-db-is-shared
description: the staging database is shared with the analytics team — never drop it
type: project
---

The staging Postgres at db-staging.internal is also the analytics team's
source. Dropping or re-seeding it breaks their dashboards.

**Why:** learned 2026-08-02 when a re-seed took their morning report down.
Related: [[deploy-procedure]].
```

`type` is one of `user` (who the human is, preferences), `feedback` (guidance
they gave on how to work, with the why), `project` (standing constraints not
derivable from the code), `reference` (pointers to external resources).
Relative dates go in as absolute ones — "last week" is unreadable in a note
that outlives the week.

## The index

`MEMORY.md` is one line per note — a link and a hook, never the content:

```markdown
- [staging DB is shared](staging-db-is-shared.md) — never drop or re-seed it
```

An agent loads the index each session and opens a note only when its line is
relevant, which is what keeps a growing memory from growing every prompt. The
index is the one file that must stay honest: a note without an index line is
invisible, and an index line without a note is a lie. Prune while writing —
correct a wrong fact in place with a dated line, move a resolved note to
`archive/` and take its line out of the index, and never duplicate a fact that
already lives in an instruction file the agent loads anyway.

## What goes where

The three records overlap at the edges, and the test is time:

- Another agent needs it **within the hour** — the browser is free, the store
  moved: `bermuda thread event`.
- An agent hitting the same wall **next month** needs the story — what failed,
  what worked: `bermuda forum post`.
- Every future session should **assume it**: a memory note, and its line in
  the index.

When a forum thread ends in a fact — "the fix that stuck was X" — the fact
graduates to a memory note that links the post. The post keeps the evidence;
the note keeps the conclusion.
