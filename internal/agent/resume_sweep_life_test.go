package agent

import (
	"strings"
	"testing"
	"time"
)

// runLists is how many run lists br answered.
func runLists(br *fakeBridge) int {
	n := 0
	for _, m := range br.callLog() {
		if m == methodKiroWorkflowList {
			n++
		}
	}
	return n
}

func awaitListCount(t *testing.T, br *fakeBridge, want int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); runLists(br) < want; {
		if time.Now().After(deadline) {
			t.Fatalf("run lists = %d, want %d", runLists(br), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// sweepHub is parkChat with one run its session launched that KAS still lists running, so the first sweep schedules
// a later one, orphanResumeDelay shortened.
func sweepHub(t *testing.T) (*Runtime, *fakeBridge, *chatLifecycle) {
	t.Helper()
	prev := orphanResumeDelay
	orphanResumeDelay = 20 * time.Millisecond
	t.Cleanup(func() { orphanResumeDelay = prev })
	h, br := agentRunOnChat(t, "wf_1")
	t.Cleanup(func() { shutdownHub(t, h) })
	br.setCallResult(methodKiroWorkflowList, kasRuns(t, map[string]any{
		"workflowId": "wf_1", "status": "running", "parentSessionId": parkSession,
	}))
	return h, br, h.coord.turns.testLifecycle(parkChat)
}

const standDownLine = "rehydrate: the chat closed; its later run sweeps stand down"

func TestResumeSweep_AClosedChatsLaterSweepStandsDown(t *testing.T) {
	t.Run("the life ended before the sweep fired", func(t *testing.T) {
		logs := captureLogs(t)
		h, br, lc := sweepHub(t)
		h.runs.resumeInterruptedRuns(t.Context(), lc, parkChat)
		if n := runLists(br); n != 1 {
			t.Fatalf("Setup: the first sweep listed %d times, want 1", n)
		}
		h.coord.turns.endLife(parkChat)
		for deadline := time.Now().Add(5 * time.Second); !strings.Contains(logs.String(), standDownLine); {
			if time.Now().After(deadline) {
				t.Fatalf("no stand-down was logged; logs:\n%s", logs)
			}
			time.Sleep(time.Millisecond)
		}
		if n := runLists(br); n != 1 {
			t.Errorf("run lists = %d after the life ended, want only the first sweep's", n)
		}
		if strings.Contains(logs.String(), "could not be listed or resumed by any sweep") {
			t.Errorf("a stand-down was reported as a spent sweep; logs:\n%s", logs)
		}
	})
	t.Run("a sweep running at the close ends and is waited for", func(t *testing.T) {
		h, br, lc := sweepHub(t)
		resumes := countResumes(t, br, "wf_1")
		h.runs.resumeInterruptedRuns(t.Context(), lc, parkChat)
		block := make(chan struct{})
		t.Cleanup(func() { close(block) })
		br.mu.Lock()
		br.blockOn = map[string]chan struct{}{methodKiroWorkflowList: block}
		br.mu.Unlock()
		awaitListCount(t, br, 2)
		h.coord.turns.endLife(parkChat)
		select {
		case <-lc.quiet:
		case <-time.After(5 * time.Second):
			t.Fatal("the ended life never went quiet: the running sweep did not end on its context")
		}
		if n := resumes.Load(); n != 0 {
			t.Errorf("resumes = %d, want none from a sweep cut by the close", n)
		}
	})
	t.Run("a live chat's later sweep runs", func(t *testing.T) {
		h, br, lc := sweepHub(t)
		h.runs.resumeInterruptedRuns(t.Context(), lc, parkChat)
		awaitListCount(t, br, 2)
	})
}
