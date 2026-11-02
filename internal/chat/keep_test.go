package chat

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

func seedChat(t *testing.T, s *Store, id marotte.ChatID, chain ...string) {
	t.Helper()
	if _, err := s.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool {
		c.Name = string(id)
		for _, sess := range chain {
			c.RecordSession(sess)
		}
		return true
	}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func readChat(t *testing.T, s *Store, id marotte.ChatID) *marotte.Chat {
	t.Helper()
	c, found, err := s.Lookup(t.Context(), id)
	if err != nil || !found {
		t.Fatalf("lookup %s: found=%v err=%v", id, found, err)
	}
	return c
}

func corruptHeader(t *testing.T, s *Store, id marotte.ChatID) (restore func()) {
	t.Helper()
	path := filepath.Join(s.dir, string(id), headerFileName)
	orig, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read header: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt header: %v", err)
	}
	return func() {
		if err := os.WriteFile(path, orig, 0o600); err != nil {
			t.Fatalf("restore header: %v", err)
		}
	}
}

func failHeaderWrites(t *testing.T, id marotte.ChatID) (repair func()) {
	t.Helper()
	prev := headerWriteFault
	failing := true
	headerWriteFault = func(c marotte.ChatID) error {
		if failing && c == id {
			return errors.New("injected header write failure")
		}
		return nil
	}
	t.Cleanup(func() { headerWriteFault = prev })
	return func() { failing = false }
}

func TestHold_WritesAFactWithAFreshTokenAndTheKeysPrior(t *testing.T) {
	s, b := newTestStore(t)
	seedChat(t, s, "c1")
	before := readChat(t, s, "c1").UpdatedAt
	b.reset()

	first, err := s.Hold(t.Context(), "c1", marotte.KeyTab, KeepDurable)
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}
	second, err := s.Hold(t.Context(), "c1", marotte.KeyTab, KeepDurable)
	if err != nil {
		t.Fatalf("second Hold: %v", err)
	}

	c := readChat(t, s, "c1")
	if first.Prior != PriorAbsent {
		t.Errorf("first claim prior = %v, want PriorAbsent", first.Prior)
	}
	if second.Prior != PriorThisBoot || second.PriorFact.Token != first.Token {
		t.Errorf("second claim prior = %v with token %q, want PriorThisBoot over %q", second.Prior, second.PriorFact.Token, first.Token)
	}
	if first.Token == "" || first.Token == second.Token {
		t.Errorf("tokens %q and %q: every hold mints its own", first.Token, second.Token)
	}
	if got := c.Keep[marotte.KeyTab]; got.Token != second.Token || got.Boot != s.Boot() || got.Process {
		t.Errorf("stored fact = %+v, want the second hold's durable fact", got)
	}
	if c.UpdatedAt != before {
		t.Errorf("UpdatedAt moved %d -> %d: a hold is not use", before, c.UpdatedAt)
	}
	if n := len(b.snapshot()); n != 0 {
		t.Errorf("Hold broadcast %d frames, want none", n)
	}
}

func TestHold_AForeignBootFactReportsPriorForeignBoot(t *testing.T) {
	s, _ := newTestStore(t)
	seedChat(t, s, "c1")
	if _, err := s.Hold(t.Context(), "c1", marotte.KeyTab, KeepDurable); err != nil {
		t.Fatal(err)
	}
	other, err := NewStore(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := other.Hold(t.Context(), "c1", marotte.KeyTab, KeepDurable)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Prior != PriorForeignBoot || claim.PriorFact.Boot != s.Boot() {
		t.Errorf("claim = %+v, want PriorForeignBoot over the first store's fact", claim)
	}
}

func TestHold_AnAbsentRecordIsRefusedByWhyItIsAbsent(t *testing.T) {
	s, _ := newTestStore(t)
	seedChat(t, s, "gone")
	seedChat(t, s, "torn")
	if _, err := s.Delete(t.Context(), "gone"); err != nil {
		t.Fatal(err)
	}
	s.beginTeardown("torn", nil)
	if _, err := s.Delete(t.Context(), "torn"); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		id   marotte.ChatID
		want error
		not  error
	}{
		{"never", ErrChatNotFound, ErrTombstoned},
		{"gone", ErrTombstoned, ErrInTeardown},
		{"torn", ErrInTeardown, ErrChatNotFound},
	}
	for _, tc := range cases {
		t.Run(string(tc.id), func(t *testing.T) {
			_, err := s.Hold(t.Context(), tc.id, marotte.KeyTab, KeepDurable)
			if !errors.Is(err, tc.want) || errors.Is(err, tc.not) {
				t.Errorf("Hold = %v, want %v and not %v", err, tc.want, tc.not)
			}
		})
	}
}

