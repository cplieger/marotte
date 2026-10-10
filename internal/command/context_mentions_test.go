package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"

	"github.com/cplieger/marotte/internal/marotte"
)

func mentionText(t *testing.T, blocks []map[string]any, token string) string {
	t.Helper()
	for _, b := range blocks {
		res, ok := b[keyResource].(map[string]any)
		if !ok || res["uri"] != token {
			continue
		}
		if res["mimeType"] != "text/plain" {
			t.Fatalf("block %s mimeType = %v, want text/plain", token, res["mimeType"])
		}
		text, _ := res[keyText].(string)
		return text
	}
	t.Fatalf("no resource block for %s in %v", token, blocks)
	return ""
}

func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBuildPromptBlocks_MentionResolvesAFileIntoAResourceBlock(t *testing.T) {
	ws := testWorkspace(t)
	writeFile(t, ws.Dir, "src/a.go", "package a\n")
	text := "look at #[[file:src/a.go]] please"

	blocks := BuildPromptBlocks(t.Context(), text, nil, 0, ws, nil)

	if got := blocks[0][keyText]; got != text {
		t.Errorf("text block = %q, want the prompt unchanged %q", got, text)
	}
	want := "File: src/a.go\n```\npackage a\n```"
	if got := mentionText(t, blocks, "#[[file:src/a.go]]"); got != want {
		t.Errorf("file mention = %q, want %q", got, want)
	}
}

func TestBuildPromptBlocks_MentionLineRangeSendsOnlyThoseLines(t *testing.T) {
	ws := testWorkspace(t)
	writeFile(t, ws.Dir, "f.txt", "one\ntwo\nthree\nfour\n")
	token := "#[[file:f.txt:2-3]]"

	got := mentionText(t, BuildPromptBlocks(t.Context(), token, nil, 0, ws, nil), token)

	want := "File: f.txt (lines 2-3)\n```\ntwo\nthree\n```"
	if got != want {
		t.Errorf("ranged mention = %q, want %q", got, want)
	}
}

func TestBuildPromptBlocks_MentionOutsideTheWorkspaceSendsAReason(t *testing.T) {
	ws := testWorkspace(t)
	outside := t.TempDir()
	writeFile(t, outside, "secret.txt", "secret")
	if err := os.Symlink(outside, filepath.Join(ws.Dir, "link")); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"#[[file:../secret.txt]]", "#[[file:link/secret.txt]]"} {
		got := mentionText(t, BuildPromptBlocks(t.Context(), token, nil, 0, ws, nil), token)
		if strings.Contains(got, "secret") && !strings.Contains(got, "Unresolved") {
			t.Errorf("%s leaked the outside file: %q", token, got)
		}
		if !strings.HasPrefix(got, "[Unresolved context reference: "+token) {
			t.Errorf("%s = %q, want an unresolved reason", token, got)
		}
	}
}

const marker50k = "\n[truncated at 50000 characters]"

func TestBuildPromptBlocks_MentionCapsEachItemAt50000CharactersWithTheFenceClosed(t *testing.T) {
	ws := testWorkspace(t)
	writeFile(t, ws.Dir, "big.txt", strings.Repeat("é", maxMentionChars+10))
	token := "#[[file:big.txt]]"

	got := mentionText(t, BuildPromptBlocks(t.Context(), token, nil, 0, ws, nil), token)

	if n := utf8.RuneCountInString(got); n != maxMentionChars {
		t.Errorf("capped mention is %d runes, want exactly %d", n, maxMentionChars)
	}
	if !strings.HasPrefix(got, "File: big.txt\n```\n") || !strings.HasSuffix(got, "é\n```"+marker50k) {
		t.Errorf("capped mention does not close its fence before the marker: head %q tail %q", got[:20], got[len(got)-50:])
	}
}

