package translate

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
)

func systemNotifyFrame(t *testing.T, level, message string) *marotte.RPCResponse {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"level": level, "message": message})
	if err != nil {
		t.Fatal(err)
	}
	return &marotte.RPCResponse{Method: "_kiro/system/notify", Params: raw}
}

func TestHandleSystemNotify_KeepsTheLevelAndTheBridgesChat(t *testing.T) {
	cases := []struct {
		name, level string
		want        marotte.NoticeLevel
	}{
		{name: "info", level: "info", want: marotte.NoticeInfo},
		{name: "warning", level: "warning", want: marotte.NoticeWarning},
		{name: "error", level: "error", want: marotte.NoticeError},
		{name: "empty", level: "", want: marotte.NoticeInfo},
		{name: "unknown", level: "debug", want: marotte.NoticeInfo},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, events, _ := depsWithStore(t, "c1")
			const msg = "The model response paused unexpectedly. Waiting for it to resume…"
			New(rolesOf(deps)).HandleSystemNotify(t.Context(), "c1", systemNotifyFrame(t, tc.level, msg))
			if len(*events) != 1 {
				t.Fatalf("HandleSystemNotify(%q) broadcast %d events, want 1", tc.level, len(*events))
			}
			e := (*events)[0]
			if e.Type != marotte.EventSystemNotice || e.ChatID != "c1" {
				t.Fatalf("event = %q on %q, want system_notice on c1", e.Type, e.ChatID)
			}
			p, ok := e.Payload.(marotte.SystemNoticePayload)
			if !ok {
				t.Fatalf("payload type = %T", e.Payload)
			}
			if p.Level != tc.want || p.Message != msg {
				t.Errorf("payload = %+v, want level %q and the message verbatim", p, tc.want)
			}
		})
	}
}

func TestHandleSystemNotify_DropsAnEmptyMessage(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	New(rolesOf(deps)).HandleSystemNotify(t.Context(), "c1", systemNotifyFrame(t, "warning", ""))
	if len(*events) != 0 {
		t.Errorf("broadcast %d events for an empty message, want 0", len(*events))
	}
}

func TestHandleSystemNotify_CarriesTheChatsNameWhenRaised(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	tr.HandleSystemNotify(t.Context(), "c1", systemNotifyFrame(t, "warning", "High load"))
	tr.HandleSystemNotify(t.Context(), "run:wf_1", systemNotifyFrame(t, "warning", "High load"))
	if len(*events) != 2 {
		t.Fatalf("broadcast %d events, want 2", len(*events))
	}
	if got := (*events)[0].Payload.(marotte.SystemNoticePayload).ChatName; got != "A" {
		t.Errorf("chat notice ChatName = %q, want the record's name %q", got, "A")
	}
	if got := (*events)[1].Payload.(marotte.SystemNoticePayload).ChatName; got != "" {
		t.Errorf("run notice ChatName = %q, want empty", got)
	}
}

func TestHandleSystemNotify_NamesAChatWhoseRecordWentBeforeItsBridge(t *testing.T) {
	store, err := chat.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "Release notes"; return true }); err != nil {
		t.Fatalf("seed: %v", err)
	}
	deps, events := newEventCaptureDeps()
	deps.store = store
	tr := New(rolesOf(deps))
	if _, err := store.Delete(t.Context(), "c1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	tr.HandleSystemNotify(t.Context(), "c1", systemNotifyFrame(t, "warning", "High load"))
	if len(*events) != 1 {
		t.Fatalf("broadcast %d events, want 1", len(*events))
	}
	if got := (*events)[0].Payload.(marotte.SystemNoticePayload).ChatName; got != "Release notes" {
		t.Errorf("HandleSystemNotify after the record's removal: ChatName = %q, want %q", got, "Release notes")
	}
}
