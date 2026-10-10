package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/fstest"

	"github.com/cplieger/marotte/internal/filebrowse"
	"go.yaml.in/yaml/v3"
)

type noRoutes struct{}

func (noRoutes) RegisterRoutes(*http.ServeMux) {}

// testKASNode prefers the node KAS runs on, so a verdict is the engine's that runs the hook; any
// node otherwise, since every case here holds across V8 versions.
var testKASNode = sync.OnceValue(func() string {
	if p := os.Getenv("KIRO_KAS_NODE_PATH"); p != "" {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		if p := filepath.Join(home, ".local", "share", "kiro-cli", "node"); isRegularFile(p) {
			return p
		}
	}
	p, _ := exec.LookPath("node")
	return p
})

func isRegularFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

func requireKASNode(t *testing.T) {
	t.Helper()
	if testKASNode() == "" {
		t.Skip("no node binary to compile matchers with")
	}
}

func docServer(workDir string, opts ...Option) *Server {
	s := New(append([]Option{
		WithWorkDir(workDir),
		WithSensitive(filebrowse.NewSensitive(filepath.Join(workDir, "no-config"))),
		WithKASNode(testKASNode()),
	}, opts...)...)
	s.staticFS = fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}
	s.agent = &fakeEngine{}
	s.push = &testPush{}
	for _, f := range []*routeHandler{&s.chats, &s.auth, &s.git, &s.files, &s.mcpConfig, &s.mcpStatus, &s.mcpRegistry} {
		*f = noRoutes{}
	}
	return s
}

func postKiroDocNew(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/workspace/kiro-docs/new", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	return rec
}

func createdPath(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", rec.Code, rec.Body.String())
	}
	var res kiroDocNewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode reply %q: %v", rec.Body.String(), err)
	}
	return res.Path
}

func readCreated(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".kiro", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read the created file: %v", err)
	}
	return string(data)
}

func treeOf(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return out
}

// The bytes each kind writes are Kiro's own template, so they are pinned whole.
func TestHandleKiroDocNew_WritesKirosTemplate(t *testing.T) {
	cases := []struct {
		name string
		body string
		rel  string
		want string
	}{
		{
			name: "steering_always",
			body: `{"category":"steering","name":"conventions","inclusion":"always"}`,
			rel:  "steering/conventions.md",
			want: "---\ninclusion: always\n---\n" +
				"<!------------------------------------------------------------------------------------\n" +
				"   Add rules to this file or a short description and have Kiro refine them for you.\n" +
				"   \n" +
				"   Learn about inclusion modes: https://kiro.dev/docs/steering/#inclusion-modes\n" +
				"-------------------------------------------------------------------------------------> ",
		},
		{
			name: "steering_manual_keeps_a_typed_md",
			body: `{"category":"steering","name":"release.md","inclusion":"manual"}`,
			rel:  "steering/release.md",
			want: "---\ninclusion: manual\n---\n" +
				"<!------------------------------------------------------------------------------------\n" +
				"   This file is included only when invoked as a slash command (`/<filename>`)\n" +
				"   in chat. Use it for prompts and instructions you want to run on demand \u2014\n" +
				"   the manual replacement for user-triggered hooks.\n\n" +
				"   Learn about inclusion modes: https://kiro.dev/docs/steering/#inclusion-modes\n" +
				"-------------------------------------------------------------------------------------> ",
		},
		{
			name: "prompt",
			body: `{"category":"prompt","name":"code-review"}`,
			rel:  "prompts/code-review.md",
			want: "# Enter your prompt content here\n\nDescribe what this prompt should do...",
		},
		{
			name: "agent",
			body: `{"category":"agent","name":"reviewer"}`,
			rel:  "agents/reviewer.json",
			want: "{\n  \"name\": \"reviewer\",\n  \"description\": \"\",\n  \"prompt\": \"\",\n  \"tools\": []\n}\n",
		},
		{
			name: "skill",
			body: `{"category":"skill","name":"pr-review","description":"Review pull requests. Use when reviewing PRs."}`,
			rel:  "skills/pr-review/SKILL.md",
			want: "---\nname: \"pr-review\"\ndescription: \"Review pull requests. Use when reviewing PRs.\"\n---\n",
		},
		{
			name: "hook_agent_drops_matcher_and_timeout_where_the_ide_does",
			body: `{"category":"hook","name":"Greet","trigger":"SessionStart","matcher":"(","action":"agent",` +
				`"content":"Say hello","timeout":5}`,
			rel: "hooks/greet.json",
			want: "{\n  \"version\": \"v1\",\n  \"hooks\": [\n    {\n" +
				"      \"name\": \"Greet\",\n" +
				"      \"trigger\": \"SessionStart\",\n" +
				"      \"action\": {\n        \"type\": \"agent\",\n        \"prompt\": \"Say hello\"\n      },\n" +
				"      \"enabled\": true\n    }\n  ]\n}\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			got := createdPath(t, postKiroDocNew(t, docServer(dir), tc.body))
			if want := strings.TrimPrefix(dir, "/") + "/.kiro/" + tc.rel; got != want {
				t.Errorf("path = %q, want %q", got, want)
			}
			if body := readCreated(t, dir, tc.rel); body != tc.want {
				t.Errorf("file = %q\nwant  %q", body, tc.want)
			}
		})
	}
}

