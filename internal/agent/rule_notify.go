package agent

import (
	"context"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/notice"
)

// NotifyNotStarted tells the user that a slot or event of rule started no run, on the page and as a Web Push under
// the rule. reason is the text the rule's row records. The caller decides which failure is worth it: the first of a
// streak, so a rule that keeps failing notifies once. The nil guard covers a bare &Runs{}.
func (rs *Runs) NotifyNotStarted(ctx context.Context, rule *launchrule.Rule, reason string) {
	if rs.coord == nil {
		return
	}
	n := notice.RunNotStarted(notice.RuleTarget(rule.ID, rule.Name), reason)
	rs.coord.Notify(ctx, "", &n)
}
