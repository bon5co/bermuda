# The threads system from the shell, which is where agents meet it.
#
# The board shows a thread; these are the commands that write to one, and the
# refusal an agent gets when it asks for a resource somebody else is holding.
Output threads.gif

Set Shell bash
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 15
Set Width 1400
Set Height 240
Set Padding 20
Set Theme "Catppuccin Mocha"

Hide
Type "clear" Enter
Sleep 500ms
Show

Type "bermuda thread log --all --limit 6" Enter
Sleep 2s
Screenshot thread-log.png
Sleep 1s

Hide
Type "clear" Enter
Sleep 500ms
Show

# Who holds what, and until when — the question a claim exists to answer.
Type "bermuda thread status" Enter
Sleep 1500ms

# And what a second agent is told when it asks for a held resource.
Type "bermuda thread claim browser --ttl 5m --why 'checking a login' --as scout" Enter
Sleep 2s
Screenshot thread-claim.png
