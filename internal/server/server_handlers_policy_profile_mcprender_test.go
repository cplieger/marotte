package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/marotte/internal/settings"
)

// fakeMCPRender records each re-render AND the profile the setting held when it
// ran. The second half is what makes the ordering assertion possible rather than
// assumed: the real renderer resolves the rung by reading that setting, so a
// re-render that ran before persistProfile would render the OUTGOING profile's
// auto-approve posture and the suspension would silently not happen.
type fakeMCPRender struct {
	configDir string
	sawEmpty  bool
	// attached reports that a render's context was still tied to the reader's
	// request. durable.Context answers nil for Done, so a non-nil one means a tab
	// close mid-request cancels the render — writeKASConfig returns early on a dead
	// context — leaving the OUTGOING rung's autoApprove grants in KAS's mcp.json.
	attached bool
	seen     []string
	renders  int
	err      error
}

func (f *fakeMCPRender) RenderKASConfig(ctx context.Context) error {
	f.renders++
	if ctx.Done() != nil {
		f.attached = true
	}
	var id string
	if !settings.FieldInto(ctx, f.configDir, settings.KeySecurityProfile, &id) {
		f.sawEmpty = true
	}
	f.seen = append(f.seen, id)
	return f.err
}

// TestPolicyProfile_RendersTheMCPConfigAfterPersisting is the ordering test, and
// it asserts the profile the renderer OBSERVED rather than merely that it ran.
// KAS watches the rendered file and re-merges on change, so this write is what
// applies the new rung's posture to the chats already running; running it any
// earlier would apply the previous rung's.
func TestPolicyProfile_RendersTheMCPConfigAfterPersisting(t *testing.T) {
	s, _, _, _, _ := profileFixture(t, nil)
	render := &fakeMCPRender{configDir: s.configDir}
	s.mcpRender = render

	if rec := postProfile(t, s, profileBody{Profile: policyfile.ProfileTrusted}); rec.Code != http.StatusOK {
		t.Fatalf("POST profile = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if render.renders != 1 {
		t.Fatalf("RenderKASConfig ran %d times, want 1; without it the previous rung's auto-approve posture stands on every live chat",
			render.renders)
	}
	if render.sawEmpty {
		t.Error("the re-render ran before the profile was persisted: it read no security_profile at all, so it would render the outgoing rung")
	}
	if got := render.seen[0]; got != policyfile.ProfileTrusted {
		t.Errorf("the re-render observed profile %q, want %q: it must run AFTER persistProfile or it renders the outgoing rung's posture",
			got, policyfile.ProfileTrusted)
	}
}

// TestPolicyProfile_AnUnwiredRendererIsNotAFailure: the option is optional, and a
// composition without it must still be able to select a profile. The cost is
// stated at the wiring site — the posture then applies at the next render — and it
// is not a reason to refuse the selection.
func TestPolicyProfile_AnUnwiredRendererIsNotAFailure(t *testing.T) {
	s, _, _, _, _ := profileFixture(t, nil)
	s.mcpRender = nil

	if rec := postProfile(t, s, profileBody{Profile: policyfile.ProfileGuarded}); rec.Code != http.StatusOK {
		t.Fatalf("POST profile = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := s.activeProfile(t.Context()); got != policyfile.ProfileGuarded {
		t.Errorf("active profile = %q, want %q", got, policyfile.ProfileGuarded)
	}
}

// TestPolicyProfile_ARenderFailureStillAnswers200 pins the deliberate asymmetry
// with failProfileSelection. By this point both policy files and config.json have
// landed, so the selection HAS happened: a 500 would tell the user their profile
// did not change when it did, and restoring the policy files here would leave
// config.json naming a profile whose rules are no longer on disk. So the failure is
// logged with its consequence and the selection is reported as what it is.
func TestPolicyProfile_ARenderFailureStillAnswers200(t *testing.T) {
	s, eng, reload, _, _ := profileFixture(t, nil)
	render := &fakeMCPRender{configDir: s.configDir, err: errors.New("disk full")}
	s.mcpRender = render

	if rec := postProfile(t, s, profileBody{Profile: policyfile.ProfileTrusted}); rec.Code != http.StatusOK {
		t.Fatalf("POST profile = %d, want 200: a failed MCP re-render must not report a selection that did happen as failed: %s",
			rec.Code, rec.Body.String())
	}
	if got := s.activeProfile(t.Context()); got != policyfile.ProfileTrusted {
		t.Errorf("active profile = %q, want %q: the selection must stand", got, policyfile.ProfileTrusted)
	}
	if reload.restarts != 1 {
		t.Errorf("RestartUtilitySession ran %d times, want 1: a render failure must not skip the rest of the fan-out", reload.restarts)
	}
	if len(eng.events) == 0 {
		t.Error("no events broadcast: a render failure must not silence the fan-out the client refetches on")
	}
}

// patchSecurityProfile drives the settings write the way a client does, so the arm
// under test is reached through its own gate rather than called directly.
func patchSecurityProfile(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/settings", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	s.handleSettingsWrite(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH /api/settings = %d, want 200: %s", rec.Code, rec.Body)
	}
	return rec
}

