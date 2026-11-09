package settings

import (
	"context"
	"encoding/json"
	"fmt"
)

// The fleet caps' defaults and ceiling. A cap of 0 would stop every event rule, which pausing the rules does.
const (
	defaultTriggerMaxLiveRuns        = 3
	defaultTriggerMaxLaunchesPerHour = 6
	maxTriggerCap                    = 100
)

// validTriggerCap reports whether n is a fleet cap the setting accepts: 1 to 100.
func validTriggerCap(n int) bool {
	return n >= 1 && n <= maxTriggerCap
}

// TriggerCaps answers the fleet caps on triggered runs: the most live at once and the most started in any rolling
// hour. A cap config.json does not hold, cannot be read for, or holds out of range reads as its default.
func TriggerCaps(ctx context.Context, configDir string) (live, perHour int) {
	return triggerCap(ctx, configDir, keyTriggerMaxLiveRuns, defaultTriggerMaxLiveRuns),
		triggerCap(ctx, configDir, keyTriggerMaxLaunchesPerHour, defaultTriggerMaxLaunchesPerHour)
}

func triggerCap(ctx context.Context, configDir, key string, def int) int {
	n, _ := fieldOr(ctx, configDir, key, def)
	if !validTriggerCap(n) {
		return def
	}
	return n
}

// validateTriggerCapsPatch refuses a patch whose cap keys hold anything but a whole number from 1 to 100.
func validateTriggerCapsPatch(patch map[string]json.RawMessage) error {
	for _, key := range []string{keyTriggerMaxLiveRuns, keyTriggerMaxLaunchesPerHour} {
		raw, ok := patch[key]
		if !ok {
			continue
		}
		var n int
		if decodeChecked(&n, raw, key, validTriggerCap) != nil {
			return fmt.Errorf("%s must be a whole number from 1 to %d", key, maxTriggerCap)
		}
	}
	return nil
}
