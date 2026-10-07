package translate

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// findPermissionNeeded returns the first permission_needed payload broadcast.
func findPermissionNeeded(t *testing.T, events *[]marotte.ServerEvent) (marotte.PermissionNeededPayload, bool) {
	t.Helper()
	for _, e := range *events {
		if e.Type != marotte.EventPermissionNeeded {
			continue
		}
		p, ok := e.Payload.(marotte.PermissionNeededPayload)
		if !ok {
			t.Fatalf("permission_needed payload type = %T, want marotte.PermissionNeededPayload", e.Payload)
		}
		return p, true
	}
	return marotte.PermissionNeededPayload{}, false
}

// TestHandlePermissionRequest_DecodesFlatParamsAndEnvelopeID pins the v3 decode: FLAT params
// and the correlation id on the envelope (a params-wrapped decode reads all zeros).
func TestHandlePermissionRequest_DecodesFlatParamsAndEnvelopeID(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	id := int64(4242)
	msg := &marotte.RPCResponse{
		ID: &id,
		Params: mustJSON(t, map[string]any{
			"sessionId": "sess_x",
			"toolCall": map[string]any{
				"toolCallId": "tc-9",
				"title":      "Write config.tf",
				"kind":       "edit",
			},
			"options": []map[string]any{
				{"optionId": "allow", "name": "Allow", "kind": "allow_once"},
				{"optionId": "deny", "name": "Deny", "kind": "reject_once"},
			},
		}),
	}
	tr.HandlePermissionRequest(t.Context(), "c1", msg)

	got, ok := findPermissionNeeded(t, events)
	if !ok {
		t.Fatal("no permission_needed event broadcast")
	}
	if got.RequestID != id {
		t.Errorf("RequestID = %d, want %d (must come from the envelope, not params)", got.RequestID, id)
	}
	if got.ToolCallID != "tc-9" {
		t.Errorf("ToolCallID = %q, want tc-9", got.ToolCallID)
	}
	if got.Title != "Write config.tf" {
		t.Errorf("Title = %q, want 'Write config.tf'", got.Title)
	}
	if len(got.Options) != 2 || got.Options[0].OptionID != "allow" || got.Options[1].OptionID != "deny" {
		t.Errorf("Options = %+v, want 2 options [allow, deny]", got.Options)
	}
}

// TestHandlePermissionRequest_MissingIDDropped pins that a request with no
// envelope id is dropped (its outcome could never be routed back to the agent)
// rather than surfaced as an unanswerable dialog.
func TestHandlePermissionRequest_MissingIDDropped(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	msg := &marotte.RPCResponse{ // no ID
		Params: mustJSON(t, map[string]any{
			"sessionId": "s",
			"toolCall":  map[string]any{"toolCallId": "tc", "title": "x", "kind": "edit"},
		}),
	}
	tr.HandlePermissionRequest(t.Context(), "c1", msg)

	if _, ok := findPermissionNeeded(t, events); ok {
		t.Fatal("permission_needed broadcast for a request with no id (should be dropped)")
	}
}

// Turn approval: an ordinary session/request_permission with the file list in `_meta.kiro`.
// A missed discriminator renders a bare Allow/Reject, approving a turn the user never saw.

// turnApprovalParams builds a session/request_permission whose _meta marks it a
// turn approval carrying `files`.
func turnApprovalParams(t *testing.T, files []map[string]any) []byte {
	t.Helper()
	return mustJSON(t, map[string]any{
		"sessionId": "sess_x",
		"toolCall": map[string]any{
			"toolCallId": "tc-turn",
			"title":      "Review changes",
			"kind":       "edit",
		},
		"options": []map[string]any{
			{"optionId": "accept", "name": "Accept", "kind": "allow_once"},
			{"optionId": "reject", "name": "Reject", "kind": "reject_once"},
		},
		"_meta": map[string]any{
			"kiro": map[string]any{
				"type":        "turn_approval",
				"executionId": "exec-1",
				"files":       files,
			},
		},
	})
}

