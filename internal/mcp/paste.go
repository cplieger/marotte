package mcp

// Pasting a publisher's block: the Claude-Desktop/KAS-family JSON map of servers a README hands
// out, translated into marotte's records as the inverse of kasfile.go's renderKASServers. Keys fall
// in three classes: consumed, known-but-unmodelled (accepted with a note), and unknown (400 naming
// the key and its nearest match), since encoding/json would silently drop a typo like "comand".

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

const (
	// A README lists two or three servers; a block naming more than this is not something to install
	// in one gesture.
	maxImportServers = 32
	// importSuggestDistance is the edit-distance ceiling for a "did you mean"
	// hint. 2 catches a transposition or a dropped letter ("comand", "agrs")
	// without pairing unrelated short keys ("url" / "env" are distance 3).
	importSuggestDistance = 2
	// maxImportBlockKeys bounds the key count of any one object before it is
	// classified, so a hostile paste cannot make the sort dominate the request.
	maxImportBlockKeys = 128
)

// `name` is here because a single-server paste (the panel's own template) carries its name inside
// the object, where a block carries it as the map key.
var pasteServerKeys = []string{
	"args", "command", "disabled", "disabledTools",
	"env", "headers", "name", "oauth", "prewarm", "timeout", "type", "url",
	"waitForReady",
}

// Each is accepted with a note naming why, so a block carrying one installs instead of erroring,
// and the user is not left wondering whether it was a typo. The reasons are the user's, not the
// schema's: "no field for it" is actionable, "unknown key" would not be.
var pasteServerIgnored = map[string]string{
	"$schema":     "a schema pointer, not configuration",
	"alwaysAllow": "marotte does not pre-approve MCP tools, so every call asks for permission",
	"autoApprove": "marotte does not pre-approve MCP tools, so every call asks for permission",
	"cwd":         "marotte has no working-directory field",
	"description": "not stored, because the name is the label",
	"icon":        "not stored",
	"oauthScopes": "marotte has no OAuth scope field",
}

// pasteOAuthKeys are the keys of a server's nested `oauth` object: KAS's closed
// schema. A scope list is the sibling `oauthScopes`, classified above.
//
// The nested object needs its own classification pass: encoding/json drops a
// key with no matching field, so without one a misspelt `clientIdd` would be
// accepted and discarded silently.
var pasteOAuthKeys = []string{"clientId", "clientMetadataUrl", "redirectUri"}

// pasteOAuthSecretKey is refused by name, before the typo pass: a confidential
// client cannot authenticate through KAS, so the server would fail at its token
// endpoint with invalid_client rather than at the paste.
const pasteOAuthSecretKey = "clientSecret"

const confidentialClientRefusal = "Kiro's v3 engine has no OAuth client secret, so it supports public pre-registered clients only"

// Only the wrapper is consumed; a single-server object is detected by the wrapper's absence.
var pasteTopKeys = []string{kasServerKey}

var pasteTopIgnored = map[string]string{
	"$schema": "a schema pointer, not configuration",
	"inputs":  "an editor's input-prompt list. Type the values into the form instead",
}

// importRequest is one parsed paste: the records to create, in the order the
// block declared them, plus the notes the translation produced (an unmodelled
// key, a name that had to be adjusted).
type importRequest struct {
	servers []*Server
	notes   []string
}

type pasteOAuth struct {
	ClientID          string `json:"clientId"`
	ClientMetadataURL string `json:"clientMetadataUrl"`
	RedirectURI       string `json:"redirectUri"`
}

// `env` and `headers` are absent on purpose: they are JSON records, and marotte stores ordered
// KeyPairs, so they are decoded from the raw bytes to keep the README's order (a Go map would
// discard it, and the order is what the user reads in the form).
type pasteServer struct {
	OAuth         *pasteOAuth `json:"oauth"`
	Command       *string     `json:"command"`
	URL           *string     `json:"url"`
	Type          *string     `json:"type"`
	Disabled      *bool       `json:"disabled"`
	Prewarm       *bool       `json:"prewarm"`
	WaitForReady  *bool       `json:"waitForReady"`
	Timeout       *int        `json:"timeout"`
	Args          []string    `json:"args"`
	DisabledTools []string    `json:"disabledTools"`
}

