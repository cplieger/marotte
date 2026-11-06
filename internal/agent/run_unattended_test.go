package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/marotte/internal/translate"
)

// TestUnattendedBudget_MatchesTheDisclaimer pins a cross-language constant: the schedule form promises the
// budget in words from UNATTENDED_BUDGET_MINUTES in static-src/schedule-picker.ts. Change both.
func TestUnattendedBudget_MatchesTheDisclaimer(t *testing.T) {
	t.Parallel()
	const disclaimerMinutes = 3 // UNATTENDED_BUDGET_MINUTES, schedule-picker.ts
	if unattendedApprovalBudget != disclaimerMinutes*time.Minute {
		t.Errorf("unattendedApprovalBudget = %v, but the schedule form tells the user %d minutes; "+
			"update UNATTENDED_BUDGET_MINUTES in static-src/schedule-picker.ts to match",
			unattendedApprovalBudget, disclaimerMinutes)
	}
}

// TestUnattendedOrigin_ReadsTheLeasesOrigin pins the `unattended` field's source: only the launch path can tell a
// scheduled or triggered parentless run from a manual one, and the lease records it.
func TestUnattendedOrigin_ReadsTheLeasesOrigin(t *testing.T) {
	h := &Runs{chats: newTestChatStore()}

	if got := h.UnattendedOrigin("wf_1"); got != "" {
		t.Errorf("an unknown run reported %q; a manual launch must never be marked", got)
	}
	h.grantLease(t.Context(), "wf_manual", "publish", manualLaunch(), hostSec())
	if got := h.UnattendedOrigin("wf_manual"); got != "" {
		t.Errorf("a manual run reported %q", got)
	}
	h.grantLease(t.Context(), "wf_1", "publish", scheduledLaunch(launchrule.Row{ID: "sched-1"}, time.Time{}), hostSec())
	if got := h.UnattendedOrigin("wf_1"); got != "scheduled" {
		t.Errorf("a scheduled run reported %q, want scheduled", got)
	}
	if got := h.UnattendedOrigin("wf_2"); got != "" {
		t.Errorf("the origin leaked to another run: %q", got)
	}
	// Released with the terminal frame, and the origin with it.
	h.releaseLease(t.Context(), "wf_1")
	if got := h.UnattendedOrigin("wf_1"); got != "" {
		t.Errorf("a released run still reported %q", got)
	}
	// An empty id is not a run.
	if got := h.UnattendedOrigin(""); got != "" {
		t.Errorf("the empty workflow id reported %q", got)
	}
}

func TestRunLabel_RebuildsTheLaunchLabelFromTheLease(t *testing.T) {
	h := &Runs{chats: newTestChatStore()}
	h.grantLease(t.Context(), "wf_manual", "publish", manualLaunch(), hostSec())
	h.grantLease(t.Context(), "wf_sched", "publish", scheduledLaunch(launchrule.Row{ID: "sched-1"}, time.Time{}), hostSec())
	if got := h.RunLabel("wf_sched"); got != "publish · scheduled" {
		t.Errorf("RunLabel(scheduled) = %q, want %q", got, "publish · scheduled")
	}
	if got := h.RunLabel("wf_manual"); got != "" {
		t.Errorf("RunLabel(manual) = %q, want empty: a manual launch sends no label", got)
	}
	if got := h.RunLabel("wf_unknown"); got != "" {
		t.Errorf("RunLabel(unknown) = %q, want empty", got)
	}
}

// TestUnattendedFloor_ArmsFromTheLeaseAndSurvivesARestart pins that the durable lease keeps the deny-fast budget across a
// restart, or a parked scheduled run waits on a human and parks its recipe.
func TestUnattendedFloor_ArmsFromTheLeaseAndSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	store, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h := &Runs{chats: newTestChatStore(), leases: store}
	h.grantLease(t.Context(), "wf_1", "nightly", scheduledLaunch(launchrule.Row{ID: "sched-1"}, time.Time{}), hostSec())

	// The restart: a new store over the same directory, a runtime that launched nothing.
	reopened, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	after := &Runs{chats: newTestChatStore(), leases: reopened}

	l, held := after.lease("wf_1")
	if !held {
		t.Fatal("the lease did not survive, so the 03:00 ask would wait for a human forever")
	}
	if !l.Unattended {
		t.Error("the unattended mark did not survive, so the floor would not arm")
	}
	if l.ScheduleID != "sched-1" {
		t.Errorf("ScheduleID = %q; the denial could not be attributed to a row", l.ScheduleID)
	}
	if after.UnattendedOrigin("wf_1") != "scheduled" {
		t.Error("the origin did not survive")
	}
}