func TestHandleKiroDocNew_WritesAHookWithItsMatcher(t *testing.T) {
	requireKASNode(t)
	dir := t.TempDir()
	createdPath(t, postKiroDocNew(t, docServer(dir), `{"category":"hook","name":"Lint on Save","description":"Lints <ts>",`+
		`"trigger":"PostFileSave","matcher":"\\.ts$","action":"command","content":"npm run lint && echo ok","timeout":30}`))
	want := "{\n  \"version\": \"v1\",\n  \"hooks\": [\n    {\n" +
		"      \"name\": \"Lint on Save\",\n" +
		"      \"trigger\": \"PostFileSave\",\n" +
		"      \"action\": {\n        \"type\": \"command\",\n        \"command\": \"npm run lint && echo ok\"\n      },\n" +
		"      \"description\": \"Lints <ts>\",\n" +
		"      \"matcher\": \"\\\\.ts$\",\n" +
		"      \"timeout\": 30,\n" +
		"      \"enabled\": true\n    }\n  ]\n}\n"
	if body := readCreated(t, dir, "hooks/lint-on-save.json"); body != want {
		t.Errorf("file = %q\nwant  %q", body, want)
	}
}

func TestHandleKiroDocNew_PathMatchesTheInventoryRow(t *testing.T) {
	dir := t.TempDir()
	srv := docServer(dir)
	got := createdPath(t, postKiroDocNew(t, srv, `{"category":"steering","name":"a","inclusion":"always"}`))
	docs := srv.collectKiroDocs(t.Context()).Docs
	if len(docs) != 1 || docs[0].Path != got {
		t.Fatalf("inventory = %+v, want one row at %q", docs, got)
	}
	if docs[0].Inclusion != "always" {
		t.Errorf("row inclusion = %q, want always", docs[0].Inclusion)
	}
}

func TestHandleKiroDocNew_NeverOverwrites(t *testing.T) {
	cases := []struct {
		name     string
		existing string
		body     string
		wantMsg  string
	}{
		{"steering", "steering/a.md", `{"category":"steering","name":"a","inclusion":"always"}`, `A file named "a.md" already exists.`},
		{"prompt", "prompts/p.md", `{"category":"prompt","name":"p"}`, "Prompt 'p' already exists."},
		{"skill", "skills/s/SKILL.md", `{"category":"skill","name":"s","description":"d"}`, `A skill named "s" already exists.`},
		{"agent_json", "agents/x.json", `{"category":"agent","name":"x"}`, "File already exists at .kiro/agents/x.json. Aborting"},
		{"agent_md_twin", "agents/x.md", `{"category":"agent","name":"x"}`, "File already exists at .kiro/agents/x.json. Aborting"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, ".kiro/"+tc.existing, "mine")
			rec := postKiroDocNew(t, docServer(dir), tc.body)
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), jsonEscaped(tc.wantMsg)) {
				t.Errorf("body = %s, want the message %q", rec.Body.String(), tc.wantMsg)
			}
			if got := readCreated(t, dir, tc.existing); got != "mine" {
				t.Errorf("existing file = %q, want it untouched", got)
			}
		})
	}
}

