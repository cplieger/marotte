package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/powers"
)

type fakePowers struct {
	catalogErr error
	verbErr    error
	syncErr    error
	installed  []string
	removed    []string
	// modes records the ServersMode of every Sync and Uninstall, in order.
	modes   []powers.ServersMode
	catalog []powers.Entry
	// syncEntered and syncRelease, when set, hold every Sync.
	syncEntered    chan struct{}
	syncRelease    chan struct{}
	installEntered chan struct{}
	mu             sync.Mutex
	syncs          int
}

func (f *fakePowers) Catalog(context.Context) ([]powers.Entry, error) {
	return f.catalog, f.catalogErr
}

func (*fakePowers) Servers(_ context.Context, name string) ([]string, bool, error) {
	if name == "unknown" {
		return nil, false, powers.ErrUnknownPower
	}
	return []string{"srv"}, true, nil
}

func (f *fakePowers) Install(ctx context.Context, name string, mode powers.ModeFunc) error {
	switch mode() {
	case powers.ServersUnresolved:
		return powers.ErrPolicyUnknown
	case powers.ServersSuppressed:
		return powers.ErrPowersLocked
	}
	f.mu.Lock()
	f.installed = append(f.installed, name)
	entered := f.installEntered
	f.mu.Unlock()
	if entered != nil {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}
	return f.verbErr
}

func (f *fakePowers) Uninstall(_ context.Context, name string, mode powers.ModeFunc) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, name)
	f.modes = append(f.modes, mode())
	return f.verbErr
}

func (f *fakePowers) Sync(_ context.Context, mode powers.ModeFunc) error {
	f.mu.Lock()
	f.syncs++
	f.modes = append(f.modes, mode())
	entered, release := f.syncEntered, f.syncRelease
	f.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
		<-release
	}
	return f.syncErr
}

func (f *fakePowers) lastMode() (powers.ServersMode, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.modes) == 0 {
		return 0, false
	}
	return f.modes[len(f.modes)-1], true
}

func (f *fakePowers) syncCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.syncs
}

func newPowersHub(t *testing.T, backend *fakePowers) (*Runtime, *fakeBridge) {
	t.Helper()
	h, _, br := newTestHub()
	h.powers.backend = backend
	h.powers.adminKnown = func() bool { return true }
	h.powers.refreshAdmin = func() {}
	t.Cleanup(h.stopUtilityBridge)
	return h, br
}

func powersChangedCount(t *testing.T, h *Runtime) int {
	t.Helper()
	n := 0
	for _, typ := range extractTypes(t, bufferedSince(h, 0)) {
		if typ == string(marotte.EventPowersChanged) {
			n++
		}
	}
	return n
}

const kasPowersListReply = `{"powers":[
 {"name":"postman","mcpServerNames":["postman"],"hasSteeringFiles":true,"isAgentPlugin":false,
  "issues":[{"code":"x"}],"_meta":{"kiro":{"resource":{"resourceType":"power","source":{"origin":"user"}}}}},
 {"name":"local-only","displayName":"Local\u202eOnly","hasSteeringFiles":false,"isAgentPlugin":true},
 {"name":"../bad"}
],"errors":[]}`

