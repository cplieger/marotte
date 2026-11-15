package command

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/policyfile"
)

type profileMove struct {
	chatID marotte.ChatID
	to     string
}

// moveRecorder answers every move with found and err, and changed for every found chat.
type moveRecorder struct {
	moves []profileMove
	found bool
	err   error
}

func (r *moveRecorder) MoveChatProfile(_ context.Context, chatID marotte.ChatID, to string) (found, changed bool, err error) {
	r.moves = append(r.moves, profileMove{chatID: chatID, to: to})
	return r.found, r.found && r.err == nil, r.err
}

func profileReq(t *testing.T, chatID marotte.ChatID, profile string) *marotte.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(marotte.SetSecurityProfileCommand{Profile: profile})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &marotte.ClientCommand{Type: marotte.CmdSetSecurityProfile, ChatID: chatID, Payload: payload}
}

func TestCmdSetSecurityProfile_MovesThatChatAndAnswersTheProfile(t *testing.T) {
	r := &moveRecorder{found: true}

	out, err := cmdSetSecurityProfile(t.Context(), r, profileReq(t, "c1", policyfile.ProfileReadOnly))
	if err != nil {
		t.Fatalf("cmdSetSecurityProfile = %v", err)
	}
	if want := []profileMove{{chatID: "c1", to: policyfile.ProfileReadOnly}}; !slices.Equal(r.moves, want) {
		t.Errorf("moves = %+v, want %+v", r.moves, want)
	}
	raw, _ := json.Marshal(out)
	var resp struct {
		Profile string `json:"profile"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil || resp.Profile != policyfile.ProfileReadOnly {
		t.Errorf("response = %s, want profile %q", raw, policyfile.ProfileReadOnly)
	}
}

func TestCmdSetSecurityProfile_RefusesWithoutMoving(t *testing.T) {
	for _, tc := range []struct {
		name    string
		chatID  marotte.ChatID
		profile string
	}{
		{name: "unknown profile", chatID: "c1", profile: "wide-open"},
		{name: "empty profile", chatID: "c1", profile: ""},
		{name: "run chat", chatID: "run:wf-1", profile: policyfile.ProfileReadOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &moveRecorder{found: true}

			_, err := cmdSetSecurityProfile(t.Context(), r, profileReq(t, tc.chatID, tc.profile))

			if got := statusOf(err); got != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (err %v)", got, http.StatusBadRequest, err)
			}
			if len(r.moves) != 0 {
				t.Errorf("moves = %+v after a refusal, want none", r.moves)
			}
		})
	}
}

func TestCmdSetSecurityProfile_AnswersWhatTheMoveFound(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    *moveRecorder
		want int
	}{
		{name: "unknown chat", r: &moveRecorder{}, want: http.StatusNotFound},
		{name: "store failure", r: &moveRecorder{found: true, err: errors.New("disk full")}, want: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cmdSetSecurityProfile(t.Context(), tc.r, profileReq(t, "c1", policyfile.ProfileReadOnly))

			if got := statusOf(err); got != tc.want {
				t.Errorf("status = %d, want %d (err %v)", got, tc.want, err)
			}
		})
	}
}
