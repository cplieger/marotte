package marotte

import "encoding/json"

// ConfigOptionValue reads option id from a payload carrying a top-level configOptions list: a session/new or
// session/load result, a set_config_option reply, or a config_option_update. offered is false when the list names no
// such option with a string value.
func ConfigOptionValue(raw json.RawMessage, id string) (value string, offered bool) {
	var out struct {
		ConfigOptions []struct {
			ID           string          `json:"id"`
			CurrentValue json.RawMessage `json:"currentValue"`
		} `json:"configOptions"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return "", false
	}
	for _, o := range out.ConfigOptions {
		if o.ID != id {
			continue
		}
		if json.Unmarshal(o.CurrentValue, &value) != nil {
			return "", false
		}
		return value, true
	}
	return "", false
}