func TestBuildPromptBlocks_MentionFenceStaysBoundedByAPathologicalBody(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{name: "a body of backticks", body: strings.Repeat("`", maxMentionReadBytes), want: "File: t.txt\n```\n\n```" + marker50k},
		{name: "a run at the fence bound is cut", body: "abc" + strings.Repeat("`", maxFenceLen+8) + "def", want: "File: t.txt\n```\nabc\n```" + marker50k},
		{name: "a trailing run is fenced", body: "a`````", want: "File: t.txt\n``````\na`````\n``````"},
		{name: "a run under the bound is fenced", body: "a" + strings.Repeat("`", maxFenceLen-1) + "b", want: "File: t.txt\n" + strings.Repeat("`", maxFenceLen) + "\na" + strings.Repeat("`", maxFenceLen-1) + "b\n" + strings.Repeat("`", maxFenceLen)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ws := testWorkspace(t)
			writeFile(t, ws.Dir, "t.txt", tc.body)
			token := "#[[file:t.txt]]"

			got := mentionText(t, BuildPromptBlocks(t.Context(), token, nil, 0, ws, nil), token)

			if n := utf8.RuneCountInString(got); n > maxMentionChars {
				t.Errorf("mention is %d runes, want at most %d", n, maxMentionChars)
			}
			if got != tc.want {
				t.Errorf("mention = %.120q, want %.120q", got, tc.want)
			}
		})
	}
}

func TestBuildPromptBlocks_MentionLineRangePastTheEndSendsAReason(t *testing.T) {
	ws := testWorkspace(t)
	writeFile(t, ws.Dir, "f.txt", "one\ntwo\nthree\n")
	token := "#[[file:f.txt:5-6]]"

	got := mentionText(t, BuildPromptBlocks(t.Context(), token, nil, 0, ws, nil), token)

	if want := "[Unresolved context reference: " + token + " (line range starts past the end of the file)]"; got != want {
		t.Errorf("past-EOF range = %q, want %q", got, want)
	}
}

type countingReader struct {
	left, read int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	if r.left <= 0 {
		return 0, io.EOF
	}
	n := min(int64(len(p)), r.left)
	for i := range n {
		p[i] = 'x'
		if (r.read+i)%100 == 99 {
			p[i] = '\n'
		}
	}
	r.left -= n
	r.read += n
	return int(n), nil
}

func TestReadLineRange_BoundsTheBytesScannedToReachTheStart(t *testing.T) {
	r := &countingReader{left: 2 * maxLineRangeScanBytes}

	_, err := readLineRange(t.Context(), r, 1<<30, 1<<30)

	if !errors.Is(err, errRangePastLimit) {
		t.Errorf("readLineRange(start past the cap) error = %v, want %v", err, errRangePastLimit)
	}
	if r.read > maxLineRangeScanBytes {
		t.Errorf("readLineRange read %d bytes, want at most %d", r.read, maxLineRangeScanBytes)
	}
}

func TestReadLineRange_StopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	out, err := readLineRange(ctx, strings.NewReader("a\nb\n"), 1, 2)

	if !errors.Is(err, context.Canceled) || out != nil {
		t.Errorf("readLineRange(cancelled) = (%q, %v), want (nil, context.Canceled)", out, err)
	}
}

func TestBuildPromptBlocks_MentionGitCapKeepsEveryFenceClosed(t *testing.T) {
	ws := testWorkspace(t)
	repo := filepath.Join(ws.Dir, "repo")
	writeFile(t, repo, "a.txt", "one\n")
	writeFile(t, repo, "b.txt", "two\n")
	gitInit(t, repo)
	writeFile(t, repo, "a.txt", strings.Repeat("staged line\n", 6000))
	if out, err := exec.Command("git", "-C", repo, "add", "a.txt").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}
	writeFile(t, repo, "b.txt", "two unstaged\n")
	token := "#[[git:repo]]"

	got := mentionText(t, BuildPromptBlocks(t.Context(), token, nil, 0, ws, nil), token)

	if n := utf8.RuneCountInString(got); n > maxMentionChars {
		t.Errorf("capped git mention is %d runes, want at most %d", n, maxMentionChars)
	}
	if !strings.HasSuffix(got, marker50k) {
		t.Fatalf("capped git mention has no marker: tail %q", got[len(got)-60:])
	}
	if opens, closes := strings.Count(got, "```diff\n"), strings.Count(got, "\n```\n"); opens != 2 || closes != 2 {
		t.Errorf("git mention has %d fence opens and %d closes, want 2 of each: %q", opens, closes, got[len(got)-200:])
	}
}

