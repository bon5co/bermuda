# VHS drives a real terminal and photographs it. Five stills: the four tabs
# somebody opening the board meets, and a flow run opened up into its steps.
Output board.gif

Set Shell bash
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 15
Set Width 1400
Set Height 520
Set Padding 20
Set Theme "Catppuccin Mocha"

Hide
Type "bermuda board" Enter
Sleep 3s
Show

# Jobs — what is scheduled, on which model, and what it last did.
Type "2"
Sleep 2s
Screenshot board-jobs.png

# Runs — what happened, including the one that failed and parked there.
Type "3"
Sleep 2s
Screenshot board-runs.png

# A two-step run opened up: a flow is one row, its steps sit underneath it.
Type "jjj"
Sleep 1s
Type " "
Sleep 2s
Screenshot board-steps.png
Type " "
Sleep 1s

# Threads — the record agents leave for each other, and who holds what.
Type "1"
Sleep 2s
Screenshot board-threads.png

# Flows — what is callable, what each takes, and when it last ran. This is the
# launcher: enter runs one and asks for its input first.
Type "4"
Sleep 2s
Screenshot board-flows.png

Type "q"
Sleep 2s
