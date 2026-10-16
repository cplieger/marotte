package signin

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/filemode"
	"github.com/cplieger/marotte/internal/marotte"
)

// clock is a settable time source shared by a vault and its fakes.
type clock struct {
	t  time.Time
	mu sync.Mutex
}

func newClock() *clock { return &clock{t: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fakeCred is a fake backend's record. Bad makes Validate refuse it.
type fakeCred struct {
	Expiry  time.Time `json:"expiry"`
	Account string    `json:"account"`
	Token   string    `json:"token"`
	Arn     string    `json:"arn,omitempty"`
	Class   string    `json:"class"`
	Bad     bool      `json:"bad,omitempty"`
}

func credOf(t *testing.T, c fakeCred) Credential {
	t.Helper()
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return Credential{Data: data}
}

func readCred(c Credential) (fakeCred, error) {
	var f fakeCred
	err := json.Unmarshal(c.Data, &f)
	return f, err
}

// fakeAnswer is what a fake backend's Result encodes.
type fakeAnswer struct {
	Backend string `json:"backend"`
	Account string `json:"account"`
	Token   string `json:"token"`
	Arn     string `json:"arn"`
	Class   string `json:"class"`
}

type approval struct {
	err  error
	cred Credential
}

// fakeBackend is a callback (or self-auth) backend whose policy is its fields: renewWithin and
// staleFloor are its thresholds, code and prefix its refusal payload.
type fakeBackend struct {
	clk         *clock
	refresh     func(ctx context.Context, c fakeCred) (fakeCred, error)
	beginHook   func()
	live        func(from, to fakeCred) bool
	approvals   chan approval
	pollEnded   chan error
	beginErr    error
	prefix      string
	decl        Declaration
	refreshes   int
	renewWithin time.Duration
	staleFloor  time.Duration
	expiresIn   time.Duration
	code        int
	mu          sync.Mutex
	// staleBandOnly makes Servable false for a Fresh record, as Backend.Servable's contract allows.
	staleBandOnly bool
}

var _ Backend = (*fakeBackend)(nil)

func newFake(clk *clock, id BackendID, callback string) *fakeBackend {
	return &fakeBackend{
		clk:         clk,
		decl:        Declaration{Backend: id, HostCallback: callback, SpawnArgs: []string{"--" + string(id)}, Flows: []FlowKind{FlowDevice}},
		approvals:   make(chan approval, 4),
		pollEnded:   make(chan error, 4),
		expiresIn:   10 * time.Minute,
		renewWithin: 5 * time.Minute,
		staleFloor:  210 * time.Second,
		code:        -32000,
		prefix:      string(id) + ": ",
	}
}

func (f *fakeBackend) Declaration() Declaration { return f.decl }

func (f *fakeBackend) Begin(context.Context, FlowParams) (DeviceStart, error) {
	if f.beginHook != nil {
		f.beginHook()
	}
	if f.beginErr != nil {
		return DeviceStart{}, f.beginErr
	}
	return DeviceStart{VerificationURL: "https://device.example/" + string(f.decl.Backend), UserCode: "CODE-1", ExpiresIn: f.expiresIn}, nil
}

func (f *fakeBackend) Poll(ctx context.Context, _ DeviceStart) (Credential, error) {
	select {
	case <-ctx.Done():
		f.pollEnded <- ctx.Err()
		return Credential{}, ctx.Err()
	case a := <-f.approvals:
		f.pollEnded <- a.err
		return a.cred, a.err
	}
}

func (f *fakeBackend) Refresh(ctx context.Context, c Credential) (Credential, error) {
	f.mu.Lock()
	f.refreshes++
	hook := f.refresh
	f.mu.Unlock()
	fc, err := readCred(c)
	if err != nil {
		return Credential{}, err
	}
	if hook != nil {
		fc, err = hook(ctx, fc)
		if err != nil {
			return Credential{}, err
		}
	} else {
		fc.Token += "+r"
		fc.Expiry = f.clk.now().Add(time.Hour)
	}
	data, err := json.Marshal(fc)
	return Credential{Data: data}, err
}

func (f *fakeBackend) refreshCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refreshes
}

func (f *fakeBackend) setRefresh(hook func(ctx context.Context, c fakeCred) (fakeCred, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refresh = hook
}

func (*fakeBackend) Validate(c Credential) error {
	fc, err := readCred(c)
	if err != nil {
		return err
	}
	if fc.Bad || fc.Account == "" {
		return errors.New("fake: unusable record")
	}
	return nil
}

func (*fakeBackend) Label(c Credential) Identity {
	fc, _ := readCred(c)
	return Identity{Method: "fake", AccountType: fc.Class, Email: fc.Account + "@example.com"}
}

func (f *fakeBackend) LiveSwitchable(from, to Credential) bool {
	if f.live == nil {
		return true
	}
	a, _ := readCred(from)
	b, _ := readCred(to)
	return f.live(a, b)
}

func (f *fakeBackend) Fresh(c Credential, now time.Time) bool {
	fc, err := readCred(c)
	return err == nil && fc.Expiry.After(now.Add(f.renewWithin))
}

func (f *fakeBackend) Servable(c Credential, now time.Time) bool {
	if f.staleBandOnly && f.Fresh(c, now) {
		return false
	}
	fc, err := readCred(c)
	return err == nil && fc.Expiry.After(now.Add(f.staleFloor))
}

func (f *fakeBackend) Result(c Credential, _ time.Time) (json.RawMessage, error) {
	fc, err := readCred(c)
	if err != nil {
		return nil, err
	}
	return json.Marshal(fakeAnswer{Backend: string(f.decl.Backend), Account: fc.Account, Token: fc.Token, Arn: fc.Arn, Class: fc.Class})
}

func (f *fakeBackend) Refusal(r Refusal) *marotte.RPCError {
	return &marotte.RPCError{Code: f.code, Message: f.prefix + r.String()}
}

// faultDisk is the host disk with faults: the next writeFails writes fail, handles report widen
// until a chmod takes, chmodFails, a staged file's stat or commit fails, the next discardFails
// discards leave the staged file, a remove fails, the next unsynced commits or directory syncs are
// not durable, reads fail (readErr), and while refusing every stage or remove after refuseAfter is
// refused. ops logs stages and staged-file removals, stagedAt each stage's time, durables the
// durable commits and syncs, exposed a last change not yet durable, and overlap the most stages
// and removes in flight at once.
type faultDisk struct {
	filemode.Disk
	removeErr error
	statErr   error
	commitErr error
	readErr   error
	// writeGate, when set, makes the next stage or remove report on entered and wait for a value
	// on it.
	writeGate    chan struct{}
	entered      chan struct{}
	ops          []string
	stagedAt     []time.Time
	writeFails   int
	refuseAfter  int
	discardFails int
	discards     int
	durables     int
	unsynced     int
	writes       int
	inFlight     int
	overlap      int
	exposed      bool
	widen        fs.FileMode
	mu           sync.Mutex
	chmodFails   bool
	refusing     bool
}

func newFaultDisk() *faultDisk { return &faultDisk{Disk: filemode.OS()} }

func (d *faultDisk) set(fn func(d *faultDisk)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	fn(d)
}

func (d *faultDisk) writeCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.writes
}

func (d *faultDisk) opLog() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.ops)
}

