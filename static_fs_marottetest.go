//go:build marotte_test

package main

import (
	"io/fs"
	"os"
)

// appStatic serves the client from MAROTTE_TEST_STATIC_ROOT's static/ when set, so the e2e
// suite drives the client it just bundled: CI builds the test binary before any bundle exists.
func appStatic() fs.FS {
	if root := os.Getenv("MAROTTE_TEST_STATIC_ROOT"); root != "" {
		return os.DirFS(root)
	}
	return staticFS
}
