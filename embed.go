package main

import "embed"

// staticFS holds the compiled web UI (static/, built by `go run ./cmd/bundle`). It lives
// in its own file because //go:embed fails on a cold clone before the bundle has run.

//go:embed static
var staticFS embed.FS
