package trigger

// The msg of every line the poller logs. No line carries forge text (a title, body, branch or author); the
// strings are the rule's own validated fields and ids.
const (
	logMsgBaselineTaken = "trigger baseline taken"
	logMsgEventMatched  = "trigger event matched"
	logMsgQueueFull     = "trigger queue full"
	logMsgCatchUpGap    = "trigger catch-up gap"
	logMsgPoll          = "trigger poll"
	logMsgLabelsCut     = "trigger labels cut"
	logMsgNotWritten    = "launch rules not written"
)
