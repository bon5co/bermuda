package main

import "github.com/bon5co/bermuda/v3/internal/improve"

const improveUsage = `usage: bermuda improve <list|read|write|edit|path>
  list [--json] [--limit 20]        Newest entries first (0 means all)
  read <id> [--json]               Read one entry
  write --kind <discovery|mistake|recovery> --repo <repo> --branch <branch> --topic <topic> --body <text|->
  edit <id> [--kind ...] [--repo ...] [--branch ...] [--topic ...] [--body <text|->]
  path                            Directory containing the Markdown files
Bodies have no word or byte cap. --body - reads stdin. Kind is required when writing.`

var learningCLI = cardFeedCLI{
	Name: "improve", Label: "IMPROVE", Usage: improveUsage, HasKind: true,
	BodyHelp: "learning note, unlimited length; - reads stdin",
	Dir:      improve.Dir, List: improve.List, Read: improve.Read, Write: improve.Write, Update: improve.Update,
}

func improveCmd(argv []string) error { return cardFeedCmd(learningCLI, argv) }
