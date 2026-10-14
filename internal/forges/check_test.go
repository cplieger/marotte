package forges

// The check: the one writer of a connection's status after Load, run by the
// keeper for every usable row each pass and by the Check route.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
	"github.com/cplieger/slogx/capture"
)

const ghID = "github:github.com"

func TestKeeper_ATemporaryFailureStaysUsableAndHeals(t *testing.T) {
	core := &whoCore{}
	core.answer(errTransient, nil)
	m, changes := checkManager(t, core)
	k := NewKeeper(m)

	k.pass(t.Context())
	if got := viewFor(t, m, ghID); got.Status != ForgeRetrying {
		t.Fatalf("row after a transient failure = %+v, want retrying", got)
	}
	if _, err := m.reader(ghID); err != nil {
		t.Errorf("reader() of a retrying row = %v, want its handle: an outage leaves the row usable", err)
	}
	k.pass(t.Context())
	if got := viewFor(t, m, ghID); got.Status != ForgeConnected || got.Error != nil {
		t.Errorf("row after the forge answered again = %+v, want connected with no error", got)
	}
	if got := changes.all(); len(got) != 2 {
		t.Errorf("notified %+v, want one change into retrying and one out of it", got)
	}
}

func TestKeeper_APolicyOrTLSFailureStaysUsableAndHeals(t *testing.T) {
	core := &whoCore{}
	core.answer(
		&forgeapi.Error{
			Op: "Whoami", Kind: forgeapi.KindUpstream, Code: forgeapi.CodePrivateAddressRefused,
			Message: "the instance resolved to a private address",
		},
		&forgeapi.Error{Op: "Whoami", Kind: forgeapi.KindUpstream, Message: "x509: certificate signed by unknown authority"},
		nil,
	)
	m, _ := checkManager(t, core)
	k := NewKeeper(m)

	for _, want := range []ForgeStatus{ForgeRetrying, ForgeRetrying, ForgeConnected} {
		k.pass(t.Context())
		if got := viewFor(t, m, ghID); got.Status != want {
			t.Errorf("row after a pass = %+v, want %s", got, want)
		}
	}
}

func TestKeeper_ADeadlineNamesTheHost(t *testing.T) {
	core := &whoCore{block: make(chan struct{})}
	m, _ := checkManager(t, core)

	if _, err := m.check(t.Context(), ghID, 50*time.Millisecond); err != nil {
		t.Fatalf("check() = %v, want a verdict", err)
	}
	got := viewFor(t, m, ghID)
	if got.Status != ForgeRetrying || got.Error == nil || !strings.Contains(got.Error.Message, "no answer from github.com") {
		t.Errorf("row after a check the forge never answered = %+v, want retrying naming the host", got)
	}
}

func TestCheck_ACallerCancelWritesNoVerdict(t *testing.T) {
	t.Run("the caller leaves mid-request", func(t *testing.T) {
		core := &whoCore{block: make(chan struct{})}
		m, changes := checkManager(t, core)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			_, err := m.check(ctx, ghID, checkBudget)
			done <- err
		}()
		waitFor(t, "the identity read to start", func() bool { return core.calls() == 1 })
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("check() after its caller left = %v, want the caller's own error", err)
		}
		if got := viewFor(t, m, ghID); got.Status != ForgeConnected || got.CheckedAt != 0 || len(changes.all()) != 0 {
			t.Errorf("row after a cancelled check = %+v with changes %+v, want it untouched", got, changes.all())
		}
	})
	t.Run("the id lock is held past the budget", func(t *testing.T) {
		core := &whoCore{}
		m, _ := checkManager(t, core)
		unlock, err := m.locks.lock(t.Context(), ghID)
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		done := make(chan error, 1)
		go func() {
			_, err := m.check(t.Context(), ghID, 50*time.Millisecond)
			done <- err
		}()
		select {
		case err := <-done:
			if !errors.Is(err, errCheckBusy) {
				t.Errorf("check() behind a held lock = %v, want errCheckBusy", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("check() behind a held lock did not give up at its budget")
		}
		if got := viewFor(t, m, ghID); got.Status != ForgeConnected || got.CheckedAt != 0 || core.calls() != 0 {
			t.Errorf("row %+v after %d forge requests, want it untouched and nothing sent", got, core.calls())
		}
	})
}

// heldWrites holds each of m's records writes until release closes or the
// write's context ends, as atomicfile's last check before its rename does, and
// signals each write it holds on entered.
func heldWrites(t *testing.T, m *Manager) (entered <-chan struct{}, release chan struct{}) {
	t.Helper()
	in, rel := make(chan struct{}, 8), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-rel:
		default:
			close(rel)
		}
	})
	m.persist.Lock()
	real := m.records.writeFile
	m.records.writeFile = func(ctx context.Context, path string, data []byte) error {
		in <- struct{}{}
		select {
		case <-rel:
			return real(ctx, path, data)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	m.persist.Unlock()
	return in, rel
}

// checkAsync runs check on ctx and answers its result channel.
func checkAsync(ctx context.Context, m *Manager, budget time.Duration) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := m.check(ctx, ghID, budget)
		done <- err
	}()
	return done
}

