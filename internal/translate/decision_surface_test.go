package translate

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/runesafe/v2"
)

// A DECISION SURFACE is a payload a human reads to make an approval choice (permission card,
// question card, elicitation form). U+202E reverses the span below so it renders as an
// innocuous `find` while the bytes Allow approves are the rm; U+202C closes the span.
const (
	rlo             = "\u202e"
	pdf             = "\u202c"
	deceptiveTitle  = "Run " + rlo + "dnuof-emaN- ecapskrow/ fr- mr" + pdf
	deceptiveOption = "Reject" + rlo + "wollA" + pdf
)

// assertNeutralizedOnTheWire asserts that the JSON the browser receives carries no direction
// override, marshalled as the SSE writer does.
func assertNeutralizedOnTheWire(t *testing.T, what string, payload any) {
	t.Helper()
	wire, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("%s: marshal payload: %v", what, err)
	}
	// json.Marshal escapes the controls as \u202e: decode before counting runes.
	var decoded any
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("%s: unmarshal payload: %v", what, err)
	}
	for _, r := range flatten(decoded) {
		if runesafe.IsBidiControl(r) {
			t.Errorf("%s: U+%04X survived to the wire; payload=%s", what, r, wire)
		}
		if unicode.IsControl(r) {
			t.Errorf("%s: control U+%04X survived to the wire; payload=%s", what, r, wire)
		}
	}
}

// flatten returns every rune of every string in a decoded JSON value, keys included.
func flatten(v any) []rune {
	var out []rune
	switch t := v.(type) {
	case string:
		out = append(out, []rune(t)...)
	case []any:
		for _, e := range t {
			out = append(out, flatten(e)...)
		}
	case map[string]any:
		for k, e := range t {
			out = append(out, []rune(k)...)
			out = append(out, flatten(e)...)
		}
	}
	return out
}

// TestPermissionCard_NeutralizesADeceptiveTitleOnTheWire pins that the title cannot
// reorder itself and the reversed span is still PRESENT (controls become spaces).
func TestPermissionCard_NeutralizesADeceptiveTitleOnTheWire(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	id := int64(9001)
	tr.HandlePermissionRequest(t.Context(), "c1", &marotte.RPCResponse{
		ID: &id,
		Params: mustJSON(t, map[string]any{
			"sessionId": "sess_x",
			"toolCall": map[string]any{
				"toolCallId": "tc-1",
				"title":      deceptiveTitle,
				"kind":       "execute",
			},
			"options": []map[string]any{
				{"optionId": "allow", "name": "Allow", "kind": "allow_once"},
				{"optionId": "deny", "name": deceptiveOption, "kind": "reject_once"},
			},
		}),
	})

	got, ok := findPermissionNeeded(t, events)
	if !ok {
		t.Fatal("no permission_needed event broadcast")
	}
	assertNeutralizedOnTheWire(t, "permission_needed", got)

	// The reversed text is still there, now readable as the nonsense it is.
	if !strings.Contains(got.Title, "mr") || !strings.Contains(got.Title, "ecapskrow/") {
		t.Errorf("Title = %q: the reversed span was deleted rather than exposed", got.Title)
	}
	if strings.Contains(got.Title, rlo) {
		t.Errorf("Title = %q still carries U+202E", got.Title)
	}
	// The button label is a decision surface too.
	if len(got.Options) != 2 {
		t.Fatalf("len(Options) = %d, want 2", len(got.Options))
	}
	if strings.Contains(got.Options[1].Name, rlo) {
		t.Errorf("Options[1].Name = %q still carries U+202E", got.Options[1].Name)
	}
	// The answer's identifiers are not display text and must arrive byte-identical.
	if got.Options[1].OptionID != "deny" || got.Options[1].Kind != "reject_once" {
		t.Errorf("Options[1] identifiers changed: %+v", got.Options[1])
	}
}

// TestPermissionCard_LeavesLegitimateTitlesByteIdentical pins that pure RTL and unmarked
// mixed text pass untouched (runesafe v1.4.2).
func TestPermissionCard_LeavesLegitimateTitlesByteIdentical(t *testing.T) {
	for _, title := range []string{
		"Write config.tf",
		"Créer le répertoire naïve",
		"מחק את כל הקבצים",  // Hebrew, pure RTL
		"احذف جميع الملفات", // Arabic, pure RTL
		"設定ファイルを書き込む",
		"설정 파일 쓰기",
		"เขียนไฟล์การตั้งค่า",
		"फ़ाइल लिखें",
		"Deploy 🚀 to prod",
		"Write שלום now", // mixed scripts, no explicit marks
	} {
		deps, events := newEventCaptureDeps()
		tr := New(rolesOf(deps))
		id := int64(1)
		tr.HandlePermissionRequest(t.Context(), "c1", &marotte.RPCResponse{
			ID: &id,
			Params: mustJSON(t, map[string]any{
				"sessionId": "s",
				"toolCall":  map[string]any{"toolCallId": "tc", "title": title, "kind": "edit"},
				"options":   []map[string]any{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}},
			}),
		})
		got, ok := findPermissionNeeded(t, events)
		if !ok {
			t.Fatalf("%q: no permission_needed event", title)
		}
		if got.Title != title {
			t.Errorf("Title = %q, want byte-identical %q", got.Title, title)
		}
	}
}

