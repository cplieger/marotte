package marotte

import "encoding/json"

// ContentCollectionParams builds the session/set_config_option params that set
// content collection, one spelling for the session door and the live push.
func ContentCollectionParams(sessionID string, enabled bool) map[string]any {
	value := ConfigValueContentCollectionDisabled
	if enabled {
		value = ConfigValueContentCollectionEnabled
	}
	return map[string]any{KeySessionID: sessionID, "configId": ConfigOptionContentCollection, "value": value}
}

// ContentCollectionReported reads the contentCollection option a set_config_option
// reply carries. ok is false when the reply names none.
func ContentCollectionReported(result json.RawMessage) (enabled, ok bool) {
	var out struct {
		ConfigOptions []struct {
			ID           string `json:"id"`
			CurrentValue string `json:"currentValue"`
		} `json:"configOptions"`
	}
	if json.Unmarshal(result, &out) != nil {
		return false, false
	}
	for _, o := range out.ConfigOptions {
		if o.ID == ConfigOptionContentCollection {
			return o.CurrentValue == ConfigValueContentCollectionEnabled, true
		}
	}
	return false, false
}