// TestHandlePermissionRequest_TurnApprovalCarriesFiles pins that KAS's ABSOLUTE paths go on the
// wire workspace-relative and its `toolCallId` as `action_id`, the decision map's key.
func TestHandlePermissionRequest_TurnApprovalCarriesFiles(t *testing.T) {
	base, events := newEventCaptureDeps()
	deps := &workDirDeps{baseDeps: base, workDir: "/work"}
	tr := New(rolesOf(deps))

	id := int64(77)
	msg := &marotte.RPCResponse{
		ID: &id,
		Params: turnApprovalParams(t, []map[string]any{
			{"path": "/work/src/a.ts", "snapshotUri": "kiro-snapshot-v2://s:abc/", "toolCallId": "act-1"},
			{"path": "/work/src/b.ts", "toolCallId": "act-2"},
		}),
	}
	tr.HandlePermissionRequest(t.Context(), "c1", msg)

	got, ok := findPermissionNeeded(t, events)
	if !ok {
		t.Fatal("no permission_needed event broadcast")
	}
	if len(got.Files) != 2 {
		t.Fatalf("Files length = %d, want 2: %+v", len(got.Files), got.Files)
	}
	want := []marotte.ApprovalFile{
		{Path: "src/a.ts", SnapshotURI: "kiro-snapshot-v2://s:abc/", ActionID: "act-1"},
		{Path: "src/b.ts", ActionID: "act-2"},
	}
	for i, w := range want {
		if got.Files[i] != w {
			t.Errorf("Files[%d] = %+v, want %+v", i, got.Files[i], w)
		}
	}
}

// TestHandlePermissionRequest_SharedActionIDPreserved pins that a multi-file rename keeps both
// entries under one action id, so the client renders one undividable row.
func TestHandlePermissionRequest_SharedActionIDPreserved(t *testing.T) {
	base, events := newEventCaptureDeps()
	deps := &workDirDeps{baseDeps: base, workDir: "/work"}
	tr := New(rolesOf(deps))

	id := int64(78)
	msg := &marotte.RPCResponse{
		ID: &id,
		Params: turnApprovalParams(t, []map[string]any{
			{"path": "/work/old.py", "toolCallId": "ren-1"},
			{"path": "/work/new.py", "toolCallId": "ren-1"},
		}),
	}
	tr.HandlePermissionRequest(t.Context(), "c1", msg)

	got, _ := findPermissionNeeded(t, events)
	if len(got.Files) != 2 {
		t.Fatalf("Files length = %d, want 2 (both halves of the rename): %+v", len(got.Files), got.Files)
	}
	if got.Files[0].ActionID != "ren-1" || got.Files[1].ActionID != "ren-1" {
		t.Errorf("ActionIDs = %q/%q, want both ren-1", got.Files[0].ActionID, got.Files[1].ActionID)
	}
}

// TestHandlePermissionRequest_OrdinaryPermissionHasNoFiles pins that only the turn_approval
// type carries files.
func TestHandlePermissionRequest_OrdinaryPermissionHasNoFiles(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	id := int64(79)
	// A DIFFERENT type with files attached: the type decides, not the array.
	msg := &marotte.RPCResponse{
		ID: &id,
		Params: mustJSON(t, map[string]any{
			"sessionId": "sess_x",
			"toolCall":  map[string]any{"toolCallId": "tc-1", "title": "ls -la", "kind": "execute"},
			"options":   []map[string]any{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}},
			"_meta": map[string]any{
				"kiro": map[string]any{
					"type":  "something_else",
					"files": []map[string]any{{"path": "/work/a.ts", "toolCallId": "act-1"}},
				},
			},
		}),
	}
	tr.HandlePermissionRequest(t.Context(), "c1", msg)

	got, ok := findPermissionNeeded(t, events)
	if !ok {
		t.Fatal("no permission_needed event broadcast")
	}
	if got.Files != nil {
		t.Errorf("Files = %+v, want nil for a non-turn_approval request", got.Files)
	}
}

