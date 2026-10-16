package kascap

import (
	"encoding/json"
	"fmt"
	"slices"
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
			if strings.TrimSpace(because[rowID(row)]) == "" {
				t.Errorf("%s has no because; state what the key buys and what breaks without it", rowID(row))
			}
			if strings.EqualFold(strings.TrimSpace(because[rowID(row)]), row.key) {
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
			if len(strings.TrimSpace(because[rowID(row)])) < withheldReasonFloor {
				t.Errorf("%s is withheld on a %d-character reason; say what withholding causes",
					rowID(row), len(strings.TrimSpace(because[rowID(row)])))
			}
			if row.value != nil {
				t.Errorf("%s is withheld but declares a wire value (%v); a value that is never sent will drift",
					rowID(row), row.value)
			}
			if row.gated() {
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
			if row.door == "" {
				t.Errorf("%s declares no door", rowID(row))
			}
			if row.resolver == "" {
				t.Errorf("%s declares no resolver", rowID(row))
			}
			gated := row.gated()
			if row.send && row.value == nil && !gated {
				t.Errorf("%s is sent with neither a value nor a gate, so it would send JSON null", rowID(row))
			}
			if row.send && row.value != nil && gated {
				t.Errorf("%s declares both a value and a gate; the gate always wins, so the value is a lie", rowID(row))
			}
			// OwnsEnv drops an inherited value of the row's name; anywhere else that name is no variable.
			if row.settingOwned && (row.door != doorEnvironment || !row.send) {
				t.Errorf("%s is settingOwned but not a sent environment row", rowID(row))
			}
			if seen[rowID(row)+string(row.door)] {
				t.Errorf("%s is declared twice on the same door", rowID(row))
			}
			seen[rowID(row)+string(row.door)] = true
		})
	}
}

func (d decl) gated() bool { return d.engineGate != nil || d.sessionGate != nil || d.promptGate != nil }

// gateFault reports why a row's gate reads another door's input, or "" when it reads its own. Each projection hands
// its rows only its own door's input, so a mismatched gate would be called with nil; the typed gates make "this
// setting needs an engine restart" a fact of the row's door.
func gateFault(d decl) string {
	engineDoor := d.door == doorConnection || d.door == doorEnvironment
	switch {
	case d.engineGate != nil && !engineDoor:
		return "reads EngineSpawn off the connection and environment doors"
	case d.sessionGate != nil && d.door != doorSession:
		return "reads SessionSpawn off the session door"
	case d.promptGate != nil && d.door != doorPrompt:
		return "reads Prompt off the prompt door"
	}
	n := 0
	for _, set := range []bool{d.engineGate != nil, d.sessionGate != nil, d.promptGate != nil} {
		if set {
			n++
		}
	}
	if n > 1 {
		return "declares more than one gate"
	}
	return ""
}

func TestTable_GateMatchesDoor(t *testing.T) {
	t.Run("the real table", func(t *testing.T) {
		for _, row := range table {
			if fault := gateFault(row); fault != "" {
				t.Errorf("%s %s", rowID(row), fault)
			}
		}
	})
	t.Run("the check catches a gate on the wrong door", func(t *testing.T) {
		for _, bad := range []decl{
			{key: "x", door: doorSession, engineGate: func(*EngineSpawn) (any, bool) { return true, true }},
			{key: "x", door: doorConnection, sessionGate: func(*SessionSpawn) (any, bool) { return true, true }},
			{key: "x", door: doorEnvironment, promptGate: func(*Prompt) (any, bool) { return true, true }},
			{key: "x", door: doorPrompt, sessionGate: func(*SessionSpawn) (any, bool) { return true, true }},
		} {
			if gateFault(bad) == "" {
				t.Errorf("gateFault(%s on %s) = \"\", want a fault", rowID(bad), bad.door)
			}
		}
	})
}

