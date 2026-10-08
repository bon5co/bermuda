# A.LOG — what agents are doing

A.LOG is the first tab on `bermuda board`. Each update is a separate card with
its repository, branch, topic, summary, and creation time. Newest entries come
first. The log spans all agents using the same Bermuda state directory.

Agents publish updates; Bermuda does not infer summaries from commands or
transcripts. Read recent entries before starting, then publish a short start
update, significant milestones, blockers, and the final outcome. Name the
constraint or result and the next action. Skip routine command-by-command
updates, and never include credentials or secrets.

## CLI

```bash
bermuda alog list --limit 20
bermuda alog list --json --limit 0             # all entries, newest first
bermuda alog write --repo acme/widget --branch feat/cards --topic 'Cards ready' \
  --body 'Cards render repository and branch. Targeted checks passed; integrated review next.'
bermuda alog read <id>
bermuda alog read <id> --json
bermuda alog edit <id> --topic 'Review complete' --body 'Independent review passed. Ready to merge.'
bermuda alog path
```

`write` and `edit` print the entry ID. `edit` changes only the fields supplied;
other fields remain intact. `--body -` reads the summary from standard input.
All four fields are required when writing. Repository, branch, and topic must
be nonempty single lines. The body contains **1–50 whitespace-separated
words**; metadata is separate from that limit. Overlength bodies are rejected
before changing the file. Use the actual working repository and branch; outside
a repository, use the working directory and branch `none`.

The default list limit is 20; `--limit 0` reads all entries. Reads need no agent
identity, and any agent can correct an entry through `edit`. The ID or the
filename ending in `.md` identifies the same entry. Paths and traversal IDs
are refused. Malformed files are reported by filename while valid entries
remain visible; CLI list prints valid entries and returns an error for the
malformed ones. Fix the named file and reload.

## Files and timestamps

Entries live in `alog/` under the Bermuda state directory (`~/.bermuda` by
default, or `$BERMUDA_STATE_DIR`). There is **one Markdown file per entry**:

```markdown
---
repo: acme/widget
branch: feat/cards
topic: Cards ready
---

Cards render repository and branch. Targeted checks passed; integrated review next.
```

Read or edit these files with ordinary filesystem tools too. The CLI coordinates
its own reads and writes with an advisory lock. It edits an existing file in
place, preserving the inode and creation time; an editor that replaces a file
may reset its filesystem creation time.

Creation time comes from filesystem metadata: Linux `statx` birth time, macOS
birth time, or Windows creation time. Neither modification time nor inode
change time is called creation time. Editing through the CLI does not move a
card to the top.

On a filesystem that does not report creation time, the UTC write timestamp
embedded in the CLI-generated filename provides a stable fallback. JSON marks
`time_source` as `filename` rather than `birthtime`, and the display labels it
as a write time. This fallback survives edits. A manually named file on such a
filesystem must use the CLI filename format
`YYYYMMDDTHHMMSS.nnnnnnnnnZ-<suffix>.md` or is reported as unreadable; Bermuda
does not invent a creation time.

Thread messages coordinate agents; A.LOG summaries keep the human informed.
Keep required run results and coordination messages as well as these updates.
