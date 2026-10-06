package kascap

import (
	"strings"
	"testing"
)

// rowID is a row's identity: a key alone is not unique, because "knowledge" is
// deliberately BOTH a top-level capability and a settings entry (the knowledge
// gate has three parts and two of them are on this wire). The resolver is what
// separates them, and it also decides which container the key lands in, so
// (resolver, key) is the identity every structural check uses.
func rowID(d decl) string { return string(d.resolver) + "." + d.key }

// TestEveryDeclHasABecause is the package's reason to exist, expressed as a
// gate: a row that cannot say why the key is on the wire (or why it is not)
// carries no more information than the map literal this table replaced.
func TestEveryDeclHasABecause(t *testing.T) {
	if len(table) == 0 {
		t.Fatal("table is empty; every check in this file would pass forever")
	}
	for _, row := range table {
		t.Run(rowID(row), func(t *testing.T) {
			if strings.TrimSpace(row.because) == "" {
				t.Errorf("%s has no because; state what the key buys and what breaks without it", rowID(row))
			}
			if strings.EqualFold(strings.TrimSpace(row.because), row.key) {
				t.Errorf("%s's because only restates its key", rowID(row))
			}
		})
	}
}

// withheldReasonFloor is a character floor on a withheld row's because. It is
// deliberately low: it rules out a token ("unused", "TODO", "not needed") and
// nothing more. It is not a prose rule, and a row that needs fewer words than
// this to explain a deliberate omission almost certainly has not explained it.
const withheldReasonFloor = 40

// TestNoSendWithoutReason gates the rows a map literal could never carry: keys marotte deliberately
// withholds, each with a reason.
func TestNoSendWithoutReason(t *testing.T) {
	withheld := 0
	for _, row := range table {
		if row.send {
			continue
		}
		withheld++
		t.Run(rowID(row), func(t *testing.T) {
			if len(strings.TrimSpace(row.because)) < withheldReasonFloor {
				t.Errorf("%s is withheld on a %d-character reason; say what withholding causes",
					rowID(row), len(strings.TrimSpace(row.because)))
			}
			if row.value != nil {
				t.Errorf("%s is withheld but declares a wire value (%v); a value that is never sent will drift",
					rowID(row), row.value)
			}
			if row.gate != nil {
				t.Errorf("%s is withheld but declares a gate; the gate can never run", rowID(row))
			}
		})
	}
	if withheld == 0 {
		t.Error(`no row declares send:false, so this gate is vacuous.
The table's stated purpose includes recording the keys marotte deliberately
withholds; if the last such row was deleted, that information was lost with it.`)
	}
}

// doorCollisions reports every row identity that appears on more than one door.
// Taking the table as a parameter is what lets the test below check the CHECK
// on a deliberately-broken table, rather than asserting a property that is
// currently vacuous.
func doorCollisions(rows []decl) []string {
	doors := make(map[string]map[door]bool)
	for _, row := range rows {
		if doors[rowID(row)] == nil {
			doors[rowID(row)] = make(map[door]bool)
		}
		doors[rowID(row)][row.door] = true
	}
	var bad []string
	for id, seen := range doors {
		if len(seen) > 1 {
			bad = append(bad, id)
		}
	}
	return bad
}

// TestSessionDoorKeysAbsentFromConnectionDoor pins that a key rides exactly one door, catching a
// move that only half happened.
func TestSessionDoorKeysAbsentFromConnectionDoor(t *testing.T) {
	t.Run("the real table", func(t *testing.T) {
		if bad := doorCollisions(table); len(bad) > 0 {
			t.Errorf("these rows are declared on both doors: %v", bad)
		}
	})

	t.Run("the check catches a half-finished move", func(t *testing.T) {
		moved := decl{
			key:      "userInput",
			door:     doorSession,
			resolver: resolverCapability,
			value:    true,
			send:     true,
			because:  "a synthetic duplicate of the connection-door row, to prove this check fires",
		}
		bad := doorCollisions(append(append([]decl{}, table...), moved))
		if len(bad) != 1 || bad[0] != "capability.userInput" {
			t.Errorf("doorCollisions did not report the planted duplicate; got %v, want [capability.userInput]", bad)
		}
	})
}

// TestDeclIsWellFormed pins the structural invariants the zero values are
// designed to expose: an unset door or resolver is not a default, and a row
// that is sent must have something to send.
func TestDeclIsWellFormed(t *testing.T) {
	seen := make(map[string]bool)
	for _, row := range table {
		t.Run(rowID(row), func(t *testing.T) {
			if row.door == doorUnset {
				t.Errorf("%s declares no door", rowID(row))
			}
			if row.resolver == resolverUnset {
				t.Errorf("%s declares no resolver", rowID(row))
			}
			if row.send && row.value == nil && row.gate == nil {
				t.Errorf("%s is sent with neither a value nor a gate, so it would send JSON null", rowID(row))
			}
			if row.send && row.value != nil && row.gate != nil {
				t.Errorf("%s declares both a value and a gate; the gate always wins, so the value is a lie", rowID(row))
			}
			if seen[rowID(row)+string(row.door)] {
				t.Errorf("%s is declared twice on the same door", rowID(row))
			}
			seen[rowID(row)+string(row.door)] = true
		})
	}
}

