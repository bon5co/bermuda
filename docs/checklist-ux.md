# CHK.L controls and verification

CHK.L answers: what work remains on this branch? The approved layout has two
panes: checklist folders, then items. No preview. It reuses board typography,
selected-row marker and tabs rather than introducing a new visual system.

| Control | User story / outcome | UX gate | Verification |
|---|---|---|---|
| CHK.L tab, Tab, Shift+Tab, `2` | Open branch work beside A.LOG | C1–C3, C9; Nielsen visible state, Jakob consistency | Reach from every tab; order A.LOG, CHK.L, IMPROVE |
| Folder row / ↑↓ / j k | Choose a branch and see its items | C2, C9; Norman feedback | Mouse or keys update right pane; repo distinguishes branch identity |
| Pane focus / ←→ / h l | Move selection between folders and items | C2, C9; Nielsen user control | Focus marker moves; selection remains on return |
| Item row / ↑↓ / j k | Select the exact work to update | C2, C9; Nielsen recognition | Display sort preserves original CLI numbering |
| Space in items | Mark work done or reopen it | C2, C8, C9; Cooper no confirmation for reversible actions | Persist checkbox and item time; same item selected after reorder |
| Space in folders | Navigate safely without changing work | C2 scope; Nielsen error prevention | No write occurs |
| `/`, Enter, Esc | Find work and clear the filter | C2, S2, S7; Nielsen recovery | Match branch/repo/title/item; Esc returns full list |
| `[ ]`, Page Up/Down, Home/End, wheel | Browse long lists | S10; Tidwell scaling | Independent scrolling; End from nonzero selection reaches last row |
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
   exactly two panes, branch/repo folder rows and item checkboxes/times.
3. Space in folders must do nothing. Move right, toggle and reopen an item;
   inspect the Markdown page and CLI `check show`. The original number must
   match the item despite the display reorder. Non-raw show hides metadata.
4. Search an item, branch and repo; clear with Esc. Click each pane and exercise
   arrows/hjkl, paging, Home/End and wheel. Test more rows than the window.
5. Update another checklist from CLI and wait for refresh. Folder order changes
   but selected folder, item, focus and both scroll windows remain stable.
6. Insert a distinct item above the selected item in Markdown and refresh.
   Selection follows the original text. Delete it, or change duplicate item
   ordering: a visible stale-selection message requires reselection before Space.
7. Save Markdown after rendering but before Space. The revision guard refuses
   the stale action. Reload and deliberately select the item before retrying.
8. Check a missing/empty directory, empty checklist, legacy page and unreadable
   file. Confirm useful commands/errors and readable entries remain visible.
9. Resize to narrow and short terminals. Confirm no line exceeds terminal width,
   tabs/help stay usable, and the two panes retain their own selections.
10. Try job actions (`n`, `e`, `R`, `D`, `p`) while in CHK.L. They must not create,
    run or delete jobs. Verify number keys through `9` still reach every tab.

Automated regressions cover identity reuse/repo separation, untouched legacy
bytes, per-item timestamps, revision refusal, original numbering, navigation,
search, independent windows, stale/duplicate selection and mouse focus. The
integrated pass uses the real terminal binary with isolated state.
