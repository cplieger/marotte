package agent

import (
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"pgregory.net/rapid"
)

// Any sequence of frames, commands and jobs leaves the record in a state its tables
// describe: a turn is bound exactly when the channel is not NONE, an outstanding
// probe exists only while PROBING, and UNCONFIRMED holds no un-owned waiting row
// (nothing is outstanding to wait behind; a closed turn's job owns its own).
func TestSteerRecords_InvariantsHoldOverAnySequence(t *testing.T) {
	h, _, _ := newTestHub()
	var chats atomic.Int64
	rapid.Check(t, func(rt *rapid.T) {
		chat := marotte.ChatID("p" + strconv.FormatInt(chats.Add(1), 10))
		s := harnessOn(h, chat, rt.Fatalf)
		keys := 0
		rt.Repeat(map[string]func(*rapid.T){
			"": func(rt *rapid.T) { checkSteerInvariants(rt, s) },
			"bind": func(rt *rapid.T) {
				if s.turnID() == "" {
					s.bind()
				}
			},
			"end": func(rt *rapid.T) {
				s.recs.TurnEnded(chat, command.SteerTurnEnd{
					TurnID: s.turnID(), Source: marotte.TurnSourcePrompt, BridgeDeath: rapid.Bool().Draw(rt, "death"),
				})
			},
			"steer": func(rt *rapid.T) {
				keys++
				key := "steer-k" + strconv.Itoa(keys)
				h := command.SteerHolder{
					Held: rapid.Bool().Draw(rt, "held"), PromptClass: true,
					Live: rapid.Bool().Draw(rt, "live"), Delivering: rapid.Bool().Draw(rt, "delivering"),
				}
				sends, _ := s.q.RouteSteer(chat, key, key, h)
				kasAnswers(rt, s, sends)
			},
			"agent": func(rt *rapid.T) {
				keys++
				s.recs.SteerWaiting(chat, &marotte.SteerQueuedPayload{SteerID: "notify-" + strconv.Itoa(keys), Origin: marotte.SteerOriginAgent})
			},
			"read": func(rt *rapid.T) {
				if ids := s.kasHolds(); len(ids) > 0 {
					id := rapid.SampledFrom(ids).Draw(rt, "read")
					if rapid.Bool().Draw(rt, "byAck") {
						s.recs.SteerForgotten(chat, []string{id})
					} else {
						s.recs.SteerRead(chat, id)
					}
				}
			},
			"foreignClear": func(rt *rapid.T) {
				if ids := s.kasHolds(); len(ids) > 0 {
					s.recs.SteerCleared(chat, ids)
				}
			},
			"remove": func(rt *rapid.T) {
				key := pickRow(rt, s)
				if key == "" {
					return
				}
				op := "op-" + key
				needsClear, refuse := s.q.BeginRemove(chat, key, op)
				if refuse != "" || !needsClear {
					return
				}
				if rapid.Bool().Draw(rt, "readMidOp") {
					if ids := s.kasHolds(); len(ids) > 0 {
						s.recs.SteerRead(chat, rapid.SampledFrom(ids).Draw(rt, "midRead"))
					}
				}
				if rapid.Bool().Draw(rt, "turnEndsMidOp") {
					s.recs.TurnEnded(chat, command.SteerTurnEnd{TurnID: s.turnID(), Source: marotte.TurnSourcePrompt})
				}
				landed := rapid.Bool().Draw(rt, "landed")
				var cleared []string
				if landed {
					cleared = s.clearKAS()
				}
				res := s.q.RemoveCleared(chat, op, cleared, landed)
				if res.Resend != nil {
					s.recs.SteerWaiting(chat, &marotte.SteerQueuedPayload{SteerID: res.Resend.ID, Text: res.Resend.Text})
					s.q.OpSent(chat, op, *res.Resend, rapid.Bool().Draw(rt, "queued"), nil)
				}
				if end := s.q.EndOp(chat, op); end != nil {
					s.q.Unsent(chat, op)
				}
			},
			"discard": func(rt *rapid.T) {
				needsClear, refuse := s.q.BeginDiscard(chat, "op-d")
				if refuse != "" || !needsClear {
					return
				}
				landed := rapid.Bool().Draw(rt, "landed")
				if landed {
					s.clearKAS()
				}
				s.q.DiscardCleared(chat, "op-d", landed)
			},
			"bridgeGone": func(*rapid.T) { s.recs.BridgeGone(chat) },
			"nextParked": func(*rapid.T) { s.q.NextParked(chat) },
			"jobs": func(rt *rapid.T) {
				for _, j := range s.spy.takeJobs() {
					if j.Chat != chat {
						continue
					}
					if j.End == nil {
						kasAnswers(rt, s, s.q.PlanFlush(chat))
						continue
					}
					if rapid.Bool().Draw(rt, "resend") {
						s.q.StraysCleared(chat, j.Owner, nil, true)
						s.q.Resent(chat, j.Owner, j.End.Lead)
						if rapid.Bool().Draw(rt, "opened") {
							s.q.Delivered(chat, j.Owner)
						} else {
							s.q.Unsent(chat, j.Owner)
						}
					} else {
						s.q.Unsent(chat, j.Owner)
					}
				}
			},
		})
	})
}

// kasAnswers folds each send as KAS answers it, queued or refused.
func kasAnswers(rt *rapid.T, s *steerHarness, sends []command.SteerSend) {
	for _, snd := range sends {
		queued := rapid.Bool().Draw(rt, "queued")
		if queued {
			s.recs.SteerWaiting(s.chat, &marotte.SteerQueuedPayload{SteerID: snd.ID, Text: snd.Text})
		}
		s.q.SteerSent(s.chat, snd, queued, nil)
	}
}

func pickRow(rt *rapid.T, s *steerHarness) string {
	var keys []string
	s.look(func(rec *steerRecord) {
		rec.live(func(w *dockRow) { keys = append(keys, w.key) })
	})
	if len(keys) == 0 {
		return ""
	}
	return rapid.SampledFrom(keys).Draw(rt, "row")
}

func checkSteerInvariants(rt *rapid.T, s *steerHarness) {
	s.look(func(rec *steerRecord) {
		if (rec.turnID == "") != (rec.channel == chanNone) {
			rt.Fatalf("turn %q with channel %d: a turn is bound exactly when the channel is not NONE", rec.turnID, rec.channel)
		}
		if rec.probe != "" && rec.channel != chanProbing {
			rt.Fatalf("probe %q outstanding in channel %d", rec.probe, rec.channel)
		}
		if rec.channel == chanUnconfirmed {
			rec.live(func(w *dockRow) {
				if w.state == rowWaiting && w.owner == "" {
					rt.Fatalf("row %s waits in UNCONFIRMED, behind nothing", w.key)
				}
			})
		}
		rec.live(func(w *dockRow) {
			if w.state >= nRowStates {
				rt.Fatalf("row %s in state %d", w.key, w.state)
			}
		})
	})
}
