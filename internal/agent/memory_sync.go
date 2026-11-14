package agent

// The Memories tab's Cloud sync section: KAS's sync state and the memorySync choice, read as kiro-cli's own terminal
// UI reads them and written at the one layer KAS grants. KAS owns both, so marotte keeps no copy and refetches on
// _kiro/memory/syncStatus.

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
)

const memorySyncKRN = "krn:::setting/memorySync"

// memorySyncUnknown stands for a state or reason this build has no wording for.
const memorySyncUnknown = "unknown"

// memorySyncReasons is each sync state KAS reports with the reasons it may carry (kiro-cli 3.1.0's syncStatus
// schema); a state with no reason maps to nil.
var memorySyncReasons = map[string][]string{
	"notStarted":  nil,
	"resolving":   nil,
	"off":         nil,
	"unavailable": {"notSignedIn", "teamKey", "notEntitled", "identityUnknown"},
	"suspended":   {"forbidden", "backendSignal"},
	"syncing":     nil,
	"synced":      nil,
	"failed":      {"sync", "storage"},
}

type memorySyncStatus struct {
	Pending    *int   `json:"pending,omitempty"`
	State      string `json:"state"`
	Reason     string `json:"reason,omitempty"`
	LastSyncAt string `json:"last_sync_at,omitempty"`
}

// memorySyncLock is the layer whose lock fixes the choice, with its display name.
type memorySyncLock struct {
	Layer string `json:"layer"`
	Name  string `json:"name"`
}

// memorySyncChoice is the composed memorySync setting: Enabled is its value, Account whether the account's layer
// states it on.
type memorySyncChoice struct {
	Lock    *memorySyncLock `json:"lock,omitempty"`
	Enabled bool            `json:"enabled"`
	Account bool            `json:"account"`
}

type memorySyncResponse struct {
	Status memorySyncStatus `json:"status"`
	Choice memorySyncChoice `json:"choice"`
}

// Scope is decoded only to refuse it: a page that still names a layer must not have its write land at the account's.
type memorySyncUpdateReq struct {
	Scope string `json:"scope"`
	On    bool   `json:"on"`
}

// decodeMemorySyncStatus reads getSyncStatus's answer; a state or reason outside the schema reads unknown.
func decodeMemorySyncStatus(raw json.RawMessage) memorySyncStatus {
	var s struct {
		Pending    *int   `json:"pending"`
		State      string `json:"state"`
		Reason     string `json:"reason"`
		LastSyncAt string `json:"lastSyncAt"`
	}
	if json.Unmarshal(raw, &s) != nil {
		return memorySyncStatus{State: memorySyncUnknown}
	}
	reasons, known := memorySyncReasons[s.State]
	if !known || (len(reasons) > 0 && !slices.Contains(reasons, s.Reason)) {
		return memorySyncStatus{State: memorySyncUnknown}
	}
	out := memorySyncStatus{State: s.State, LastSyncAt: s.LastSyncAt, Pending: s.Pending}
	if len(reasons) > 0 {
		out.Reason = s.Reason
	}
	return out
}

// memorySyncChoiceOf reduces the composed row as kiro-cli's terminal UI does: a lock is named by the last
// assertion's layer, looked up in names.
func memorySyncChoiceOf(s *kasSetting, names map[string]string) memorySyncChoice {
	c := memorySyncChoice{Enabled: settingOn(s.Value)}
	for _, k := range s.Contributions {
		if k.Layer == configurationAccountLayer && k.Stated != nil && settingOn(k.Stated.Value) {
			c.Account = true
		}
	}
	if s.Compose == "locked" && len(s.Assertions) > 0 {
		raw := s.Assertions[len(s.Assertions)-1].Layer
		layer, name := configurationText(raw), configurationText(names[raw])
		if name == "" {
			name = layer
		}
		c.Lock = &memorySyncLock{Layer: layer, Name: name}
	}
	return c
}

