// Command foldgen writes the accent table textsearch.FoldText and static-src/textsearch's foldText both read,
// from one Unicode source, so the Go and TypeScript folds cannot drift. Run it from the module root.
package main

import (
	"log"
	"os"
)

func main() {
	t := accentTable()
	goSrc, err := goSource(t)
	if err != nil {
		log.Fatalf("format %s: %v", goPath, err)
	}
	if err := os.WriteFile(goPath, goSrc, 0o600); err != nil {
		log.Fatalf("write %s: %v", goPath, err)
	}
	if err := os.WriteFile(tsPath, tsSource(t), 0o600); err != nil {
		log.Fatalf("write %s: %v", tsPath, err)
	}
}
