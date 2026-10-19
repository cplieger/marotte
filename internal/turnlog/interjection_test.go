package turnlog

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
)

// interjection is one message entry kind, appended into an open turn.
type interjection struct {
	name   string
	append func(ctx context.Context, turn *Turn) ([]Sealed, error)
}

// The message entries that reach a turn on their own clock, whatever the model is streaming.
func interjections() []interjection {
	steer := func(s *marotte.EntrySteer) func(context.Context, *Turn) ([]Sealed, error) {
		return func(ctx context.Context, turn *Turn) ([]Sealed, error) {
			return turn.Steer(ctx, "wfmsg-1", s)
		}
	}
	delivered := func(d *marotte.EntrySteerDelivered) func(context.Context, *Turn) ([]Sealed, error) {
		return func(ctx context.Context, turn *Turn) ([]Sealed, error) {
			return turn.SteerDelivered(ctx, d)
		}
	}
	return []interjection{
		{"a step message received", steer(&marotte.EntrySteer{
			Text: "Task 12: APPROVE", Origin: marotte.SteerOriginStep, Step: "review", ProducedTs: 1,
		})},
		{"a parent message received", steer(&marotte.EntrySteer{
			Text: "also check the tests", Origin: marotte.SteerOriginParent, ProducedTs: 1,
		})},
		{"a read steer", steer(&marotte.EntrySteer{
			Text: "use the other file", Origin: marotte.SteerOriginUser, State: marotte.SteerStateRead,
		})},
		{"a dropped steer", steer(&marotte.EntrySteer{
			Origin: marotte.SteerOriginAgent, State: marotte.SteerStateDropped, Reason: marotte.SteerReasonRestart,
		})},
		{"the receiver's take-up", delivered(&marotte.EntrySteerDelivered{SteerID: "wfmsg-1", KASID: "notify-1", ReadTs: 2})},
		{"the peer's take-up", delivered(&marotte.EntrySteerDelivered{SteerID: "wfmsg-1", ReadTs: 2})},
		{"a clear's settlement", delivered(&marotte.EntrySteerDelivered{SteerID: "wfmsg-1", ReadTs: 2, Cleared: true})},
	}
}

type streamKind struct {
	kind  marotte.EntryKind
	delta func(turn *Turn, ctx context.Context, lane, say, text string) ([]Sealed, error)
}

func streamKinds() []streamKind {
	return []streamKind{
		{marotte.EntryKindThinking, (*Turn).ThinkingDelta},
		{marotte.EntryKindText, (*Turn).TextDelta},
	}
}

// A message entry arriving between two chunks of one say, mid-word, leaves the say one entry
// holding the whole text, and sits before it in seq order.
func TestAMessageEntryDoesNotCutTheStream(t *testing.T) {
	ctx := t.Context()
	for _, sk := range streamKinds() {
		for _, in := range interjections() {
			t.Run(string(sk.kind)+" "+in.name, func(t *testing.T) {
				turn, rec := open(t)
				rec.seal(sk.delta(turn, ctx, "", "f4d9-say", "I need to locate the arrapi agent on Ag"))
				sealed := rec.seal(in.append(ctx, turn))
				rec.seal(sk.delta(turn, ctx, "", "f4d9-say", "ent Mail, register"))
				rec.seal(turn.Close(ctx, marotte.TurnConclusion{Outcome: marotte.TurnOutcomeCompleted}))

				if len(sealed) != 1 || sealed[0].Entry.Lane != "" {
					t.Errorf("the message sealed %d entries, want only itself", len(sealed))
				}
				var says []marotte.Entry
				for _, e := range rec.entries {
					if e.Kind == sk.kind {
						says = append(says, e)
					}
				}
				if len(says) != 1 {
					t.Fatalf("the record holds %d %s entries %v, want one", len(says), sk.kind, rec.ids())
				}
				if says[0].ID != "f4d9-say" {
					t.Errorf("the say's id is %q, want it unsegmented", says[0].ID)
				}
				var payload marotte.EntryText
				if err := json.Unmarshal(says[0].Payload, &payload); err != nil {
					t.Fatalf("parse the say: %v", err)
				}
				if want := "I need to locate the arrapi agent on Agent Mail, register"; payload.Text != want {
					t.Errorf("the say holds %q, want %q", payload.Text, want)
				}
				if sealed[0].Entry.Seq >= says[0].Seq {
					t.Errorf("the message took seq %d and the say %d, want the message at the say's boundary before it",
						sealed[0].Entry.Seq, says[0].Seq)
				}
			})
		}
	}
}

