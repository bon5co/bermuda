package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bon5co/bermuda/v2/internal/memory"
)

// The memory layer: curated facts as Markdown notes in an Obsidian vault.
//
// Threads are for now, the forum is for finding later, and both are records of
// what happened. Memory is the third kind: what is *true* — who the user is,
// what a project's constraints are, which fix turned out to be permanent. One
// fact per note, an index the agent loads each session, wikilinks between
// notes. The format is Obsidian's because a human should read and edit the
// same notes their agents do, in an editor built for exactly this shape —
// links that resolve, a graph, search. Nothing here parses the notes: agents
// read and write them with their own file tools, and Bermuda's part is only to
// anchor where they live and to wire that anchor into a real vault.
func memoryCmd(argv []string) error {
	if len(argv) == 0 {
		return errors.New("usage: bermuda memory <path|init>")
	}
	switch argv[0] {
	case "path":
		fmt.Println(memoryDir())
		return nil
	case "init":
		return memoryInit(argv[1:])
	default:
		return fmt.Errorf("unknown memory command %q", argv[0])
	}
}

// memoryDir resolves where memory notes live: $BERMUDA_MEMORY_DIR, else
// memory/ inside the state directory. The state-directory default keeps the
// whole of Bermuda findable by looking in one place; the override exists
// because a vault often already has a home.
func memoryDir() string {
	return memory.Dir(stateDir())
}

// memoryInit creates the memory directory and seeds its index.
//
// With --vault, the memory directory becomes a symlink into an existing
// Obsidian vault, so the notes live where the human already reads: the vault
// folder is created if missing, the link points at it, and everything else is
// the same. An existing directory or a link that points elsewhere is refused
// rather than replaced — memory is the one thing an init must never eat.
func memoryInit(argv []string) error {
	fs := flag.NewFlagSet("memory init", flag.ContinueOnError)
	vault := fs.String("vault", "", "folder inside an Obsidian vault to hold the notes; memory becomes a symlink to it")
	if err := fs.Parse(argv); err != nil {
		return err
	}

	dir := memoryDir()
	if *vault != "" {
		target, err := filepath.Abs(*vault)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(target, 0o755); err != nil {
			return err
		}
		switch existing, err := os.Readlink(dir); {
		case err == nil && existing == target:
			// already wired; fall through to seed the index
		case err == nil:
			return fmt.Errorf("%s already links to %s — remove it first if the vault really moved", dir, existing)
		case os.IsNotExist(err):
			if err := os.Symlink(target, dir); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s exists and is not a symlink — move its notes into %s yourself, then remove it and re-run", dir, target)
		}
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	index := filepath.Join(dir, memory.IndexName)
	if _, err := os.Stat(index); err == nil {
		fmt.Printf("memory at %s, index already present\n", dir)
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.WriteFile(index, []byte(memoryIndexSeed), 0o644); err != nil {
		return err
	}
	fmt.Printf("memory at %s, index seeded\n", dir)
	return nil
}

// The seed says what the index is for, because the first reader is an agent
// that has never seen this machine. Format details live in docs/memory.md and
// the skill, not here: a seed that restates the docs is a copy that drifts.
const memoryIndexSeed = `Index of memory notes. One line per note — a name and a hook, never the
content. Load this file each session; open a note only when its line is
relevant. One fact per note; resolved notes move to archive/.

`
