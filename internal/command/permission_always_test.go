package command

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/securityprofile"
)

type alwaysDeps struct {
	*takeDeps
	switchErr    error
	duringSwitch func()
	ask          marotte.PermissionNeededPayload
	events       []string
}

func (d *alwaysDeps) PendingPermission(_ marotte.ChatID, id int64) (marotte.PermissionNeededPayload, bool) {
	return d.ask, id == d.ask.RequestID
}

func (d *alwaysDeps) EnsureCustomProfile(context.Context) error {
	d.events = append(d.events, "custom")
	if d.duringSwitch != nil {
		d.duringSwitch()
	}
	return d.switchErr
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
		Consent:   &marotte.PermissionConsent{Capability: "shell", Subject: "head -1", Resource: "head -1"},
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

func TestCmdPermission_AlwaysSavesAUserRuleAfterSwitchingToCustom(t *testing.T) {
	for _, tc := range []struct {
		name, option, want string
	}{
		{
			name: "always allow", option: "always-accept",
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
				RequestID: 7, OptionID: tc.option, AlwaysResource: "  head *  ",
			})

			if _, err := cmdPermission(t.Context(), d, d, cmd); err != nil {
				t.Fatalf("CmdPermission = %v", err)
			}

			if got := d.events; len(got) != 3 || got[0] != "custom" || got[1] != "claim" || got[2] != "answer" {
				t.Errorf("order = %v, want [custom claim answer]: kiro-cli's write must follow marotte's", got)
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

func TestCmdPermission_AlwaysRefusalLeavesTheAskPending(t *testing.T) {
	d, br := newAlwaysDeps()
	d.switchErr = securityprofile.ErrNoLivePolicy
	cmd := decisionCommand(t, marotte.CmdPermissionResponse, marotte.PermissionResponseCommand{
		RequestID: 7, OptionID: "always-accept", AlwaysResource: "head *",
	})

	_, err := cmdPermission(t.Context(), d, d, cmd)

	var se *statusError
	if !errors.As(err, &se) || se.code != http.StatusConflict || se.reason != reasonAlwaysRuleNotSaved {
		t.Fatalf("CmdPermission = %v, want a 409 carrying %q", err, reasonAlwaysRuleNotSaved)
	}
	if len(d.takes) != 0 || len(br.answers) != 0 {
		t.Errorf("claims = %v, answers = %d after a refused switch, want none", d.takes, len(br.answers))
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

			_, err := cmdPermission(t.Context(), d, d, cmd)

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

	if _, err := cmdPermission(t.Context(), d, d, cmd); err != nil {
		t.Fatalf("CmdPermission = %v", err)
	}

	if got := answerJSON(t, br.answers[0]); got != `{"outcome":{"outcome":"selected","optionId":"accept"}}` {
		t.Errorf("answer = %s, want a plain accept", got)
	}
	if len(d.events) != 2 || d.events[0] != "claim" {
		t.Errorf("events = %v, want [claim answer] with no switch to Custom", d.events)
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
			cmd := decisionCommand(t, marotte.CmdPermissionResponse, marotte.PermissionResponseCommand{
				RequestID: 7, OptionID: "always-accept", AlwaysResource: tc.chosen,
			})

			if _, err := cmdPermission(t.Context(), d, d, cmd); err != nil {
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

// A claimed ask nobody answers wedges the turn, so the answer outlives the request.
func TestCmdPermission_AlwaysAnswerSurvivesAClientThatLeftDuringTheSwitch(t *testing.T) {
	d, br := newAlwaysDeps()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	d.duringSwitch = cancel
	cmd := decisionCommand(t, marotte.CmdPermissionResponse, marotte.PermissionResponseCommand{
		RequestID: 7, OptionID: "always-accept", AlwaysResource: "head *",
	})

	if _, err := cmdPermission(ctx, d, d, cmd); err != nil {
		t.Fatalf("CmdPermission = %v", err)
	}

	if got := d.events; len(got) != 3 || got[2] != "answer" || len(br.answers) != 1 {
		t.Errorf("events = %v, answers = %d after the client left mid-switch, want [custom claim answer] and 1", got, len(br.answers))
	}
}