// TestSettingRowsCarryTheEnabledObject asserts that isSettingEnabled returns val.enabled for an object and
// false otherwise, so every sent settings value must be {"enabled": …}.
func TestSettingRowsCarryTheEnabledObject(t *testing.T) {
	vetoRows := map[string]bool{
		"steeringSupervisor": true,
	}

	checked := 0
	for _, row := range table {
		if row.resolver != resolverSetting || !row.send {
			continue
		}
		checked++
		t.Run(rowID(row), func(t *testing.T) {
			for _, v := range settingValuesOf(t, row) {
				obj, ok := v.value.(map[string]any)
				if !ok {
					t.Fatalf("%s's value %s is %T, not an object; isSettingEnabled resolves that to false",
						rowID(row), v.origin, v.value)
				}
				on, ok := obj["enabled"].(bool)
				if !ok {
					t.Fatalf("%s's value %s is %v; enabled must be a bool, and any other type resolves false",
						rowID(row), v.origin, obj)
				}
				if !on && !v.gated && !vetoRows[row.key] {
					t.Errorf(`%s sends enabled:false %s and is not listed as a veto.
An ungated row is a statement that the feature is ON, so a false here is either a
mistake or a refusal nobody wrote down. Add it to vetoRows with the reason.`, rowID(row), v.origin)
				}
			}
		})
	}
	if checked == 0 {
		t.Error("no settings row is sent; this gate checked nothing")
	}
}

// settingValue is one wire value a settings row can produce, with where it came
// from, so a failure names the state that produced it rather than just the row.
type settingValue struct {
	value  any
	origin string
	gated  bool
}

// settingValuesOf returns every wire value a sent settings row can put on the wire: the compiled
// one, or the gate's answer in both Spawn states.
func settingValuesOf(t *testing.T, row decl) []settingValue {
	t.Helper()
	if row.gate == nil {
		return []settingValue{{value: row.value, origin: "(compiled)", gated: false}}
	}
	var out []settingValue
	for _, st := range []struct {
		name  string
		spawn Spawn
	}{
		{"with every gate field off", Spawn{}},
		{"with every gate field on", Spawn{
			SecretStorage: true, Hooks: true,
			Presets:   []string{"read-workspace"},
			Knowledge: true, ToolLoad: true,
			SpecPlan: "full", SpecAskClarification: true,
			WorkValidation: "on", InfraSafetyMonitor: "on",
			TerminalCommandTimeoutMs: 300000,
			InlineAgents:             true, SteeringReminders: true,
		}},
	} {
		if v, present := row.gate(&st.spawn); present {
			out = append(out, settingValue{value: v, origin: st.name, gated: true})
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s is sent but its gate withholds in every state, so it can never reach the wire", rowID(row))
	}
	return out
}

// The tool-search setting must stay off the wire: "Load MCP tools on demand"
// drives the tool_load arm through the child environment, and a still-sent
// toolSearch key would reinstate the array-growing mode if that arm were removed.
func TestToolSearchSettingIsWithheld(t *testing.T) {
	row := findRow(t, resolverSetting, "toolSearch")
	if row.send {
		t.Errorf("%s is sent; it must be a withheld claim row", rowID(row))
	}
	if row.gate != nil || row.value != nil {
		t.Errorf("%s carries a gate or value; a withheld row has no wire value", rowID(row))
	}
}

// findRow returns the one row with this identity, failing when it is absent so a
// renamed key surfaces as a missing row rather than a vacuous pass.
func findRow(t *testing.T, r resolver, key string) decl {
	t.Helper()
	for _, row := range table {
		if row.resolver == r && row.key == key {
			return row
		}
	}
	t.Fatalf("no %s.%s row in the table", r, key)
	return decl{}
}

// TestBuildersDoNotAliasTheTable pins that a caller cannot corrupt the table through a payload it
// was handed.
func TestBuildersDoNotAliasTheTable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(*Spawn) map[string]any
		key   string
	}{
		{"connection", Capabilities, "codeIntelligence"},
		{"session", SessionMeta, "backgroundExecution"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := settingsOf(t, tc.build(&Spawn{}))
			entry, ok := first[tc.key].(map[string]any)
			if !ok {
				t.Fatalf("no %s settings object; got %T", tc.key, first[tc.key])
			}
			entry["enabled"] = false
			delete(first, tc.key)

			second := settingsOf(t, tc.build(&Spawn{}))
			again, ok := second[tc.key].(map[string]any)
			if !ok {
				t.Fatalf("mutating one payload's settings object removed %s from the next one", tc.key)
			}
			if again["enabled"] != true {
				t.Errorf("mutating one payload flipped the next one's %s setting to %v", tc.key, again["enabled"])
			}
		})
	}
}

// settingsOf returns a payload's settings container, failing when it is absent —
// which would otherwise make every assertion above pass over an empty map.
func settingsOf(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	settings, ok := payload[settingsKey].(map[string]any)
	if !ok {
		t.Fatalf("no settings object in the payload; got %T", payload[settingsKey])
	}
	return settings
}
