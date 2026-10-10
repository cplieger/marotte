package server

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/webhttp/v3"
)

// kiroDocNewRequest is POST /api/workspace/kiro-docs/new's body; each kind reads only its fields.
type kiroDocNewRequest struct {
	// Timeout is a hook command's limit in seconds; absent leaves KAS's default.
	Timeout     *int   `json:"timeout,omitempty"`
	Category    string `json:"category"`
	Name        string `json:"name"`
	Inclusion   string `json:"inclusion,omitempty"`
	Description string `json:"description,omitempty"`
	Trigger     string `json:"trigger,omitempty"`
	Matcher     string `json:"matcher,omitempty"`
	Action      string `json:"action,omitempty"`
	// Content is a hook's command or its instructions, by Action.
	Content string `json:"content,omitempty"`
}

// kiroDocNewResponse is the 201 reply: the new file's path in the inventory rows' spelling.
type kiroDocNewResponse struct {
	Path string `json:"path"`
}

// newDoc is a validated document; the file name it finally takes is length-checked by the
// create, once that name is known.
type newDoc struct {
	// rel is the slash-separated path under `.kiro`.
	rel string
	// exists is the refusal when rel (or a twin) is taken.
	exists string
	data   []byte
	// twins are other names whose presence also refuses the create.
	twins []string
	// numbered retries a taken rel as `<stem>-1<ext>`, `-2<ext>` …, the IDE's hook-file rule.
	numbered bool
}

// fieldError is a refusal of one field, answered 400 with its text.
type fieldError string

func (e fieldError) Error() string { return string(e) }

// matcherCheck reports whether KAS accepts a hook matcher for a trigger's subject.
type matcherCheck func(subject marotte.HookMatcherSubject, matcher string) (bool, error)

var (
	errDocExists    = errors.New("document exists")
	errDocProtected = errors.New(".kiro reaches the sensitive denylist")
)

// maxFileName is the common filesystem limit on one name, in bytes.
const maxFileName = 255

const msgNoKASRuntime = "Kiro's runtime is not installed yet, so the matcher cannot be checked. Open a chat, then try again."

// writeDocData is the template write; a test swaps it to fail after the file exists.
var writeDocData = func(f *os.File, data []byte) error {
	_, err := f.Write(data)
	return err
}

var newDocBuilders = map[string]func(*kiroDocNewRequest, matcherCheck) (newDoc, error){
	catSteering: newSteeringDoc,
	catSkill:    newSkillDoc,
	catPrompt:   newPromptDoc,
	catAgent:    newAgentDoc,
	catHook:     newHookDoc,
}

func (s *Server) handleKiroDocNew(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var req kiroDocNewRequest
	if !decodeBody(w, r, &req) {
		return
	}
	build, ok := newDocBuilders[req.Category]
	if !ok {
		httpreply.BadRequest(w, "unknown document kind")
		return
	}
	re := kasRegExp{node: s.kasNode}
	doc, err := build(&req, func(subject marotte.HookMatcherSubject, m string) (bool, error) {
		return kasAcceptsMatcher(r.Context(), re, subject, m)
	})
	if errors.Is(err, errKASRuntimeMissing) {
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON(msgNoKASRuntime))
		return
	}
	if fe, isField := errors.AsType[fieldError](err); isField {
		httpreply.BadRequest(w, fe.Error())
		return
	}
	if err != nil {
		httpreply.InternalError(w, err)
		return
	}
	rel, err := s.createKiroDoc(&doc)
	if fe, isField := errors.AsType[fieldError](err); isField {
		httpreply.BadRequest(w, fe.Error())
		return
	}
	switch {
	case errors.Is(err, errDocExists):
		httpreply.Conflict(w, doc.exists)
		return
	case errors.Is(err, errDocProtected):
		httpreply.Forbidden(w, "refusing to create a protected path")
		return
	case err != nil:
		httpreply.ServerError(w, "could not create the document", err)
		return
	}
	slog.Info("kiro docs: created", "category", req.Category, "path", logsafe.Field(rel))
	webhttp.WriteJSONStatus(w, http.StatusCreated, kiroDocNewResponse{
		Path: strings.TrimPrefix(s.workDir, "/") + "/.kiro/" + rel,
	})
}

