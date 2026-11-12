package marotte_test

// The tab vocabulary is the half of the tab collection that crosses the wire, so
// these tests pin two things a Go compiler cannot: the kind SET (a ninth member
// reaches every client's per-kind factory as a switch with no case for it) and the
// JSON KEYS (the client decodes them by name, and a renamed tag is a field that
// silently arrives undefined).

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestTabKind_ValidAndSingletonAgreeOnEveryMember walks the whole vocabulary in
// one table, because Valid and Singleton are two readings of one map and the
// failure mode of them disagreeing is not an error: it is Settings opening twice,
// or a chat tab accepted with no chat behind it.
func TestTabKind_ValidAndSingletonAgreeOnEveryMember(t *testing.T) {
	cases := []struct {
		desc          string
		kind          marotte.TabKind
		wantValid     bool
		wantSingleton bool
	}{
		{desc: "chat, one tab per conversation", kind: marotte.TabKindChat, wantValid: true},
		{desc: "editor, one tab per absolute path", kind: marotte.TabKindEditor, wantValid: true},
		{desc: "run, one tab per workflow run", kind: marotte.TabKindRun, wantValid: true},
		{desc: "settings", kind: marotte.TabKindSettings, wantValid: true, wantSingleton: true},
		{desc: "git", kind: marotte.TabKindGit, wantValid: true, wantSingleton: true},
		{desc: "files, one tab per folder the browser was opened at", kind: marotte.TabKindFiles, wantValid: true},
		{desc: "history", kind: marotte.TabKindHistory, wantValid: true, wantSingleton: true},
		{desc: "docs", kind: marotte.TabKindDocs, wantValid: true, wantSingleton: true},
		{desc: "subagent, one tab per delegate execution", kind: marotte.TabKindSubagent, wantValid: true},
		{desc: "spec, one tab per spec directory", kind: marotte.TabKindSpec, wantValid: true},
		{desc: "web, one tab per previewed page", kind: marotte.TabKindWeb, wantValid: true},
		{desc: "plan, deleted from the client on 2026-08-25 and deliberately absent here", kind: "plan"},
		{desc: "the empty kind, which is what a payload with no kind field decodes to", kind: ""},
		{desc: "a kind from some other vocabulary", kind: "vibe"},
		{desc: "a kind differing only in case", kind: "Chat"},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.desc, " ", "-"), func(t *testing.T) {
			if got := tc.kind.Valid(); got != tc.wantValid {
				t.Errorf("TabKind(%q).Valid() = %v, want %v (%s)", tc.kind, got, tc.wantValid, tc.desc)
			}
			if got := tc.kind.Singleton(); got != tc.wantSingleton {
				t.Errorf("TabKind(%q).Singleton() = %v, want %v (%s)", tc.kind, got, tc.wantSingleton, tc.desc)
			}
		})
	}
}

// TestTabKind_TheSetIsExactlyEleven is the guard on the set's SIZE, which the table
// above cannot give: a twelfth member added to the map and forgotten there would
// leave every existing case passing. A new kind is a cross-language change (the
// client's TabKind union, its icon table and its per-kind factory), so it should
// fail here first.
func TestTabKind_TheSetIsExactlyEleven(t *testing.T) {
	all := []marotte.TabKind{
		marotte.TabKindChat, marotte.TabKindEditor, marotte.TabKindRun,
		marotte.TabKindSubagent, marotte.TabKindSettings, marotte.TabKindGit,
		marotte.TabKindFiles, marotte.TabKindHistory, marotte.TabKindDocs,
		marotte.TabKindSpec, marotte.TabKindWeb,
	}
	valid := 0
	seen := make([]string, 0, len(all))
	for _, k := range all {
		if k.Valid() {
			valid++
		}
		seen = append(seen, string(k))
	}
	if valid != len(all) {
		t.Errorf("%d of the %d declared kinds are Valid, want all of them", valid, len(all))
	}
	slices.Sort(seen)
	want := []string{"chat", "docs", "editor", "files", "git", "history", "run", "settings", "spec", "subagent", "web"}
	if !slices.Equal(seen, want) {
		t.Errorf("the declared kinds are %v, want %v; a change here is a change to the client's TabKind union", seen, want)
	}
}

