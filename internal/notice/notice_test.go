package notice

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cplieger/marotte/internal/marotte"
)

func TestBuilders_titleIsTheTabAndBodyIsPhraseThenDetail(t *testing.T) {
	chat := ChatTarget("c1", "Fix the login bug")
	run := RunTarget("wf1", "nightly · scheduled")
	tests := []struct {
		name string
		got  marotte.NotificationPayload
		want marotte.NotificationPayload
	}{
		{"turn finished with focus", TurnFinished(chat, "Refactored the auth handler"), marotte.NotificationPayload{
			PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindAgentFinished,
			Title: "Fix the login bug", Body: "Response complete · Refactored the auth handler",
		}},
		{"turn finished without focus", TurnFinished(chat, "  "), marotte.NotificationPayload{
			PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindAgentFinished,
			Title: "Fix the login bug", Body: "Response complete",
		}},
		{"turn failed", TurnFailed(chat, "The model refused the request."), marotte.NotificationPayload{
			PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindAgentFinished,
			Title: "Fix the login bug", Body: "Turn failed · The model refused the request.",
		}},
		{"tool permission", Permission(chat, "", "rm -rf build", 0), marotte.NotificationPayload{
			PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindPermission,
			Title: "Fix the login bug", Body: "Permission required · rm -rf build",
		}},
		{"turn approval of one file", Permission(chat, "", "ignored", 1), marotte.NotificationPayload{
			PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindPermission,
			Title: "Fix the login bug", Body: "Permission required · Review 1 changed file",
		}},
		{"turn approval of several files", Permission(chat, "", "", 3), marotte.NotificationPayload{
			PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindPermission,
			Title: "Fix the login bug", Body: "Permission required · Review 3 changed files",
		}},
		{"step permission", Permission(run, "builder", "git push", 0), marotte.NotificationPayload{
			PushSubject: marotte.RunSubject("wf1"), Kind: marotte.PushKindPermission,
			Title: "nightly · scheduled", Body: "Permission required · builder: git push",
		}},
		{"chat question", Question(chat, "", "Which branch?"), marotte.NotificationPayload{
			PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindPermission,
			Title: "Fix the login bug", Body: "Input required · Which branch?",
		}},
		{"step question with no text", Question(run, "reviewer", ""), marotte.NotificationPayload{
			PushSubject: marotte.RunSubject("wf1"), Kind: marotte.PushKindPermission,
			Title: "nightly · scheduled", Body: "Input required · reviewer",
		}},
		{"pr flip", PRStatus(marotte.PRSubject("github:github.com", "r1", 7), "acme/web", 7, "Add login", true), marotte.NotificationPayload{
			PushSubject: marotte.PRSubject("github:github.com", "r1", 7), Kind: marotte.PushKindPRStatus,
			Title: "acme/web #7", Body: "Checks passed · Add login",
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("got %+v, want %+v", tc.got, tc.want)
			}
		})
	}
}

func TestTargets_unnamedTabsReadAsTheirDefaultLabels(t *testing.T) {
	if got := ChatTarget("c1", "").Name; got != marotte.DefaultChatName {
		t.Errorf("ChatTarget(c1, \"\").Name = %q, want %q", got, marotte.DefaultChatName)
	}
	if got := RunTarget("wf1", " ").Name; got != fallbackRunName {
		t.Errorf("RunTarget(wf1, \" \").Name = %q, want %q", got, fallbackRunName)
	}
}

func TestRunEnded(t *testing.T) {
	run := RunTarget("wf1", "deploy")
	tests := []struct {
		name               string
		status             marotte.RunStatus
		parent, stop, kind string
		wantBody           string
		wantOK             bool
	}{
		{name: "completed parentless", status: marotte.RunStatusCompleted, wantBody: "Workflow completed", wantOK: true},
		{name: "completed from a chat", status: marotte.RunStatusCompleted, parent: "Ship it", wantBody: "Workflow completed · from Ship it", wantOK: true},
		{name: "failed", status: marotte.RunStatusFailed, wantBody: "Workflow failed", wantOK: true},
		{name: "aborted with a stop reason", status: marotte.RunStatusAborted, parent: "Ship it", stop: "operator stop", wantBody: "Workflow aborted · operator stop", wantOK: true},
		{name: "cancelled", status: marotte.RunStatusCancelled, wantBody: "Workflow cancelled", wantOK: true},
		{name: "parentless pause at the iteration limit", status: marotte.RunStatusPaused, kind: "maxIterations", wantBody: "Workflow paused · iteration limit reached", wantOK: true},
		{name: "pause of a run whose parent agent intervenes", status: marotte.RunStatusPaused, parent: "Ship it", kind: "maxIterations"},
		{name: "parentless pause for a step's input", status: marotte.RunStatusPaused, kind: "stepInput"},
		{name: "parentless pause with no kind", status: marotte.RunStatusPaused},
		{name: "running", status: marotte.RunStatusRunning},
		{name: "an unknown status", status: "exploded"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n, ok := RunEnded(run, tc.status, tc.parent, tc.stop, tc.kind)
			if ok != tc.wantOK {
				t.Fatalf("RunEnded(%q, parent %q) ok = %v, want %v", tc.status, tc.parent, ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if n.Body != tc.wantBody || n.Title != "deploy" || n.Kind != marotte.PushKindRunOutcome || n.PushSubject != marotte.RunSubject("wf1") {
				t.Errorf("RunEnded(%q) = %+v, want body %q under run wf1", tc.status, n, tc.wantBody)
			}
		})
	}
}

func TestAskSubject_aStepAskIsTheRunsWhicheverBridgeItTravelledOn(t *testing.T) {
	tests := []struct {
		chatID marotte.ChatID
		runID  string
		want   marotte.PushSubject
	}{
		{"c1", "", marotte.ChatSubject("c1")},
		{"c1", "wf1", marotte.RunSubject("wf1")},
		{"run:wf2", "", marotte.RunSubject("wf2")},
		{"run:wf2", "wf3", marotte.RunSubject("wf3")},
	}
	for _, tc := range tests {
		if got := AskSubject(tc.chatID, tc.runID); got != tc.want {
			t.Errorf("AskSubject(%q, %q) = %+v, want %+v", tc.chatID, tc.runID, got, tc.want)
		}
	}
}

func TestBuild_boundsAndSanitizesEveryField(t *testing.T) {
	long := strings.Repeat("é", 300)
	n := Question(ChatTarget("c1", long), "", "line one\n\n  line\u202etwo\t"+long)
	if c := utf8.RuneCountInString(n.Title); c != titleRunes || !strings.HasSuffix(n.Title, ellipsis) {
		t.Errorf("title = %d runes %q, want %d ending in %q", c, n.Title, titleRunes, ellipsis)
	}
	if c := utf8.RuneCountInString(n.Body); c > bodyRunes || !strings.HasSuffix(n.Body, ellipsis) {
		t.Errorf("body = %d runes, want at most %d ending in %q", c, bodyRunes, ellipsis)
	}
	if !strings.HasPrefix(n.Body, "Input required · line one line two ") {
		t.Errorf("body = %q, want one line with whitespace collapsed and the bidi control gone", n.Body)
	}
}

func TestBuild_aDetailThatSanitizesToNothingLeavesNoSeparator(t *testing.T) {
	if got := Question(ChatTarget("c1", "x"), "", "\u202e\n\t").Body; got != "Input required" {
		t.Errorf("Question body = %q, want %q", got, "Input required")
	}
}
