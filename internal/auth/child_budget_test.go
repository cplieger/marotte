package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The synchronous handlers' wall-clock caps must bound the handler, not the child. CommandContext SIGKILLs only the
// parent and helpers inherit the pipes, so Run waits for the last descendant: on go1.27.0 with this `sleep 10`
// fixture under 50ms, Run took 10.001s by default and with Setpgid alone. The assertion is on time because a cap
// bound to the child returns the right status after the wrong duration.
const (
	budget = 50 * time.Millisecond
	// childLife is how long the fake CLI lives, two orders of magnitude above the budget.
	childLife = 10 * time.Second
	// tolerance is the scheduling slack over budget+childWaitDelay, still far below childLife so the test can fail.
	tolerance = 2 * time.Second
)

func maxHandlerTime() time.Duration { return budget + childWaitDelay + tolerance }

func TestHandleWhoami_ReturnsOnItsOwnBudgetNotTheChildsLifetime(t *testing.T) {
	skipIfNotUnix(t)

	path := writeFakeCLIScript(t, "sleep 10\n")
	h := NewHandler(fixedPath(path), WithConfig(Config{
		LoginURLTimeout: DefaultConfig.LoginURLTimeout,
		LoginTimeout:    DefaultConfig.LoginTimeout,
		LogoutTimeout:   DefaultConfig.LogoutTimeout,
		WhoamiTimeout:   budget,
	}))

	rr := httptest.NewRecorder()
	start := time.Now()
	h.handleWhoami(rr, httptest.NewRequest(http.MethodGet, "/api/whoami", nil))
	elapsed := time.Since(start)

	if elapsed > maxHandlerTime() {
		t.Errorf("handleWhoami returned after %v, want <= %v: the handler waited out the child "+
			"(%v) instead of honouring WhoamiTimeout (%v)", elapsed, maxHandlerTime(), childLife, budget)
	}
	// Fail-soft is unchanged: 200 with the sentinel, on time.
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (fail-soft banner)", rr.Code)
	}
}

func TestHandleLogout_ReturnsOnItsOwnBudgetNotTheChildsLifetime(t *testing.T) {
	skipIfNotUnix(t)

	path := writeFakeCLIScript(t, "sleep 10\n")
	h := NewHandler(fixedPath(path), WithConfig(Config{
		LoginURLTimeout: DefaultConfig.LoginURLTimeout,
		LoginTimeout:    DefaultConfig.LoginTimeout,
		LogoutTimeout:   budget,
		WhoamiTimeout:   DefaultConfig.WhoamiTimeout,
	}))

	rr := httptest.NewRecorder()
	start := time.Now()
	h.handleLogout(rr, httptest.NewRequest(http.MethodPost, "/api/logout", nil))
	elapsed := time.Since(start)

	if elapsed > maxHandlerTime() {
		t.Errorf("handleLogout returned after %v, want <= %v: the handler waited out the child "+
			"(%v) instead of honouring LogoutTimeout (%v)", elapsed, maxHandlerTime(), childLife, budget)
	}
	if rr.Code != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want 504", rr.Code)
	}
}
