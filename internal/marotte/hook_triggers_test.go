package marotte

import "testing"

// TestCanonicalHookTrigger pins the event-type -> PascalCase trigger map:
// canonical names pass through, v2/IDE camelCase aliases are rewritten
// (case-insensitively), and each resolves to the same trigger its canonical
// spelling does.
func TestCanonicalHookTrigger(t *testing.T) {
	cases := []struct{ in, want string }{
		// Canonical PascalCase passes through.
		{"SessionStart", "SessionStart"},
		{"PostFileSave", "PostFileSave"},
		{"Manual", "Manual"},
		// v2 / Kiro-IDE camelCase aliases map to PascalCase.
		{"fileEdited", "PostFileSave"},
		{"fileCreated", "PostFileCreate"},
		{"fileDeleted", "PostFileDelete"},
		{"userTriggered", "Manual"},
		{"agentStop", "Stop"},
		{"userPromptSubmit", "UserPromptSubmit"},
		// Case-insensitive + trimmed.
		{"POSTFILESAVE", "PostFileSave"},
		{"  fileEdited  ", "PostFileSave"},
		// KAS aliases.
		{"agentSpawn", "SessionStart"},
		{"AfterFileEdit", "PostFileSave"},
		// SessionEnd is canonical, never an alias of the per-turn Stop.
		{"SessionEnd", "SessionEnd"},
		{"sessionEnd", "SessionEnd"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := canonicalHookTrigger(tc.in)
			if !ok {
				t.Fatalf("canonicalHookTrigger(%q) reported unknown, want %q", tc.in, tc.want)
			}
			if got != tc.want {
				t.Errorf("canonicalHookTrigger(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestHookTriggerSubject_RejectsUnknown — KAS's parseHookDocument DROPS a hook
// whose trigger it does not recognise.
func TestHookTriggerSubject_RejectsUnknown(t *testing.T) {
	for _, in := range []string{"someFutureTrigger", "  x  ", "", "PostFileSaved!"} {
		if got, ok := HookTriggerSubject(in); ok {
			t.Errorf("HookTriggerSubject(%q) = (%q, true), want it reported unknown", in, got)
		}
	}
}

// TestEveryCanonicalTriggerHasASubject is what keeps ClassifyHookMatcher TOTAL.
//
// The classifier switches on the subject and reports nothing for a value it does
// not recognise, so a trigger added to the map with a zero-value subject would
// silently opt out of both diagnostics — the exact shape of failure the
// diagnostics exist to remove. The set is closed and small, so listing the
// expected partition here rather than deriving it is deliberate: a derived
// expectation would agree with any map, including a wrong one.
func TestEveryCanonicalTriggerHasASubject(t *testing.T) {
	want := map[string]HookMatcherSubject{
		"PreToolUse":       HookMatcherSubjectToolName,
		"PostToolUse":      HookMatcherSubjectToolName,
		"PostFileCreate":   hookMatcherSubjectFilePath,
		"PostFileSave":     hookMatcherSubjectFilePath,
		"PostFileDelete":   hookMatcherSubjectFilePath,
		"SessionStart":     HookMatcherSubjectNone,
		"SessionEnd":       HookMatcherSubjectNone,
		"Stop":             HookMatcherSubjectNone,
		"UserPromptSubmit": HookMatcherSubjectNone,
		"PreTaskExec":      HookMatcherSubjectNone,
		"PostTaskExec":     HookMatcherSubjectNone,
		"Manual":           HookMatcherSubjectNone,
	}

	for name, subject := range triggerSubjects {
		if w, ok := want[name]; !ok || subject != w {
			t.Errorf("trigger %s has the subject %q, want %q (absent from this test: %v)", name, subject, w, !ok)
		}
	}
	for name := range want {
		if _, ok := triggerSubjects[name]; !ok {
			t.Errorf("trigger %s is expected in the table and absent from it", name)
		}
	}
	// Every spelling names a canonical trigger, which is what makes an alias a spelling rather
	// than a second trigger with no subject.
	for alias, name := range hookTriggerNames {
		if _, ok := triggerSubjects[name]; !ok {
			t.Errorf("alias %q maps to trigger %q, which has no subject; "+
				"add it with its subject or ClassifyHookMatcher will silently ignore it", alias, name)
		}
	}
}

// TestClassifyHookMatcher covers the pairing rule in both directions plus the
// cases that must report NOTHING, because a false positive badges a hook the user
// wrote correctly.
func TestClassifyHookMatcher(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trigger string
		matcher string
		want    HookMatcherDefect
	}{
		// none-subject + a matcher: ignored upstream.
		{"session start with a matcher", "SessionStart", `\.go$`, hookMatcherIneffective},
		{"stop with a matcher", "Stop", "anything", hookMatcherIneffective},
		{"manual with a matcher", "Manual", "x", hookMatcherIneffective},
		{"session end with a matcher", "SessionEnd", "x", hookMatcherIneffective},
		// The alias spelling has to reach the same verdict, or the check is
		// bypassable by writing the trigger differently.
		{"an alias reaches the same verdict", "userTriggered", "x", hookMatcherIneffective},
		// Whitespace is not a matcher.
		{"whitespace is not a matcher", "SessionStart", "   ", hookMatcherOK},
		{"session start with no matcher", "SessionStart", "", hookMatcherOK},
		// toolName-subject with no matcher: runs on every tool call.
		{"pre tool use with no matcher", "PreToolUse", "", hookMatcherMissingToolName},
		{"post tool use with no matcher", "PostToolUse", "  ", hookMatcherMissingToolName},
		{"pre tool use with a matcher", "PreToolUse", "fsWrite", hookMatcherOK},
		// KAS owns the tool-matcher grammar: a tag, an alias or a glob that is not a
		// valid regex still matches tools, so none of them may read as a defect.
		{"a tag is a tool matcher", "PreToolUse", "shell", hookMatcherOK},
		{"a glob that is not a regex", "PostToolUse", "*", hookMatcherOK},
		{"an mcp tag", "PreToolUse", "@mcp", hookMatcherOK},
		{"a tag on a none-subject trigger", "Stop", "shell", hookMatcherIneffective},
		// filePath-subject: effective either way, so neither direction reports.
		{"file save with a matcher", "PostFileSave", `\.go$`, hookMatcherOK},
		{"file save with no matcher", "PostFileSave", "", hookMatcherOK},
		// An unknown trigger is a different and larger defect: KAS drops the hook.
		{"unknown trigger reports nothing", "someFutureTrigger", "x", hookMatcherOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyHookMatcher(tc.trigger, tc.matcher); got != tc.want {
				t.Errorf("ClassifyHookMatcher(%q, %q) = %q, want %q", tc.trigger, tc.matcher, got, tc.want)
			}
		})
	}
}
