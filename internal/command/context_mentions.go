package command

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/cplieger/marotte/internal/git"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/spec"
	"github.com/cplieger/marotte/internal/workspace"
)

const (
	// maxMentionChars caps the text one `#` reference contributes, in runes,
	// matching KAS's own cap on a steering reference.
	maxMentionChars = 50_000
	// maxMentionsPerPrompt caps how many distinct references one prompt resolves,
	// matching KAS's cap on steering references per prompt.
	maxMentionsPerPrompt = 32
	maxFolderEntries     = 1000
	// maxMentionReadBytes bounds the bytes read for one item: four bytes per
	// rune covers maxMentionChars of any text.
	maxMentionReadBytes = 4*maxMentionChars + 4
	// maxLineRangeScanBytes bounds what a line range scans to reach its start,
	// the per-attachment file cap.
	maxLineRangeScanBytes = MaxDocumentBytes
	// A body holding a backtick run this long is cut before that run, so the fence never grows with
	// the body.
	maxFenceLen = 32
)

// mentionMCPBudget bounds every MCP read of one prompt together, from the first
// one, so stalled servers cannot multiply the wait before session/prompt.
var mentionMCPBudget = 30 * time.Second

var (
	errRangePastEOF   = errors.New("line range starts past the end of the file")
	errRangePastLimit = fmt.Errorf("line range starts past the first %d MiB of the file", maxLineRangeScanBytes>>20)
)

// mentionRe is the token grammar, identical to TOKEN_RE in
// static-src/context-mentions.ts: a query holds no `]` and no line break, so the
// client's mid-turn hold and this resolver agree on what is a token, and a
// failure reason naming the token stays one line.
var mentionRe = regexp.MustCompile(`#\[\[([a-z]+):([^\]\r\n]*)\]\]`)

// lineRangeRe and the trailing-colon rule in splitFileQuery are fileQuery in
// static-src/context-mentions.ts: a path that itself ends in ":" or ":a-b" is
// written with one more ":", so every file name has a whole-file query.
var lineRangeRe = regexp.MustCompile(`^(.+):(\d+)-(\d+)$`)

// A reference that fails to resolve still sends a block carrying its reason, so the prompt never
// fails on one. Each item is rendered as soon as it resolves, so at most one unbounded body is held
// at a time.
func mentionBlocks(ctx context.Context, text string, ws Workspace, mcp bridgeCaller) []map[string]any {
	matches := mentionRe.FindAllStringSubmatch(text, -1)
	var resolve [][]string
	seen := make(map[string]struct{}, len(matches))
	skipped := 0
	for _, m := range matches {
		if _, dup := seen[m[0]]; dup {
			continue
		}
		seen[m[0]] = struct{}{}
		if len(resolve) == maxMentionsPerPrompt {
			skipped++
			continue
		}
		resolve = append(resolve, m)
	}
	var budget mcpBudget
	defer budget.release()
	blocks := make([]map[string]any, 0, len(resolve))
	for i, m := range resolve {
		if ctx.Err() != nil {
			break
		}
		rctx := ctx
		if m[1] == "mcp" {
			rctx = budget.context(ctx)
		}
		parts, err := resolveMention(rctx, m[1], m[2], ws, mcp)
		if err != nil {
			parts = []mentionPart{flexText(fmt.Sprintf("[Unresolved context reference: %s (%s)]", m[0], err))}
		}
		if skipped > 0 && i == len(resolve)-1 {
			parts = append(parts, fixedText(fmt.Sprintf(
				"\n[%d more context references were not resolved: a prompt resolves at most %d]",
				skipped, maxMentionsPerPrompt,
			)))
		}
		blocks = append(blocks, textResourceBlock(m[0], renderMention(parts)))
	}
	return blocks
}

// mcpBudget starts the prompt-wide MCP deadline at the prompt's first MCP read.
type mcpBudget struct {
	ctx  context.Context
	stop context.CancelFunc
}

func (b *mcpBudget) context(parent context.Context) context.Context {
	if b.ctx == nil {
		b.ctx, b.stop = context.WithTimeout(parent, mentionMCPBudget)
	}
	return b.ctx
}

func (b *mcpBudget) release() {
	if b.stop != nil {
		b.stop()
	}
}

const (
	keyResource = "resource"
	keyText     = "text"
	keyMimeType = "mimeType"
	keyURI      = "uri"
)

func textResourceBlock(uri, text string) map[string]any {
	return map[string]any{
		keyType: keyResource,
		keyResource: map[string]any{
			keyURI:      uri,
			keyMimeType: "text/plain",
			keyText:     text,
		},
	}
}

