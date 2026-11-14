package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/marotte/internal/translate"
)

func writeProfileSetting(t *testing.T, dir, value string) {
	t.Helper()
	body := map[string]any{}
	if value != "" {
		body[settings.KeySecurityProfile] = value
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
}

// TestSecurityPresets_ResolvesEachProfile pins each profile's preset set; Custom resolves to none, withholding the wire key.
func TestSecurityPresets_ResolvesEachProfile(t *testing.T) {
	for _, p := range policyfile.Profiles() {
		t.Run(p.ID, func(t *testing.T) {
			dir := t.TempDir()
			writeProfileSetting(t, dir, p.ID)
			got := securityPresets(securityProfileID(docAt(t, dir)))
			if !slices.Equal(got, p.Presets) {
				t.Errorf("securityPresets(%q) = %v, want %v", p.ID, got, p.Presets)
			}
		})
	}
}

// TestSecurityPresets_FallsBackLoudlyNotSilently pins that an unknown id resolves to the default, never to Custom's empty set.
func TestSecurityPresets_FallsBackLoudlyNotSilently(t *testing.T) {
	fallback, ok := policyfile.ProfileFor(policyfile.DefaultProfile)
	if !ok {
		t.Fatal("DefaultProfile does not resolve; the fallback itself is broken")
	}
	for _, value := range []string{"", "nonexistent", "Guarded", "yolo"} {
		t.Run("value="+value, func(t *testing.T) {
			dir := t.TempDir()
			writeProfileSetting(t, dir, value)
			got := securityPresets(securityProfileID(docAt(t, dir)))
			if len(got) == 0 {
				t.Fatalf("securityPresets(%q) returned nothing; that is the Custom wire and would drop the fs_read floor", value)
			}
			if !slices.Equal(got, fallback.Presets) {
				t.Errorf("securityPresets(%q) = %v, want the default profile's %v", value, got, fallback.Presets)
			}
		})
	}
}

// TestSecurityPresets_AbsentConfigResolvesToDefault covers a first boot, a different reader path.
func TestSecurityPresets_AbsentConfigResolvesToDefault(t *testing.T) {
	fallback, _ := policyfile.ProfileFor(policyfile.DefaultProfile)
	got := securityPresets(securityProfileID(settings.Document{}))
	if !slices.Equal(got, fallback.Presets) {
		t.Errorf("securityPresets with no config.json = %v, want %v", got, fallback.Presets)
	}
}

// TestSecurityPresets_CallerCannotMutateTheProfile pins that the returned slice must not alias the profile.
func TestSecurityPresets_CallerCannotMutateTheProfile(t *testing.T) {
	dir := t.TempDir()
	writeProfileSetting(t, dir, policyfile.ProfileTrusted)
	first := securityPresets(securityProfileID(docAt(t, dir)))
	if len(first) == 0 {
		t.Fatal("trusted resolved to nothing")
	}
	for i := range first {
		first[i] = "tampered"
	}
	second := securityPresets(securityProfileID(docAt(t, dir)))
	if slices.Contains(second, "tampered") {
		t.Error("securityPresets hands out the package's own slice; one caller's write reaches every later session")
	}
}

func profilePresets(t *testing.T, id string) []string {
	t.Helper()
	p, ok := policyfile.ProfileFor(id)
	if !ok {
		t.Fatalf("Setup: no profile %q", id)
	}
	return p.Presets
}

func TestChatDoor_SendsTheChatsOwnProfileOverTheSettingsOne(t *testing.T) {
	h, configDir := reopenFixture(t)
	writeSetting(t, configDir, settings.KeySecurityProfile, policyfile.ProfileUnrestricted)
	if _, err := h.chatStore.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.SecurityProfile = policyfile.ProfileReadOnly
		return true
	}); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	sb, err := h.coord.openReleased(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}

	if got, want := fakeOf(sb).lastSessionOpts().Presets, profilePresets(t, policyfile.ProfileReadOnly); !slices.Equal(got, want) {
		t.Errorf("the door of a read-only chat under an unrestricted Settings profile sent %v, want the chat's %v", got, want)
	}
}

