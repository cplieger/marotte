package tabs

import (
	"fmt"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func owedRefs(owed []OwedRelease) []string {
	out := make([]string, 0, len(owed))
	for _, e := range owed {
		out = append(out, e.Ref)
	}
	slices.Sort(out)
	return out
}

func TestClose_OwesTheTabReleaseOfEveryClosedChat(t *testing.T) {
	s, dir := newTestStore(t)
	parent := mustOpen(t, s, chatSpec("c-parent"))
	mustOpen(t, s, marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-fork", Parent: parent.ID})
	mustOpen(t, s, marotte.OpenTab{Kind: marotte.TabKindEditor, Ref: "/w/a.ts", Parent: parent.ID})
	mustOpen(t, s, chatSpec("c-other"))

	_, owed, version, err := s.Close(t.Context(), parent.ID)
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := owedRefs(owed); !slices.Equal(got, []string{"c-fork", "c-parent"}) {
		t.Errorf("Close(parent) owed %v, want the parent and the fork, never the editor or the open chat", got)
	}
	for _, e := range owed {
		if e.ID == "" || e.ClosedAt == 0 || e.Kind != marotte.TabKindChat {
			t.Errorf("owed entry %+v, want an id, a close time and the chat kind", e)
		}
	}
	doc := onDisk(t, dir)
	if got := owedRefs(doc.Owed); !slices.Equal(got, []string{"c-fork", "c-parent"}) || doc.Version != version {
		t.Errorf("tabs.json owed %v at version %d, want both entries in the close's own write (version %d)", got, doc.Version, version)
	}

	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := owedRefs(reopened.Owed()); !slices.Equal(got, []string{"c-fork", "c-parent"}) {
		t.Errorf("Owed() after a restart = %v, want the entries the close wrote", got)
	}
}

func TestSettle_RemovesOnlyTheNamedEntriesAndBumpsNoVersion(t *testing.T) {
	s, dir := newTestStore(t)
	a := mustOpen(t, s, chatSpec("c-a"))
	b := mustOpen(t, s, chatSpec("c-b"))
	_, owedA, _, err := s.Close(t.Context(), a.ID)
	if err != nil {
		t.Fatalf("close a: %v", err)
	}
	if _, _, _, err := s.Close(t.Context(), b.ID); err != nil {
		t.Fatalf("close b: %v", err)
	}
	_, before := s.List()

	if err := s.Settle(t.Context(), []string{owedA[0].ID}); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if got := owedRefs(s.Owed()); !slices.Equal(got, []string{"c-b"}) {
		t.Errorf("Owed() after settling a = %v, want only b", got)
	}
	if _, after := s.List(); after != before {
		t.Errorf("Settle moved the version %d -> %d, want no bump: the client renders none of it", before, after)
	}
	if got := owedRefs(onDisk(t, dir).Owed); !slices.Equal(got, []string{"c-b"}) {
		t.Errorf("tabs.json owed %v after Settle, want only b", got)
	}
}

func TestNewStore_DropsInvalidOwedEntriesAndCapsTheList(t *testing.T) {
	cases := []struct {
		name string
		in   []OwedRelease
		want []string
	}{
		{name: "valid entry kept", in: []OwedRelease{{ID: "e1", Kind: marotte.TabKindChat, Ref: "c-a"}}, want: []string{"c-a"}},
		{name: "empty id dropped", in: []OwedRelease{{Kind: marotte.TabKindChat, Ref: "c-a"}}, want: []string{}},
		{name: "invalid ref dropped", in: []OwedRelease{{ID: "e1", Kind: marotte.TabKindChat, Ref: "../x"}}, want: []string{}},
		{name: "invalid holder dropped", in: []OwedRelease{{ID: "e1", Kind: marotte.TabKindRun, Ref: "wf-1", Holders: []string{"c/x"}}}, want: []string{}},
		{name: "other kind dropped", in: []OwedRelease{{ID: "e1", Kind: marotte.TabKindEditor, Ref: "c-a"}}, want: []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeDoc(t, dir, file{Owed: tc.in})
			s, err := NewStore(dir)
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			if got := owedRefs(s.Owed()); !slices.Equal(got, tc.want) {
				t.Errorf("Owed() = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("cap drops the oldest", func(t *testing.T) {
		dir := t.TempDir()
		in := make([]OwedRelease, 0, maxOwed+2)
		for i := range maxOwed + 2 {
			in = append(in, OwedRelease{ID: fmt.Sprintf("e%d", i), Kind: marotte.TabKindChat, Ref: fmt.Sprintf("c-%d", i)})
		}
		writeDoc(t, dir, file{Owed: in})
		s, err := NewStore(dir)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		owed := s.Owed()
		if len(owed) != maxOwed || owed[0].ID != "e2" {
			t.Errorf("Owed() = %d entries starting at %q, want %d starting at e2", len(owed), owed[0].ID, maxOwed)
		}
	})
}
