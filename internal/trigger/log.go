package trigger

import (
	"context"
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/rpcerr"
)

const (
	logMsgBaselineTaken   = "trigger baseline taken"
	logMsgEventMatched    = "trigger event matched"
	logMsgQueueFull       = "trigger queue full"
	logMsgCatchUpGap      = "trigger catch-up gap"
	logMsgPoll            = "trigger poll"
	logMsgPollReadFailed  = "trigger poll read failed"
	logMsgPollFailing     = "trigger poll failing"
	logMsgPollRecovered   = "trigger poll recovered"
	logMsgLabelsCut       = "trigger labels cut"
	logMsgNotWritten      = "launch rules not written"
	logMsgRuleSkipped     = "trigger poll skipped a rule"
	logMsgStateBad        = "trigger poll skipped a rule: its state does not decode"
	logMsgStateUnreadable = "launch rule blocked: its state file is unreadable"
	logMsgImpossible      = "attempt table: impossible transition"
	logMsgRunStarted      = "triggered run started"
	logMsgRunNotStarted   = "triggered run did not start"
	logMsgLaunchAbandoned = "triggered launch abandoned"
	logMsgBackedOff       = "trigger poll backed off"
	logMsgResumed         = "trigger poll resumed"
)

// Every trigger line is written by a function below, whose parameters are a rule's own validated fields, ids,
// counts, instants and errors: none takes a forge's item, so no line can carry an issue's or pull request's title,
// body, branch or author.

type ruleRef struct{ id, recipe, repo, kind string }

func refOf(r *launchrule.Rule) ruleRef {
	ref := ruleRef{id: r.ID, recipe: r.Name}
	if ev := r.When.Event; ev != nil {
		ref.repo, ref.kind = ev.Repo, string(ev.Kind)
	}
	return ref
}

func (r ruleRef) attrs(more ...any) []any {
	return append([]any{"rule_id", r.id, "recipe", r.recipe, "repo", r.repo, "kind", r.kind}, more...)
}

// errText is an error as a line carries it: sanitized to one line and capped.
func errText(err error) string { return launchrule.ReasonText(rpcerr.Text(err)) }

func logBaselineTaken(r ruleRef, items int) {
	slog.Info(logMsgBaselineTaken, r.attrs("items", items)...)
}

func logEventMatched(r ruleRef, key string, held int) {
	slog.Info(logMsgEventMatched, r.attrs("key", key, "held", held)...)
}

func logQueueFull(r ruleRef, held, capacity int) {
	slog.Warn(logMsgQueueFull, r.attrs("held", held, "cap", capacity)...)
}

func logLabelsCut(r ruleRef, number, omitted int) {
	slog.Debug(logMsgLabelsCut, r.attrs("item", number, "omitted_at_least", omitted)...)
}

func logCatchUpGap(r ruleRef, g *launchrule.Gap) {
	attrs := r.attrs("cause", g.Cause, "from", g.From, "to", g.To)
	switch {
	case g.RunID != 0:
		attrs = append(attrs, "run_id", g.RunID)
	case g.Cause == launchrule.GapResultWindow:
		attrs = append(attrs, "omitted_at_least", g.OmittedAtLeast)
	}
	slog.Warn(logMsgCatchUpGap, attrs...)
}

func logPoll(f Feed, reads int, since time.Time, took time.Duration) {
	slog.Debug(logMsgPoll, "connection", f.Connection, "repo", f.Selector, "reads", reads, "since", since, "took", took)
}

// logPollRead logs a failed read of f's list, the n-th in a row: at DEBUG, except the pollFailingAfter-th, at WARN.
func logPollRead(f Feed, list string, n int, kind string, err error) {
	msg, level := logMsgPollReadFailed, slog.LevelDebug
	if n == pollFailingAfter {
		msg, level = logMsgPollFailing, slog.LevelWarn
	}
	slog.Log(context.Background(), level, msg, "connection", f.Connection, "repo", f.Selector, "list", list,
		"failures", n, "error_kind", kind, "error", errText(err))
}

func logPollRecovered(f Feed, list string, failures int) {
	slog.Info(logMsgPollRecovered, "connection", f.Connection, "repo", f.Selector, "list", list, "failures", failures)
}

func logBackedOff(f Feed, bo *backoff) {
	slog.Warn(logMsgBackedOff, "connection", f.Connection, "repo", f.Selector, "pool", bo.pool,
		"reason", bo.reason, "remaining", bo.remaining, "resumes_at", bo.until)
}

func logResumed(f Feed, pool string) {
	slog.Info(logMsgResumed, "connection", f.Connection, "repo", f.Selector, "pool", pool)
}

func logRuleSkipped(ruleID string, err error) {
	slog.Debug(logMsgRuleSkipped, "rule_id", ruleID, "error", errText(err))
}

func logStateBad(ruleID string, err error) {
	slog.Error(logMsgStateBad, "rule_id", ruleID, "error", errText(err))
}

func logStateUnreadable(ruleID string, err error) {
	slog.Error(logMsgStateUnreadable, "rule_id", ruleID, "error", errText(err))
}

func logImpossible(ruleID, state, event, workflowID string) {
	slog.Error(logMsgImpossible, "rule_id", ruleID, "state", state, "event", event, "workflow_id", workflowID)
}

func logUnknownChange(ruleID string, change launchrule.Change) {
	slog.Error(logMsgImpossible, "rule_id", ruleID, "event", string(change), "error", errUnknownChange.Error())
}

func logRunStarted(ruleID, workflowID, key string) {
	slog.Info(logMsgRunStarted, "rule_id", ruleID, "workflow_id", workflowID, "key", key)
}

func logLaunchAbandoned(ruleID, workflowID, key, cause string) {
	slog.Info(logMsgLaunchAbandoned, "rule_id", ruleID, "workflow_id", workflowID, "key", key, "cause", cause)
}

// The classes of a triggered run did not start line.
const (
	notStartedRetryable = "retryable"
	notStartedBlocked   = "blocked"
)

// logRunNotStarted logs why a launch of the rule started no run; workflowID is empty for a launch that created none.
// sentence is a row text or a reasonText, already sanitized.
func logRunNotStarted(ruleID, workflowID, class, sentence string) {
	slog.Warn(logMsgRunNotStarted, "rule_id", ruleID, "workflow_id", workflowID, "class", class, "error", sentence)
}

// The operations a launch rules not written line names.
const (
	opWrite  = "write"
	opRemove = "remove"
)

func logNotWritten(ruleID, op string, err error) {
	slog.Error(logMsgNotWritten, "file", stateFile(ruleID), "op", op, "rule_id", ruleID, "error", errText(err))
}
