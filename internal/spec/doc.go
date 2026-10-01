// Package spec reads a Kiro spec directory the way Kiro itself does: the
// roots kiro_docs.go scans, the lexical classifier that names the spec
// directory a workspace path belongs to, a tasks.md parser transcribed from
// the five functions of the KAS 2.21.4 bundle (jTr the line regex, _gt the
// task number, jqi one task and its content, Llc the number re-parenting,
// uw the file walk), the bounded loader over os.OpenRoot, and the coalescer
// that turns file writes into one spec_changed per directory per window.
// The parser's goldens are generated from those five functions by
// `node internal/spec/testdata/gen-goldens.mjs <acp-server.js>`, so the Go
// transcription is checked against Kiro's own output rather than against a
// description of it.
package spec