// TestTabSubject_WireKeys pins the JSON contract by name. The client decodes a
// subject field by field, so a renamed tag arrives as undefined rather than as an
// error — and Parent and Ref both carry meaning that cannot be re-derived.
//
// Every field is present with no omitempty on purpose: a subject is a complete
// record of one tab, and an absent "parent" would be indistinguishable from a tab
// whose parent the encoder dropped.
func TestTabSubject_WireKeys(t *testing.T) {
	data, err := json.Marshal(marotte.TabSubject{
		ID:     "a3f1",
		Kind:   marotte.TabKindRun,
		Ref:    "wf-1",
		Parent: "b7c2",
		Pinned: true,
		Owns:   true,
	})
	if err != nil {
		t.Fatalf("Marshal(TabSubject): %v", err)
	}
	want := `{"id":"a3f1","kind":"run","ref":"wf-1","parent":"b7c2","pinned":true,"owns":true}`
	if got := string(data); got != want {
		t.Errorf("Marshal(TabSubject) = %s, want %s", got, want)
	}

	var back marotte.TabSubject
	if err := json.Unmarshal([]byte(want), &back); err != nil {
		t.Fatalf("Unmarshal(%s): %v", want, err)
	}
	if back.Kind != marotte.TabKindRun || back.Ref != "wf-1" || back.Parent != "b7c2" || !back.Owns {
		t.Errorf("Unmarshal(%s) = %+v, want the fields round-tripped", want, back)
	}
}

// TestTabSubject_ZeroValueEncodesEveryField is the same claim from the other side:
// a fresh singleton tab has no ref and no parent, and its record still names them,
// so a client's decoder never has to guess whether a key was dropped or was empty.
func TestTabSubject_ZeroValueEncodesEveryField(t *testing.T) {
	data, err := json.Marshal(marotte.TabSubject{ID: "x", Kind: marotte.TabKindSettings})
	if err != nil {
		t.Fatalf("Marshal(TabSubject): %v", err)
	}
	want := `{"id":"x","kind":"settings","ref":"","parent":"","pinned":false,"owns":false}`
	if got := string(data); got != want {
		t.Errorf("Marshal(a singleton subject) = %s, want %s", got, want)
	}
}

// TestRecentClose_WireKeys pins the Recently closed row's keys: the client draws the name and
// sorts by closed_at, and a pinned entry is the one a reopen returns to the Pinned section.
func TestRecentClose_WireKeys(t *testing.T) {
	cases := []struct {
		desc  string
		entry marotte.RecentClose
		want  string
	}{
		{
			desc:  "a pinned chat with its name",
			entry: marotte.RecentClose{Ref: "c-1", Name: "Alpha", ClosedAt: 1700000000000, Pinned: true},
			want:  `{"ref":"c-1","name":"Alpha","closed_at":1700000000000,"pinned":true}`,
		},
		{
			desc:  "an unpinned chat whose record could not be read",
			entry: marotte.RecentClose{Ref: "c-2", ClosedAt: 1},
			want:  `{"ref":"c-2","name":"","closed_at":1}`,
		},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.desc, " ", "-"), func(t *testing.T) {
			data, err := json.Marshal(tc.entry)
			if err != nil {
				t.Fatalf("Marshal(%+v): %v", tc.entry, err)
			}
			if got := string(data); got != tc.want {
				t.Errorf("Marshal(%s) = %s, want %s", tc.desc, got, tc.want)
			}
		})
	}
}

// wireKeys marshals v and returns its top-level keys with their raw values, so a test reads a key
// by name whatever order the struct declares its fields in.
func wireKeys(t *testing.T, v any) map[string]string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal(%+v): %v", v, err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal(%s): %v", data, err)
	}
	out := make(map[string]string, len(raw))
	for k, val := range raw {
		out[k] = string(val)
	}
	return out
}