// createKiroDoc writes doc exclusively under the workspace's `.kiro` and returns the rel it took.
// Nothing is touched when `.kiro` resolves into, or around, the sensitive denylist; any failure
// removes what the call created, so the inventory is as it was.
func (s *Server) createKiroDoc(doc *newDoc) (string, error) {
	kiroDir, existed, err := resolveKiroDir(s.workDir)
	if err != nil {
		return "", err
	}
	// The root handle confines every write below kiroDir, so checking kiroDir covers them all.
	if s.sensitive.ExposedBy(kiroDir) {
		return "", errDocProtected
	}
	var tx createTx
	defer tx.close()
	rel, err := tx.write(kiroDir, existed, doc)
	if err != nil {
		return "", errors.Join(err, tx.rollback())
	}
	return rel, nil
}

// resolveKiroDir answers `<workDir>/.kiro` with every symlink followed, and whether it exists.
func resolveKiroDir(workDir string) (dir string, exists bool, err error) {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return "", false, err
	}
	ws, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", false, err
	}
	kiroDir := filepath.Join(ws, ".kiro")
	resolved, err := filepath.EvalSymlinks(kiroDir)
	switch {
	case err == nil:
		return resolved, true, nil
	case errors.Is(err, fs.ErrNotExist):
		return kiroDir, false, nil
	default:
		return "", false, err
	}
}

// createTx records what one create made, so a failure can take exactly that back.
type createTx struct {
	root    *os.Root
	kiroDir string
	file    string
	dirs    []string
}

func (tx *createTx) write(kiroDir string, existed bool, doc *newDoc) (string, error) {
	if !existed {
		if err := os.Mkdir(kiroDir, 0o755); err != nil {
			return "", err
		}
		tx.kiroDir = kiroDir
	}
	root, err := os.OpenRoot(kiroDir)
	if err != nil {
		return "", err
	}
	tx.root = root
	for _, twin := range doc.twins {
		if _, err := root.Lstat(twin); err == nil {
			return "", errDocExists
		}
	}
	if err := tx.mkdirs(path.Dir(doc.rel)); err != nil {
		return "", err
	}
	return tx.createFirstFree(doc)
}

// createFirstFree writes doc at rel, or for a numbered doc at the first free `-<n>` name, and
// refuses the first candidate whose file name is too long.
func (tx *createTx) createFirstFree(doc *newDoc) (string, error) {
	for i := 0; i == 0 || doc.numbered; i++ {
		rel := numberedRel(doc.rel, i)
		if n := len(path.Base(rel)); n > maxFileName {
			return "", fieldError(fmt.Sprintf("That name makes a %d-byte file name; the limit is %d bytes.", n, maxFileName))
		}
		created, err := tx.createExclusive(rel, doc.data)
		if err != nil {
			return "", err
		}
		if created {
			return rel, nil
		}
	}
	return "", errDocExists
}

func (tx *createTx) close() {
	if tx.root != nil {
		_ = tx.root.Close()
	}
}

func (tx *createTx) mkdirs(dir string) error {
	var p string
	for part := range strings.SplitSeq(dir, "/") {
		p = path.Join(p, part)
		err := tx.root.Mkdir(p, 0o755)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		tx.dirs = append(tx.dirs, p)
	}
	return nil
}

// createExclusive reports false, with no error, when rel is already taken.
func (tx *createTx) createExclusive(rel string, data []byte) (bool, error) {
	f, err := tx.root.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	tx.file = rel
	if err := errors.Join(writeDocData(f, data), f.Close()); err != nil {
		return false, err
	}
	return true, nil
}