func TestHandlePowersList_MergesTheCatalogueWithWhatKASReports(t *testing.T) {
	backend := &fakePowers{catalog: []powers.Entry{
		{Name: "postman", DisplayName: "API Testing", PublisherTier: "Official"},
		{Name: "figma", DisplayName: "Figma"},
	}}
	h, br := newPowersHub(t, backend)
	br.setCallResult(methodKiroPowersList, json.RawMessage(kasPowersListReply))

	rec := httptest.NewRecorder()
	h.powers.handleList(rec, httptest.NewRequest(http.MethodGet, "/api/powers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/powers = %d (%s)", rec.Code, rec.Body.String())
	}
	var body powersListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Catalog != powersReady || body.Installed != powersReady {
		t.Errorf("states = %s/%s, want ready/ready", body.Catalog, body.Installed)
	}
	names := make([]string, 0, len(body.Powers))
	for _, p := range body.Powers {
		names = append(names, p.Name)
	}
	if !slices.Equal(names, []string{"postman", "figma", "local-only"}) {
		t.Fatalf("rows = %v, want catalogue order then the installed-only power, the invalid name dropped", names)
	}
	pm, figma, local := body.Powers[0], body.Powers[1], body.Powers[2]
	if !pm.Installed || !pm.InCatalog || pm.Origin != "user" || !slices.Equal(pm.MCPServers, []string{"postman"}) ||
		pm.Issues != 1 || !pm.Steering || pm.PublisherTier != "Official" {
		t.Errorf("postman row = %+v", pm)
	}
	if figma.Installed {
		t.Errorf("figma reads installed; KAS did not report it")
	}
	if !local.Installed || local.InCatalog || !local.AgentPlugin || strings.ContainsRune(local.DisplayName, '\u202e') {
		t.Errorf("local-only row = %+v", local)
	}
	if backend.syncCount() != 1 {
		t.Errorf("Sync ran %d times, want once per list so a shell install gets its servers", backend.syncCount())
	}
}

func TestHandlePowersList_EachHalfFailsAlone(t *testing.T) {
	backend := &fakePowers{catalogErr: errors.New("offline")}
	h, br := newPowersHub(t, backend)
	br.setCallResult(methodKiroPowersList, json.RawMessage(kasPowersListReply))
	rec := httptest.NewRecorder()
	h.powers.handleList(rec, httptest.NewRequest(http.MethodGet, "/api/powers", nil))
	var body powersListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || body.Catalog != powersUnavailable || body.Installed != powersReady || len(body.Powers) != 2 {
		t.Errorf("offline catalogue: %d %+v, want 200, catalog unavailable, the two installed rows", rec.Code, body)
	}

	br.setCallErr(methodKiroPowersList, errors.New("bridge down"))
	backend.catalogErr = nil
	backend.catalog = []powers.Entry{{Name: "figma"}}
	rec = httptest.NewRecorder()
	h.powers.handleList(rec, httptest.NewRequest(http.MethodGet, "/api/powers", nil))
	body = powersListResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Installed != powersUnavailable || len(body.Powers) != 1 || body.Powers[0].Installed {
		t.Errorf("failed list: %+v, want installed unavailable and the catalogue row alone", body)
	}
}

func TestHandlePowersList_AFailedPowerMakesTheInstalledHalfPartialAndCarriesKASReason(t *testing.T) {
	var errs strings.Builder
	for i := range maxPowerLoadErrors + 5 {
		if i > 0 {
			errs.WriteByte(',')
		}
		fmt.Fprintf(&errs, `{"name":"bad-%d","source":"/secret/path/bad-%d","message":"the power could not\nbe read"}`, i, i)
	}
	reply := `{"powers":[{"name":"postman"}],"errors":[{"name":"","source":"/x","message":""},` + errs.String() + `]}`
	h, br := newPowersHub(t, &fakePowers{})
	br.setCallResult(methodKiroPowersList, json.RawMessage(reply))

	rec := httptest.NewRecorder()
	h.powers.handleList(rec, httptest.NewRequest(http.MethodGet, "/api/powers", nil))
	var body powersListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Installed != powersPartial {
		t.Errorf("installed = %q, want %q while KAS reports load errors", body.Installed, powersPartial)
	}
	if len(body.LoadErrors) != maxPowerLoadErrors {
		t.Fatalf("load_errors = %d entries, want the first %d that name something", len(body.LoadErrors), maxPowerLoadErrors)
	}
	if got := body.LoadErrors[0]; got.Name != "bad-0" || got.Message != "the power could not be read" {
		t.Errorf("first load error = %+v, want bad-0 with its reason on one line", got)
	}
	if strings.Contains(rec.Body.String(), "/secret/path") {
		t.Errorf("response carries KAS's source path: %s", rec.Body.String())
	}
	if len(body.Powers) != 1 || !body.Powers[0].Installed {
		t.Errorf("rows = %+v, want the Power KAS did load", body.Powers)
	}
}

