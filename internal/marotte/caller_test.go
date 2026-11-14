package marotte

import "testing"

func TestCaller_SentBy(t *testing.T) {
	if got := (&Caller{Label: "Agent Mail"}).SentBy(); got != "Sent by Agent Mail" {
		t.Errorf("labelled SentBy = %q, want %q", got, "Sent by Agent Mail")
	}
	if got := (&Caller{}).SentBy(); got != "Sent by an API caller" {
		t.Errorf("unlabelled SentBy = %q, want %q", got, "Sent by an API caller")
	}
}

// A joined message is a caller's only when no part is the user's: one part of the user's keeps it the user's, and
// callers that differ join as an unnamed API caller.
func TestSharedCaller(t *testing.T) {
	mail, other := &Caller{Label: "Agent Mail"}, &Caller{Label: "cron"}
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SharedCaller(tc.parts)
			if (got == nil) != (tc.want == nil) || (got != nil && got.Label != tc.want.Label) {
				t.Errorf("SharedCaller = %+v, want %+v", got, tc.want)
			}
		})
	}
}
