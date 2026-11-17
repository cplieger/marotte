package agent

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/go/textsafe"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
)

// configurationAccountLayer is the account's synced settings, the one layer kiro-cli 3.4.0 grants a client
// cloudMemory and modelFallback at: _kiro/configuration/set refuses either at user-local as notGranted.
const configurationAccountLayer = "user-synced"

// configurationNotStoredCode is KAS's -32000, which a configuration write answers when it could not store the value
// now (ModelFallbackWriteError, ModelFallbackIdentityChangedError).
const configurationNotStoredCode = -32000

// maxConfigurationTextBytes bounds each engine string (a layer id or name, a refusal message) relayed to the client.
const maxConfigurationTextBytes = 256

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

func configurationText(s string) string {
	return textsafe.SanitizeSingleLineBounded(s, maxConfigurationTextBytes)
}

// writeConfigurationSetErr answers a failed _kiro/configuration/set of subject, a setting's lower-case name.
// ConfigurationSetRefusedError carries its reason in data, whose text rpcerr.Text would show as JSON: locked and
// unavailable are 409s, notGranted a 400 with KAS's message. A value KAS could not store now is a 409 with its text,
// any other refusal a 400 with its text, and a failure KAS did not answer a 502.
func writeConfigurationSetErr(w http.ResponseWriter, err error, subject string) {
	rpc, ok := errors.AsType[*marotte.RPCError](err)
	if !ok || marotte.ReplyUnread(err) {
		slog.Warn("a configuration write failed", "setting", subject, "error", err)
		httpserver.WriteJSONStatus(w, http.StatusBadGateway, httpreply.ErrorJSON(subject+" request failed"))
		return
	}
	var data struct {
		Reason string `json:"reason"`
		Layer  string `json:"layer"`
	}
	_ = json.Unmarshal(rpc.Data, &data)
	switch {
	case data.Reason == "locked":
		httpserver.WriteJSONStatus(w, http.StatusConflict, httpreply.ErrorJSONWithCode(
			sentenceCase(subject)+" is locked by the "+configurationText(data.Layer)+" layer.", "locked",
		))
	case data.Reason == "unavailable":
		httpserver.WriteJSONStatus(w, http.StatusConflict, httpreply.ErrorJSONWithCode(
			"kiro-cli cannot change "+subject+" now. Sign in to Kiro, then try again.", "unavailable",
		))
	case data.Reason == "notGranted":
		httpreply.BadRequest(w, configurationText(rpc.Message))
	case rpc.Code == configurationNotStoredCode:
		httpserver.WriteJSONStatus(w, http.StatusConflict, httpreply.ErrorJSON(configurationText(rpcerr.Text(rpc))))
	default:
		httpreply.BadRequest(w, configurationText(rpcerr.Text(rpc)))
	}
}

func sentenceCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