func TestBuildPromptBlocks_MentionOverflowNoteStaysWithinTheCap(t *testing.T) {
	ws := testWorkspace(t)
	var text strings.Builder
	for i := range maxMentionsPerPrompt + 1 {
		writeFile(t, ws.Dir, fmt.Sprintf("f%d.txt", i), strings.Repeat("x", maxMentionChars))
		fmt.Fprintf(&text, "#[[file:f%d.txt]] ", i)
	}

	last := mentionText(t, BuildPromptBlocks(t.Context(), text.String(), nil, 0, ws, nil), "#[[file:f31.txt]]")

	if n := utf8.RuneCountInString(last); n > maxMentionChars {
		t.Errorf("last mention with the overflow note is %d runes, want at most %d", n, maxMentionChars)
	}
	if !strings.Contains(last, "1 more context references were not resolved") {
		t.Errorf("last mention lost the overflow note: tail %q", last[len(last)-120:])
	}
}

func TestBuildPromptBlocks_MentionFileQueryNamesEveryFileAsWritten(t *testing.T) {
	var fx struct {
		Cases []struct {
			Path  string `json:"path"`
			Range string `json:"range"`
			Query string `json:"query"`
		} `json:"cases"`
	}
	raw, err := os.ReadFile("testdata/file_query.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	for _, c := range fx.Cases {
		t.Run(strings.ReplaceAll(c.Query, "/", "_"), func(t *testing.T) {
			ws := testWorkspace(t)
			writeFile(t, ws.Dir, c.Path, "one\ntwo\nthree\n")
			token := "#[[file:" + c.Query + "]]"

			got := mentionText(t, BuildPromptBlocks(t.Context(), token, nil, 0, ws, nil), token)

			want := "File: " + c.Path + "\n```\none\ntwo\nthree\n```"
			if c.Range != "" {
				lo, hi, _ := strings.Cut(c.Range, "-")
				want = fmt.Sprintf("File: %s (lines %s-%s)\n```\n%s\n```", c.Path, lo, hi,
					strings.Join([]string{"one", "two", "three"}[atoi(t, lo)-1:atoi(t, hi)], "\n"))
			}
			if got != want {
				t.Errorf("mention of %q (range %q) as %q = %q, want %q", c.Path, c.Range, c.Query, got, want)
			}
		})
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func gitInit(t *testing.T, repo string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "."},
		{"-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-qm", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
}

func TestBuildPromptBlocks_MentionsResolveAtMost32DistinctReferences(t *testing.T) {
	ws := testWorkspace(t)
	var text strings.Builder
	for i := range maxMentionsPerPrompt + 3 {
		writeFile(t, ws.Dir, fmt.Sprintf("f%d.txt", i), "x")
		fmt.Fprintf(&text, "#[[file:f%d.txt]] ", i)
	}
	text.WriteString("#[[file:f0.txt]]")

	blocks := BuildPromptBlocks(t.Context(), text.String(), nil, 0, ws, nil)

	if got := len(blocks) - 1; got != maxMentionsPerPrompt {
		t.Fatalf("resource blocks = %d, want %d", got, maxMentionsPerPrompt)
	}
	last := mentionText(t, blocks, "#[[file:f31.txt]]")
	if !strings.Contains(last, "3 more context references were not resolved") {
		t.Errorf("last mention = %q, want the overflow note", last)
	}
}

func TestBuildPromptBlocks_MentionFolderListsEntriesSorted(t *testing.T) {
	ws := testWorkspace(t)
	writeFile(t, ws.Dir, "pkg/b.go", "")
	writeFile(t, ws.Dir, "pkg/a.go", "")
	writeFile(t, ws.Dir, "pkg/sub/c.go", "")
	token := "#[[folder:pkg]]"

	got := mentionText(t, BuildPromptBlocks(t.Context(), token, nil, 0, ws, nil), token)

	if want := "Contents of pkg:\na.go\nb.go\nsub/\n"; got != want {
		t.Errorf("folder mention = %q, want %q", got, want)
	}
}

func TestBuildPromptBlocks_MentionSteeringRefusesAPathOutsideSteering(t *testing.T) {
	ws := testWorkspace(t)
	writeFile(t, ws.Dir, ".kiro/steering/style.md", "Use tabs.\n")
	writeFile(t, ws.Dir, "notes.md", "not steering")
	ok := "#[[steering:.kiro/steering/style.md]]"
	bad := "#[[steering:notes.md]]"

	blocks := BuildPromptBlocks(t.Context(), ok+" "+bad, nil, 0, ws, nil)

	if got, want := mentionText(t, blocks, ok), "## Included Rules (workspace:.kiro/steering/style.md)\n\nUse tabs.\n"; got != want {
		t.Errorf("steering mention = %q, want %q", got, want)
	}
	if got := mentionText(t, blocks, bad); !strings.Contains(got, "not a steering document") {
		t.Errorf("non-steering mention = %q, want a refusal", got)
	}
}

func TestBuildPromptBlocks_MentionSpecCarriesItsDocumentsInOrder(t *testing.T) {
	ws := testWorkspace(t)
	writeFile(t, ws.Dir, "repo/.kiro/specs/login/tasks.md", "- [ ] 1. task\n")
	writeFile(t, ws.Dir, "repo/.kiro/specs/login/requirements.md", "Req.\n")
	token := "#[[spec:repo/.kiro/specs/login]]"

	got := mentionText(t, BuildPromptBlocks(t.Context(), token, nil, 0, ws, nil), token)

	want := "# Spec: login\n\n## requirements.md\n\nReq.\n\n## tasks.md\n\n- [ ] 1. task\n"
	if got != want {
		t.Errorf("spec mention = %q, want %q", got, want)
	}
}

// mcpPool is one bridge's MCP pool: it answers getResource for its own servers.
type mcpPool struct {
	contents map[string]string
	calls    []map[string]any
}

func (p *mcpPool) Call(_ context.Context, method string, params any) (*marotte.RPCResponse, error) {
	m, _ := params.(map[string]any)
	p.calls = append(p.calls, m)
	if method != marotte.MethodMCPGetResource {
		return nil, fmt.Errorf("unexpected method %s", method)
	}
	server, _ := m["serverName"].(string)
	result, ok := p.contents[server]
	if !ok {
		return nil, &marotte.RPCError{Code: -32602, Message: "unknown MCP server " + server}
	}
	return &marotte.RPCResponse{Result: json.RawMessage(result)}, nil
}

func (p *mcpPool) CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error) {
	resp, err := p.Call(ctx, method, params)
	return resp, 0, err
}

func TestBuildPromptBlocks_MentionMCPReadsTheResourceText(t *testing.T) {
	ws := testWorkspace(t)
	pool := &mcpPool{contents: map[string]string{
		"github": `{"contents":[{"uri":"gh://issues/7","text":"Issue body"},{"uri":"x","blob":"AA==","mimeType":"image/png"}]}`,
	}}
	token := "#[[mcp:github:gh://issues/7]]"

	got := mentionText(t, BuildPromptBlocks(t.Context(), token, nil, 0, ws, pool), token)

	if len(pool.calls) != 1 || pool.calls[0]["serverName"] != "github" || pool.calls[0]["uri"] != "gh://issues/7" {
		t.Errorf("getResource calls = %v, want one {github, gh://issues/7}", pool.calls)
	}
	want := "MCP resource gh://issues/7 from github:\n\nIssue body\n\n[binary content (image/png) omitted]\n"
	if got != want {
		t.Errorf("mcp mention = %q, want %q", got, want)
	}
}

func TestBuildPromptBlocks_MentionMCPReadsTheDestinationBridgesPool(t *testing.T) {
	ws := testWorkspace(t)
	chatA := &mcpPool{contents: map[string]string{"docs": `{"contents":[{"uri":"d://x","text":"pool A"}]}`}}
	chatB := &mcpPool{contents: map[string]string{
		"docs":  `{"contents":[{"uri":"d://x","text":"pool B"}]}`,
		"extra": `{"contents":[{"uri":"e://x","text":"only B"}]}`,
	}}
	token := "#[[mcp:docs:d://x]]"

	if got := mentionText(t, BuildPromptBlocks(t.Context(), token, nil, 0, ws, chatA), token); !strings.Contains(got, "pool A") {
		t.Errorf("mention sent on chat A's bridge = %q, want chat A's resource", got)
	}
	if got := mentionText(t, BuildPromptBlocks(t.Context(), token, nil, 0, ws, chatB), token); !strings.Contains(got, "pool B") {
		t.Errorf("mention sent on chat B's bridge = %q, want chat B's resource", got)
	}
	extra := "#[[mcp:extra:e://x]]"
	got := mentionText(t, BuildPromptBlocks(t.Context(), extra, nil, 0, ws, chatA), extra)
	if want := "[Unresolved context reference: " + extra + " (the MCP server could not be read)]"; got != want {
		t.Errorf("server only chat B knows, sent on chat A = %q, want %q", got, want)
	}
}

func TestBuildPromptBlocks_MentionMCPWithoutABridgeSendsAReason(t *testing.T) {
	token := "#[[mcp:github:gh://issues/7]]"

	got := mentionText(t, BuildPromptBlocks(t.Context(), token, nil, 0, testWorkspace(t), nil), token)

	if want := "[Unresolved context reference: " + token + " (MCP is not available)]"; got != want {
		t.Errorf("mcp mention with no bridge = %q, want %q", got, want)
	}
}

func TestMentionMCP_CapsAnOversizedResourceBeforeItIsKept(t *testing.T) {
	body := strings.Repeat("é", 1<<20)
	pool := &mcpPool{contents: map[string]string{
		"big": fmt.Sprintf(`{"contents":[{"uri":"b://x","text":%q},{"uri":"b://y","text":%q}]}`, body, body),
	}}

	parts, err := mentionMCP(t.Context(), "big:b://x", pool)
	if err != nil {
		t.Fatalf("mentionMCP(big) error = %v", err)
	}
	text := parts[0].text
	if len(text) > maxMentionReadBytes || !utf8.ValidString(text) {
		t.Errorf("mentionMCP(big) kept %d bytes (valid UTF-8 %v), want at most %d on a rune boundary",
			len(text), utf8.ValidString(text), maxMentionReadBytes)
	}
	if n := utf8.RuneCountInString(text); n <= maxMentionChars {
		t.Errorf("mentionMCP(big) kept %d runes, want more than %d so the render marks the cut", n, maxMentionChars)
	}
}

func TestBuildPromptBlocks_SeveralOversizedMCPResourcesEachFitTheItemCap(t *testing.T) {
	body := strings.Repeat("x", 4<<20)
	result := fmt.Sprintf(`{"contents":[{"uri":"r","text":%q}]}`, body)
	pool := &mcpPool{contents: map[string]string{"a": result, "b": result, "c": result}}
	tokens := []string{"#[[mcp:a:r]]", "#[[mcp:b:r]]", "#[[mcp:c:r]]"}

	blocks := BuildPromptBlocks(t.Context(), strings.Join(tokens, " "), nil, 0, testWorkspace(t), pool)

	for _, token := range tokens {
		got := mentionText(t, blocks, token)
		if n := utf8.RuneCountInString(got); n > maxMentionChars || !strings.HasSuffix(got, truncationMarker) {
			t.Errorf("block %s = %d runes ending %q, want at most %d ending with the truncation marker",
				token, n, got[max(len(got)-40, 0):], maxMentionChars)
		}
	}
}

// stalledPool never answers: each read waits until its context ends.
type stalledPool struct{ calls int }

func (p *stalledPool) Call(ctx context.Context, _ string, _ any) (*marotte.RPCResponse, error) {
	p.calls++
	<-ctx.Done()
	return nil, ctx.Err()
}

func (p *stalledPool) CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error) {
	resp, err := p.Call(ctx, method, params)
	return resp, 0, err
}