func resolveMention(ctx context.Context, provider, query string, ws Workspace, mcp bridgeCaller) ([]mentionPart, error) {
	switch provider {
	case "file":
		return mentionFile(ctx, query, ws)
	case "folder":
		return mentionFolder(query, ws)
	case "git":
		return mentionGitDiff(ctx, query, ws)
	case "spec":
		return mentionSpec(ctx, query, ws)
	case "steering":
		return mentionSteering(ctx, query, ws)
	case "mcp":
		return mentionMCP(ctx, query, mcp)
	case "terminal":
		return mentionTerminal(query, ws.Terminal)
	}
	return nil, fmt.Errorf("unknown provider %q", provider)
}

// A fixed piece is written whole; any other piece may be cut to keep the item within
// maxMentionChars, and a fenced one is cut inside its fence, so the fence always closes.
type mentionPart struct {
	text   string
	lang   string
	fenced bool
	fixed  bool
}

func fixedText(s string) mentionPart { return mentionPart{text: s, fixed: true} }

func flexText(s string) mentionPart { return mentionPart{text: s} }

func fencedText(lang, body string) mentionPart {
	return mentionPart{text: strings.TrimSuffix(body, "\n"), lang: lang, fenced: true}
}

var truncationMarker = fmt.Sprintf("\n[truncated at %d characters]", maxMentionChars)

// When they do not fit, or a fenced body had to be cut before an over-long backtick run, the fixed
// pieces, every fence and the truncation marker are reserved first, and the other pieces share what
// is left, earliest first.
func renderMention(parts []mentionPart) string {
	texts, fences, cut := fenceParts(parts)
	reserved, flexible := 0, 0
	for i, p := range parts {
		if p.fenced {
			reserved += 2*len(fences[i]) + len(p.lang) + 2
		}
		if n := utf8.RuneCountInString(texts[i]); p.fixed {
			reserved += n
		} else {
			flexible += n
		}
	}
	room := -1
	if cut || reserved+flexible > maxMentionChars {
		room = max(maxMentionChars-reserved-utf8.RuneCountInString(truncationMarker), 0)
	}
	var b strings.Builder
	for i, p := range parts {
		text := texts[i]
		if !p.fixed && room >= 0 {
			text = cutRunes(text, room)
			room -= utf8.RuneCountInString(text)
		}
		if p.fenced {
			text = fences[i] + p.lang + "\n" + text + "\n" + fences[i]
		}
		b.WriteString(text)
	}
	if room >= 0 {
		b.WriteString(truncationMarker)
	}
	return b.String()
}

// cut reports whether any body was cut.
func fenceParts(parts []mentionPart) (texts, fences []string, cut bool) {
	texts = make([]string, len(parts))
	fences = make([]string, len(parts))
	for i, p := range parts {
		texts[i] = p.text
		if !p.fenced {
			continue
		}
		fence, at := fenceFor(p.text)
		if at >= 0 {
			texts[i], cut = p.text[:at], true
		}
		fences[i] = fence
	}
	return texts, fences, cut
}

func cutRunes(s string, n int) string {
	for i := range s {
		if n == 0 {
			return s[:i]
		}
		n--
	}
	return s
}

// fenceFor returns a fence longer than every backtick run in body, in one pass.
// When a run reaches maxFenceLen it returns that run's offset, where the body
// must be cut, and a fence for the prefix; otherwise the offset is -1.
func fenceFor(body string) (fence string, cutAt int) {
	longest, run := 0, 0
	for i := range len(body) {
		if body[i] != '`' {
			longest, run = max(longest, run), 0
			continue
		}
		run++
		if run == maxFenceLen {
			return strings.Repeat("`", max(3, longest+1)), i + 1 - run
		}
	}
	return strings.Repeat("`", max(3, longest+1, run+1)), -1
}

// confineMention confines to the workspace root alone: uploads are attachments,
// not context references. The query is a file name as written, never trimmed.
func confineMention(query string, ws Workspace) (root, name string, err error) {
	if query == "" || query == "." {
		return ws.Dir, ".", nil
	}
	return workspace.ConfineAnyAbs([]string{ws.Dir}, query)
}

func displayRel(name string) string {
	if name == "." || name == "" {
		return "."
	}
	return filepath.ToSlash(name)
}

