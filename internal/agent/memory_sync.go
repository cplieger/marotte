package agent

// The Memories tab's Cloud sync section: KAS's sync state and the memorySync choice, read and written as kiro-cli's
// own terminal UI does. KAS owns both, so marotte keeps no copy and refetches on _kiro/memory/syncStatus.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
)

const memorySyncKRN = "krn:::setting/memorySync"

// The layers a client may write memorySync at: the account's synced settings, and this host's, which KAS writes
// with the account's on as well when it is on.
const (
	memorySyncAccountLayer = "user-synced"
	memorySyncDeviceLayer  = "user-local"
)

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
	State      string `json:"state"`
	Reason     string `json:"reason,omitempty"`
	LastSyncAt string `json:"last_sync_at,omitempty"`
	Pending    *int   `json:"pending,omitempty"`
}

// memorySyncLock is the layer whose lock fixes the choice, with its display name.
type memorySyncLock struct {
	Layer string `json:"layer"`
	Name  string `json:"name"`
}

// memorySyncChoice is the composed memorySync setting: Enabled is its value, Account and Device whether the
// account's and this host's layers state it on.
type memorySyncChoice struct {
	Lock    *memorySyncLock `json:"lock,omitempty"`
	Enabled bool            `json:"enabled"`
	Account bool            `json:"account"`
	Device  bool            `json:"device"`
}

type memorySyncResponse struct {
	Status memorySyncStatus `json:"status"`
	Choice memorySyncChoice `json:"choice"`
}

type memorySyncUpdateReq struct {
	Scope string `json:"scope"`
	On    bool   `json:"on"`
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
		if k.Stated == nil || !settingOn(k.Stated.Value) {
			continue
		}
		switch k.Layer {
		case memorySyncAccountLayer:
			c.Account = true
		case memorySyncDeviceLayer:
			c.Device = true
		}
	}
	if s.Compose == "locked" && len(s.Assertions) > 0 {
		layer := s.Assertions[len(s.Assertions)-1].Layer
		name := names[layer]
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
	var view struct {
		Layers []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"layers"`
		Settings []kasSetting `json:"settings"`
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		return memorySyncChoice{}, err
	}
	names := make(map[string]string, len(view.Layers))
	for _, l := range view.Layers {
		names[l.ID] = l.Name
	}
	for i := range view.Settings {
		if view.Settings[i].KRN == memorySyncKRN {
			return memorySyncChoiceOf(&view.Settings[i], names), nil
		}
	}
	return memorySyncChoice{}, nil
}

// handleMemorySync serves /api/memory/sync: GET answers the state and the choice, PUT {scope: account|device, on}
// writes the choice at that scope's layer and answers it.
func (st *Settings) handleMemorySync(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		st.getMemorySync(w, r)
	case http.MethodPut:
		var body memorySyncUpdateReq
		if !httpreply.DecodeJSON(w, r, &body) {
			return
		}
		layer, ok := map[string]string{"account": memorySyncAccountLayer, "device": memorySyncDeviceLayer}[body.Scope]
		if !ok {
			httpreply.BadRequest(w, "scope must be account or device")
			return
		}
		value := "off"
		if body.On {
			value = "on"
		}
		st.lifecycle.answerEngineMutation(w, r, engineMutation{
			verb: "memory sync", changed: st.broadcastMemorySyncChanged,
			call: func(ctx context.Context) engineReply { return st.setMemorySync(ctx, value, layer) },
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

func (st *Settings) setMemorySync(ctx context.Context, value, layer string) engineReply {
	cctx, cancel := st.engine.stallAware(ctx, memoryCallTimeout)
	defer cancel()
	raw, err := st.engine.configurationSetRaw(cctx, memorySyncKRN, value, layer)
	if err != nil {
		return engineReply{err: err, write: func(w http.ResponseWriter) { writeMemorySyncErr(w, err) }}
	}
	var res struct {
		Setting kasSetting `json:"setting"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return engineReply{err: err, write: func(w http.ResponseWriter) { writeMemoryErr(w, err) }, applied: true}
	}
	choice := memorySyncChoiceOf(&res.Setting, nil)
	return engineReply{write: func(w http.ResponseWriter) { httpserver.WriteJSON(w, choice) }, applied: true}
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
				"Memory sync is locked by the "+data.Layer+" layer.", "locked"))
			return
		case "unavailable":
			httpserver.WriteJSONStatus(w, http.StatusConflict, httpreply.ErrorJSONWithCode(
				"kiro-cli cannot change memory sync now. Sign in to Kiro, then try again.", "unavailable"))
			return
		case "notGranted":
			httpreply.BadRequest(w, rpc.Message)
			return
		}
	}
	writeMemoryErr(w, err)
}

func (st *Settings) broadcastMemorySyncChanged() {
	st.broadcast(context.Background(), marotte.NewEvent(marotte.EventMemorySyncChanged, "", marotte.MemorySyncChangedPayload{}))
}