// TestPermissionCard_MixedScriptWithExplicitMarksIsTheWholeCost pins the one accepted loss:
// mixed-script text relying on explicit marks loses them to spaces.
func TestPermissionCard_MixedScriptWithExplicitMarksIsTheWholeCost(t *testing.T) {
	const lrm = "\u200e"
	title := "Write " + lrm + "שלום" + lrm + " now"

	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	id := int64(1)
	tr.HandlePermissionRequest(t.Context(), "c1", &marotte.RPCResponse{
		ID: &id,
		Params: mustJSON(t, map[string]any{
			"sessionId": "s",
			"toolCall":  map[string]any{"toolCallId": "tc", "title": title, "kind": "edit"},
			"options":   []map[string]any{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}},
		}),
	})
	got, _ := findPermissionNeeded(t, events)
	if want := "Write  שלום  now"; got.Title != want {
		t.Errorf("Title = %q, want %q (the two LRMs become spaces)", got.Title, want)
	}
}

// TestUserInputCard_NeutralizesEveryLabelOnTheWire covers every agent-composed label at both
// nesting levels.
func TestUserInputCard_NeutralizesEveryLabelOnTheWire(t *testing.T) {
	base, events := newEventCaptureDeps()
	deps := &pendingCaptureDeps{baseDeps: base}
	tr := New(rolesOf(deps))
	reqID := int64(42)

	tr.HandleUserInput(t.Context(), "c1", userInputMsg(t, &reqID, map[string]any{
		"sessionId": "s",
		"question":  "Apply " + rlo + "?sehctap 41 lla" + pdf,
		"options": []map[string]any{{
			"title":           "Apply" + rlo + "enon" + pdf,
			"description":     "Writes" + rlo + "gnihton" + pdf,
			"subOptionsLabel": "Include" + rlo + ":" + pdf,
			"subOptions": []map[string]any{
				{"title": "Tests" + rlo + "yortsed" + pdf, "description": "Also" + rlo + "setirwrevo" + pdf},
			},
		}},
	}))

	var got *marotte.UserInputNeededPayload
	for _, e := range *events {
		if e.Type == marotte.EventUserInputNeeded {
			p := e.Payload.(marotte.UserInputNeededPayload)
			got = &p
		}
	}
	if got == nil {
		t.Fatal("no user_input_needed event broadcast")
	}
	assertNeutralizedOnTheWire(t, "user_input_needed", *got)

	if len(got.Options) != 1 || len(got.Options[0].SubOptions) != 1 {
		t.Fatalf("shape = %d options / %d sub-options, want 1/1", len(got.Options), len(got.Options[0].SubOptions))
	}
}

// TestUserInputCard_TitleIsSanitizedBeforeTheDropAndDedupRules pins the ORDER: sanitize, then
// trim (an override-only title becomes empty and is dropped), then dedup on the sanitized form.
func TestUserInputCard_TitleIsSanitizedBeforeTheDropAndDedupRules(t *testing.T) {
	base, events := newEventCaptureDeps()
	deps := &pendingCaptureDeps{baseDeps: base}
	tr := New(rolesOf(deps))
	reqID := int64(43)

	tr.HandleUserInput(t.Context(), "c1", userInputMsg(t, &reqID, map[string]any{
		"sessionId": "s",
		"question":  "Pick one",
		"options": []map[string]any{
			{"title": rlo}, // invisible-only: must be DROPPED
			{"title": "Proceed"},
			{"title": "Pro\u200eceed"},   // same rendered text: must be DEDUPED away
			{"title": "\u202dProceed 2"}, // distinct once sanitized: kept
		},
	}))

	var got *marotte.UserInputNeededPayload
	for _, e := range *events {
		if e.Type == marotte.EventUserInputNeeded {
			p := e.Payload.(marotte.UserInputNeededPayload)
			got = &p
		}
	}
	if got == nil {
		t.Fatal("no user_input_needed event broadcast")
	}
	titles := make([]string, len(got.Options))
	for i, o := range got.Options {
		titles[i] = o.Title
	}
	// "Pro ceed" (the LRM's sanitized form) is not "Proceed": the dedup catches identical text only.
	want := []string{"Proceed", "Pro ceed", "Proceed 2"}
	if len(titles) != len(want) {
		t.Fatalf("titles = %q, want %q", titles, want)
	}
	for i := range want {
		if titles[i] != want[i] {
			t.Errorf("titles = %q, want %q", titles, want)
			break
		}
	}
	for _, ti := range titles {
		if strings.TrimSpace(ti) == "" {
			t.Errorf("an invisible-only title survived as %q instead of being dropped", ti)
		}
	}
}

