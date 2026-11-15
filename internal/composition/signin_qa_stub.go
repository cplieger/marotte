//go:build !marotte_test

package composition

import (
	"context"

	"github.com/cplieger/marotte/internal/signin"
)

// qaSignIn returns b: the scripted sign-in exists only in a binary built with -tags marotte_test
// (signin_qa_marottetest.go).
func qaSignIn(b signin.Backend) signin.Backend { return b }

func startQASignIn(context.Context, *signin.Vault, signin.BackendID) {}
