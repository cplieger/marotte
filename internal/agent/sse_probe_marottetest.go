//go:build marotte_test

package agent

import (
	"context"
	"encoding/json"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/turnlog"
)

// PublishRunLog publishes one closed step turn of run runID through the run log's own publishers: turn_opened,
// entry_delta and entry_sealed for a streamed entry, entry_appended for a born-sealed one, then turn_closed. Nothing
// is filed in the run's log. Test builds only: the e2e-sse suite has no engine to drive a step.
func (rt *Runtime) PublishRunLog(ctx context.Context, runID string) {
	ts := time.Now().UnixMilli()
	const turn = "t-e2e-run-log"
	entry := func(kind marotte.EntryKind, seq uint64, payload string) *marotte.Entry {
		return &marotte.Entry{
			ID: turn + "-" + string(kind), Turn: turn, Kind: kind,
			Payload: json.RawMessage(payload), Seq: seq, Ts: ts,
		}
	}
	translate.PublishAppended(ctx, rt.bus, "", runID, entry(marotte.EntryKindTurnOpen, 0, `{}`))
	translate.PublishSealed(ctx, rt.bus, "", runID, []turnlog.Sealed{
		{Entry: entry(marotte.EntryKindText, 1, `{"text":"streamed"}`), Delta: "ed", N: 2},
		{Entry: entry(marotte.EntryKindThinking, 2, `{"text":"born sealed"}`)},
	})
	translate.PublishAppended(ctx, rt.bus, "", runID, entry(marotte.EntryKindTurnClose, 3, `{}`))
}
