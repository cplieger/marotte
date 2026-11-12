package kascap

import (
	"regexp"
	"slices"
	"testing"
)

// memorySyncGrantRe is one memorySync row of the grant table _kiro/configuration/set checks a client's write against.
var memorySyncGrantRe = regexp.MustCompile(`\{kind:"setting",setting:[\w$]+\.memorySync,layer:"([a-z-]+)"`)

// TestMemorySyncGrantsMatchBundle pins the premise of the Memories tab's Cloud sync switch
// (internal/agent/memory_sync.go): KAS grants a client memorySync at user-synced only, so the switch writes the
// account's layer and offers no per-device choice.
func TestMemorySyncGrantsMatchBundle(t *testing.T) {
	forEachBundle(t, func(t *testing.T, b bundle) {
		var layers []string
		for _, m := range memorySyncGrantRe.FindAllStringSubmatch(b.src, -1) {
			layers = append(layers, m[1])
		}
		if !slices.Equal(layers, []string{"user-synced"}) {
			t.Errorf("KAS grants memorySync at %v, want [user-synced]; re-judge the Cloud sync scopes in internal/agent/memory_sync.go and static-src/memories.ts", layers)
		}
	})
}
