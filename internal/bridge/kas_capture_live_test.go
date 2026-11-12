package bridge_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"iter"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/cplieger/marotte/internal/bridge"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/kascap"
	"github.com/cplieger/marotte/internal/kirorelease"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workspace"
)

var kasCapture = flag.Bool("kas-capture", false,
	"run TestCaptureKASCorpus: drive the kiro-cli named by MAROTTE_LIVE_KIRO_CLI and rewrite internal/translate/testdata/kas")

const (
	corpusDir       = "../translate/testdata/kas"
	captureAttempts = 3
	turnBudget      = 5 * time.Minute
	scrubWork       = "/work"
	scrubHome       = "~/.kiro"
	elicitServer    = "capture"
	elicitAnswer    = "teal-7341"
	notesBody       = "The release train leaves on Thursday.\nThe build cache lives in /var/cache/build.\nOwners rotate weekly.\n"
)

var (
	kiroCLIPinRe = regexp.MustCompile(`(?m)^KIRO_CLI_VERSION="([^"]+)"`)
	emailRe      = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+\.[A-Za-z][A-Za-z.]+`)
	// A capture holding one of these would publish the host's layout or a credential: marotte is public.
	forbiddenInCapture = []string{"/config/", "/workspace/", "/root/", "/home/", "_kiro/auth/", "accessToken"}
)

// TestCaptureKASCorpus records the KAS frames marotte's own bridge receives for each scenario and
// rewrites internal/translate/testdata/kas/*.jsonl, whose decode tests check invariants, not snapshots.
// It spends model credits, so it needs both MAROTTE_LIVE_KIRO_CLI (a signed-in kiro-cli of the
// entrypoint's pinned version) and -kas-capture.
func TestCaptureKASCorpus(t *testing.T) {
	if !*kasCapture {
		t.Skip("-kas-capture not set: the capture spends credits and rewrites testdata")
	}
	bin, pin := liveKiroCLI(t)
	env := captureEnv{cli: bin, kiroCLI: pin, elicitBin: buildElicitServer(t)}
	for _, sc := range []scenario{
		{name: "steer", about: "A prompt that reads a file through fs/read_text_file, steered on its first tool call; then session/close and a same-id session/load on the same engine.", run: runSteer},
		{name: "compact", about: "Two one-word turns, _kiro/session/compact, a third turn; then a fresh engine and a session/load of the same id.", run: runCompact},
		{name: "delegate", about: "A prompt that delegates to a sub-agent with AutoDelegation on; then session/close and a same-id session/load on the same engine.", run: runDelegate},
		{name: "elicit", about: "An MCP tool that sends elicitation/create, answered accept with marotte.ElicitationResult, and the tool's completion.", run: runElicit},
		{name: "bot_role", about: "A regular chat's session/new; a Bot's session/new on its role and a prompt; reopens through the Bot door with the same role, with an edited role (goal changed, memory on as a context file, a prompt), with memory off (the file removed), and with no role, then session/set_mode to the role.", run: runBotRole},
		{name: "bot_policy", about: "Under each Permission profile, _kiro/policy/check on a Default chat and on a Bot for the same probes, then the Bot's memory write; under Trusted, each session lists its own tools with sub-agents on.", run: runBotPolicy},
		{name: "bot_memory", about: "A Bot whose memory file is a context file added before the file exists: a prompt, the file written, a prompt, an edit, a prompt, the file deleted and its listing, the file recreated, a prompt; then a regular session's context and a prompt, and a fork of the Bot loaded and its context.", run: runBotMemory},
		{name: "bot_memory_write", about: "A Bot with memory on and its directory in additionalDirectories writes its memory file in a turn; reopens with memory off (no directory) and on again, a prompt; then a regular session asked to write the same file, which the client refuses.", run: runBotMemoryWrite},
		{name: "bot_tangent", about: "A Bot with memory on and a prompt; a tangent forked from it on the default mode, loaded as a regular chat, asked to read the Bot's memory file, which the client refuses.", run: runBotTangent},
	} {
		t.Run(sc.name, func(t *testing.T) { captureScenario(t, &env, sc) })
	}
}

type captureEnv struct {
	cli, kiroCLI, kas, elicitBin string
}

type scenario struct {
	run   func(ctx context.Context, c *capture) error
	name  string
	about string
}

func captureScenario(t *testing.T, env *captureEnv, sc scenario) {
	var lastErr error
	for attempt := 1; attempt <= captureAttempts; attempt++ {
		c := newCapture(t, env)
		ctx, cancel := context.WithTimeout(t.Context(), 4*turnBudget)
		err := sc.run(ctx, c)
		cancel()
		c.shutdown()
		if err == nil {
			// kiro-cli unpacks its runtime at its first engine start, so the version is read after one ran.
			env.kas = kasVersion(t, env.kiroCLI)
			err = c.write(sc)
		}
		if err == nil {
			return
		}
		lastErr = err
		t.Logf("attempt %d/%d: %v", attempt, captureAttempts, err)
	}
	t.Fatalf("%s: no attempt met its preconditions: %v", sc.name, lastErr)
}

// capLine is one corpus line. laneSeq orders a reply among its lane's frames.
type capLine struct {
	Dir     string          `json:"dir"`
	Frame   json.RawMessage `json:"frame"`
	lane    int
	laneSeq uint64
	isFrame bool
}

type capture struct {
	t       *testing.T
	env     *captureEnv
	work    string
	home    string
	reached map[int]uint64
	cutoff  map[int]uint64
	// grants are the directories past the workspace a session's file requests reach, as marotte grants a Bot's own
	// directory to the sessions of its chain alone.
	grants map[marotte.SessionID][]string
	// onFrame is the scenario's hook, run on the pump goroutine after the frame is recorded.
	onFrame func(s *bridge.Session, msg *marotte.RPCResponse)
	cond    *sync.Cond
	engines []*bridge.Engine
	cancels []context.CancelFunc
	lines   []*capLine
	pumps   sync.WaitGroup
	mu      sync.Mutex
	lanes   int
}

func newCapture(t *testing.T, env *captureEnv) *capture {
	t.Helper()
	c := &capture{
		t:       t,
		env:     env,
		work:    t.TempDir(),
		home:    t.TempDir(),
		reached: map[int]uint64{},
		cutoff:  map[int]uint64{},
		grants:  map[marotte.SessionID][]string{},
	}
	c.cond = sync.NewCond(&c.mu)
	t.Setenv("KIRO_HOME", c.home)
	// Unset, the pinned binary replaces itself with the newest release during a capture, so the next run on the same
	// path is refused by checkBinaryVersion.
	if out, err := exec.CommandContext(t.Context(), env.cli, kirorelease.AutoUpdateGate().Args...).CombinedOutput(); err != nil {
		t.Fatalf("turn off kiro-cli's self-update: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(c.work, "notes.txt"), []byte(notesBody), 0o600); err != nil {
		t.Fatal(err)
	}
	return c
}

func (c *capture) startEngine(ctx context.Context, features marotte.EngineFeatures) (*bridge.Engine, error) {
	e := bridge.NewEngine(c.env.cli, c.work)
	lifetime, cancel := context.WithCancel(context.Background())
	c.engines = append(c.engines, e)
	c.cancels = append(c.cancels, cancel)
	go c.drainEngineLane(e)
	if err := e.Start(ctx, &marotte.EngineOpts{
		Lifetime: lifetime,
		AuthArgs: []string{"--auth-method", "cli"},
		Features: features,
	}); err != nil {
		return nil, fmt.Errorf("engine start: %w", err)
	}
	return e, nil
}

// drainEngineLane refuses the requests no session lane claimed, so none wedges KAS.
func (c *capture) drainEngineLane(e *bridge.Engine) {
	for n := range e.EngineNotifCh() {
		if n.Msg.ID != nil && n.Msg.Method != "" {
			c.t.Logf("engine-lane request %s refused", n.Msg.Method)
			_ = e.Respond(context.Background(), *n.Msg.ID, nil, &marotte.RPCError{Code: -32601, Message: "method not found"})
		}
	}
}

func (c *capture) shutdown() {
	for i, e := range c.engines {
		e.Stop()
		<-e.Done()
		c.cancels[i]()
	}
	c.pumps.Wait()
}

// attach returns the door's attach hook: it starts the lane pump before the door writes anything.
func (c *capture) attach() (lane int, attach func(*bridge.Session) error) {
	c.mu.Lock()
	c.lanes++
	lane = c.lanes
	c.mu.Unlock()
	return lane, func(s *bridge.Session) error {
		c.pumps.Go(func() { c.pump(s, lane) })
		return nil
	}
}

func (c *capture) pump(s *bridge.Session, lane int) {
	for n := range s.NotifCh() {
		c.mu.Lock()
		stop, frozen := c.cutoff[lane]
		if frozen && n.Seq > stop {
			c.mu.Unlock()
			continue
		}
		raw, _ := json.Marshal(n.Msg)
		c.lines = append(c.lines, &capLine{Dir: "in", Frame: raw, lane: lane, laneSeq: n.Seq, isFrame: true})
		c.reached[lane] = n.Seq
		c.cond.Broadcast()
		hook := c.onFrame
		c.mu.Unlock()
		if n.Msg.ID != nil && n.Msg.Method != "" {
			c.answer(s, lane, n.Msg)
		}
		if hook != nil {
			hook(s, n.Msg)
		}
	}
}

// waitReached blocks until the lane's pump recorded the frame at seq.
func (c *capture) waitReached(lane int, seq uint64) error {
	deadline := time.Now().Add(30 * time.Second)
	stop := time.AfterFunc(30*time.Second, func() {
		c.mu.Lock()
		c.cond.Broadcast()
		c.mu.Unlock()
	})
	defer stop.Stop()
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.reached[lane] < seq {
		if time.Now().After(deadline) {
			return fmt.Errorf("lane %d never reached frame %d", lane, seq)
		}
		c.cond.Wait()
	}
	return nil
}

// freeze records nothing on lane after seq: the frames a door's own appliers cause are not the scenario's.
func (c *capture) freeze(lane int, seq uint64) error {
	if err := c.waitReached(lane, seq); err != nil {
		return err
	}
	c.mu.Lock()
	c.cutoff[lane] = seq
	c.mu.Unlock()
	return nil
}

func (c *capture) out(lane int, frame map[string]any) *capLine {
	frame["jsonrpc"] = "2.0"
	raw, _ := json.Marshal(frame)
	c.mu.Lock()
	defer c.mu.Unlock()
	l := &capLine{Dir: "out", Frame: raw, lane: lane, laneSeq: c.reached[lane]}
	c.lines = append(c.lines, l)
	return l
}

// reply records the response to req at the lane position it arrived at and gives req the id the
// engine wrote it under.
func (c *capture) reply(lane int, req *capLine, resp *marotte.RPCResponse, seq uint64) {
	if resp == nil {
		return
	}
	raw, _ := json.Marshal(resp)
	c.mu.Lock()
	defer c.mu.Unlock()
	if resp.ID != nil {
		var f map[string]any
		_ = json.Unmarshal(req.Frame, &f)
		f["id"] = *resp.ID
		req.Frame, _ = json.Marshal(f)
	}
	l := &capLine{Dir: "in", Frame: raw, lane: lane, laneSeq: seq}
	at := len(c.lines)
	for i, x := range c.lines {
		if x.lane == lane && x.isFrame && x.laneSeq > seq {
			at = i
			break
		}
	}
	c.lines = slices.Insert(c.lines, at, l)
}

func (c *capture) call(ctx context.Context, s *bridge.Session, lane int, method string, params map[string]any) (*marotte.RPCResponse, error) {
	req := c.out(lane, map[string]any{"method": method, "params": params})
	resp, seq, err := s.CallAt(ctx, method, params)
	c.reply(lane, req, resp, seq)
	if err == nil && resp != nil && resp.Error != nil {
		err = resp.Error
	}
	return resp, err
}

// prompt sends text with marotte's own prompt door and waits for the turn's stop reason.
func (c *capture) prompt(ctx context.Context, s *bridge.Session, lane int, text string) error {
	params := map[string]any{
		marotte.KeySessionID: s.SessionID(),
		marotte.KeyPrompt:    []map[string]any{{"type": marotte.ContentTypeText, "text": text}},
	}
	if meta := kascap.PromptMeta(&kascap.Prompt{Tag: ids.NewMessageID()}); len(meta) > 0 {
		params["_meta"] = map[string]any{"kiro": meta}
	}
	req := c.out(lane, map[string]any{"method": marotte.MethodPrompt, "params": params})
	tctx, cancel := context.WithTimeout(ctx, turnBudget)
	defer cancel()
	resp, seq, err := s.PromptAt(tctx, params, nil)
	c.reply(lane, req, resp, seq)
	if err != nil {
		return fmt.Errorf("prompt %q: %w", text, err)
	}
	if resp.Error != nil {
		return fmt.Errorf("prompt %q: %w", text, resp.Error)
	}
	return c.waitReached(lane, seq)
}

func (c *capture) newSession(ctx context.Context, e *bridge.Engine) (*bridge.Session, int, error) {
	lane, attach := c.attach()
	c.out(lane, map[string]any{"method": marotte.MethodSessionNew, "params": map[string]any{"cwd": c.work}})
	s, err := e.NewSession(ctx, &marotte.SessionOpts{Role: marotte.SessionRoleChat}, attach)
	if err != nil {
		return nil, 0, fmt.Errorf("session/new: %w", err)
	}
	return s, lane, nil
}

// closeSession is marotte's release before a reopen.
func (c *capture) closeSession(ctx context.Context, s *bridge.Session, lane int) error {
	c.out(lane, map[string]any{"method": marotte.MethodSessionClose, "params": map[string]any{marotte.KeySessionID: s.SessionID()}})
	if err := s.Close(ctx); err != nil {
		return fmt.Errorf("session/close: %w", err)
	}
	return nil
}

// loadSession reopens id and records its replay up to the load's own response.
func (c *capture) loadSession(ctx context.Context, e *bridge.Engine, id marotte.SessionID) error {
	lane, attach := c.attach()
	c.out(lane, map[string]any{"method": marotte.MethodSessionLoad, "params": map[string]any{marotte.KeySessionID: id, "cwd": c.work}})
	s, err := e.LoadSession(ctx, &marotte.SessionOpts{SessionID: string(id), Role: marotte.SessionRoleChat}, attach)
	if err != nil {
		return fmt.Errorf("session/load: %w", err)
	}
	return c.freeze(lane, s.SessionLoadSeq())
}

// answer replies to a session-lane request the way a chat with every ask approved would.
func (c *capture) answer(s *bridge.Session, lane int, msg *marotte.RPCResponse) {
	result, err := c.resolve(msg)
	frame := map[string]any{"id": *msg.ID}
	var rpcErr *marotte.RPCError
	switch {
	case errors.As(err, &rpcErr):
		frame["error"] = map[string]any{"code": rpcErr.Code, "message": rpcErr.Message}
	case err != nil:
		rpcErr = &marotte.RPCError{Code: -32603, Message: err.Error()}
		frame["error"] = map[string]any{"code": rpcErr.Code, "message": rpcErr.Message}
		err = rpcErr
	default:
		frame["result"] = result
	}
	c.out(lane, frame)
	if rErr := s.Respond(context.Background(), *msg.ID, result, err); rErr != nil {
		c.t.Logf("answer %s: %v", msg.Method, rErr)
	}
}

func (c *capture) resolve(msg *marotte.RPCResponse) (any, error) {
	var p struct {
		SessionID marotte.SessionID `json:"sessionId"`
		Path      string            `json:"path"`
		Content   string            `json:"content"`
		Options   []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	_ = json.Unmarshal(msg.Params, &p)
	switch msg.Method {
	case marotte.MethodRequestPermission:
		for _, o := range p.Options {
			if o.Kind == "allow_once" {
				return marotte.PermissionOutcomeSelected(o.OptionID), nil
			}
		}
		return nil, errors.New("no allow_once option offered")
	case marotte.MethodFSRead:
		data, err := c.readScratch(p.SessionID, p.Path)
		if err != nil {
			return nil, err
		}
		return map[string]any{"content": string(data)}, nil
	case marotte.MethodFSWrite:
		if err := c.writeScratch(p.SessionID, p.Path, p.Content); err != nil {
			return nil, err
		}
		return map[string]any{}, nil
	case "_kiro/fs/stat":
		info, err := c.statScratch(p.SessionID, p.Path)
		if err != nil {
			return nil, err
		}
		kind := "file"
		if info.IsDir() {
			kind = "directory"
		}
		return map[string]any{"type": kind, "size": info.Size()}, nil
	case "_kiro/fs/read_directory":
		return c.listScratch(p.SessionID, p.Path)
	case "_kiro/search/text_search", "_kiro/search/find_files":
		return c.search(msg)
	case marotte.MethodElicitationCreate:
		return marotte.ElicitationResult{
			Action:  marotte.ElicitationActionAccept,
			Content: map[string]any{"value": elicitAnswer},
		}, nil
	}
	return nil, &marotte.RPCError{Code: -32601, Message: "method not found: " + msg.Method}
}

// grant sets the directories past the workspace session's file requests reach; none takes them away.
func (c *capture) grant(session marotte.SessionID, dirs ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.grants[session] = dirs
}

// scratchRoot opens the root holding path for session's request: the workspace, or a directory granted to it.
func (c *capture) scratchRoot(session marotte.SessionID, path string) (*os.Root, string, error) {
	c.mu.Lock()
	dirs := append([]string{c.work}, c.grants[session]...)
	c.mu.Unlock()
	for _, dir := range dirs {
		rel, err := filepath.Rel(dir, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			root, err := os.OpenRoot(dir)
			return root, rel, err
		}
	}
	return nil, "", fmt.Errorf("%s is outside the capture workspace", path)
}

func (c *capture) readScratch(session marotte.SessionID, path string) ([]byte, error) {
	root, rel, err := c.scratchRoot(session, path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.ReadFile(rel)
}

func (c *capture) writeScratch(session marotte.SessionID, path, content string) error {
	root, rel, err := c.scratchRoot(session, path)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.WriteFile(rel, []byte(content), 0o600)
}

func (c *capture) statScratch(session marotte.SessionID, path string) (os.FileInfo, error) {
	root, rel, err := c.scratchRoot(session, path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Stat(rel)
}

func (c *capture) listScratch(session marotte.SessionID, path string) (any, error) {
	root, rel, err := c.scratchRoot(session, path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	des, err := f.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	entries := make([]map[string]any, 0, len(des))
	for _, d := range des {
		kind := "file"
		if d.IsDir() {
			kind = "directory"
		}
		entries = append(entries, map[string]any{"name": d.Name(), "type": kind})
	}
	return map[string]any{"entries": entries}, nil
}

// write scrubs the scratch paths and the model catalog, refuses a capture that would leak, and writes the
// corpus file.
func (c *capture) write(sc scenario) error {
	c.mu.Lock()
	lines := slices.Clone(c.lines)
	c.mu.Unlock()
	header, _ := json.Marshal(map[string]any{"_meta": map[string]any{
		"recorded": "live", "kiroCli": c.env.kiroCLI, "kas": c.env.kas,
		"date": time.Now().UTC().Format(time.DateOnly), "scenario": sc.about,
	}})
	var buf bytes.Buffer
	buf.Write(header)
	buf.WriteByte('\n')
	for _, l := range lines {
		raw, _ := json.Marshal(l)
		buf.Write(raw)
		buf.WriteByte('\n')
	}
	out, err := scrubModels(c.scrub(buf.Bytes()))
	if err != nil {
		return err
	}
	for _, bad := range forbiddenInCapture {
		if i := bytes.Index(out, []byte(bad)); i >= 0 {
			return fmt.Errorf("capture holds %q near %q", bad, excerpt(out, i))
		}
	}
	if m := emailRe.Find(out); m != nil {
		return fmt.Errorf("capture holds an email address %q", m)
	}
	if err := os.MkdirAll(corpusDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(corpusDir, sc.name+".jsonl"), out, 0o644)
}

func (c *capture) scrub(b []byte) []byte {
	for _, r := range []struct{ from, to string }{{c.work, scrubWork}, {c.home, scrubHome}} {
		b = bytes.ReplaceAll(b, []byte(r.from), []byte(r.to))
		quoted, _ := json.Marshal(r.from)
		b = bytes.ReplaceAll(b, quoted[1:len(quoted)-1], []byte(r.to))
	}
	return b
}

var (
	// modelTagRe is one bracketed tag in a model description; modeltext.Hidden reads [Deprecated]
	// and [Legacy] from these, so the scrub keeps every tag.
	modelTagRe = regexp.MustCompile(`\[[^\]]*\]`)
	// modelStemRe is a word of an id or display name: digits and punctuation split it, and a run of
	// one or two letters (k3, m2.5, 1m) is a version, not a name.
	modelStemRe  = regexp.MustCompile(`\pL{3,}`)
	wordRe       = regexp.MustCompile(`\pL+`)
	jsonStringRe = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)
	// genericModelWords are catalog words ordinary prose also uses ("check next"); refusing them
	// would refuse a capture for plain prose.
	genericModelWords = []string{
		"auto", "beta", "coder", "deprecated", "experimental", "internal", "latest", "legacy", "max",
		"mini", "model", "next", "preview", "pro", "thinking",
	}
)

type modelOption struct{ value, name, desc string }

// scrubModels replaces each model the catalog offers, other than Auto, with a placeholder in order of
// first appearance (model-a, "Model A"): every JSON string equal to an option's id, name or
// description becomes the placeholder id, name, or the description's bracketed tags plus the name.
// Upstream retires model ids, and the corpus is public. It refuses a capture that still names an
// offered model.
func scrubModels(b []byte) ([]byte, error) {
	var opts []modelOption
	for line := range bytes.Lines(b) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var v any
		if err := json.Unmarshal(line, &v); err != nil {
			return nil, fmt.Errorf("decode capture line: %w", err)
		}
		collectModelOptions(v, &opts)
	}
	placeholder := map[string]string{}
	placeholders := map[string]bool{}
	replace := map[string]string{}
	set := func(from, to string) {
		// An empty old string would turn every empty JSON string in the capture into a model name.
		if _, taken := replace[from]; from != "" && !taken {
			replace[from] = to
		}
	}
	for _, o := range opts {
		if _, ok := placeholder[o.value]; !ok {
			placeholder[o.value] = "model-" + modelLetters(len(placeholder))
		}
		// Ids are set before any name or description: a catalog may name a model by its id.
		set(o.value, placeholder[o.value])
	}
	for _, o := range opts {
		id := placeholder[o.value]
		name := "Model " + strings.ToUpper(id[len("model-"):])
		placeholders[id], placeholders[strings.ToLower(name)] = true, true
		set(o.desc, strings.Join(append(modelTagRe.FindAllString(o.desc, -1), name), " "))
		set(o.name, name)
	}
	out := jsonStringRe.ReplaceAllFunc(b, func(tok []byte) []byte {
		var s string
		if json.Unmarshal(tok, &s) != nil {
			return tok
		}
		if to, ok := replace[s]; ok {
			return []byte(quoteJSON(to))
		}
		return tok
	})
	return out, refuseModelMentions(out, opts, placeholders)
}

// refuseModelMentions errors when a decoded string in b holds an offered model's id, display name,
// a word of either, or a vendor word from its description, in any case and with no letter right
// before or after it, unless that text is itself a placeholder or a generic word.
func refuseModelMentions(b []byte, opts []modelOption, placeholders map[string]bool) error {
	seen := map[string]bool{}
	var words []string
	add := func(s string) {
		l := strings.ToLower(s)
		if l == "" || seen[l] || placeholders[l] || slices.Contains(genericModelWords, l) {
			return
		}
		seen[l] = true
		words = append(words, regexp.QuoteMeta(l))
	}
	for _, o := range opts {
		add(o.value)
		add(o.name)
		for _, s := range modelStemRe.FindAllString(o.value+" "+o.name, -1) {
			add(s)
		}
		for _, s := range wordRe.FindAllString(o.desc, -1) {
			if isVendorWord(s) {
				add(s)
			}
		}
	}
	if len(words) == 0 {
		return nil
	}
	re := regexp.MustCompile(`(?i)(?:^|\PL)(` + strings.Join(words, "|") + `)(?:$|\PL)`)
	for line := range bytes.Lines(b) {
		var v any
		if json.Unmarshal(line, &v) != nil {
			continue
		}
		for s := range jsonStrings(v) {
			if m := re.FindStringSubmatchIndex(s); m != nil {
				return fmt.Errorf("capture still names an offered model near %q", excerpt([]byte(s), m[2]))
			}
		}
	}
	return nil
}

// isVendorWord reports a description word spelled in mixed case after its first letter (eCorp,
// QuillSoft), the shape of a vendor name; title-case and all-caps description words are ordinary prose.
func isVendorWord(w string) bool {
	r := []rune(w)
	return len(r) >= 3 && slices.ContainsFunc(r[1:], unicode.IsUpper) && slices.ContainsFunc(r, unicode.IsLower)
}

// jsonStrings yields every key and string value under v. The check reads decoded strings so a JSON
// escape such as \n before a word does not hide its word boundary.
func jsonStrings(v any) iter.Seq[string] {
	return func(yield func(string) bool) {
		var walk func(any) bool
		walk = func(v any) bool {
			switch t := v.(type) {
			case string:
				return yield(t)
			case map[string]any:
				for k, child := range t {
					if !yield(k) || !walk(child) {
						return false
					}
				}
			case []any:
				for _, child := range t {
					if !walk(child) {
						return false
					}
				}
			}
			return true
		}
		walk(v)
	}
}

// collectModelOptions appends the options of every model select under v, once per distinct option.
func collectModelOptions(v any, opts *[]modelOption) {
	switch t := v.(type) {
	case map[string]any:
		if list, ok := t["options"].([]any); ok && t["id"] == "model" {
			for _, item := range list {
				m, _ := item.(map[string]any)
				o := modelOption{}
				o.value, _ = m["value"].(string)
				o.name, _ = m["name"].(string)
				o.desc, _ = m["description"].(string)
				if o.value != "" && o.value != marotte.ModelAuto && !slices.Contains(*opts, o) {
					*opts = append(*opts, o)
				}
			}
		}
		for _, k := range slices.Sorted(maps.Keys(t)) {
			collectModelOptions(t[k], opts)
		}
	case []any:
		for _, child := range t {
			collectModelOptions(child, opts)
		}
	}
}

// modelLetters is i in bijective base 26: a to z, then aa.
func modelLetters(i int) string {
	s := ""
	for i++; i > 0; i = (i - 1) / 26 {
		s = string(rune('a'+(i-1)%26)) + s
	}
	return s
}

// quoteJSON is s as a JSON string without HTML escaping, the form KAS writes.
func quoteJSON(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(buf.String(), "\n")
}

func excerpt(b []byte, i int) string {
	lo, hi := max(0, i-60), min(len(b), i+60)
	return string(b[lo:hi])
}

func (l *capLine) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Dir   string          `json:"dir"`
		Frame json.RawMessage `json:"frame"`
	}{l.Dir, l.Frame})
}

// frameView is the part of a recorded frame a precondition reads.
type frameView struct {
	Method string `json:"method"`
	Params struct {
		Update struct {
			SessionUpdate string          `json:"sessionUpdate"`
			ToolCallID    string          `json:"toolCallId"`
			Status        string          `json:"status"`
			Content       json.RawMessage `json:"content"`
			RawOutput     json.RawMessage `json:"rawOutput"`
			Meta          struct {
				Kiro map[string]any `json:"kiro"`
			} `json:"_meta"`
		} `json:"update"`
	} `json:"params"`
}

func (c *capture) inFrames(lane int) []frameView {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []frameView
	for _, l := range c.lines {
		if l.Dir != "in" || !l.isFrame || l.lane != lane {
			continue
		}
		var f frameView
		_ = json.Unmarshal(l.Frame, &f)
		out = append(out, f)
	}
	return out
}

func (f *frameView) kind() string {
	k, _ := f.Params.Update.Meta.Kiro["kind"].(string)
	return k
}

func (f *frameView) metaString(key string) string {
	v, _ := f.Params.Update.Meta.Kiro[key].(string)
	return v
}

func (f *frameView) text() string {
	var block struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(f.Params.Update.Content, &block)
	return block.Text
}

// indexOf is the first frame at or after from that match accepts, or -1.
func indexOf(frames []frameView, from int, match func(*frameView) bool) int {
	for i := max(from, 0); i < len(frames); i++ {
		if match(&frames[i]) {
			return i
		}
	}
	return -1
}

func infoKind(kind string) func(*frameView) bool {
	return func(f *frameView) bool {
		return f.Params.Update.SessionUpdate == "session_info_update" && f.kind() == kind
	}
}

func runSteer(ctx context.Context, c *capture) error {
	e, err := c.startEngine(ctx, marotte.EngineFeatures{})
	if err != nil {
		return err
	}
	s, lane, err := c.newSession(ctx, e)
	if err != nil {
		return err
	}
	msgID := ids.NewMessageID()
	steerID := marotte.SteerIDFor(msgID)
	var once sync.Once
	steerErr := make(chan error, 1)
	c.mu.Lock()
	c.onFrame = func(s *bridge.Session, msg *marotte.RPCResponse) {
		var f frameView
		if msg.Method != marotte.MethodSessionUpdate || json.Unmarshal(msg.Params, &f.Params) != nil || f.Params.Update.SessionUpdate != "tool_call" {
			return
		}
		once.Do(func() {
			go func() {
				_, err := c.call(ctx, s, lane, marotte.MethodSessionSteer, map[string]any{
					marotte.KeySessionID: s.SessionID(),
					"message":            "Also say how many lines the file has.",
					"messageId":          msgID,
				})
				steerErr <- err
			}()
		})
	}
	c.mu.Unlock()
	if err := c.prompt(ctx, s, lane, "Read the file notes.txt in the workspace with your file-reading tool, then summarize it in one sentence."); err != nil {
		return err
	}
	c.mu.Lock()
	c.onFrame = nil
	c.mu.Unlock()
	select {
	case err := <-steerErr:
		if err != nil {
			return fmt.Errorf("steer: %w", err)
		}
	default:
		return errors.New("the turn made no tool call to steer on")
	}
	live := c.inFrames(lane)
	queued := indexOf(live, 0, func(f *frameView) bool { return infoKind("steering_queued")(f) && f.metaString("messageId") == steerID })
	injected := indexOf(live, queued, func(f *frameView) bool {
		return infoKind("steering_injected")(f) && f.metaString("messageId") == steerID
	})
	marker := indexOf(live, injected, func(f *frameView) bool { return strings.Contains(f.text(), "[STEERING") })
	end := indexOf(live, 0, infoKind("turn_end"))
	cleared := indexOf(live, end, infoKind("steering_cleared"))
	if queued < 0 || injected < 0 || marker < 0 || cleared < 0 {
		return fmt.Errorf("live half: steering_queued %d, steering_injected %d, [STEERING marker %d, steering_cleared after turn_end %d", queued, injected, marker, cleared)
	}
	return c.reopenAndCheck(ctx, e, s, lane, func(replay []frameView) error {
		if indexOf(replay, 0, func(f *frameView) bool {
			return f.Params.Update.SessionUpdate == "user_message_chunk" && f.metaString("source") == "steer"
		}) < 0 {
			return errors.New("replay holds no user_message_chunk with source steer")
		}
		return nil
	})
}

// reopenAndCheck closes s and loads its id on the same engine, then runs check on the replay.
func (c *capture) reopenAndCheck(ctx context.Context, e *bridge.Engine, s *bridge.Session, lane int, check func([]frameView) error) error {
	if err := c.closeSession(ctx, s, lane); err != nil {
		return err
	}
	if err := c.loadSession(ctx, e, s.SessionID()); err != nil {
		return err
	}
	return check(c.inFrames(c.lanes))
}

func runCompact(ctx context.Context, c *capture) error {
	e, err := c.startEngine(ctx, marotte.EngineFeatures{})
	if err != nil {
		return err
	}
	s, lane, err := c.newSession(ctx, e)
	if err != nil {
		return err
	}
	for _, word := range []string{"alpha", "beta"} {
		if err := c.prompt(ctx, s, lane, "Reply with the single word "+word+" and nothing else."); err != nil {
			return err
		}
	}
	resp, err := c.call(ctx, s, lane, marotte.MethodSessionCompact, map[string]any{marotte.KeySessionID: s.SessionID()})
	if err != nil {
		return fmt.Errorf("compact: %w", err)
	}
	var result struct {
		Success bool `json:"success"`
	}
	if json.Unmarshal(resp.Result, &result) != nil || !result.Success {
		return fmt.Errorf("compact answered %s", resp.Result)
	}
	if indexOf(c.inFrames(lane), 0, infoKind("summarization_completed")) < 0 {
		return errors.New("no summarization_completed before the compact's answer")
	}
	if err := c.prompt(ctx, s, lane, "Reply with the single word gamma and nothing else."); err != nil {
		return err
	}
	id := s.SessionID()
	if err := c.closeSession(ctx, s, lane); err != nil {
		return err
	}
	e.Stop()
	<-e.Done()
	fresh, err := c.startEngine(ctx, marotte.EngineFeatures{})
	if err != nil {
		return err
	}
	if err := c.loadSession(ctx, fresh, id); err != nil {
		return err
	}
	replay := c.inFrames(c.lanes)
	sep := indexOf(replay, 0, infoKind("summarization_separator"))
	if sep < 0 || indexOf(replay, sep, infoKind("summary_message")) < 0 {
		return fmt.Errorf("replay: summarization_separator at %d, no summary_message after it", sep)
	}
	return nil
}

func runDelegate(ctx context.Context, c *capture) error {
	e, err := c.startEngine(ctx, marotte.EngineFeatures{AutoDelegation: "on"})
	if err != nil {
		return err
	}
	s, lane, err := c.newSession(ctx, e)
	if err != nil {
		return err
	}
	if err := c.prompt(ctx, s, lane, "Delegate this to a sub-agent rather than doing it yourself: the sub-agent reads notes.txt in the workspace and reports its first line. Then tell me what it reported."); err != nil {
		return err
	}
	live := c.inFrames(lane)
	call := indexOf(live, 0, func(f *frameView) bool {
		return f.Params.Update.SessionUpdate == "tool_call" && f.kind() == "agent-subtask"
	})
	if call < 0 {
		return errors.New("no agent-subtask tool_call: KAS did not delegate")
	}
	callID := live[call].Params.Update.ToolCallID
	if indexOf(live, call, func(f *frameView) bool {
		return f.Params.Update.SessionUpdate == "tool_call_update" && f.Params.Update.ToolCallID == callID &&
			slices.Contains([]string{"completed", "failed"}, f.Params.Update.Status)
	}) < 0 {
		return fmt.Errorf("delegate call %s never reached a terminal update", callID)
	}
	if indexOf(live, call, func(f *frameView) bool {
		return f.Params.Update.SessionUpdate == "agent_message_chunk" && f.metaString("agentSubtaskId") != ""
	}) < 0 {
		return errors.New("no delegate prose stamped agentSubtaskId")
	}
	return c.reopenAndCheck(ctx, e, s, lane, func(replay []frameView) error {
		if indexOf(replay, 0, func(f *frameView) bool {
			return f.Params.Update.SessionUpdate == "tool_call" && strings.HasSuffix(f.Params.Update.ToolCallID, "-sub-agent-start")
		}) < 0 {
			return errors.New("replay holds no -sub-agent-start tool_call")
		}
		return nil
	})
}

func runElicit(ctx context.Context, c *capture) error {
	settings := filepath.Join(c.work, ".kiro", "settings")
	if err := os.MkdirAll(settings, 0o755); err != nil {
		return err
	}
	cfg, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{
		elicitServer: map[string]any{"command": c.env.elicitBin, "args": []string{}},
	}})
	if err := os.WriteFile(filepath.Join(settings, "mcp.json"), cfg, 0o600); err != nil {
		return err
	}
	e, err := c.startEngine(ctx, marotte.EngineFeatures{})
	if err != nil {
		return err
	}
	s, lane, err := c.newSession(ctx, e)
	if err != nil {
		return err
	}
	if err := c.awaitFrameMentioning(lane, "ask_value", 60*time.Second); err != nil {
		c.t.Logf("elicit: %v; prompting anyway", err)
	}
	if err := c.prompt(ctx, s, lane, "Call the ask_value tool of the "+elicitServer+" MCP server once, then tell me the value it recorded."); err != nil {
		return err
	}
	live := c.inFrames(lane)
	if indexOf(live, 0, func(f *frameView) bool { return f.Method == marotte.MethodElicitationCreate }) < 0 {
		return errors.New("no _kiro/mcp/elicitation request reached the session lane")
	}
	if indexOf(live, 0, func(f *frameView) bool {
		u := &f.Params.Update
		return u.SessionUpdate == "tool_call_update" && u.Status == "completed" &&
			(bytes.Contains(u.Content, []byte(elicitAnswer)) || bytes.Contains(u.RawOutput, []byte(elicitAnswer)))
	}) < 0 {
		return errors.New("no completed tool_call_update echoes the accepted value")
	}
	return nil
}

// awaitFrameMentioning waits for a recorded frame on lane whose bytes hold needle: KAS connects
// MCP servers after the session opens, so a prompt sent at once can find no tool.
func (c *capture) awaitFrameMentioning(lane int, needle string, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		found := slices.ContainsFunc(c.lines, func(l *capLine) bool {
			return l.lane == lane && l.Dir == "in" && bytes.Contains(l.Frame, []byte(needle))
		})
		c.mu.Unlock()
		if found {
			return nil
		}
		timer := time.NewTimer(500 * time.Millisecond)
		<-timer.C
	}
	return fmt.Errorf("no frame mentioned %q within %s", needle, budget)
}

// liveKiroCLI is the kiro-cli MAROTTE_LIVE_KIRO_CLI names and the entrypoint pin it must report. It skips when
// the variable is unset, so a caller checks its own opt-in flag first.
func liveKiroCLI(t *testing.T) (bin, pin string) {
	t.Helper()
	bin = os.Getenv("MAROTTE_LIVE_KIRO_CLI")
	if bin == "" {
		t.Skip("MAROTTE_LIVE_KIRO_CLI names no kiro-cli binary")
	}
	pin = pinnedKiroCLI(t)
	if dir := os.Getenv("KIRO_RUNTIME_DIR"); dir != "" {
		if other := foreignRuntimes(dir, pin); len(other) > 0 {
			t.Fatalf("KIRO_RUNTIME_DIR holds %v, which kiro-cli %s deletes at its start and a running engine may use: "+
				"point it at a scratch folder or unset it", other, pin)
		}
	}
	checkBinaryVersion(t, bin, pin)
	return bin, pin
}

// runtimeEntryRe is a KIRO_RUNTIME_DIR entry's name, <version>-<16 hex>.
var runtimeEntryRe = regexp.MustCompile(`^(.+)-[0-9a-f]{16}$`)

// foreignRuntimes are dir's complete runtime entries of a kiro-cli version other than pin: a start of pin prunes
// every one of them.
func foreignRuntimes(dir, pin string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		m := runtimeEntryRe.FindStringSubmatch(e.Name())
		if !e.IsDir() || m == nil || m[1] == pin {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, e.Name(), ".complete")); err == nil {
			out = append(out, e.Name())
		}
	}
	return out
}

func pinnedKiroCLI(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	m := kiroCLIPinRe.FindSubmatch(raw)
	if m == nil {
		t.Fatal(`entrypoint.sh holds no KIRO_CLI_VERSION="..." pin`)
	}
	return string(m[1])
}

func checkBinaryVersion(t *testing.T, bin, pin string) {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), bin, "--version").Output()
	if err != nil {
		t.Fatalf("%s --version: %v", bin, err)
	}
	if got := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "kiro-cli")); got != pin {
		t.Fatalf("%s is kiro-cli %s; the corpus is captured on the pinned %s only", bin, got, pin)
	}
}

// kasVersion reads @kiro/agent's version from the runtime the pinned kiro-cli unpacked, found as
// kiro-cli finds it.
func kasVersion(t *testing.T, pin string) string {
	t.Helper()
	server := workspace.KASLookupFromEnv().Find(pin).Server
	if server == "" {
		t.Fatalf("no unpacked agent runtime for kiro-cli %s after an engine start", pin)
	}
	pkgJSON := filepath.Join(filepath.Dir(server), "..", "..", "package.json")
	raw, err := os.ReadFile(pkgJSON)
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(raw, &pkg) != nil || pkg.Version == "" {
		t.Fatalf("%s carries no version", pkgJSON)
	}
	return pkg.Version
}

func buildElicitServer(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "kas_elicit_server")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", out, "./testdata/kas_elicit_server")
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the elicitation MCP server: %v\n%s", err, msg)
	}
	return out
}
