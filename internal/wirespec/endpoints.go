package wirespec

import (
	"cmp"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/go/wiregen/v2"
	"github.com/cplieger/marotte/internal/apitable"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/route"
)

// Security scheme names, as components.securitySchemes declares them.
const (
	schemeSession = "session"
	schemeAPIKey  = "apiKey"
)

var openAPIInfo = wiregen.OpenAPIInfo{
	Title:       "marotte",
	Version:     "0",
	Description: "The HTTP API of a marotte instance. docs/api.md covers authentication, errors and versioning.",
	SecuritySchemes: map[string]wiregen.SecurityScheme{
		schemeSession: {
			Type: "apiKey", In: "cookie", Name: "__Host-marotte_session",
			Description: "The browser session. Over plain HTTP the cookie is named marotte_session.",
		},
		schemeAPIKey: {Type: "http", Scheme: "bearer", Description: "A scoped API key, sent as Authorization: Bearer."},
	},
}

// loopbackReach is where a ClassLoopbackOperator route answers: its credential is the loopback
// address, so the standard servers field says so, not only the extension.
var loopbackReach = []wiregen.Server{{
	URL:         "http://127.0.0.1:{port}",
	Description: "reachable only from loopback inside the container",
	Variables:   map[string]wiregen.ServerVariable{"port": {Default: "9847"}},
}}

// securityFor is an entry's OpenAPI security: each requirement is an alternative a caller may use
// alone, so a scheme the gate refuses for the class is never listed.
func securityFor(a route.Access) []wiregen.SecurityRequirement {
	switch a.Class {
	case route.ClassKeyScope:
		return []wiregen.SecurityRequirement{{schemeSession: nil}, {schemeAPIKey: {a.KeyScope}}}
	case route.ClassSession, route.ClassRecovery, route.ClassRecentAuth:
		return []wiregen.SecurityRequirement{{schemeSession: nil}}
	default:
		return nil
	}
}

// accessExtension is the x-marotte-access member: the class, with the key scope and the
// recent-auth window where the class has them.
func accessExtension(a route.Access) map[string]any {
	ext := map[string]any{"class": string(a.Class)}
	if a.KeyScope != "" {
		ext["key_scope"] = a.KeyScope
	}
	if a.RecentAuth > 0 {
		ext["recent_auth"] = window(a.RecentAuth)
	}
	return ext
}

// window spells a recent-auth window in whole hours, or else whole minutes: "8h", "10m".
func window(d time.Duration) string {
	if d%time.Hour == 0 {
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	}
	return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
}

var wireKinds = map[route.Kind]wiregen.Kind{
	route.KindJSON:        wiregen.KindJSON,
	route.KindRaw:         wiregen.KindRaw,
	route.KindEventStream: wiregen.KindEventStream,
	route.KindRedirect:    wiregen.KindRedirect,
}

// published reports whether e is an OpenAPI operation: an /api/ entry that is no catch-all, no
// WebSocket and no HEAD twin, which its GET describes.
func published(e *route.Entry) bool {
	return strings.HasPrefix(e.Path, "/api/") && !e.Any && e.Kind != route.KindWebSocket && e.Method != http.MethodHead
}

// Sockets is every WebSocket entry, which has no wiregen kind: cmd/wire-codegen writes their paths
// into sockets.gen.ts itself.
func Sockets() []route.Entry {
	var out []route.Entry
	entries := apitable.Entries()
	for i := range entries {
		if entries[i].Kind == route.KindWebSocket && entries[i].Method != http.MethodHead {
			out = append(out, entries[i])
		}
	}
	return out
}

// openAPIPath drops ServeMux's {$} anchor and a trailing wildcard's dots, which a URL template
// does not carry.
func openAPIPath(p string) string {
	return strings.ReplaceAll(strings.TrimSuffix(p, "{$}"), "...}", "}")
}

func typeOf(t reflect.Type) wiregen.Type {
	if t == nil {
		return wiregen.Type{}
	}
	return wiregen.TypeOf(t)
}