// TestSettingRowsCarryTheEnabledObject asserts that isSettingEnabled returns val.enabled for an object and
// false otherwise, so every sent settings value must be {"enabled": …}.
func TestSettingRowsCarryTheEnabledObject(t *testing.T) {
	vetoRows := map[string]bool{
		"steeringSupervisor": true,
		// KAS's own default, sent so every session states it.
		"_providerPowers": true,
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

// valueShapeFault reports why v cannot be a value KAS reads through r, or "" when it can.
func valueShapeFault(r resolver, v any) string {
	switch r {
	case resolverCapability:
		if _, ok := v.(bool); !ok {
			return fmt.Sprintf("is %T; KAS compares a capability against true, so it must be a bool", v)
		}
	case resolverSetting:
		obj, ok := v.(map[string]any)
		if !ok {
			return fmt.Sprintf("is %T; isSettingEnabled resolves anything but an object to false", v)
		}
		if _, ok := obj["enabled"].(bool); !ok {
			return fmt.Sprintf("is %v; isSettingEnabled needs a bool enabled member", obj)
		}
	case resolverObject, resolverSettingObject:
		if _, ok := v.(map[string]any); !ok {
			return fmt.Sprintf("is %T; KAS destructures this key, so it must be an object", v)
		}
	case resolverEnv:
		if s, ok := v.(string); !ok || (s != "true" && s != "false") {
			return fmt.Sprintf("is %#v; KAS's env provider accepts only \"true\" or \"false\"", v)
		}
	case resolverEnvJSON:
		s, ok := v.(string)
		var obj map[string]any
		if !ok || json.Unmarshal([]byte(s), &obj) != nil {
			return fmt.Sprintf("is %#v; KAS JSON-parses a _CONFIG arm, so it must be a JSON object string", v)
		}
	case resolverEnum:
		if s, ok := v.(string); !ok || s == "" {
			return fmt.Sprintf("is %#v; an enum key is a non-empty option id", v)
		}
	case resolverText:
		if s, ok := v.(string); !ok || s == "" {
			return fmt.Sprintf("is %#v; a text key is a non-empty string", v)
		}
	case resolverEnumList:
		ids, ok := v.([]string)
		if !ok || len(ids) == 0 || slices.Contains(ids, "") {
			return fmt.Sprintf("is %#v; an enum-list key is a non-empty array of non-empty ids", v)
		}
	default:
		return fmt.Sprintf("has no shape rule for resolver %q", r)
	}
	return ""
}

func TestRowValuesMatchTheirResolver(t *testing.T) {
	t.Run("the real table", func(t *testing.T) {
		for _, row := range table {
			if !row.send {
				continue
			}
			t.Run(rowID(row), func(t *testing.T) {
				for _, v := range settingValuesOf(t, row) {
					if fault := valueShapeFault(row.resolver, v.value); fault != "" {
						t.Errorf("%s's value %s %s", rowID(row), v.origin, fault)
					}
				}
			})
		}
	})

	t.Run("the check catches a mismatched declaration", func(t *testing.T) {
		for _, tc := range []struct {
			r resolver
			v any
		}{
			{resolverCapability, "concise"},
			{resolverSetting, true},
			{resolverSetting, map[string]any{"enabled": "yes"}},
			{resolverObject, true},
			{resolverSettingObject, "on"},
			{resolverEnv, true},
			{resolverEnv, "1"},
			{resolverEnvJSON, "true"},
			{resolverEnvJSON, map[string]any{"cascadeEnabled": true}},
			{resolverEnum, true},
			{resolverEnum, ""},
			{resolverText, true},
			{resolverText, ""},
			{resolverEnumList, "read-all"},
			{resolverEnumList, []string{}},
			{"", true},
		} {
			if valueShapeFault(tc.r, tc.v) == "" {
				t.Errorf("valueShapeFault(%q, %#v) = \"\", want a fault", tc.r, tc.v)
			}
		}
	})
}

// settingValue is one wire value a row can produce, with where it came from, so a failure names
// the state that produced it rather than just the row.
type settingValue struct {
	value  any
	origin string
	gated  bool
}

// engineStates and sessionStates are the gate inputs every sent row's values are read under: all off, all on, and
// every follow-kiro-cli choice explicitly off.
var (
	engineStates = []struct {
		name  string
		spawn EngineSpawn
	}{
		{"with every gate field off", EngineSpawn{}},
		{"with every gate field on", EngineSpawn{
			SecretStorage: true, Knowledge: true, InfraSafetyMonitor: "on",
			AutoRouting: "on", AutoDelegation: "on",
			TerminalCommandTimeoutMs: 300000, SteeringReminders: true,
			DisableTelemetry: true, ModelFallback: true,
		}},
		{"with every choice off", EngineSpawn{AutoRouting: "off", AutoDelegation: "off", InfraSafetyMonitor: "off"}},
	}
	sessionStates = []struct {
		name  string
		spawn SessionSpawn
	}{
		{"with every gate field off", SessionSpawn{}},
		{"with every gate field on", SessionSpawn{
			OverrideSessionID: "sess_00000000-0000-4000-8000-000000000000",
			Presets:           []string{"read-workspace"},
			SpecPlan:          "full", SpecAskClarification: true, WorkValidation: "on",
			MemoryMode: "read_write", MemoryReflection: true,
			Workflows: true, DisableAutoCompaction: true,
		}},
		{"with every choice off", SessionSpawn{WorkValidation: "off"}},
	}
)

// settingValuesOf returns every wire value a sent row can put on the wire: the compiled one, or
// the gate's answer in each state of its door's input.
func settingValuesOf(t *testing.T, row decl) []settingValue {
	t.Helper()
	var out []settingValue
	switch {
	case row.promptGate != nil:
		return promptValuesOf(t, row)
	case row.engineGate != nil:
		for _, st := range engineStates {
			if v, present := row.engineGate(&st.spawn); present {
				out = append(out, settingValue{value: v, origin: st.name, gated: true})
			}
		}
	case row.sessionGate != nil:
		for _, st := range sessionStates {
			if v, present := row.sessionGate(&st.spawn); present {
				out = append(out, settingValue{value: v, origin: st.name, gated: true})
			}
		}
	default:
		return []settingValue{{value: row.value, origin: "(compiled)", gated: false}}
	}
	if len(out) == 0 {
		t.Fatalf("%s is sent but its gate withholds in every state, so it can never reach the wire", rowID(row))
	}
	return out
}

func promptValuesOf(t *testing.T, row decl) []settingValue {
	t.Helper()
	var out []settingValue
	for _, st := range promptMatrix {
		if v, present := row.promptGate(&st.prompt); present {
			out = append(out, settingValue{value: v, origin: "for prompt " + st.name, gated: true})
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s is sent but its prompt gate withholds in every state, so it can never reach the wire", rowID(row))
	}
	return out
}

// No engine reads these two delegation keys, so no row may send or claim them.
func TestDeadDelegationSettingsAreNotDeclared(t *testing.T) {
	for _, row := range table {
		if row.key == "subagentOrchestration" || row.key == "inlineAgents" {
			t.Errorf("%s is declared; KAS has no reader for it", rowID(row))
		}
	}
}

// TestBuildersDoNotAliasTheTable pins that a caller cannot corrupt the table through a payload it
// was handed.
func TestBuildersDoNotAliasTheTable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func() map[string]any
		key   string
	}{
		{"connection", func() map[string]any { return Capabilities(&EngineSpawn{}) }, "codeIntelligence"},
		{"session", func() map[string]any { return SessionMeta(&SessionSpawn{}) }, "backgroundExecution"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := settingsOf(t, tc.build())
			entry, ok := first[tc.key].(map[string]any)
			if !ok {
				t.Fatalf("no %s settings object; got %T", tc.key, first[tc.key])
			}
			entry["enabled"] = false
			delete(first, tc.key)

			second := settingsOf(t, tc.build())
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
