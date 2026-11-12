package agent

// The Memories tab's Cloud sync section: KAS's sync state and the memorySync choice, read as kiro-cli's own terminal
// UI reads them and written at the one layer KAS grants. KAS owns both, so marotte keeps no copy and refetches on
// _kiro/memory/syncStatus.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/go/textsafe"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
)

const memorySyncKRN = "krn:::setting/memorySync"

// memorySyncAccountLayer is the one layer kiro-cli 3.2.1 lets a client write memorySync at, the account's synced
// settings: _kiro/configuration/set refuses user-local as notGranted.
const memorySyncAccountLayer = "user-synced"

// memorySyncUnknown stands for a state or reason this build has no wording for.
const memorySyncUnknown = "unknown"

// maxMemorySyncTextBytes bounds each engine string (a layer id or name, a refusal message) relayed to the client.
const maxMemorySyncTextBytes = 256

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

type memorySyncUpdateReq struct {
	On bool `json:"on"`
}

// kasSetting is one composed setting row of a configuration view.
type kasSetting struct {
	KRN        string          `json:"krn"`
	Value      json.RawMessage `json:"value"`
	Compose    string          `json:"compose"`
	Assertions []struct {
		Layer string `json:"layer"`
	} `json:"assertions"`
	Contributions []struct {
		Stated *struct {
			Value json.RawMessage `json:"value"`
		} `json:"stated"`
		Layer string `json:"layer"`
	} `json:"contributions"`
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
		if k.Layer == memorySyncAccountLayer && k.Stated != nil && settingOn(k.Stated.Value) {
			c.Account = true
		}
	}
	if s.Compose == "locked" && len(s.Assertions) > 0 {
		raw := s.Assertions[len(s.Assertions)-1].Layer
		layer, name := memorySyncText(raw), memorySyncText(names[raw])
		if name == "" {
			name = layer
		}
		c.Lock = &memorySyncLock{Layer: layer, Name: name}
	}
	return c
}

func memorySyncText(s string) string {
	return textsafe.SanitizeSingleLineBounded(s, maxMemorySyncTextBytes)
}

func settingOn(raw json.RawMessage) bool {
	var v string
	return json.Unmarshal(raw, &v) == nil && v == "on"
}

// kasView is a getState answer: its layers' display names and its composed settings.
type kasView struct {
	Layers []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"layers"`
	Settings []kasSetting `json:"settings"`
}

func (v *kasView) layerNames() map[string]string {
	names := make(map[string]string, len(v.Layers))
	for _, l := range v.Layers {
		names[l.ID] = l.Name
	}
	return names
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
	raw, err := st.engine.configurationSetRaw(cctx, memorySyncKRN, value, memorySyncAccountLayer)
	if err != nil {
		return engineReply{err: err, write: func(w http.ResponseWriter) { writeMemorySyncErr(w, err) }}
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

// writeMemorySyncErr answers KAS's refusal of the write by its reason (ConfigurationSetRefusedError's data, whose
// text rpcerr.Text would show as JSON): locked and unavailable are 409s, notGranted a 400 with KAS's message, and
// anything else is writeMemoryErr's.
func writeMemorySyncErr(w http.ResponseWriter, err error) {
	if rpc, ok := errors.AsType[*marotte.RPCError](err); ok {
		var data struct {
			Reason string `json:"reason"`
			Layer  string `json:"layer"`
		}
		_ = json.Unmarshal(rpc.Data, &data)
		switch data.Reason {
		case "locked":
			httpserver.WriteJSONStatus(w, http.StatusConflict, httpreply.ErrorJSONWithCode(
				"Memory sync is locked by the "+memorySyncText(data.Layer)+" layer.", "locked",
			))
			return
		case "unavailable":
			httpserver.WriteJSONStatus(w, http.StatusConflict, httpreply.ErrorJSONWithCode(
				"kiro-cli cannot change memory sync now. Sign in to Kiro, then try again.", "unavailable",
			))
			return
		case "notGranted":
			httpreply.BadRequest(w, memorySyncText(rpc.Message))
			return
		}
	}
	writeMemoryErr(w, err)
}

func (st *Settings) broadcastMemorySyncChanged() {
	st.broadcast(context.Background(), marotte.NewEvent(marotte.EventMemorySyncChanged, "", marotte.MemorySyncChangedPayload{}))
}