func (d *faultDisk) discardCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.discards
}

func (d *faultDisk) lastChangeDurable() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.exposed
}

func (d *faultDisk) maxOverlap() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.overlap
}

// begin counts one stage or remove in flight until done, held ones included, and waits on a set
// writeGate.
func (d *faultDisk) begin() (done func()) {
	d.mu.Lock()
	gate, entered := d.writeGate, d.entered
	d.writeGate = nil
	d.inFlight++
	d.overlap = max(d.overlap, d.inFlight)
	d.mu.Unlock()
	if gate != nil {
		entered <- struct{}{}
		<-gate
	}
	return func() {
		d.mu.Lock()
		d.inFlight--
		d.mu.Unlock()
	}
}

// refuseLocked reports whether this stage or remove is refused. The caller holds mu.
func (d *faultDisk) refuseLocked() bool {
	if !d.refusing {
		return false
	}
	if d.refuseAfter > 0 {
		d.refuseAfter--
		return false
	}
	return true
}

// takeUnsyncedLocked consumes one unsynced fault. The caller holds mu.
func (d *faultDisk) takeUnsyncedLocked() bool {
	if d.unsynced == 0 {
		return false
	}
	d.unsynced--
	return true
}

func (d *faultDisk) SyncDir(dir string) error {
	d.mu.Lock()
	fail := d.takeUnsyncedLocked()
	d.mu.Unlock()
	if fail {
		d.mu.Lock()
		d.exposed = true
		d.mu.Unlock()
		return errors.New("fault: sync refused")
	}
	if err := d.Disk.SyncDir(dir); err != nil {
		return err
	}
	d.mu.Lock()
	d.durables++
	d.exposed = false
	d.mu.Unlock()
	return nil
}

