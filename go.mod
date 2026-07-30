module github.com/bon5co/bermuda

go 1.26.5

// Bermuda v1 is retired. Development continues at github.com/bon5co/bermuda/v2,
// which is where every tag from v2.0.0 onwards lives.
//
// This tag exists only to carry these lines. Until it was published, v2 was
// tagged while go.mod still said `module github.com/bon5co/bermuda`, so the
// proxy could not see any v2 version and `go install
// github.com/bon5co/bermuda/cmd/bermuda@latest` — the command the README gave —
// resolved the newest v1 tag and installed v1.1.1 without a word. A working
// binary from before flows and threads existed is a worse answer than an error,
// because nothing about it says it is the wrong one.
//
// Retracting the whole line, this version included, turns that silence into a
// refusal: there is no unretracted v1 left to fall back to, so the old command
// fails and names its replacement instead of quietly succeeding.
retract (
	[v1.0.0, v1.1.1] // Retired. Use github.com/bon5co/bermuda/v2.
	v1.1.2 // This tag is only the retraction notice; it is not a release.
)

require (
	github.com/charmbracelet/bubbles v1.0.0
	github.com/charmbracelet/bubbletea v1.3.10
	github.com/charmbracelet/lipgloss v1.1.0
	github.com/charmbracelet/x/term v0.2.2
	github.com/muesli/termenv v0.16.0
	github.com/robfig/cron/v3 v3.0.1
	golang.org/x/sys v0.46.0
	modernc.org/sqlite v1.54.0
)

require (
	github.com/atotto/clipboard v0.1.4 // indirect
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/charmbracelet/colorprofile v0.4.1 // indirect
	github.com/charmbracelet/x/ansi v0.11.6 // indirect
	github.com/charmbracelet/x/cellbuf v0.0.15 // indirect
	github.com/clipperhouse/displaywidth v0.9.0 // indirect
	github.com/clipperhouse/stringish v0.1.1 // indirect
	github.com/clipperhouse/uax29/v2 v2.5.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/erikgeiser/coninput v0.0.0-20211004153227-1c3628e74d0f // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.3.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-localereader v0.0.1 // indirect
	github.com/mattn/go-runewidth v0.0.19 // indirect
	github.com/muesli/ansi v0.0.0-20230316100256-276c6243b2f6 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	golang.org/x/text v0.3.8 // indirect
	modernc.org/libc v1.74.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)
