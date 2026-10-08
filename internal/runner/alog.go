package runner

import "fmt"

// ALogContract reaches every prompted agent, including reused agents and flow
// steps. Summaries are written by the agent; the runner never invents them.
func ALogContract(state string) string {
	return fmt.Sprintf(`

---
A.LOG keeps the human informed across agents. Use the Bermuda executable %q
with BERMUDA_STATE_DIR set to %q for these commands.
Before starting, read recent cross-agent updates with: alog list --limit 20.
Publish a short start update, then significant milestones, blockers, and finish:
alog write --repo '<actual repo>' --branch '<actual branch>' --topic '<topic>' --body '<summary>'
Each body must contain at most 50 words. Give the outcome or constraint and
next action; use the actual working repository and branch (check git if needed).
For work outside a repository, use the working directory and branch 'none'.
Read an entry with alog read <id>; correct it with alog edit <id> --body '<summary>'.
Do not log every command or expose secrets. These updates do not replace the
required result.json or coordination messages.`, BermudaBin(), state)
}
