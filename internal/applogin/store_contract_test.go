package applogin

import (
	"context"
	"testing"

	"github.com/cplieger/access/accesstest"
	"github.com/cplieger/access/account"
)

func TestStoreContract(t *testing.T) {
	newStore := func(t *testing.T) account.Store {
		s, _ := openTestStore(t)
		return s
	}
	newFaulty := func(t *testing.T) (account.Store, accesstest.DurabilityFaults) {
		s, d := openTestStore(t)
		return s, d.faults()
	}
	reopen := func(t *testing.T, old account.Store) account.Store {
		s := old.(*Store)
		s.Close()
		d, ok := testDisks.Load(s.path)
		if !ok {
			t.Fatalf("no test disk for %s", s.path)
		}
		r, _, err := OpenStore(context.Background(), s.path, d.(*testDisk))
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		t.Cleanup(r.Close)
		return r
	}
	accesstest.StoreContract(t, newStore, accesstest.ContractConfig{NewFaulty: newFaulty, Reopen: reopen})
}