// TestUnattendedFloor_DelegatedLeaseRefusesAnUnclearedAsk pins that the floor reads the delegation from the presets the
// run's lease recorded, and that a lease which recorded none fails closed.
func TestUnattendedFloor_DelegatedLeaseRefusesAnUnclearedAsk(t *testing.T) {
	delegated, ok := policyfile.ProfileFor(policyfile.ProfileDelegated)
	if !ok {
		t.Fatal("no Delegated profile")
	}
	trusted, ok := policyfile.ProfileFor(policyfile.ProfileTrusted)
	if !ok {
		t.Fatal("no Trusted profile")
	}
	prev := unattendedAskBudget
	unattendedAskBudget = 10 * time.Millisecond
	t.Cleanup(func() { unattendedAskBudget = prev })
	for _, tc := range []struct {
		name       string
		host       *runlease.HostSecurity
		wantOption string
	}{
		{name: "delegated_presets", host: &runlease.HostSecurity{Presets: delegated.Presets}, wantOption: "reject"},
		{name: "no_recorded_presets", host: nil, wantOption: "reject"},
		{name: "trusted_presets", host: &runlease.HostSecurity{Presets: trusted.Presets}, wantOption: "accept"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			body, err := json.Marshal(map[string]any{settings.KeyScheduledAutoApprove: true})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "config.json"), body, 0o600); err != nil {
				t.Fatal(err)
			}
			h, br := hubForFSTest(t, t.TempDir(), WithConfigDir(dir))
			chatID := runChatID("wf_floor")
			h.insertBridgeNow(chatID, testBridge(br, bridgeIdle))
			h.runs.grantLease(t.Context(), "wf_floor", "nightly", scheduledLaunch(launchrule.Row{ID: "sched-1"}, time.Time{}), tc.host)

			id := int64(4244)
			h.bus.pendingPerms.add(id, marotte.NewEvent(marotte.EventPermissionNeeded, chatID,
				marotte.PermissionNeededPayload{RequestID: id}), br)
			wrapped := h.runs.permissionWithUnattendedFloor(func(context.Context, marotte.ChatID, translate.AskOrigin, *marotte.RPCResponse) {})
			wrapped(t.Context(), chatID, br, &marotte.RPCResponse{
				Method: marotte.MethodRequestPermission,
				ID:     &id,
				Params: json.RawMessage(`{"options":[{"optionId":"accept","kind":"allow_once"},{"optionId":"reject","kind":"reject_once"}],` +
					`"_meta":{"kiro":{"toolId":"fs_write","consent":{"askType":"implicit","capability":"fs_write"}}}}`),
			})

			select {
			case <-br.done:
			case <-time.After(2 * time.Second):
				t.Fatal("the floor answered nothing")
			}
			br.respMu.Lock()
			got := br.response
			br.respMu.Unlock()
			outcome, ok := got.result.(*marotte.PermissionOutcome)
			if !ok {
				t.Fatalf("answered with %T (%v), want a permission outcome", got.result, got.result)
			}
			if outcome.Outcome.OptionID != tc.wantOption {
				t.Errorf("optionId = %q, want %q", outcome.Outcome.OptionID, tc.wantOption)
			}
		})
	}
}

// TestUnattendedFloor_ArmsNothingForAnUnanswerableAsk pins that the floor answers by request id, so an id-less ask must
// not arm (it would dereference nothing); the ordinary handler still runs.
func TestUnattendedFloor_ArmsNothingForAnUnanswerableAsk(t *testing.T) {
	rs := &Runs{chats: newTestChatStore()}
	rs.grantLease(t.Context(), "wf_1", "nightly",
		scheduledLaunch(launchrule.Row{ID: "sched-1"}, time.Now().Add(30*time.Second)), hostSec())
	if l, held := rs.lease("wf_1"); !held || !l.Unattended {
		t.Fatal("the fixture did not produce the unattended lease the floor reaches through")
	}

	inner := 0
	noteAsk := func(context.Context, marotte.ChatID, translate.AskOrigin, *marotte.RPCResponse) { inner++ }
	wrapped := rs.permissionWithUnattendedFloor(noteAsk)

	// A permission frame with no id.
	wrapped(t.Context(), runChatID("wf_1"), nil, &marotte.RPCResponse{
		Method: marotte.MethodRequestPermission,
		ID:     nil,
	})

	if inner != 1 {
		t.Errorf("the ordinary permission handler ran %d times, want 1: the wrapper swallowed the "+
			"frame instead of passing it on", inner)
	}
}

