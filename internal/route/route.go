// Package route is the vocabulary a route registration and the gate share. It is a leaf:
// handler packages import it to bind, and it imports no marotte package.
package route

import (
	"net/http"
	"reflect"
	"strings"
	"time"
)

// Key names one entry: one HTTP method and the registered pattern without its method prefix.
type Key struct {
	Method string
	Path   string
}

// Kind is how an entry's response is carried.
type Kind uint8

// The kinds an entry's response is carried as.
const (
	KindJSON        Kind = iota + 1 // a JSON body, or none (204)
	KindRaw                         // bytes the handler writes itself, with its own Content-Type
	KindEventStream                 // a text/event-stream
	KindRedirect                    // a 3xx with Location
	KindWebSocket                   // an upgrade; no OpenAPI operation
)

// Class is an entry's access-row class. Every value but ClassLoopbackOperator is the string of an
// accesshttp.Class, so {Class, KeyScope} is access's route-gate row; route imports nothing from access.
type Class string

// The access classes, one per access row.
const (
	ClassPublic     Class = "public"      // no credential
	ClassSession    Class = "session"     // an owner session only; a key or the agent key is refused auth_session_required
	ClassKeyScope   Class = "key_scope"   // an owner session, or a key or the agent key holding KeyScope; else auth_scope_required
	ClassRecentAuth Class = "recent_auth" // an owner session with AuthTime inside RecentAuth; a key or the agent key is refused auth_session_required
	ClassRecovery   Class = "recovery"    // a recovery session only; any other principal is refused auth_session_required
	// ClassLoopbackOperator has no access row: loopbackOnly is its credential, and access's gate never sees it.
	ClassLoopbackOperator Class = "loopback_operator"
)

// The scopes a ClassKeyScope entry names.
const (
	ScopeChatsRead   = "chats:read"
	ScopeRunsRead    = "runs:read"
	ScopeRunsAnswer  = "runs:answer"
	ScopeRunsControl = "runs:control"
	ScopeTools       = "tools"
	ScopeMetricsRead = "metrics:read"
	ScopeNotify      = "notify"
)

// KeyScopes is every scope a ClassKeyScope entry may name.
//
//deadset:ignore DS1102 -- the login wiring passes it to access as the key scope list; until then the table tests read it
func KeyScopes() []string {
	return []string{ScopeChatsRead, ScopeRunsRead, ScopeRunsAnswer, ScopeRunsControl, ScopeTools, ScopeMetricsRead, ScopeNotify}
}

// Transport is an entry's rule for the plain-HTTP transport.
type Transport uint8

// The plain-HTTP transport rules.
const (
	TransportAny Transport = iota + 1 // both transports, no method rule
	//deadset:ignore DS1005 -- the passkey and OIDC entries the login adds carry it
	TransportHTTPSOnly // passkey and OIDC routes: plain answers access_method_needs_https
	//deadset:ignore DS1005 -- the sign-in, setup, step-up and recovery-start entries the login adds carry it
	TransportPlainSwitch // sign-in, setup, step-up, recovery start: plain needs the switch on
)

// Access is one entry's access level: Class and KeyScope are access's row, the other fields marotte's.
// Nothing else decides a class.
type Access struct {
	Class    Class
	KeyScope string // set exactly on ClassKeyScope: the scope a key or the agent key must hold
	//deadset:ignore DS1301 -- the gate's plain-HTTP method rule reads it once the login is wired; the table tests read it until then
	Transport Transport // zero is refused by the table test
	//deadset:ignore DS1301 -- the GET-safety audit: the table tests hold every GET and HEAD entry to it, and no request path reads it
	Safe       bool          // GET/HEAD only; true exactly when its audit row names no effect
	RecentAuth time.Duration // ClassRecentAuth only: 10 min, or 8 h for the shell upgrade
}

// Code is one envelope code with the status it is answered with. A code has one status
// across the whole table (wiregen's ErrorRef rule), so two owners writing one code agree on it.
type Code struct {
	Code   string
	Status int // 400 to 599
}

// CodeSet is the closed list of envelope codes one owner package writes.
type CodeSet []Code

// Header is one request header a route reads, passed to wiregen's Endpoint.Headers.
type Header struct {
	Name     string
	Arg      string // the generated client's argument, a TS identifier: sseClient for SSE-Client
	Required bool
	MinBytes int
	MaxBytes int
}

// MethodTypes are one method's types and statuses, which the registrar compares with its entry.
type MethodTypes struct {
	Params   reflect.Type // nil exactly when the pattern has no {name} wildcard; else a struct of string fields, one per wildcard
	Request  reflect.Type // nil: no body
	Query    reflect.Type // nil: the route accepts no query argument; else a struct whose json tags are the keys
	Response reflect.Type // nil: no JSON body; a named struct, or a slice of one
	Method   string
	Headers  []Header // request headers the handler reads from a caller
	Status   int      // the success status; 0 is 200, and a KindJSON route with no Response answers 204
}

// Binding is one registration: a handler, the pattern it is mounted at, its kind, and the
// request and response types per method, which the registrar compares with the table.
type Binding struct {
	Handler http.Handler
	Pattern string
	Methods []MethodTypes // one per method; a method-qualified pattern has exactly one
	Kind    Kind
	Any     bool // AnyMethod: the seven catch-all methods, no filter
}

