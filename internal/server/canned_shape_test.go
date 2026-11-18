package server

import (
	"fmt"
	"testing"

	"github.com/cplieger/marotte/internal/testsupport"
	"github.com/cplieger/toolbelt/v3"
)

// The client's canned set must be what these handlers write: its read-budget tests replay it as the server.
func TestCannedAnswers_ReadAsTheHandlersWrite(t *testing.T) {
	// health.CheckHandler's documented answer; its type is unexported.
	type healthAnswer struct {
		Status  string   `json:"status"`
		Reasons []string `json:"reasons,omitempty"`
	}
	type versionAnswer struct {
		Marotte   string `json:"marotte"`
		ConfigDir string `json:"config_dir,omitempty"`
		KiroCLI   string `json:"kiro_cli,omitempty"`
	}
	type kiroSettingsAnswer struct {
		Settings map[string]string `json:"settings"`
	}
	testsupport.HoldCannedShape[healthAnswer](t, "GET /healthz")
	testsupport.HoldCannedShape[versionAnswer](t, "GET /api/version")
	testsupport.HoldCannedShape[kiroSettingsAnswer](t, "GET /api/kiro-settings?keys")
	testsupport.HoldCanned(t, "GET /api/tools/{id}", func(a *testsupport.CannedAnswer) error {
		if a.URL != toolsAPIPrefix+"/catalog" {
			return fmt.Errorf("records %s, whose answer no test here holds", a.URL)
		}
		var v toolbelt.CatalogInfo
		return testsupport.ReadsBackAs(a.Body, &v)
	})
}