// TestPermissionAskName_PrefersTheMachineAuthoredName walks the six backend ask shapes; a gate on toolId
// would blank five of them.
func TestPermissionAskName_PrefersTheMachineAuthoredName(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		params string
		want   string
	}{
		"tool approval names the tool id, not the model's title": {
			params: `{"toolCall":{"title":"Run a helpful command","kind":"execute"},
				"_meta":{"kiro":{"toolId":"execute_bash","command":"ls"}}}`,
			want: "execute_bash",
		},
		"hook approval names the hook": {
			params: `{"toolCall":{"title":"pre-commit"},"_meta":{"kiro":{"hookName":"lint","command":"make lint"}}}`,
			want:   "lint",
		},
		"turn approval gets marotte's own name": {
			// KAS's literal "Review changes" tells an operator nothing.
			params: `{"toolCall":{"title":"Review changes"},"_meta":{"kiro":{"type":"turn_approval","executionId":"e1"}}}`,
			want:   turnApprovalName,
		},
		"no _meta at all falls back to the title": {
			// Three of the six carry no _meta, so the title is all there is.
			params: `{"toolCall":{"title":"Allow this hook to run?","kind":"other"}}`,
			want:   "Allow this hook to run?",
		},
		"an untitled request falls back to the kind": {
			params: `{"toolCall":{"kind":"edit"}}`,
			want:   "edit",
		},
		"an undecodable request is nameless": {
			params: `{"toolCall":[]}`,
			want:   "",
		},
		"a Power ask names the Power tool it asks about": {
			params: `{"toolCall":{"title":"@github/github/create_pr"},"_meta":{"kiro":{"toolId":"kiro_powers",
				"consent":{"capability":"power","resource":"github/github/create_pr"}}}}`,
			want: "kiro_powers (github/github/create_pr)",
		},
		"a compound ask names the part it is about": {
			params: `{"_meta":{"kiro":{"toolId":"fs_write",
				"consent":{"capability":"fs_write","resource":"/workspace","triggeringResource":"/workspace/a.go"}}}}`,
			want: "fs_write (/workspace/a.go)",
		},
		"a shell ask keeps the tool name alone": {
			params: `{"_meta":{"kiro":{"toolId":"execute_bash",
				"consent":{"capability":"shell","resource":"curl -H 'Authorization: Bearer s3cret' x"}}}}`,
			want: "execute_bash",
		},
		"a subject equal to the tool is not repeated": {
			params: `{"_meta":{"kiro":{"toolId":"web_search","consent":{"capability":"web_search","resource":"web_search"}}}}`,
			want:   "web_search",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := permissionAskName([]byte(tc.params)).label(); got != tc.want {
				t.Errorf("permissionAskName().label() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPermissionAskName_KeepsCredentialsOffTheRow pins that a URL subject (an MCP server or a fetch target)
// loses the userinfo, query and fragment that carry its token before it is persisted and logged.
func TestPermissionAskName_KeepsCredentialsOffTheRow(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		resource string
		want     string
	}{
		"a token in the query is dropped": {
			resource: "https://mcp.example.com/sse?token=s3cret&x=1",
			want:     "https://mcp.example.com/sse",
		},
		"userinfo and fragment are dropped": {
			resource: "https://user:s3cret@mcp.example.com/sse#s3cret",
			want:     "https://mcp.example.com/sse",
		},
		"a URL that does not parse is dropped whole": {
			resource: "https://user:s3cret@mcp.example.com/%zz",
			want:     "",
		},
		"a clean URL is byte-identical": {
			resource: "HTTPS://mcp.example.com/sse",
			want:     "HTTPS://mcp.example.com/sse",
		},
		"a path is not a URL": {
			resource: "/workspace/a.go?keep",
			want:     "/workspace/a.go?keep",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			params := `{"_meta":{"kiro":{"toolId":"web_fetch","consent":{"capability":"web_fetch","resource":` +
				jsonString(tc.resource) + `}}}}`
			got := permissionAskName([]byte(params))
			if got.subject != tc.want {
				t.Errorf("permissionAskName().subject = %q, want %q", got.subject, tc.want)
			}
			if strings.Contains(got.label(), "s3cret") {
				t.Errorf("label() = %q carries the credential onto the schedule row", got.label())
			}
		})
	}
}

// TestPermissionAskName_DefusesTheModelsTitle pins that the model composes the title, so a poisoned file can put a
// bidi override in the log and in the schedule row an operator reads.
func TestPermissionAskName_DefusesTheModelsTitle(t *testing.T) {
	t.Parallel()

	// Renders as a harmless find while asking to approve `rm -rf /workspace`.
	const title = "Run \u202ednuof-emaN- ecapskrow/ fr- mr\u202c"

	t.Run("a bidi override cannot reach the row", func(t *testing.T) {
		t.Parallel()
		got := permissionAskName([]byte(`{"toolCall":{"title":` + jsonString(title) + `}}`)).label()
		for _, r := range []rune{'\u202e', '\u202c'} {
			if strings.ContainsRune(got, r) {
				t.Errorf("permissionAskName().label() = %q still carries U+%04X; the row can name a "+
					"different tool than the one that was refused", got, r)
			}
		}
	})

	t.Run("a bidi override in the subject cannot reach the row", func(t *testing.T) {
		t.Parallel()
		// A Power subject comes from the model's kiro_powers arguments.
		got := permissionAskName([]byte(`{"_meta":{"kiro":{"toolId":"kiro_powers",
			"consent":{"capability":"power","resource":` + jsonString(title) + `}}}}`)).label()
		for _, r := range []rune{'\u202e', '\u202c'} {
			if strings.ContainsRune(got, r) {
				t.Errorf("permissionAskName().label() = %q still carries U+%04X from the subject", got, r)
			}
		}
	})

	t.Run("an unbounded title is capped", func(t *testing.T) {
		t.Parallel()
		// The truncation marker rides outside the cap.
		const ceiling = maxToolNameBytes + len("...")
		got := permissionAskName([]byte(`{"toolCall":{"title":"` + strings.Repeat("a", 4096) + `"}}`)).tool
		if len(got) > ceiling {
			t.Errorf("permissionAskName().tool is %d bytes, want at most %d: this value is "+
				"concatenated into the schedule row and persisted on every fire",
				len(got), ceiling)
		}
		if !strings.HasSuffix(got, "...") {
			t.Errorf("a truncated name = %q, want the truncation marked so the row does not read "+
				"as a complete name", got)
		}
	})

	t.Run("a legitimate name is byte-identical", func(t *testing.T) {
		t.Parallel()
		// The sanitizer replaces rather than deletes, so ordinary names pass unchanged.
		for _, want := range []string{"execute_bash", "fs_write", "Modifier le fichier", "ファイルを書く"} {
			got := permissionAskName([]byte(`{"toolCall":{"title":` + jsonString(want) + `}}`)).tool
			if got != want {
				t.Errorf("permissionAskName(%q).tool = %q, want it unchanged", want, got)
			}
		}
	})
}

// TestOptionIDByKind_MatchesTheKindExactly pins that a prefix match would pick a persisting `_always` kind and turn one
// unattended refusal into a standing rule.
func TestOptionIDByKind_MatchesTheKindExactly(t *testing.T) {
	t.Parallel()
	// All four kinds, as a persistable ask advertises.
	const persistable = `{"options":[
		{"optionId":"accept","kind":"allow_once"},
		{"optionId":"always-accept","kind":"allow_always"},
		{"optionId":"reject","kind":"reject_once"},
		{"optionId":"always-reject","kind":"reject_always"}]}`

	if got := optionIDByKind([]byte(persistable), optionKindRejectOnce); got != "reject" {
		t.Errorf("optionIDByKind(reject_once) = %q, want %q", got, "reject")
	}
	if got := optionIDByKind([]byte(persistable), optionKindAllowOnce); got != "accept" {
		t.Errorf("optionIDByKind(allow_once) = %q, want %q", got, "accept")
	}
	// The kind is matched and the id echoed; confusing them answers with an unoffered choice.
	if got := optionIDByKind([]byte(persistable), "reject"); got != "" {
		t.Errorf("optionIDByKind(%q) = %q, want no match: the kind is matched, never the id",
			"reject", got)
	}
	// Only the persistent twin advertised: exact match declines and the caller falls back to cancelled.
	const persistentOnly = `{"options":[{"optionId":"always-reject","kind":"reject_always"},
		{"optionId":"always-accept","kind":"allow_always"}]}`
	for _, kind := range []string{optionKindRejectOnce, optionKindAllowOnce} {
		if got := optionIDByKind([]byte(persistentOnly), kind); got != "" {
			t.Errorf("optionIDByKind(%q) = %q against a request advertising only the persistent "+
				"twins; selecting that makes the backend write a standing rule nobody asked for",
				kind, got)
		}
	}
	// Only the one-shot pair: the twins yield nothing.
	const oneShot = `{"options":[{"optionId":"accept","kind":"allow_once"},{"optionId":"reject","kind":"reject_once"}]}`
	for _, kind := range []string{"allow_always", "reject_always"} {
		if got := optionIDByKind([]byte(oneShot), kind); got != "" {
			t.Errorf("optionIDByKind(%q) = %q on a non-persistable ask, want none", kind, got)
		}
	}
	if got := optionIDByKind([]byte(`{"options":[]}`), optionKindRejectOnce); got != "" {
		t.Errorf("optionIDByKind on an empty option list = %q, want none", got)
	}
	if got := optionIDByKind([]byte(`{"options":42}`), optionKindRejectOnce); got != "" {
		t.Errorf("optionIDByKind on undecodable params = %q, want none", got)
	}
}

// TestAnswerUnattended_DenyUsesTheAdvertisedRejectOption pins that answer with an offered choice. TakePendingPerm runs
// before the response, since the floor races a human.
func TestAnswerUnattended_DenyUsesTheAdvertisedRejectOption(t *testing.T) {
	for name, tc := range map[string]struct {
		options     string
		meta        string
		wantOpt     string
		wantOutcome string
		wantReason  string
	}{
		"a reject option is selected by id": {
			options: `[{"optionId":"accept","kind":"allow_once"},
				{"optionId":"reject","kind":"reject_once"},
				{"optionId":"always-reject","kind":"reject_always"}]`,
			wantOpt:     "reject",
			wantOutcome: "selected",
			wantReason:  unattendedRejectionReason,
		},
		"no reject option advertised falls back to cancelled": {
			options:     `[{"optionId":"accept","kind":"allow_once"}]`,
			wantOutcome: string(marotte.StopReasonCancelled),
		},
		// KAS hands a plan's deny note to the next approved plan's steps as the user's correction.
		"a plan approval is refused with no note": {
			options: `[{"optionId":"accept","kind":"allow_once"},
				{"optionId":"reject","kind":"reject_once"}]`,
			meta:        `,"_meta":{"kiro":{"toolId":"update_workflow","workflowPlan":{}}}`,
			wantOpt:     "reject",
			wantOutcome: "selected",
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, br := hubForFSTest(t, t.TempDir())
			chatID := runChatID("wf_deny")
			h.insertBridgeNow(chatID, testBridge(br, bridgeIdle))

			id := int64(90210)
			h.bus.pendingPerms.add(id, marotte.NewEvent(marotte.EventPermissionNeeded, chatID,
				marotte.PermissionNeededPayload{RequestID: id}), br)

			h.runs.answerUnattended(chatID, br, id, launchrule.Run{ScheduleID: "sched-1", WorkflowID: "wf_1"}, false, askName{tool: "execute_bash"},
				[]byte(`{"options":`+tc.options+tc.meta+`}`))

			select {
			case <-br.done:
			case <-time.After(2 * time.Second):
				t.Fatal("the floor answered nothing, so the ask waits for a human who is not there")
			}
			br.respMu.Lock()
			got := br.response
			br.respMu.Unlock()

			outcome, ok := got.result.(*marotte.PermissionOutcome)
			if !ok {
				t.Fatalf("answered with %T (%v), want a permission outcome", got.result, got.result)
			}
			if outcome.Outcome.Outcome != tc.wantOutcome {
				t.Errorf("outcome = %q, want %q", outcome.Outcome.Outcome, tc.wantOutcome)
			}
			if outcome.Outcome.OptionID != tc.wantOpt {
				t.Errorf("optionId = %q, want %q", outcome.Outcome.OptionID, tc.wantOpt)
			}
			var reason string
			if outcome.Meta != nil {
				reason = outcome.Meta.Kiro.RejectionReason
			}
			if reason != tc.wantReason {
				t.Errorf("rejectionReason = %q, want %q", reason, tc.wantReason)
			}
			// Never the persistent twin.
			if outcome.Outcome.OptionID == "always-reject" {
				t.Error("the floor selected the PERSISTENT reject; one automated refusal became a " +
					"standing rule nobody wrote")
			}
		})
	}
}

// TestAnswerUnattended_AdministratorAskIsRefusedDespiteAutoApprove pins that KAS treats any allow_once as satisfying an administrator ask.
func TestAnswerUnattended_AdministratorAskIsRefusedDespiteAutoApprove(t *testing.T) {
	dir := t.TempDir()
	body, err := json.Marshal(map[string]any{settings.KeyScheduledAutoApprove: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, t.TempDir(), WithConfigDir(dir))
	chatID := runChatID("wf_admin")
	h.insertBridgeNow(chatID, testBridge(br, bridgeIdle))

	id := int64(4242)
	h.bus.pendingPerms.add(id, marotte.NewEvent(marotte.EventPermissionNeeded, chatID,
		marotte.PermissionNeededPayload{RequestID: id}), br)

	h.runs.answerUnattended(chatID, br, id, launchrule.Run{ScheduleID: "sched-1", WorkflowID: "wf_1"}, false, askName{tool: "execute_bash"}, []byte(`{
		"options":[{"optionId":"accept","kind":"allow_once"},{"optionId":"reject","kind":"reject_once"}],
		"_meta":{"kiro":{"consent":{"scope":"administration"}}}}`))

	select {
	case <-br.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the floor answered nothing")
	}
	br.respMu.Lock()
	got := br.response
	br.respMu.Unlock()
	outcome, ok := got.result.(*marotte.PermissionOutcome)
	if !ok {
		t.Fatalf("answered with %T (%v), want a permission outcome", got.result, got.result)
	}
	if outcome.Outcome.OptionID != "reject" {
		t.Errorf("optionId = %q, want %q: an administrator ask was auto-approved with nobody watching",
			outcome.Outcome.OptionID, "reject")
	}
}

// With the switch on, an ask Kiro's safety check handed to a person is still refused, and the row says who
// asked. KAS stamps only a tripped breaker and an oversized action; under a delegated session every other implicit
// tool ask (a failed or timed-out check, a sensitive path) reaches the floor unstamped and is refused too.
func TestAnswerUnattended_RefusesClassifierHandedAsk(t *testing.T) {
	const implicitWrite = `,"_meta":{"kiro":{"toolId":"fs_write","consent":{"askType":"implicit","capability":"fs_write"}}}`
	for _, tc := range []struct {
		name, meta, wantOption string
		delegated              bool
	}{
		{name: "handed_over", meta: `,"_meta":{"kiro":{"trustClassifier":{"decision":"prompt","reason":"Kiro's safety check flagged this"}}}`, wantOption: "reject"},
		{name: "not_delegated_unstamped", meta: implicitWrite, wantOption: "accept"},
		{name: "delegated_implicit_unstamped", meta: implicitWrite, delegated: true, wantOption: "reject"},
		{name: "delegated_undecodable_kiro_block", meta: `,"_meta":{"kiro":{"toolId":"fs_write","consent":"implicit"}}`, delegated: true, wantOption: "reject"},
		// An ask rule's ask is the policy's own, not one the safety check passed on.
		{name: "delegated_explicit_ask_rule", meta: `,"_meta":{"kiro":{"toolId":"fs_write","consent":{"askType":"explicit","capability":"fs_write"}}}`, delegated: true, wantOption: "accept"},
		// KAS never sends a workflow ask to the safety check, so a plan approval stays approvable.
		{name: "delegated_workflow_capability", meta: `,"_meta":{"kiro":{"toolId":"update_workflow","consent":{"askType":"implicit","capability":"workflow"}}}`, delegated: true, wantOption: "accept"},
		{name: "delegated_turn_approval", meta: `,"_meta":{"kiro":{"type":"turn_approval"}}`, delegated: true, wantOption: "accept"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			body, err := json.Marshal(map[string]any{settings.KeyScheduledAutoApprove: true})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "config.json"), body, 0o600); err != nil {
				t.Fatal(err)
			}
			h, br := hubForFSTest(t, t.TempDir(), WithConfigDir(dir))
			chatID := runChatID("wf_classifier")
			h.bridge.mgr.insert(chatID, testBridge(br, bridgeIdle))
			st, _, err := launchrule.NewStore(t.TempDir())
			if err != nil {
				t.Fatalf("launchrule.NewStore: %v", err)
			}
			entry := putClockRule(t, st, "bundled://nightly", launchrule.Spec{Freq: launchrule.FreqDaily, Hour: 2}, true)
			h.runs.launchRules = st
			run := launchrule.Run{ScheduleID: entry.ID, WorkflowID: "wf_classifier"}
			if rErr := st.RecordStart(t.Context(), run); rErr != nil {
				t.Fatalf("RecordStart: %v", rErr)
			}
			id := int64(4243)
			h.bus.pendingPerms.add(id, marotte.NewEvent(marotte.EventPermissionNeeded, chatID,
				marotte.PermissionNeededPayload{RequestID: id}), br)

			h.runs.answerUnattended(chatID, br, id, run, tc.delegated, askName{tool: "fs_write"}, []byte(
				`{"options":[{"optionId":"accept","kind":"allow_once"},{"optionId":"reject","kind":"reject_once"}]`+tc.meta+`}`,
			))

			select {
			case <-br.done:
			case <-time.After(2 * time.Second):
				t.Fatal("the floor answered nothing")
			}
			br.respMu.Lock()
			got := br.response
			br.respMu.Unlock()
			outcome, ok := got.result.(*marotte.PermissionOutcome)
			if !ok {
				t.Fatalf("answered with %T (%v), want a permission outcome", got.result, got.result)
			}
			if outcome.Outcome.OptionID != tc.wantOption {
				t.Errorf("optionId = %q, want %q", outcome.Outcome.OptionID, tc.wantOption)
			}
			if tc.wantOption != "reject" {
				return
			}
			rows := st.Clocks()
			const wantReason = "Kiro's safety check asked a person to approve fs_write with nobody watching. " +
				"Approve it in a chat, or add a permission rule to allow it"
			if len(rows) != 1 || rows[0].LastStatus != launchrule.StatusFailed || rows[0].LastReason != wantReason {
				t.Errorf("schedule rows = %+v, want one failed row reading %q", rows, wantReason)
			}
		})
	}
}

