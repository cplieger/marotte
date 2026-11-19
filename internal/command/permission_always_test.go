package command

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

type alwaysDeps struct {
	*takeDeps
	ask    marotte.PermissionNeededPayload
	events []string
}

func (d *alwaysDeps) PendingPermission(_ marotte.ChatID, id int64) (marotte.PermissionNeededPayload, bool) {
	return d.ask, id == d.ask.RequestID
}

func (d *alwaysDeps) TakePendingPermissionOption(chatID marotte.ChatID, id int64, opt string, by marotte.SettledBy) (AskReply, bool, bool) {
	d.events = append(d.events, "claim")
	return d.takeDeps.TakePendingPermissionOption(chatID, id, opt, by)
}

type alwaysBridge struct {
	recordingBridge
	deps    *alwaysDeps
	answers []any
}

func (b *alwaysBridge) Respond(ctx context.Context, _ int64, result any, _ error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.deps.events = append(b.deps.events, "answer")
	b.answers = append(b.answers, result)
	return nil
}

func newAlwaysDeps() (*alwaysDeps, *alwaysBridge) {
	d := &alwaysDeps{ask: marotte.PermissionNeededPayload{
		RequestID: 7,
		Consent: &marotte.PermissionConsent{
			Capability: "shell", Subject: "head -1", Resource: "head -1",
			Reaches: []marotte.AlwaysReach{marotte.AlwaysReachChat, marotte.AlwaysReachUser},
		},
		Options: []marotte.PermissionOption{
			{OptionID: "accept", Kind: "allow_once"},
			{OptionID: "always-accept", Kind: "allow_always"},
			{OptionID: "reject", Kind: "reject_once"},
			{OptionID: "always-reject", Kind: "reject_always"},
		},
	}}
	br := &alwaysBridge{deps: d}
	d.takeDeps = &takeDeps{benchDeps: newBenchDeps(), bridge: br, takeOK: true}
	return d, br
}

func answerJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal answer: %v", err)
	}
	return string(raw)
}