func TestCheck_TheRecordsWriteEndsWithTheCheck(t *testing.T) {
	t.Run("a write held past the budget", func(t *testing.T) {
		core := &whoCore{}
		core.answer(errForbidden)
		m, _ := checkManager(t, core)
		before := fileBytes(t, m.records.path)
		heldWrites(t, m)

		select {
		case err := <-checkAsync(t.Context(), m, 200*time.Millisecond):
			if err != nil {
				t.Fatalf("check() = %v, want the fallback installed", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("check() with its records write held did not end at its budget")
		}
		got := viewFor(t, m, ghID)
		if got.Status != ForgeRetrying || got.Error == nil || !strings.Contains(got.Error.Message, errSaveTimedOut.Error()) {
			t.Errorf("row after a save that ran out of time = %+v, want retrying naming the late save", got)
		}
		if fileBytes(t, m.records.path) != before {
			t.Error("the records file changed, want it as it was")
		}
	})
	t.Run("the caller leaves during the write", func(t *testing.T) {
		core := &whoCore{}
		core.answer(errForbidden)
		m, changes := checkManager(t, core)
		before := fileBytes(t, m.records.path)
		entered, release := heldWrites(t, m)
		ctx, cancel := context.WithCancel(t.Context())

		done := checkAsync(ctx, m, checkBudget)
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("Setup: the check's records write never started")
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("check() after its caller left = %v, want the caller's own error", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("check() did not end when its caller left during the write")
		}
		close(release)
		if got := viewFor(t, m, ghID); got.Status != ForgeConnected || got.CheckedAt != 0 || len(changes.all()) != 0 {
			t.Errorf("row after a check whose caller left = %+v with changes %+v, want it untouched", got, changes.all())
		}
		if fileBytes(t, m.records.path) != before {
			t.Error("the records file changed after the caller left, want it as it was")
		}
	})
	t.Run("a forge that never answers", func(t *testing.T) {
		isolateGit(t)
		cfg := t.TempDir()
		seedConnections(t, cfg, "bob", githubRecord())
		writeRecords(t, cfg, connectionRecord{connectionSpec: githubRecord(), Standing: standingRefused})
		core := &whoCore{block: make(chan struct{})}
		m, _ := loadedManager(t, cfg, core)

		if _, err := m.check(t.Context(), ghID, 2*time.Second); err != nil {
			t.Fatalf("check() = %v", err)
		}
		if got := viewFor(t, m, ghID); got.Status != ForgeRetrying || got.Error == nil ||
			!strings.Contains(got.Error.Message, "no answer from github.com") {
			t.Errorf("row after a check the forge never answered = %+v, want retrying naming the host", got)
		}
		if recs := readRecords(t, cfg); len(recs) != 1 || recs[0].Standing != standingUsable {
			t.Errorf("records = %+v, want the verdict saved in the budget's held-back share", recs)
		}
	})
}

func TestCheck_ARefusalStopsTheKeeperUntilCheck(t *testing.T) {
	core := &whoCore{}
	core.answer(errForbidden)
	m, _ := checkManager(t, core)
	k := NewKeeper(m)

	k.pass(t.Context())
	if got := viewFor(t, m, ghID); got.Status != ForgeRefused {
		t.Fatalf("row after a 403 = %+v, want refused", got)
	}
	k.pass(t.Context())
	if n := core.calls(); n != 1 {
		t.Errorf("the forge saw %d identity reads after a second pass, want 1: a refused row waits for a Check", n)
	}
	core.answer(nil)
	if _, err := m.check(t.Context(), ghID, checkBudget); err != nil {
		t.Fatalf("Check() = %v", err)
	}
	if got := viewFor(t, m, ghID); got.Status != ForgeConnected {
		t.Errorf("row after a Check with the forge fixed = %+v, want connected", got)
	}
}

func TestStatus_EachTransitionClearsThePrior(t *testing.T) {
	core := &whoCore{}
	m, _ := checkManager(t, core)
	steps := []struct {
		answer error
		status ForgeStatus
		kind   string
	}{
		{errTransient, ForgeRetrying, "transient"},
		{errForbidden, ForgeRefused, "forbidden"},
		{nil, ForgeConnected, ""},
	}
	for _, s := range steps {
		core.answer(s.answer)
		if _, err := m.check(t.Context(), ghID, checkBudget); err != nil {
			t.Fatalf("check() = %v", err)
		}
		got := viewFor(t, m, ghID)
		switch {
		case got.Status != s.status:
			t.Errorf("row = %+v, want %s", got, s.status)
		case s.kind == "" && got.Error != nil:
			t.Errorf("connected row carries %+v, want no error left from before", got.Error)
		case s.kind != "" && (got.Error == nil || got.Error.Kind != s.kind):
			t.Errorf("%s row carries %+v, want only its own %s error", s.status, got.Error, s.kind)
		}
	}
}

// Installs swap whole values under the lock, so readers running beside a
// keeper flipping a row never see one half built (the race detector is the
// assertion).
func TestCheck_AMemoryOnlyInstallRacesNoReader_Concurrent(t *testing.T) {
	core := &whoCore{}
	m, _ := checkManager(t, core)
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for ctx.Err() == nil {
				_ = m.List()
				if h, err := m.reader(ghID); err == nil && h.id != ghID {
					t.Errorf("reader() answered %q, want %s", h.id, ghID)
				}
				_ = m.readers()
			}
		})
	}
	for i := range 50 {
		if i%2 == 0 {
			core.answer(errTransient)
		} else {
			core.answer(nil)
		}
		if _, err := m.check(t.Context(), ghID, checkBudget); err != nil {
			t.Fatalf("check() = %v", err)
		}
	}
	cancel()
	wg.Wait()
}

