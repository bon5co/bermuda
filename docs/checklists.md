# Checklists — is that done yet

Bermuda keeps four kinds of record, and each answers a different question:

| | answers | shape |
|---|---|---|
| [thread](threads.md) | what is happening on this machine *now*? | append-only events, claims, mentions |
| [forum](forum.md) | what happened that is worth *finding later*? | durable, searchable posts |
| [memory](memory.md) | what is *true*? | one fact per Markdown note, curated |
| checklist | what is still *outstanding*, and on whom? | one page of checkboxes per repository and branch |

```bash
bermuda check new "ship 640 fix" --about 'timezone fix, webapp'
bermuda check add "open PR into dev" --ref https://github.com/acme/widget/pull/474
bermuda check add "merge release-42" --blocked-on operator --why 'auto-deploys to QA'
bermuda check tick 'open PR'
bermuda check ls
```

```
branch-a1b2c3d4e5f6a1b2c3d4e5f6 ship-640-fix   2/5 done, 2 blocked on operator
```

The **CHK.L** board tab follows **A.LOG**. It displays one tree across the full
terminal width. Folder rows read `📁 <repo> · <branch>`, with full update dates
(`YYYY-MM-DD HH:mm`) aligned at the right edge. Items appear indented directly
under their folder. Folder labels are bold cyan; the selected row has a bright
marker; completed item text and timestamps are muted. Both folders and items
sort by last update, newest first. There is no preview.

↑/↓ or `j`/`k` move the keyboard selection through folders and items. Selecting a
checklist automatically expands it and collapses the previous one. Selecting an
item keeps its parent open. Down from a folder enters its first item; down from
its last item selects the next folder. Up from the first item selects its parent;
up from a folder selects the previous collapsed folder. Only the selected
checklist is expanded. `h`/← selects the parent; `l`/→ enters the first child.

Space toggles the selected item and does nothing on folder rows. `/` searches
branches, repositories, titles and item text; Esc clears the filter. `[ ]` pages
through visible tree rows. Home/End select the first/last visible row. Mouse
clicks select a row across its full width, and the wheel moves the selection.
Selection and scrolling survive refresh and switching tabs. A manual edit that
deletes or ambiguously changes the selected item requires deliberate reselection
before toggling. Extremely narrow terminals clip labels and omit dates only when
a complete timestamp cannot fit beside the selection marker.

## The gap it fills

One session produced two PRs on one repo, two issues on another, a project-board
card move, three notes, a report, a forum post, a credential save, a job
migration, and a four-step flow run that had to be resumed. Every one of them
was recorded *somewhere*. Nothing enumerated them, and nothing said which were
finished, which were blocked, and on whom — so the session ended with four
questions whose only purpose was reconstructing state the harness already held
in pieces: *how did the scheduled fix go; why did it not run; we have one PR
here and two there, right; what is 640.*

The other three records each hold a piece of that and none can answer it:

- **A thread is append-only and time-ordered.** It records that a thing
  happened; it cannot say a thing is still outstanding. Reading state out of it
  means reading the log and mentally diffing "opened PR" against "merged PR" —
  inside a window that is 50 messages and 24 hours by default, so on a long task
  the earliest open item is the first to fall off.
- **A flow carries step state**, but only inside one run, only for steps
  declared before the run started, and the record dies with the run. Half the
  work in any real session was never a flow step: it came from the scope
  changing mid-session, and a flow cannot represent work discovered after step
  one.
- **The forum is for what should still be findable in a month.** An open PR has
  the wrong half-life.
- **Memory is for standing facts.** An open PR is not one.

## It is just a page

New checklists are one Markdown file per repository and branch, in
`checklists/` inside the memory vault (`bermuda memory path`). `check new`
automatically detects the current git branch and the repository shared by its
worktrees. Calling it again for that identity returns the existing path without
changing the title, description or items. The same branch in a different
repository gets its own checklist.

Outside git, or in detached HEAD, provide an explicit identity:

```bash
bermuda check new "ship the fix" --repo acme/widget --branch feat/status
```

Repository identity is the absolute repository path when detected automatically;
explicit `--repo` values are used as given. Keep the same value when creating
from outside git. New filenames include an identity hash and readable title
slug. Hidden Markdown comments store repository, branch and per-item update
timestamps; Obsidian still displays ordinary checkboxes. Hand-added items without
a timestamp use the checklist's creation time. Checklist folder order uses file
modification time and item timestamps.

Existing pages remain unchanged and accessible by CLI path, name, prefix or
`BERMUDA_CHECK`. CHK.L displays them as `legacy: <title>`, using file modification
time for folder order; unstamped legacy item times show `—`. There is no migration.

The body is ordinary checkboxes:

