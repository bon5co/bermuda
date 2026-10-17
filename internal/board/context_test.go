package board

import (
	"github.com/bon5co/bermuda/v3/internal/store"
	"strings"
	"testing"
)

func TestContextVisibleInInspector(t *testing.T) {
	m := newTestModel(t)
	m.last["alpha"] = store.Run{ID: "run", JobID: "alpha", Outcome: "done", Context: "resumed"}
	if got := m.renderInspector(64); !strings.Contains(got, "context") || !strings.Contains(got, "resumed") {
		t.Fatalf("continuity hidden: %s", got)
	}
}

func TestContextLossPolicyCanBeEditedOnBoard(t *testing.T) {
	j := store.Job{OnContextLoss: "fresh"}
	for _, field := range jobFields() {
		if field.key != "on-context-loss" {
			continue
		}
		if err := field.set(&j, "park"); err != nil || j.OnContextLoss != "park" {
			t.Fatal("cannot park context loss")
		}
		if err := field.set(&j, "ignore"); err == nil {
			t.Fatal("invalid policy accepted")
		}
		return
	}
	t.Fatal("context-loss policy missing from editor")
}

func TestContextRunRowsKeepTheirClickTargets(t *testing.T) {
	m := newTestModel(t)
	m.height = 80
	m.press(t, "l")
	m.detailRuns = []store.Run{
		{ID: "first", Outcome: "done", Context: "resumed", Note: "first remembered"},
		{ID: "second", Outcome: "parked", ParkReason: "blocked", Context: "kept", Note: "second waiting"},
	}
	_, y := frameRow(t, m, "waiting: blocked")
	m.clickCell(t, 4, y)
	if m.cursor != 1 {
		t.Fatalf("click on second rendered run selected %d, want 1", m.cursor)
	}
}
