package server

import (
	"net/http"
	"os/exec"
	"reflect"
	"strings"

	"github.com/cplieger/go/httpserver"
)

// ToolPresence is GET /api/tools/status' body: whether PATH has each binary kiro-cli or a feature
// panel expects (the MCP add modal gates on node/npx/uv). Each json name is the binary probed, and
// the fields are in name order, the order a map of the same keys encodes in.
type ToolPresence struct {
	Cargo                    bool `json:"cargo"`
	Clangd                   bool `json:"clangd"`
	Go                       bool `json:"go"`
	Gofmt                    bool `json:"gofmt"`
	Gopls                    bool `json:"gopls"`
	Java                     bool `json:"java"`
	Jdtls                    bool `json:"jdtls"`
	KotlinLanguageServer     bool `json:"kotlin-language-server"`
	Node                     bool `json:"node"`
	Npm                      bool `json:"npm"`
	Npx                      bool `json:"npx"`
	Pyrefly                  bool `json:"pyrefly"`
	Pyright                  bool `json:"pyright"`
	RustAnalyzer             bool `json:"rust-analyzer"`
	Rustc                    bool `json:"rustc"`
	Tsc                      bool `json:"tsc"`
	TypescriptLanguageServer bool `json:"typescript-language-server"`
	Uv                       bool `json:"uv"`
	Uvx                      bool `json:"uvx"`
}

// handleToolStatus serves GET /api/tools/status: a bare PATH presence probe per ToolPresence field.
func handleToolStatus(w http.ResponseWriter, _ *http.Request) {
	var out ToolPresence
	v := reflect.ValueOf(&out).Elem()
	for i := range v.NumField() {
		name, _, _ := strings.Cut(v.Type().Field(i).Tag.Get("json"), ",")
		_, err := exec.LookPath(name)
		v.Field(i).SetBool(err == nil)
	}
	httpserver.WriteJSON(w, out)
}
