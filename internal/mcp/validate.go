package mcp

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Package-level errors. All HTTP handlers map these to 4xx responses;
// anything else is a 500. errPersist wraps the underlying filesystem
// error from SaveJSON's temp+rename so writeErr can route mutator
// failures to 500 with a generic body (no filesystem path leaked to
// the browser) while the full error still shows up in slog.
var (
	errNotFound     = errors.New("server not found")
	errNameConflict = errors.New("server name already exists")
	errPersist      = errors.New("persist failed")

	// ErrPersistMarshal and ErrPersistWrite sub-classify errPersist (errors.Is still matches it), so
	// the HTTP layer logs a marshal bug and a transient write failure at different levels.
	errPersistMarshal = fmt.Errorf("%w: marshal", errPersist)
	errPersistWrite   = fmt.Errorf("%w: write", errPersist)
)

// nameMaxLen is the byte bound on a server name.
//
// The name becomes the agent's tool prefix (mcp_<name>_<tool>), so the bound is
// a property of that namespace rather than of any one admission door — which is
// why it is exported beside the validator instead of appearing as a literal in
// each caller.
const nameMaxLen = 64

// nameLeadRune reports whether r may open a name. Deliberately ASCII-only: the
// name becomes the agent's tool prefix, and a non-ASCII prefix is not something
// the tool namespace accepts.
//
// This and nameAllowedRune are the ONLY executable statement of the charset in the
// package: a second copy (a regexp, a hard-coded grammar string) drifts from it.
func nameLeadRune(r rune) bool {
	return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z'
}

// nameAllowedRune reports whether r may appear anywhere in a name (position 2
// onward). The leading position is narrower — see nameLeadRune.
func nameAllowedRune(r rune) bool {
	return nameLeadRune(r) || r >= '0' && r <= '9' || r == '_' || r == '-'
}

// nameGrammar is prose built from nameMaxLen, so it cannot drift into a second grammar restating
// the charset or the bound.
func nameGrammar() string {
	return "a letter, then letters, digits, underscores or hyphens, up to " +
		strconv.Itoa(nameMaxLen) + " characters"
}

// validateName is the ONE admission rule for a server name, implemented
// directly from the rune predicates and the length constant.
//
// Three doors reach a name and they must agree: Validate, parseServerID,
// and paste.go's sanitizeName (which REPAIRS rather than rejects). All
// three read the same two predicates, so agreement is structural.
func validateName(name string) error {
	if err := checkName(name); err != nil {
		return &fieldError{Field: fieldName, Msg: err.Error()}
	}
	return nil
}

// checkName is the rule itself, returning a plain error so the attribution wrapper
// above is the only place that knows about form fields.
func checkName(name string) error {
	if name == "" {
		return fmt.Errorf("name must be %s: %q", nameGrammar(), name)
	}
	if len(name) > nameMaxLen {
		return fmt.Errorf("name too long: %d bytes (max %d)", len(name), nameMaxLen)
	}
	for i, r := range name {
		ok := nameAllowedRune(r)
		if i == 0 {
			ok = nameLeadRune(r)
		}
		if !ok {
			return fmt.Errorf("name must be %s: %q", nameGrammar(), name)
		}
	}
	return nil
}

