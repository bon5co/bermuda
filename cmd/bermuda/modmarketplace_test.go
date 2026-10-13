package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The marketplace file is how Claude Code finds the bundled mod, and it only
// ever runs on someone else's machine. A source path that points nowhere, or a
// listing whose name differs from the mod's own manifest, fails the install
// there and nowhere here.
func TestMarketplaceShipsTheMod(t *testing.T) {
	var market struct {
		Name    string `json:"name"`
		Plugins []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"plugins"`
	}
	readJSON(t, "../../.claude-plugin/marketplace.json", &market)
	if market.Name == "" || len(market.Plugins) == 0 {
		t.Fatalf("marketplace lists no plugins: %+v", market)
	}
	for _, p := range market.Plugins {
		dir := filepath.Join("../..", p.Source)
		var manifest struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		readJSON(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), &manifest)
		if manifest.Name != p.Name {
			t.Errorf("marketplace lists %q but %s/plugin.json is %q", p.Name, p.Source, manifest.Name)
		}
		if manifest.Version == "" {
			t.Errorf("%s has no version", p.Source)
		}
		var hooks struct {
			Modules []string `json:"modules"`
		}
		readJSON(t, filepath.Join(dir, "hooks", "hooks.json"), &hooks)
		for _, m := range hooks.Modules {
			if _, err := os.Stat(filepath.Join(dir, "hooks", m)); err != nil {
				t.Errorf("%s names module %s: %v", p.Name, m, err)
			}
		}
	}
}

func readJSON(t *testing.T, path string, into any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}