// An interjection leaves every lane's open entry as it was, a delegate's included.
func TestAMessageEntryLeavesEveryLaneOpen(t *testing.T) {
	ctx := t.Context()
	turn, rec := open(t)
	rec.seal(turn.TextDelta(ctx, "", "s1-say", "parent"))
	rec.seal(turn.ThinkingDelta(ctx, "d1", "s2-say", "delegate"))
	before := turn.OpenEntries()
	for _, in := range interjections() {
		rec.seal(in.append(ctx, turn))
	}
	after := turn.OpenEntries()
	if len(after) != len(before) {
		t.Fatalf("open entries went from %+v to %+v", before, after)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("open entry %d went from %+v to %+v", i, before[i], after[i])
		}
	}
}

// A boundary entry still cuts the stream: the interjection rule is the message kinds', not every
// lane-less entry's.
func TestABoundaryEntryStillSealsTheStream(t *testing.T) {
	ctx := t.Context()
	boundaries := map[string]func(*Turn) ([]Sealed, error){
		"a plan": func(turn *Turn) ([]Sealed, error) {
			return turn.Plan(ctx, marotte.EntryPlan{Entries: []marotte.PlanEntry{{Content: "a"}}})
		},
		"a compaction": func(turn *Turn) ([]Sealed, error) { return turn.Compaction(ctx, "summary", 0) },
	}
	for name, boundary := range boundaries {
		t.Run(name, func(t *testing.T) {
			turn, rec := open(t)
			rec.seal(turn.ThinkingDelta(ctx, "", "r1-say", "before "))
			rec.seal(boundary(turn))
			rec.seal(turn.ThinkingDelta(ctx, "", "r1-say", "after"))
			rec.seal(turn.Close(ctx, marotte.TurnConclusion{Outcome: marotte.TurnOutcomeCompleted}))
			if got := strings.Join(rec.ids()[:3:3], ","); !strings.HasPrefix(got, "r1-say,") || !strings.HasSuffix(got, ",r1-say#2") {
				t.Errorf("ids are %s, want the say split around the boundary", got)
			}
		})
	}
}

// The record read back off disk holds the same entries the live appends sealed: one say, the
// message before it.
func TestAMessageEntryMidStreamReadsBackAsOneSay(t *testing.T) {
	ctx := t.Context()
	for _, sk := range streamKinds() {
		t.Run(string(sk.kind), func(t *testing.T) {
			lg, err := chat.OpenEntryLog(ctx, t.TempDir(), chat.NoHeader())
			if err != nil {
				t.Fatalf("open entry log: %v", err)
			}
			defer func() { _ = lg.Close() }()
			opened, err := lg.OpenTurn(ctx, &chat.TurnSpec{
				Source: marotte.TurnOpenNamePrompt,
				Prompt: &marotte.EntryPrompt{ID: "m-1", Text: "go"},
			})
			if err != nil {
				t.Fatalf("open turn: %v", err)
			}
			turn := Open(opened.Turn, lg)
			var live []string
			note := func(sealed []Sealed, err error) {
				t.Helper()
				if err != nil {
					t.Fatalf("append: %v", err)
				}
				for _, s := range sealed {
					live = append(live, string(s.Entry.Kind)+":"+s.Entry.ID)
				}
			}
			note(sk.delta(turn, ctx, "", "f4d9-say", "on Ag"))
			note(turn.Steer(ctx, "wfmsg-1", &marotte.EntrySteer{Text: "APPROVE", Origin: marotte.SteerOriginStep, ProducedTs: 1}))
			note(sk.delta(turn, ctx, "", "f4d9-say", "ent Mail"))
			note(turn.SteerDelivered(ctx, &marotte.EntrySteerDelivered{SteerID: "wfmsg-1", ReadTs: 2}))
			note(sk.delta(turn, ctx, "", "f4d9-say", ", register"))
			note(turn.Close(ctx, marotte.TurnConclusion{Outcome: marotte.TurnOutcomeCompleted}))

			persisted, _, err := lg.TurnPage(opened.Turn, 0)
			if err != nil {
				t.Fatalf("read the turn back: %v", err)
			}
			var disk []string
			for _, e := range persisted[1:] {
				disk = append(disk, string(e.Kind)+":"+e.ID)
			}
			want := []string{"steer:wfmsg-1", "steer_delivered:wfmsg-1:delivered", string(sk.kind) + ":f4d9-say"}
			if got := strings.Join(disk, " "); !strings.HasPrefix(got, strings.Join(want, " ")+" turn_close:") {
				t.Errorf("the record reads back %s, want %v then the close", got, want)
			}
			if strings.Join(disk, " ") != strings.Join(live, " ") {
				t.Errorf("the record reads back %v, the live appends sealed %v", disk, live)
			}
			var payload marotte.EntryText
			if err := json.Unmarshal(persisted[3].Payload, &payload); err != nil || payload.Text != "on Agent Mail, register" {
				t.Errorf("the say reads back %q (%v), want the whole stream", payload.Text, err)
			}
		})
	}
}