// Entry is one endpoint of the table.
type Entry struct {
	Params      reflect.Type // the path wildcards, one struct; nil when the path has none
	Request     reflect.Type
	Query       reflect.Type // the accepted query arguments, one struct; nil when the route reads none
	Response    reflect.Type
	Name        string // stable, unique; the generated client's function name and the OpenAPI operationId
	Method      string
	Path        string
	ContentType string // KindRaw only
	Summary     string // one line, for OpenAPI
	Headers     []Header
	Owners      []CodeSet // the owner packages whose Codes this entry's handler can write
	AuthCodes   CodeSet   // the access library's own codes, set only on its route entries; nil elsewhere
	Access      Access
	Status      int
	Kind        Kind
	Any         bool // one of the seven entries of a catch-all mount
}

// Key is e's (Method, Path).
func (e *Entry) Key() Key { return Key{Method: e.Method, Path: e.Path} }

// AnyMethods are the seven methods each catch-all mount has an entry for.
func AnyMethods() []string {
	return []string{
		http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions,
	}
}

// SplitPattern splits a ServeMux pattern into its method and the rest; method is "" for a plain path.
func SplitPattern(pattern string) (method, path string) {
	if m, rest, ok := strings.Cut(pattern, " "); ok {
		return m, strings.TrimLeft(rest, " \t")
	}
	return "", pattern
}

// typeOf is T's type, or nil for struct{}: the empty body.
func typeOf[T any]() reflect.Type {
	t := reflect.TypeFor[T]()
	if t == reflect.TypeFor[struct{}]() {
		return nil
	}
	return t
}

// JSON binds a method-qualified pattern; Req or Resp struct{} means no body.
func JSON[Req, Resp any](pattern string, h http.Handler) Binding {
	m, _ := SplitPattern(pattern)
	return Binding{Pattern: pattern, Handler: h, Kind: KindJSON, Methods: []MethodTypes{Typed[Req, Resp](m)}}
}

// Typed is one method of a plain-path binding.
func Typed[Req, Resp any](method string) MethodTypes {
	return MethodTypes{Method: method, Request: typeOf[Req](), Response: typeOf[Resp]()}
}

// Methods binds a plain path for exactly the methods ms name; any other method is the registrar's 405.
func Methods(path string, h http.Handler, ms ...MethodTypes) Binding {
	return Binding{Pattern: path, Handler: h, Kind: KindJSON, Methods: ms}
}

// bodyless binds pattern with kind k: a method-qualified pattern binds its method, a plain path GET.
func bodyless(k Kind, pattern string, h http.Handler) Binding {
	m, _ := SplitPattern(pattern)
	if m == "" {
		m = http.MethodGet
	}
	return Binding{Pattern: pattern, Handler: h, Kind: k, Methods: []MethodTypes{{Method: m}}}
}

// Raw binds a handler that writes its own bytes. A plain path binds GET behind the method filter.
func Raw(pattern string, h http.Handler) Binding { return bodyless(KindRaw, pattern, h) }

// Stream binds an event stream, as Raw does.
func Stream(pattern string, h http.Handler) Binding { return bodyless(KindEventStream, pattern, h) }

// WebSocket binds an upgrade, as Raw does.
func WebSocket(pattern string, h http.Handler) Binding { return bodyless(KindWebSocket, pattern, h) }

// Redirect binds a redirecting handler, as Raw does.
//
//deadset:ignore DS1102 -- the binding constructor for a KindRedirect route; no marotte route binds through it yet, and the route tests cover it
func Redirect(pattern string, h http.Handler) Binding { return bodyless(KindRedirect, pattern, h) }

// AnyMethod binds a catch-all mount that answers every method itself, with no filter.
func AnyMethod(pattern string, h http.Handler) Binding {
	return Binding{Pattern: pattern, Handler: h, Kind: KindRaw, Any: true}
}

// each returns b with f applied to every method's types.
func (b Binding) each(f func(*MethodTypes)) Binding {
	ms := make([]MethodTypes, len(b.Methods))
	copy(ms, b.Methods)
	for i := range ms {
		f(&ms[i])
	}
	b.Methods = ms
	return b
}

// WithQuery returns b with Q as the query type of every method.
func WithQuery[Q any](b Binding) Binding {
	return b.each(func(mt *MethodTypes) { mt.Query = reflect.TypeFor[Q]() })
}

// WithParams returns b with P as the path-parameter type of every method.
func WithParams[P any](b Binding) Binding {
	return b.each(func(mt *MethodTypes) { mt.Params = reflect.TypeFor[P]() })
}

// WithStatus returns b with status as every method's success status.
func WithStatus(b Binding, status int) Binding {
	return b.each(func(mt *MethodTypes) { mt.Status = status })
}

// WithHeaders returns b with hs as every method's request headers.
func WithHeaders(b Binding, hs ...Header) Binding {
	return b.each(func(mt *MethodTypes) { mt.Headers = hs })
}

// WithQuery returns mt with q as its query type.
//
//nolint:gocritic // hugeParam: a value receiver, so route.Typed(...).WithQuery(...) chains on a returned value.
func (mt MethodTypes) WithQuery(q reflect.Type) MethodTypes { mt.Query = q; return mt }

// WithParams returns mt with p as its path-parameter type.
//
//nolint:gocritic // hugeParam: as WithQuery.
func (mt MethodTypes) WithParams(p reflect.Type) MethodTypes { mt.Params = p; return mt }

// WithStatus returns mt with status as its success status.
//
//nolint:gocritic // hugeParam: as WithQuery.
func (mt MethodTypes) WithStatus(status int) MethodTypes { mt.Status = status; return mt }

// WithHeaders returns mt with hs as its request headers.
//
//nolint:gocritic // hugeParam: as WithQuery.
func (mt MethodTypes) WithHeaders(hs ...Header) MethodTypes { mt.Headers = hs; return mt }
