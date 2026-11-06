// Command foldgen writes the accent table textsearch.FoldText and static-src/textsearch's foldText both read,
// from one Unicode source, so the Go and TypeScript folds cannot drift. Run it from the module root.
package main

import (
	"log"
	"os"
)

func main() {
	t := Table()
	goSrc, err := GoSource(t)
	if err != nil {
		log.Fatalf("format %s: %v", GoPath, err)
	}
	if err := os.WriteFile(GoPath, goSrc, 0o600); err != nil {
		log.Fatalf("write %s: %v", GoPath, err)
	}
	if err := os.WriteFile(TSPath, TSSource(t), 0o600); err != nil {
		log.Fatalf("write %s: %v", TSPath, err)
	}
}
