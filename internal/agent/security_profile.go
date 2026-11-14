package agent

import (
	"context"
	"log/slog"

	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/marotte/internal/settings"
)

// securityProfileID is the stored security_profile; empty when absent or invalid.
func securityProfileID(d settings.Document) string {
	var id string
	d.Into(settings.KeySecurityProfile, &id)
	return id
}

// knownProfile is id when it names a profile, else the default profile. Empty is the default; an unknown id falls
// back loudly rather than to nothing, which means Custom and would drop the fs_read floor on a typo.
func knownProfile(id string) string {
	if id == "" {
		return policyfile.DefaultProfile
	}
	if _, ok := policyfile.ProfileFor(id); !ok {
		slog.Warn("unknown security profile; falling back", "value", id, "fallback", policyfile.DefaultProfile)
		return policyfile.DefaultProfile
	}
	return id
}

// securityPresets resolves a security profile id into the KAS preset ids a session opens with (SessionOpts.Presets),
// one resolver for every session.
func securityPresets(id string) []string {
	p, _ := policyfile.ProfileFor(knownProfile(id))
	return p.Presets
}

// settingsProfile is the Settings profile as a known id, the profile a chat with none of its own is pinned to. ok is
// false while config.json has never been readable: its defaults are no choice to pin.
func (rt *Runtime) settingsProfile(ctx context.Context) (id string, ok bool) {
	doc, src := rt.configDocument(ctx)
	if src == fromDefaults {
		return "", false
	}
	return knownProfile(doc.session.profile), true
}

// pinSecurityProfile gives a chat with no profile of its own the Settings profile at its door, so a later Settings
// change leaves it on the profile it opened with.
func (bc *bridgeCoordinator) pinSecurityProfile(ctx context.Context, chatID marotte.ChatID, rec *marotte.Chat) {
	if rec.SecurityProfile != "" {
		return
	}
	id, ok := bc.settingsProfile(ctx)
	if !ok {
		return
	}
	pinned := id
	if _, err := bc.chatStore.Mutate(durable.Context(ctx), chatID, func(c *marotte.Chat, ex bool) bool {
		if !ex || c.SecurityProfile != "" {
			pinned = c.SecurityProfile
			return false
		}
		c.SecurityProfile = id
		return true
	}); err != nil {
		slog.Error("security profile: pin the chat to the Settings profile", "chat_id", chatID, "error", err)
		return
	}
	rec.SecurityProfile = pinned
}

// ReloadChatProfile reloads chatID's resident session on the profile its header now names, in the background so the
// chat's next send does not wait on the load. A run's host session and a chat with none resident are left alone: the
// next door sends the header's profile.
func (rt *Runtime) ReloadChatProfile(chatID marotte.ChatID) {
	sb := rt.bridge.mgr.get(chatID)
	lc, ok := rt.coord.turns.closeOwner(chatID, sb)
	if !ok {
		return
	}
	lc.markReload(sb)
	rt.lifecycle.goUnlessDraining(func() { rt.coord.reloadWhenUnheld(chatID, lc, sb) })
}

// reloadWhenUnheld loads chatID's next generation once nothing holds sb's marked reload. It waits itself because a
// kept chat's closer never closes it, so a reload a turn held would otherwise wait for the next send.
func (bc *bridgeCoordinator) reloadWhenUnheld(chatID marotte.ChatID, lc *chatLifecycle, sb *sharedBridge) {
	ctx, cancel := bc.lifecycle.derivedContext()
	defer cancel()
	for lc.awaitReloadUnheld(ctx, sb) && !bc.configHeld() && bc.bridge.mgr.get(chatID) == sb {
		if err := bc.LoadSession(ctx, chatID); err != nil {
			slog.Warn("a chat's session could not reload on its new security profile; its next message reloads it",
				"chat_id", chatID, "error", err)
			return
		}
	}
}

// awaitReloadUnheld blocks until no hold refuses sb's marked reload, reporting whether the reload is still owed: false
// once the mark is gone (a door reloaded the chat), the life ended or ctx did.
func (lc *chatLifecycle) awaitReloadUnheld(ctx context.Context, sb *sharedBridge) bool {
	for {
		lc.mu.Lock()
		if lc.ended || lc.reloadFor != sb {
			lc.mu.Unlock()
			return false
		}
		if lc.blockerLocked(0) == holdKinds {
			lc.mu.Unlock()
			return true
		}
		changed := lc.changed
		lc.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return false
		}
	}
}
