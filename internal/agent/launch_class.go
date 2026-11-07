package agent

import (
	"errors"
	"fmt"

	"github.com/cplieger/marotte/internal/schedule"
	"github.com/cplieger/marotte/internal/trigger"
)

// classLaunch marks an error LaunchTriggered returns before workflow/new with its row of the trigger error table,
// which internal/trigger cannot read from this package's unexported errors. An error the table does not list is
// returned unmarked; the trigger package counts it on the rule's 24-hour clock.
func classLaunch(err error) error {
	switch {
	case errors.Is(err, errUnknownRecipe), errors.Is(err, errDefaultsRefused):
		return trigger.Classed(trigger.ErrLaunchBlocked, err)
	case errors.Is(err, errRunsNeedSignIn), errors.Is(err, errRunsProviderDown):
		return trigger.Classed(trigger.ErrLaunchRetryable, fmt.Errorf("%w: %w", schedule.ErrNoRunCanStart, err))
	case errors.Is(err, errEngineSlow):
		return trigger.Classed(trigger.ErrLaunchRetryable, fmt.Errorf("%w: %w", schedule.ErrEngineBusy, err))
	case errors.Is(err, errRecipeListSlow), errors.Is(err, errRunListUnavailable), errors.Is(err, errRuntimeDraining),
		errors.Is(err, errRunHostStart), errors.Is(err, errHostSessionTaken), errors.Is(err, errDefaultsUncheckable):
		return trigger.Classed(trigger.ErrLaunchRetryable, err)
	}
	return err
}
