//go:build !marotte_test

package apitable

import "github.com/cplieger/marotte/internal/route"

// testHookEntries is empty: the test hooks exist only in a marotte_test build.
func testHookEntries() []route.Entry { return nil }
