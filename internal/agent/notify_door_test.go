package agent

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/notify"
)

type doorRig struct {
	door      notifyDoor
	delivered []marotte.ChatID
	froms     []*marotte.Caller
	answer    error
	draining  bool
	ready     bool
}

func newDoorRig(headers ...marotte.ChatHeader) *doorRig {
	rig := &doorRig{ready: true}
	rig.door = notifyDoor{
		enter: func() (func(), bool) {
			if rig.draining {
				return nil, false
			}
			return func() {}, true
		},
		engineReady: func() bool { return rig.ready },
		chats:       func(context.Context) []marotte.ChatHeader { return headers },
		deliver: func(_ context.Context, chatID marotte.ChatID, _ string, from *marotte.Caller) (command.NotifyDelivery, error) {
			rig.delivered = append(rig.delivered, chatID)
			rig.froms = append(rig.froms, from)
			if rig.answer != nil {
				return "", rig.answer
			}
			return command.NotifySteer, nil
		},
	}
	return rig
}

func header(id, current string, prior ...string) marotte.ChatHeader {
	return marotte.ChatHeader{ID: id, ACPSessionID: current, PriorACPSessionIDs: prior}
}

func TestNotifyDoor_FindsTheChatBySession(t *testing.T) {
	rig := newDoorRig(header("c1", "s-old-other"), header("c2", "s-now", "s-before"))
	got, err := rig.door.Deliver(t.Context(), "s-now", "wake", "grelot")
	if err != nil || got != notify.DeliveredSteer {
		t.Fatalf("Deliver = %q, %v; want steer", got, err)
	}
	if len(rig.delivered) != 1 || rig.delivered[0] != "c2" {
		t.Errorf("delivered into %v, want c2", rig.delivered)
	}
	if from := rig.froms[0]; from == nil || from.Label != "grelot" {
		t.Errorf("caller = %+v, want the source as its label", from)
	}
}

func TestNotifyDoor_SessionLookup(t *testing.T) {
	cases := []struct {
		name    string
		headers []marotte.ChatHeader
		session string
		want    marotte.ChatID
	}{
		{"an earlier session of a chat", []marotte.ChatHeader{header("c1", "s2", "s1")}, "s1", "c1"},
		{
			"the current holder wins over a chat that ran on it before",
			[]marotte.ChatHeader{header("older", "s-older", "s1"), header("c1", "s1")}, "s1", "c1",
		},
		{"no chat", []marotte.ChatHeader{header("c1", "s2")}, "s1", ""},
		{
			"two chats with it only in their history",
			[]marotte.ChatHeader{header("a", "s-a", "s1"), header("b", "s-b", "s1")}, "s1", "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newDoorRig(tc.headers...)
			_, err := rig.door.Deliver(t.Context(), tc.session, "wake", "grelot")
			if tc.want == "" {
				if !errors.Is(err, notify.ErrUnknownSession) || len(rig.delivered) != 0 {
					t.Errorf("Deliver err = %v after %v, want ErrUnknownSession and no delivery", err, rig.delivered)
				}
				return
			}
			if err != nil || len(rig.delivered) != 1 || rig.delivered[0] != tc.want {
				t.Errorf("Deliver err = %v into %v, want %s", err, rig.delivered, tc.want)
			}
		})
	}
}

func TestNotifyDoor_Unavailable(t *testing.T) {
	t.Run("while draining", func(t *testing.T) {
		rig := newDoorRig(header("c1", "s1"))
		rig.draining = true
		if _, err := rig.door.Deliver(t.Context(), "s1", "wake", "grelot"); !errors.Is(err, notify.ErrUnavailable) {
			t.Errorf("err = %v, want ErrUnavailable", err)
		}
	})
	t.Run("while the engine is not ready", func(t *testing.T) {
		rig := newDoorRig(header("c1", "s1"))
		rig.ready = false
		if _, err := rig.door.Deliver(t.Context(), "s1", "wake", "grelot"); !errors.Is(err, notify.ErrUnavailable) {
			t.Errorf("err = %v, want ErrUnavailable", err)
		}
		if len(rig.delivered) != 0 {
			t.Errorf("delivered with the engine not ready")
		}
	})
}

func TestNotifyDoor_MapsRefusals(t *testing.T) {
	t.Run("a chat gone meanwhile is unknown", func(t *testing.T) {
		rig := newDoorRig(header("c1", "s1"))
		rig.answer = command.StatusErrorReason(http.StatusConflict, "chat_not_found", command.ErrChatNotFound)
		if _, err := rig.door.Deliver(t.Context(), "s1", "wake", "grelot"); !errors.Is(err, notify.ErrUnknownSession) {
			t.Errorf("err = %v, want ErrUnknownSession", err)
		}
	})
	t.Run("a saturated chat is unavailable", func(t *testing.T) {
		rig := newDoorRig(header("c1", "s1"))
		rig.answer = command.StatusErrorReason(http.StatusConflict, "full", errors.New("full"))
		if _, err := rig.door.Deliver(t.Context(), "s1", "wake", "grelot"); !errors.Is(err, notify.ErrUnavailable) {
			t.Errorf("err = %v, want ErrUnavailable", err)
		}
	})
	t.Run("refused text names its reason", func(t *testing.T) {
		rig := newDoorRig(header("c1", "s1"))
		rig.answer = command.StatusError(http.StatusBadRequest, errors.New("reads as a system notice"))
		_, err := rig.door.Deliver(t.Context(), "s1", "wake", "grelot")
		refused, ok := errors.AsType[*notify.TextRefusedError](err)
		if !ok || refused.Reason != "reads as a system notice" {
			t.Errorf("err = %v, want a TextRefusedError with the reason", err)
		}
	})
	t.Run("any other failure passes through", func(t *testing.T) {
		rig := newDoorRig(header("c1", "s1"))
		disk := errors.New("disk full")
		rig.answer = disk
		if _, err := rig.door.Deliver(t.Context(), "s1", "wake", "grelot"); !errors.Is(err, disk) {
			t.Errorf("err = %v, want the failure itself", err)
		}
	})
}
