package command

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// outcomeBridge records the result each Respond carried.
type outcomeBridge struct {
	recordingBridge
	results []any
}

func (b *outcomeBridge) Respond(_ context.Context, _ int64, result any, _ error) error {
	b.results = append(b.results, result)
	return nil
}

func TestCmdPermission_RejectionReason(t *testing.T) {
	tooLong := strings.Repeat("é", marotte.MaxRejectionReasonRunes+1)
	atCap := strings.Repeat("é", marotte.MaxRejectionReasonRunes)
	for name, tc := range map[string]struct {
		payload    marotte.PermissionResponseCommand
		wantStatus int
		wantReason string
		wantClaim  bool
	}{
		"a note rides the deny trimmed": {
			payload:    marotte.PermissionResponseCommand{RequestID: 7, OptionID: "reject", RejectionReason: "  use staging  "},
			wantStatus: http.StatusOK, wantReason: "use staging", wantClaim: true,
		},
		"a blank note is a plain deny": {
			payload:    marotte.PermissionResponseCommand{RequestID: 7, OptionID: "reject", RejectionReason: " \n "},
			wantStatus: http.StatusOK, wantClaim: true,
		},
		"a note of exactly the cap in runes is accepted": {
			payload:    marotte.PermissionResponseCommand{RequestID: 7, OptionID: "reject", RejectionReason: atCap},
			wantStatus: http.StatusOK, wantReason: atCap, wantClaim: true,
		},
		"a note over the cap is refused before the claim": {
			payload:    marotte.PermissionResponseCommand{RequestID: 7, OptionID: "reject", RejectionReason: tooLong},
			wantStatus: http.StatusBadRequest,
		},
		"a note beside file decisions is refused before the claim": {
			payload: marotte.PermissionResponseCommand{
				RequestID: 7, OptionID: "accept", RejectionReason: "no",
				FileDecisions: map[string]bool{"act-1": true},
			},
			wantStatus: http.StatusBadRequest,
		},
	} {
		t.Run(name, func(t *testing.T) {
			bridge := &outcomeBridge{}
			deps := &takeDeps{benchDeps: newBenchDeps(), bridge: bridge, takeOK: true}
			cmd := decisionCommand(t, marotte.CmdPermissionResponse, tc.payload)

			_, err := CmdPermission(t.Context(), deps, deps, cmd)

			if got := statusOf(err); got != tc.wantStatus {
				t.Fatalf("CmdPermission(%+v) status = %d, want %d (err %v)", tc.payload, got, tc.wantStatus, err)
			}
			if claimed := len(deps.takes) > 0; claimed != tc.wantClaim {
				t.Errorf("claimed = %v, want %v (a refused note must leave the request answerable)", claimed, tc.wantClaim)
			}
			if tc.wantStatus != http.StatusOK {
				if len(bridge.results) != 0 {
					t.Errorf("answered kiro-cli %d times on a refused note, want 0", len(bridge.results))
				}
				return
			}
			if len(bridge.results) != 1 {
				t.Fatalf("answered kiro-cli %d times, want 1", len(bridge.results))
			}
			outcome, ok := bridge.results[0].(*marotte.PermissionOutcome)
			if !ok {
				t.Fatalf("answered with %T, want *marotte.PermissionOutcome", bridge.results[0])
			}
			var reason string
			if outcome.Meta != nil {
				reason = outcome.Meta.Kiro.RejectionReason
			}
			if reason != tc.wantReason {
				t.Errorf("rejectionReason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}
