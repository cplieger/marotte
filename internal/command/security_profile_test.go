package command

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/marotte/internal/testsupport"
)

type reloadRecorder struct{ reloaded []marotte.ChatID }

func (r *reloadRecorder) ReloadChatProfile(chatID marotte.ChatID) {
	r.reloaded = append(r.reloaded, chatID)
}

func profileReq(t *testing.T, chatID marotte.ChatID, profile string) *marotte.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(marotte.SetSecurityProfileCommand{Profile: profile})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &marotte.ClientCommand{Type: marotte.CmdSetSecurityProfile, ChatID: chatID, Payload: payload}
}

func chatProfile(t *testing.T, store chatStore, id marotte.ChatID) string {
	t.Helper()
	c, ok := store.Get(t.Context(), id)
	if !ok {
		t.Fatalf("chat %s is gone", id)
	}
	return c.SecurityProfile
}

func TestCmdSetSecurityProfile_WritesTheHeaderAndReloadsOnlyThatChat(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEmptyChat(t, store, "c1")
	seedEmptyChat(t, store, "c2")
	r := &reloadRecorder{}

	out, err := cmdSetSecurityProfile(t.Context(), store, r, profileReq(t, "c1", policyfile.ProfileReadOnly))

	if err != nil {
		t.Fatalf("cmdSetSecurityProfile = %v", err)
	}
	if got := chatProfile(t, store, "c1"); got != policyfile.ProfileReadOnly {
		t.Errorf("c1 security_profile = %q, want %q", got, policyfile.ProfileReadOnly)
	}
	if got := chatProfile(t, store, "c2"); got != "" {
		t.Errorf("c2 security_profile = %q, want it untouched", got)
	}
	if !slices.Equal(r.reloaded, []marotte.ChatID{"c1"}) {
		t.Errorf("reloaded = %v, want [c1]", r.reloaded)
	}
	raw, _ := json.Marshal(out)
	var resp struct {
		Profile string `json:"profile"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil || resp.Profile != policyfile.ProfileReadOnly {
		t.Errorf("response = %s, want profile %q", raw, policyfile.ProfileReadOnly)
	}
}

func TestCmdSetSecurityProfile_TheSameProfileReloadsNothing(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEmptyChat(t, store, "c1")
	r := &reloadRecorder{}
	if _, err := cmdSetSecurityProfile(t.Context(), store, r, profileReq(t, "c1", policyfile.ProfileTrusted)); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	r.reloaded = nil

	if _, err := cmdSetSecurityProfile(t.Context(), store, r, profileReq(t, "c1", policyfile.ProfileTrusted)); err != nil {
		t.Fatalf("cmdSetSecurityProfile = %v", err)
	}

	if len(r.reloaded) != 0 {
		t.Errorf("reloaded = %v for an unchanged profile, want none", r.reloaded)
	}
}

func TestCmdSetSecurityProfile_RefusesWithoutWritingOrReloading(t *testing.T) {
	for _, tc := range []struct {
		name    string
		chatID  marotte.ChatID
		profile string
		want    int
	}{
		{name: "unknown profile", chatID: "c1", profile: "wide-open", want: http.StatusBadRequest},
		{name: "empty profile", chatID: "c1", profile: "", want: http.StatusBadRequest},
		{name: "run chat", chatID: "run:wf-1", profile: policyfile.ProfileReadOnly, want: http.StatusBadRequest},
		{name: "unknown chat", chatID: "nope", profile: policyfile.ProfileReadOnly, want: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			seedEmptyChat(t, store, "c1")
			r := &reloadRecorder{}

			_, err := cmdSetSecurityProfile(t.Context(), store, r, profileReq(t, tc.chatID, tc.profile))

			if got := statusOf(err); got != tc.want {
				t.Fatalf("status = %d, want %d (err %v)", got, tc.want, err)
			}
			if got := chatProfile(t, store, "c1"); got != "" {
				t.Errorf("c1 security_profile = %q after a refusal, want it untouched", got)
			}
			if _, ok := store.Get(t.Context(), tc.chatID); ok && tc.chatID != "c1" {
				t.Errorf("a refusal created chat %s", tc.chatID)
			}
			if len(r.reloaded) != 0 {
				t.Errorf("reloaded = %v after a refusal, want none", r.reloaded)
			}
		})
	}
}
