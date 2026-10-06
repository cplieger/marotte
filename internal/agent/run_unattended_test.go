package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/schedule"
	"github.com/cplieger/marotte/internal/settings"
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

// TestIsScheduledRun_ReadsTheLeasesOrigin pins the `scheduled` flag's source: only the launch path can tell a
// scheduled parentless run from a manual one, and the lease records it.
func TestIsScheduledRun_ReadsTheLeasesOrigin(t *testing.T) {
	h := &Runs{}

	if h.IsScheduled("wf_1") {
		t.Error("an unknown run reported scheduled; a manual launch must never be marked")
	}
	h.grantLease(t.Context(), "wf_manual", "publish", manualLaunch())
	if h.IsScheduled("wf_manual") {
		t.Error("a manual run reported scheduled")
	}
	h.grantLease(t.Context(), "wf_1", "publish", scheduledLaunch("sched-1", time.Time{}))
	if !h.IsScheduled("wf_1") {
		t.Error("a scheduled run did not report scheduled")
	}
	if h.IsScheduled("wf_2") {
		t.Error("the origin leaked to another run")
	}
	// Released with the terminal frame, and the flag with it.
	h.releaseLease(t.Context(), "wf_1")
	if h.IsScheduled("wf_1") {
		t.Error("a released run still reported scheduled")
	}
	// An empty id is not a run.
	if h.IsScheduled("") {
		t.Error("the empty workflow id reported scheduled")
	}
}

func TestRunLabel_RebuildsTheLaunchLabelFromTheLease(t *testing.T) {
	h := &Runs{}
	h.grantLease(t.Context(), "wf_manual", "publish", manualLaunch())
	h.grantLease(t.Context(), "wf_sched", "publish", scheduledLaunch("sched-1", time.Time{}))
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
	h := &Runs{leases: store}
	h.grantLease(t.Context(), "wf_1", "nightly", scheduledLaunch("sched-1", time.Time{}))

	// The restart: a new store over the same directory, a runtime that launched nothing.
	reopened, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	after := &Runs{leases: reopened}

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
	if !after.IsScheduled("wf_1") {
		t.Error("the origin did not survive")
	}
}

// TestUnattendedFloor_ArmsNothingForAnUnanswerableAsk pins that the floor answers by request id, so an id-less ask must
// not arm (it would dereference nothing); the ordinary handler still runs.
func TestUnattendedFloor_ArmsNothingForAnUnanswerableAsk(t *testing.T) {
	rs := &Runs{}
	rs.grantLease(t.Context(), "wf_1", "nightly",
		scheduledLaunch("sched-1", time.Now().Add(30*time.Second)))
	if l, held := rs.lease("wf_1"); !held || !l.Unattended {
		t.Fatal("the fixture did not produce the unattended lease the floor reaches through")
	}

	inner := 0
	noteAsk := func(context.Context, marotte.ChatID, *marotte.RPCResponse) { inner++ }
	wrapped := rs.permissionWithUnattendedFloor(noteAsk)

	// A permission frame with no id.
	wrapped(t.Context(), runChatID("wf_1"), &marotte.RPCResponse{
		Method: marotte.MethodRequestPermission,
		ID:     nil,
	})

	if inner != 1 {
		t.Errorf("the ordinary permission handler ran %d times, want 1: the wrapper swallowed the "+
			"frame instead of passing it on", inner)
	}
}

