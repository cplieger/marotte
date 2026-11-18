package chat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// readCosts is what each transcript read costs the server per request on the long chat at x1 and x10. A change that
// moves one by design rewrites its entry in the same diff.
var readCosts = map[string]testsupport.Cost{
	"window read x1":     {Allocs: 19318, Bytes: 16905616},
	"window read x10":    {Allocs: 18830, Bytes: 17334288},
	"range read x1":      {Allocs: 2709, Bytes: 1768143},
	"range read x10":     {Allocs: 2557, Bytes: 1618385},
	"tool-call read x1":  {Allocs: 17697, Bytes: 12775147},
	"tool-call read x10": {Allocs: 176750, Bytes: 130662493},
}

// serveOK answers one GET of url from the chat router and fails t unless it is a 200.
func serveOK(t *testing.T, rt *router, url string) {
	rec := httptest.NewRecorder()
	rt.handleOne(rec, httptest.NewRequest(http.MethodGet, url, http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200; body = %.200s", url, rec.Code, rec.Body.String())
	}
}

func TestReadCost_TranscriptReadsHoldTheirBudgets(t *testing.T) {
	testsupport.RequireCostBuild(t)
	const id = marotte.ChatID("c-longchat")
	for _, scale := range []int{1, 10} {
		s, lc := longChatStore(t, id, scale)
		rt := newRouter(s)
		newest := lc.entries[len(lc.entries)-1]
		reads := []struct {
			op   string
			url  string
			runs int
		}{
			{"window read", "/api/chats/" + string(id), testsupport.CostRuns(scale, 5, 3)},
			// The client's repair read for the last five entries of a turn.
			{"range read", fmt.Sprintf("/api/chats/%s/turns/%s?after=%d", id, lc.newest, newest.Seq-5), 201},
			{"tool-call read", "/api/chats/" + string(id) + "/tools/" + lc.toolCall, testsupport.CostRuns(scale, 3, 1)},
		}
		for _, r := range reads {
			op := fmt.Sprintf("%s x%d", r.op, scale)
			t.Run(op, func(t *testing.T) {
				got := testsupport.MeasureCost(r.runs, func() { serveOK(t, rt, r.url) })
				testsupport.HoldCost(t, op, readCosts[op], got)
			})
		}
	}
}

// A whole-log read allocates in proportion to the lines it decodes, however many turns hold them: a run's log holds a
// turn per started node, ten thousand at x10.
func TestReadCost_AWholeReadCostsItsLinesNotItsTurns(t *testing.T) {
	root := t.TempDir()
	var log bytes.Buffer
	enc := json.NewEncoder(&log)
	for i := range 1000 {
		turn := fmt.Sprintf("t-%05d", i)
		for seq, kind := range []marotte.EntryKind{marotte.EntryKindTurnOpen, marotte.EntryKindTurnClose} {
			payload := json.RawMessage(`{"outcome":"completed"}`)
			if kind == marotte.EntryKindTurnOpen {
				payload = json.RawMessage(`{"source":"prompt","n":1}`)
			}
			if err := enc.Encode(marotte.Entry{ID: fmt.Sprintf("%s-%d", turn, seq), Turn: turn, Kind: kind, Payload: payload, Seq: uint64(seq)}); err != nil {
				t.Fatalf("Setup: %v", err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(root, EntriesFileName), log.Bytes(), 0o600); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	l, err := OpenEntryLog(t.Context(), root, NoHeader())
	if err != nil {
		t.Fatalf("Setup: OpenEntryLog: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	got := testsupport.MeasureCost(3, func() {
		if all, err := l.All(); err != nil || len(all) != 2000 {
			t.Fatalf("All() = %d entries, err %v; want the 2,000 lines", len(all), err)
		}
	})

	// Decoding costs about 7 B per log byte, 19 under -race; a read buffer per turn costs over 300.
	if limit := uint64(32 * log.Len()); got.Bytes > limit {
		t.Errorf("a whole read of a %d B log of 1,000 turns allocates %d B, over %d B", log.Len(), got.Bytes, limit)
	}
}
