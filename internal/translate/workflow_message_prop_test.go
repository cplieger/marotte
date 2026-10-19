package translate

import (
	"fmt"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"pgregory.net/rapid"
)

// kasBufferModel is KAS 2.32.0's handling of the messages a step sends the chat, from deliverSendMessageAdmitted (its
// info-wake busy fallback included), clearSteeringAtTurnBoundary and cancelLocalTurn: the oracle of what reached the
// agent.
type kasBufferModel struct {
	severities []string
	delivered  []bool
	// buffer is the messages KAS holds for the chat, in append order.
	buffer []int
	// waker is the message whose wake prompt is reserved, or -1.
	waker int
	// rewake is the reason of the turn KAS queued to re-deliver what a cancel cleared, or "".
	rewake string
	busy   bool
	// wakeTurn is an open turn KAS started for a message's notify.
	wakeTurn bool
}

// messageText is the words of the model's message i.
func messageText(i int) string { return fmt.Sprintf("message %d", i) }

// rowDelivered reports whether the chat's log shows row delivered: read at its send, or settled read.
func rowDelivered(row steerRow, stamps []marotte.EntrySteerDelivered) bool {
	if row.ReadTs != 0 {
		return true
	}
	return slices.ContainsFunc(stamps, func(d marotte.EntrySteerDelivered) bool { return !d.Cleared })
}

// Over any order of sends, wakes, take-ups, turn ends and stops, a workflow message never reads delivered unless KAS
// delivered it (its reserved wake prompt aside, which KAS runs next), never stays waiting once a clear of its session
// followed it, and is settled at most once in one row.
func TestWorkflowMessage_SettlementsFollowOnlyWhatKASDid(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		deps, tr := messageDeps(t)
		k := &kasBufferModel{waker: -1}
		clearedUpTo := 0

		startTurn := func(reason string) {
			turnStartOn(t, tr, FrameAttribution{})
			deps.turns.chatTurn(testChat)
			k.busy = true
			if reason != "" {
				wakeChunk(t, tr, reason, FrameAttribution{})
			}
		}
		clear := func() {
			ids := make([]string, 0, len(k.buffer))
			for _, i := range k.buffer {
				ids = append(ids, fmt.Sprintf("notify-%d", i))
			}
			k.buffer = nil
			clearSteering(t, tr, ids...)
			clearedUpTo = len(k.severities)
		}

		for range rapid.IntRange(1, 40).Draw(rt, "steps") {
			actions := []string{"send"}
			if !k.busy {
				actions = append(actions, "start")
			}
			if k.busy {
				actions = append(actions, "frame", "end", "stop")
				if len(k.buffer) > 0 {
					actions = append(actions, "take-up")
				}
			}
			switch rapid.SampledFrom(actions).Draw(rt, "action") {
			case "send":
				i := len(k.severities)
				sev := rapid.SampledFrom([]string{"info", "warning", "success"}).Draw(rt, "severity")
				k.severities, k.delivered = append(k.severities, sev), append(k.delivered, false)
				stepReport(t, tr, sev, messageText(i))
				switch {
				case sev == severitySuccess || k.busy || k.waker >= 0 || k.rewake != "":
					k.buffer = append(k.buffer, i)
				case sev == "info" && rapid.Bool().Draw(rt, "busy fallback"):
					// The session turned busy during the notify's persist: KAS buffers the info wake.
					k.buffer = append(k.buffer, i)
				default:
					k.waker = i
				}
			case "start":
				switch {
				case k.rewake != "":
					startTurn(k.rewake)
					k.rewake = ""
				case k.waker >= 0:
					k.delivered[k.waker], k.waker, k.wakeTurn = true, -1, true
					startTurn("send-message-wake")
				default:
					startTurn("")
				}
			case "frame":
				if k.wakeTurn {
					wakeChunk(t, tr, "send-message-wake", FrameAttribution{})
				}
			case "take-up":
				i := k.buffer[0]
				k.buffer = k.buffer[1:]
				k.delivered[i] = true
				tr.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "steering_injected", map[string]any{
					"messageId": fmt.Sprintf("notify-%d", i), "content": fmt.Sprintf("[notification/%s] %s", k.severities[i], messageText(i)),
					"notificationSeverity": k.severities[i],
				}), FrameAttribution{})
			case "end":
				if len(k.buffer) > 0 {
					clear()
				}
				closeChatTurn(t, deps, testChat)
				k.busy, k.wakeTurn = false, false
			case "stop":
				stopTurn(deps, testChat)
				if len(k.buffer) > 0 {
					for _, i := range k.buffer {
						k.delivered[i] = true
					}
					clear()
					k.rewake = rapid.SampledFrom([]string{"notification-rewake", "send-message-wake"}).Draw(rt, "rewake")
				}
				resumeAfterStop(deps)
				closeChatTurn(t, deps, testChat)
				k.busy, k.wakeTurn = false, false
			}

			rows := chatLogRows(t, deps)
			if len(rows) != len(k.severities) {
				rt.Fatalf("chat rows = %d, want one per message sent (%d)", len(rows), len(k.severities))
			}
			stamps := chatLogStamps(t, deps)
			for i, r := range rows {
				d := stamps[r.ID]
				if len(d) > 1 {
					rt.Fatalf("message %d settled %d times: %+v", i, len(d), d)
				}
				if rowDelivered(r, d) && !k.delivered[i] && i != k.waker {
					rt.Fatalf("message %d (%s) reads delivered, but KAS never delivered it: %+v", i, k.severities[i], d)
				}
				if i < clearedUpTo && len(d) == 0 && r.ReadTs == 0 {
					rt.Fatalf("message %d still waits after a clear of its session followed it", i)
				}
			}
		}
	})
}
