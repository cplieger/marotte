package agent

// The Memory setting: kiro-cli's device Memory choice, the memoryMode and memoryReflection rows KAS keeps for the
// signed-in account on this machine. A chat session sends no memory key, so KAS's session/new takes this choice;
// marotte keeps no copy beyond the pushed connection view.

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

const (
	memoryModeKRN       = "krn:::setting/memoryMode"
	memoryReflectionKRN = "krn:::setting/memoryReflection"
)

type memoryModeUpdateReq struct {
	Mode string `json:"mode"`
}

// handleMemoryMode serves PUT /api/memory/mode {mode}: it writes the choice at the device layer and answers the
// reduced choice.
func (st *Settings) handleMemoryMode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		httpreply.MethodNotAllowed(w, http.MethodPut)
		return
	}
	var body memoryModeUpdateReq
	if !httpreply.DecodeJSON(w, r, &body) {
		return
	}
	modeValue, reflectionValue, ok := translate.DeviceMemoryRows(body.Mode)
	if !ok {
		httpreply.BadRequest(w, "mode must be off, read_only, read_write or learn")
		return
	}
	st.lifecycle.answerEngineMutation(w, r, engineMutation{
		verb: "memory", changed: st.announceSettings,
		call: func(ctx context.Context) engineReply { return st.setMemoryMode(ctx, modeValue, reflectionValue) },
	})
}

// setMemoryMode writes the mode row before the reflection row, as kiro-cli's terminal UI does, skipping a row whose
// composed value already matches.
func (st *Settings) setMemoryMode(ctx context.Context, modeValue, reflectionValue string) engineReply {
	cctx, cancel := st.engine.stallAware(ctx, memoryCallTimeout)
	defer cancel()
	view := st.deviceMemoryView()
	curMode, curReflection := view.Values()
	applied := false
	for _, row := range []struct{ krn, want, have string }{
		{memoryModeKRN, modeValue, curMode},
		{memoryReflectionKRN, reflectionValue, curReflection},
	} {
		if row.want == row.have {
			continue
		}
		raw, err := st.engine.configurationSetRaw(cctx, row.krn, row.want, configurationDeviceLayer)
		if err != nil {
			return engineReply{err: err, write: func(w http.ResponseWriter) { writeConfigurationSetErr(w, err, "memory") }, applied: applied}
		}
		applied = true
		var res struct {
			Setting json.RawMessage `json:"setting"`
		}
		if json.Unmarshal(raw, &res) != nil || !view.Store(res.Setting) {
			err := marotte.ErrReplyUndecoded
			return engineReply{err: err, write: func(w http.ResponseWriter) { writeConfigurationSetErr(w, err, "memory") }, applied: true}
		}
		st.storeDeviceMemoryRow(res.Setting)
	}
	choice := view.Choice()
	return engineReply{write: func(w http.ResponseWriter) { httpserver.WriteJSON(w, choice) }, applied: applied}
}