// Always-allow persistability is ABSENT-MEANS-YES: a plain bool would suppress the row on
// every pre-2.19.1 request.

// consentParams builds a shell permission request whose `_meta.kiro` carries
// whatever `consent` object the case wants — or none at all when nil.
func consentParams(t *testing.T, consent map[string]any) []byte {
	t.Helper()
	kiro := map[string]any{}
	if consent != nil {
		kiro["consent"] = consent
	}
	return mustJSON(t, map[string]any{
		"sessionId": "sess_x",
		"toolCall": map[string]any{
			"toolCallId": "tc-sh",
			"title":      "git status",
			"kind":       "execute",
		},
		"options": []map[string]any{
			{"optionId": "allow", "name": "Allow", "kind": "allow_once"},
			{"optionId": "deny", "name": "Deny", "kind": "reject_once"},
		},
		"_meta": map[string]any{"kiro": kiro},
	})
}

// TestHandlePermissionRequest_AbsentConsentIsNotBlocked pins absent consent (older KAS wires and
// the common case) as "the offer stands"; red-check by making PersistableConsent a plain bool.
func TestHandlePermissionRequest_AbsentConsentIsNotBlocked(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	id := int64(3001)
	tr.HandlePermissionRequest(t.Context(), "c1", &marotte.RPCResponse{
		ID:     &id,
		Params: consentParams(t, nil),
	})

	got, ok := findPermissionNeeded(t, events)
	if !ok {
		t.Fatal("no permission_needed event broadcast")
	}
	if got.AlwaysAllowBlocked != "" {
		t.Errorf("AlwaysAllowBlocked = %q, want empty: an absent consent object means PERSISTABLE, "+
			"and reading it as blocked suppresses the Always-allow row on every 2.19.0 request",
			got.AlwaysAllowBlocked)
	}
}

var (
	longPart = "python3 -c '" + strings.Repeat("x", 600) + "'"
	longDir  = strings.Repeat("d", 600)
)

func TestHandlePermissionRequest_ConsentNamesThePartAsked(t *testing.T) {
	for _, tc := range []struct {
		consent map[string]any
		want    *marotte.PermissionConsent
		name    string
	}{
		{
			name: "a compound command's round names its part",
			consent: map[string]any{
				"capability": "shell", "resource": "echo a | head -1", "triggeringResource": "head -1",
				"askType": "implicit",
			},
			want: &marotte.PermissionConsent{Capability: "shell", Subject: "head -1", Resource: "head -1"},
		},
		{
			name:    "a single command is its own subject",
			consent: map[string]any{"capability": "fs_write", "resource": "/w/notes.md"},
			want: &marotte.PermissionConsent{
				Capability: "fs_write", Subject: "/w/notes.md", Resource: "/w/notes.md",
				Folder: "/w/**", FolderResource: "/w/**",
			},
		},
		{
			name:    "a path's folder keeps its raw name in the rule key",
			consent: map[string]any{"capability": "fs_read", "resource": "/w/a\tb/notes.md"},
			want: &marotte.PermissionConsent{
				Capability: "fs_read", Subject: "/w/a b/notes.md", Resource: "/w/a\tb/notes.md",
				Folder: "/w/a b/**", FolderResource: "/w/a\tb/**",
			},
		},
		{
			name:    "a folder that would read the same as the cut subject is not offered",
			consent: map[string]any{"capability": "fs_read", "resource": "/" + longDir + "/notes.md"},
			want: &marotte.PermissionConsent{
				Capability: "fs_read", Subject: ("/" + longDir + "/notes.md")[:512] + "...", Resource: "/" + longDir + "/notes.md",
			},
		},
		{
			name:    "a shell part has no folder",
			consent: map[string]any{"capability": "shell", "resource": "cat /w/notes.md"},
			want:    &marotte.PermissionConsent{Capability: "shell", Subject: "cat /w/notes.md", Resource: "cat /w/notes.md"},
		},
		{
			name:    "a bidi override in the shown subject is defused",
			consent: map[string]any{"capability": "shell", "resource": "rm -rf /w\u202e"},
			want:    &marotte.PermissionConsent{Capability: "shell", Subject: "rm -rf /w ", Resource: "rm -rf /w\u202e"},
		},
		{
			name:    "a multi-line part keeps its newline in the rule key",
			consent: map[string]any{"capability": "shell", "triggeringResource": "git commit -m 'a\nb'"},
			want:    &marotte.PermissionConsent{Capability: "shell", Subject: "git commit -m 'a b'", Resource: "git commit -m 'a\nb'"},
		},
		{
			name:    "a part over the display cap keeps its whole text in the rule key",
			consent: map[string]any{"capability": "shell", "triggeringResource": longPart},
			want:    &marotte.PermissionConsent{Capability: "shell", Subject: longPart[:512] + "...", Resource: longPart},
		},
		{name: "no consent, no subject"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, events := newEventCaptureDeps()
			tr := New(rolesOf(deps))
			id := int64(3010)
			tr.HandlePermissionRequest(t.Context(), "c1", &marotte.RPCResponse{ID: &id, Params: consentParams(t, tc.consent)})
			got, ok := findPermissionNeeded(t, events)
			if !ok {
				t.Fatal("no permission_needed event broadcast")
			}
			if !reflect.DeepEqual(got.Consent, tc.want) {
				t.Errorf("Consent = %+v, want %+v", got.Consent, tc.want)
			}
		})
	}
}