func jsonEscaped(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.Trim(strings.TrimSpace(b.String()), `"`)
}

// The IDE numbers a colliding hook file rather than refusing it.
func TestHandleKiroDocNew_HookFileIsNumberedOnCollision(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".kiro/hooks/lint.json", "{}")
	writeFile(t, dir, ".kiro/hooks/lint-1.json", "{}")
	got := createdPath(t, postKiroDocNew(t, docServer(dir), `{"category":"hook","name":"Lint","trigger":"Stop","action":"command","content":"x"}`))
	if !strings.HasSuffix(got, "/.kiro/hooks/lint-2.json") {
		t.Errorf("path = %q, want lint-2.json", got)
	}
	if body := readCreated(t, dir, "hooks/lint.json"); body != "{}" {
		t.Errorf("lint.json = %q, want it untouched", body)
	}
}

func TestHandleKiroDocNew_HookNameWithNoSlugIsHook(t *testing.T) {
	dir := t.TempDir()
	got := createdPath(t, postKiroDocNew(t, docServer(dir),
		`{"category":"hook","name":"!!!","trigger":"Stop","action":"agent","content":"x"}`))
	if !strings.HasSuffix(got, "/.kiro/hooks/hook.json") {
		t.Errorf("path = %q, want hook.json", got)
	}
}

func TestHandleKiroDocNew_RefusesBadFields(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantMsg string
	}{
		{"unknown_kind", `{"category":"spec","name":"x"}`, "unknown document kind"},
		{"steering_no_inclusion", `{"category":"steering","name":"a"}`, "Choose agent steering or manual steering."},
		{"steering_empty", `{"category":"steering","name":"  ","inclusion":"always"}`, "Filename cannot be empty"},
		{"steering_slash", `{"category":"steering","name":"../x","inclusion":"always"}`, "A file name cannot contain"},
		{"steering_dot", `{"category":"steering","name":".hidden","inclusion":"always"}`, "A file name cannot contain"},
		{"skill_upper", `{"category":"skill","name":"Bad","description":"d"}`, "Use lowercase letters"},
		{"skill_double_hyphen", `{"category":"skill","name":"a--b","description":"d"}`, "Use lowercase letters"},
		{"skill_no_description", `{"category":"skill","name":"ok","description":" "}`, "Description is required"},
		{"skill_multiline", `{"category":"skill","name":"ok","description":"a\nb"}`, "Description must be a single line."},
		{"prompt_empty", `{"category":"prompt","name":""}`, "Prompt name cannot be empty."},
		{"prompt_space", `{"category":"prompt","name":"a b"}`, "Prompt name can only contain"},
		{"prompt_long", `{"category":"prompt","name":"` + strings.Repeat("a", 51) + `"}`, "Prompt name must be 50 characters or less. Current length: 51 characters."},
		{"agent_empty", `{"category":"agent","name":""}`, "Agent name is required."},
		{"agent_slash", `{"category":"agent","name":"a/b"}`, "Agent name can only contain"},
		{"hook_no_name", `{"category":"hook","name":"","trigger":"Stop","action":"command","content":"x"}`, "Hook name is required."},
		{"hook_bad_trigger", `{"category":"hook","name":"n","trigger":"Manual","action":"command","content":"x"}`, "Choose a trigger."},
		{"hook_no_command", `{"category":"hook","name":"n","trigger":"Stop","action":"command","content":" "}`, "A command is required for the Run Command action."},
		{"hook_no_prompt", `{"category":"hook","name":"n","trigger":"Stop","action":"agent"}`, "Instructions are required for the Ask Kiro action."},
		{"hook_negative_timeout", `{"category":"hook","name":"n","trigger":"Stop","action":"command","content":"x","timeout":-1}`, "Timeout must be 0 seconds or more."},
		{"hook_no_action", `{"category":"hook","name":"n","trigger":"Stop","content":"x"}`, "Choose Ask Kiro or Run Command."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			rec := postKiroDocNew(t, docServer(dir), tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantMsg) {
				t.Errorf("body = %s, want it to carry %q", rec.Body.String(), tc.wantMsg)
			}
			if _, err := os.Stat(filepath.Join(dir, ".kiro")); err == nil {
				t.Errorf("a refused create left a .kiro tree behind")
			}
		})
	}
}