func (tx *createTx) rollback() error {
	var errs []error
	if tx.file != "" {
		errs = append(errs, tx.root.Remove(tx.file))
	}
	for _, dir := range slices.Backward(tx.dirs) {
		errs = append(errs, tx.root.Remove(dir))
	}
	if tx.kiroDir != "" {
		errs = append(errs, os.Remove(tx.kiroDir))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("roll back the create: %w", err)
	}
	return nil
}

func numberedRel(rel string, i int) string {
	if i == 0 {
		return rel
	}
	ext := path.Ext(rel)
	return strings.TrimSuffix(rel, ext) + "-" + strconv.Itoa(i) + ext
}

// IDE getDefaultSteeringContent, workspace variants, byte for byte (trailing spaces included).
const (
	steeringTemplateAlways = "---\ninclusion: always\n---\n" +
		"<!------------------------------------------------------------------------------------\n" +
		"   Add rules to this file or a short description and have Kiro refine them for you.\n" +
		"   \n" +
		"   Learn about inclusion modes: https://kiro.dev/docs/steering/#inclusion-modes\n" +
		"-------------------------------------------------------------------------------------> "
	steeringTemplateManual = "---\ninclusion: manual\n---\n" +
		"<!------------------------------------------------------------------------------------\n" +
		"   This file is included only when invoked as a slash command (`/<filename>`)\n" +
		"   in chat. Use it for prompts and instructions you want to run on demand \u2014\n" +
		"   the manual replacement for user-triggered hooks.\n" +
		"\n" +
		"   Learn about inclusion modes: https://kiro.dev/docs/steering/#inclusion-modes\n" +
		"-------------------------------------------------------------------------------------> "
)

// The template kiro-cli's `/prompts create` opens the editor on.
const promptTemplate = "# Enter your prompt content here\n\nDescribe what this prompt should do..."

var steeringTemplates = map[string]string{
	"always": steeringTemplateAlways,
	"manual": steeringTemplateManual,
}

func newSteeringDoc(req *kiroDocNewRequest, _ matcherCheck) (newDoc, error) {
	tmpl, ok := steeringTemplates[req.Inclusion]
	if !ok {
		return newDoc{}, fieldError("Choose agent steering or manual steering.")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return newDoc{}, fieldError("Filename cannot be empty")
	}
	if !strings.HasSuffix(name, ".md") {
		name += ".md"
	}
	if !isFileName(name) {
		return newDoc{}, fieldError("A file name cannot contain a slash or a control character, or start with a dot.")
	}
	return newDoc{
		rel:    "steering/" + name,
		data:   []byte(tmpl),
		exists: fmt.Sprintf("A file named %q already exists.", name),
	}, nil
}

// The agentskills.io name rule KAS applies: it drops a skill whose name breaks it.
var skillNameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

const (
	maxSkillName        = 64
	maxSkillDescription = 1024
)

func newSkillDoc(req *kiroDocNewRequest, _ matcherCheck) (newDoc, error) {
	name := strings.TrimSpace(req.Name)
	if len(name) > maxSkillName || !skillNameRe.MatchString(name) {
		return newDoc{}, fieldError("Use lowercase letters, numbers and single hyphens, up to 64 characters.")
	}
	desc := strings.TrimSpace(req.Description)
	switch {
	case desc == "":
		// KAS skips a skill with no description, so an empty one would never load.
		return newDoc{}, fieldError("Description is required: Kiro matches it against your requests.")
	case utf16Len(desc) > maxSkillDescription:
		return newDoc{}, fieldError("Description must be 1,024 characters or less.")
	case strings.ContainsFunc(desc, unicode.IsControl):
		return newDoc{}, fieldError("Description must be a single line.")
	}
	body := frontMatter(yamlField{"name", name}, yamlField{"description", desc})
	return newDoc{
		rel:    "skills/" + name + "/SKILL.md",
		data:   []byte(body),
		exists: fmt.Sprintf("A skill named %q already exists.", name),
	}, nil
}

// kiro-cli's prompt-name rule and its wording.
var promptNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