func splitFileQuery(query string) (p string, start, end int, err error) {
	m := lineRangeRe.FindStringSubmatch(query)
	if m == nil {
		return strings.TrimSuffix(query, ":"), 0, 0, nil
	}
	start, sErr := strconv.Atoi(m[2])
	end, eErr := strconv.Atoi(m[3])
	if sErr != nil || eErr != nil || start < 1 || end < start {
		return "", 0, 0, errors.New("invalid line range")
	}
	return m[1], start, end, nil
}

func mentionFile(ctx context.Context, query string, ws Workspace) ([]mentionPart, error) {
	p, start, end, err := splitFileQuery(query)
	if err != nil {
		return nil, err
	}
	data, rel, err := readMentionFile(ctx, p, ws, start, end)
	if err != nil {
		return nil, err
	}
	header := "File: " + rel
	if start > 0 {
		header += fmt.Sprintf(" (lines %d-%d)", start, end)
	}
	return []mentionPart{fixedText(header + "\n"), fencedText("", data)}, nil
}

func readMentionFile(ctx context.Context, p string, ws Workspace, start, end int) (text, rel string, err error) {
	root, name, err := confineMention(p, ws)
	if err != nil {
		return "", "", errors.New("outside the workspace")
	}
	f, _, err := openConfined(root, name)
	if err != nil {
		return "", "", openReason(err)
	}
	defer f.Close()
	var data []byte
	if start > 0 {
		data, err = readLineRange(ctx, f, start, end)
	} else {
		data, err = io.ReadAll(io.LimitReader(f, maxMentionReadBytes))
	}
	switch {
	case errors.Is(err, errRangePastEOF), errors.Is(err, errRangePastLimit):
		return "", "", err
	case err != nil:
		return "", "", errors.New("read failed")
	}
	if bytes.IndexByte(data[:min(len(data), 8192)], 0) >= 0 {
		return "", "", errors.New("binary file")
	}
	return strings.ToValidUTF8(string(data), "\uFFFD"), displayRel(name), nil
}

// readLineRange scans at most maxLineRangeScanBytes and stops when ctx ends. A
// range that starts beyond what was scanned is an error, never an empty block.
func readLineRange(ctx context.Context, r io.Reader, start, end int) ([]byte, error) {
	lr := &io.LimitedReader{R: r, N: maxLineRangeScanBytes}
	sc := bufio.NewScanner(lr)
	sc.Buffer(make([]byte, 0, 64*1024), maxMentionReadBytes)
	var out []byte
	n := 0
	for n < end && sc.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n++
		if n < start {
			continue
		}
		out = append(out, sc.Bytes()...)
		out = append(out, '\n')
		if len(out) >= maxMentionReadBytes {
			break
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if n < start {
		if lr.N == 0 {
			return nil, errRangePastLimit
		}
		return nil, errRangePastEOF
	}
	return out, nil
}

func openReason(err error) error {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return errors.New("not found")
	case errors.Is(err, os.ErrPermission):
		return errors.New("permission denied")
	}
	return errors.New("not a readable file")
}

// openMentionDir opens a confined directory and hands back the handle, so a
// caller works in the directory that was checked rather than in whatever its
// pathname names later. O_NONBLOCK so a FIFO at the name cannot block the
// prompt; O_DIRECTORY so a file is refused by the kernel at the open.
func openMentionDir(query string, ws Workspace) (*os.File, string, error) {
	r, name, err := openMentionRoot(query, ws)
	if err != nil {
		return nil, "", err
	}
	defer r.Close()
	d, err := r.OpenFile(name, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, "", dirOpenReason(err)
	}
	return d, displayRel(name), nil
}

func openMentionRoot(query string, ws Workspace) (*os.Root, string, error) {
	root, name, err := confineMention(query, ws)
	if err != nil {
		return nil, "", errors.New("outside the workspace")
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, "", openReason(err)
	}
	return r, name, nil
}

func dirOpenReason(err error) error {
	if errors.Is(err, syscall.ENOTDIR) {
		return errors.New("not a folder")
	}
	return openReason(err)
}

func mentionFolder(query string, ws Workspace) ([]mentionPart, error) {
	d, rel, err := openMentionDir(query, ws)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	entries, err := d.ReadDir(maxFolderEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, errors.New("read failed")
	}
	more := len(entries) > maxFolderEntries
	entries = entries[:min(len(entries), maxFolderEntries)]
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() {
			n += "/"
		}
		names = append(names, n)
	}
	slices.Sort(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n)
		b.WriteByte('\n')
	}
	if more {
		fmt.Fprintf(&b, "[listing stopped at %d entries]\n", maxFolderEntries)
	}
	return []mentionPart{fixedText("Contents of " + rel + ":\n"), flexText(b.String())}, nil
}