func (d *faultDisk) Stage(ctx context.Context, path string) (filemode.Staged, error) {
	defer d.begin()()
	d.mu.Lock()
	d.writes++
	d.ops = append(d.ops, "stage")
	d.stagedAt = append(d.stagedAt, time.Now())
	fail := d.writeFails > 0
	if fail {
		d.writeFails--
	}
	fail = d.refuseLocked() || fail
	d.mu.Unlock()
	if fail {
		return nil, errors.New("fault: write refused")
	}
	st, err := d.Disk.Stage(ctx, path)
	if err != nil {
		return nil, err
	}
	return faultStaged{Staged: st, d: d}, nil
}

func (d *faultDisk) Remove(path string) error {
	defer d.begin()()
	d.mu.Lock()
	err := d.removeErr
	if d.refuseLocked() {
		err = errors.New("fault: remove refused")
	}
	d.mu.Unlock()
	if err != nil {
		return err
	}
	return d.Disk.Remove(path)
}

func (d *faultDisk) Lstat(path string) (fs.FileInfo, error) {
	d.mu.Lock()
	readErr := d.readErr
	d.mu.Unlock()
	if readErr != nil {
		return nil, readErr
	}
	return d.Disk.Lstat(path)
}

func (d *faultDisk) OpenNoFollow(path string) (filemode.Handle, error) {
	d.mu.Lock()
	readErr := d.readErr
	d.mu.Unlock()
	if readErr != nil {
		return nil, readErr
	}
	h, err := d.Disk.OpenNoFollow(path)
	if err != nil {
		return nil, err
	}
	return faultHandle{File: h.(*os.File), d: d}, nil
}

type modeInfo struct {
	fs.FileInfo
	mode fs.FileMode
}

func (m modeInfo) Mode() fs.FileMode { return m.mode }

func (d *faultDisk) stat(h filemode.ModeHandle) (fs.FileInfo, error) {
	fi, err := h.Stat()
	d.mu.Lock()
	widen := d.widen
	d.mu.Unlock()
	if err != nil || widen == 0 {
		return fi, err
	}
	return modeInfo{FileInfo: fi, mode: widen}, nil
}

func (d *faultDisk) chmod(h filemode.ModeHandle, mode fs.FileMode) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.chmodFails {
		return errors.New("fault: chmod refused")
	}
	d.widen = 0
	return h.Chmod(mode)
}

type faultHandle struct {
	*os.File
	d *faultDisk
}

func (h faultHandle) Stat() (fs.FileInfo, error)   { return h.d.stat(h.File) }
func (h faultHandle) Chmod(mode fs.FileMode) error { return h.d.chmod(h.File, mode) }

type faultStaged struct {
	filemode.Staged
	d *faultDisk
}

func (s faultStaged) Stat() (fs.FileInfo, error) {
	s.d.mu.Lock()
	err := s.d.statErr
	s.d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return s.d.stat(s.Staged)
}

func (s faultStaged) Chmod(mode fs.FileMode) error { return s.d.chmod(s.Staged, mode) }

func (s faultStaged) Commit(ctx context.Context) (bool, error) {
	s.d.mu.Lock()
	err := s.d.commitErr
	unsynced := err == nil && s.d.takeUnsyncedLocked()
	s.d.mu.Unlock()
	if err != nil {
		return false, err
	}
	durable, err := s.Staged.Commit(ctx)
	if err == nil {
		s.d.mu.Lock()
		s.d.exposed = unsynced || !durable
		if !s.d.exposed {
			s.d.durables++
		}
		s.d.mu.Unlock()
	}
	durable = durable && !unsynced
	return durable, err
}

func (s faultStaged) Discard() error {
	s.d.mu.Lock()
	s.d.discards++
	fail := s.d.discardFails > 0
	if fail {
		s.d.discardFails--
	}
	s.d.mu.Unlock()
	if fail {
		return errors.New("fault: discard refused")
	}
	if err := s.Staged.Discard(); err != nil {
		return err
	}
	s.d.mu.Lock()
	s.d.ops = append(s.d.ops, "discard")
	s.d.mu.Unlock()
	return nil
}

// harness is one vault over a temp config dir, reopenable on the same dir.
type harness struct {
	t           *testing.T
	clk         *clock
	disk        *faultDisk
	v           *Vault
	cancel      context.CancelFunc
	dir         string
	backends    []Backend
	retryBase   time.Duration
	verdictWait time.Duration
}