func TestCheck_AStatusChangeEvictsCapabilities(t *testing.T) {
	core := &whoCore{}
	m, _ := checkManager(t, core)
	mux := repoRoutes(m)
	if rec := getRoute(t, mux, githubCapsPath); rec.Code != http.StatusOK {
		t.Fatalf("Setup: GET capabilities = %d %s", rec.Code, rec.Body)
	}
	core.answer(errForbidden, nil)
	for range 2 {
		if _, err := m.check(t.Context(), ghID, checkBudget); err != nil {
			t.Fatalf("check() = %v", err)
		}
	}
	if got := viewFor(t, m, ghID); got.Status != ForgeConnected {
		t.Fatalf("Setup: row after refused then fixed = %+v, want connected", got)
	}

	if rec := getRoute(t, mux, githubCapsPath); rec.Code != http.StatusOK {
		t.Fatalf("GET capabilities = %d %s", rec.Code, rec.Body)
	}
	if core.capsRead != 2 {
		t.Errorf("the client was asked for capabilities %d times, want 2: the revival must read them again", core.capsRead)
	}
}

func TestCheck_ARenewedRateLimitNotifies(t *testing.T) {
	limited := &forgeapi.Error{Op: "Whoami", Kind: forgeapi.KindRateLimited, Status: 429, RetryAfter: time.Minute}
	core := &whoCore{}
	core.answer(limited)
	m, changes := checkManager(t, core)
	k := NewKeeper(m)

	k.pass(t.Context())
	first := viewFor(t, m, ghID)
	time.Sleep(5 * time.Millisecond) // two checks a real-clock instant apart
	k.pass(t.Context())
	second := viewFor(t, m, ghID)

	if first.Error == nil || first.Error.RetryAt != first.CheckedAt+60_000 {
		t.Fatalf("first limited row = %+v, want retry_at its check's end plus 60 s", first)
	}
	if second.Error == nil || second.Error.RetryAt != second.CheckedAt+60_000 || second.Error.RetryAt == first.Error.RetryAt ||
		second.Error.Message != first.Error.Message || second.Error.Kind != first.Error.Kind {
		t.Errorf("second limited row = %+v after %+v, want the same reason with the wait counted from its own end", second, first)
	}
	if got := changes.all(); len(got) != 2 {
		t.Errorf("two limited passes notified %+v, want two: the renewed wait is news", got)
	}

	t.Run("two healthy passes", func(t *testing.T) {
		core := &whoCore{}
		m, changes := checkManager(t, core)
		k := NewKeeper(m)
		k.pass(t.Context())
		k.pass(t.Context())
		if got := changes.all(); len(got) != 1 {
			t.Errorf("two healthy passes notified %+v, want only the first's new login: a moved checked_at is not news", got)
		}
	})
}

// keeperDone is the attributes of each keeper done line a capture recorded.
func keeperDone(logs *capture.Recorder) []map[string]any {
	var out []map[string]any
	for _, r := range logs.Records() {
		if r.Message != "forges: keeper pass done" {
			continue
		}
		attrs := map[string]any{}
		r.Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.Any()
			return true
		})
		out = append(out, attrs)
	}
	return out
}

