// Package spec reads a Kiro spec directory the way Kiro does: the roots, the classifier naming
// a path's spec directory, a tasks.md parser transcribed from the KAS 2.21.4 bundle, the
// bounded loader, and the spec_changed coalescer. The parser's goldens come from KAS's own
// functions via `node internal/spec/testdata/gen-goldens.mjs <acp-server.js>`.
package spec