func TestHold_AnUnreadableRecordIsAnErrorAndNothingIsHeld(t *testing.T) {
	s, _ := newTestStore(t)
	seedChat(t, s, "c1")
	restore := corruptHeader(t, s, "c1")
	_, err := s.Hold(t.Context(), "c1", marotte.KeyTab, KeepDurable)
	if err == nil || isAbsence(err) {
		t.Fatalf("Hold on an unreadable record = %v, want a read error", err)
	}
	restore()
	if c := readChat(t, s, "c1"); len(c.Keep) != 0 {
		t.Errorf("keep = %v, want nothing held", c.Keep)
	}
	if s.hasPending("c1") {
		t.Error("Hold pended a fact; only HoldOrPend may")
	}
}

func TestHoldOrPend_AFailedWritePendsAndTheNextReadFlushesIt(t *testing.T) {
	s, _ := newTestStore(t)
	seedChat(t, s, "c1")
	key, _ := marotte.KeyRun("wf_1")
	repair := failHeaderWrites(t, "c1")

	claim, err := s.HoldOrPend(t.Context(), "c1", key, KeepDurable)
	if err != nil || claim.Prior != PriorUnknown {
		t.Fatalf("HoldOrPend = %+v, %v; want a PriorUnknown claim and nil", claim, err)
	}
	if !s.hasPending("c1") {
		t.Fatal("no pending hold after a failed write")
	}
	if err := s.Rollback(t.Context(), &claim); err != nil || !s.hasPending("c1") {
		t.Errorf("Rollback of a pended claim = %v, pending=%v; want a no-op", err, s.hasPending("c1"))
	}

	repair()
	tabClaim, err := s.Hold(t.Context(), "c1", marotte.KeyTab, KeepDurable)
	if err != nil {
		t.Fatal(err)
	}
	if s.hasPending("c1") {
		t.Error("pending hold survived a successful write of the record")
	}
	c := readChat(t, s, "c1")
	if _, ok := c.Keep[key]; !ok {
		t.Errorf("keep = %v, want the pended %s flushed", c.Keep, key)
	}
	if tabClaim.Prior != PriorAbsent {
		t.Errorf("tab claim prior = %v", tabClaim.Prior)
	}
}

func TestHoldOrPend_APendedKeyReportsPriorThisBootToTheHoldThatFlushesIt(t *testing.T) {
	s, _ := newTestStore(t)
	seedChat(t, s, "c1")
	repair := failHeaderWrites(t, "c1")
	if _, err := s.HoldOrPend(t.Context(), "c1", marotte.KeyTab, KeepDurable); err != nil {
		t.Fatal(err)
	}
	repair()
	claim, err := s.Hold(t.Context(), "c1", marotte.KeyTab, KeepDurable)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Prior != PriorThisBoot {
		t.Fatalf("prior = %v, want PriorThisBoot", claim.Prior)
	}
	if err := s.Rollback(t.Context(), &claim); err != nil {
		t.Fatal(err)
	}
	if _, ok := readChat(t, s, "c1").Keep[marotte.KeyTab]; !ok {
		t.Error("the rollback removed the fact the pended writer took")
	}
}