func TestCheck_AReconnectRowIsNeverSent(t *testing.T) {
	isolateGit(t)
	cfg := t.TempDir()
	marked, standing, absent := specFor(KindGitHub, "github.com"), specFor(KindGitLab, "gitlab.com"), specFor(KindGitea, "gitea.test")
	seedConnections(t, cfg, "bob", marked, standing)
	store := openTestStore(t, cfg)
	cred := staticCredential(&marked, "bob", "test-token")
	cred.Usability = creds.UsabilityReconnectRequired
	if err := store.Save(slotKey(marked.ID, 0), cred); err != nil {
		t.Fatal(err)
	}
	writeRecords(t, cfg, connectionRecord{connectionSpec: marked},
		connectionRecord{connectionSpec: standing, Standing: standingReconnect}, connectionRecord{connectionSpec: absent})
	core := &whoCore{}
	m, changes := loadedManager(t, cfg, core)
	mux := repoRoutes(m)
	credsBefore := fileBytes(t, filepath.Join(cfg, credentialStoreDir, "credentials.json"))
	recordsBefore := fileBytes(t, m.records.path)

	for _, id := range []string{marked.ID, standing.ID, absent.ID} {
		before := viewFor(t, m, id)
		if before.Status != ForgeReconnect {
			t.Fatalf("Setup: %s = %+v, want reconnect", id, before)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/forges/"+id+"/probe", nil))
		if body := decodeBody(t, rec); rec.Code != http.StatusConflict || body["code"] != codeReconnectRequired ||
			body["error"] != errReconnectRequired.Error() {
			t.Errorf("Check of %s = %d %v, want 409 reconnect_required with its sentence", id, rec.Code, body)
		}
		if after := viewFor(t, m, id); !reflect.DeepEqual(after, before) {
			t.Errorf("row %s after the Check = %+v, want it untouched %+v", id, after, before)
		}
	}
	logs := capture.Default(t)
	NewKeeper(m).pass(t.Context())

	if core.calls() != 0 || len(changes.all()) != 0 {
		t.Errorf("the forge saw %d requests and %d changes were notified, want none", core.calls(), len(changes.all()))
	}
	if fileBytes(t, filepath.Join(cfg, credentialStoreDir, "credentials.json")) != credsBefore ||
		fileBytes(t, m.records.path) != recordsBefore {
		t.Error("a Check or a pass over reconnect rows rewrote a file, want both byte-identical")
	}
	if done := keeperDone(logs); len(done) != 1 || done[0]["checked"] != int64(0) {
		t.Errorf("the pass's done line = %v, want checked 0", done)
	}
}

func TestKeeper_LogsAStartAndADoneLinePerPass(t *testing.T) {
	m, _ := checkManager(t, &whoCore{})
	logs := capture.Default(t)
	k := NewKeeper(m)

	k.pass(t.Context())
	k.pass(t.Context())

	starts := 0
	for _, r := range logs.Records() {
		if r.Message == "forges: keeper pass started" && r.Level == slog.LevelInfo {
			starts++
		}
	}
	done := keeperDone(logs)
	if starts != 2 || len(done) != 2 {
		t.Fatalf("two passes logged %d start and %d done lines, want two of each", starts, len(done))
	}
	for _, key := range []string{"connections", "checked", "changed", "duration", "hits", "misses", "saved", "bytes"} {
		if _, ok := done[1][key]; !ok {
			t.Errorf("the done line %v carries no %q", done[1], key)
		}
	}
	if done[1]["connections"] != int64(1) || done[1]["checked"] != int64(1) {
		t.Errorf("the done line = %v, want one connection checked", done[1])
	}

	t.Run("a pass cancelled mid-check", func(t *testing.T) {
		core := &whoCore{block: make(chan struct{})}
		m, _ := checkManager(t, core)
		addConnections(t, m, "bob", specFor(KindGitLab, "gitlab.com"))
		logs := capture.Default(t)
		ctx, cancel := context.WithCancel(t.Context())
		finished := make(chan struct{})
		go func() {
			NewKeeper(m).pass(ctx)
			close(finished)
		}()
		waitFor(t, "the first check's identity read", func() bool { return core.calls() == 1 })
		cancel()
		<-finished

		starts := 0
		for _, r := range logs.Records() {
			if r.Message == "forges: keeper pass started" {
				starts++
			}
		}
		done := keeperDone(logs)
		if starts != 1 || len(done) != 1 {
			t.Fatalf("a cancelled pass logged %d start and %d done lines, want one of each", starts, len(done))
		}
		if done[0]["connections"] != int64(2) || done[0]["checked"] != int64(0) || core.calls() != 1 {
			t.Errorf("the done line = %v after %d identity reads, want two connections, none checked, the second never sent",
				done[0], core.calls())
		}
		for _, r := range logs.Records() {
			if r.Level >= slog.LevelWarn {
				t.Errorf("a cancelled pass logged %s %q, want no warning: nothing failed", r.Level, r.Message)
			}
		}
	})
}
