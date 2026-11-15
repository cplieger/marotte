package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

const modelFallbackKRN = "krn:::setting/modelFallback/"

// ModelFallbackUpdate is PUT /api/model-fallback's body: the model whose fallback changes and its new target, ""
// for Kiro's own.
type ModelFallbackUpdate struct {
	Model  string `json:"model"`
	Target string `json:"target"`
}

// handleModelFallback serves PUT /api/model-fallback {model, target}: it writes the model's chain at the account's
// layer and answers the row KAS composes from it. KAS validates the pair (both served, not the model itself, no
// consent needed); an empty chain clears the account's choice, so there is no way to turn one model's fallback off.
func (st *Settings) handleModelFallback(w http.ResponseWriter, r *http.Request) {
	var body ModelFallbackUpdate
	if !httpreply.DecodeJSON(w, r, &body) {
		return
	}
	model, target := strings.TrimSpace(body.Model), strings.TrimSpace(body.Target)
	if model == "" {
		httpreply.BadRequest(w, "model required")
		return
	}
	chain := []string{}
	if target != "" {
		chain = []string{target}
	}
	st.lifecycle.answerEngineMutation(w, r, engineMutation{
		verb: "model fallback", changed: st.announceSettings,
		call: func(ctx context.Context) engineReply { return st.setModelFallback(ctx, model, chain) },
	})
}

func (st *Settings) setModelFallback(ctx context.Context, model string, chain []string) engineReply {
	cctx, cancel := st.engine.stallAware(ctx, memoryCallTimeout)
	defer cancel()
	raw, err := st.engine.configurationSetRaw(cctx, modelFallbackKRN+model, chain, configurationAccountLayer)
	if err != nil {
		return engineReply{err: err, write: func(w http.ResponseWriter) { writeConfigurationSetErr(w, err, "model fallback") }}
	}
	var res struct {
		Setting json.RawMessage `json:"setting"`
	}
	_ = json.Unmarshal(raw, &res)
	row, ok := translate.DecodeModelFallbackSetting(res.Setting)
	if !ok {
		err := marotte.ErrReplyUndecoded
		return engineReply{err: err, write: func(w http.ResponseWriter) { writeConfigurationSetErr(w, err, "model fallback") }, applied: true}
	}
	st.storeModelFallback(row)
	return engineReply{write: func(w http.ResponseWriter) { httpserver.WriteJSON(w, row) }, applied: true}
}

// storeModelFallback puts row in the cached view, so GET /api/settings answers it before KAS pushes the next view.
func (st *Settings) storeModelFallback(row marotte.ModelFallback) {
	st.kiroDefaults.mu.Lock()
	defer st.kiroDefaults.mu.Unlock()
	rows := slices.DeleteFunc(st.kiroDefaults.fallbacks, func(f marotte.ModelFallback) bool { return f.Model == row.Model })
	i, _ := slices.BinarySearchFunc(rows, row.Model, func(f marotte.ModelFallback, m string) int { return cmp.Compare(f.Model, m) })
	st.kiroDefaults.fallbacks = slices.Insert(rows, i, row)
}
