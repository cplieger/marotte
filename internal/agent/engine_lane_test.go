package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// laneRequest is one KAS request on the engine lane.
func laneRequest(id int64, method, sessionID string) *marotte.RPCResponse {
	params, _ := json.Marshal(map[string]any{"sessionId": sessionID})
	return &marotte.RPCResponse{Method: method, ID: &id, Params: params}
}

// awaitAnswers waits until the engine lane answered every id, then returns the answers it wrote.
func awaitAnswers(t *testing.T, src *fakeEngineSource, before int, ids ...int64) []int64 {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := src.lane.answeredIDs()[before:]
		done := true
		for _, id := range ids {
			if !slices.Contains(got, id) {
				done = false
			}
		}
		if done {
			time.Sleep(10 * time.Millisecond)
			return src.lane.answeredIDs()[before:]
		}
		if time.Now().After(deadline) {
			t.Fatalf("answers = %v, want every one of %v", got, ids)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestEngineLane_AnswersEveryRequestOnce(t *testing.T) {
	h, src := declRuntime(t, &declBackend{callback: "_test/auth/token"})
	e := src.current()
	rows := []*marotte.RPCResponse{
		laneRequest(1, "_test/auth/token", ""),
		laneRequest(2, methodKiroShellType, ""),
		laneRequest(3, marotte.MethodRequestPermission, "sess_unknown"),
		laneRequest(4, "fs/read_text_file", "sess_unknown"),
		laneRequest(5, "terminal/create", ""),
		laneRequest(6, "_kiro/userInput", "sess_unknown"),
		laneRequest(7, "_kiro/some/future/verb", ""),
		laneRequest(8, methodKiroSecretGet, ""),
	}
	before := len(src.lane.answeredIDs())
	for _, msg := range rows {
		h.engineFrame(e, msg)
	}
	got := awaitAnswers(t, src, before, 1, 2, 3, 4, 5, 6, 7, 8)
	for _, msg := range rows {
		n := 0
		for _, id := range got {
			if id == *msg.ID {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s (id %d) answered %d times, want exactly once", msg.Method, *msg.ID, n)
		}
	}
}

func TestEngineLane_AHeldRequestDoesNotBlockLaterFrames(t *testing.T) {
	held := make(chan struct{})
	h, src := declRuntime(t, &declBackend{callback: "_test/auth/token", held: held})
	e := src.current()
	before := len(src.lane.answeredIDs())

	h.engineFrame(e, laneRequest(1, "_test/auth/token", ""))
	h.engineFrame(e, laneRequest(2, methodKiroShellType, ""))

	if got := src.lane.answeredIDs()[before:]; !slices.Equal(got, []int64{2}) {
		t.Errorf("answers while the sign-in answer is held = %v, want the shell type's [2] at once", got)
	}
	close(held)
	awaitAnswers(t, src, before, 1, 2)
}

func TestEngineLane_HostCallbackRoutedByTheDeclaration(t *testing.T) {
	h, src := declRuntime(t, &declBackend{callback: "_test/auth/token"})
	e := src.current()
	before := len(src.lane.answeredIDs())

	h.engineFrame(e, laneRequest(1, "_test/auth/token", ""))
	awaitAnswers(t, src, before, 1)
	if _, ok := h.signin.hostCallback("_kiro/auth/getAccessToken"); ok {
		t.Error("the engine lane takes _kiro/auth/getAccessToken from a backend that never declared it")
	}
	if _, ok := h.signin.hostCallback("_test/auth/token"); !ok {
		t.Error("the declared callback is not the sign-in's")
	}
}

func TestRestartRoute_ClientDisconnectAfterThe202StillCompletes(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	src := fakeSourceOf(t, h)
	ctx, cancel := context.WithCancel(t.Context())
	req := httptest.NewRequest(http.MethodPost, "/api/engine/restart", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	h.handleEngineRestart(rec, req)
	cancel()

	if rec.Code != http.StatusAccepted {
		t.Fatalf("restart = %d, want 202", rec.Code)
	}
	waitGenerations(t, src, 2)
	waitReady(t, h.engine, 2)
}

func TestRestartRoute_SecondRequestWhileRunningIs409(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	src := fakeSourceOf(t, h)
	var stopping atomic.Bool
	release := make(chan struct{})
	src.mu.Lock()
	src.stopHook = func() {
		if stopping.CompareAndSwap(false, true) {
			<-release
		}
	}
	src.mu.Unlock()

	post := func() int {
		rec := httptest.NewRecorder()
		h.handleEngineRestart(rec, httptest.NewRequest(http.MethodPost, "/api/engine/restart", nil))
		return rec.Code
	}
	if code := post(); code != http.StatusAccepted {
		t.Fatalf("first restart = %d, want 202", code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !stopping.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the restart never reached the engine's stop")
		}
		time.Sleep(time.Millisecond)
	}
	if code := post(); code != http.StatusConflict {
		t.Errorf("a restart while one runs = %d, want 409", code)
	}
	close(release)
	waitReady(t, h.engine, 2)
}
