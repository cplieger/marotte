package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/kirosession"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// An observer pass opens a session with the profile's presets as they are now and nothing else, answers every tool
// or permission request on it with a refusal, and closes the session when its reads return.
func TestObserverPass_OpensWithTheCurrentPresetsRefusesToolRequestsAndCloses(t *testing.T) {
	t.Parallel()
	h, dir := reopenFixture(t)
	awaitBoot(h)
	writeSetting(t, dir, settings.KeySecurityProfile, "unrestricted")
	o := oneObserver(t, h)
	fsID, permID := int64(41), int64(42)

	err := h.observe(t.Context(), func(_ context.Context, s engineSession) error {
		params := json.RawMessage(`{"sessionId":"` + string(s.SessionID()) + `"}`)
		o.deliver(&marotte.RPCResponse{ID: &fsID, Method: "fs/read_text_file", Params: params})
		o.deliver(&marotte.RPCResponse{ID: &permID, Method: marotte.MethodRequestPermission, Params: params})
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
			if _, a := o.responseTo(fsID); a {
				if _, b := o.responseTo(permID); b {
					return nil
				}
			}
		}
		return errors.New("the pass's requests were not answered within 5s")
	})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}

	so := o.lastSessionOpts()
	if so.Role != marotte.SessionRoleObserver || so.Mode != "" || so.Model != "" || so.SessionID != "" {
		t.Errorf("door = {Role %v, Mode %q, Model %q, SessionID %q}, want a new observer session with no mode or model",
			so.Role, so.Mode, so.Model, so.SessionID)
	}
	if !slices.Equal(so.Presets, []string{"allow-all"}) {
		t.Errorf("presets = %v, want [allow-all] from the profile set just now", so.Presets)
	}
	var rpcErr *marotte.RPCError
	if r, _ := o.responseTo(fsID); !errors.As(r.err, &rpcErr) || rpcErr.Code != marotte.RPCCodeMethodNotFound {
		t.Errorf("fs request answered %+v, want a method-not-found refusal", r)
	}
	if r, _ := o.responseTo(permID); !samePermissionOutcome(r.result, marotte.PermissionOutcomeCancelled()) {
		t.Errorf("permission request answered %+v, want cancelled", r)
	}
	if !sessionClosed(o) {
		t.Error("the observer pass left its session open")
	}
}

// A pass whose close KAS refuses deletes the session on the engine, so KAS drops it, and then closes it, so marotte's
// lane for it ends: nothing is left live.
func TestObserverPass_ARefusedCloseDeletesTheSessionAndClosesIt(t *testing.T) {
	t.Parallel()
	h, _ := reopenFixture(t)
	awaitBoot(h)
	o := oneObserver(t, h)
	o.mu.Lock()
	o.closeErr = fmt.Errorf("ACP error -32603: %w", &marotte.RPCError{Code: marotte.RPCCodeInternal, Message: "a lifecycle operation is in progress"})
	o.mu.Unlock()
	src := fakeSourceOf(t, h)
	var deleted []string
	src.mu.Lock()
	src.callGate = func(_ context.Context, method string, resp *marotte.RPCResponse, err error) (*marotte.RPCResponse, error) {
		if method == marotte.MethodSessionDelete {
			deleted = append(deleted, string(o.SessionID()))
			o.mu.Lock()
			o.closeErr = nil
			o.mu.Unlock()
		}
		return resp, err
	}
	src.mu.Unlock()

	if err := h.observe(t.Context(), func(context.Context, engineSession) error { return nil }); err != nil {
		t.Fatalf("observe: %v", err)
	}

	if want := []string{string(o.SessionID())}; !slices.Equal(deleted, want) {
		t.Errorf("engine session/delete sent for %v, want %v", deleted, want)
	}
	if !sessionClosed(o) {
		t.Error("the pass's session stayed open after its refused close")
	}
}

// passFixture is a runtime whose observer passes all open as sessionID on one fake, with a reaper over sessionsDir.
func passFixture(t *testing.T, sessionsDir, sessionID string) (*Runtime, *fakeBridge) {
	t.Helper()
	cs := newTestChatStore()
	h := newRuntimeOn(t.Context(), testReaperWorkDir, fakeEngineOf(func() engineSession { return newFakeBridge() }, nil), cs,
		WithSessionReaper(kirosession.New(sessionsDir, testReaperWorkDir)))
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	awaitBoot(h)
	o := oneObserver(t, h)
	o.mu.Lock()
	o.sessionID = sessionID
	o.mu.Unlock()
	return h, o
}

// observeWritingState runs one pass that leaves KAS's on-disk state for its session at dir, as KAS does for every
// session it opens.
func observeWritingState(t *testing.T, h *Runtime, dir string) {
	t.Helper()
	err := h.observe(t.Context(), func(context.Context, engineSession) error {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		writeSessionRecord(t, dir, testReaperWorkDir)
		return nil
	})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
}

// Nothing loads a pass's session again, so a closed pass leaves no state on disk: left there, it is a session no
// chat names, which the orphan sweep refuses to judge on an install that has no chat yet.
func TestObserverPass_RemovesItsSessionStateOnceClosed(t *testing.T) {
	t.Parallel()
	sessionsDir := t.TempDir()
	h, o := passFixture(t, sessionsDir, "sess_pass1")
	dir := filepath.Join(sessionsDir, "hash01", "sess_pass1")

	observeWritingState(t, h, dir)

	if !sessionClosed(o) {
		t.Fatal("Setup: the pass left its session open")
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stat %s after the pass = %v, want not exist: the closed pass left its session state on disk", dir, err)
	}
}

// A pass whose session neither closes nor deletes is still live in KAS, so its state stays.
func TestObserverPass_KeepsTheStateOfASessionItCouldNotEnd(t *testing.T) {
	t.Parallel()
	sessionsDir := t.TempDir()
	h, o := passFixture(t, sessionsDir, "sess_pass2")
	o.mu.Lock()
	o.closeErr = fmt.Errorf("ACP error -32603: %w", &marotte.RPCError{Code: marotte.RPCCodeInternal, Message: "a lifecycle operation is in progress"})
	o.mu.Unlock()
	src := fakeSourceOf(t, h)
	src.mu.Lock()
	src.callGate = func(_ context.Context, method string, resp *marotte.RPCResponse, err error) (*marotte.RPCResponse, error) {
		if method == marotte.MethodSessionDelete {
			return nil, &marotte.RPCError{Code: -32000, Message: "session owns live workflows"}
		}
		return resp, err
	}
	src.mu.Unlock()
	dir := filepath.Join(sessionsDir, "hash01", "sess_pass2")

	observeWritingState(t, h, dir)

	if _, err := os.Stat(dir); err != nil {
		t.Errorf("stat %s after the pass = %v, want it kept: KAS still holds the session", dir, err)
	}
}

func samePermissionOutcome(got, want any) bool {
	a, errA := json.Marshal(got)
	b, errB := json.Marshal(want)
	return errA == nil && errB == nil && string(a) == string(b)
}