func TestRollback(t *testing.T) {
	type prior int
	const (
		absent prior = iota
		thisBoot
		foreignBoot
	)
	for _, p := range []prior{absent, thisBoot, foreignBoot} {
		for _, overwritten := range []bool{false, true} {
			name := []string{"absent", "this_boot", "foreign_boot"}[p]
			if overwritten {
				name += "_overwritten"
			}
			t.Run(name, func(t *testing.T) {
				s, _ := newTestStore(t)
				seedChat(t, s, "c1")
				var before marotte.KeepFact
				switch p {
				case thisBoot:
					if _, err := s.Hold(t.Context(), "c1", marotte.KeyTab, KeepDurable); err != nil {
						t.Fatal(err)
					}
				case foreignBoot:
					other, err := NewStore(s.dir)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := other.Hold(t.Context(), "c1", marotte.KeyTab, KeepDurable); err != nil {
						t.Fatal(err)
					}
				case absent:
				}
				before = readChat(t, s, "c1").Keep[marotte.KeyTab]
				claim, err := s.Hold(t.Context(), "c1", marotte.KeyTab, KeepDurable)
				if err != nil {
					t.Fatal(err)
				}
				var later marotte.KeepFact
				if overwritten {
					if _, err := s.Hold(t.Context(), "c1", marotte.KeyTab, KeepDurable); err != nil {
						t.Fatal(err)
					}
					later = readChat(t, s, "c1").Keep[marotte.KeyTab]
				}
				if err := s.Rollback(t.Context(), &claim); err != nil {
					t.Fatalf("Rollback: %v", err)
				}
				got, held := readChat(t, s, "c1").Keep[marotte.KeyTab]
				switch {
				case overwritten:
					if got != later {
						t.Errorf("fact = %+v, want the later writer's %+v untouched", got, later)
					}
				case p == absent:
					if held {
						t.Errorf("fact = %+v, want the key gone", got)
					}
				default:
					if got != before {
						t.Errorf("fact = %+v, want the prior value %+v restored exactly", got, before)
					}
				}
			})
		}
	}
}

func TestRollback_AnAbsentRecordIsNilAndAnUnreadableOneIsAnError(t *testing.T) {
	s, _ := newTestStore(t)
	seedChat(t, s, "c1")
	seedChat(t, s, "c2")
	c1, err := s.Hold(t.Context(), "c1", marotte.KeyTab, KeepDurable)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := s.Hold(t.Context(), "c2", marotte.KeyTab, KeepDurable)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete(t.Context(), "c1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Rollback(t.Context(), &c1); err != nil {
		t.Errorf("Rollback on a deleted record = %v, want nil", err)
	}
	restore := corruptHeader(t, s, "c2")
	if err := s.Rollback(t.Context(), &c2); err == nil {
		t.Error("Rollback on an unreadable record = nil, want the read error")
	}
	restore()
	if _, ok := readChat(t, s, "c2").Keep[marotte.KeyTab]; !ok {
		t.Error("a failed rollback dropped the fact")
	}
}

func TestRelease_RemovesTheKeyOnlyOnEvidence(t *testing.T) {
	s, _ := newTestStore(t)
	seedChat(t, s, "c1")
	key, _ := marotte.KeyRun("wf_1")
	if _, err := s.Hold(t.Context(), "c1", key, KeepDurable); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(t.Context(), "c1", key, evidenceNone); !errors.Is(err, errNoEvidence) {
		t.Errorf("Release with no evidence = %v, want errNoEvidence", err)
	}
	if _, ok := readChat(t, s, "c1").Keep[key]; !ok {
		t.Fatal("a release with no evidence removed the fact")
	}
	for _, ev := range []Evidence{EvidenceTabClosed, EvidenceRunTerminal, EvidenceRunCancelled, EvidenceRunDeleted, EvidenceTurnEnded, EvidenceProcessGone} {
		if _, err := s.Hold(t.Context(), "c1", key, KeepDurable); err != nil {
			t.Fatal(err)
		}
		if err := s.Release(t.Context(), "c1", key, ev); err != nil {
			t.Errorf("Release(%s) = %v", ev, err)
		}
		if _, ok := readChat(t, s, "c1").Keep[key]; ok {
			t.Errorf("Release(%s) left the fact", ev)
		}
	}
	if err := s.Release(t.Context(), "never", key, EvidenceRunTerminal); err != nil {
		t.Errorf("Release on an absent record = %v, want nil", err)
	}
}

func TestRelease_DropsAPendedHoldEvenWhenTheRecordIsUnreadable(t *testing.T) {
	s, _ := newTestStore(t)
	seedChat(t, s, "c1")
	key, _ := marotte.KeyRun("wf_1")
	repair := failHeaderWrites(t, "c1")
	if _, err := s.HoldOrPend(t.Context(), "c1", key, KeepDurable); err != nil {
		t.Fatal(err)
	}
	repair()
	restore := corruptHeader(t, s, "c1")
	if err := s.Release(t.Context(), "c1", key, EvidenceRunTerminal); err == nil {
		t.Error("Release on an unreadable record = nil, want the read error")
	}
	restore()
	if s.hasPending("c1") {
		t.Error("the pended hold of an ended run survived its release")
	}
	if _, err := s.Hold(t.Context(), "c1", marotte.KeyTab, KeepDurable); err != nil {
		t.Fatal(err)
	}
	if _, ok := readChat(t, s, "c1").Keep[key]; ok {
		t.Error("a later read flushed the ended run's pended fact")
	}
}