// Two shapes are accepted, because a user pastes whatever the README gave them: a `mcpServers`
// block (one or more servers, keyed by name) or a single server object carrying its own `name`.
func parseImportBody(data []byte) (*importRequest, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("body must be a JSON object: %w", err)
	}
	if len(doc) == 0 {
		return nil, errors.New(`empty object: paste the "mcpServers" block from the server's README`)
	}
	// Non-nil so the response carries [] rather than null: the client's type says
	// an array, and one shape on the wire beats two the reader has to handle.
	req := &importRequest{notes: []string{}}
	block, isBlock := doc[kasServerKey]
	if !isBlock {
		return parseSingleServer(doc, data, req)
	}
	if err := classifyKeys("", doc, pasteTopKeys, pasteTopIgnored, req); err != nil {
		return nil, err
	}
	return parseServerBlock(block, req)
}

// parseSingleServer handles the one-object shape: the whole body is a server
// and its name is a field on it.
func parseSingleServer(doc map[string]json.RawMessage, raw json.RawMessage, req *importRequest) (*importRequest, error) {
	var named struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &named); err != nil {
		return nil, errors.New(`"name" must be a string`)
	}
	if strings.TrimSpace(named.Name) == "" {
		return nil, errors.New(`missing "name": paste an "mcpServers" block, or a single server object with a name`)
	}
	sv, err := translateServer(named.Name, doc, raw, req)
	if err != nil {
		return nil, err
	}
	req.servers = append(req.servers, sv)
	return req, nil
}