// TestElicitationForm_NeutralizesItsMessageOnTheWire covers the third surface.
// An MCP server is further from marotte's trust than the agent is, and accept /
// decline is an approval choice like any other.
func TestElicitationForm_NeutralizesItsMessageOnTheWire(t *testing.T) {
	base, events := newEventCaptureDeps()
	deps := &pendingCaptureDeps{baseDeps: base}
	tr := New(rolesOf(deps))
	id := int64(77)

	tr.HandleElicitationCreate(t.Context(), "c1", &marotte.RPCResponse{
		ID: &id,
		Params: mustJSON(t, map[string]any{
			"sessionId":  "s",
			"toolCallId": "tc",
			"elicitation": map[string]any{
				"mode":    "form",
				"message": "Share " + rlo + "gnihton" + pdf,
			},
		}),
	})

	var got *marotte.ElicitationNeededPayload
	for _, e := range *events {
		if e.Type == marotte.EventElicitationNeeded {
			p := e.Payload.(marotte.ElicitationNeededPayload)
			got = &p
		}
	}
	if got == nil {
		t.Fatal("no elicitation_needed event broadcast")
	}
	assertNeutralizedOnTheWire(t, "elicitation_needed", *got)
}

// TestDisplayText_BoundsAnUnboundedUpstreamString pins the cap on upstream text.
func TestDisplayText_BoundsAnUnboundedUpstreamString(t *testing.T) {
	// A 3-byte rune repeated, so a naive byte cut would split one.
	long := strings.Repeat("設", 400)
	got := displayText(long)
	if len(got) > maxDisplayTextBytes+len("...") {
		t.Errorf("len = %d bytes, want <= %d", len(got), maxDisplayTextBytes+3)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("a truncated result must end in the cut marker; got %q", got[max(0, len(got)-12):])
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Error("the cut split a rune")
	}
	// Within the cap, byte-identical: no marker on a value that was not cut.
	if short := "Write config.tf"; displayText(short) != short {
		t.Errorf("displayText(%q) = %q, want byte-identical", short, displayText(short))
	}
}

// upstreamConsentReason stands in for KAS's persistableConsentReason (kiro-cli 2.19.1): long,
// naming a file the user never hand-edits, with a Windows shell tail.
const upstreamConsentReason = "Cannot save a rule for this command: the shell pattern would not match. " +
	"Edit ~/.kiro/settings/permissions.yaml by hand, or on Windows run the equivalent from cmd.exe or PowerShell."

// TestPermissionCard_DropsTheUpstreamConsentReason pins that the reason string is dropped at
// the seam: KAS owns the verdict, marotte owns the copy.
func TestPermissionCard_DropsTheUpstreamConsentReason(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	id := int64(9101)
	tr.HandlePermissionRequest(t.Context(), "c1", &marotte.RPCResponse{
		ID: &id,
		Params: mustJSON(t, map[string]any{
			"sessionId": "sess_x",
			"toolCall": map[string]any{
				"toolCallId": "tc-1",
				"title":      "for f in *; do rm $f; done",
				"kind":       "execute",
			},
			"options": []map[string]any{
				{"optionId": "allow", "name": "Allow", "kind": "allow_once"},
			},
			"_meta": map[string]any{"kiro": map[string]any{
				"consent": map[string]any{
					"persistableConsent":       false,
					"persistableConsentReason": upstreamConsentReason,
				},
			}},
		}),
	})

	got, ok := findPermissionNeeded(t, events)
	if !ok {
		t.Fatal("no permission_needed event broadcast")
	}
	// The verdict must have ARRIVED, or the assertions pass vacuously.
	if got.AlwaysAllowBlocked != marotte.AlwaysAllowBlockUnparseable {
		t.Fatalf("AlwaysAllowBlocked = %q, want %q: the verdict did not decode, so the "+
			"reason-is-absent checks below would prove nothing",
			got.AlwaysAllowBlocked, marotte.AlwaysAllowBlockUnparseable)
	}

	wire, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var decoded any
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	// Every string in the payload, keys included, as assertNeutralizedOnTheWire walks it.
	all := string(flatten(decoded))
	for _, fragment := range []string{
		upstreamConsentReason,
		"permissions.yaml",
		"PowerShell",
		"cmd.exe",
		"would not match",
	} {
		if strings.Contains(all, fragment) {
			t.Errorf("the upstream consent reason reached the wire: %q is present; payload=%s", fragment, wire)
		}
	}
}