// TestAnswerUnattended_ADenialFailsTheScheduleRow pins that the row reads as a failure naming the tool.
func TestAnswerUnattended_ADenialFailsTheScheduleRow(t *testing.T) {
	h, br := hubForFSTest(t, t.TempDir())
	chatID := runChatID("wf_deny")
	h.insertBridgeNow(chatID, testBridge(br, bridgeIdle))

	st, _, err := launchrule.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("launchrule.NewStore: %v", err)
	}
	entry := putClockRule(t, st, "bundled://nightly", launchrule.Spec{Freq: launchrule.FreqDaily, Hour: 2}, true)
	h.runs.launchRules = st
	// The row tracks the run its fire launched; only that run's outcome lands on it.
	run := launchrule.Run{ScheduleID: entry.ID, WorkflowID: "wf_deny"}
	if rErr := st.RecordStart(t.Context(), run); rErr != nil {
		t.Fatalf("RecordStart: %v", rErr)
	}

	id := int64(90211)
	h.bus.pendingPerms.add(id, marotte.NewEvent(marotte.EventPermissionNeeded, chatID,
		marotte.PermissionNeededPayload{RequestID: id}), br)
	logs := captureLogs(t)

	h.runs.answerUnattended(chatID, br, id, run, false, askName{tool: "execute_bash"},
		[]byte(`{"options":[{"optionId":"reject","kind":"reject_once"}]}`))

	rows := st.Clocks()
	if len(rows) != 1 {
		t.Fatalf("the schedule store holds %d rows, want 1", len(rows))
	}
	const wantReason = "needed approval for execute_bash with nobody watching. Add a permission rule to allow it"
	if rows[0].LastStatus != launchrule.StatusFailed || rows[0].LastReason != wantReason {
		t.Errorf("answerUnattended(deny) row = (%q, %q), want (%q, %q)",
			rows[0].LastStatus, rows[0].LastReason, launchrule.StatusFailed, wantReason)
	}
	// The reader's record of the refusal: the one message, with the refused outcome.
	if got := logs.String(); !strings.Contains(got, `"msg":"`+logMsgUnattendedPermission+`"`) ||
		!strings.Contains(got, `"outcome":"refused"`) {
		t.Errorf("answerUnattended(deny) logged %s, want %q with outcome=refused", got, logMsgUnattendedPermission)
	}
}

