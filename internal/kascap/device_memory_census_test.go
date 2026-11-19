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
	// deviceMemoryFallbackRe is the frozen config the device read falls back to for an unstated row.
	deviceMemoryFallbackRe = regexp.MustCompile(`([\w$]+)=Object\.freeze\(\{mode:"disabled",reflection:!1\}\)`)
	// deviceMemoryReadRe is deviceMemorySettings' read: the two rows' composed values, each unstated one the
	// fallback's, explicit when a layer other than kiro-agent-defaults decides either row.
	deviceMemoryReadRe = regexp.MustCompile(`let ([\w$]+)=([\w$]+)\.value\(([\w$]+)\.memoryMode\),([\w$]+)=([\w$]+)\.value\(` +
		`([\w$]+)\.memoryReflection\),([\w$]+)=\[[\w$]+\.memoryMode,[\w$]+\.memoryReflection\]\.some\(([\w$]+)=>([\w$]+)\.` +
		`decidedBy\(([\w$]+)\)!=="kiro-agent-defaults"\);return\{config:[\w$]+\(\{mode:([\w$]+)\?\?([\w$]+)\.mode,` +
		`reflection:([\w$]+)===void 0\?([\w$]+)\.reflection:([\w$]+)==="on"\}\),source:([\w$]+)\?"explicit":"legacy"\}`)
	// chooseSessionMemoryRe is chooseSessionMemory: an explicit request, then an opt-out, then the device read.
	chooseSessionMemoryRe = regexp.MustCompile(`async function [\w$]+\(([\w$]+),([\w$]+)\)\{if\(([\w$]+)\.source==="explicit"\)return` +
		`\{choice:\{config:[\w$]+\(([\w$]+)\.config\),source:"explicit"\},origin:"request"\};if\(([\w$]+)\.optsOut\)return` +
		`\{choice:\{config:[\w$]+\(\{\.\.\.([\w$]+)\.config,mode:"disabled"\}\),source:"explicit"\},origin:"optOut"\};` +
		`let ([\w$]+)=await ([\w$]+)\.read\(\);return\{choice:([\w$]+),`)
)

// TestDeviceMemoryDefaultMatchesBundle pins the premises of the Memory setting (internal/agent/memory_mode.go and
// the withheld memory row): KAS grants a client memoryMode and memoryReflection at user-local only, a session/new
// with no memory field and no opt-out takes the device read, and that read takes each row's composed value, an
// unstated one falling back to {disabled, false}, which is what translate.DeviceMemoryView reads from the view.
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
		read := false
		for _, m := range deviceMemoryReadRe.FindAllStringSubmatch(b.src, -1) {
			mode, view, rows, reflection, explicit := m[1], m[2], m[3], m[4], m[7]
			read = read || (m[5] == view && m[6] == rows && m[9] == view && m[8] == m[10] && m[11] == mode &&
				m[13] == reflection && m[15] == reflection && m[12] == m[14] && fallbacks[m[12]] && m[16] == explicit)
		}
		if !read {
			t.Fatal("deviceMemorySettings no longer reads the composed memoryMode and memoryReflection rows with " +
				"{mode: disabled, reflection: false} for an unstated one; re-judge what an unset Memory reads as in " +
				"translate.DeviceMemoryView and which rows PUT /api/memory/mode writes")
		}

		chosen := false
		for _, m := range chooseSessionMemoryRe.FindAllStringSubmatch(b.src, -1) {
			req := m[1]
			chosen = chosen || (m[3] == req && m[4] == req && m[5] == req && m[6] == req && m[8] == m[2] && m[9] == m[7])
		}
		if !chosen {
			t.Error("chooseSessionMemory no longer takes the device read for a request with no memory and no opt-out; " +
				"re-judge withholding the memory row on chat and host doors")
		}
	})
}