func TestCmdPermission_AlwaysSavesAUserRuleAndKeepsTheProfile(t *testing.T) {
	for _, tc := range []struct {
		name, option, want string
		reach              marotte.AlwaysReach
	}{
		{
			name: "always allow", option: "always-accept",
			want: `{"_meta":{"kiro":{"consent":{"scope":"user","capability":"shell","resource":"head *"}}},"outcome":{"outcome":"selected","optionId":"always-accept"}}`,
		},
		{
			name: "always allow naming the user reach", option: "always-accept", reach: marotte.AlwaysReachUser,
			want: `{"_meta":{"kiro":{"consent":{"scope":"user","capability":"shell","resource":"head *"}}},"outcome":{"outcome":"selected","optionId":"always-accept"}}`,
		},
		{
			name: "always deny", option: "always-reject",
			want: `{"_meta":{"kiro":{"consent":{"scope":"user","capability":"shell","resource":"head *"}}},"outcome":{"outcome":"selected","optionId":"always-reject"}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, br := newAlwaysDeps()
			cmd := decisionCommand(t, marotte.CmdPermissionResponse, marotte.PermissionResponseCommand{
				RequestID: 7, OptionID: tc.option, AlwaysResource: "  head *  ", AlwaysReach: tc.reach,
			})

			if _, err := cmdPermission(t.Context(), d, cmd); err != nil {
				t.Fatalf("CmdPermission = %v", err)
			}

			if got := d.events; len(got) != 2 || got[0] != "claim" || got[1] != "answer" {
				t.Errorf("events = %v, want [claim answer]: a saved rule loosens every chat, so it switches no profile", got)
			}
			if len(br.answers) != 1 {
				t.Fatalf("answers = %d, want 1", len(br.answers))
			}
			if got := answerJSON(t, br.answers[0]); got != tc.want {
				t.Errorf("answer = %s\nwant      %s", got, tc.want)
			}
		})
	}
}

func TestCmdPermission_AlwaysSessionScopeSavesNoFileAndKeepsTheProfile(t *testing.T) {
	d, br := newAlwaysDeps()
	cmd := decisionCommand(t, marotte.CmdPermissionResponse, marotte.PermissionResponseCommand{
		RequestID: 7, OptionID: "always-accept", AlwaysResource: "head *", AlwaysReach: marotte.AlwaysReachChat,
	})

	if _, err := cmdPermission(t.Context(), d, cmd); err != nil {
		t.Fatalf("CmdPermission = %v", err)
	}

	if got := d.events; len(got) != 2 || got[0] != "claim" || got[1] != "answer" {
		t.Errorf("events = %v, want [claim answer] for a session rule", got)
	}
	if len(br.answers) != 1 {
		t.Fatalf("answers = %d, want 1", len(br.answers))
	}
	want := `{"_meta":{"kiro":{"consent":{"scope":"session","capability":"shell","resource":"head *"}}},"outcome":{"outcome":"selected","optionId":"always-accept"}}`
	if got := answerJSON(t, br.answers[0]); got != want {
		t.Errorf("answer = %s\nwant      %s", got, want)
	}
}

// The scope each reach is kept at is KAS's consent scope vocabulary (session, launching-session,
// user), so the rule lands on the session the card's label names.
func TestCmdPermission_AlwaysReachMapsToKASScope(t *testing.T) {
	for _, tc := range []struct {
		reach marotte.AlwaysReach
		want  string
	}{
		{reach: marotte.AlwaysReachChat, want: "session"},
		{reach: marotte.AlwaysReachStep, want: "session"},
		{reach: marotte.AlwaysReachRun, want: "launching-session"},
		{reach: marotte.AlwaysReachLaunchingChat, want: "launching-session"},
		{reach: marotte.AlwaysReachUser, want: "user"},
		{reach: "", want: "user"},
	} {
		t.Run(cmp.Or(string(tc.reach), "no reach"), func(t *testing.T) {
			d, br := newAlwaysDeps()
			d.ask.Consent.Reaches = allReaches()
			cmd := decisionCommand(t, marotte.CmdPermissionResponse, marotte.PermissionResponseCommand{
				RequestID: 7, OptionID: "always-accept", AlwaysResource: "head *", AlwaysReach: tc.reach,
			})

			if _, err := cmdPermission(t.Context(), d, cmd); err != nil {
				t.Fatalf("CmdPermission(reach %q) = %v", tc.reach, err)
			}

			if len(br.answers) != 1 {
				t.Fatalf("answers = %d, want 1", len(br.answers))
			}
			want := `{"_meta":{"kiro":{"consent":{"scope":"` + tc.want + `","capability":"shell","resource":"head *"}}},"outcome":{"outcome":"selected","optionId":"always-accept"}}`
			if got := answerJSON(t, br.answers[0]); got != want {
				t.Errorf("answer = %s\nwant      %s", got, want)
			}
		})
	}
}

// KAS refuses a scope it did not offer and applies the answer once, so a reach the ask did not
// list is refused here with the ask left pending.
func TestCmdPermission_AlwaysReachNotOfferedIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reach   marotte.AlwaysReach
		offered []marotte.AlwaysReach
	}{
		{name: "a run reach when the launching chat is not live", reach: marotte.AlwaysReachRun, offered: []marotte.AlwaysReach{marotte.AlwaysReachChat, marotte.AlwaysReachUser}},
		{name: "a step reach on a chat's own ask", reach: marotte.AlwaysReachStep, offered: []marotte.AlwaysReach{marotte.AlwaysReachChat, marotte.AlwaysReachUser}},
		{name: "no reach on an ask offering no user rule", offered: []marotte.AlwaysReach{marotte.AlwaysReachStep}},
		{name: "a chat reach on an ask offering none", reach: marotte.AlwaysReachChat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, br := newAlwaysDeps()
			d.ask.Consent.Reaches = tc.offered
			cmd := decisionCommand(t, marotte.CmdPermissionResponse, marotte.PermissionResponseCommand{
				RequestID: 7, OptionID: "always-accept", AlwaysResource: "head *", AlwaysReach: tc.reach,
			})

			_, err := cmdPermission(t.Context(), d, cmd)

			if got := statusOf(err); got != http.StatusBadRequest || !errors.Is(err, errAlwaysReachInvalid) {
				t.Fatalf("CmdPermission(reach %q) = %v (status %d), want 400 always_reach_invalid", tc.reach, err, got)
			}
			if len(d.events) != 0 || len(br.answers) != 0 {
				t.Errorf("events = %v after a refused reach, want none: the ask must stay pending", d.events)
			}
		})
	}
}

func TestCmdPermission_AlwaysReachIsChecked(t *testing.T) {
	for _, tc := range []struct {
		name, option, resource string
		reach                  marotte.AlwaysReach
	}{
		{name: "a workspace reach", option: "always-accept", resource: "head *", reach: "workspace"},
		{name: "an invocation reach", option: "always-accept", resource: "head *", reach: "invocation"},
		{name: "a KAS scope name", option: "always-accept", resource: "head *", reach: "session"},
		{name: "an unknown reach", option: "always-accept", resource: "head *", reach: "x"},
		{name: "a chat reach on an always deny", option: "always-reject", resource: "head *", reach: marotte.AlwaysReachChat},
		{name: "a step reach on an always deny", option: "always-reject", resource: "head *", reach: marotte.AlwaysReachStep},
		{name: "a run reach on an always deny", option: "always-reject", resource: "head *", reach: marotte.AlwaysReachRun},
		{name: "a launching chat reach on an always deny", option: "always-reject", resource: "head *", reach: marotte.AlwaysReachLaunchingChat},
		{name: "a reach on a one-time allow", option: "accept", reach: marotte.AlwaysReachChat},
		{name: "a user reach on a one-time deny", option: "reject", reach: marotte.AlwaysReachUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, br := newAlwaysDeps()
			d.ask.Consent.Reaches = allReaches()
			cmd := decisionCommand(t, marotte.CmdPermissionResponse, marotte.PermissionResponseCommand{
				RequestID: 7, OptionID: tc.option, AlwaysResource: tc.resource, AlwaysReach: tc.reach,
			})

			_, err := cmdPermission(t.Context(), d, cmd)

			if got := statusOf(err); got != http.StatusBadRequest || !errors.Is(err, errAlwaysReachInvalid) {
				t.Fatalf("CmdPermission(%q, reach %q) = %v (status %d), want 400 always_reach_invalid", tc.option, tc.reach, err, got)
			}
			if len(d.events) != 0 || len(br.answers) != 0 {
				t.Errorf("events = %v after a refused reach, want none: the ask must stay pending", d.events)
			}
		})
	}
}

func allReaches() []marotte.AlwaysReach {
	return []marotte.AlwaysReach{
		marotte.AlwaysReachChat, marotte.AlwaysReachStep, marotte.AlwaysReachRun,
		marotte.AlwaysReachLaunchingChat, marotte.AlwaysReachUser,
	}
}

func TestCmdPermission_AlwaysPatternIsChecked(t *testing.T) {
	for _, tc := range []struct {
		name, option, resource string
	}{
		{name: "an always answer with no pattern", option: "always-accept", resource: " "},
		{name: "a pattern with a control character", option: "always-accept", resource: "head\x00 *"},
		{name: "a pattern on a one-time answer", option: "accept", resource: "head *"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, br := newAlwaysDeps()
			cmd := decisionCommand(t, marotte.CmdPermissionResponse, marotte.PermissionResponseCommand{
				RequestID: 7, OptionID: tc.option, AlwaysResource: tc.resource,
			})

			_, err := cmdPermission(t.Context(), d, cmd)

			if got := statusOf(err); got != http.StatusBadRequest || !errors.Is(err, errAlwaysResourceInvalid) {
				t.Fatalf("CmdPermission(%q, %q) = %v (status %d), want 400 always_resource_invalid", tc.option, tc.resource, err, got)
			}
			if len(d.events) != 0 || len(br.answers) != 0 {
				t.Errorf("events = %v after a refused pattern, want none", d.events)
			}
		})
	}
}

func TestCmdPermission_OneTimeAnswerLeavesTheProfileAlone(t *testing.T) {
	d, br := newAlwaysDeps()
	cmd := decisionCommand(t, marotte.CmdPermissionResponse, marotte.PermissionResponseCommand{RequestID: 7, OptionID: "accept"})

	if _, err := cmdPermission(t.Context(), d, cmd); err != nil {
		t.Fatalf("CmdPermission = %v", err)
	}

	if got := answerJSON(t, br.answers[0]); got != `{"outcome":{"outcome":"selected","optionId":"accept"}}` {
		t.Errorf("answer = %s, want a plain accept", got)
	}
	if len(d.events) != 2 || d.events[0] != "claim" {
		t.Errorf("events = %v, want [claim answer] for a one-time answer", d.events)
	}
}

// A rule saved from the display copy (controls replaced, cut at the cap) never matches the part,
// so KAS would ask for it again.
func TestCmdPermission_AlwaysSubjectPatternSavesKASOwnSubject(t *testing.T) {
	long := "python3 -c '" + strings.Repeat("x", 600) + "'"
	for _, tc := range []struct {
		consent marotte.PermissionConsent
		name    string
		chosen  string
		want    string
	}{
		{
			name:    "a multi-line part",
			consent: marotte.PermissionConsent{Capability: "shell", Subject: "git commit -m 'a b'", Resource: "git commit -m 'a\nb'"},
			chosen:  "git commit -m 'a b'", want: "git commit -m 'a\nb'",
		},
		{
			name:    "a part over the display cap",
			consent: marotte.PermissionConsent{Capability: "shell", Subject: long[:512] + "...", Resource: long},
			chosen:  long[:512] + "...", want: long,
		},
		{
			name: "the folder of a path whose name holds a tab",
			consent: marotte.PermissionConsent{
				Capability: "fs_write", Subject: "/w/a b/notes.md", Resource: "/w/a\tb/notes.md",
				Folder: "/w/a b/**", FolderResource: "/w/a\tb/**",
			},
			chosen: "/w/a b/**", want: "/w/a\tb/**",
		},
		{
			name:    "a pattern the card built some other way is saved as chosen",
			consent: marotte.PermissionConsent{Capability: "shell", Subject: "git commit -m 'a b'", Resource: "git commit -m 'a\nb'"},
			chosen:  "git *", want: "git *",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, br := newAlwaysDeps()
			d.ask.Consent = &tc.consent
			d.ask.Consent.Reaches = []marotte.AlwaysReach{marotte.AlwaysReachUser}
			cmd := decisionCommand(t, marotte.CmdPermissionResponse, marotte.PermissionResponseCommand{
				RequestID: 7, OptionID: "always-accept", AlwaysResource: tc.chosen,
			})

			if _, err := cmdPermission(t.Context(), d, cmd); err != nil {
				t.Fatalf("CmdPermission(%q) = %v", tc.chosen, err)
			}

			if len(br.answers) != 1 {
				t.Fatalf("answers = %d, want 1", len(br.answers))
			}
			out, ok := br.answers[0].(*marotte.PermissionOutcome)
			if !ok || out.Meta == nil || out.Meta.Kiro.Consent == nil {
				t.Fatalf("answer = %s, want one carrying consent", answerJSON(t, br.answers[0]))
			}
			if got := out.Meta.Kiro.Consent.Resource; got != tc.want {
				t.Errorf("CmdPermission(%q) saved %q, want %q", tc.chosen, got, tc.want)
			}
		})
	}
}