func TestHandleKiroDocNew_LimitsTheFileNameItWrites(t *testing.T) {
	steering := func(name string) string {
		return `{"category":"steering","name":"` + name + `","inclusion":"always"}`
	}
	hook := func(title string) string {
		return `{"category":"hook","name":"` + title + `","trigger":"Stop","action":"agent","content":"x"}`
	}
	cases := []struct {
		name string
		body string
		// rel is the file a fitting name creates; empty means the create is refused.
		rel string
	}{
		{"steering_255_bytes_with_md", steering(strings.Repeat("s", 252)), "steering/" + strings.Repeat("s", 252) + ".md"},
		{"steering_256_bytes_with_md", steering(strings.Repeat("s", 253)), ""},
		{"steering_multibyte_255", steering(strings.Repeat("\u00e9", 126)), "steering/" + strings.Repeat("\u00e9", 126) + ".md"},
		{"steering_multibyte_257", steering(strings.Repeat("\u00e9", 127)), ""},
		{"agent_255_bytes_with_json", `{"category":"agent","name":"` + strings.Repeat("a", 250) + `"}`, "agents/" + strings.Repeat("a", 250) + ".json"},
		{"agent_256_bytes_with_json", `{"category":"agent","name":"` + strings.Repeat("a", 251) + `"}`, ""},
		{"hook_slug_255_bytes", hook(strings.Repeat("h", 250)), "hooks/" + strings.Repeat("h", 250) + ".json"},
		{"hook_slug_256_bytes", hook(strings.Repeat("h", 251)), ""},
		{"hook_long_title_short_slug", hook("Lint" + strings.Repeat("!", 400)), "hooks/lint.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			rec := postKiroDocNew(t, docServer(dir), tc.body)
			if tc.rel != "" {
				if got := createdPath(t, rec); !strings.HasSuffix(got, "/.kiro/"+tc.rel) {
					t.Errorf("path = %q, want it to end in %q", got, tc.rel)
				}
				return
			}
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "the limit is 255 bytes") {
				t.Errorf("status = %d, body %s; want 400 naming the 255-byte limit", rec.Code, rec.Body.String())
			}
			if got := treeOf(t, dir); len(got) != 1 {
				t.Errorf("workspace tree = %v, want the refused create to leave it empty", got)
			}
		})
	}
}

func TestHandleKiroDocNew_HookNumberingStopsOnlyWhenTheNameNoLongerFits(t *testing.T) {
	t.Run("past_999", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, ".kiro/hooks/x.json", "{}")
		for i := 1; i < 1000; i++ {
			writeFile(t, dir, ".kiro/hooks/x-"+strconv.Itoa(i)+".json", "{}")
		}
		got := createdPath(t, postKiroDocNew(t, docServer(dir), `{"category":"hook","name":"x","trigger":"Stop","action":"agent","content":"x"}`))
		if !strings.HasSuffix(got, "/.kiro/hooks/x-1000.json") {
			t.Errorf("path = %q, want x-1000.json", got)
		}
	})
	t.Run("suffix_overflows", func(t *testing.T) {
		dir := t.TempDir()
		slug := strings.Repeat("h", 249)
		writeFile(t, dir, ".kiro/hooks/"+slug+".json", "{}")
		rec := postKiroDocNew(t, docServer(dir), `{"category":"hook","name":"`+slug+`","trigger":"Stop","action":"agent","content":"x"}`)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "a 256-byte file name") {
			t.Errorf("status = %d, body %s; want 400 for the 256-byte -1 name", rec.Code, rec.Body.String())
		}
	})
}

