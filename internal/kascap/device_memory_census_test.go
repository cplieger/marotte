package kascap

import (
	"regexp"
	"slices"
	"testing"
)

var (
	// deviceMemoryGrantRe is one memoryMode or memoryReflection row of the grant table _kiro/configuration/set checks
	// a client's write against.
	deviceMemoryGrantRe = regexp.MustCompile(`\{kind:"setting",setting:[\w$]+\.(memoryMode|memoryReflection),layer:"([a-z-]+)"`)
	// deviceMemoryFallbackRe is the frozen config deviceChoice falls back to for an unstated row.
	deviceMemoryFallbackRe = regexp.MustCompile(`([\w$]+)=Object\.freeze\(\{mode:"disabled",reflection:!1\}\)`)
	// deviceChoiceRe is deviceChoice: each unstated member takes the fallback's, and a stated one makes it explicit.
	deviceChoiceRe = regexp.MustCompile(`function ([\w$]+)\(([\w$]+)\)\{let ([\w$]+)=([\w$]+)\.mode!==void 0\|\|([\w$]+)\.reflection!==void 0;` +
		`return\{config:[\w$]+\(\{mode:([\w$]+)\.mode\?\?([\w$]+)\.mode,reflection:([\w$]+)\.reflection\?\?([\w$]+)\.reflection\}\),` +
		`source:([\w$]+)\?"explicit":"legacy"\}\}`)
	// chooseSessionMemoryRe is chooseSessionMemory: an explicit request, then an opt-out, then the device read.
	chooseSessionMemoryRe = regexp.MustCompile(`async function [\w$]+\(([\w$]+),([\w$]+)\)\{if\(([\w$]+)\.source==="explicit"\)return` +
		`\{choice:\{config:[\w$]+\(([\w$]+)\.config\),source:"explicit"\},origin:"request"\};if\(([\w$]+)\.optsOut\)return` +
		`\{choice:\{config:[\w$]+\(\{\.\.\.([\w$]+)\.config,mode:"disabled"\}\),source:"explicit"\},origin:"optOut"\};` +
		`let [\w$]+=([\w$]+)\(await ([\w$]+)\.read\(\)\);`)
	// deviceMemoryReadRe is deviceMemorySettings' read of the two device rows.
	deviceMemoryReadRe = regexp.MustCompile(`let [\w$]+=[\w$]+\.value\([\w$]+\.memoryMode\),[\w$]+=[\w$]+\.value\([\w$]+\.memoryReflection\);` +
		`return\{\.\.\.[\w$]+===void 0\?\{\}:\{mode:[\w$]+\},\.\.\.[\w$]+===void 0\?\{\}:\{reflection:[\w$]+==="on"\}\}`)
)

// TestDeviceMemoryDefaultMatchesBundle pins the premises of the Memory setting (internal/agent/memory_mode.go and
// the withheld memory row): KAS grants a client memoryMode and memoryReflection at user-local only, a session/new
// with no memory field and no opt-out takes the device rows, and an unstated row falls back to {disabled, false}.
func TestDeviceMemoryDefaultMatchesBundle(t *testing.T) {
	forEachBundle(t, func(t *testing.T, b bundle) {
		grants := map[string][]string{}
		for _, m := range deviceMemoryGrantRe.FindAllStringSubmatch(b.src, -1) {
			grants[m[1]] = append(grants[m[1]], m[2])
		}
		for _, setting := range []string{"memoryMode", "memoryReflection"} {
			if !slices.Equal(grants[setting], []string{"user-local"}) {
				t.Errorf("KAS grants %s at %v, want [user-local]; re-judge configurationDeviceLayer in internal/agent", setting, grants[setting])
			}
		}

		fallbacks := map[string]bool{}
		for _, m := range deviceMemoryFallbackRe.FindAllStringSubmatch(b.src, -1) {
			fallbacks[m[1]] = true
		}
		deviceChoice := ""
		for _, m := range deviceChoiceRe.FindAllStringSubmatch(b.src, -1) {
			arg := m[2]
			if m[4] == arg && m[5] == arg && m[6] == arg && m[8] == arg && m[7] == m[9] && fallbacks[m[7]] && m[3] == m[10] {
				deviceChoice = m[1]
			}
		}
		if deviceChoice == "" {
			t.Fatal("deviceChoice no longer falls back to {mode: disabled, reflection: false} for an unstated row; " +
				"re-judge what an unset Memory reads as in translate.DeviceMemoryView")
		}

		chosen := false
		for _, m := range chooseSessionMemoryRe.FindAllStringSubmatch(b.src, -1) {
			req, device := m[1], m[2]
			chosen = chosen || (m[3] == req && m[4] == req && m[5] == req && m[6] == req && m[7] == deviceChoice && m[8] == device)
		}
		if !chosen {
			t.Error("chooseSessionMemory no longer takes the device choice for a request with no memory and no opt-out; " +
				"re-judge withholding the memory row on chat and host doors")
		}
		if !deviceMemoryReadRe.MatchString(b.src) {
			t.Error("deviceMemorySettings no longer reads the memoryMode and memoryReflection rows; " +
				"re-judge which rows PUT /api/memory/mode writes")
		}
	})
}