func installReq(method, path, name string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.SetPathValue("name", name)
	return r
}

func TestHandlePowerInstall_RunsTheVerbRefreshesLiveProcessesAndBroadcasts(t *testing.T) {
	backend := &fakePowers{}
	h, utility := newPowersHub(t, backend)
	chat := newFakeBridge()
	h.bridge.mgr.bridges["c1"] = &sharedBridge{bridge: chat}

	rec := httptest.NewRecorder()
	h.powers.handleInstall(rec, installReq(http.MethodPost, "/api/powers/postman/install", "postman"))
	if rec.Code != http.StatusOK {
		t.Fatalf("install = %d (%s)", rec.Code, rec.Body.String())
	}
	if !slices.Equal(backend.installed, []string{"postman"}) {
		t.Errorf("installed = %v, want [postman]", backend.installed)
	}
	if got := chat.notified(methodKiroPowersRefresh); len(got) != 1 {
		t.Errorf("chat bridge powers refreshes = %v, want one", got)
	}
	if utility.startCount() != 0 || len(utility.notifyLog()) != 0 {
		t.Errorf("a stopped utility session was started (%d) or notified (%v)", utility.startCount(), utility.notifyLog())
	}
	if n := powersChangedCount(t, h); n != 1 {
		t.Errorf("powers_changed broadcast %d times, want 1", n)
	}

	rec = httptest.NewRecorder()
	h.powers.handleUninstall(rec, installReq(http.MethodDelete, "/api/powers/postman", "postman"))
	if rec.Code != http.StatusOK || !slices.Equal(backend.removed, []string{"postman"}) {
		t.Errorf("uninstall = %d, removed %v", rec.Code, backend.removed)
	}
}