func TestHandleKiroDocNew_ChecksMatchersWithKASsEngine(t *testing.T) {
	requireKASNode(t)
	cases := []struct {
		name    string
		trigger string
		matcher string
		ok      bool
	}{
		{"lookahead", "PostFileSave", `a(?=b)`, true},
		{"lookbehind", "PostFileSave", `(?<=src/)x`, true},
		{"backreference", "PostFileSave", `(a)\\1`, true},
		{"named_backreference", "PostFileSave", `(?<d>a)\\k<d>`, true},
		{"alternation", "PreToolUse", `fs_write|str_replace`, true},
		{"unterminated_group", "PostFileSave", `(`, false},
		{"inline_flag", "PostFileSave", `(?i)readme`, false},
		{"python_named_group", "PostFileSave", `(?P<d>src)`, false},
		{"bare_star_on_a_file_trigger", "PostFileSave", `*`, false},
		{"bare_star_on_a_tool_trigger", "PreToolUse", `*`, true},
		{"glob_on_a_tool_trigger", "PostToolUse", `@mcp/?`, true},
		{"glob_with_regex_syntax", "PreToolUse", `(*`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			body := `{"category":"hook","name":"n","trigger":"` + tc.trigger + `","matcher":"` + tc.matcher +
				`","action":"command","content":"x"}`
			rec := postKiroDocNew(t, docServer(dir), body)
			if tc.ok {
				createdPath(t, rec)
				return
			}
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Matcher must be a valid regular expression.") {
				t.Errorf("matcher %q: status = %d, body %s; want 400", tc.matcher, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandleKiroDocNew_NoRuntimeNoMatcherCheck(t *testing.T) {
	const body = `{"category":"hook","name":"n","trigger":"PostFileSave","matcher":"x","action":"command","content":"x"}`
	for name, node := range map[string]string{"unset": "", "absent": filepath.Join(t.TempDir(), "node")} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			rec := postKiroDocNew(t, docServer(dir, WithKASNode(node)), body)
			if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "Open a chat, then try again.") {
				t.Errorf("status = %d, body %s; want 503", rec.Code, rec.Body.String())
			}
			if got := treeOf(t, dir); len(got) != 1 {
				t.Errorf("workspace tree = %v, want it empty", got)
			}
		})
	}
	t.Run("not_needed_without_a_matcher", func(t *testing.T) {
		createdPath(t, postKiroDocNew(t, docServer(t.TempDir(), WithKASNode("")),
			`{"category":"hook","name":"n","trigger":"PostFileSave","action":"command","content":"x"}`))
	})
}

func TestKASRegExp_RunsWithoutTheServersNodeOptions(t *testing.T) {
	requireKASNode(t)
	t.Setenv("NODE_OPTIONS", "--require=/nonexistent/preload.js")
	ok, err := kasRegExp{node: testKASNode()}.compiles(t.Context(), "a+")
	if err != nil || !ok {
		t.Errorf("compiles(%q) = %v, %v; want true, nil", "a+", ok, err)
	}
}

