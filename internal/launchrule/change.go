package launchrule

// Change is what a landed definitions write did to an event rule, as the attempt table needs to know it.
//
//deadset:ignore DS1101 -- TriggerConfirmer.RuleChanged's parameter; its first caller lands with the trigger runner
type Change string