// TestSettingsWrite_RendersTheMCPConfigForTheRungsSecondWriter pins the OTHER
// writer of the rung. security_profile is a KnownKeys entry, so PATCH
// /api/settings sets it too, and the re-render was wired to the picker's endpoint
// alone: the panel resolves the posture live on every GET /api/mcp and would say
// suspended while KAS's mcp.json still carried autoApprove for that server — the
// wider grant, standing under the suspension notice that denies it.
//
// It asserts the id the renderer OBSERVED, which pins the ordering the same way
// the profile endpoint's test does: the arm has to run after settings.Update or it
// renders the outgoing rung.
func TestSettingsWrite_RendersTheMCPConfigForTheRungsSecondWriter(t *testing.T) {
	dir := t.TempDir()
	render := &fakeMCPRender{configDir: dir}
	s := &Server{agent: &fakeEngine{}, push: &testPush{}, configDir: dir, mcpRender: render}

	patchSecurityProfile(t, s, `{"`+settings.KeySecurityProfile+`":"`+policyfile.ProfileGuarded+`"}`)

	if render.renders != 1 {
		t.Fatalf("RenderKASConfig ran %d times, want 1; without it mcp.json keeps the outgoing rung's autoApprove grants while the panel says suspended",
			render.renders)
	}
	if render.sawEmpty {
		t.Error("the re-render read no security_profile at all: it must run after the settings write")
	}
	if got := render.seen[0]; got != policyfile.ProfileGuarded {
		t.Errorf("the re-render observed profile %q, want %q", got, policyfile.ProfileGuarded)
	}
}

// TestSettingsWrite_DoesNotRenderForAnUnrelatedKey is the control the case above
// needs: an arm that ignored its gate and rendered on every PATCH would satisfy it.
// Every file-browser navigation PATCHes fb_path, so that is the traffic a
// gate-free arm would spend a KAS render on.
func TestSettingsWrite_DoesNotRenderForAnUnrelatedKey(t *testing.T) {
	dir := t.TempDir()
	render := &fakeMCPRender{configDir: dir}
	s := &Server{agent: &fakeEngine{}, push: &testPush{}, configDir: dir, mcpRender: render}

	patchSecurityProfile(t, s, `{"`+settings.KeyFBPath+`":"/workspace/src"}`)

	if render.renders != 0 {
		t.Errorf("RenderKASConfig ran %d times for a patch that does not touch %s, want 0",
			render.renders, settings.KeySecurityProfile)
	}
}

// TestPolicyProfile_RendersOnAContextDetachedFromTheReader is the disconnect case.
// The selection has already landed by the time the render runs, so the render must
// not die with the reader: writeKASConfig returns early on a cancelled context, and
// a tab closed mid-POST would then leave the new rung on disk while KAS's mcp.json
// still carries the OUTGOING rung's autoApprove grants for every live chat — the
// wider posture, until the next MCP mutation or a restart.
//
// The request carries a LIVE cancellable context rather than a cancelled one,
// because the writes ahead of the render take the same context and refuse a dead
// one; the property under test is the detachment itself, which durable.Context
// states as a nil Done channel.
func TestPolicyProfile_RendersOnAContextDetachedFromTheReader(t *testing.T) {
	s, _, _, _, _ := profileFixture(t, nil)
	render := &fakeMCPRender{configDir: s.configDir}
	s.mcpRender = render

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	b, err := json.Marshal(profileBody{Profile: policyfile.ProfileGuarded})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/permissions/profile", bytes.NewReader(b)).WithContext(ctx)
	rec := httptest.NewRecorder()
	s.handlePolicyProfile(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST profile = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if render.renders != 1 {
		t.Fatalf("RenderKASConfig ran %d times, want 1", render.renders)
	}
	if render.attached {
		t.Error("the re-render ran on the request's own context: a tab close mid-POST cancels it and leaves the outgoing rung's autoApprove grants in KAS's mcp.json")
	}
}
