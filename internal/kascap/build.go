package kascap

import (
	"maps"
	"slices"
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
func Capabilities(s *Spawn) map[string]any { return buildDoor(doorConnection, s, nil) }

// SessionMeta returns the _meta.kiro map for the session door, ready to sit
// under a session/new or session/load request's _meta.kiro.
//
// The caller sends it on BOTH verbs and only when it is NON-EMPTY, so a table
// that declares no session key adds no bytes to a call that carries none. Like
// Capabilities, the returned map is the caller's own.
func SessionMeta(s *Spawn) map[string]any { return buildDoor(doorSession, s, nil) }

// PromptMeta returns the _meta.kiro map for one session/prompt. The caller sends it only when it is
// NON-EMPTY; like the other projections, the returned map is the caller's own.
func PromptMeta(p *Prompt) map[string]any { return buildDoor(doorPrompt, nil, p) }

// ChildEnv returns the environment-door rows as KEY=value assignments, sorted
// by name, for the bridge to append AFTER its credential screen so they win
// over anything inherited.
func ChildEnv(s *Spawn) []string {
	vars := buildDoor(doorEnvironment, s, nil)
	out := make([]string, 0, len(vars))
	for name, v := range vars {
		val, _ := v.(string)
		out = append(out, name+"="+val)
	}
	slices.Sort(out)
	return out
}

func buildDoor(d door, s *Spawn, p *Prompt) map[string]any {
	out := make(map[string]any, len(table))
	settings := make(map[string]any)
	for i := range table {
		row := &table[i]
		if row.door != d || !row.send {
			continue
		}
		value, present := row.value, true
		switch {
		case row.gate != nil:
			value, present = row.gate(s)
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