// A chat with no profile of its own takes the Settings profile at its first door and keeps it: a later Settings
// change is the default for new chats only.
func TestChatDoor_PinsAChatWithNoProfileToTheSettingsProfile(t *testing.T) {
	h, configDir := reopenFixture(t)
	writeSetting(t, configDir, settings.KeySecurityProfile, policyfile.ProfileTrusted)
	h.ReconcileSessionSettings(t.Context())

	first, err := h.coord.openReleased(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	if rec, _ := h.chatStore.Get(t.Context(), "c1"); rec.SecurityProfile != policyfile.ProfileTrusted {
		t.Errorf("the header's security_profile after the first door = %q, want %q", rec.SecurityProfile, policyfile.ProfileTrusted)
	}
	writeSetting(t, configDir, settings.KeySecurityProfile, policyfile.ProfileUnrestricted)
	h.SecurityProfileChanged(t.Context())
	h.markSessionsForReload("test")

	second, err := h.coord.openReleased(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge after the reload mark: %v", err)
	}
	if second == first {
		t.Fatal("Setup: the marked chat did not reload")
	}
	if got, want := fakeOf(second).lastSessionOpts().Presets, profilePresets(t, policyfile.ProfileTrusted); !slices.Equal(got, want) {
		t.Errorf("the reloaded door sent %v, want the pinned trusted profile's %v", got, want)
	}
}

// While config.json has never read, the door sends the default profile, so the header names it: a later readable
// Settings profile is the default for new chats only.
func TestChatDoor_PinsTheDefaultProfileWhileConfigHasNeverRead(t *testing.T) {
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	h := reopenFixtureIn(t, configDir, nil)

	sb, err := h.coord.openReleased(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	if got, want := fakeOf(sb).lastSessionOpts().Presets, profilePresets(t, policyfile.DefaultProfile); !slices.Equal(got, want) {
		t.Fatalf("Setup: the door sent %v, want the default profile's %v", got, want)
	}
	if rec, _ := h.chatStore.Get(t.Context(), "c1"); rec.SecurityProfile != policyfile.DefaultProfile {
		t.Errorf("the header's security_profile = %q, want %q, the profile its session opened on", rec.SecurityProfile, policyfile.DefaultProfile)
	}

	writeProfileSetting(t, configDir, policyfile.ProfileGuarded)
	h.ReconcileSessionSettings(t.Context())
	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge after config.json reads: %v", err)
	}
	if rec, _ := h.chatStore.Get(t.Context(), "c1"); rec.SecurityProfile != policyfile.DefaultProfile {
		t.Errorf("a readable guarded config.json moved the header to %q, want it left on %q", rec.SecurityProfile, policyfile.DefaultProfile)
	}
}

func TestChatDoor_AnUnknownChatProfileFallsBackToTheDefault(t *testing.T) {
	h, _ := reopenFixture(t)
	if _, err := h.chatStore.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.SecurityProfile = "no-such-profile"
		return true
	}); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	sb, err := h.coord.openReleased(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}

	if got, want := fakeOf(sb).lastSessionOpts().Presets, profilePresets(t, policyfile.DefaultProfile); !slices.Equal(got, want) {
		t.Errorf("the door of a chat naming an unknown profile sent %v, want the default's %v", got, want)
	}
}

func setChatProfile(t *testing.T, h *Runtime, chatID marotte.ChatID, profile string) {
	t.Helper()
	if _, err := h.chatStore.Mutate(t.Context(), chatID, func(c *marotte.Chat, _ bool) bool {
		c.SecurityProfile = profile
		return true
	}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
}

// awaitNextGeneration polls until chatID's resident entry is a settled generation other than prev.
func awaitNextGeneration(t *testing.T, h *Runtime, chatID marotte.ChatID, prev *sharedBridge) *sharedBridge {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if sb := h.bridge.mgr.get(chatID); sb != nil && sb != prev {
			if err := sb.settled.wait(t.Context()); err != nil {
				t.Fatalf("the next generation failed: %v", err)
			}
			return sb
		}
	}
	t.Fatalf("no new session for %s within 5s: the profile change was not loaded in the background", chatID)
	return nil
}

