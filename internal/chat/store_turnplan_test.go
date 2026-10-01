package chat

import (
	"context"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestUpsertTurnPlan_SteerDoesNotBoundTheTurn pins the half of the steer rule that
// lives in the store rather than in the projection: "this turn" is the tail up to the
// first PROMPT, so a steer persisted mid-turn must not stop the walk. Reverting the
// predicate to `Role == RoleUser` appends a second plan row for one turn, which is
// exactly the N-snapshots-per-plan defect UpsertTurnPlan exists to prevent.
func TestUpsertTurnPlan_SteerDoesNotBoundTheTurn(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t)
	if _, err := s.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "plan turn"
		return true
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	seed := []marotte.Message{
		{ID: "u1", Role: marotte.RoleUser, Content: "go"},
		{ID: "p1", Role: marotte.RoleAssistant, Plan: []marotte.PlanEntry{{Content: "first", Status: marotte.PlanPending}}},
		{ID: "s1", Role: marotte.RoleUser, UserKind: marotte.UserKindSteer, Content: "use tabs"},
	}
	for i := range seed {
		if err := s.AppendMessage(ctx, "c1", &seed[i]); err != nil {
			t.Fatalf("AppendMessage %s: %v", seed[i].ID, err)
		}
	}

	next := &marotte.Message{
		ID:   "p2",
		Role: marotte.RoleAssistant,
		Plan: []marotte.PlanEntry{{Content: "second", Status: marotte.PlanCompleted}},
	}
	if err := s.UpsertTurnPlan(ctx, "c1", next); err != nil {
		t.Fatalf("UpsertTurnPlan: %v", err)
	}

	c, ok := s.Get(ctx, "c1")
	if !ok {
		t.Fatal("Get: chat not found")
	}
	if len(c.Messages) != 3 {
		ids := make([]string, 0, len(c.Messages))
		for i := range c.Messages {
			ids = append(ids, c.Messages[i].ID)
		}
		t.Fatalf("UpsertTurnPlan across a steer: Messages = %v (len %d), want 3 — the plan row is overwritten, not appended", ids, len(c.Messages))
	}
	if c.Messages[1].ID != "p1" {
		t.Errorf("plan row ID = %q, want p1 — the overwrite keeps the row's identity", c.Messages[1].ID)
	}
	if got := c.Messages[1].Plan[0].Content; got != "second" {
		t.Errorf("plan row entry = %q, want %q (the newest frame's)", got, "second")
	}
}

// TestUpsertTurnPlan_PromptStillBoundsTheTurn is the other direction, so the test
// above cannot pass by the walk having stopped bounding turns at all.
func TestUpsertTurnPlan_PromptStillBoundsTheTurn(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t)
	if _, err := s.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "plan turn"
		return true
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	seed := []marotte.Message{
		{ID: "u1", Role: marotte.RoleUser, Content: "go"},
		{ID: "p1", Role: marotte.RoleAssistant, Plan: []marotte.PlanEntry{{Content: "first", Status: marotte.PlanPending}}},
		{ID: "u2", Role: marotte.RoleUser, Content: "again"},
	}
	for i := range seed {
		if err := s.AppendMessage(ctx, "c1", &seed[i]); err != nil {
			t.Fatalf("AppendMessage %s: %v", seed[i].ID, err)
		}
	}

	next := &marotte.Message{
		ID:   "p2",
		Role: marotte.RoleAssistant,
		Plan: []marotte.PlanEntry{{Content: "second", Status: marotte.PlanPending}},
	}
	if err := s.UpsertTurnPlan(ctx, "c1", next); err != nil {
		t.Fatalf("UpsertTurnPlan: %v", err)
	}

	c, ok := s.Get(ctx, "c1")
	if !ok {
		t.Fatal("Get: chat not found")
	}
	if len(c.Messages) != 4 {
		t.Fatalf("UpsertTurnPlan after a prompt: Messages len = %d, want 4 — the new turn gets its own plan row", len(c.Messages))
	}
	if got := c.Messages[1].Plan[0].Content; got != "first" {
		t.Errorf("turn 1 plan = %q, want %q — a later turn must not overwrite it", got, "first")
	}
	if got := c.Messages[3].Plan[0].Content; got != "second" {
		t.Errorf("turn 2 plan = %q, want %q", got, "second")
	}
}
