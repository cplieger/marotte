package kascap

import (
	"regexp"
	"slices"
	"testing"
)

// cloudMemoryGrantRe is one cloudMemory row of the grant table _kiro/configuration/set checks a client's write against.
var cloudMemoryGrantRe = regexp.MustCompile(`\{kind:"setting",setting:[\w$]+\.cloudMemory,layer:"([a-z-]+)"`)

// TestMemorySyncGrantsMatchBundle pins the premise of the Memories tab's Cloud sync switch
// (internal/agent/memory_sync.go): KAS grants a client cloudMemory at user-synced only, so the switch writes the
// account's layer and offers no per-device choice.
func TestMemorySyncGrantsMatchBundle(t *testing.T) {
	forEachBundle(t, func(t *testing.T, b bundle) {
		var layers []string
		for _, m := range cloudMemoryGrantRe.FindAllStringSubmatch(b.src, -1) {
			layers = append(layers, m[1])
		}
		if !slices.Equal(layers, []string{"user-synced"}) {
			t.Errorf("KAS grants cloudMemory at %v, want [user-synced]; re-judge the Cloud sync scopes in internal/agent/memory_sync.go and static-src/memories.ts", layers)
		}
	})
}