func TestReloadChatProfile_LoadsAnIdleChatOnItsNewProfileInTheBackground(t *testing.T) {
	h, _ := reopenFixture(t)
	first, err := h.coord.openReleased(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	setChatProfile(t, h, "c2", policyfile.ProfileTrusted)
	other, err := h.coord.openReleased(t.Context(), "c2", "")
	if err != nil {
		t.Fatalf("OpenBridge c2: %v", err)
	}
	setChatProfile(t, h, "c1", policyfile.ProfileReadOnly)

	h.ReloadChatProfile("c1")

	second := awaitNextGeneration(t, h, "c1", first)
	if !sessionClosed(fakeOf(first)) {
		t.Error("the reload left the old session open")
	}
	opts := fakeOf(second).lastSessionOpts()
	if opts.SessionID != string(first.SessionID()) {
		t.Errorf("the reload loaded %q, want the chat's own session %q", opts.SessionID, first.SessionID())
	}
	if want := profilePresets(t, policyfile.ProfileReadOnly); !slices.Equal(opts.Presets, want) {
		t.Errorf("the reloaded session's presets = %v, want the chat's new profile %v", opts.Presets, want)
	}
	if h.bridge.mgr.get("c2") != other || sessionClosed(fakeOf(other)) {
		t.Error("one chat's profile change reloaded another chat")
	}
}

func TestReloadChatProfile_ARunningTurnHoldsTheReloadUntilItEnds(t *testing.T) {
	h, _ := reopenFixture(t)
	first, err := h.coord.openReleased(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	h.coord.WireTurnStart(t.Context(), "c1", translate.TurnStartFrame{})
	setChatProfile(t, h, "c1", policyfile.ProfileReadOnly)

	h.ReloadChatProfile("c1")

	if h.bridge.mgr.get("c1") != first || sessionClosed(fakeOf(first)) {
		t.Fatal("the reload closed the session under a running turn")
	}
	h.coord.WireTurnEnd(t.Context(), "c1", marotte.StopReasonEndTurn)
	second := awaitNextGeneration(t, h, "c1", first)
	if want := profilePresets(t, policyfile.ProfileReadOnly); !slices.Equal(fakeOf(second).lastSessionOpts().Presets, want) {
		t.Errorf("the session loaded after the turn has presets %v, want %v", fakeOf(second).lastSessionOpts().Presets, want)
	}
}

// A session that refuses its close keeps the reload owed to the chat's next send: the background reload tries once.
func TestReloadChatProfile_ARefusedCloseLeavesTheReloadToTheNextSend(t *testing.T) {
	h, _ := reopenFixture(t)
	first, err := h.coord.openReleased(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	var closes atomic.Int32
	br := fakeOf(first)
	br.mu.Lock()
	br.closeErr = errors.New("session/close refused")
	br.onCloseStart = func() { closes.Add(1) }
	br.mu.Unlock()
	setChatProfile(t, h, "c1", policyfile.ProfileReadOnly)

	h.ReloadChatProfile("c1")

	for deadline := time.Now().Add(5 * time.Second); closes.Load() == 0 && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	if n := closes.Load(); n != 1 {
		t.Fatalf("the background reload asked the session to close %d times, want 1: a refusal waits for the next send", n)
	}
	if h.bridge.mgr.get("c1") != first {
		t.Fatal("the refused close replaced the session")
	}

	br.mu.Lock()
	br.closeErr = nil
	br.mu.Unlock()
	second, err := h.coord.openReleased(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge after the refusal: %v", err)
	}
	if second == first {
		t.Fatal("the next door kept the session whose reload was refused")
	}
	if want := profilePresets(t, policyfile.ProfileReadOnly); !slices.Equal(fakeOf(second).lastSessionOpts().Presets, want) {
		t.Errorf("the next door loaded presets %v, want the chat's new profile %v", fakeOf(second).lastSessionOpts().Presets, want)
	}
}
