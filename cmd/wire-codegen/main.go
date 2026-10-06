// Command wire-codegen generates the TypeScript interfaces, validating decoders and
// SSE event registry in static-src/wire/ from the Go wire types, using
// github.com/cplieger/wiregen/v3. The contract lives in internal/wirespec.
//
// Run: go run ./cmd/wire-codegen   (from the marotte repo root)
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/cplieger/marotte/internal/wirespec"
)

func main() {
	os.Exit(run())
}

// run returns the exit code so the deferred cancel runs on every path. The context is a
// signal context because loading packages runs `go` as a subprocess that can fetch for
// minutes; an interrupt must cancel it so the staged output is cleaned up.
func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	r := wirespec.Registry()
	outDir := filepath.Join("static-src", "wire")
	if err := r.Generate(ctx, outDir); err != nil {
		// Name the signal so a cancelled run does not read as a broken registry.
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "wire-codegen: interrupted")
			return 130
		}
		fmt.Fprintf(os.Stderr, "wire-codegen: %v\n", err)
		return 1
	}
	fmt.Println("wire-codegen: generated " + outDir + "/{types,decoders,registry,arbitraries}.gen.ts")
	return 0
}