// TestTabsChangedPayload_RecentIsAbsentWhenUnchangedAndAnObjectWhenEmptied pins the frame's
// three states: no key means the list did not change, which is a different fact from a list
// that emptied, so an emptied list is sent as {"items":[]} and never as null or an omission.
func TestTabsChangedPayload_RecentIsAbsentWhenUnchangedAndAnObjectWhenEmptied(t *testing.T) {
	cases := []struct {
		desc       string
		payload    marotte.TabsChangedPayload
		wantRecent string // "" means the key is absent
	}{
		{
			desc:    "a reorder, which leaves the list alone",
			payload: marotte.TabsChangedPayload{Order: []string{"t1"}, Version: 3},
		},
		{
			desc:       "a reopen that emptied the list",
			payload:    marotte.TabsChangedPayload{Recent: &marotte.RecentList{Items: []marotte.RecentClose{}}, Version: 4},
			wantRecent: `{"items":[]}`,
		},
		{
			desc: "a close that listed a chat",
			payload: marotte.TabsChangedPayload{
				RemovedIDs: []string{"t1"},
				Recent:     &marotte.RecentList{Items: []marotte.RecentClose{{Ref: "c-1", Name: "Alpha", ClosedAt: 5}}},
				Version:    5,
			},
			wantRecent: `{"items":[{"ref":"c-1","name":"Alpha","closed_at":5}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.desc, " ", "-"), func(t *testing.T) {
			got, present := wireKeys(t, tc.payload)["recent"]
			if tc.wantRecent == "" {
				if present {
					t.Errorf("%s: recent = %s, want the key absent", tc.desc, got)
				}
				return
			}
			if got != tc.wantRecent {
				t.Errorf("%s: recent = %q, want %s", tc.desc, got, tc.wantRecent)
			}
		})
	}
}

// TestTabList_RecentIsNeverOmitted pins the GET /api/tabs key: the generated decoder expects an
// array, so an empty list is [] on the wire.
func TestTabList_RecentIsNeverOmitted(t *testing.T) {
	keys := wireKeys(t, marotte.TabList{Tabs: []marotte.TabSubject{}, Recent: []marotte.RecentClose{}})
	if got, ok := keys["recent"]; !ok || got != "[]" {
		t.Errorf("recent = %q (present %v), want []", got, ok)
	}
	full := wireKeys(t, marotte.TabList{Recent: []marotte.RecentClose{{Ref: "c-1", Name: "Alpha", ClosedAt: 9, Pinned: true}}})
	if want := `[{"ref":"c-1","name":"Alpha","closed_at":9,"pinned":true}]`; full["recent"] != want {
		t.Errorf("recent = %s, want %s", full["recent"], want)
	}
}

// TestOpenTab_WireKeys covers the command payload's half. Here the optional fields
// ARE omitted, because the spec is what a client ASKS for: a chat open naming a
// parent it does not have would otherwise send parent:"" and read as a deliberate
// top-level placement, which is the same value but a different statement.
func TestOpenTab_WireKeys(t *testing.T) {
	cases := []struct {
		desc string
		spec marotte.OpenTab
		want string
	}{
		{
			desc: "a chat open, the commonest one",
			spec: marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-1"},
			want: `{"kind":"chat","ref":"c-1"}`,
		},
		{
			desc: "a singleton, which carries nothing else",
			spec: marotte.OpenTab{Kind: marotte.TabKindDocs},
			want: `{"kind":"docs"}`,
		},
		{
			desc: "a launcher-owned run sub-tab, the case Owns exists for",
			spec: marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf-1", Parent: "b7c2", Owns: true},
			want: `{"kind":"run","ref":"wf-1","parent":"b7c2","owns":true}`,
		},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.desc, " ", "-"), func(t *testing.T) {
			data, err := json.Marshal(tc.spec)
			if err != nil {
				t.Fatalf("Marshal(%+v): %v", tc.spec, err)
			}
			if got := string(data); got != tc.want {
				t.Errorf("Marshal(%s) = %s, want %s", tc.desc, got, tc.want)
			}
			var back marotte.OpenTab
			if err := json.Unmarshal(data, &back); err != nil {
				t.Fatalf("Unmarshal(%s): %v", data, err)
			}
			if !reflect.DeepEqual(back, tc.spec) {
				t.Errorf("round trip of %s = %+v, want %+v", tc.desc, back, tc.spec)
			}
		})
	}
}