// fakeNode writes an executable under ForkLock: a concurrent fork would otherwise hold its
// write descriptor and fail the exec with ETXTBSY (https://go.dev/issue/22315).
func fakeNode(t *testing.T, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "node")
	syscall.ForkLock.RLock()
	err := os.WriteFile(p, []byte("#!/bin/sh\n"+script+"\n"), 0o755)
	syscall.ForkLock.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestKASRegExp_AnswersOnlyAnExplicitVerdict(t *testing.T) {
	for name, script := range map[string]string{
		"other_output": `printf yes`,
		"no_output":    `exit 0`,
		"crash":        `printf valid; exit 1`,
	} {
		t.Run(name, func(t *testing.T) {
			ok, err := kasRegExp{node: fakeNode(t, script)}.compiles(t.Context(), "a")
			if ok || err == nil {
				t.Errorf("compiles with a runtime that printed no verdict = %v, %v; want false and an error", ok, err)
			}
		})
	}
	t.Run("a_runtime_fault_is_a_500", func(t *testing.T) {
		rec := postKiroDocNew(t, docServer(t.TempDir(), WithKASNode(fakeNode(t, `exit 3`))),
			`{"category":"hook","name":"n","trigger":"PostFileSave","matcher":"x","action":"command","content":"x"}`)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500; body %s", rec.Code, rec.Body.String())
		}
	})
}

func TestHandleKiroDocNew_RefusesALinkOutOfTheTree(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".kiro"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, ".kiro", "steering")); err != nil {
		t.Fatal(err)
	}
	rec := postKiroDocNew(t, docServer(dir), `{"category":"steering","name":"a","inclusion":"always"}`)
	if rec.Code == http.StatusCreated {
		t.Fatalf("status = 201, want a refusal; body %s", rec.Body.String())
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Errorf("outside dir entries = %v (err %v), want none", entries, err)
	}
}

func TestHandleKiroDocNew_RefusesAKiroDirReachingSensitiveStorage(t *testing.T) {
	t.Run("kiro_links_into_home", func(t *testing.T) {
		dir := t.TempDir()
		cfg := t.TempDir()
		home := filepath.Join(cfg, "home")
		if err := os.MkdirAll(home, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(home, filepath.Join(dir, ".kiro")); err != nil {
			t.Fatal(err)
		}
		srv := docServer(dir, WithSensitive(filebrowse.NewSensitive(cfg)))
		rec := postKiroDocNew(t, srv, `{"category":"steering","name":"a","inclusion":"always"}`)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body %s", rec.Code, rec.Body.String())
		}
		if got := treeOf(t, home); len(got) != 1 {
			t.Errorf("home tree = %v, want it empty", got)
		}
	})
	t.Run("category_links_to_sensitive_storage_inside_kiro", func(t *testing.T) {
		dir := t.TempDir()
		cfg := filepath.Join(dir, ".kiro", "cfg")
		secret := filepath.Join(cfg, "home", "secret")
		if err := os.MkdirAll(secret, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(secret, filepath.Join(dir, ".kiro", "steering")); err != nil {
			t.Fatal(err)
		}
		srv := docServer(dir, WithSensitive(filebrowse.NewSensitive(cfg)))
		rec := postKiroDocNew(t, srv, `{"category":"steering","name":"a","inclusion":"always"}`)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body %s", rec.Code, rec.Body.String())
		}
		if got := treeOf(t, secret); len(got) != 1 {
			t.Errorf("secret tree = %v, want it empty", got)
		}
	})
}

func TestHandleKiroDocNew_FailedCreateLeavesNothingBehind(t *testing.T) {
	failWrites := func(t *testing.T) {
		t.Helper()
		orig := writeDocData
		writeDocData = func(*os.File, []byte) error { return errors.New("disk full") }
		t.Cleanup(func() { writeDocData = orig })
	}
	t.Run("into_a_fresh_workspace", func(t *testing.T) {
		failWrites(t)
		dir := t.TempDir()
		rec := postKiroDocNew(t, docServer(dir), `{"category":"skill","name":"pr-review","description":"d"}`)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500; body %s", rec.Code, rec.Body.String())
		}
		if got := treeOf(t, dir); len(got) != 1 {
			t.Errorf("workspace tree = %v, want it empty", got)
		}
	})
	t.Run("beside_existing_documents", func(t *testing.T) {
		failWrites(t)
		dir := t.TempDir()
		writeFile(t, dir, ".kiro/skills/other/SKILL.md", "---\nname: other\ndescription: d\n---\n")
		srv := docServer(dir)
		before := srv.collectKiroDocs(t.Context()).Docs
		rec := postKiroDocNew(t, srv, `{"category":"skill","name":"pr-review","description":"d"}`)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500; body %s", rec.Code, rec.Body.String())
		}
		after := srv.collectKiroDocs(t.Context()).Docs
		if len(after) != len(before) || len(after) != 1 || after[0].Path != before[0].Path {
			t.Errorf("inventory = %+v, want it unchanged from %+v", after, before)
		}
		if _, err := os.Stat(filepath.Join(dir, ".kiro", "skills", "pr-review")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the skill directory survived the failed create: %v", err)
		}
		writeDocData = func(f *os.File, b []byte) error { _, err := f.Write(b); return err }
		createdPath(t, postKiroDocNew(t, srv, `{"category":"skill","name":"pr-review","description":"d"}`))
	})
}

