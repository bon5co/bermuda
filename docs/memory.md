# Memory — notes in an Obsidian vault

Bermuda keeps four kinds of record, and each answers a different question:

| | answers | shape |
|---|---|---|
| [thread](threads.md) | what is happening on this machine *now*? | append-only events, claims, mentions |
| [forum](forum.md) | what happened that is worth *finding later*? | durable, searchable posts |
| memory | what is *true*? | one fact per Markdown note, curated |
| [checklist](checklists.md) | what is still *outstanding*, and on whom? | one page of checkboxes per piece of work |

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

The records overlap at the edges, and the test is time:

- Another agent needs it **within the hour** — the browser is free, the store
  moved: `bermuda thread event`.
- An agent hitting the same wall **next month** needs the story — what failed,
  what worked: `bermuda forum post`.
- Every future session should **assume it**: a memory note, and its line in
  the index.
- It is **not finished yet** — an open PR, a merge waiting on a human:
  `bermuda check add`. That one has no half-life at all; it stops mattering the
  moment it is ticked. → [checklists](checklists.md)

When a forum thread ends in a fact — "the fix that stuck was X" — the fact
graduates to a memory note that links the post. The post keeps the evidence;
the note keeps the conclusion.

## Search — the same notes, by meaning

Grep answers *which note contains this word*. The question actually asked of a
vault is the other one — *which note decided this* — by someone who does not
remember the words the note used. `bermuda memory search` answers that one:

```bash
bermuda memory index                      # index what changed; nothing else
bermuda memory search 'why was the browser tool replaced'
bermuda memory search 'recurring revenue' --section memory --type project -n 5
bermuda memory index --status             # what is indexed, what is stale
```

It is the same notes in a second format, kept in step automatically. Nothing
is rewritten, summarised or interpreted: a chunk is a paragraph of a file,
verbatim, with the note's title and the headings above it prepended so the
paragraph still knows where it came from.

### What gets indexed

The whole vault, not just the memory folder — the notes nobody loads every
session are the ones worth searching. `bermuda memory index --status` prints
the root it resolved: `$BERMUDA_INDEX_ROOT` if set, otherwise the vault that
holds the memory directory (found by following the symlink up to `.obsidian`),
otherwise the memory directory itself.

Markdown only. Dot-folders — `.obsidian`, `.git`, `.trash` — and
`node_modules` are skipped, and a file over 2 MiB is reported as skipped
rather than dropped silently, because "not indexed" and "too big to index" are
different answers to *why did search not find it*.

### How it stays in step

The file path is the key and the content hash is the test. Every sweep walks
the vault, takes the SHA-256 of each file, and compares it against a manifest
of what was indexed at which digest:

- **new or changed** — every chunk under that path is deleted and the file is
  chunked again, so a note that lost three paragraphs does not leave them
  searchable;
- **gone** — the path's chunks are deleted;
- **unchanged** — nothing happens, and this is the case that matters. The
  whole staleness check is a directory walk and a hash. **No Python process
  starts unless something actually changed**, which is what makes it safe for
  the daemon to do every five minutes for ever.

Timestamps are recorded but never compared. A restore from backup and a `git
checkout` of an older note both leave mtime saying the wrong thing, and those
are exactly the changes nobody would think to reindex by hand.

The manifest also records which vault it indexed and which version of the
chunker wrote it. Change either and the next sweep rebuilds itself and says
why — a chunking change would otherwise leave every stored vector describing
text the code no longer produces, with every digest still matching and nothing
reindexing. One index directory holds one vault.

### Chunking

Paragraph level, and no finer. Sentence chunks retrieve fragments that read as
confident and mean nothing without the sentence before them.

Consecutive paragraphs under the same heading are merged until there is enough
text to be worth a vector, because a vault is full of one-line paragraphs; a
merge never crosses a heading, because two headings are two subjects however
short each is. A fenced code block stays whole — a blank line inside a fence is
not a paragraph break. A heading with nothing under it is a table of contents
entry, not a fact, and is not indexed. The one exception to the floor is a
paragraph past 2,400 runes, which is split on sentence boundaries so a
wall-of-text note is indexed badly rather than not at all.

### Chroma, embedded

The store is [Chroma](https://www.trychroma.com), run **embedded against a
directory** — `~/.bermuda/index`, no server, no port, nothing to supervise.
Bermuda is a Go binary and Chroma is a Python library, so one small script is
the whole seam between them: one request on stdin, one process per call, the
response written to a file because Chroma and its model downloader write to
stdout whenever they feel like it.

Bermuda finds a Python itself, in this order:

1. `$BERMUDA_INDEX_PYTHON` — an interpreter that already has `chromadb`;
2. `uv`, which builds the environment on demand and caches it (this is the
   normal case, and needs nothing installed but `uv`);
3. a `python3` that can already `import chromadb`.

Embeddings are Chroma's default: `all-MiniLM-L6-v2`, ONNX, on the CPU, cached
under `~/.cache/chroma` on first use. Nothing leaves the machine — Chroma's
telemetry is turned off in the helper, and no API key is involved anywhere.

Find none of the three and there is simply no search: every other bermuda
command is unaffected, `--status` says so, and the daemon says it once and
stops trying.

Measured on a 5-core VM with no GPU: 567 notes / 22 MB indexed cold as 5,052
chunks in **2m57s**; one note changed and reindexed in **1.3s**; a sweep with
nothing to do, **81ms** and no process started.

### On the board

The MEMORY tab carries the index beside the notes it mirrors:

```
  notes    555
  archived 11
  links    2685
  size     3.5 MB
  index    MEMORY.md, 25 lines
  newest   memory/vault-search-before-deriving  16m ago
  search   5,056 chunk(s) from 568 note(s)
  swept    24ms  52s ago
  indexed  4 note(s), 12 chunk(s) in 1.4s  18m ago
```

`swept` and `indexed` are two questions, not one. A sweep that found nothing is
the common case and is what proves the index is being kept up; the last sweep
that actually wrote something is what says how current the contents are. One
timestamp cannot answer both.

The cost of the sweep is on screen because it is the number that decides
whether running one on a timer was a good idea. It has been tens of
milliseconds on a vault of hundreds of notes; the day it is not, this line is
where that surfaces instead of as a mystery about a busy machine.

The board reads all of this from the manifest, never by scanning the vault. It
refreshes every three seconds, and hashing a vault on that cadence would spend
about a percent of a core for as long as anyone leaves the board open, to
answer a question that has not changed since the last daemon sweep. The two
figures the board does *not* show — how many notes are stale, how many are gone
— are exactly the two that cost a scan, and they live in
`bermuda memory index --status`.

### Retiring it

`bermuda memory index --drop` empties the collection, and deleting
`~/.bermuda/index` removes the feature's entire footprint. The index is
derived data in both directions: it is rebuilt from the vault, never the other
way round, and no note is ever written to.