// TestAnswerUnattended_APowerDenialNamesItsSubjectOnTheScheduleRow pins that every Power tool asks through
// kiro_powers, so a row naming only the tool cannot say which rule to add.
func TestAnswerUnattended_APowerDenialNamesItsSubjectOnTheScheduleRow(t *testing.T) {
	h, br := hubForFSTest(t, t.TempDir())
	chatID := runChatID("wf_deny")
	h.insertBridgeNow(chatID, testBridge(br, bridgeIdle))

	st, _, err := launchrule.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("launchrule.NewStore: %v", err)
	}
	entry := putClockRule(t, st, "bundled://nightly", launchrule.Spec{Freq: launchrule.FreqDaily, Hour: 2}, true)
	h.runs.launchRules = st
	// The row tracks the run its fire launched; only that run's outcome lands on it.
	run := launchrule.Run{ScheduleID: entry.ID, WorkflowID: "wf_deny"}
	if rErr := st.RecordStart(t.Context(), run); rErr != nil {
		t.Fatalf("RecordStart: %v", rErr)
	}

	id := int64(90213)
	h.bus.pendingPerms.add(id, marotte.NewEvent(marotte.EventPermissionNeeded, chatID,
		marotte.PermissionNeededPayload{RequestID: id}), br)
	logs := captureLogs(t)

	params := []byte(`{"options":[{"optionId":"reject","kind":"reject_once"}],"_meta":{"kiro":{"toolId":"kiro_powers",
		"consent":{"capability":"power","resource":"github/github/create_pr"}}}}`)
	h.runs.answerUnattended(chatID, br, id, run, false, permissionAskName(params), params)

	rows := st.Clocks()
	if len(rows) != 1 {
		t.Fatalf("the schedule store holds %d rows, want 1", len(rows))
	}
	const wantReason = "needed approval for kiro_powers (github/github/create_pr) with nobody watching. " +
		"Add a permission rule to allow it"
	if rows[0].LastReason != wantReason {
		t.Errorf("answerUnattended(Power deny) row reason = %q, want %q", rows[0].LastReason, wantReason)
	}
	if got := logs.String(); !strings.Contains(got, `"tool":"kiro_powers"`) ||
		!strings.Contains(got, `"subject":"github/github/create_pr"`) {
		t.Errorf("answerUnattended(Power deny) logged %s, want tool=kiro_powers and subject=github/github/create_pr", got)
	}
}

