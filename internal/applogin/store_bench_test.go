package applogin

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/access"
	"github.com/cplieger/access/account"
	"github.com/cplieger/go/slogx/capture"
	"github.com/cplieger/marotte/internal/filemode"
)

// benchStore opens a store under MAROTTE_ACCESS_BENCH_DIR, the medium being measured, or else a
// temporary directory.
func benchStore(b *testing.B) (*Store, *testDisk) {
	b.Helper()
	capture.SwapDefault(b, slog.New(slog.DiscardHandler))
	dir := b.TempDir()
	if root := os.Getenv("MAROTTE_ACCESS_BENCH_DIR"); root != "" {
		var err error
		if dir, err = os.MkdirTemp(root, "bench-"); err != nil {
			b.Fatal(err)
		}
		b.Cleanup(func() { _ = os.RemoveAll(dir) })
	}
	d := &testDisk{t: b, real: filemode.OS(), path: filepath.Join(dir, "access.json")}
	testDisks.Store(d.path, d)
	b.Cleanup(func() { testDisks.Delete(d.path) })
	return d.open(b), d
}

// countAttempt is the library's failure count for one password attempt: one Update reading the
// name's user and record and writing the record back plus one.
func countAttempt(ctx context.Context, s *Store, ref account.FailureRef) error {
	return s.Update(ctx, func(tx account.Tx) error {
		if _, _, err := tx.UserByUsername(ctx, "owner"); err != nil {
			return err
		}
		rec, _, err := tx.AuthFailure(ctx, ref)
		if err != nil {
			return err
		}
		rec.FailureRef = ref
		rec.Count++
		return tx.PutAuthFailure(ctx, rec)
	})
}

// BenchmarkCountCommit is the latency the count commit adds to a password attempt on the real
// store, for one attempt at a time and for 16 concurrent ones sharing group commits.
func BenchmarkCountCommit(b *testing.B) {
	for _, n := range []int{1, 16} {
		b.Run(fmt.Sprintf("concurrent=%d", n), func(b *testing.B) {
			s, d := benchStore(b)
			ctx := context.Background()
			refs := make([]account.FailureRef, n)
			for i := range refs {
				refs[i] = account.FailureRef{Name: fmt.Sprintf("%064x", i), Method: access.MethodPassword}
			}
			base := d.publishes.Load()
			attempts := 0
			start := time.Now()
			for b.Loop() {
				var wg sync.WaitGroup
				for i := range n {
					wg.Go(func() {
						if err := countAttempt(ctx, s, refs[i]); err != nil {
							b.Error(err)
						}
					})
				}
				wg.Wait()
				attempts += n
			}
			b.ReportMetric(float64(time.Since(start).Nanoseconds())/float64(attempts), "ns/attempt")
			b.ReportMetric(float64(d.publishes.Load()-base)/float64(attempts), "publishes/attempt")
		})
	}
}

// fillToBound writes 2 × account.MaxUntrustedCounts unknown names' failure records beside the
// owner's and, with log set, 1,000 log entries in each class, marotte's caps.
func fillToBound(b *testing.B, s *Store, log bool) account.FailureRef {
	b.Helper()
	ctx := context.Background()
	owner := account.FailureRef{Name: fmt.Sprintf("%064x", 0), Method: access.MethodPassword}
	err := s.write(ctx, func(tx *txView) error {
		if err := tx.PutAuthFailure(ctx, account.FailureCount{FailureRef: owner, Count: 3}); err != nil {
			return err
		}
		for i := 1; i <= 2*account.MaxUntrustedCounts; i++ {
			ref := account.FailureRef{Name: fmt.Sprintf("%064x", i), Method: access.MethodPassword}
			if err := tx.PutAuthFailure(ctx, account.FailureCount{FailureRef: ref, Count: 1}); err != nil {
				return err
			}
		}
		for _, class := range account.LogClasses() {
			for i := 0; log && i < 1000; i++ {
				e := &account.LogEntry{At: epoch, Class: class, Event: account.Event{Kind: account.EventSignedIn, At: epoch, UserID: 1}}
				if err := tx.AppendLog(ctx, e); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		b.Fatal(err)
	}
	return owner
}

// BenchmarkPublishAndPruneAtBound is the cost of one publish and of one prune with rule 15's
// bound of failure records in the store and the security log at marotte's caps.
func BenchmarkPublishAndPruneAtBound(b *testing.B) {
	b.Run("publish", func(b *testing.B) {
		s, _ := benchStore(b)
		owner := fillToBound(b, s, true)
		ctx := context.Background()
		for b.Loop() {
			if err := countAttempt(ctx, s, owner); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("prune", func(b *testing.B) {
		s, _ := benchStore(b)
		owner := fillToBound(b, s, true)
		ctx := context.Background()
		for b.Loop() {
			b.StopTimer()
			fillToBound(b, s, false)
			b.StartTimer()
			if _, err := s.PruneAuthFailures(ctx, []string{owner.Name}); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkArgon2Verify is one password verification at the default parameters, the reference
// the count commit's latency is judged against.
func BenchmarkArgon2Verify(b *testing.B) {
	params := access.DefaultArgon2Params()
	budget, err := access.NewHashBudget(access.HashCost(params))
	if err != nil {
		b.Fatal(err)
	}
	h, err := access.NewHasher(access.HasherConfig{Params: params, Budget: budget})
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	hash, err := h.Hash(ctx, "correct horse battery staple 42")
	if err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		if ok, _, err := h.Verify(ctx, "correct horse battery staple 42", hash); err != nil || !ok {
			b.Fatalf("Verify = (%t, %v)", ok, err)
		}
	}
}