func settingOn(raw json.RawMessage) bool {
	var v string
	return json.Unmarshal(raw, &v) == nil && v == "on"
}

// decodeMemorySyncChoice reads the memorySync row out of a getState answer; a view stating none reads off.
func decodeMemorySyncChoice(raw json.RawMessage) (memorySyncChoice, error) {
	var view kasView
	if err := json.Unmarshal(raw, &view); err != nil {
		return memorySyncChoice{}, err
	}
	for i := range view.Settings {
		if view.Settings[i].KRN == memorySyncKRN {
			return memorySyncChoiceOf(&view.Settings[i], view.layerNames()), nil
		}
	}
	return memorySyncChoice{}, nil
}

// handleMemorySync serves /api/memory/sync: GET answers the state and the choice, PUT {on} writes the choice at the
// account's layer and answers it.
func (st *Settings) handleMemorySync(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		st.getMemorySync(w, r)
	case http.MethodPut:
		var body memorySyncUpdateReq
		if !httpreply.DecodeJSON(w, r, &body) {
			return
		}
		if body.Scope != "" {
			httpreply.BadRequest(w, "memory sync takes {on} only")
			return
		}
		value := "off"
		if body.On {
			value = "on"
		}
		st.lifecycle.answerEngineMutation(w, r, engineMutation{
			verb: "memory sync", changed: st.broadcastMemorySyncChanged,
			call: func(ctx context.Context) engineReply { return st.setMemorySync(ctx, value) },
		})
	default:
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodPut)
	}
}

func (st *Settings) getMemorySync(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := st.engine.stallAware(r.Context(), memoryCallTimeout)
	defer cancel()
	rawStatus, err := st.engine.memoryRaw(ctx, methodKiroMemoryGetSyncStatus, map[string]any{})
	if err != nil {
		writeMemoryErr(w, err)
		return
	}
	rawView, err := st.engine.connectionConfigurationRaw(ctx)
	if err != nil {
		writeMemoryErr(w, err)
		return
	}
	choice, err := decodeMemorySyncChoice(rawView)
	if err != nil {
		writeMemoryErr(w, err)
		return
	}
	httpserver.WriteJSON(w, memorySyncResponse{Status: decodeMemorySyncStatus(rawStatus), Choice: choice})
}

func (st *Settings) setMemorySync(ctx context.Context, value string) engineReply {
	cctx, cancel := st.engine.stallAware(ctx, memoryCallTimeout)
	defer cancel()
	raw, err := st.engine.configurationSetRaw(cctx, memorySyncKRN, value, configurationAccountLayer)
	if err != nil {
		return engineReply{err: err, write: func(w http.ResponseWriter) { writeConfigurationSetErr(w, err, "memory sync") }}
	}
	var res struct {
		Setting kasSetting `json:"setting"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return engineReply{err: err, write: func(w http.ResponseWriter) { writeMemoryErr(w, err) }, applied: true}
	}
	choice := memorySyncChoiceOf(&res.Setting, st.memorySyncLayerNames(cctx))
	return engineReply{write: func(w http.ResponseWriter) { httpserver.WriteJSON(w, choice) }, applied: true}
}

// memorySyncLayerNames reads the layers' display names from the connection's view, as the GET does, so a PUT names
// a lock the same way; a failed read leaves the lock named by its layer id.
func (st *Settings) memorySyncLayerNames(ctx context.Context) map[string]string {
	raw, err := st.engine.connectionConfigurationRaw(ctx)
	if err != nil {
		return nil
	}
	var view kasView
	if json.Unmarshal(raw, &view) != nil {
		return nil
	}
	return view.layerNames()
}

func (st *Settings) broadcastMemorySyncChanged() {
	st.broadcast(context.Background(), marotte.NewEvent(marotte.EventMemorySyncChanged, "", marotte.MemorySyncChangedPayload{}))
}