// An ask whose run host session ended while the floor's refusal was being written was refused by nobody, so the
// schedule row is not failed with a "nobody watching" reason that never reached the run.
func TestAnswerUnattended_ADenialTheAskWasWithdrawnUnderLeavesTheScheduleRow(t *testing.T) {
	h, _ := hubForFSTest(t, t.TempDir())
	chatID := runChatID("wf_deny")
	br := &duringWriteBridge{fakeBridge: newFakeBridge()}
	br.during = func() { h.bus.endAsksOf(br) }
	h.insertBridgeNow(chatID, testBridge(br, bridgeIdle))

	st, _, err := launchrule.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("launchrule.NewStore: %v", err)
	}
	entry := putClockRule(t, st, "bundled://nightly", launchrule.Spec{Freq: launchrule.FreqDaily, Hour: 2}, true)
	h.runs.launchRules = st
	// The row tracks the run its fire launched; only that run's outcome lands on it.
	run := launchrule.Run{ScheduleID: entry.ID, WorkflowID: "wf_deny"}
	if rErr := st.RecordStart(t.Context(), run); rErr != nil {
		t.Fatalf("RecordStart: %v", rErr)
	}

	id := int64(90212)
	h.bus.pendingPerms.add(id, marotte.NewEvent(marotte.EventPermissionNeeded, chatID,
		marotte.PermissionNeededPayload{RequestID: id}), br)
	logs := captureLogs(t)

	h.runs.answerUnattended(chatID, br, id, run, false, askName{tool: "execute_bash"},
		[]byte(`{"options":[{"optionId":"reject","kind":"reject_once"}]}`))

	rows := st.Clocks()
	if len(rows) != 1 {
		t.Fatalf("the schedule store holds %d rows, want 1", len(rows))
	}
	if rows[0].LastStatus != "started" || rows[0].LastReason != "" {
		t.Errorf("answerUnattended(deny, withdrawn) row = (%q, %q), want it untouched (started)", rows[0].LastStatus, rows[0].LastReason)
	}
	// A refusal that reached nobody answered nothing, so no line may say it did.
	if got := logs.String(); strings.Contains(got, logMsgUnattendedPermission) {
		t.Errorf("answerUnattended(deny, withdrawn) logged %s, want no %q line", got, logMsgUnattendedPermission)
	}
}

