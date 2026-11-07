package trigger

// The msg of every line the poller and the confirmer log. No line carries forge text (a title, body, branch or author); the
// strings are the rule's own validated fields and ids.
const (
	logMsgBaselineTaken   = "trigger baseline taken"
	logMsgEventMatched    = "trigger event matched"
	logMsgQueueFull       = "trigger queue full"
	logMsgCatchUpGap      = "trigger catch-up gap"
	logMsgPoll            = "trigger poll"
	logMsgLabelsCut       = "trigger labels cut"
	logMsgNotWritten      = "launch rules not written"
	logMsgRuleSkipped     = "trigger poll skipped a rule"
	logMsgStateBad        = "trigger poll skipped a rule: its state does not decode"
	logMsgStateUnreadable = "launch rule blocked: its state file is unreadable"
	logMsgImpossible      = "attempt table: impossible transition"
	logMsgRunStarted      = "triggered run started"
	logMsgRunNotStarted   = "triggered run did not start"
	logMsgLaunchAbandoned = "triggered launch abandoned"
)