// Permissive enough for both (env disallows "-", headers disallow "_" on paper; in practice both
// fly everywhere and we let the server/ kiro-cli be the final judge).
var keyRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,127}$`)

// Length caps on user-supplied fields. These are defense-in-depth
// bounds: webhttp.MaxJSONBody caps the whole PUT payload, but a single
// 500 KB env value still slows every masked read and bloats mcp.json.
// Values are generous enough to accept any realistic MCP config.
const (
	commandMax       = 512
	urlMax           = 2048
	argMax           = 4096
	maxArgs          = 64
	envValueMax      = 32 * 1024 // 32 KiB
	maxEnvEntries    = 64
	headerValueMax   = 8 * 1024 // 8 KiB
	maxHeaderEntries = 32
	disabledToolMax  = 128
	maxDisabledTools = 256
	// Real-world client IDs are typically 20–80 chars (UUID-ish or app-id-ish); 256 leaves headroom
	// and rejects clearly-malformed input.
	oauthClientIDMax = 256
	// oauthRedirectMinPort is the OAuth relay's own floor (agent relayMinPort):
	// a privileged pin installs, then fails at the relay's paste-back step.
	oauthRedirectMinPort = 1024
)

// oauthRedirectHosts are the loopback hosts KAS serves its OAuth callback on.
var oauthRedirectHosts = []string{"localhost", "127.0.0.1"}

// Adding a new transport requires only a map entry, not a control-flow change. The init() below
// validates that every known transport has a registered validator, preventing nil-call panics.
// TransportSSE shares validateRemote with TransportHTTP: both are remote transports whose wire
// shape is url + headers (+ optional oauth), differing only in the ACP `type` discriminator emitted
// at export time.
var transportValidators = map[Transport]func(*Server) error{
	TransportStdio:    validateStdio,
	TransportHTTP:     validateRemote,
	TransportSSE:      validateRemote,
	TransportRegistry: validateRegistry,
}

func init() {
	for _, t := range []Transport{TransportStdio, TransportHTTP, TransportSSE, TransportRegistry} {
		if _, ok := transportValidators[t]; !ok {
			panic("mcp: no validator registered for transport " + string(t))
		}
	}
}

// Wire field names, used for error attribution.
//
// Named rather than spelled at each site because a field name is a CONTRACT with
// the form: the client's field-to-input map keys on exactly these strings, so a
// typo here does not fail a build, it silently stops one input being marked.
const (
	fieldName          = "name"
	fieldTransport     = "transport"
	fieldCommand       = "command"
	fieldArgs          = "args"
	fieldURL           = "url"
	fieldEnvPairs      = "env"
	fieldHeaderPairs   = "headers"
	fieldDisabledTools = "disabled_tools"
	fieldTimeoutMS     = "timeout_ms"
	fieldOAuthClientID = "oauth_client_id"

	fieldOAuthClientMetadataURL = "oauth_client_metadata_url"
	fieldOAuthRedirectURI       = "oauth_redirect_uri"
)

// fieldError is one validation failure, attributed to the wire field it
// came from. Msg is unchanged from what the check always said — an
// indexed message like `headers[1]: duplicate name "X"` keeps its
// index.
type fieldError struct {
	Field string `json:"field"`
	Msg   string `json:"message"`
}

func (e *fieldError) Error() string { return e.Msg }

// maxFieldErrors bounds one response's error list: accumulation turns a
// per-entry check into a per-entry ALLOCATION, so a paste naming
// thousands of bad tool names would otherwise build thousands of
// messages.
const maxFieldErrors = 32

// fieldErrs accumulates independent validation failures.
//
// Two verbs, and the difference is the whole point. addf() attributes a LEAF
// check to its field. merge() splices an error that is already attributed —
// including a joined one from a sub-validator — so nesting keeps every inner
// field instead of collapsing the group under one outer name.
type fieldErrs struct {
	errs []error
}

func (c *fieldErrs) addf(field, format string, args ...any) {
	if len(c.errs) >= maxFieldErrors {
		return
	}
	c.errs = append(c.errs, &fieldError{Field: field, Msg: fmt.Sprintf(format, args...)})
}

func (c *fieldErrs) merge(err error) {
	if err == nil || len(c.errs) >= maxFieldErrors {
		return
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range joined.Unwrap() {
			c.merge(e)
		}
		return
	}
	c.errs = append(c.errs, err)
}

func (c *fieldErrs) any() bool { return len(c.errs) > 0 }

// join returns the accumulated failures as one error, or nil.
//
// errors.Join is the idiomatic fit and needs no dependency: its Error() is the
// newline-joined messages (so every existing substring assertion still matches)
// and errors.Is/As walk into it (so the sentinels the HTTP layer routes on keep
// working through a wrap).
func (c *fieldErrs) join() error { return errors.Join(c.errs...) }

// fieldErrors flattens an error tree into the field failures it carries,
// for a caller that wants to mark inputs rather than print a paragraph.
//
// It walks BOTH wrap shapes: errors.Join's Unwrap() []error, and
// fmt.Errorf("%w")'s Unwrap() error.
func fieldErrors(err error) []fieldError {
	var out []fieldError
	var walk func(error)
	walk = func(e error) {
		if e == nil || len(out) >= maxFieldErrors {
			return
		}
		// The concrete type, deliberately, and not errors.As: As descends the
		// whole tree and would return the FIRST fieldError under a join, so the
		// walk below would never run and a three-field failure would report one.
		// A fieldError wraps nothing, so this branch terminates.
		if fe, ok := e.(*fieldError); ok {
			out = append(out, *fe)
			return
		}
		switch u := e.(type) {
		case interface{ Unwrap() []error }:
			for _, inner := range u.Unwrap() {
				walk(inner)
			}
		case interface{ Unwrap() error }:
			walk(u.Unwrap())
		}
	}
	walk(err)
	return out
}

// validate checks a fully-populated Server record before every create or update persists, the
// single source of truth. It ACCUMULATES problems, short-circuiting only on an unknown transport,
// whose per-transport check cannot run.
func validate(s *Server) error {
	var errs fieldErrs
	errs.merge(validateName(s.Name))
	errs.merge(validateTransportChain(s))
	errs.merge(validateToolNames(fieldDisabledTools, s.DisabledTools))
	if s.TimeoutMS < 0 || s.TimeoutMS > maxTimeoutMS {
		errs.addf(fieldTimeoutMS, "timeout_ms must be between 0 and %d, got %d", maxTimeoutMS, s.TimeoutMS)
	}
	return errs.join()
}

// KAS clamps a larger timeout silently, so a value past it would be stored as one number and
// enforced as another.
const maxTimeoutMS = 600_000

// validateTransportChain is the dependent run: each step's input is the previous
// step's verdict, so a failure ends the chain instead of joining a list.
func validateTransportChain(s *Server) error {
	if s.Transport == "" {
		return &fieldError{Field: fieldTransport, Msg: "transport required"}
	}
	if !s.Transport.valid() {
		return &fieldError{Field: fieldTransport, Msg: fmt.Sprintf("unknown transport: %q", s.Transport)}
	}
	fn, ok := transportValidators[s.Transport]
	if !ok {
		// Unreachable: init() panics on a known transport with no validator, and Valid refused the
		// rest. Not accumulated, since it describes a state the process cannot reach.
		return &fieldError{
			Field: fieldTransport,
			Msg:   fmt.Sprintf("no validator registered for transport %q", s.Transport),
		}
	}
	return fn(s)
}

// hasCtl reports whether s contains any C0 control character
// (U+0000..U+001F) or DEL (U+007F). Intentionally rejects \t — RFC 7230
// forbids non-VCHAR/SP/HTAB in header values, so this errs strict. The
// byte-wise scan is correct: every byte of a UTF-8 continuation is >
// 0x7F, so multi-byte runes can never trigger a false positive.
func hasCtl(s string) bool {
	for i := range len(s) {
		c := s[i]
		if c < 0x20 || c == 0x7F {
			return true
		}
	}
	return false
}

// validateToolNames enforces the shared shape rules for a list of MCP
// tool names (disabled_tools): bounded count, no control
// characters, per-entry length cap. field names the list in errors.
//
// The count cap and the per-entry checks are independent, and so is each entry
// from the next, so all of them accumulate. Within one entry the two checks are
// independent too — a name can be both control-bearing and oversize, and saying
// so once per problem is the point.
func validateToolNames(field string, tools []string) error {
	var errs fieldErrs
	if len(tools) > maxDisabledTools {
		errs.addf(field, "%s: too many entries (%d, max %d)",
			field, len(tools), maxDisabledTools)
	}
	for i, t := range tools {
		if hasCtl(t) {
			errs.addf(field, "%s[%d]: control character", field, i)
		}
		if len(t) > disabledToolMax {
			errs.addf(field, "%s[%d]: too long (%d bytes, max %d)",
				field, i, len(t), disabledToolMax)
		}
	}
	return errs.join()
}

func validateStdio(s *Server) error {
	var errs fieldErrs
	errs.merge(validateCommand(s.Command))
	// One error per present field: the checks are independent, and attribution exists to mark
	// every wrong input, not one of them.
	if s.URL != "" {
		errs.addf(fieldURL, "stdio transport cannot have url")
	}
	if len(s.Headers) > 0 {
		errs.addf(fieldHeaderPairs, "stdio transport cannot have headers")
	}
	if s.OAuthClientID != "" {
		errs.addf(fieldOAuthClientID, "stdio transport cannot have oauth_client_id")
	}
	if s.OAuthClientMetadataURL != "" {
		errs.addf(fieldOAuthClientMetadataURL, "stdio transport cannot have oauth_client_metadata_url")
	}
	if s.OAuthRedirectURI != "" {
		errs.addf(fieldOAuthRedirectURI, "stdio transport cannot have oauth_redirect_uri")
	}
	errs.merge(validateArgs(s.Args))
	errs.merge(validateKeyPairs(fieldEnvPairs, s.Env, maxEnvEntries, envValueMax, false))
	return errs.join()
}

// validateCommand is a DEPENDENT chain within one field: "command required"
// precedes the control-character and length checks on the same value, because
// those two have nothing to say about a value that is not there.
func validateCommand(command string) error {
	if strings.TrimSpace(command) == "" {
		return &fieldError{Field: fieldCommand, Msg: "command required for stdio transport"}
	}
	var errs fieldErrs
	if hasCtl(command) {
		errs.addf(fieldCommand, "command contains a control character")
	}
	if len(command) > commandMax {
		errs.addf(fieldCommand, "command too long: %d bytes (max %d)", len(command), commandMax)
	}
	return errs.join()
}

// validateArgs accumulates the count cap and every per-entry failure: one arg
// being wrong says nothing about the next.
func validateArgs(args []string) error {
	var errs fieldErrs
	if len(args) > maxArgs {
		errs.addf(fieldArgs, "args: too many entries (%d, max %d)", len(args), maxArgs)
	}
	for i, a := range args {
		if hasCtl(a) {
			errs.addf(fieldArgs, "args[%d] contains a control character", i)
		}
		if len(a) > argMax {
			errs.addf(fieldArgs, "args[%d] too long: %d bytes (max %d)", i, len(a), argMax)
		}
	}
	return errs.join()
}

// validateRegistry refuses every field the catalog supplies: a registry entry
// names a catalog server and KAS resolves the rest.
func validateRegistry(s *Server) error {
	var errs fieldErrs
	for _, f := range []struct {
		field string
		set   bool
	}{
		{fieldCommand, s.Command != ""},
		{fieldArgs, len(s.Args) > 0},
		{fieldEnvPairs, len(s.Env) > 0},
		{fieldURL, s.URL != ""},
		{fieldHeaderPairs, len(s.Headers) > 0},
		{fieldOAuthClientID, s.OAuthClientID != ""},
		{fieldOAuthClientMetadataURL, s.OAuthClientMetadataURL != ""},
		{fieldOAuthRedirectURI, s.OAuthRedirectURI != ""},
	} {
		if f.set {
			errs.addf(f.field, "a registry server cannot have %s: the organization's catalog supplies it", f.field)
		}
	}
	return errs.join()
}

func validateRemote(s *Server) error {
	var errs fieldErrs
	// One attribution per field, as in validateStdio: a pasted stdio block switched to remote
	// hits all three.
	if s.Command != "" {
		errs.addf(fieldCommand, "remote transport cannot have command")
	}
	if len(s.Args) > 0 {
		errs.addf(fieldArgs, "remote transport cannot have args")
	}
	if len(s.Env) > 0 {
		errs.addf(fieldEnvPairs, "remote transport cannot have env")
	}
	errs.merge(validateRemoteURL(s.URL))
	errs.merge(validateOAuthField(fieldOAuthClientID, s.OAuthClientID, oauthClientIDMax))
	errs.merge(validateOAuthMetadataURL(s.OAuthClientMetadataURL))
	errs.merge(validateOAuthRedirectURI(s.OAuthRedirectURI))
	errs.merge(validateKeyPairs(fieldHeaderPairs, s.Headers, maxHeaderEntries, headerValueMax, true))
	return errs.join()
}

// The length and control-char checks are independent of each other and accumulate; everything after
// them is SEQUENTIAL by necessity — url.Parse has to succeed before Scheme, Host and User can be
// read, and a control character makes Parse fail with a message about syntax rather than about the
// character.
func validateRemoteURL(raw string) error {
	var errs fieldErrs
	if len(raw) > urlMax {
		errs.addf(fieldURL, "url too long: %d bytes (max %d)", len(raw), urlMax)
	}
	if hasCtl(raw) {
		errs.addf(fieldURL, "url contains a control character")
	}
	if errs.any() {
		return errs.join()
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return &fieldError{
			Field: fieldURL,
			Msg:   fmt.Sprintf("url must be an absolute http(s) URL: %q", raw),
		}
	}
	if u.Scheme != schemeHTTP && u.Scheme != "https" {
		return &fieldError{
			Field: fieldURL,
			Msg:   fmt.Sprintf("url scheme must be http or https: %q", u.Scheme),
		}
	}
	// Userinfo in URL would stash a credential outside the Headers
	// secret-masking path — List() returns URL verbatim, so the
	// browser and anyone dumping mcp.json would see the token. Reject
	// at the boundary; users get a clean 400 pointing them at Headers.
	if u.User != nil {
		return &fieldError{
			Field: fieldURL,
			Msg:   "url must not contain userinfo. Use Headers for auth",
		}
	}
	return nil
}

// validateOAuthMetadataURL mirrors KAS's check on clientMetadataUrl (an https
// URL with a non-root path): a value KAS rejects drops the server from its
// config with no status, so it is refused here, naming the field.
func validateOAuthMetadataURL(raw string) error {
	if raw == "" {
		return nil
	}
	if err := validateOAuthField(fieldOAuthClientMetadataURL, raw, urlMax); err != nil {
		return err
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path == "" || u.Path == "/" {
		return &fieldError{
			Field: fieldOAuthClientMetadataURL,
			Msg:   fmt.Sprintf("oauth_client_metadata_url must be an https URL with a path: %q", raw),
		}
	}
	return nil
}

// schemeHTTP is the plain-HTTP URL scheme, which shares its spelling with
// TransportHTTP but is a different vocabulary.
const schemeHTTP = "http"

// validateOAuthRedirectURI mirrors KAS's three accepted redirectUri forms
// ("host:port", ":port", or an http URL with no query or fragment) on a
// loopback host, with the relay's port floor on top of KAS's 1-65535.
func validateOAuthRedirectURI(raw string) error {
	if raw == "" {
		return nil
	}
	if err := validateOAuthField(fieldOAuthRedirectURI, raw, urlMax); err != nil {
		return err
	}
	host, port, msg := splitOAuthRedirectURI(strings.TrimSpace(raw), raw)
	switch {
	case msg != "":
	case !slices.Contains(oauthRedirectHosts, host):
		msg = fmt.Sprintf("host must be localhost or 127.0.0.1, not %q", host)
	case port == "":
		msg = `needs a port, for example "localhost:7778"`
	default:
		n, err := strconv.Atoi(port)
		if err != nil || strings.Trim(port, "0123456789") != "" || len(port) > 5 || n < oauthRedirectMinPort || n > 65535 {
			msg = fmt.Sprintf("port must be between %d and 65535, got %q", oauthRedirectMinPort, port)
		}
	}
	if msg != "" {
		return &fieldError{Field: fieldOAuthRedirectURI, Msg: "oauth_redirect_uri " + msg}
	}
	return nil
}

// A non-empty msg is the refusal. A ":port" form keeps the loopback default host.
func splitOAuthRedirectURI(v, raw string) (host, port, msg string) {
	shape := fmt.Sprintf(`must be "host:port", ":port", or an http URL: %q`, raw)
	switch {
	case strings.Contains(v, "://"):
		u, err := url.Parse(v)
		switch {
		case err != nil:
			return "", "", shape
		case u.Scheme != schemeHTTP:
			return "", "", "must use http: the loopback callback is served over plain HTTP"
		case u.RawQuery != "" || u.Fragment != "" || u.ForceQuery:
			return "", "", "cannot carry a query string or fragment"
		}
		return strings.ToLower(u.Hostname()), u.Port(), ""
	case strings.HasPrefix(v, ":"):
		return "127.0.0.1", v[1:], ""
	}
	h, p, ok := strings.Cut(v, ":")
	if !ok || h == "" || strings.ContainsAny(h, " /@") {
		return "", "", shape
	}
	return h, p, ""
}

// validateOAuthField enforces the shared length cap and control-character
// rule for the optional oauth members. An empty value is allowed.
func validateOAuthField(field, value string, maxLen int) error {
	if value == "" {
		return nil
	}
	var errs fieldErrs
	if len(value) > maxLen {
		errs.addf(field, "%s too long: %d bytes (max %d)", field, len(value), maxLen)
	}
	if hasCtl(value) {
		errs.addf(field, "%s contains a control character", field)
	}
	return errs.join()
}

// validateKeyPairs enforces the shared shape rules for env entries and
// HTTP header entries: bounded entry count, regex-valid names, unique
// names (case-insensitive for headers, case-sensitive for env), control-
// character-free values, and a length cap per value. Error messages
// follow the "<kind>[i]: ..." format both call sites relied on, so the
// existing validate_test.go assertions continue to match substring-
// wise. See validateStdio / validateRemote for the call sites.
func validateKeyPairs(kind string, pairs []keyPair, maxEntries, maxValue int, caseInsensitiveDedup bool) error {
	var errs fieldErrs
	if len(pairs) > maxEntries {
		errs.addf(kind, "%s: too many entries (%d, max %d)",
			kind, len(pairs), maxEntries)
	}
	seen := make(map[string]struct{}, len(pairs))
	for i, kv := range pairs {
		// Every check accumulates per index, the duplicate check included. A bad name still lands
		// in `seen` under its own spelling, so a repeated bad name reports both problems.
		if !keyRe.MatchString(kv.Name) {
			errs.addf(kind, "%s[%d]: bad name %q", kind, i, kv.Name)
		}
		key := kv.Name
		if caseInsensitiveDedup {
			key = strings.ToLower(kv.Name)
		}
		if _, dup := seen[key]; dup {
			errs.addf(kind, "%s[%d]: duplicate name %q", kind, i, kv.Name)
		}
		seen[key] = struct{}{}
		if hasCtl(kv.Value) {
			errs.addf(kind, "%s[%d]: value contains a control character", kind, i)
		}
		if len(kv.Value) > maxValue {
			errs.addf(kind, "%s[%d]: value too long (%d bytes, max %d)",
				kind, i, len(kv.Value), maxValue)
		}
	}
	return errs.join()
}
