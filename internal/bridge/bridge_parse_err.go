package bridge

// parseErrTracker is a pure burst, summarize, circuit-break state machine for kiro-cli's malformed JSON lines.

import "time"

type parseErrAction int

const (
	parseErrLog          parseErrAction = iota // emit the error verbatim
	parseErrSuppress                           // within window, suppress
	parseErrSummarize                          // emit a summary line
	parseErrCircuitBreak                       // consecutive ceiling hit, tear down
)

// parseErrBurst is the verbatim lines before summary mode, parseErrWindow the summary cadence, and
// parseErrMaxConsecutive the consecutive-failure ceiling that tears the bridge down for a fresh one.
const (
	parseErrBurst          = 10
	parseErrWindow         = 30 * time.Second
	parseErrMaxConsecutive = 1000
)

type parseErrTracker struct {
	windowStart time.Time
	lastErrorAt time.Time
	total       int
	consecutive int
}

// parseErrDecay is how long the storm window lives without a new error, so a long-lived bridge regains its verbatim
// burst. It never touches the breaker: Reset clears that on every valid frame, so a slow total failure still trips.
const parseErrDecay = 5 * time.Minute

// record notes a parse error and returns the action readLoop should take.
func (t *parseErrTracker) record() parseErrAction {
	now := time.Now()
	if !t.lastErrorAt.IsZero() && now.Sub(t.lastErrorAt) > parseErrDecay {
		t.total = 0
		t.windowStart = time.Time{}
	}
	t.lastErrorAt = now
	t.total++
	t.consecutive++
	if t.consecutive >= parseErrMaxConsecutive {
		return parseErrCircuitBreak
	}
	if t.total <= parseErrBurst {
		if t.total == parseErrBurst {
			t.windowStart = now
		}
		return parseErrLog
	}
	// One clock reading per decision, so decay and window agree.
	if now.Sub(t.windowStart) > parseErrWindow {
		t.windowStart = now
		return parseErrSummarize
	}
	return parseErrSuppress
}

// Reset clears the consecutive counter on a successful parse.
func (t *parseErrTracker) Reset() { t.consecutive = 0 }

// summaryCount returns the errors suppressed since the last summary (total minus the burst).
func (t *parseErrTracker) summaryCount() int { return t.total - parseErrBurst }
