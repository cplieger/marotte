package marotte

import "testing"

func TestKeyRun(t *testing.T) {
	for _, wf := range []string{"", "../x", "a/b"} {
		if _, ok := KeyRun(wf); ok {
			t.Errorf("KeyRun(%q) accepted", wf)
		}
		if _, ok := KeyRunTab(wf); ok {
			t.Errorf("KeyRunTab(%q) accepted", wf)
		}
	}
	run, ok := KeyRun("wf_0a1b")
	if !ok {
		t.Fatal("KeyRun(wf_0a1b) refused")
	}
	tab, _ := KeyRunTab("wf_0a1b")
	for key, want := range map[string]string{run: KeepKindRun, tab: KeepKindRunTab, KeyTab: KeepKindTab, KeyTurn: KeepKindTurn, "future": ""} {
		kind, wf := KeepKind(key)
		if kind != want {
			t.Errorf("KeepKind(%q) = %q, want %q", key, kind, want)
		}
		if (want == KeepKindRun || want == KeepKindRunTab) && wf != "wf_0a1b" {
			t.Errorf("KeepKind(%q) workflow = %q", key, wf)
		}
	}
	if kind, _ := KeepKind("run:../x"); kind != "" {
		t.Errorf("KeepKind of an invalid run key = %q, want unrecognised", kind)
	}
}
