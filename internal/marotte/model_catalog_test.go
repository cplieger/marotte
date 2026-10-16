package marotte

import (
	"slices"
	"testing"
)

func TestApplyServedModels(t *testing.T) {
	deprecated := SessionModel{ID: "old", Name: "Old", Description: "[Deprecated]"}
	fresh := SessionModel{ID: "new", Name: "New"}
	chat := &Chat{ServedModelIDs: []string{"seed"}}

	if ApplyServedModels(chat, nil) {
		t.Error("ApplyServedModels(nil) changed the chat, want keep-on-absent")
	}
	if !slices.Equal(chat.ServedModelIDs, []string{"seed"}) {
		t.Errorf("served after nil = %v, want [seed]", chat.ServedModelIDs)
	}

	if !ApplyServedModels(chat, []SessionModel{deprecated, fresh}) {
		t.Error("ApplyServedModels(new catalog) reported unchanged")
	}
	// The deprecated id stays: this is the ENTITLEMENT set, and filtering it would
	// refuse a model the account can still run.
	if !slices.Equal(chat.ServedModelIDs, []string{"old", "new"}) {
		t.Errorf("served = %v, want [old new] including deprecated", chat.ServedModelIDs)
	}
	if ApplyServedModels(chat, []SessionModel{deprecated, fresh}) {
		t.Error("ApplyServedModels(repeat) reported changed")
	}
	if !ApplyServedModels(chat, []SessionModel{fresh}) {
		t.Error("ApplyServedModels(dropped id) reported unchanged")
	}
	if !slices.Equal(chat.ServedModelIDs, []string{"new"}) {
		t.Errorf("served = %v, want [new]", chat.ServedModelIDs)
	}
}

func TestColdCatalogPin(t *testing.T) {
	for name, tc := range map[string]struct {
		current string
		choices []string
		want    bool
	}{
		"the saved model alone":         {"opus", []string{"opus"}, true},
		"one choice naming another":     {"opus", []string{"fable"}, false},
		"a catalog holding the current": {"opus", []string{"auto", "opus"}, false},
		"no current value":              {"", []string{""}, false},
		"no choices":                    {"opus", nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := ColdCatalogPin(tc.current, tc.choices); got != tc.want {
				t.Errorf("ColdCatalogPin(%q, %v) = %v, want %v", tc.current, tc.choices, got, tc.want)
			}
		})
	}
}
