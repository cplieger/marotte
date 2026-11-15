package push

import (
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// The registry's DefaultOn and the effective view must answer the same thing for
// every keyed kind, because the two are read by different halves of one feature:
// the send gate falls back to DefaultOn for a key config.json does not carry, and
// GET /api/settings answers EffectiveDefaults for the same absence. A third copy
// of a polarity, or a kind added to one side only, renders the toggle in one state
// while the server delivers in the other.
func TestKindRegistry_DefaultsMatchEffectiveDefaults(t *testing.T) {
	eff := settings.EffectiveDefaults()
	byKey := map[string]bool{
		settings.KeyNotifyAgentFinished: eff.NotifyAgentFinished,
		settings.KeyNotifyPRStatus:      eff.NotifyPRStatus,
		settings.KeyNotifyRunOutcome:    eff.NotifyRunOutcome,
		settings.KeyNotifySignIn:        eff.NotifySignIn,
	}

	var keyed int
	for _, k := range Kinds() {
		if k.SettingsKey == "" {
			continue // a floor has no settings key, so there is nothing to agree with
		}
		keyed++
		want, ok := byKey[k.SettingsKey]
		if !ok {
			t.Errorf("kind %s carries settings key %q, which no field of EffectiveSettings answers for: "+
				"the registry gained a keyed kind the effective view does not resolve",
				k.Kind, k.SettingsKey)
			continue
		}
		if k.DefaultOn != want {
			t.Errorf("kindRegistry[%s].DefaultOn = %v, EffectiveDefaults().%s = %v: "+
				"the send gate and the rendered toggle would disagree for every install whose "+
				"config.json has never carried %q",
				k.Kind, k.DefaultOn, k.SettingsKey, want, k.SettingsKey)
		}
	}
	if keyed != len(byKey) {
		t.Errorf("keyed kinds = %d, want %d: a kind was added to EffectiveSettings without a registry row, "+
			"so its switch would be settable and never consulted", keyed, len(byKey))
	}
}

// The sign-in kind has its own switch, on by default: an outage stops every chat and run, and the person may be
// away from every page that would say so.
func TestKindRegistry_SignInHasItsOwnSwitchOnByDefault(t *testing.T) {
	i := slices.IndexFunc(Kinds(), func(k KindPref) bool { return k.Kind == marotte.PushKindSignIn })
	if i < 0 {
		t.Fatal("no registry row for sign_in")
	}
	if k := Kinds()[i]; k.SettingsKey != "notify_sign_in" || !k.DefaultOn {
		t.Errorf("sign_in row = %+v, want settings key notify_sign_in, on by default", k)
	}
}

func TestService_TheSignInSwitchSilencesItsPushes(t *testing.T) {
	sub := marotte.PushSubscription{Endpoint: "https://fcm.googleapis.com/fcm/send/1"}
	s := New(t.Context(), t.TempDir(), testSubject)
	defer s.Close()
	s.Subscribe(sub)
	if !s.Wants(marotte.PushKindSignIn) {
		t.Error("Wants(sign_in) on a fresh install with a subscription = false, want true")
	}
	s.SetPreferences(map[marotte.PushKind]bool{marotte.PushKindSignIn: false})
	if s.Wants(marotte.PushKindSignIn) {
		t.Error("Wants(sign_in) with its switch off = true, want false")
	}
}