// jsonString quotes s as a JSON literal, so a case can carry a bidi control readably.
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// answerTriggered runs the floor's answer for an ask of a triggered run, rule-1's wf_trig, with
// scheduled_auto_approve set to autoApprove, and returns the answer and the confirmer that saw the row writes.
func answerTriggered(t *testing.T, autoApprove bool, params string) (*marotte.PermissionOutcome, *fakeConfirmer) {
	t.Helper()
	dir := t.TempDir()
	body, err := json.Marshal(map[string]any{settings.KeyScheduledAutoApprove: autoApprove})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, t.TempDir(), WithConfigDir(dir))
	fc := &fakeConfirmer{}
	h.runs.confirmer = fc
	chatID := runChatID("wf_trig")
	h.insertBridgeNow(chatID, testBridge(br, bridgeIdle))
	h.runs.grantLease(t.Context(), "wf_trig", "triage", eventLaunch(), hostSec())
	l, held := h.runs.lease("wf_trig")
	if !held || !l.Unattended {
		t.Fatalf("triggered lease = %+v, %v; want it held and unattended, or the floor never arms", l, held)
	}

	id := int64(5150)
	h.bus.pendingPerms.add(id, marotte.NewEvent(marotte.EventPermissionNeeded, chatID,
		marotte.PermissionNeededPayload{RequestID: id}), br)
	h.runs.answerUnattended(chatID, br, id, ruleRun(&l), false, askName{tool: "execute_bash"}, []byte(params))

	select {
	case <-br.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the floor answered nothing for a triggered run")
	}
	br.respMu.Lock()
	got := br.response
	br.respMu.Unlock()
	outcome, ok := got.result.(*marotte.PermissionOutcome)
	if !ok {
		t.Fatalf("answered with %T (%v), want a permission outcome", got.result, got.result)
	}
	return outcome, fc
}

