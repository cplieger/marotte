package agent

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/notice"
	"github.com/cplieger/marotte/internal/obs"
	"github.com/cplieger/marotte/internal/signin"
)

// signInGrace is how long renewals must have failed before an unreachable identity provider reads failing. The vault's
// own thresholds already give 120 s before its first refusal on the normal path; the grace covers a short lease and a
// token past its floor at the first ask, which reach ProviderUnreachable sooner.
var signInGrace = 2 * time.Minute

// signInHealth owns the Kiro sign-in's state every surface reports: it reads the vault's Standing and the engine's own
// refusals, and on every move tells each client, the gauge, the log and, once per episode, push.
type signInHealth struct {
	lt        *lifetime
	notify    func(context.Context, *marotte.NotificationPayload)
	broadcast func(context.Context, marotte.SignInHealth)
	timer     *time.Timer
	// episodeAt is when the current failing or signed_out episode was entered.
	episodeAt time.Time
	link      signinLink
	cur       marotte.SignInHealth
	mu        sync.Mutex
	// refused latches an engine refusal of a credential the vault served while the provider answered, until a turn
	// is accepted or the account changes.
	refused bool
	// pushed: this episode's push went; ok or none re-arms it.
	pushed bool
}

func newSignInHealth(
	lt *lifetime, link signinLink, boot int64,
	broadcast func(context.Context, marotte.SignInHealth), notify func(context.Context, *marotte.NotificationPayload),
) *signInHealth {
	h := &signInHealth{lt: lt, link: link, broadcast: broadcast, notify: notify}
	h.cur.Version.BootMS = boot
	h.cur.State = marotte.SignInOK
	obs.SignInState(string(marotte.SignInOK))
	link.onChange(func(signin.Change) { h.accountChanged() })
	link.onAnswerability(h.recompute)
	context.AfterFunc(lt.shutdownCtx, h.stopTimer)
	h.recompute()
	return h
}

func (h *signInHealth) snapshot() marotte.SignInHealth {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.cur
	if h.cur.Since != nil {
		since := *h.cur.Since
		out.Since = &since
	}
	return out
}

func (h *signInHealth) refusedNow() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.refused
}

// noteRefusal latches an engine refusal unless the identity provider is down, which makes the refusal the outage's.
func (h *signInHealth) noteRefusal() {
	if st, ok := h.standing(); ok && st.Answerability == signin.ProviderUnreachable {
		return
	}
	h.mu.Lock()
	h.refused = true
	h.mu.Unlock()
	h.recompute()
}

func (h *signInHealth) noteAccepted() {
	h.mu.Lock()
	was := h.refused
	h.refused = false
	h.mu.Unlock()
	if was {
		h.recompute()
	}
}

// accountChanged voids a refusal of the account a sign-in, sign-out or lapse replaced.
func (h *signInHealth) accountChanged() {
	h.mu.Lock()
	h.refused = false
	h.mu.Unlock()
	h.recompute()
}

// standing reads the vault, ok false when the vault answers no ask (none wired, or the API key signs the engine in).
func (h *signInHealth) standing() (signin.Standing, bool) {
	if h.link.vault == nil || h.link.apiKey {
		return signin.Standing{}, false
	}
	return h.link.vault.Standing(h.link.backend), true
}

// verdict is the state the inputs give now, and how long until an unreachable provider's grace ends (zero for none).
func (h *signInHealth) verdict(now time.Time, refused bool) (state marotte.SignInState, cause marotte.SignInCause, since time.Time, wait time.Duration) {
	st, vaulted := h.standing()
	switch {
	case !vaulted && refused:
		return marotte.SignInSignedOut, marotte.SignInCauseRefused, time.Time{}, 0
	case !vaulted:
		return marotte.SignInOK, "", time.Time{}, 0
	case st.Lapsed:
		return marotte.SignInSignedOut, marotte.SignInCauseRejected, time.Time{}, 0
	case st.Answerability == signin.NeedsSignIn:
		return marotte.SignInNone, "", time.Time{}, 0
	case st.Answerability == signin.ProviderUnreachable:
		failing := st.FailingSince
		if failing.IsZero() {
			failing = now
		}
		if left := signInGrace - now.Sub(failing); left > 0 {
			return marotte.SignInOK, "", time.Time{}, left
		}
		return marotte.SignInFailing, marotte.SignInCauseProviderUnreachable, failing, 0
	case refused:
		return marotte.SignInSignedOut, marotte.SignInCauseRefused, time.Time{}, 0
	default:
		return marotte.SignInOK, "", time.Time{}, 0
	}
}