func mentionGitDiff(ctx context.Context, query string, ws Workspace) ([]mentionPart, error) {
	r, name, err := openMentionRoot(query, ws)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	staged, unstaged, err := git.WorkingDiff(ctx, r, name, maxMentionReadBytes)
	if err != nil {
		if errors.Is(err, git.ErrNotARepo) {
			return nil, err
		}
		if _, ok := errors.AsType[*os.PathError](err); ok {
			return nil, dirOpenReason(err)
		}
		return nil, err
	}
	parts := []mentionPart{fixedText("Git diff of " + displayRel(name) + ":\n")}
	for _, part := range []struct{ label, diff string }{{"Staged", staged}, {"Unstaged", unstaged}} {
		if strings.TrimSpace(part.diff) == "" {
			parts = append(parts, fixedText("\n"+part.label+": no changes\n"))
			continue
		}
		parts = append(parts,
			fixedText("\n"+part.label+":\n"),
			fencedText("diff", strings.ToValidUTF8(part.diff, "\uFFFD")),
			fixedText("\n"),
		)
	}
	return parts, nil
}

var specDocs = []string{"requirements.md", "design.md", "tasks.md"}

func mentionSpec(ctx context.Context, query string, ws Workspace) ([]mentionPart, error) {
	dir, ok := spec.DirOf(query)
	if !ok || dir != query {
		return nil, errors.New("not a spec directory")
	}
	parts := []mentionPart{fixedText("# Spec: " + path.Base(dir) + "\n")}
	for _, doc := range specDocs {
		text, _, err := readMentionFile(ctx, dir+"/"+doc, ws, 0, 0)
		if err != nil {
			continue
		}
		parts = append(parts,
			fixedText("\n## "+doc+"\n\n"),
			flexText(strings.TrimSuffix(text, "\n")),
			fixedText("\n"),
		)
	}
	if len(parts) == 1 {
		return nil, errors.New("no spec documents found")
	}
	return parts, nil
}

func mentionSteering(ctx context.Context, query string, ws Workspace) ([]mentionPart, error) {
	q := filepath.ToSlash(filepath.Clean(query))
	if path.Ext(q) != ".md" || !strings.Contains("/"+q, "/.kiro/steering/") {
		return nil, errors.New("not a steering document")
	}
	text, rel, err := readMentionFile(ctx, q, ws, 0, 0)
	if err != nil {
		return nil, err
	}
	return []mentionPart{fixedText("## Included Rules (workspace:" + rel + ")\n\n"), flexText(text)}, nil
}

// mentionMCP reads through the prompt's own bridge, so the server is resolved
// in that chat's pool; a server the pool lacks is KAS's refusal. It has no
// template arm: the composer writes a template already filled.
func mentionMCP(ctx context.Context, query string, mcp bridgeCaller) ([]mentionPart, error) {
	server, uri, ok := strings.Cut(query, ":")
	if !ok || server == "" || uri == "" {
		return nil, errors.New("expected server:uri")
	}
	if mcp == nil {
		return nil, errors.New("MCP is not available")
	}
	resp, err := mcp.Call(ctx, marotte.MethodMCPGetResource, map[string]any{"serverName": server, keyURI: uri})
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("this prompt's MCP reads took longer than %s", mentionMCPBudget)
	}
	if err != nil || resp == nil {
		return nil, errors.New("the MCP server could not be read")
	}
	raw := resp.Result
	var res struct {
		Contents []struct {
			Text     *string `json:"text"`
			Blob     *string `json:"blob"`
			URI      string  `json:"uri"`
			MimeType string  `json:"mimeType"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, errors.New("unreadable MCP response")
	}
	if len(res.Contents) == 0 {
		return nil, errors.New("the resource is empty")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "MCP resource %s from %s:\n", uri, server)
	for _, c := range res.Contents {
		switch {
		case c.Text != nil:
			writeCapped(&b, "\n"+*c.Text+"\n")
		case c.Blob != nil:
			writeCapped(&b, fmt.Sprintf("\n[binary content (%s) omitted]\n", cmp.Or(c.MimeType, "unknown type")))
		}
	}
	return []mentionPart{flexText(b.String())}, nil
}

// A body cut there still holds more than maxMentionChars runes, so renderMention marks the cut.
func writeCapped(b *strings.Builder, s string) {
	room := maxMentionReadBytes - b.Len()
	if room <= 0 {
		return
	}
	if len(s) > room {
		for room > 0 && !utf8.RuneStart(s[room]) {
			room--
		}
		s = s[:room]
	}
	b.WriteString(s)
}