func TestReleaseAll_ReleasesEveryHolderTheScanReads(t *testing.T) {
	s, _ := newTestStore(t)
	key, _ := marotte.KeyRun("wf_1")
	for _, id := range []marotte.ChatID{"a", "b", "c", "d"} {
		seedChat(t, s, id)
	}
	for _, id := range []marotte.ChatID{"a", "b", "c"} {
		if _, err := s.Hold(t.Context(), id, key, KeepDurable); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Hold(t.Context(), "d", marotte.KeyTab, KeepDurable); err != nil {
		t.Fatal(err)
	}
	restore := corruptHeader(t, s, "c")

	err := s.ReleaseAll(t.Context(), key, EvidenceRunTerminal)
	if err == nil {
		t.Error("ReleaseAll with an unreadable holder = nil, want an error")
	}
	restore()
	for _, id := range []marotte.ChatID{"a", "b"} {
		if _, ok := readChat(t, s, id).Keep[key]; ok {
			t.Errorf("%s still holds %s", id, key)
		}
	}
	if _, ok := readChat(t, s, "c").Keep[key]; !ok {
		t.Error("the record the scan could not read lost its fact")
	}
	if _, ok := readChat(t, s, "d").Keep[marotte.KeyTab]; !ok {
		t.Error("an unrelated fact was released")
	}
}

func TestHoldTurn(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.HoldTurn(t.Context(), "fresh"); err != nil {
		t.Errorf("HoldTurn on a record not yet created = %v, want nil", err)
	}
	if s.hasPending("fresh") {
		t.Error("HoldTurn pended a fact for a record that does not exist")
	}
	seedChat(t, s, "c1")
	if err := s.HoldTurn(t.Context(), "c1"); err != nil {
		t.Fatal(err)
	}
	if f := readChat(t, s, "c1").Keep[marotte.KeyTurn]; !f.Process || f.Boot != s.Boot() {
		t.Errorf("turn fact = %+v, want a process fact of this boot", f)
	}
	s.beginTeardown("c1", nil)
	if _, err := s.Delete(t.Context(), "c1"); err != nil {
		t.Fatal(err)
	}
	if err := s.HoldTurn(t.Context(), "c1"); !errors.Is(err, ErrInTeardown) {
		t.Errorf("HoldTurn in teardown = %v, want ErrInTeardown", err)
	}
}

func TestEndTurn_ReleasesStampsAndAnnounces(t *testing.T) {
	s, _ := newTestStore(t)
	seedChat(t, s, "c1")
	if err := s.HoldTurn(t.Context(), "c1"); err != nil {
		t.Fatal(err)
	}
	ageChat(t, s, "c1", 48*time.Hour)
	before := readChat(t, s, "c1").UpdatedAt
	if !s.EndTurn(t.Context(), "c1") {
		t.Fatal("EndTurn = false, want an announce for a released turn")
	}
	c := readChat(t, s, "c1")
	if _, ok := c.Keep[marotte.KeyTurn]; ok {
		t.Error("turn fact survived EndTurn")
	}
	if c.UpdatedAt <= before {
		t.Errorf("UpdatedAt %d not after %d: a turn's end is use", c.UpdatedAt, before)
	}
	if s.EndTurn(t.Context(), "c1") {
		t.Error("a second EndTurn announced with nothing to release")
	}
}

func TestEndTurn_DropsAPendedTurnFirst(t *testing.T) {
	s, _ := newTestStore(t)
	seedChat(t, s, "c1")
	repair := failHeaderWrites(t, "c1")
	if err := s.HoldTurn(t.Context(), "c1"); err != nil {
		t.Fatal(err)
	}
	repair()
	restore := corruptHeader(t, s, "c1")
	if s.EndTurn(t.Context(), "c1") {
		t.Error("EndTurn on an unreadable record announced")
	}
	restore()
	if s.hasPending("c1") {
		t.Error("a pended turn outlived the lifecycle's end")
	}
}

func TestTeardown_RefusesACreateAndANewBindUntilItEnds(t *testing.T) {
	s, _ := newTestStore(t)
	seedChat(t, s, "torn", "sess_a")
	seedChat(t, s, "other", "sess_b")
	s.beginTeardown("torn", []string{"sess_a"})
	if _, err := s.Delete(t.Context(), "torn"); err != nil {
		t.Fatal(err)
	}
	s.tombMu.Lock()
	delete(s.tombstone, "torn")
	s.tombMu.Unlock()

	_, err := s.Mutate(t.Context(), "torn", func(c *marotte.Chat, _ bool) bool { return true })
	if !errors.Is(err, ErrInTeardown) {
		t.Errorf("create of a torn-down id past its tombstone = %v, want ErrInTeardown", err)
	}
	_, err = s.Mutate(t.Context(), "other", func(c *marotte.Chat, _ bool) bool {
		c.RecordSession("sess_a")
		return true
	})
	if !errors.Is(err, ErrInTeardown) {
		t.Errorf("bind of a torn-down session = %v, want ErrInTeardown", err)
	}
	if _, err := s.Mutate(t.Context(), "other", func(c *marotte.Chat, _ bool) bool {
		c.Name = "renamed"
		return true
	}); err != nil {
		t.Errorf("a save that binds nothing new = %v", err)
	}
	if !s.SessionInTeardown("sess_a") || s.SessionInTeardown("sess_b") {
		t.Error("SessionInTeardown does not name exactly the torn-down chain")
	}
	s.EndTeardown("torn", []string{"sess_a"})
	if s.SessionInTeardown("sess_a") {
		t.Error("EndTeardown left the session marked")
	}
	if _, err := s.Mutate(t.Context(), "torn", func(c *marotte.Chat, _ bool) bool { return true }); err != nil {
		t.Errorf("create after the teardown ended = %v", err)
	}
}

func TestCreate_StartsWithNoFactsAndAFillInStamp(t *testing.T) {
	s, _ := newTestStore(t)
	parent := marotte.Chat{ID: "parent", Keep: map[string]marotte.KeepFact{marotte.KeyTab: {Boot: "x"}}}
	if _, err := s.Mutate(t.Context(), "child", func(c *marotte.Chat, _ bool) bool {
		id := c.ID
		*c = parent
		c.ID = id
		return true
	}); err != nil {
		t.Fatal(err)
	}
	c := readChat(t, s, "child")
	if len(c.Keep) != 0 {
		t.Errorf("child keep = %v, want none inherited", c.Keep)
	}
	if c.KeepFilledAt <= 0 || !s.Reconciled("child") {
		t.Errorf("KeepFilledAt = %d, reconciled = %v; a create is reconciled at birth", c.KeepFilledAt, s.Reconciled("child"))
	}
}

func TestHeaderWriters_PreserveKeepState(t *testing.T) {
	s, _ := newTestStore(t)
	seedChat(t, s, "c1")
	if _, err := s.Hold(t.Context(), "c1", marotte.KeyTab, KeepDurable); err != nil {
		t.Fatal(err)
	}
	want := readChat(t, s, "c1")
	if err := s.header("c1").Counters(t.Context(), 7, marotte.TurnOutcome("completed")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "x"; return true }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetDraft(t.Context(), "c1", "words"); err != nil {
		t.Fatal(err)
	}
	got := readChat(t, s, "c1")
	if got.Keep[marotte.KeyTab] != want.Keep[marotte.KeyTab] || got.KeepFilledAt != want.KeepFilledAt {
		t.Errorf("keep %v filled %d, want %v filled %d after counter, mutate and draft writes",
			got.Keep, got.KeepFilledAt, want.Keep, want.KeepFilledAt)
	}
}

func TestSessionOwners_NamesReadAndSkippedChats(t *testing.T) {
	s, _ := newTestStore(t)
	seedChat(t, s, "a", "sess_1", "sess_2")
	seedChat(t, s, "b", "sess_2")
	seedChat(t, s, "empty")
	seedChat(t, s, "bad", "sess_3")
	corruptHeader(t, s, "bad")

	scan := s.SessionOwners(t.Context())
	if !scan.Listed || scan.Complete {
		t.Errorf("listed=%v complete=%v, want listed and incomplete", scan.Listed, scan.Complete)
	}
	if !slices.Equal(scan.Skipped, []marotte.ChatID{"bad"}) {
		t.Errorf("skipped = %v, want [bad]", scan.Skipped)
	}
	for _, id := range []marotte.ChatID{"a", "b", "empty"} {
		if _, ok := scan.Read[id]; !ok {
			t.Errorf("Read lacks %s", id)
		}
	}
	if got := scan.Owners.Of("sess_2"); !slices.Equal(got, []marotte.ChatID{"a", "b"}) {
		t.Errorf("Of(sess_2) = %v, want [a b]", got)
	}
	if got := scan.Owners.Of("sess_1"); !slices.Equal(got, []marotte.ChatID{"a"}) {
		t.Errorf("Of(sess_1) = %v (a prior session), want [a]", got)
	}
	if got := OwnersOfChain("x", []string{"p", "q"}).Of("p"); !slices.Equal(got, []marotte.ChatID{"x"}) {
		t.Errorf("OwnersOfChain.Of = %v", got)
	}
	if got := scan.Owners.Of("sess_3"); len(got) != 0 || scan.Complete {
		t.Errorf("Of(sess_3) over an unread owner = %v, complete %v; want no owner and incomplete", got, scan.Complete)
	}
}

func TestSessionOwners_AnUnlistableDirectoryIsNotListed(t *testing.T) {
	s, _ := newTestStore(t)
	if err := os.RemoveAll(s.Dir()); err != nil {
		t.Fatal(err)
	}
	scan := s.SessionOwners(t.Context())
	if scan.Listed || scan.Complete {
		t.Errorf("listed=%v complete=%v for a directory that cannot be read", scan.Listed, scan.Complete)
	}
}

func TestOpenTurn_HoldsTheTurnAndStampsUse(t *testing.T) {
	s, _ := newTestStore(t)
	seedChat(t, s, "c1")
	ageChat(t, s, "c1", 48*time.Hour)
	before := readChat(t, s, "c1").UpdatedAt
	if _, err := s.OpenTurn(t.Context(), "c1", &TurnSpec{Source: marotte.TurnOpenNameWireTurnStart}, nil); err != nil {
		t.Fatal(err)
	}
	c := readChat(t, s, "c1")
	if f := c.Keep[marotte.KeyTurn]; !f.Process || f.Boot != s.Boot() {
		t.Errorf("turn fact = %+v, want a process fact of this boot", f)
	}
	if c.UpdatedAt <= before {
		t.Errorf("UpdatedAt %d not after %d: a turn opening is use", c.UpdatedAt, before)
	}
}

func TestOpenTurn_ACreatedRecordHoldsTheTurn(t *testing.T) {
	s, _ := newTestStore(t)
	init := func(c *marotte.Chat) { c.Name = "new" }
	if _, err := s.OpenTurn(t.Context(), "fresh", &TurnSpec{Source: marotte.TurnOpenNameWireTurnStart}, init); err != nil {
		t.Fatal(err)
	}
	if _, ok := readChat(t, s, "fresh").Keep[marotte.KeyTurn]; !ok {
		t.Error("a record OpenTurn created carries no turn fact")
	}
}

func TestOpenTurn_AFailedFactWritePendsAndTheOpenProceeds(t *testing.T) {
	s, _ := newTestStore(t)
	seedChat(t, s, "c1")
	failHeaderWrites(t, "c1")
	if _, err := s.OpenTurn(t.Context(), "c1", &TurnSpec{Source: marotte.TurnOpenNameWireTurnStart}, nil); err != nil {
		t.Fatalf("OpenTurn with a failing header write = %v, want the open to proceed", err)
	}
	if !s.hasPending("c1") {
		t.Error("the turn fact the open could not write is not pending")
	}
}

func TestOpenTurn_AChatInTeardownIsRefusedEvenWithInit(t *testing.T) {
	for name, tombstoneLive := range map[string]bool{"tombstone live": true, "tombstone expired": false} {
		t.Run(name, func(t *testing.T) {
			s, _ := newTestStore(t)
			seedChat(t, s, "torn")
			s.beginTeardown("torn", nil)
			if _, err := s.Delete(t.Context(), "torn"); err != nil {
				t.Fatal(err)
			}
			if !tombstoneLive {
				s.tombMu.Lock()
				delete(s.tombstone, "torn")
				s.tombMu.Unlock()
			}
			init := func(c *marotte.Chat) { c.Name = "reborn" }
			_, err := s.OpenTurn(t.Context(), "torn", &TurnSpec{Source: marotte.TurnOpenNameWireTurnStart}, init)
			if !errors.Is(err, ErrInTeardown) {
				t.Errorf("OpenTurn of a chat in teardown = %v, want ErrInTeardown", err)
			}
			if err := s.headerErr("torn"); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the refused open left a record: %v", err)
			}
		})
	}
}
