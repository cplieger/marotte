package preview

import (
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func stageOpenat2(t *testing.T, errs ...error) *int {
	t.Helper()
	calls := 0
	prev := openat2
	t.Cleanup(func() { openat2 = prev })
	openat2 = func(dirfd int, path string, how *unix.OpenHow) (int, error) {
		calls++
		if calls <= len(errs) {
			return -1, errs[calls-1]
		}
		return unix.Openat2(dirfd, path, how)
	}
	return &calls
}

func openDemo(t *testing.T, f *fixture) error {
	t.Helper()
	fd, err := openDir(f.h.workFD, "demo")
	if err == nil {
		t.Cleanup(func() { _ = unix.Close(fd) })
	}
	return err
}

func TestOpenBeneath_RetriesATransientEAGAIN(t *testing.T) {
	f := newFixture(t)
	calls := stageOpenat2(t, unix.EAGAIN, unix.EINTR)
	if err := openDemo(t, f); err != nil {
		t.Fatalf("openDir after one EAGAIN and one EINTR = %v, want success", err)
	}
	if *calls != 3 {
		t.Errorf("openat2 calls = %d, want 3", *calls)
	}
}

func TestOpenBeneath_APersistentEAGAINFailsClosed(t *testing.T) {
	f := newFixture(t)
	errs := make([]error, 2*maxResolveAttempts)
	for i := range errs {
		errs[i] = unix.EAGAIN
	}
	calls := stageOpenat2(t, errs...)
	if err := openDemo(t, f); !errors.Is(err, errResolveBusy) {
		t.Fatalf("openDir under endless EAGAIN = %v, want errResolveBusy", err)
	}
	if *calls != maxResolveAttempts {
		t.Errorf("openat2 calls = %d, want %d", *calls, maxResolveAttempts)
	}
	if got, body := f.grantStatus(filepath.Join(f.demo, "index.html")); got != http.StatusServiceUnavailable {
		t.Errorf("grant under endless EAGAIN = %d %s, want 503", got, body)
	}
}

func TestOpenBeneath_AnUnsupportedKernelRefusesEveryRoute(t *testing.T) {
	for _, kerr := range []unix.Errno{unix.ENOSYS, unix.EPERM} {
		t.Run(kerr.Error(), func(t *testing.T) {
			f := newFixture(t)
			g := f.grant(t, filepath.Join(f.demo, "index.html"))
			errs := make([]error, 8)
			for i := range errs {
				errs[i] = kerr
			}
			calls := stageOpenat2(t, errs...)
			if err := openDemo(t, f); !errors.Is(err, errResolveUnsupported) {
				t.Errorf("openDir on %v = %v, want errResolveUnsupported", kerr, err)
			}
			if *calls != 1 {
				t.Errorf("openat2 calls on %v = %d, want 1 (no retry)", kerr, *calls)
			}
			if got, body := f.grantStatus(filepath.Join(f.demo, "index.html")); got != http.StatusServiceUnavailable {
				t.Errorf("grant on %v = %d %s, want 503", kerr, got, body)
			}
			if rec := f.do(http.MethodGet, g.URL, ""); rec.Code != http.StatusServiceUnavailable {
				t.Errorf("GET page on %v = %d %s, want 503", kerr, rec.Code, rec.Body)
			}
			stamp := "/api/preview/stamp?path=" + url.QueryEscape(filepath.Join(f.demo, "index.html"))
			if rec := f.do(http.MethodGet, stamp, ""); rec.Code != http.StatusServiceUnavailable {
				t.Errorf("GET stamp on %v = %d %s, want 503", kerr, rec.Code, rec.Body)
			}
		})
	}
}
