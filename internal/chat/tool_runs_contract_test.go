package chat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// The fixture is the contract and static-src/tool-runs-contract.test.ts is the other reader, through the
// rendered groups: a boundary scanToolRuns draws where indexGroups does not would hide from find a hook the
// transcript shows.
const toolRunsFixture = "testdata/tool_runs.json"

type toolRunsCase struct {
	Name string `json:"name"`
	Body []struct {
		ID      string            `json:"id"`
		Kind    marotte.EntryKind `json:"kind"`
		Lane    string            `json:"lane"`
		Payload json.RawMessage   `json:"payload"`
	} `json:"body"`
	LaterWake         json.RawMessage       `json:"later_wake"`
	Runs              [][]string            `json:"runs"`
	LaneRuns          map[string][][]string `json:"lane_runs"`
	StatusOffHookRows []string              `json:"status_off_hook_rows"`
}

func TestToolRunsContract_ServerHalf(t *testing.T) {
	raw, err := os.ReadFile(filepath.Clean(toolRunsFixture))
	if err != nil {
		t.Fatalf("read %s: %v", toolRunsFixture, err)
	}
	var fx struct {
		Cases []toolRunsCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("parse %s: %v", toolRunsFixture, err)
	}
	if len(fx.Cases) < 15 {
		t.Fatalf("read %d cases, want the whole fixture", len(fx.Cases))
	}
	for _, c := range fx.Cases {
		t.Run(c.Name, func(t *testing.T) {
			const turn = "t-1"
			entries := []marotte.Entry{{ID: "open-1", Turn: turn, Kind: marotte.EntryKindTurnOpen, Payload: json.RawMessage(`{}`)}}
			for _, e := range c.Body {
				entries = append(entries, marotte.Entry{ID: e.ID, Turn: turn, Lane: e.Lane, Kind: e.Kind, Payload: e.Payload})
			}
			if c.LaterWake != nil {
				entries = append(entries,
					marotte.Entry{ID: "open-2", Turn: "t-2", Kind: marotte.EntryKindTurnOpen, Payload: json.RawMessage(`{}`)},
					marotte.Entry{ID: "wake-2", Turn: "t-2", Kind: marotte.EntryKindTurnWake, Payload: c.LaterWake})
			}
			byLane := map[string][][]string{}
			for _, run := range scanToolRuns(entries, settledResults(entries)).done {
				if run[0].at >= len(c.Body)+1 {
					continue
				}
				ids := make([]string, len(run))
				for i, m := range run {
					ids[i] = m.id
				}
				lane := entries[run[0].at].Lane
				byLane[lane] = append(byLane[lane], ids)
			}
			if got := byLane[""]; !slices.EqualFunc(got, c.Runs, slices.Equal) {
				t.Errorf("runs = %v, want %v", got, c.Runs)
			}
			delete(byLane, "")
			for lane, want := range c.LaneRuns {
				if got := byLane[lane]; !slices.EqualFunc(got, want, slices.Equal) {
					t.Errorf("lane %s runs = %v, want %v", lane, got, want)
				}
			}
			for lane, got := range byLane {
				if _, listed := c.LaneRuns[lane]; !listed {
					t.Errorf("lane %s runs = %v, which lane_runs does not list", lane, got)
				}
			}
			if c.StatusOffHookRows != nil {
				if got := statusOffHookRows(entries); !slices.Equal(got, c.StatusOffHookRows) {
					t.Errorf("hook rows with status off = %v, want %v", got, c.StatusOffHookRows)
				}
			}
		})
	}
}

// statusOffHookRows are the chat lane's hook calls find counts while hooks.showStatus is off, in log order.
func statusOffHookRows(entries []marotte.Entry) []string {
	hidden := hiddenHookIDs(entries)
	rows := []string{}
	for i := range entries {
		e := &entries[i]
		var call marotte.EntryToolCall
		if e.Kind != marotte.EntryKindToolCall || e.Lane != "" || json.Unmarshal(e.Payload, &call) != nil ||
			call.Kind != marotte.ToolKindHook {
			continue
		}
		if _, gone := hidden[e.ID]; !gone {
			rows = append(rows, e.ID)
		}
	}
	return rows
}
