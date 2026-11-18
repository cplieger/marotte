package filebrowse

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/testsupport"
)

// listCosts is what a folder answer costs the server per request for tests/perf/synth's listed folder, 300 entries
// at x1. A change that moves one by design rewrites its entry in the same diff.
var listCosts = map[string]testsupport.Cost{
	"folder answer x1":  {Allocs: 6417, Bytes: 424062},
	"folder answer x10": {Allocs: 63122, Bytes: 4118080},
}

func TestListCost_FolderAnswerHoldsItsBudget(t *testing.T) {
	testsupport.RequireCostBuild(t)
	for _, scale := range []int{1, 10} {
		op := fmt.Sprintf("folder answer x%d", scale)
		t.Run(op, func(t *testing.T) {
			dir := testsupport.FixedLengthDir(t)
			h, err := New(Sensitive{}, []string{dir})
			if err != nil {
				t.Fatalf("Setup: %v", err)
			}
			for i := range 300 * scale {
				ext := [...]string{"go", "ts", "md", "json"}[i%4]
				line := "// " + strings.Repeat("entry ", 20) + "\n"
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("entry-%05d.%s", i, ext)), []byte(line), 0o644); err != nil {
					t.Fatalf("Setup: %v", err)
				}
			}
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)
			url := "/api/files?path=" + strings.TrimPrefix(dir, "/")
			got := testsupport.MeasureCost(testsupport.CostRuns(scale, 201, 101), func() {
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, http.NoBody))
				if rec.Code != http.StatusOK {
					t.Fatalf("GET %s = %d: %s", url, rec.Code, rec.Body.String())
				}
			})
			testsupport.HoldCost(t, op, listCosts[op], got)
		})
	}
}