// Entries are translated in the block's own key order (sorted, since a JSON object has none once
// decoded) so a re-paste of the same block produces the same order.
func parseServerBlock(block json.RawMessage, req *importRequest) (*importRequest, error) {
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(block, &entries); err != nil {
		return nil, fmt.Errorf(`"%s" must be a JSON object of servers keyed by name: %w`, kasServerKey, err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf(`"%s" is empty: no servers to connect`, kasServerKey)
	}
	if len(entries) > maxImportServers {
		return nil, fmt.Errorf(`"%s" names %d servers (max %d)`, kasServerKey, len(entries), maxImportServers)
	}
	// Entries are translated in the block's own key order (sorted, since a JSON
	// object has none once decoded), so a re-paste of the same block produces
	// the same order.
	for _, key := range slices.Sorted(maps.Keys(entries)) {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(entries[key], &obj); err != nil {
			return nil, fmt.Errorf("server %q: must be a JSON object: %w", key, err)
		}
		sv, err := translateServer(key, obj, entries[key], req)
		if err != nil {
			return nil, err
		}
		req.servers = append(req.servers, sv)
	}
	return req, nil
}

// validate is NOT called here: the store calls it on every record it takes, and running it twice
// would report the same problem in two voices.
func translateServer(rawName string, obj map[string]json.RawMessage, raw json.RawMessage, req *importRequest) (*Server, error) {
	name, nameErr := importName(rawName, req)
	if nameErr != nil {
		return nil, nameErr
	}
	where := fmt.Sprintf("server %q: ", name)
	if keyErr := classifyKeys(where, obj, pasteServerKeys, pasteServerIgnored, req); keyErr != nil {
		return nil, keyErr
	}
	// Classified before the decode below, and regardless of transport: the outer
	// pass only sees that `oauth` is a consumed key, so the object's own members
	// would otherwise reach json.Unmarshal's silent-drop.
	if oauthErr := classifyOAuthKeys(where, obj["oauth"], req); oauthErr != nil {
		return nil, oauthErr
	}
	var spec pasteServer
	if decErr := json.Unmarshal(raw, &spec); decErr != nil {
		return nil, fmt.Errorf("%s%w", where, decErr)
	}
	transport, err := transportFor(&spec)
	if err != nil {
		return nil, fmt.Errorf("%s%w", where, err)
	}
	sv := &Server{
		Name:          name,
		Transport:     transport,
		Args:          spec.Args,
		DisabledTools: spec.DisabledTools,
		// A publisher block spells the flag the other way round, and marotte's
		// own default is on: a server nobody switched off is one the user just
		// asked for.
		Enabled:      spec.Disabled == nil || !*spec.Disabled,
		WaitForReady: spec.WaitForReady != nil && *spec.WaitForReady,
	}
	if spec.Timeout != nil {
		sv.TimeoutMS = *spec.Timeout
	}
	switch transport {
	case TransportStdio:
		sv.Command = strings.TrimSpace(*spec.Command)
		if sv.Env, err = decodeOrderedPairs(where+"env", obj["env"]); err != nil {
			return nil, err
		}
		sv.Prewarm = prewarmFor(&spec, sv.Command)
	case TransportHTTP, TransportSSE:
		sv.URL = strings.TrimSpace(*spec.URL)
		if sv.Headers, err = decodeOrderedPairs(where+"headers", obj["headers"]); err != nil {
			return nil, err
		}
		if spec.OAuth != nil {
			sv.OAuthClientID = strings.TrimSpace(spec.OAuth.ClientID)
			sv.OAuthClientMetadataURL = strings.TrimSpace(spec.OAuth.ClientMetadataURL)
			sv.OAuthRedirectURI = strings.TrimSpace(spec.OAuth.RedirectURI)
		}
	}
	return sv, nil
}

// transportFor infers the transport the way KAS does: from which fields are
// present. `type` is advisory — KAS accepts it and ignores it — so it decides
// only http-versus-sse for a remote server, and an unrecognised value falls
// through to http rather than failing, which is what KAS's own negotiation does.
func transportFor(spec *pasteServer) (Transport, error) {
	cmd := strings.TrimSpace(deref(spec.Command))
	remote := strings.TrimSpace(deref(spec.URL))
	switch {
	case cmd != "" && remote != "":
		return "", errors.New(`has both "command" and "url". A server is either local with a command or hosted at a url`)
	case cmd != "":
		return TransportStdio, nil
	case remote != "":
		if t, ok := supportedRemoteTypes[strings.ToLower(deref(spec.Type))]; ok {
			return t, nil
		}
		return TransportHTTP, nil
	default:
		return "", errors.New(`needs either "command" (a local server) or "url" (a hosted one)`)
	}
}

// prewarmFor mirrors the npm form's default: an npx server pays an install cost
// on the first chat after a container start unless it is pre-installed, and
// nothing else is prewarm-eligible (see prewarm.extractNpxPackage). An explicit
// flag in the block wins.
func prewarmFor(spec *pasteServer, command string) bool {
	if spec.Prewarm != nil {
		return *spec.Prewarm
	}
	return command == "npx"
}

// importName maps a block key (or a single object's "name") onto a name
// the store will accept. An adjustment is REPORTED rather than made
// silently: the name becomes the agent's tool prefix, so a user whose
// README said one thing and whose tool list says another needs to be
// told.
func importName(raw string, req *importRequest) (string, error) {
	trimmed := strings.TrimSpace(raw)
	clean := sanitizeName(trimmed)
	if clean == "" {
		return "", fmt.Errorf("server %q: name needs at least one letter", trimmed)
	}
	if clean != trimmed {
		req.notes = append(req.notes,
			fmt.Sprintf("named %q %q: a name starts with a letter and holds letters, digits, %q and %q",
				trimmed, clean, "_", "-"))
	}
	return clean, nil
}

// sanitizeName folds a raw name into the shared grammar (outside nameAllowedRune becomes "-",
// trimmed to open on a lead rune, capped at nameMaxLen), so a README's `@scope/pkg` installs.
// TestSanitizeNameAlwaysValid asserts validateName accepts every output.
func sanitizeName(raw string) string {
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		if nameAllowedRune(r) {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('-')
	}
	out := strings.TrimFunc(b.String(), func(r rune) bool { return !nameLeadRune(r) })
	// TrimFunc only strips the ends, so the leading rune is now a lead rune (or
	// the string is empty). Trailing separators went with it, which is the shape
	// the grammar wants anyway. The cap is applied last and is byte-safe: every
	// kept rune is single-byte ASCII by nameAllowedRune's construction.
	if len(out) > nameMaxLen {
		out = out[:nameMaxLen]
	}
	return out
}

// classifyOAuthKeys runs the same three-way classification over a server's
// nested `oauth` object, so a typo there is NAMED like every other one instead
// of being dropped into an empty credential. Absent or null yields nothing to
// classify; a non-object is named here rather than surfacing later as a decode
// error about the whole server.
func classifyOAuthKeys(where string, raw json.RawMessage, req *importRequest) error {
	if len(raw) == 0 {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return fmt.Errorf(`%s"oauth" must be a JSON object: %w`, where, err)
	}
	if _, ok := obj[pasteOAuthSecretKey]; ok {
		return fmt.Errorf("%soauth: %q is not supported. %s", where, pasteOAuthSecretKey, confidentialClientRefusal)
	}
	return classifyKeys(where+"oauth: ", obj, pasteOAuthKeys, nil, req)
}

// classifyKeys splits an object's keys into the ones the translator consumes,
// the ones it recognises and cannot store (recorded as a note), and the rest —
// which is a typo, and is named. `where` prefixes the message with the server
// it came from; it is empty at the top level.
func classifyKeys(where string, obj map[string]json.RawMessage, consumed []string, ignored map[string]string, req *importRequest) error {
	if len(obj) > maxImportBlockKeys {
		return fmt.Errorf("%s%d keys (max %d)", where, len(obj), maxImportBlockKeys)
	}
	unknown := make([]string, 0, len(obj))
	notes := make([]string, 0, len(obj))
	for key := range obj {
		switch {
		case slices.Contains(consumed, key):
		case ignored[key] != "":
			notes = append(notes, fmt.Sprintf("%signoring %q: %s", where, key, ignored[key]))
		default:
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		// Sorted so a block with several typos names the same one every time;
		// a caller fixing them one at a time needs a stable answer.
		slices.Sort(unknown)
		return fmt.Errorf("%sunknown key %q%s", where, unknown[0],
			suggestKey(unknown[0], consumed, ignored))
	}
	slices.Sort(notes)
	req.notes = append(req.notes, notes...)
	return nil
}

// This is what turns "comand" from a rejection into a fix.
func suggestKey(got string, consumed []string, ignored map[string]string) string {
	lower := strings.ToLower(got)
	best, bestDist := "", importSuggestDistance+1
	candidates := slices.AppendSeq(slices.Clone(consumed), maps.Keys(ignored))
	slices.Sort(candidates)
	for _, cand := range candidates {
		if d := editDistance(lower, strings.ToLower(cand)); d < bestDist {
			best, bestDist = cand, d
		}
	}
	if best == "" {
		return ""
	}
	return fmt.Sprintf(" (did you mean %q?)", best)
}

// editDistance is Levenshtein over two short ASCII-ish keys, computed with one rolling row. Only
// ever called on JSON object keys, so the quadratic cost is bounded by maxImportBlockKeys and the
// key length cap the body limit implies.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, min(cur[j-1]+1, prev[j-1]+cost))
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// decodeOrderedPairs reads a JSON record into ordered KeyPairs (nil when absent), stringifying
// scalars and naming an object, array or null as a mistake. The count is bounded here: an env
// object at the 1 MiB body cap decoded to 174,762 pairs and 39.4 MB before reaching the store's
// limit (go1.27.0).
func decodeOrderedPairs(field string, raw json.RawMessage) ([]keyPair, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", field, err)
	}
	if tok != json.Delim('{') {
		return nil, fmt.Errorf("%s must be a JSON object of name/value pairs", field)
	}
	var out []keyPair
	for dec.More() {
		if len(out) >= maxImportBlockKeys {
			return nil, fmt.Errorf("%s: more than %d entries", field, maxImportBlockKeys)
		}
		keyTok, kErr := dec.Token()
		if kErr != nil {
			return nil, fmt.Errorf("%s: %w", field, kErr)
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("%s: non-string key", field)
		}
		var val any
		if vErr := dec.Decode(&val); vErr != nil {
			return nil, fmt.Errorf("%s[%q]: %w", field, key, vErr)
		}
		text, ok := scalarString(val)
		if !ok {
			return nil, fmt.Errorf("%s[%q]: value must be a string, number or boolean", field, key)
		}
		out = append(out, keyPair{Name: key, Value: text})
	}
	return out, nil
}

// The bool reports whether the value was a scalar at all.
func scalarString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case json.Number:
		return t.String(), true
	case bool:
		if t {
			return "true", true
		}
		return "false", true
	default:
		return "", false
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
