package board

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestEditorKeepsSelectedFieldsAndHelpVisible(t *testing.T) {
	m := newTestModel(t)
	m.height = 24
	e := openEditorOn(t, m, "alpha", "name")
	for i, f := range e.fields {
		e.cursor = i
		out := m.View()
		if !strings.Contains(out, f.label) || !strings.Contains(out, "ctrl+s save job") || !strings.Contains(out, "esc abandon") {
			t.Fatalf("field %s or controls hidden:\n%s", f.key, out)
		}
		if blockRows(out) > 24 {
			t.Fatalf("editor exceeded height: %d", blockRows(out))
		}
	}
}

func TestActivePromptFitsAfterResize(t *testing.T) {
	m := newTestModel(t)
	e := openEditorOn(t, m, "alpha", "prompt")
	e.beginField()
	e.area.SetValue(strings.Repeat("a line of prompt text\n", 20) + "LAST TYPED ")
	e.area.CursorEnd()
	m.typeText(t, "LINE")
	for _, size := range []tea.WindowSizeMsg{{Width: 80, Height: 24}, {Width: 50, Height: 12}, {Width: 100, Height: 24}} {
		m.Update(size)
		out := m.View()
		if !strings.Contains(out, "LAST TYPED LINE") || !strings.Contains(out, "ctrl+s save job") {
			t.Fatalf("active prompt or save help clipped at %dx%d:\n%s", size.Width, size.Height, out)
		}
		if blockRows(out) > size.Height {
			t.Fatalf("prompt exceeded height")
		}
		for _, line := range strings.Split(out, "\n") {
			if lipgloss.Width(line) > size.Width {
				t.Fatalf("line exceeds width %d: %q", size.Width, line)
			}
		}
	}
}

func TestRejectedFieldErrorStaysVisible(t *testing.T) {
	m := newTestModel(t)
	m.height = 12
	e := openEditorOn(t, m, "alpha", "timeout")
	e.beginField()
	e.input.SetValue("soon")
	m.pressSpecial(t, tea.KeyCtrlS)
	out := m.View()
	if !strings.Contains(out, "timeout:") || !strings.Contains(out, "ctrl+s save job") || !strings.Contains(out, "soon") {
		t.Fatalf("active value, error, or save controls hidden:\n%s", out)
	}
}

func TestEditorFitsTinyPanes(t *testing.T) {
	m := newTestModel(t)
	e := openEditorOn(t, m, "alpha", "prompt")
	e.beginField()
	for height := 1; height <= 10; height++ {
		m.Update(tea.WindowSizeMsg{Width: 50, Height: height})
		if got := blockRows(m.View()); got > height {
			t.Fatalf("height %d rendered %d rows", height, got)
		}
	}
}

func TestActiveSingleLineInputFitsAfterResize(t *testing.T) {
	m := newTestModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	e := openEditorOn(t, m, "alpha", "name")
	e.beginField()
	m.typeText(t, strings.Repeat("x", 100)+"TAIL")
	m.View()
	m.Update(tea.WindowSizeMsg{Width: 50, Height: 24})
	out := m.View()
	if !strings.Contains(out, "TAIL") {
		t.Fatalf("typed tail hidden after resize:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if lipgloss.Width(line) > 50 {
			t.Fatalf("resized line exceeds 50 columns (%d): %q", lipgloss.Width(line), line)
		}
	}
}

func TestPromptWidgetFitsBelowTableMinimumWidth(t *testing.T) {
	m := newTestModel(t)
	m.Update(tea.WindowSizeMsg{Width: 20, Height: 24})
	e := openEditorOn(t, m, "alpha", "prompt")
	e.beginField()
	m.typeText(t, "a longer prompt line to wrap")
	m.View()
	for _, line := range strings.Split(e.area.View(), "\n") {
		if lipgloss.Width(line) > 20 {
			t.Fatalf("prompt widget exceeds actual pane: %q", line)
		}
	}
}

func TestEditorRowsFitBelowTableMinimumWidth(t *testing.T) {
	m := newTestModel(t)
	m.Update(tea.WindowSizeMsg{Width: 20, Height: 24})
	e := openEditorOn(t, m, "alpha", "name")
	for _, active := range []bool{false, true} {
		if active {
			e.beginField()
			m.typeText(t, "long input ending TAIL")
		}
		for _, line := range strings.Split(m.View(), "\n") {
			if lipgloss.Width(line) > 20 {
				t.Fatalf("editor row exceeds actual width: %q", line)
			}
		}
	}
}

func TestClosingPromptEditorResetsDetailScroll(t *testing.T) {
	for _, key := range []tea.KeyType{tea.KeyEsc, tea.KeyCtrlS} {
		t.Run(key.String(), func(t *testing.T) {
			m := newTestModel(t)
			m.height = 24
			m.focus = focusJobs
			m.pressSpecial(t, tea.KeyEnter)
			m.press(t, "e")
			e := m.editor
			e.cursor = fieldIndex(t, e, "prompt")
			e.beginField()
			m.View()
			if m.scroll == 0 {
				t.Fatal("test did not scroll the prompt editor")
			}
			_, cmd := m.Update(tea.KeyMsg{Type: key})
			if cmd != nil {
				drainEditAction(t, m, cmd())
			}
			if m.editor != nil || m.scroll != 0 {
				t.Fatalf("editor left detail scrolled: editor=%v scroll=%d", m.editor != nil, m.scroll)
			}
		})
	}
}