// TestHandlePermissionRequest_PersistableFalseBlocksAlwaysAllow pins the block when no
// candidate pattern would match.
func TestHandlePermissionRequest_PersistableFalseBlocksAlwaysAllow(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	id := int64(3002)
	tr.HandlePermissionRequest(t.Context(), "c1", &marotte.RPCResponse{
		ID: &id,
		Params: consentParams(t, map[string]any{
			"persistableConsent":       false,
			"persistableConsentReason": upstreamConsentReason,
		}),
	})

	got, ok := findPermissionNeeded(t, events)
	if !ok {
		t.Fatal("no permission_needed event broadcast")
	}
	if got.AlwaysAllowBlocked != marotte.AlwaysAllowBlockUnparseable {
		t.Errorf("AlwaysAllowBlocked = %q, want %q",
			got.AlwaysAllowBlocked, marotte.AlwaysAllowBlockUnparseable)
	}
}

// TestHandlePermissionRequest_PersistableTrueIsNotBlocked pins present-and-true as absent.
func TestHandlePermissionRequest_PersistableTrueIsNotBlocked(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	id := int64(3003)
	tr.HandlePermissionRequest(t.Context(), "c1", &marotte.RPCResponse{
		ID: &id,
		Params: consentParams(t, map[string]any{
			"persistableConsent": true,
		}),
	})

	got, ok := findPermissionNeeded(t, events)
	if !ok {
		t.Fatal("no permission_needed event broadcast")
	}
	if got.AlwaysAllowBlocked != "" {
		t.Errorf("AlwaysAllowBlocked = %q, want empty for an explicit persistableConsent:true",
			got.AlwaysAllowBlocked)
	}
}

func TestHandlePermissionRequest_CarriesVerifiedMCPIdentity(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	id := int64(4243)

	tr.HandlePermissionRequest(t.Context(), "c1", &marotte.RPCResponse{
		ID: &id,
		Params: mustJSON(t, map[string]any{
			"sessionId": "sess_x",
			"toolCall": map[string]any{
				"toolCallId": "tc-mcp",
				"title":      "model-authored title",
				"kind":       "other",
			},
			"options": []map[string]any{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}},
			"_meta": map[string]any{"kiro": map[string]any{
				"mcpTool": map[string]any{
					"version":  1,
					"identity": map[string]any{"serverName": "issues", "toolName": "create_issue"},
				},
			}},
		}),
	})

	got, ok := findPermissionNeeded(t, events)
	if !ok {
		t.Fatal("no permission_needed event broadcast")
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	mcp, ok := payload["mcp_tool"].(map[string]any)
	if !ok {
		t.Fatalf("mcp_tool = %T, want an object", payload["mcp_tool"])
	}
	if mcp["server_name"] != "issues" || mcp["tool_name"] != "create_issue" {
		t.Errorf("mcp_tool = %+v, want issues/create_issue", mcp)
	}
}

