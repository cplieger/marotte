package launchrule

// Change is what a landed definitions write did to an event rule, as the attempt table needs to know it.
type Change string

// The changes a definitions write makes to an event rule. A write that changes only enabled is a pause or a
// resume; one that changes the connection, the repository or the kind is a rebaseline; any other is a filter edit.
const (
	ChangeFilter     Change = "filter"
	ChangeRebaseline Change = "rebaseline"
	ChangeDelete     Change = "delete"
	ChangePause      Change = "pause"
	ChangeResume     Change = "resume"
)