func newHarness(t *testing.T, backends ...Backend) *harness {
	t.Helper()
	return newTimedHarness(t, time.Millisecond, 5*time.Second, backends...)
}

// newRetryingHarness is newHarness on the shipped retry cadence and sign-out wait.
func newRetryingHarness(t *testing.T, backends ...Backend) *harness {
	t.Helper()
	return newTimedHarness(t, time.Second, verdictWait, backends...)
}

func newTimedHarness(t *testing.T, retryBase, wait time.Duration, backends ...Backend) *harness {
	t.Helper()
	h := &harness{t: t, clk: newClock(), disk: newFaultDisk(), dir: t.TempDir(), backends: backends, retryBase: retryBase, verdictWait: wait}
	for _, b := range backends {
		if fb, ok := b.(*fakeBackend); ok {
			fb.clk = h.clk
		}
	}
	h.open()
	t.Cleanup(h.close)
	return h
}

func (h *harness) open() {
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.v = open(ctx, h.dir, config{disk: h.disk, now: h.clk.now, retryBase: h.retryBase, renewBudget: 5 * time.Second, verdictWait: h.verdictWait}, h.backends...)
}

func (h *harness) close() {
	h.cancel()
	h.v.Wait()
}

// reopen closes the vault and opens a fresh one on the same directory, as a marotte restart does.
func (h *harness) reopen() {
	h.close()
	h.open()
}

// crashed opens a second vault on the directory as it is now, with no shutdown save by the first,
// as a restart after a power loss that kept every rename does.
func (h *harness) crashed() *Vault {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	v := open(ctx, h.dir, config{disk: filemode.OS(), now: h.clk.now, retryBase: h.retryBase, renewBudget: 5 * time.Second, verdictWait: h.verdictWait}, h.backends...)
	h.t.Cleanup(func() {
		cancel()
		v.Wait()
	})
	return v
}

func (h *harness) path() string { return h.dir + "/" + fileName }

// signIn runs one sign-in to approval (or failure) and waits for the attempt to finish.
func (h *harness) signIn(fb *fakeBackend, a approval) Status {
	h.t.Helper()
	if _, err := h.v.StartSignIn(h.t.Context(), fb.decl.Backend, FlowParams{Flow: FlowDevice}); err != nil {
		h.t.Fatalf("StartSignIn: %v", err)
	}
	fb.approvals <- a
	return h.settled(fb.decl.Backend)
}

// settled waits until the backend's attempt is no longer pending.
func (h *harness) settled(id BackendID) Status {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st := h.v.Status(id); st.SignIn.State != AttemptPending {
			return st
		}
		time.Sleep(time.Millisecond)
	}
	h.t.Fatalf("the sign-in of %s never settled", id)
	return Status{}
}

func (h *harness) account(name string) fakeCred {
	return fakeCred{Account: name, Token: name + "-token", Arn: "arn:" + name, Class: "personal", Expiry: h.clk.now().Add(time.Hour)}
}

// answer decodes one Answer; it fails the test on a refusal unless the caller asks for refusals.
func (h *harness) answer(id BackendID) (fakeAnswer, *marotte.RPCError) {
	h.t.Helper()
	res, rerr := h.v.Answer(h.t.Context(), id)
	if rerr != nil {
		return fakeAnswer{}, rerr
	}
	var a fakeAnswer
	if err := json.Unmarshal(res, &a); err != nil {
		h.t.Fatalf("answer is not the fake's shape: %v", err)
	}
	return a, nil
}

func (h *harness) mustAnswer(id BackendID) fakeAnswer {
	h.t.Helper()
	a, rerr := h.answer(id)
	if rerr != nil {
		h.t.Fatalf("Answer(%s) refused: %v", id, rerr.Message)
	}
	return a
}

// changes records every OnChange in order.
type changes struct {
	got []Change
	mu  sync.Mutex
}

func (c *changes) record(ch Change) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, ch)
}

func (c *changes) list() []Change {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.got)
}

// wait returns the changes once n have been delivered; delivery follows the commit Status shows.
func (c *changes) wait(t *testing.T, n int) []Change {
	t.Helper()
	eventually(t, "the change deliveries", func() bool { return len(c.list()) >= n })
	return c.list()
}

func (h *harness) watch() *changes {
	c := &changes{}
	h.v.OnChange(c.record)
	return c
}

// eventually polls cond until it holds or 5 s pass.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
