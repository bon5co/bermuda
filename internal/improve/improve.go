// Package improve stores detailed discoveries, mistakes, and proven recoveries
// as Markdown cards. Bodies have no word or byte cap.
package improve

import (
	"path/filepath"

	"github.com/bon5co/bermuda/v3/internal/alog"
)

const (
	Discovery = "discovery"
	Mistake   = "mistake"
	Recovery  = "recovery"
)

type Entry = alog.Entry

var learning = alog.Feed{Label: "IMPROVE", Kinds: []string{Discovery, Mistake, Recovery}}

func Dir(state string) string                         { return filepath.Join(state, "improve") }
func List(dir string) ([]Entry, error)                { return learning.List(dir) }
func Read(dir, id string) (Entry, error)              { return learning.Read(dir, id) }
func Write(dir string, entry Entry) (Entry, error)    { return learning.Write(dir, entry) }
func Edit(dir, id string, entry Entry) (Entry, error) { return learning.Edit(dir, id, entry) }
func Update(dir, id string, change func(*Entry) error) (Entry, error) {
	return learning.Update(dir, id, change)
}