func TestHandleKiroDocNew_MethodGuard(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/workspace/kiro-docs/new", nil)
	rec := httptest.NewRecorder()
	docServer(t.TempDir()).routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET returned %d, want 405", rec.Code)
	}
}

func skillBody(t *testing.T, name, desc string) string {
	t.Helper()
	body, err := json.Marshal(kiroDocNewRequest{Category: catSkill, Name: name, Description: desc})
	if err != nil {
		t.Fatalf("Setup: encode the request: %v", err)
	}
	return string(body)
}

func TestHandleKiroDocNew_SkillFrontMatterReadsBackAsStrings(t *testing.T) {
	descs := []string{
		".nan", ".NaN", ".NAN", ".inf", "-.Inf", "+.INF", "yes", "no", "on", "null", "~", "0x1", "0o7",
		"1e3", "42", "true", "False", "Use when: reviewing", "it's # not a comment", "- starts a list",
		`say "hi" \ back`, "{a: b}", "&anchor *alias", "a\u2028b", "a\ufeffb", "a\ufffeb", "a\uffffb",
	}
	for i, desc := range descs {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			dir := t.TempDir()
			createdPath(t, postKiroDocNew(t, docServer(dir), skillBody(t, "s", desc)))
			file := readCreated(t, dir, "skills/s/SKILL.md")
			matter, ok := strings.CutPrefix(file, "---\n")
			matter, _, found := strings.Cut(matter, "---\n")
			if !ok || !found {
				t.Fatalf("description %q: SKILL.md = %q, want a front-matter block", desc, file)
			}
			var fields map[string]any
			if err := yaml.Unmarshal([]byte(matter), &fields); err != nil {
				t.Fatalf("description %q: front matter %q does not load: %v", desc, matter, err)
			}
			if got := fields["name"]; got != "s" {
				t.Errorf("description %q: name loads as %#v, want the string \"s\"", desc, got)
			}
			if got := fields["description"]; got != desc {
				t.Errorf("description loads as %#v, want the string %q (front matter %q)", got, desc, matter)
			}
		})
	}
}

func TestHandleKiroDocNew_SkillDescriptionFitsAt1024UTF16Units(t *testing.T) {
	for name, desc := range map[string]string{
		"bmp":    strings.Repeat("a", 1024),
		"astral": strings.Repeat("\U0001F600", 512),
	} {
		t.Run(name, func(t *testing.T) {
			createdPath(t, postKiroDocNew(t, docServer(t.TempDir()), skillBody(t, "s", desc)))
		})
	}
}

func TestHandleKiroDocNew_SkillDescriptionPast1024UTF16UnitsIsRefused(t *testing.T) {
	for name, desc := range map[string]string{
		"bmp":            strings.Repeat("a", 1025),
		"astral":         strings.Repeat("\U0001F600", 513),
		"astral_and_bmp": strings.Repeat("\U0001F600", 512) + "a",
	} {
		t.Run(name, func(t *testing.T) {
			rec := postKiroDocNew(t, docServer(t.TempDir()), skillBody(t, "s", desc))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "Description must be 1,024 characters or less.") {
				t.Errorf("body = %s, want the length refusal", rec.Body.String())
			}
		})
	}
}