// An ask the administrator's managed-settings rules raised carries
// consent.scope "administration", and the card has to say a person must answer it.
func TestHandlePermissionRequest_MarksAnAdministratorAsk(t *testing.T) {
	for _, tc := range []struct {
		name  string
		scope string
		want  bool
	}{
		{name: "administration", scope: "administration", want: true},
		{name: "user_scope", scope: "user"},
		{name: "no_consent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, events := newEventCaptureDeps()
			tr := New(rolesOf(deps))
			id := int64(7)
			params := map[string]any{
				"sessionId": "sess_x",
				"toolCall":  map[string]any{"toolCallId": "tc-1", "title": "rm x", "kind": "execute"},
				"options":   []map[string]any{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}},
			}
			if tc.scope != "" {
				params["_meta"] = map[string]any{"kiro": map[string]any{"consent": map[string]any{"scope": tc.scope, "askType": "explicit"}}}
			}
			tr.HandlePermissionRequest(t.Context(), "c1", &marotte.RPCResponse{ID: &id, Params: mustJSON(t, params)})
			got, ok := findPermissionNeeded(t, events)
			if !ok {
				t.Fatal("no permission_needed event broadcast")
			}
			if got.AdminRequired != tc.want {
				t.Errorf("AdminRequired = %v for consent scope %q, want %v", got.AdminRequired, tc.scope, tc.want)
			}
		})
	}
}

// KAS reads a deny note only on the ordinary tool approval (the ask carrying
// `_meta.kiro.toolId`) and only through its reject_once option, so the card
// offers the note box exactly there.
func TestHandlePermissionRequest_AcceptsRejectionReason(t *testing.T) {
	ordinary := []map[string]any{
		{"optionId": "allow", "name": "Allow", "kind": "allow_once"},
		{"optionId": "deny", "name": "Deny", "kind": "reject_once"},
	}
	for name, tc := range map[string]struct {
		kiro    map[string]any
		options []map[string]any
		want    bool
	}{
		"a tool approval with a reject option": {
			kiro: map[string]any{"toolId": "execute_bash"}, options: ordinary, want: true,
		},
		"a turn approval": {
			kiro: map[string]any{"toolId": "x", "type": "turn_approval"}, options: ordinary,
		},
		"a hook approval carries no toolId": {
			kiro: map[string]any{"hookName": "pre-commit"}, options: ordinary,
		},
		"no reject_once option": {
			kiro: map[string]any{"toolId": "execute_bash"},
			options: []map[string]any{
				{"optionId": "allow", "name": "Allow", "kind": "allow_once"},
				{"optionId": "never", "name": "Always deny", "kind": "reject_always"},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			deps, events := newEventCaptureDeps()
			tr := New(rolesOf(deps))
			id := int64(5)
			tr.HandlePermissionRequest(t.Context(), "c1", &marotte.RPCResponse{
				ID: &id,
				Params: mustJSON(t, map[string]any{
					"sessionId": "sess_x",
					"toolCall":  map[string]any{"toolCallId": "tc-1", "title": "Run", "kind": "execute"},
					"options":   tc.options,
					"_meta":     map[string]any{"kiro": tc.kiro},
				}),
			})
			got, ok := findPermissionNeeded(t, events)
			if !ok {
				t.Fatal("no permission_needed event broadcast")
			}
			if got.AcceptsRejectionReason != tc.want {
				t.Errorf("AcceptsRejectionReason = %v, want %v", got.AcceptsRejectionReason, tc.want)
			}
		})
	}
}
