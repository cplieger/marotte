package agent

import (
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// E14 drops a row in every live state, and only a row with no entry yet is handed
// back for the close path's note.
func TestSteerTables_ATeardownDropsEveryLiveState(t *testing.T) {
	s := newSteerHarness(t)
	s.bind()
	var want []string
	s.look(func(rec *steerRecord) {
		for st := range rowRead {
			key := "steer-" + st.name()
			rec.rows = append(rec.rows, dockRow{key: key, kasID: key, text: key, state: st, noted: st == rowCleared})
			if st != rowCleared {
				want = append(want, key)
			}
		}
	})

	unread := s.recs.beginTeardown(s.chat, nil)

	var got []string
	for _, p := range unread {
		got = append(got, p.SteerID)
	}
	if !slices.Equal(got, want) {
		t.Errorf("unread = %v, want %v", got, want)
	}
	s.look(func(rec *steerRecord) {
		if len(rec.rows) != 0 || rec.channel != chanNone || rec.turnID != "" {
			t.Errorf("rows %d channel %v turn %q, want an empty record in NONE", len(rec.rows), rec.channel, rec.turnID)
		}
	})
}

// E10 closes the channel only for the chat's own turn: another turn's end leaves
// every channel state as it was.
func TestSteerTables_OnlyTheOwnTurnsEndClosesTheChannel(t *testing.T) {
	for c := range nChannelStates {
		for _, own := range []bool{true, false} {
			t.Run(c.name()+map[bool]string{true: "-own", false: "-other"}[own], func(t *testing.T) {
				s := newSteerHarness(t)
				s.bind()
				s.look(func(rec *steerRecord) { rec.channel, rec.turnID = c, "t-1" })
				end := "t-other"
				if own {
					end = "t-1"
				}

				s.recs.turnEnded(s.chat, command.SteerTurnEnd{TurnID: end, Source: marotte.TurnSourcePrompt})

				want := c
				if own {
					want = chanNone
				}
				if got := s.channel(); got != want {
					t.Errorf("channel = %v, want %v", got, want)
				}
			})
		}
	}
}

// Every delete event's row cell is the one beginRemoveLocked applies: an idle or
// waiting row leaves at once, a row KAS holds waits for the clear.
func TestSteerTables_ADeleteFollowsItsCell(t *testing.T) {
	for st := range rowRead {
		t.Run(st.name(), func(t *testing.T) {
			s := newSteerHarness(t)
			s.bind()
			s.look(func(rec *steerRecord) {
				rec.rows = append(rec.rows, dockRow{key: "steer-a", kasID: "steer-a", text: "a", state: st})
			})

			needsClear, refuse := s.q.BeginRemove(s.chat, "steer-a", "op-a")

			rule := rowRuleFor(st, deleteEvent(st))
			_, live := s.state("steer-a")
			switch rule {
			case rDeleted:
				if needsClear || refuse != "" || live {
					t.Errorf("BeginRemove = %v, %q, row live %v, want it gone at once", needsClear, refuse, live)
				}
			case rResubmit:
				if !needsClear || refuse != "" || !live {
					t.Errorf("BeginRemove = %v, %q, row live %v, want it held for the clear", needsClear, refuse, live)
				}
			default:
				t.Errorf("rowRuleFor(%v, delete) = %d, want a delete cell", st, rule)
			}
		})
	}
}

func (c channelState) name() string {
	return [nChannelStates]string{"none", "open", "unconfirmed", "probing"}[c]
}

func (s rowState) name() string {
	return [nRowStates]string{"parked", "queued", "outstanding", "waiting", "cleared", "unsent", "read", "done"}[s]
}