func TestBuildPromptBlocks_StalledMCPReadsShareOnePromptBudget(t *testing.T) {
	ws := testWorkspace(t)
	synctest.Test(t, func(t *testing.T) {
		pool := &stalledPool{}
		tokens := []string{"#[[mcp:a:r1]]", "#[[mcp:b:r2]]", "#[[mcp:c:r3]]", "#[[mcp:d:r4]]"}
		start := time.Now()

		blocks := BuildPromptBlocks(t.Context(), strings.Join(tokens, " "), nil, 0, ws, pool)

		if elapsed := time.Since(start); elapsed != mentionMCPBudget {
			t.Errorf("4 stalled MCP reads took %s, want one shared budget of %s", elapsed, mentionMCPBudget)
		}
		reason := fmt.Sprintf("this prompt's MCP reads took longer than %s", mentionMCPBudget)
		var order []string
		for _, b := range blocks {
			if res, ok := b[keyResource].(map[string]any); ok {
				order = append(order, res[keyURI].(string))
			}
		}
		if strings.Join(order, " ") != strings.Join(tokens, " ") {
			t.Errorf("resource block order = %v, want the token order %v", order, tokens)
		}
		for _, token := range tokens {
			if got, want := mentionText(t, blocks, token), "[Unresolved context reference: "+token+" ("+reason+")]"; got != want {
				t.Errorf("stalled mention %s = %q, want %q", token, got, want)
			}
		}
	})
}

