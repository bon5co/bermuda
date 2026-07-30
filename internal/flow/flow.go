// Package flow reads and writes flows, which are YAML files on disk.
//
// A flow is a harness that forces an agent through a sequence: declared steps,
// run in series, where a step that cannot show it finished stops the ones after
// it. It is the opposite of asking one agent to remember five stages — which it
// does four of, most of the time.
//
// Flows are files rather than rows, and that is the whole design. The two
// populations that write them are a person in an editor and an agent with a
// filesystem, and both already know how to open a file. A row in SQLite needs a
// command to read, a command to change, and a schema to learn first; a file can
// be diffed, reviewed, copied between machines, and committed next to the code
// it is about. Nothing here is a database.
package flow

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/bon5co/bermuda/v2/internal/store"
)

// Ext is the extension a flow file carries. Only one is accepted: two spellings
// would mean `triage.yml` and `triage.yaml` are both "triage", and whichever
// loses is a file somebody keeps editing with no effect.
const Ext = ".yml"

// Flow is one declared sequence.
//
// The id is not a field. It is the filename without its extension, so renaming
// the file renames the flow and there is no second place for the name to live
// and disagree — a file called triage.yml whose body says `id: cleanup` is a
// bug waiting for somebody to run the wrong thing.
type Flow struct {
	ID string `yaml:"-"`
	// Path is where it was read from, for error messages that can be acted on.
	Path string `yaml:"-"`

	// About is what this flow is for, shown in `flow list` and on the board.
	About string `yaml:"about,omitempty"`
	// Input describes what the caller has to supply. It is a description, not a
	// type: the steps are prompts, and a sentence tells a person and an agent
	// equally well what belongs here. An empty Input means the flow takes none.
	Input string `yaml:"input,omitempty"`

	// SkipPermissions runs this flow's agent steps with the permission bypass.
	//
	// It defaults to on, and that default is the whole reason flows work
	// unattended: there is nobody sitting in a step's pane, so a permission
	// prompt is a step that waits until its grace runs out and then parks. A
	// flow that does something consequential can turn it off here, and any step
	// can override either way.
	//
	// A pointer because unset and explicitly false are different answers, and
	// only a pointer can tell them apart.
	SkipPermissions *bool `yaml:"skip_permissions,omitempty"`

	// Steps run in order.
	Steps []store.Step `yaml:"steps"`
}

// BypassesPermissions reports whether this flow's agent steps run with
// --dangerously-skip-permissions, absent a per-step override.
func (f Flow) BypassesPermissions() bool {
	if f.SkipPermissions == nil {
		return true
	}
	return *f.SkipPermissions
}

// TakesInput reports whether the flow declares one.
func (f Flow) TakesInput() bool { return strings.TrimSpace(f.Input) != "" }

// Dir is where flows live under a state directory.
func Dir(stateDir string) string { return filepath.Join(stateDir, "flows") }

// idPattern is the same shape as a job id and a thread id: lowercase, digits,
// single hyphens. It is a filename and a run-directory name, so anything else
// would either be illegal on some filesystem or invisible in a listing.
var idPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ParseID validates a flow id.
func ParseID(s string) (string, error) {
	id := strings.TrimSpace(s)
	if id == "" {
		return "", errors.New("a flow needs an id")
	}
	if !idPattern.MatchString(id) {
		return "", fmt.Errorf("%q is not a flow id: lowercase letters, digits and single "+
			"hyphens, like a job id (e.g. triage, release-check)", s)
	}
	return id, nil
}

// Load reads one flow.
func Load(dir, id string) (Flow, error) {
	parsed, err := ParseID(id)
	if err != nil {
		return Flow{}, err
	}
	path := filepath.Join(dir, parsed+Ext)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Flow{}, fmt.Errorf("no flow %s: `bermuda flow list` says which there are", parsed)
	}
	if err != nil {
		return Flow{}, err
	}
	return decode(data, parsed, path)
}

// decode parses and validates one flow file.
//
// Unknown fields are refused rather than ignored. A typo in a hand-written file
// is the ordinary case here — these are edited by people and by agents, neither
// of which gets a schema in front of them — and a silently dropped `agnet:` key
// is a step that never runs, in a flow that reports success.
func decode(data []byte, id, path string) (Flow, error) {
	var f Flow
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return Flow{}, fmt.Errorf("%s: %w", path, err)
	}
	f.ID, f.Path = id, path
	if len(f.Steps) == 0 {
		return Flow{}, fmt.Errorf("%s: a flow needs at least one step", path)
	}
	// The same rules a job's steps were held to, so a flow cannot express
	// something the runner would refuse halfway through.
	if err := store.ValidateSteps(f.Steps, ""); err != nil {
		return Flow{}, fmt.Errorf("%s: %w", path, err)
	}
	// Placeholders are checked here, at read time, so a typo costs nothing. The
	// same check at run time has already spent an agent turn producing a prompt
	// with a literal {{inpt}} in it, which the agent will then try to interpret.
	if err := CheckPlaceholders(f); err != nil {
		return Flow{}, err
	}
	return f, nil
}

// List reads every flow in the directory, by id.
//
// A file that does not parse is returned as an error alongside the ones that
// do, rather than failing the listing: one broken flow must not make the other
// nine unlistable, and the broken one is exactly what the reader needs told.
func List(dir string) ([]Flow, []error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []error{err}
	}
	var out []Flow
	var bad []error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), Ext) {
			continue
		}
		id := strings.TrimSuffix(e.Name(), Ext)
		f, err := Load(dir, id)
		if err != nil {
			bad = append(bad, err)
			continue
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, bad
}

// Save writes a flow, refusing to overwrite one that exists.
//
// Overwriting silently is how a `flow new` with a name somebody else already
// used replaces a working sequence with an empty template, and the first anyone
// hears of it is a run that does nothing.
func Save(dir string, f Flow) error {
	if _, err := ParseID(f.ID); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, f.ID+Ext)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("flow %s already exists at %s", f.ID, path)
	}
	data, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Remove deletes a flow file.
func Remove(dir, id string) error {
	parsed, err := ParseID(id)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, parsed+Ext)
	if err := os.Remove(path); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no flow %s", parsed)
	} else if err != nil {
		return err
	}
	return nil
}