const maxPromptName = 50

func newPromptDoc(req *kiroDocNewRequest, _ matcherCheck) (newDoc, error) {
	name := strings.TrimSpace(req.Name)
	switch {
	case name == "":
		return newDoc{}, fieldError("Prompt name cannot be empty. Please provide a valid name for your prompt.")
	case !promptNameRe.MatchString(name):
		return newDoc{}, fieldError("Prompt name can only contain letters, numbers, hyphens (-), and underscores (_). Special characters, spaces, and path separators are not allowed.")
	case len(name) > maxPromptName:
		return newDoc{}, fieldError(fmt.Sprintf("Prompt name must be %d characters or less. Current length: %d characters.", maxPromptName, len(name)))
	}
	return newDoc{
		rel:    "prompts/" + name + ".md",
		data:   []byte(promptTemplate),
		exists: fmt.Sprintf("Prompt '%s' already exists.", name),
	}, nil
}

// The agent-run button's rule, so every created agent can also be launched from the tab.
var agentNameRe = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*$`)

// agentSkeleton is kiro-cli's `/agent create` file; field order is the file's.
type agentSkeleton struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Prompt      string   `json:"prompt"`
	Tools       []string `json:"tools"`
}

func newAgentDoc(req *kiroDocNewRequest, _ matcherCheck) (newDoc, error) {
	name := strings.TrimSpace(req.Name)
	switch {
	case name == "":
		return newDoc{}, fieldError("Agent name is required.")
	case !agentNameRe.MatchString(name):
		return newDoc{}, fieldError("Agent name can only contain letters, numbers, dots, hyphens and underscores.")
	}
	data, err := jsonFile(agentSkeleton{Name: name, Tools: []string{}})
	if err != nil {
		return newDoc{}, err
	}
	// kiro-cli refuses when either spelling exists; the reply names the JSON it would have written.
	rel := "agents/" + name + ".json"
	return newDoc{
		rel:    rel,
		twins:  []string{"agents/" + name + ".md"},
		data:   data,
		exists: "File already exists at .kiro/" + rel + ". Aborting",
	}, nil
}

// The IDE hook editor's trigger list.
var hookTriggers = map[string]bool{
	"PostFileCreate": true, "PostFileSave": true, "PostFileDelete": true,
	"PreToolUse": true, "PostToolUse": true,
	"UserPromptSubmit": true, "SessionStart": true, "Stop": true,
	"PreTaskExec": true, "PostTaskExec": true,
}

// hookEntry and hookAction are the IDE's toAgentHookDocument; field order is its key order.
//
//nolint:govet // fieldalignment: the declaration order is the written file's key order.
type hookEntry struct {
	Name        string     `json:"name"`
	Trigger     string     `json:"trigger"`
	Action      hookAction `json:"action"`
	Description string     `json:"description,omitempty"`
	Matcher     string     `json:"matcher,omitempty"`
	Timeout     *int       `json:"timeout,omitempty"`
	Enabled     bool       `json:"enabled"`
}

// A hook action's type, as the wire and the file both spell it.
const (
	hookActionCommand = "command"
	hookActionAgent   = "agent"
)

type hookAction struct {
	Type    string `json:"type"`
	Command string `json:"command,omitempty"`
	Prompt  string `json:"prompt,omitempty"`
}

type hookFile struct {
	Version string      `json:"version"`
	Hooks   []hookEntry `json:"hooks"`
}

func newHookDoc(req *kiroDocNewRequest, accepts matcherCheck) (newDoc, error) {
	entry, err := hookEntryFrom(req, accepts)
	if err != nil {
		return newDoc{}, err
	}
	data, err := jsonFile(hookFile{Version: "v1", Hooks: []hookEntry{entry}})
	if err != nil {
		return newDoc{}, err
	}
	// The suffix search never ends on a taken name: the next number is always free.
	return newDoc{
		rel:      "hooks/" + cmp.Or(hookSlug(entry.Name), "hook") + ".json",
		data:     data,
		numbered: true,
	}, nil
}

func hookEntryFrom(req *kiroDocNewRequest, accepts matcherCheck) (hookEntry, error) {
	name := strings.TrimSpace(req.Name)
	switch {
	case name == "":
		return hookEntry{}, fieldError("Hook name is required.")
	case !hookTriggers[req.Trigger]:
		return hookEntry{}, fieldError("Choose a trigger.")
	}
	entry := hookEntry{
		Name:        name,
		Trigger:     req.Trigger,
		Description: strings.TrimSpace(req.Description),
		Enabled:     true,
	}
	content := strings.TrimSpace(req.Content)
	switch req.Action {
	case hookActionCommand:
		if content == "" {
			return hookEntry{}, fieldError("A command is required for the Run Command action.")
		}
		if req.Timeout != nil && *req.Timeout < 0 {
			return hookEntry{}, fieldError("Timeout must be 0 seconds or more.")
		}
		entry.Action = hookAction{Type: hookActionCommand, Command: content}
		entry.Timeout = req.Timeout
	case hookActionAgent:
		if content == "" {
			return hookEntry{}, fieldError("Instructions are required for the Ask Kiro action.")
		}
		entry.Action = hookAction{Type: hookActionAgent, Prompt: content}
	default:
		return hookEntry{}, fieldError("Choose Ask Kiro or Run Command.")
	}
	// Last, so a request the cheap checks refuse never starts the runtime.
	matcher, err := hookMatcher(req, accepts)
	if err != nil {
		return hookEntry{}, err
	}
	entry.Matcher = matcher
	return entry, nil
}

// hookMatcher is the request's trimmed matcher, dropped for a trigger with no subject to match.
func hookMatcher(req *kiroDocNewRequest, accepts matcherCheck) (string, error) {
	subject, _ := marotte.HookTriggerSubject(req.Trigger)
	m := strings.TrimSpace(req.Matcher)
	if subject == marotte.HookMatcherSubjectNone || m == "" {
		return "", nil
	}
	ok, err := accepts(subject, m)
	switch {
	case err != nil:
		return "", err
	case !ok:
		return "", fieldError("Matcher must be a valid regular expression.")
	}
	return m, nil
}

var nonSlugRun = regexp.MustCompile(`[^a-z0-9]+`)

// hookSlug is the IDE's slugify: lowercase, runs of anything else to one hyphen, trimmed.
func hookSlug(name string) string {
	return strings.Trim(nonSlugRun.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-"), "-")
}

// jsonFile is JSON.stringify(v, null, 2) plus a newline: two-space indent, no HTML escaping.
func jsonFile(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// isFileName reports whether name is one safe path segment: no separator, NUL or control
// character, no leading dot (which also rules out "." and "..").
func isFileName(name string) bool {
	return name != "" && !strings.HasPrefix(name, ".") &&
		!strings.ContainsAny(name, `/\`) && !strings.ContainsFunc(name, unicode.IsControl)
}

// utf16Len is s's length in UTF-16 code units: JavaScript's String length, which KAS's
// limits and the form's maxlength count.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// yamlField is one key of a front-matter block.
type yamlField struct{ key, value string }

// frontMatter is a YAML front-matter block whose every value is a double-quoted string, so
// KAS reads each back as that exact string and never as a number, bool or null. A value must
// hold no control character; the caller refuses those.
func frontMatter(fields ...yamlField) string {
	var b strings.Builder
	b.WriteString("---\n")
	for _, f := range fields {
		b.WriteString(f.key + `: "` + yamlDoubleQuoted.Replace(f.value) + "\"\n")
	}
	b.WriteString("---\n")
	return b.String()
}

// U+FFFE and U+FFFF are outside YAML's printable set (YAML 1.2 §5.1), so they go escaped.
var yamlDoubleQuoted = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\uFFFE", `\uFFFE`, "\uFFFF", `\uFFFF`)