func TestBuildPromptBlocks_MentionGitSendsStagedAndUnstagedDiffs(t *testing.T) {
	ws := testWorkspace(t)
	repo := filepath.Join(ws.Dir, "repo")
	writeFile(t, repo, "a.txt", "one\n")
	writeFile(t, repo, "b.txt", "two\n")
	gitInit(t, repo)
	writeFile(t, repo, "a.txt", "one staged\n")
	if out, err := exec.Command("git", "-C", repo, "add", "a.txt").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}
	writeFile(t, repo, "b.txt", "two unstaged\n")
	token := "#[[git:repo]]"

	got := mentionText(t, BuildPromptBlocks(t.Context(), token, nil, 0, ws, nil), token)

	staged, unstaged, ok := strings.Cut(got, "\nUnstaged:")
	if !ok {
		t.Fatalf("git mention has no unstaged section: %q", got)
	}
	if !strings.Contains(staged, "+one staged") || strings.Contains(staged, "two unstaged") {
		t.Errorf("staged section = %q, want only the staged change", staged)
	}
	if !strings.Contains(unstaged, "+two unstaged") || strings.Contains(unstaged, "one staged") {
		t.Errorf("unstaged section = %q, want only the unstaged change", unstaged)
	}
}

func TestBuildPromptBlocks_MentionGrammarRejectsLineBreaksAndMalformedTokens(t *testing.T) {
	ws := testWorkspace(t)
	writeFile(t, ws.Dir, "a\nb.txt", "x")
	writeFile(t, ws.Dir, "a\rb.txt", "x")
	for _, text := range []string{
		"#[[file:a\nb.txt]]",
		"#[[file:a\rb.txt]]",
		"#[[File:a.txt]]",
		"#[[file:a.txt]",
		"#[file:a.txt]]",
		"#[[:a.txt]]",
	} {
		if blocks := BuildPromptBlocks(t.Context(), text, nil, 0, ws, nil); len(blocks) != 1 {
			t.Errorf("BuildPromptBlocks(%q) = %d blocks, want only the text block", text, len(blocks))
		}
	}
}

func TestBuildPromptBlocks_MentionUnknownProviderSendsAReason(t *testing.T) {
	token := "#[[problems:1]]"

	got := mentionText(t, BuildPromptBlocks(t.Context(), token, nil, 0, testWorkspace(t), nil), token)

	if !strings.Contains(got, `unknown provider "problems"`) {
		t.Errorf("unknown provider mention = %q", got)
	}
}