```markdown
# ship 640 fix
timezone handling in the export path, webapp

- [x] open PR into dev — https://github.com/acme/widget/pull/474
- [x] open PR into release-42 — #475
- [ ] human approving review on 474 — BLOCKED: operator (agent has admin:false, rulesets)
- [ ] merge release-42 — BLOCKED: operator (auto-deploys to QA, breaks it until 812 ships)
- [ ] client-side date picker — tracker#812
```

No new store, no schema, no migration. The page is readable and editable in
Obsidian by a human who never runs the CLI, greppable by any agent that has only
a filesystem, and it inherits the vault's sync, file history and full-text
search for free. It survives the agent, the flow run, and bermuda itself.

It lives in the vault but deliberately **not** in `MEMORY.md`: same filesystem,
different half-life. The index is what every session loads, and filling it with
work that will be finished by Thursday is how the standing facts stop being
read.

## `--blocked-on`

The recurring shape is not "the agent has not done it yet". It is **"the agent
cannot do it"**: a PR needing a human approving review on a repo where the agent
has `admin:false`, a merge held pending somebody's timing call. Those two look
identical in every other record, and a checklist that cannot tell them apart
reads as an idle agent.

```bash
bermuda check add "merge release-42" --blocked-on operator --why 'auto-deploys to QA'
```

`--blocked-on` names *who*, not what, and `--why` says what only they can do. A
`--why` with no `--blocked-on` is refused: a stop sign with nothing written on
it leaves the next agent no way to tell whether it still holds. Blocked items
are counted separately in `check ls`, which is what turns the summary line into
something a person can act on.

## The commands

```bash
bermuda check new "<title>" [--about '...'] [--repo <repo> --branch <branch>]
bermuda check add [<list>] "<item>" [--ref <url>]
bermuda check add [<list>] "<item>" --blocked-on <who> --why '...'
bermuda check add [<list>] "<item>" --done        # work finished before the list existed
bermuda check tick   [<list>] <n|prefix>
bermuda check untick [<list>] <n|prefix>
bermuda check ls [--all]                          # every list with something open
bermuda check show [<list>] [--raw]
```

`<list>` is optional, and the arity settles which is which: `check tick 3` names
an item, `check tick ship-640 3` names both. It resolves by filename prefix,
slug, or a fuzzy match on the title, and defaults to `$BERMUDA_CHECK`, then the
checklist for the current repository and branch. It refuses to guess another
branch's page if none exists, or if git identity is unavailable. To use an explicit
identity created outside git, name the page or set `BERMUDA_CHECK`. A query
matching two pages is refused with both named rather than guessed at.

An item is picked by its number from `check show` or by the start of its text.
Ambiguity is refused there too: two items starting "open PR" would otherwise
mean ticking the wrong artifact and being told it worked.

`check ls` hides pages with nothing left open, because "what is outstanding" is
the question it answers; `--all` shows them.

## Updating items

On branch checklists, a tick changes the checkbox and its hidden update timestamp
on that line. Other lines and the original item order remain unchanged. Display
sorting never renumbers the CLI items. Legacy ticks retain the historical single
byte update with no added metadata. Adding an item goes after the last existing
checkbox, so notes written under the list stay under it. CLI writes share a file
lock; board toggles also refuse a page that changed after the displayed snapshot.

The page is re-read at the moment it is written, never trusted from a copy
loaded earlier, so a human who added two lines in between does not get a
different item ticked than the one that was named.

Ticking something already ticked is not an error and not a second tick — it says
`already ticked` rather than `now ticked`, because an agent that ran the same
command an hour apart should be able to tell which it did.

## Flows tick their own items

A flow step can name the checklist item it is:

```yaml
steps:
  - id: survey
    check: survey the repo
    agent: Read {{input}} and list what changed.
  - id: build
    check: make the change
    agent: "{{previous}} — make the change."
```

```bash
bermuda flow run nightly --input '...' --check ship-640
```

Every step that names an item gets one on the page **before the first step
runs** — a page that only grows an item once its step has succeeded would show
the flow as finished at every point during it. Each item is ticked when its step
reports ok, so a step that parked leaves its item open, which is exactly what
the page should say.

`check` is legal on both kinds of step: it configures the harness's bookkeeping,
not an agent. The binding is recorded on the run, so `bermuda flow resume` ticks
the same page the first attempt did, and re-seeding finds the items already
there rather than writing a second copy of every step. `bermuda flow status`
names the page.

Inside a step, `$BERMUDA_CHECK` already points at it, so `bermuda check add 'the
thing I just found'` lands on the run's page with no id to remember — which is
the half a flow cannot declare in advance.

Failures here are reported and never fatal. The checklist is the human's view of
the run, not the run: a vault that is momentarily unwritable must not stop a
flow that was going to work.

## Deliberately out of scope

Not a task tracker. No assignment, no cross-agent ownership, no due dates, no
priorities. Issues and project boards already do that and are where humans look.

This is scratch state for one piece of work in flight, whose whole job is to
survive an agent restart and answer one question without a human having to ask
it.