// TestPermissionToolName_PrefersTheMachineAuthoredName walks the six backend ask shapes; a gate on toolId
// would blank five of them.
func TestPermissionToolName_PrefersTheMachineAuthoredName(t *testing.T) {
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
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := permissionToolName([]byte(tc.params)); got != tc.want {
				t.Errorf("permissionToolName() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPermissionToolName_DefusesTheModelsTitle pins that the model composes the title, so a poisoned file can put a
// bidi override in the log and in the schedule row an operator reads.
func TestPermissionToolName_DefusesTheModelsTitle(t *testing.T) {
	t.Parallel()

	t.Run("a bidi override cannot reach the row", func(t *testing.T) {
		t.Parallel()
		// Renders as a harmless find while asking to approve `rm -rf /workspace`.
		const title = "Run \u202ednuof-emaN- ecapskrow/ fr- mr\u202c"
		got := permissionToolName([]byte(`{"toolCall":{"title":` + jsonString(title) + `}}`))
		for _, r := range []rune{'\u202e', '\u202c'} {
			if strings.ContainsRune(got, r) {
				t.Errorf("permissionToolName() = %q still carries U+%04X; the row can name a "+
					"different tool than the one that was refused", got, r)
			}
		}
	})

	t.Run("an unbounded title is capped", func(t *testing.T) {
		t.Parallel()
		// The truncation marker rides outside the cap.
		const ceiling = maxToolNameBytes + len("...")
		got := permissionToolName([]byte(`{"toolCall":{"title":"` + strings.Repeat("a", 4096) + `"}}`))
		if len(got) > ceiling {
			t.Errorf("permissionToolName() returned %d bytes, want at most %d: this value is "+
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
			got := permissionToolName([]byte(`{"toolCall":{"title":` + jsonString(want) + `}}`))
			if got != want {
				t.Errorf("permissionToolName(%q) = %q, want it unchanged", want, got)
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
	} {
		t.Run(name, func(t *testing.T) {
			h, br := hubForFSTest(t, t.TempDir())
			chatID := runChatID("wf_deny")
			h.bridge.mgr.insert(chatID, &sharedBridge{bridge: br, state: bridgeIdle})

			id := int64(90210)
			h.bus.pendingPerms.Add(id, marotte.NewEvent(marotte.EventPermissionNeeded, chatID,
				marotte.PermissionNeededPayload{RequestID: id}))

			h.runs.answerUnattended(chatID, id, "sched-1", "execute_bash",
				[]byte(`{"options":`+tc.options+`}`))

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
	h, br := hubForFSTest(t, t.TempDir())
	dir := t.TempDir()
	body, err := json.Marshal(map[string]any{settings.KeyScheduledAutoApprove: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	h.runs.lifecycle.configDir = dir
	chatID := runChatID("wf_admin")
	h.bridge.mgr.insert(chatID, &sharedBridge{bridge: br, state: bridgeIdle})

	id := int64(4242)
	h.bus.pendingPerms.Add(id, marotte.NewEvent(marotte.EventPermissionNeeded, chatID,
		marotte.PermissionNeededPayload{RequestID: id}))

	h.runs.answerUnattended(chatID, id, "sched-1", "execute_bash", []byte(`{
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

// TestAnswerUnattended_ADenialFailsTheScheduleRow pins that the row reads as a failure naming the tool.
func TestAnswerUnattended_ADenialFailsTheScheduleRow(t *testing.T) {
	h, br := hubForFSTest(t, t.TempDir())
	chatID := runChatID("wf_deny")
	h.bridge.mgr.insert(chatID, &sharedBridge{bridge: br, state: bridgeIdle})

	st, err := schedule.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("schedule.NewStore: %v", err)
	}
	entry := schedule.Entry{
		ID: "sched-1", Source: "bundled://nightly", Enabled: true,
		Spec: schedule.Spec{Freq: schedule.FreqDaily, Hour: 2},
	}
	if pErr := st.Put(t.Context(), &entry); pErr != nil {
		t.Fatalf("Put schedule: %v", pErr)
	}
	h.runs.schedules = st

	id := int64(90211)
	h.bus.pendingPerms.Add(id, marotte.NewEvent(marotte.EventPermissionNeeded, chatID,
		marotte.PermissionNeededPayload{RequestID: id}))

	h.runs.answerUnattended(chatID, id, "sched-1", "execute_bash",
		[]byte(`{"options":[{"optionId":"reject","kind":"reject_once"}]}`))

	rows := st.List()
	if len(rows) != 1 {
		t.Fatalf("the schedule store holds %d rows, want 1", len(rows))
	}
	const wantReason = "needed approval for execute_bash with nobody watching. Add a permission rule to allow it"
	if rows[0].LastStatus != schedule.StatusFailed || rows[0].LastReason != wantReason {
		t.Errorf("answerUnattended(deny) row = (%q, %q), want (%q, %q)",
			rows[0].LastStatus, rows[0].LastReason, schedule.StatusFailed, wantReason)
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
