// Deprecated: Bermuda v2 is retired and every v2 version is retracted.
// Use github.com/bon5co/bermuda/v3.
module github.com/bon5co/bermuda/v2

go 1.26.6

// The whole v2 line is withdrawn, this tag included. v2.9.0 and v2.9.1 were
// already retracted for publishing identifiers belonging to a private repository
// in a documentation example and its fixtures; the rest are withdrawn because v3
// replaced them, and an unretracted v2 left standing is one `go get
// github.com/bon5co/bermuda/v2` away from installing a version nobody maintains.
//
// proxy.golang.org caches published versions permanently and says so, so deleting
// tags does nothing -- this directive is the only thing that makes the go command
// refuse them. Retracting the line down to its own last tag means there is no
// unretracted v2 to fall back to, so a v2 request fails instead of quietly
// succeeding with the wrong binary. Retraction alone does not forward anyone to
// v3 -- an all-retracted major answers `@latest` with "no matching versions" --
// so the deprecation notice above the module directive carries the redirect, and
// each rationale below names v3 for the reader who only sees one line.
retract (
	[v2.0.0, v2.9.2] // Retired. Use github.com/bon5co/bermuda/v3.
	v2.9.3 // Retraction notice only, not a release. Use github.com/bon5co/bermuda/v3.
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
	gopkg.in/yaml.v3 v3.0.1 // indirect
	modernc.org/libc v1.74.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)