// errorsOf is e's derived error union: its owners' codes, its library codes, the gate's codes for
// its access and the codes every handler can answer.
func errorsOf(e *route.Entry) []wiregen.ErrorRef {
	sets := slices.Concat(e.Owners, []route.CodeSet{e.AuthCodes, apitable.GateCodes(e), httpreply.Codes})
	seen := map[string]bool{}
	var out []wiregen.ErrorRef
	for _, set := range sets {
		for _, c := range set {
			if !seen[c.Code] {
				seen[c.Code] = true
				out = append(out, wiregen.ErrorRef{Code: c.Code, Status: c.Status})
			}
		}
	}
	slices.SortFunc(out, func(a, b wiregen.ErrorRef) int { return cmp.Compare(a.Code, b.Code) })
	return out
}

func endpointOf(e *route.Entry) wiregen.Endpoint {
	ep := wiregen.Endpoint{
		Name:        e.Name,
		Method:      e.Method,
		Path:        openAPIPath(e.Path),
		Kind:        wireKinds[e.Kind],
		ContentType: e.ContentType,
		Summary:     e.Summary,
		Params:      typeOf(e.Params),
		Query:       typeOf(e.Query),
		Request:     typeOf(e.Request),
		Status:      e.Status,
		Errors:      errorsOf(e),
		Security:    securityFor(e.Access),
		Extensions:  map[string]any{"x-marotte-access": accessExtension(e.Access)},
	}
	if resp := e.Response; resp != nil {
		if resp.Kind() == reflect.Slice {
			ep.Array, resp = true, resp.Elem()
		}
		ep.Response = wiregen.TypeOf(resp)
	}
	if e.Access.Class == route.ClassLoopbackOperator {
		ep.Servers = loopbackReach
	}
	for _, h := range e.Headers {
		ep.Headers = append(ep.Headers, wiregen.Header{
			Name: h.Name, Arg: h.Arg, Required: h.Required, MinBytes: h.MinBytes, MaxBytes: h.MaxBytes,
		})
	}
	return ep
}

// commandUnion is POST /api/command's request: ClientCommand tagged by type, its payload typed by
// the tag. Every case admits chat_id and caller, as the dispatcher reads them for any command.
func commandUnion(commands []apitable.Command) wiregen.Union {
	u := wiregen.Union{
		Type:         wiregen.TypeRef[marotte.ClientCommand](),
		Tag:          "type",
		Payload:      "payload",
		Cases:        make(map[string][]string, len(commands)),
		PayloadTypes: make(map[string]wiregen.Type, len(commands)),
	}
	for _, c := range commands {
		u.Cases[string(c.Type)] = []string{"chat_id", "caller"}
		u.PayloadTypes[string(c.Type)] = typeOf(c.Payload)
	}
	return u
}

// reachedOverrides is tsNames limited to the names eps' types carry, since wiregen refuses an
// override naming no registered type and a fixture table reaches few of them.
func reachedOverrides(eps []wiregen.Endpoint) map[string]string {
	out := map[string]string{}
	for i := range eps {
		for _, t := range []wiregen.Type{eps[i].Params, eps[i].Query, eps[i].Request, eps[i].Response} {
			if ts, ok := tsNames[t.Name]; ok {
				out[t.Name] = ts
			}
		}
	}
	return out
}

// registryFor is the wire contract over entries: today's registrations, an endpoint per published
// entry and, when commands is non-empty, the command union.
func registryFor(entries []route.Entry, commands []apitable.Command) *wiregen.Registry {
	r := &wiregen.Registry{
		Types:              wireTypes,
		Enums:              wireEnums,
		EnumTSNameOverride: enumTSNames,
		PathNameOverride:   pathNameOverrides,
		Events:             wireEvents,
		ErrorEnvelope:      wiregen.TypeRef[httpserver.ErrorResponse](),
		OpenAPI:            openAPIInfo,
	}
	for i := range entries {
		if published(&entries[i]) {
			r.Endpoints = append(r.Endpoints, endpointOf(&entries[i]))
		}
	}
	r.TSNameOverride = reachedOverrides(r.Endpoints)
	if len(commands) > 0 {
		r.Unions = []wiregen.Union{commandUnion(commands)}
	}
	return r
}
