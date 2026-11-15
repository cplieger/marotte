package server

import (
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/policyfile"
)

func TestPolicyRuleAdd_ConcurrentAddsKeepEveryRule(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	s := &Server{workDir: work}
	const adds = 32

	codes := make([]int, adds)
	var wg sync.WaitGroup
	for i := range adds {
		wg.Go(func() {
			body := PolicyRuleWrite{Op: "add", Scope: "user", Capability: "fs_write", Effect: "allow", Match: []string{fmt.Sprintf("dir%02d/**", i)}}
			rec := postRules(t, s, body)
			codes[i] = rec.Code
		})
	}
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Errorf("add %d answered %d, want 200", i, code)
		}
	}
	up, _ := policyfile.PathFor(policyfile.ScopeUser, policyfile.Roots{KiroHome: filepath.Join(home, ".kiro"), WorkDir: work})
	f, err := policyfile.Load(up)
	if err != nil {
		t.Fatalf("Load the user permissions file: %v", err)
	}
	if len(f.Rules) != adds {
		t.Errorf("the user permissions file holds %d rules after %d concurrent adds of distinct rules, want every one", len(f.Rules), adds)
	}
}
