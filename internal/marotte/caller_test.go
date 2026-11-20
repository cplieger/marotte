package marotte

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCaller_SentBy(t *testing.T) {
	cases := []struct {
		name   string
		caller Caller
		want   string
	}{
		{name: "labelled", caller: Caller{Label: "Agent Mail"}, want: "Sent by Agent Mail"},
		{name: "unlabelled", caller: Caller{}, want: "Sent by an API caller"},
		{name: "a rule", caller: ruleCaller("Weekly questions", "ev-1"), want: "Rule: Weekly questions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.caller.SentBy(); got != tc.want {
				t.Errorf("SentBy = %q, want %q", got, tc.want)
			}
		})
	}
}

// One sender's later message replaces its waiting one: an outside caller that labels itself "marotte" is not
// marotte, so neither replaces the other.
func TestCaller_SameSender(t *testing.T) {
	marotteCaller := Caller{Kind: CallerKindMarotte, Label: "marotte"}
	weekly := ruleCaller("Weekly questions", "ev-1")
	cases := []struct {
		name string
		a, b Caller
		want bool
	}{
		{name: "one API label", a: Caller{Label: "Agent Mail"}, b: Caller{Label: "Agent Mail"}, want: true},
		{name: "two API labels", a: Caller{Label: "Agent Mail"}, b: Caller{Label: "cron"}, want: false},
		{name: "marotte and marotte", a: marotteCaller, b: Caller{Kind: CallerKindMarotte, Label: "marotte"}, want: true},
		{name: "marotte and an API caller named marotte", a: marotteCaller, b: Caller{Label: "marotte"}, want: false},
		{name: "an API caller named marotte and marotte", a: Caller{Label: "marotte"}, b: marotteCaller, want: false},
		{name: "one rule event under two names", a: weekly, b: ruleCaller("Renamed", "ev-1"), want: true},
		{name: "one rule, two events", a: weekly, b: ruleCaller("Weekly questions", "ev-2"), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.SameSender(&tc.b); got != tc.want {
				t.Errorf("%+v.SameSender(%+v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func ruleCaller(label, event string) Caller {
	return Caller{Kind: CallerKindRule, Label: label, Rule: "r-weekly", Event: event, Depth: 2}
}

// A joined message is a caller's only when no part is the user's: one part of the user's keeps it the user's, and
// callers that differ in anything join as an unnamed API caller.
func TestSharedCaller(t *testing.T) {
	mail, other := &Caller{Label: "Agent Mail"}, &Caller{Label: "cron"}
	weekly := ruleCaller("Weekly questions", "ev-1")
	otherEvent := ruleCaller("Weekly questions", "ev-2")
	cases := []struct {
		name  string
		parts []*Caller
		want  *Caller
	}{
		{name: "no parts", parts: nil, want: nil},
		{name: "one caller throughout", parts: []*Caller{mail, {Label: "Agent Mail"}}, want: mail},
		{name: "an unlabelled caller throughout", parts: []*Caller{{}, {}}, want: &Caller{}},
		{name: "the user's words among them", parts: []*Caller{mail, nil}, want: nil},
		{name: "the user's words first", parts: []*Caller{nil, mail}, want: nil},
		{name: "two different callers", parts: []*Caller{mail, other}, want: &Caller{}},
		{name: "a labelled and an unlabelled caller", parts: []*Caller{mail, {}}, want: &Caller{}},
		{name: "different callers, then the user's words", parts: []*Caller{mail, other, nil}, want: nil},
		{name: "one rule caller throughout", parts: []*Caller{&weekly, new(ruleCaller("Weekly questions", "ev-1"))}, want: &weekly},
		{name: "one rule, two events", parts: []*Caller{&weekly, &otherEvent}, want: &Caller{}},
		{name: "a rule and an API caller of its label", parts: []*Caller{&weekly, {Label: "Weekly questions"}}, want: &Caller{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SharedCaller(tc.parts)
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Errorf("SharedCaller = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// A rule's caller is the transcript's only record of which rule and event opened a turn, so every place a caller is
// written keeps all of it through a reload.
func TestCaller_ARuleCallerRoundTripsWhereverACallerIsStored(t *testing.T) {
	caller := ruleCaller("Weekly questions", "every:t1:1700000000000")
	want := `"caller":{"kind":"rule","label":"Weekly questions","rule":"r-weekly","event":"every:t1:1700000000000","depth":2}`
	rows := []struct {
		name string
		row  any
		back func(raw []byte) (*Caller, error)
	}{
		{name: "a turn's prompt", row: EntryPrompt{Caller: &caller, ID: "m1", Text: "hi"}, back: callerOf[EntryPrompt](func(p EntryPrompt) *Caller { return p.Caller })},
		{name: "a steer row", row: EntrySteer{Caller: &caller, Text: "hi", Origin: SteerOriginUser}, back: callerOf[EntrySteer](func(p EntrySteer) *Caller { return p.Caller })},
		{name: "a queued row", row: QueuedPrompt{Caller: &caller, ID: "m1", Text: "hi"}, back: callerOf[QueuedPrompt](func(p QueuedPrompt) *Caller { return p.Caller })},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.row)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !bytes.Contains(raw, []byte(want)) {
				t.Errorf("stored row = %s, want it to carry %s", raw, want)
			}
			got, err := tc.back(raw)
			if err != nil {
				t.Fatalf("unmarshal %s: %v", raw, err)
			}
			if got == nil || *got != caller {
				t.Errorf("reloaded caller = %+v, want %+v", got, caller)
			}
		})
	}
}

// An API caller's stored caller is unchanged by the rule fields: none of them is written for it.
func TestCaller_AnAPICallerStoresOnlyItsLabel(t *testing.T) {
	raw, err := json.Marshal(Caller{Label: "Agent Mail"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != `{"label":"Agent Mail"}` {
		t.Errorf("API caller = %s, want {\"label\":\"Agent Mail\"}", raw)
	}
}

func callerOf[T any](pick func(T) *Caller) func([]byte) (*Caller, error) {
	return func(raw []byte) (*Caller, error) {
		var v T
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		return pick(v), nil
	}
}

// No client can state a caller, a rule's least of all: the command envelope's From and a prompt's Caller are set only
// in process, so a body naming either decodes to none.
func TestCaller_ADecodedClientCommandSetsNoCaller(t *testing.T) {
	var cmd ClientCommand
	body := `{"type":"prompt","chat_id":"c1","From":{"kind":"rule","rule":"r1"},"from":{"kind":"rule"},` +
		`"payload":{"text":"hi","message_id":"m1","caller":{"kind":"rule","rule":"r1","event":"e1"},"Caller":{"kind":"rule"}}}`
	if err := json.Unmarshal([]byte(body), &cmd); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if cmd.From != nil {
		t.Errorf("a decoded envelope set From = %+v", cmd.From)
	}
	var prompt PromptCommand
	if err := json.Unmarshal(cmd.Payload, &prompt); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if prompt.Caller != nil {
		t.Errorf("a decoded prompt set Caller = %+v", prompt.Caller)
	}
}