func TestHandlePowerInstall_OutlivesTheRequestButNotShutdown(t *testing.T) {
	backend := &fakePowers{installEntered: make(chan struct{})}
	h, _ := newPowersHub(t, backend)
	reqCtx, closeTab := context.WithCancel(t.Context())
	req := installReq(http.MethodPost, "/api/powers/postman/install", "postman").WithContext(reqCtx)

	done := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.powers.handleInstall(rec, req)
		done <- rec.Code
	}()
	<-backend.installEntered
	closeTab()
	select {
	case code := <-done:
		t.Fatalf("install answered %d once the tab closed; it must keep running", code)
	case <-time.After(50 * time.Millisecond):
	}
	h.lifecycle.shutdownCancel()
	select {
	case code := <-done:
		if code == http.StatusOK {
			t.Errorf("install = 200 after shutdown cancelled it, want a failure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("install kept running after shutdown")
	}
}

func TestHandlePowerInstall_RefreshesARunningUtilitySession(t *testing.T) {
	h, utility := newPowersHub(t, &fakePowers{})
	if _, err := h.utility.get().session.acquire(t.Context()); err != nil {
		t.Fatalf("start utility: %v", err)
	}
	rec := httptest.NewRecorder()
	h.powers.handleInstall(rec, installReq(http.MethodPost, "/api/powers/postman/install", "postman"))
	if rec.Code != http.StatusOK || !slices.Contains(utility.notifyLog(), methodKiroPowersRefresh) {
		t.Errorf("install = %d, utility notifications %v; want a refresh", rec.Code, utility.notifyLog())
	}
}

func TestHandlePowerInstall_Refusals(t *testing.T) {
	for _, tc := range []struct {
		err      error
		name     string
		wantBody string
		wantCode int
	}{
		{name: "../x", wantCode: http.StatusBadRequest, wantBody: "invalid power name"},
		{name: "nope", err: powers.ErrUnknownPower, wantCode: http.StatusNotFound},
		{name: "postman", err: &powers.CLIError{Message: "tar not found"}, wantCode: http.StatusBadGateway, wantBody: "tar not found"},
		{name: "postman", err: errors.New("disk /secret/path full"), wantCode: http.StatusBadGateway, wantBody: "powers request failed"},
	} {
		backend := &fakePowers{verbErr: tc.err}
		h, _ := newPowersHub(t, backend)
		rec := httptest.NewRecorder()
		h.powers.handleInstall(rec, installReq(http.MethodPost, "/api/powers/x/install", tc.name))
		if rec.Code != tc.wantCode || !strings.Contains(rec.Body.String(), tc.wantBody) {
			t.Errorf("install(%q, %v) = %d %s, want %d containing %q", tc.name, tc.err, rec.Code, rec.Body.String(), tc.wantCode, tc.wantBody)
		}
		if strings.Contains(rec.Body.String(), "/secret/path") {
			t.Errorf("an internal error reached the client: %s", rec.Body.String())
		}
		if n := powersChangedCount(t, h); n != 0 {
			t.Errorf("install(%q) failed and still broadcast powers_changed", tc.name)
		}
		if tc.err == nil && len(backend.installed) != 0 {
			t.Errorf("an invalid name reached the backend: %v", backend.installed)
		}
	}
}

func TestHandlePowerVerbs_ARenderFailureStillConvergesAndSaysTheChangeWasMade(t *testing.T) {
	for _, tc := range []struct {
		name    string
		method  string
		path    string
		message string
		call    func(*powersSurface, http.ResponseWriter, *http.Request)
	}{
		{"install", http.MethodPost, "/api/powers/postman/install", "postman was installed, but its MCP servers could not be configured", (*powersSurface).handleInstall},
		{"uninstall", http.MethodDelete, "/api/powers/postman", "postman was removed, but its MCP servers could not be configured", (*powersSurface).handleUninstall},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &fakePowers{verbErr: &powers.RenderError{Err: errors.New("disk /secret/path full")}}
			h, _ := newPowersHub(t, backend)
			chat := newFakeBridge()
			h.bridge.mgr.bridges["c1"] = &sharedBridge{bridge: chat}

			rec := httptest.NewRecorder()
			tc.call(h.powers, rec, installReq(tc.method, tc.path, "postman"))
			var body struct {
				Error string `json:"error"`
				Code  string `json:"code"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("%s body %q: %v", tc.name, rec.Body.String(), err)
			}
			if rec.Code != http.StatusBadGateway || body.Code != powerRenderFailedCode || body.Error != tc.message {
				t.Errorf("%s = %d %+v, want 502 %q %q", tc.name, rec.Code, body, powerRenderFailedCode, tc.message)
			}
			if strings.Contains(rec.Body.String(), "/secret/path") {
				t.Errorf("an internal error reached the client: %s", rec.Body.String())
			}
			if got := chat.notified(methodKiroPowersRefresh); len(got) != 1 {
				t.Errorf("chat bridge powers refreshes = %v, want one", got)
			}
			if n := powersChangedCount(t, h); n != 1 {
				t.Errorf("powers_changed broadcast %d times, want 1", n)
			}
		})
	}
}

func TestPowersRoutes_AnswerUnavailableWithNoBackend(t *testing.T) {
	h, _, _ := newTestHub()
	h.powers.backend = nil
	for _, tc := range []struct {
		handler func(http.ResponseWriter, *http.Request)
		req     *http.Request
		route   string
	}{
		{route: "GET /api/powers", handler: h.powers.handleList, req: httptest.NewRequest(http.MethodGet, "/api/powers", nil)},
		{route: "POST install", handler: h.powers.handleInstall, req: installReq(http.MethodPost, "/api/powers/postman/install", "postman")},
		{route: "DELETE", handler: h.powers.handleUninstall, req: installReq(http.MethodDelete, "/api/powers/postman", "postman")},
	} {
		rec := httptest.NewRecorder()
		tc.handler(rec, tc.req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s with no backend = %d, want 503", tc.route, rec.Code)
		}
	}
}

func TestPowers_AnAdministratorLockRefusesInstallAndLeavesRemoval(t *testing.T) {
	backend := &fakePowers{}
	h, br := newPowersHub(t, backend)
	h.powers.locks = func() map[string]marotte.GovernanceLock {
		return map[string]marotte.GovernanceLock{marotte.LockPowers: {Source: marotte.LockSourceAdministrator, Reason: "Powers are blocked by your organization"}}
	}

	rec := httptest.NewRecorder()
	h.powers.handleInstall(rec, installReq(http.MethodPost, "/api/powers/postman/install", "postman"))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "blocked by your organization") {
		t.Errorf("locked install = %d %s, want 403 carrying the lock's reason", rec.Code, rec.Body.String())
	}
	if len(backend.installed) != 0 {
		t.Errorf("a locked install reached kiro-cli: %v", backend.installed)
	}

	br.setCallResult(methodKiroPowersList, json.RawMessage(kasPowersListReply))
	rec = httptest.NewRecorder()
	h.powers.handleList(rec, httptest.NewRequest(http.MethodGet, "/api/powers", nil))
	var body powersListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Blocked != "Powers are blocked by your organization" {
		t.Errorf("list blocked = %q, want the lock's reason", body.Blocked)
	}

	rec = httptest.NewRecorder()
	h.powers.handleUninstall(rec, installReq(http.MethodDelete, "/api/powers/postman", "postman"))
	if rec.Code != http.StatusOK || !slices.Equal(backend.removed, []string{"postman"}) {
		t.Errorf("uninstall under the lock = %d, removed %v; want 200 and the removal", rec.Code, backend.removed)
	}
}

func TestPowers_ALockLandingAfterThePrecheckStillRefusesTheInstall(t *testing.T) {
	backend := &fakePowers{}
	h, _ := newPowersHub(t, backend)
	var reads atomic.Int32
	h.powers.locks = func() map[string]marotte.GovernanceLock {
		if reads.Add(1) == 1 {
			return nil
		}
		return map[string]marotte.GovernanceLock{marotte.LockPowers: {Source: marotte.LockSourceAdministrator, Reason: "Powers are blocked by your organization"}}
	}

	rec := httptest.NewRecorder()
	h.powers.handleInstall(rec, installReq(http.MethodPost, "/api/powers/postman/install", "postman"))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "blocked by your organization") {
		t.Errorf("install with the lock landing after the precheck = %d %s, want 403 carrying the lock's reason", rec.Code, rec.Body.String())
	}
	if len(backend.installed) != 0 {
		t.Errorf("a locked install reached kiro-cli: %v", backend.installed)
	}
	if n := powersChangedCount(t, h); n != 0 {
		t.Errorf("a refused install broadcast powers_changed %d times, want 0", n)
	}
}

func TestHandlePowerServers_NamesTheDeclaredServers(t *testing.T) {
	h, _ := newPowersHub(t, &fakePowers{})
	rec := httptest.NewRecorder()
	h.powers.handleServers(rec, installReq(http.MethodGet, "/api/powers/postman/servers", "postman"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"servers":["srv"]`) || !strings.Contains(rec.Body.String(), `"known":true`) {
		t.Errorf("servers = %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.powers.handleServers(rec, installReq(http.MethodGet, "/api/powers/unknown/servers", "unknown"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("servers(unknown) = %d, want 404", rec.Code)
	}
}

func itemsChangedMsg(status string) *marotte.RPCResponse {
	return &marotte.RPCResponse{
		Method: methodV3Powers,
		Params: json.RawMessage(`{"sessionId":"s","status":"` + status + `","powers":[]}`),
	}
}

func TestPowersItemsChanged_FromAChatBridgeSyncsAndBroadcasts(t *testing.T) {
	backend := &fakePowers{}
	h, _ := newPowersHub(t, backend)
	h.translateACPEvent("c1", h.originOf("c1"), itemsChangedMsg("success"))
	waitFor(t, func() bool { return powersChangedCount(t, h) == 1 })
	if backend.syncCount() != 1 {
		t.Errorf("after items_changed: %d syncs, want 1", backend.syncCount())
	}
	h.translateACPEvent("c1", h.originOf("c1"), itemsChangedMsg("failed"))
	if len(h.powers.wake) != 0 {
		t.Errorf("a failed scan queued a sync")
	}
}

func TestPowersItemsChanged_FromTheUtilitySession(t *testing.T) {
	backend := &fakePowers{}
	h, _ := newPowersHub(t, backend)
	if !h.utility.get().session.dispatchNotification(itemsChangedMsg("success")) {
		t.Fatal("the utility session did not claim items_changed")
	}
	waitFor(t, func() bool { return powersChangedCount(t, h) == 1 })
	if backend.syncCount() != 1 {
		t.Errorf("utility items_changed: %d syncs, want 1", backend.syncCount())
	}
}

func TestPowersItemsChanged_NeverWaitsOnASlowSyncAndCoalesces(t *testing.T) {
	backend := &fakePowers{syncEntered: make(chan struct{}), syncRelease: make(chan struct{})}
	h, _ := newPowersHub(t, backend)
	h.powers.itemsChanged(t.Context(), json.RawMessage(`{"status":"success"}`))
	<-backend.syncEntered

	returned := make(chan struct{})
	go func() {
		for range 5 {
			h.powers.itemsChanged(t.Context(), json.RawMessage(`{"status":"success"}`))
		}
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("itemsChanged blocked behind a running sync")
	}
	backend.syncRelease <- struct{}{}
	<-backend.syncEntered
	backend.syncRelease <- struct{}{}
	waitFor(t, func() bool { return powersChangedCount(t, h) == 2 })
	if n := backend.syncCount(); n != 2 {
		t.Errorf("six items_changed during a slow sync ran %d syncs, want 2 (the running one and one coalesced)", n)
	}
}

func TestPowers_TheLockKeepsInstalledServersOutOfEveryRender(t *testing.T) {
	backend := &fakePowers{}
	h, br := newPowersHub(t, backend)
	var mu sync.Mutex
	locked := true
	h.powers.locks = func() map[string]marotte.GovernanceLock {
		mu.Lock()
		defer mu.Unlock()
		if !locked {
			return nil
		}
		return map[string]marotte.GovernanceLock{marotte.LockPowers: {Source: marotte.LockSourceAdministrator, Reason: "blocked"}}
	}
	wantMode := func(step string, want powers.ServersMode) {
		t.Helper()
		if got, ok := backend.lastMode(); !ok || got != want {
			t.Errorf("%s rendered in mode %v (recorded %v), want %v", step, got, ok, want)
		}
	}

	h.SyncPowers(t.Context())
	wantMode("boot", powers.ServersSuppressed)

	br.setCallResult(methodKiroPowersList, json.RawMessage(kasPowersListReply))
	h.powers.handleList(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/powers", nil))
	wantMode("GET /api/powers", powers.ServersSuppressed)

	syncs := backend.syncCount()
	h.translateACPEvent("c1", h.originOf("c1"), itemsChangedMsg("success"))
	waitFor(t, func() bool { return backend.syncCount() == syncs+1 })
	wantMode("items_changed", powers.ServersSuppressed)

	h.powers.handleUninstall(httptest.NewRecorder(), installReq(http.MethodDelete, "/api/powers/postman", "postman"))
	wantMode("uninstall", powers.ServersSuppressed)

	mu.Lock()
	locked = false
	mu.Unlock()
	syncs, broadcasts := backend.syncCount(), powersChangedCount(t, h)
	h.onGovernanceLocksChanged(t.Context())
	waitFor(t, func() bool { return backend.syncCount() == syncs+1 && powersChangedCount(t, h) == broadcasts+1 })
	wantMode("unlock", powers.ServersActive)

	mu.Lock()
	locked = true
	mu.Unlock()
	syncs = backend.syncCount()
	h.onGovernanceLocksChanged(t.Context())
	waitFor(t, func() bool { return backend.syncCount() == syncs+1 })
	wantMode("lock", powers.ServersSuppressed)
}

func TestPowers_ServersStayOffUntilTheAdministratorRulesResolve(t *testing.T) {
	backend := &fakePowers{}
	h, _, _ := newTestHub()
	h.powers.backend = backend
	asked := 0
	h.powers.refreshAdmin = func() { asked++ }
	t.Cleanup(h.stopUtilityBridge)

	h.SyncPowers(t.Context())
	if got, ok := backend.lastMode(); !ok || got != powers.ServersUnresolved {
		t.Fatalf("boot render mode = %v (recorded %v), want unresolved before the administrator rules are read", got, ok)
	}
	if asked != 1 {
		t.Errorf("boot asked for the administrator rules %d times, want 1", asked)
	}

	// Rules that lock nothing leave the lock map alone, so only the resolution renders.
	syncs := backend.syncCount()
	h.config.publishGovernance(t.Context(), h.config.governance.setAdmin(reduceAdminRules(nil)))
	waitFor(t, func() bool { return backend.syncCount() == syncs+1 })
	if got, _ := backend.lastMode(); got != powers.ServersActive {
		t.Errorf("render after the rules resolved = %v, want active", got)
	}
}

func TestPowers_InstallWaitsForTheAdministratorRules(t *testing.T) {
	backend := &fakePowers{}
	h, _, br := newTestHub()
	h.powers.backend = backend
	h.powers.refreshAdmin = func() {}
	t.Cleanup(h.stopUtilityBridge)
	br.setCallResult(methodKiroPowersList, json.RawMessage(`{"powers":[],"errors":[]}`))

	rec := httptest.NewRecorder()
	h.powers.handleInstall(rec, installReq(http.MethodPost, "/api/powers/postman/install", "postman"))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), powerPolicyPendingText) {
		t.Errorf("install before the rules are read = %d %s, want 409 %q", rec.Code, rec.Body.String(), powerPolicyPendingText)
	}
	if len(backend.installed) != 0 {
		t.Errorf("an install before the rules were read reached kiro-cli: %v", backend.installed)
	}
	rec = httptest.NewRecorder()
	h.powers.handleList(rec, httptest.NewRequest(http.MethodGet, "/api/powers", nil))
	if !strings.Contains(rec.Body.String(), `"policy_pending":true`) {
		t.Errorf("list before the rules are read = %s, want policy_pending", rec.Body.String())
	}

	h.config.publishGovernance(t.Context(), h.config.governance.setAdmin(reduceAdminRules(nil)))
	rec = httptest.NewRecorder()
	h.powers.handleInstall(rec, installReq(http.MethodPost, "/api/powers/postman/install", "postman"))
	if rec.Code != http.StatusOK || !slices.Equal(backend.installed, []string{"postman"}) {
		t.Errorf("install after the rules resolved = %d %s, installed %v; want 200 and the install", rec.Code, rec.Body.String(), backend.installed)
	}
	rec = httptest.NewRecorder()
	h.powers.handleList(rec, httptest.NewRequest(http.MethodGet, "/api/powers", nil))
	if strings.Contains(rec.Body.String(), "policy_pending") {
		t.Errorf("list after the rules resolved = %s, want no policy_pending", rec.Body.String())
	}
}

func TestHandlePowersList_APowerWhoseServersCouldNotBeConfiguredIsALoadError(t *testing.T) {
	backend := &fakePowers{syncErr: &powers.PartialError{Failed: []string{"broken", "both"}}}
	h, br := newPowersHub(t, backend)
	reply := `{"powers":[{"name":"postman"}],"errors":[{"name":"both","message":"kas could not load it"}]}`
	br.setCallResult(methodKiroPowersList, json.RawMessage(reply))

	rec := httptest.NewRecorder()
	h.powers.handleList(rec, httptest.NewRequest(http.MethodGet, "/api/powers", nil))
	var body powersListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := []powerLoadError{
		{Name: "both", Message: "kas could not load it"},
		{Name: "broken", Message: powerUnconfiguredText},
	}
	if body.Installed != powersPartial || !slices.Equal(body.LoadErrors, want) {
		t.Errorf("list = %s / %+v, want partial with %+v", body.Installed, body.LoadErrors, want)
	}
}
