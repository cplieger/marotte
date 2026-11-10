package command

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// Stamps a reservationLedger hands out: lifeStamp is the life the reservation was taken in, intentStamp the
// registry's current LifeStamp, which a caller reads at its intent and which is never the life's.
const (
	lifeStamp   uint64 = 9
	intentStamp uint64 = 5
)

// reservationLedger mints each taken reservation a token of its own, in the chat's life lifeStamp, and records
// every release.
type reservationLedger struct {
	turnAdmission
	issued   []uint64
	released []uint64
	mu       sync.Mutex
	next     uint64
}

func (a *reservationLedger) mint(res Reservation, ok bool) (Reservation, bool) {
	if !ok {
		return Reservation{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.next++
	token := 100 + a.next
	a.issued = append(a.issued, token)
	return Reservation{Chat: res.Chat, Token: token, Stamp: lifeStamp}, true
}

func (a *reservationLedger) ReserveTurnForPrompt(ctx context.Context, chatID marotte.ChatID, wait time.Duration, stamp uint64, messageID string) (AdmissionOutcome, Reservation) {
	out, res := a.turnAdmission.(promptReserver).ReserveTurnForPrompt(ctx, chatID, wait, stamp, messageID)
	res, _ = a.mint(res, out == AdmissionAcquired)
	return out, res
}

func (a *reservationLedger) ReserveTurn(ctx context.Context, chatID marotte.ChatID, source marotte.TurnOpenSource, stamp uint64, wait time.Duration) (IdleAdmission, Reservation) {
	out, res := a.turnAdmission.(idleReserver).ReserveTurn(ctx, chatID, source, stamp, wait)
	res, _ = a.mint(res, out == IdleAcquired)
	return out, res
}

func (a *reservationLedger) ReserveIdleTurn(ctx context.Context, chatID marotte.ChatID, source marotte.TurnOpenSource, stamp uint64, wait time.Duration) (IdleAdmission, Reservation) {
	out, res := a.turnAdmission.(idleReserver).ReserveIdleTurn(ctx, chatID, source, stamp, wait)
	res, _ = a.mint(res, out == IdleAcquired)
	return out, res
}

func (a *reservationLedger) ReleaseTurnReservation(res Reservation) {
	a.mu.Lock()
	a.released = append(a.released, res.Token)
	a.mu.Unlock()
	a.turnAdmission.ReleaseTurnReservation(res)
}

func (*reservationLedger) LifeStamp() uint64 { return intentStamp }

// stampedOpens records the life stamp every session open was asked for.
type stampedOpens struct {
	bridgeAccess
	stamps []uint64
	mu     sync.Mutex
}

func (b *stampedOpens) OpenBridgeAt(ctx context.Context, chatID marotte.ChatID, model string, res Reservation) (Bridge, func(), error) {
	b.mu.Lock()
	b.stamps = append(b.stamps, res.Stamp)
	b.mu.Unlock()
	return b.bridgeAccess.OpenBridgeAt(ctx, chatID, model, res)
}

// withLedger routes roles' admission and session opens through a ledger and a stamp recorder.
func withLedger(roles *promptRoles) (*reservationLedger, *stampedOpens) {
	ledger := &reservationLedger{turnAdmission: roles.admission}
	opens := &stampedOpens{bridgeAccess: roles.bridges}
	roles.admission, roles.bridges = ledger, opens
	return ledger, opens
}

// checkCarried fails unless the run opened wantOpens sessions, each in the reservation's own life, and released
// every reservation it took exactly once, by the token the reserve returned.
func checkCarried(t *testing.T, ledger *reservationLedger, opens *stampedOpens, wantOpens int) {
	t.Helper()
	opens.mu.Lock()
	stamps := slices.Clone(opens.stamps)
	opens.mu.Unlock()
	ledger.mu.Lock()
	issued, released := slices.Clone(ledger.issued), slices.Clone(ledger.released)
	ledger.mu.Unlock()
	if len(stamps) != wantOpens {
		t.Fatalf("session opens = %v, want %d", stamps, wantOpens)
	}
	for _, s := range stamps {
		if s != lifeStamp {
			t.Errorf("a session open asked for life %d, want the reservation's life %d", s, lifeStamp)
		}
	}
	slices.Sort(issued)
	slices.Sort(released)
	if len(issued) == 0 || !slices.Equal(issued, released) {
		t.Errorf("released tokens %v, want each issued token %v released once", released, issued)
	}
}

// Every caller of a prompt hands its own reservation to the turn it starts: the session opens in the reservation's
// life, and the reservation released is the one the reserve returned.
func TestStartPrompt_EveryCallerCarriesItsReservation(t *testing.T) {
	t.Run("the prompt POST", func(t *testing.T) {
		_, roles, join := newAdmissionFixture(t, marotte.TurnResult{}, false)
		ledger, opens := withLedger(roles)

		if _, err := cmdPrompt(t.Context(), roles, promptReq(t, "c1", "do the thing")); err != nil {
			t.Fatalf("cmdPrompt: %v", err)
		}
		join.join()
		checkCarried(t, ledger, opens, 1)
	})
	t.Run("a queued row's launch", func(t *testing.T) {
		h := newResendHost(newSteerBridge(), AdmissionAcquired, 0, false)
		roles := drainRoles(h, newStubSteerQueue())
		ledger, opens := withLedger(roles)

		res, _ := ledger.mint(testReservation("c1", true), true)
		_, _ = launchQueuedRow(t.Context(), roles, &EntryLaunch{
			ID: "m-1", Kind: marotte.RunKindRow, Row: marotte.QueuedPrompt{ID: "m-1", Text: "next"}, Res: res,
		})
		h.waitInflight(t)
		checkCarried(t, ledger, opens, 1)
	})
	t.Run("the unread steers' launch", func(t *testing.T) {
		h := newResendHost(newSteerBridge(), AdmissionAcquired, 0, false)
		q := newStubSteerQueue()
		q.unsentRows = []SteerRow{{Key: "steer-a", Text: "first"}}
		roles := drainRoles(h, q)
		ledger, opens := withLedger(roles)

		res, _ := ledger.mint(testReservation("c1", true), true)
		_, _ = sendUnread(t.Context(), roles, res, TurnFence{})
		h.waitInflight(t)
		checkCarried(t, ledger, opens, 1)
	})
	t.Run("the merge's summary and delivery", func(t *testing.T) {
		f := newMergeFixture(t)
		ledger, opens := withLedger(f.roles)

		if _, err := f.merge(t, &fakeTangents{parent: "c-parent", reply: "The cache halves the load."}); err != nil {
			t.Fatalf("CmdMergeTangent: %v", err)
		}
		f.host.waitInflight(t)
		checkCarried(t, ledger, opens, 2)
	})
}

// A launch with no reservation is a programming error: it answers 500 before it opens a turn, and logs ERROR.
func TestStartPrompt_RefusesALaunchWithNoReservation(t *testing.T) {
	logs := captureLogs(t)
	host, roles, _ := newAdmissionFixture(t, marotte.TurnResult{}, false)

	_, err := startPrompt(t.Context(), roles, "c1", &marotte.PromptCommand{Text: "hi", MessageID: "m1"}, &launch{})

	if statusOf(err) != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", statusOf(err))
	}
	if host.admission.rec.indexOf("openTurn") != -1 {
		t.Errorf("an unreserved launch opened a turn; log = %v", host.admission.rec.snapshot())
	}
	if !strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("an unreserved launch logged no ERROR: %s", logs.String())
	}
}
