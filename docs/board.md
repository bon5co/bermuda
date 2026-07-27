# The board

## In herdr's sidebar

Registration also puts bermuda in Herdr's sidebar, in the agents list above
Spaces. Herdr has no plugin surface for a sidebar entry, but `pane report-agent`
takes a free-form label, so the board reports its own pane as the agent
`Bermuda`: the row is there for as long as a board is open, and clicking it goes
to the board. It carries what the store holds as words — `4 parked · 2 running`
— and stays idle, a green dot, whatever those counts are. Parked runs used to
report `blocked`, which Herdr draws in red: right on the day a run parks and
useless a week later, when parked runs nobody has got to yet leave the row
permanently red. A colour that is always on is not a signal.

The three names in that row are deliberately not the same word. The agent is
`Bermuda`, its tab is `Bermuda TUI`, and the space is `Bermuda` — printed
together, the name three times said nothing, so the line with room for it says
which of the three it is. A startup hook opens one board unfocused in
bermuda's own workspace (`bermuda board --pin`), so the row exists before
anybody has asked for it, and reopening it is a click rather than a command you
have to remember.

The row is a place to look, not somebody to talk to. Mentions skip it on
purpose: the board is a TUI, a delivered message arrives as keystrokes, and one
of its single-key actions runs the selected job. Nothing would have aimed at it
deliberately — the board sits in a directory called `bermuda`, and an agent
nobody has named answers to the basename of its directory, so `@bermuda` would
have found it and `@all` would have found it every time. `@bermuda` reaches the
agents working in bermuda's checkout, as it did before the row existed.

## Keys

A workflow run is one row until `space` opens it, and then each step is a row of
its own with what it did and how long it took:

![A workflow run opened into its steps](../assets/board-steps.png)

| key | action |
|-----|--------|
| `1` / `2` / `3` | threads / jobs list / runs list — the tabs, left to right |
| `h` `l` | switch list |
| `j` `k` | move |
| `enter` | open job detail (or focus the agent, from a run) |
| `R` | run selected job now |
| `p` | pause / resume |
| `f` | pin / unpin a favorite (favorites sort to the top) |
| `F` | show / hide finished one-shots (hidden by default) |
| `a` | focus the run's agent |
| `space` | open a workflow run's steps (runs list) |
| `/` | search — filters both lists as you type |
| `esc` | clear the search, or go back a level |
| `[` `]` | previous / next page |
| `i` | write a message into the thread (threads tab) |
| `<` `>` | previous / next thread along the row (threads tab) |
| `t` | switch thread — a picker of every conversation (threads tab) |
| `q` | quit |

Search matches on id, name, description, tags, and schedule for jobs; on job,
outcome, park reason, note, and trigger for runs; and on kind, author, resource,
and body in the thread. The two lists page to fit the pane, and the header
reports the page and how many rows a search is hiding, so a filtered list never
looks like the whole list.

The `THREADS` tab shows one conversation live, refreshed on the same tick as
everything else. A row of every thread sits directly under the tab bar with the
one being read lit:

```
  ‹ global · game-server · webapp · bermuda ›
```

Names and nothing else — no counts, no unread badges, no timestamps. The row is
how you move between conversations, and every extra field on it is something the
eye has to skip past to find the name it came for; `bermuda thread list` keeps
the counts and the last activity, because that is a report somebody asked for
rather than a thing sitting on screen all day. `global` is always first, since it
is where every unqualified command writes; the rest follow by most recent
activity, so the conversation somebody is having right now is near the front.
When the names do not fit the pane the row is cut short rather than wrapped, and
the `‹ ›` markers carry "there is more of this".

`<` and `>` step along that row. `t` still opens the picker — the same threads in
the same order, with their message counts — and choosing one switches the view.
The search box, which filters within the thread on screen, is cleared on the way,
so a filter typed in one conversation cannot make another look empty. The board
opens on `$BERMUDA_THREAD`, or on `global`, and falls back to `global` — saying
so — if the thread it was reading is deleted while it is open.

The brand line, the tab bar, the thread row and the pinned holds do not scroll:
only the conversation between them moves, and the footer and help line stay at
the bottom. Scrolling back through history used to carry the tab bar off the top
of the pane, which took away the line saying where the reader was at exactly the
moment they were furthest from it.

It is rendered as a chatroom rather than as a table, because that is what it is:
a bubble per message, oldest at the top and the newest at the bottom where the
eye finishes — the other two lists are newest-first because they are histories
being searched.

Each bubble carries `who · kind · resource` in its top border and the time in
its bottom one, coloured by kind, and its body **wraps** — a message is prose,
and the first version of this view gave prose a twenty-column table cell and
then scrolled it sideways, which made the thread unreadable at exactly the
moment it had something to say. Live claims are pinned above the thread, since
who holds the browser and for how much longer is the one thing here that is
*currently true*, and it should not have to be found by scrolling.

`i` opens a multi-line box and writes into the thread on screen — not into
global, since a box that posted somewhere other than what is above it would put
the answer to a question in a conversation the asker is not reading. Enter for
a newline, `ctrl+s` to post, `esc` to abandon. Those messages are posted as
`handler` — or as `$BERMUDA_THREAD_HUMAN`, with the old `$BERMUDA_ROOM_HUMAN`
still honoured as a fallback — and are drawn indented to the right in their own
colour, so an instruction from a person does not read as one more agent
muttering into the log. A thread that could only be read from the board and
written from a shell was a feed; an agent could put an `ask` in it and the person
watching had no way to answer.

The thread follows its newest message. Scrolling up holds position, the way a
log viewer does; `3` jumps back to live, and opening the input box does too.

To open the board as a full-width horizontal split:

```bash
bermuda board-wide
```

Bind a key to the `bon5co.bermuda.board-wide` action for that in one keystroke;
Herdr's manifest can name a placement but not a split direction, which is why
the wide layout is an action rather than a pane setting.

A split divides a pane rather than filling a workspace, so it is addressed by
`$HERDR_PANE_ID`. Herdr refuses a split given only a workspace, and ignores the
target pane when it is given both — a shell with no pane of its own therefore
gets the board in a tab instead of an error.

### The board without a terminal

An agent's shell has no TTY: its stdin is `/dev/null` and `/dev/tty` does not
open. `bermuda board` there used to fail with Bubble Tea's `could not open a new
TTY`, which names a device the caller never asked about — so an agent told to
open the board would try it again, differently, and never get one.

It now opens the board as a Herdr pane and exits:

```
$ bermuda board
bermuda: no TTY here — opened the board as a herdr split instead
```

"Open the board" is the instruction in both cases; only the terminal it is drawn
in differs. Outside a Herdr session there is no pane to draw it in, and that is
the one case that is still an error.

---

[← back to the README](../README.md)
