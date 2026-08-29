package board

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bon5co/bermuda/v2/internal/mention"
	"github.com/bon5co/bermuda/v2/internal/store"
)

// Writing into the thread from the board.
//
// The thread was readable from the board and writable only from a shell, which
// made it a log rather than a conversation: an agent could put a question in it
// and the person watching the board had no way to answer without leaving the
// board. A chatroom you cannot type into is a feed.
//
// It is multi-line on purpose. What actually gets written here is a correction
// or an instruction, and both of those run to more than one line often enough
// that a single-line prompt would train everyone to write terse and lose the
// detail.

// composeDefault is who the board posts as when nothing says otherwise.
//
// Unlike an agent's identity this deliberately has a default. The CLI refuses
// to guess because a wrong guess there silently makes two agents the same
// lease-holder; here there is a person at a keyboard, and the cost of being
// wrong is a mislabelled note.
const composeDefault = "handler"

// composer is the open input box.
type composer struct {
	area textarea.Model
	// err is the last refusal, shown under the box rather than swallowed.
	err string
}

func newComposer() *composer {
	ta := textarea.New()
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetHeight(3)
	ta.Placeholder = "say something to the thread"
	ta.Focus()
	return &composer{area: ta}
}

// composeAs is the name the board writes under. It is also how the thread
// decides which bubbles are a person's.
//
// The old BERMUDA_ROOM_HUMAN is still honoured. The variable is exported by
// shells and session files that were written before the feature was renamed,
// and a board that quietly stopped seeing them would start labelling the person's
// own messages as an agent's — a silent wrong answer, not an error.
func (m *Model) composeAs() string {
	for _, env := range []string{"BERMUDA_THREAD_HUMAN", "BERMUDA_ROOM_HUMAN"} {
		if who := strings.TrimSpace(os.Getenv(env)); who != "" {
			return who
		}
	}
	return composeDefault
}

// openCompose starts a message.
func (m *Model) openCompose() {
	m.compose = newComposer()
	m.compose.area.SetWidth(m.bubbleTextMax(m.humanIndent()))
	// The box names its destination, since which conversation this is going
	// into is the one thing about the message that cannot be seen once it is
	// typed.
	m.compose.area.Placeholder = "say something to " + m.currentThread()
	// A box you are typing into that has scrolled off the pane is worse than no
	// box, so composing pins the thread to its newest end.
	m.threadFollow = true
}

// handleComposeKey drives the input box.
//
// Enter inserts a newline, so the box commits with ctrl+s — the same bargain
// the job editor's multi-line fields make, and for the same reason.
func (m *Model) handleComposeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.compose = nil
		return m, nil
	case "ctrl+s":
		return m, m.postComposed()
	}
	var cmd tea.Cmd
	m.compose.area, cmd = m.compose.area.Update(msg)
	return m, cmd
}

// postComposed writes the message and closes the box.
func (m *Model) postComposed() tea.Cmd {
	body := strings.TrimSpace(m.compose.area.Value())
	if body == "" {
		// Closing on an empty box would look identical to posting, and the
		// reader would go looking in the thread for a message that was never
		// written.
		m.compose.err = "nothing to post yet"
		return nil
	}
	who := m.composeAs()
	// The message goes into the conversation on screen, not into global. A box
	// that posted somewhere other than what is above it would put a reply to a
	// question in a thread the asker is not reading.
	into := m.currentThread()
	m.compose = nil
	m.threadFollow = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		msg, err := m.store.ThreadPost(ctx, store.ThreadMessage{
			Thread: into,
			Kind:   store.KindNote,
			By:     store.Identity{Name: who},
			Body:   body,
		})
		if err != nil {
			return actionMsg{err: err}
		}
		status := "posted to " + into + " as " + who
		if reached := m.tellTheMentioned(msg); reached != "" {
			status += " · " + reached
		}
		return actionMsg{status: status}
	}
}

// tellTheMentioned pushes a just-posted message to the agents it names, and
// returns a line about it for the status bar.
//
// It runs inside the tea.Cmd that posted the message, which is a goroutine
// bubbletea runs off its event loop — the board keeps ticking, scrolling and
// redrawing while herdr is being talked to. Doing this in Update instead would
// freeze the pane for as long as herdr takes to answer, which is exactly the
// sort of stall a board left open in a split gets blamed for.
//
// It cannot fail the post: the message is already written by the time this
// runs, so every outcome here is something to say rather than something to
// raise.
func (m *Model) tellTheMentioned(msg store.ThreadMessage) string {
	ctx, cancel := context.WithTimeout(context.Background(), mentionTimeout)
	defer cancel()
	res, err := mention.Deliver(ctx, m.herd(), mention.Message{
		Thread: msg.Thread, Author: msg.By.String(), Body: msg.Body,
		Workspace: m.workspaceOf(ctx, msg.Thread),
	}, mention.Me(msg.By.Name))
	if err != nil {
		return "could not ask herdr who is live"
	}
	return mention.Status(res)
}

// workspaceOf is the space a thread belongs to, which is what bounds an `@all`
// posted from the board. Empty for global and for any thread made by hand, and
// `@all` in one of those reaches nobody by design.
func (m *Model) workspaceOf(ctx context.Context, thread string) string {
	if thread == "" || thread == store.GlobalThread {
		return ""
	}
	threads, err := m.store.Threads(ctx)
	if err != nil {
		return ""
	}
	for _, t := range threads {
		if t.ID == thread {
			return t.WorkspaceID
		}
	}
	return ""
}

// mentionTimeout bounds the whole delivery, however many agents were named.
// The board is a viewer: a message that could not be delivered in this long is
// better reported than waited on.
const mentionTimeout = 15 * time.Second

// herd is who the board can reach. Tests set m.mentions to a fake; nothing else
// does, so in normal use this is herdr.
func (m *Model) herd() mention.Herd {
	if m.mentions != nil {
		return m.mentions
	}
	return mention.FromHerdr(m.herdr)
}

// renderCompose draws the input box in the same shape as the message it is
// about to become, indented to the side a person's bubbles sit on.
func (m *Model) renderCompose() string {
	indent := m.humanIndent()
	textW := m.bubbleTextMax(indent)
	m.compose.area.SetWidth(textW)

	lines := strings.Split(strings.TrimRight(m.compose.area.View(), "\n"), "\n")
	box := bubbleBox{
		header: m.composeAs(),
		footer: "enter newline · ctrl+s post · esc cancel",
		lines:  lines, textW: textW,
		border: humanStyle, head: humanStyle.Bold(true), indent: indent,
	}.render()
	if m.compose.err != "" {
		box += strings.Repeat(" ", indent) +
			outcomeStyles["failed"].Render(m.compose.err) + "\n"
	}
	return box
}
