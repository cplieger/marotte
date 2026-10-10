package spec

import (
	"reflect"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

const testWindow = 500 * time.Millisecond

// The mutex is for the emit goroutine the timer runs.
type recorder struct {
	mu   sync.Mutex
	dirs []string
}

func (r *recorder) emit(dir string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dirs = append(r.dirs, dir)
}

func (r *recorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.dirs...)
}

func TestNotifier_EmitsOncePerWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &recorder{}
		n := NewNotifier(testWindow, rec.emit)
		n.Mark("a")
		n.Mark("a")
		synctest.Sleep(testWindow - time.Millisecond)
		if got := rec.seen(); len(got) != 0 {
			t.Fatalf("emits before the window closed = %v, want none", got)
		}
		n.Mark("a")
		synctest.Sleep(time.Millisecond)
		if got, want := rec.seen(), []string{"a"}; !reflect.DeepEqual(got, want) {
			t.Errorf("emits after three marks in one window = %v, want %v", got, want)
		}
		synctest.Sleep(testWindow)
		if got := rec.seen(); len(got) != 1 {
			t.Errorf("emits with no further mark = %v, want the one", got)
		}
	})
}

func TestNotifier_AMarkAfterTheCloseOpensTheNextWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &recorder{}
		n := NewNotifier(testWindow, rec.emit)
		n.Mark("a")
		synctest.Sleep(testWindow)
		n.Mark("a")
		synctest.Sleep(testWindow - time.Millisecond)
		if got := rec.seen(); len(got) != 1 {
			t.Fatalf("emits inside the second window = %v, want the first only", got)
		}
		synctest.Sleep(time.Millisecond)
		if got, want := rec.seen(), []string{"a", "a"}; !reflect.DeepEqual(got, want) {
			t.Errorf("emits after two windows = %v, want %v", got, want)
		}
	})
}

func TestNotifier_DirectoriesAreIndependent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &recorder{}
		n := NewNotifier(testWindow, rec.emit)
		n.Mark("a")
		synctest.Sleep(testWindow / 2)
		n.Mark("b")
		synctest.Sleep(testWindow / 2)
		if got, want := rec.seen(), []string{"a"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("emits at a's close = %v, want %v", got, want)
		}
		synctest.Sleep(testWindow / 2)
		if got, want := rec.seen(), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
			t.Errorf("emits at b's close = %v, want %v", got, want)
		}
	})
}

func TestNotifier_EmitRunsOutsideTheLock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &recorder{}
		var n *Notifier
		// A re-entrant Mark from emit deadlocks if emit ran under the mutex.
		n = NewNotifier(testWindow, func(dir string) {
			rec.emit(dir)
			if len(rec.seen()) == 1 {
				n.Mark(dir)
			}
		})
		n.Mark("a")
		synctest.Sleep(2 * testWindow)
		if got, want := rec.seen(), []string{"a", "a"}; !reflect.DeepEqual(got, want) {
			t.Errorf("emits after a re-entrant mark = %v, want %v", got, want)
		}
	})
}