const triggeredAskOptions = `"options":[{"optionId":"accept","kind":"allow_once"},{"optionId":"reject","kind":"reject_once"}]`

// scheduled_auto_approve covers triggered runs: on, the ask is approved once; off, it is refused and the refusal
// reaches the rule's row through the confirmer.
func TestTriggeredRunAutoApprovedWhenOn(t *testing.T) {
	t.Run("on", func(t *testing.T) {
		outcome, fc := answerTriggered(t, true, `{`+triggeredAskOptions+`}`)
		if outcome.Outcome.OptionID != "accept" {
			t.Errorf("optionId = %q, want accept with auto-approve on", outcome.Outcome.OptionID)
		}
		if got := fc.count("RecordOutcome"); got != 0 {
			t.Errorf("an approval wrote the row: %q", fc.recorded())
		}
	})
	t.Run("off", func(t *testing.T) {
		outcome, fc := answerTriggered(t, false, `{`+triggeredAskOptions+`}`)
		if outcome.Outcome.OptionID != "reject" {
			t.Errorf("optionId = %q, want reject with auto-approve off", outcome.Outcome.OptionID)
		}
		want := "RecordOutcome rule-1 wf_trig failed needed approval for execute_bash with nobody watching"
		if got := fc.count(want); got != 1 {
			t.Errorf("confirmer calls %q, want one starting %q", fc.recorded(), want)
		}
	})
}

// An administrator ask needs a person, for a triggered run as for a scheduled one.
func TestTriggeredAdminAskStillRefused(t *testing.T) {
	outcome, _ := answerTriggered(t, true, `{`+triggeredAskOptions+`,"_meta":{"kiro":{"consent":{"scope":"administration"}}}}`)
	if outcome.Outcome.OptionID != "reject" {
		t.Errorf("optionId = %q, want reject: an administrator ask was auto-approved in a triggered run",
			outcome.Outcome.OptionID)
	}
}

// The deny note is read by the step's agent, which a triggered run has as well: it must not call the run scheduled.
func TestTriggeredDenyNoteNamesNoSchedule(t *testing.T) {
	outcome, _ := answerTriggered(t, false, `{`+triggeredAskOptions+`}`)
	var note string
	if outcome.Meta != nil {
		note = outcome.Meta.Kiro.RejectionReason
	}
	if note == "" || strings.Contains(strings.ToLower(note), "schedule") {
		t.Errorf("deny note = %q, want one that names no schedule", note)
	}
}

// A triggered run paused with nobody to resume it is ended like a scheduled one, and its line says which origin and
// rule it was.
func TestTriggeredPauseEndsUnattended(t *testing.T) {
	logs := captureLogs(t)
	fastUnattendedPause(t)
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowCancel:  json.RawMessage(`{}`),
		methodKiroWorkflowInspect: inspectPaused(t, "wf_1", stepQuestionReason),
	}
	fc := &fakeConfirmer{}
	h.runs.confirmer = fc
	h.insertBridgeNow(runChatID("wf_1"), testBridge(br, bridgeIdle))
	h.runs.grantLease(t.Context(), "wf_1", "triage", eventLaunch(), hostSec())
	h.runs.armDeadline(t.Context(), "wf_1")

	pauseRun(t, h, "wf_1", stepQuestionReason)

	if got := waitForEndReason(h, "wf_1", 5*time.Second); got != runEndUnattended {
		t.Fatalf("a triggered run paused with nobody to resume it recorded %q, want %q; calls were %v",
			got, runEndUnattended, br.callLog())
	}
	lines := unattendedEndLines(logs)
	if len(lines) != 1 {
		t.Fatalf("%d %q lines, want 1: %s", len(lines), logMsgRunPausedUnattended, logs.String())
	}
	if lines[0]["origin"] != "triggered" || lines[0]["rule_id"] != "rule-1" {
		t.Errorf("the line carries origin=%v rule_id=%v, want triggered and rule-1", lines[0]["origin"], lines[0]["rule_id"])
	}
	deadline := time.Now().Add(5 * time.Second)
	for fc.count("RecordOutcome rule-1 wf_1 failed paused with nobody watching") == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if fc.count("RecordOutcome rule-1 wf_1 failed paused with nobody watching") != 1 {
		t.Errorf("confirmer calls %q, want the pause's reason sent to the rule's row", fc.recorded())
	}
}
