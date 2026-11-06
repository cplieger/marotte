package trigger

import "time"

// queueEntry is one held event: matched, not yet confirmed launched.
type queueEntry struct {
	ObservedAt time.Time         `json:"observed_at"`
	Inputs     map[string]string `json:"inputs"`
	Key        string            `json:"key"`
	// CoalesceKey groups the entries a newest hold keeps one of; it can hold forge text and is never logged.
	CoalesceKey string `json:"coalesce_key"`
	// Live names the entry's live attempt record by its run, empty while it has none.
	Live string `json:"live,omitempty"`
}

// launchedEntry is one confirmed event and the run that confirmed it.
type launchedEntry struct {
	Inputs      map[string]string `json:"inputs"`
	Key         string            `json:"key"`
	CoalesceKey string            `json:"coalesce_key"`
	WorkflowID  string            `json:"workflow_id"`
}

// supersededEntry is a key a newest hold replaced, By naming the key that replaced it and, once that one is
// confirmed, its run.
type supersededEntry struct {
	Key        string `json:"key"`
	By         string `json:"by"`
	WorkflowID string `json:"workflow_id,omitempty"`
}