func episodic(s marotte.SignInState) bool {
	return s == marotte.SignInFailing || s == marotte.SignInSignedOut
}

// recompute reads the inputs and, on a move, versions the state and reports it.
func (h *signInHealth) recompute() {
	ctx := h.lt.shutdownCtx
	if ctx.Err() != nil {
		return
	}
	now := time.Now()
	h.mu.Lock()
	state, cause, since, wait := h.verdict(now, h.refused)
	h.stopTimerLocked()
	if wait > 0 {
		h.timer = h.lt.afterUnlessDraining(wait, h.recompute)
	}
	prev := h.cur
	if state == prev.State && cause == prev.Cause {
		h.mu.Unlock()
		return
	}
	next := advance(prev, state, cause, since, now)
	lasted := now.Sub(h.episodeAt)
	if episodic(state) && !episodic(prev.State) {
		h.episodeAt = now
	}
	push := episodic(state) && !h.pushed
	h.pushed = episodic(state)
	h.cur = next
	h.mu.Unlock()

	obs.SignInState(string(state))
	logSignInMove(prev, next, lasted)
	h.broadcast(ctx, next)
	if push && h.notify != nil {
		n := notice.SignInFailing()
		if state == marotte.SignInSignedOut {
			n = notice.SignInEnded()
		}
		h.notify(ctx, &n)
	}
}

// advance is prev moved to state: the next version, and Since as the episode's start (failing: the first failed
// renewal; signed_out: when it was first seen, kept while it stays signed_out).
func advance(prev marotte.SignInHealth, state marotte.SignInState, cause marotte.SignInCause, since, now time.Time) marotte.SignInHealth {
	next := marotte.SignInHealth{State: state, Cause: cause, Version: prev.Version}
	next.Version.Seq++
	switch {
	case state == marotte.SignInFailing:
		next.Since = &since
	case state == marotte.SignInSignedOut && prev.State == marotte.SignInSignedOut:
		next.Since = prev.Since
	case state == marotte.SignInSignedOut:
		next.Since = &now
	}
	return next
}

// logSignInMove logs a move once: the start of an episode, a cause change inside one, and its end with how long it
// lasted.
func logSignInMove(prev, next marotte.SignInHealth, lasted time.Duration) {
	switch {
	case episodic(next.State) && !episodic(prev.State):
		msg := "sign-in health: the Kiro sign-in is failing"
		if next.State == marotte.SignInSignedOut {
			msg = "sign-in health: the Kiro sign-in ended"
		}
		attrs := []any{"state", next.State, "cause", next.Cause}
		if next.Since != nil {
			attrs = append(attrs, "since", next.Since.UTC())
		}
		slog.Warn(msg, attrs...)
	case episodic(next.State):
		slog.Info("sign-in health: the Kiro sign-in's state changed",
			"from", prev.State, "from_cause", prev.Cause, "state", next.State, "cause", next.Cause)
	case episodic(prev.State) && next.State == marotte.SignInOK:
		slog.Info("sign-in health: the Kiro sign-in works again",
			"from", prev.State, "cause", prev.Cause, "duration", lasted.Round(time.Second))
	case episodic(prev.State):
		slog.Info("sign-in health: the sign-in problem ended with the account signed out",
			"from", prev.State, "cause", prev.Cause, "duration", lasted.Round(time.Second))
	}
}

func (h *signInHealth) stopTimer() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopTimerLocked()
}

func (h *signInHealth) stopTimerLocked() {
	if h.timer != nil {
		h.timer.Stop()
		h.timer = nil
	}
}

// SignInHealth is the Kiro sign-in's current state.
func (rt *Runtime) SignInHealth() marotte.SignInHealth { return rt.signInHealth.snapshot() }

// SignInRefused reports whether Kiro refused a credential marotte served and no turn has been accepted since.
func (rt *Runtime) SignInRefused() bool { return rt.signInHealth.refusedNow() }

// NoteEngineRefusal records a turn the engine failed on a refused credential; while the identity provider is down the
// refusal is the outage's and changes nothing.
func (rt *Runtime) NoteEngineRefusal() { rt.signInHealth.noteRefusal() }

// NoteEngineAccepted records a turn the engine accepted.
func (rt *Runtime) NoteEngineAccepted() { rt.signInHealth.noteAccepted() }
