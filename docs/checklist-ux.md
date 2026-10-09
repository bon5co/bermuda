# CHK.L controls and verification

CHK.L answers: what work remains on this branch? The approved layout is one tree
across the full terminal width: bold cyan checklist folders with indented items.
Only the selected checklist expands. No preview. Full update dates align at the
right edge. Selection has a bright text marker; completed text and dates are
muted with a scoped ANSI 246 text style (rather than the board's darker hints).
It retains the existing board tabs and typography.

| Control | User story / outcome | UX gate | Verification |
|---|---|---|---|
| CHK.L tab, Tab, Shift+Tab, `2` | Open branch work beside A.LOG | C1–C3, C9; Nielsen visible state, Jakob consistency | Reach from every tab; order A.LOG, CHK.L, IMPROVE |
| Folder row / ↑↓ / j k | Choose a branch and see its items | C2, C9; Norman feedback | Up/down selection expands current folder and collapses previous; repo distinguishes branch identity |
| Parent/child / ←→ / h l | Select the parent or its first item | C2, C9; Nielsen user control | Marker moves to actual tree row; child keeps parent open |
| Item row / ↑↓ / j k | Select the exact work to update | C2, C9; Nielsen recognition | Display sort preserves original CLI numbering |
| Space on item | Mark work done or reopen it | C2, C8, C9; Cooper no confirmation for reversible actions | Persist checkbox and item time; same item selected after reorder |
| Space on folder | Navigate safely without changing work | C2 scope; Nielsen error prevention | No write occurs |
| `/`, Enter, Esc | Find work and clear the filter | C2, S2, S7; Nielsen recovery | Match branch/repo/title/item; Esc returns full list |
| `[ ]`, Page Up/Down, Home/End, wheel | Browse long lists | S10; Tidwell scaling | One tree window; arrows skip empty hints; End from nonzero selection reaches last visible row |
| Refresh / `r` | Read external edits without losing place | F6; Nielsen recognition | Preserve folder identity and selected item text; stale/ambiguous item cannot toggle |
| Empty state | Start a branch checklist or add its first item | S5, S7; Tidwell blank slate | Visible CLI command; legacy and new pages coexist |
| Read/write error | Understand the failure and retry | S7, W6; Nielsen error recovery | Error names file and action; readable pages remain visible |

Mobile target geometry (C5/C6), screen reader DOM roles (C12), and browser
navigation are not applicable to this terminal UI. State uses the existing text
marker and checkbox glyphs as well as styling (C9/K3). No destructive action is
introduced. Toggling is immediately reversible. All displayed file content is
stripped of terminal control sequences and fitted to terminal cell widths.

## Integrated terminal test plan

1. Start with an isolated state directory and git fixture. Create a checklist,
   add open, done and blocked items; create another branch with the same item
   names. Re-run creation and confirm the original path/items remain.
2. Open the real `bermuda board` terminal UI. Confirm A.LOG → CHK.L → IMPROVE,
   one full-width tree, repo/branch folder rows, indented item checkboxes and full
   dates at the far right at 80, 160 and 240 columns.
3. Space on folder must do nothing. Move right, toggle and reopen an item;
   inspect the Markdown page and CLI `check show`. The original number must
   match the item despite the display reorder. Non-raw show hides metadata.
4. Search an item, branch and repo; clear with Esc. Arrow down through folder,
   children and next folder: old children collapse and new children appear. Up
   selects the parent or previous folder. Click full-width rows (including the
   timestamp), then test hjkl, paging, Home/End and wheel with a long tree.
5. Update another checklist from CLI and wait for refresh. Folder order changes
   but selected folder, item, expansion and tree scroll remain stable.
6. Insert a distinct item above the selected item in Markdown and refresh.
   Selection follows the original text. Delete it, or change duplicate item
   ordering: a visible stale-selection message requires reselection before Space.
7. Save Markdown after rendering but before Space. The revision guard refuses
   the stale action. Reload and deliberately select the item before retrying.
8. Check a missing/empty directory, empty checklist, legacy page and unreadable
   file. Confirm useful commands/errors and readable entries remain visible.
9. Resize to narrow and short terminals. Confirm no line exceeds terminal width,
   tabs/help stay usable, and the tree retains selection and expansion.
10. Try job actions (`n`, `e`, `R`, `D`, `p`) while in CHK.L. They must not create,
    run or delete jobs. Verify number keys through `9` still reach every tab.

Automated regressions cover identity reuse/repo separation, untouched legacy
bytes, per-item timestamps, revision refusal, original numbering, navigation,
search, automatic expansion/collapse, full-width dates, stale/duplicate selection
and rendered-row mouse hit mappings. The
integrated pass uses the real terminal binary with isolated state.
