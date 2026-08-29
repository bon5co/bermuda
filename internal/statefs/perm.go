// Package statefs holds the permission bits every file and directory bermuda
// creates under its state directory is made with.
//
// The reason they are constants in one package rather than a literal at each
// call site: what bermuda writes there is not configuration. It is prompts,
// transcripts, thread bodies, forum posts and result files — the whole content
// of what agents were told to do and what they said back. On a machine with a
// second login, 0644 hands all of it to that login for the cost of a `cat`.
// Nothing bermuda writes is meant to be read by another user, and nothing it
// writes is executed, so the owner-only bits are the whole set it needs.
package statefs

const (
	// Dir is the mode for a directory bermuda creates. MkdirAll only applies
	// it to directories it actually creates, so pointing bermuda at an
	// existing directory — a vault someone else made — leaves that one alone.
	Dir = 0o700

	// File is the mode for a file bermuda creates.
	File = 0o600
)
