package bridge

import "testing"

// FuzzParseErrTracker drives random Record/Reset sequences and checks the state machine's invariants.
func FuzzParseErrTracker(f *testing.F) {
	f.Add([]byte{0, 0, 0, 1, 0, 0})
	f.Add([]byte{})
	f.Add(make([]byte, 100))

	f.Fuzz(func(t *testing.T, ops []byte) {
		var tr parseErrTracker
		for _, op := range ops {
			if op%2 == 0 {
				action := tr.Record()
				// consecutive <= total; total never decreases.
				if tr.consecutive > tr.total {
					t.Fatalf("consecutive (%d) > total (%d)", tr.consecutive, tr.total)
				}
				// Circuit-break iff consecutive reached parseErrMaxConsecutive.
				if tr.consecutive >= parseErrMaxConsecutive && action != parseErrCircuitBreak {
					t.Fatalf("consecutive=%d but action=%d, want circuitBreak", tr.consecutive, action)
				}
				if action == parseErrCircuitBreak && tr.consecutive < parseErrMaxConsecutive {
					t.Fatalf("circuitBreak at consecutive=%d (< %d)", tr.consecutive, parseErrMaxConsecutive)
				}
			} else {
				tr.Reset()
				// After Reset, consecutive is 0.
				if tr.consecutive != 0 {
					t.Fatalf("consecutive=%d after Reset, want 0", tr.consecutive)
				}
			}
		}
	})
}
