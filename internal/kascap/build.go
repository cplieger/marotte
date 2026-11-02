package kascap

import (
	"maps"
	"slices"
	"strings"
)

// settingsKey is the container every resolverSetting row lands in. KAS reads
// _meta.kiro.settings as one object and resolves each member independently, so
// the container is derived from the rows rather than declared as a row of its
// own: with no settings sent there is no settings key.
const settingsKey = "settings"

// Capabilities returns the _meta.kiro map for the initialize handshake, ready
// to sit under clientCapabilities._meta.kiro.
//
// The returned map is the caller's own: it shares no object with the table, so
// a caller may hold or modify it without reaching back into this package.
func Capabilities(e *EngineSpawn) map[string]any { return buildDoor(doorConnection, e, nil, nil) }

// SessionMeta returns the _meta.kiro map for the session door, ready to sit
// under a session/new or session/load request's _meta.kiro.
//
// The caller sends it on BOTH verbs and only when it is NON-EMPTY, so a table
// that declares no session key adds no bytes to a call that carries none. Like
// Capabilities, the returned map is the caller's own.
func SessionMeta(s *SessionSpawn) map[string]any { return buildDoor(doorSession, nil, s, nil) }

// PromptMeta returns the _meta.kiro map for one session/prompt. The caller sends it only when it is
// NON-EMPTY; like the other projections, the returned map is the caller's own.
func PromptMeta(p *Prompt) map[string]any { return buildDoor(doorPrompt, nil, nil, p) }

// ChildEnv returns the environment-door rows as KEY=value assignments, sorted
// by name, for the engine spawn to append after its credential screen and the
// OwnsEnv drop, so they win over anything inherited.
func ChildEnv(e *EngineSpawn) []string {
	vars := buildDoor(doorEnvironment, e, nil, nil)
	out := make([]string, 0, len(vars))
	for name, v := range vars {
		val, _ := v.(string)
		out = append(out, name+"="+val)
	}
	slices.Sort(out)
	return out
}

// ownedEnv is every settingOwned row's name (OwnsEnv).
var ownedEnv = func() map[string]bool {
	out := map[string]bool{}
	for i := range table {
		if table[i].settingOwned {
			out[table[i].key] = true
		}
	}
	return out
}()

// OwnsEnv reports whether kv, a KEY=value assignment, names a variable a marotte setting decides.
// The engine spawn drops such names from the inherited environment before appending ChildEnv, so a
// withheld arm leaves KAS's own default in force. Every other name stays the operator's to set.
func OwnsEnv(kv string) bool {
	name, _, _ := strings.Cut(kv, "=")
	return ownedEnv[name]
}

// buildDoor projects door d's sent rows; each gate receives only its own door's input.
func buildDoor(d door, e *EngineSpawn, s *SessionSpawn, p *Prompt) map[string]any {
	out := make(map[string]any, len(table))
	settings := make(map[string]any)
	for i := range table {
		row := &table[i]
		if row.door != d || !row.send {
			continue
		}
		value, present := row.value, true
		switch {
		case row.engineGate != nil:
			value, present = row.engineGate(e)
		case row.sessionGate != nil:
			value, present = row.sessionGate(s)
		case row.promptGate != nil:
			value, present = row.promptGate(p)
		}
		if !present {
			continue
		}
		if inSettings(row.resolver) {
			settings[row.key] = cloneValue(value)
			continue
		}
		out[row.key] = cloneValue(value)
	}
	if len(settings) > 0 {
		out[settingsKey] = settings
	}
	return out
}

// cloneValue returns a value the caller can hold without aliasing the table: maps and slices are
// built once at init, so a caller mutating a payload would change the next one. One level suffices
// as the table stands; a nested container would need a deeper clone
// (TestBuildersDoNotAliasTheTable).
func cloneValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return maps.Clone(t)
	case []string:
		return slices.Clone(t)
	default:
		return v
	}
}
