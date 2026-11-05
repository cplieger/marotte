package agent

import "context"

// admitAllLife is a test entry's life: it always admits, on context.Background, as runHostLife does on the runtime's
// lifetime. A test that exercises a close builds its entry through openBridge instead, with a real lifecycle.
type admitAllLife struct{}

func (admitAllLife) beginLease(context.CancelCauseFunc) (context.Context, func(), bool) {
	return context.Background(), func() {}, true
}

// testBridge builds an entry holding s in state, its leases always admitted; an entry past its door is settled.
func testBridge(s engineSession, state bridgeState) *sharedBridge {
	sb := newSharedBridge(admitAllLife{})
	sb.state = state
	if state != bridgeStarting {
		sb.settled.settle(nil)
	}
	if s != nil {
		sb.attach(s)
	}
	return sb
}
