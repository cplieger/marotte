package translate

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// Each ask is notified with what it asks, under the tab it belongs to: a step's ask under its
// run, whichever chat's bridge carried it.
func TestAsks_notifyWhatTheyAskUnderTheirTab(t *testing.T) {
	id := int64(9)
	stepMeta := map[string]any{"kiro": map[string]any{
		"workflowWatch": map[string]any{"workflowId": "wf1", "nodeId": "builder"},
	}}
	tests := []struct {
		name string
		run  func(tr *Translator, t *testing.T)
		want marotte.NotificationPayload
	}{
		{"tool permission", func(tr *Translator, t *testing.T) {
			tr.HandlePermissionRequest(t.Context(), "c1", nopOrigin{}, &marotte.RPCResponse{ID: &id, Params: mustJSON(t, map[string]any{
				"sessionId": "s", "toolCall": map[string]any{"toolCallId": "tc", "title": "git push --force"},
			})})
		}, marotte.NotificationPayload{
			PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindPermission,
			Title: "chat c1", Body: "Permission required · git push --force",
		}},
		{"turn approval", func(tr *Translator, t *testing.T) {
			tr.HandlePermissionRequest(t.Context(), "c1", nopOrigin{}, &marotte.RPCResponse{ID: &id, Params: mustJSON(t, map[string]any{
				"sessionId": "s", "toolCall": map[string]any{"toolCallId": "tc", "title": "Review"},
				"_meta": map[string]any{"kiro": map[string]any{"type": "turn_approval", "files": []map[string]any{
					{"path": "/w/a.go"}, {"path": "/w/b.go"},
				}}},
			})})
		}, marotte.NotificationPayload{
			PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindPermission,
			Title: "chat c1", Body: "Permission required · Review 2 changed files",
		}},
		{"a step's permission on its parent chat's bridge", func(tr *Translator, t *testing.T) {
			tr.HandlePermissionRequest(t.Context(), "c1", nopOrigin{}, &marotte.RPCResponse{ID: &id, Params: mustJSON(t, map[string]any{
				"sessionId": "s", "toolCall": map[string]any{"toolCallId": "tc", "title": "make deploy"}, "_meta": stepMeta,
			})})
		}, marotte.NotificationPayload{
			PushSubject: marotte.RunSubject("wf1"), Kind: marotte.PushKindPermission,
			Title: "nightly", Body: "Permission required · builder: make deploy",
		}},
		{"question", func(tr *Translator, t *testing.T) {
			tr.HandleUserInput(t.Context(), "c1", nopOrigin{}, userInputMsg(t, &id, map[string]any{"question": "Which branch?"}))
		}, marotte.NotificationPayload{
			PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindPermission,
			Title: "chat c1", Body: "Input required · Which branch?",
		}},
		{"elicitation on a parentless run's bridge", func(tr *Translator, t *testing.T) {
			tr.HandleElicitationCreate(t.Context(), "run:wf1", nopOrigin{}, &marotte.RPCResponse{ID: &id, Params: mustJSON(t, map[string]any{
				"elicitation": map[string]any{"mode": "form", "message": "API token for staging"},
			})})
		}, marotte.NotificationPayload{
			PushSubject: marotte.RunSubject("wf1"), Kind: marotte.PushKindPermission,
			Title: "nightly", Body: "Input required · API token for staging",
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps := newBaseDeps()
			deps.runLabels = map[string]string{"wf1": "nightly"}
			tc.run(New(rolesOf(deps)), t)
			if len(deps.notices) != 1 {
				t.Fatalf("notifications = %+v, want one", deps.notices)
			}
			if got := deps.notices[0]; got != tc.want {
				t.Errorf("notification = %+v, want %+v", got, tc.want)
			}
		})
	}
}
