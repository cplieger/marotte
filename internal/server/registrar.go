package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/cplieger/marotte/internal/route"
	"github.com/cplieger/marotte/internal/route/mount"
)

// routeMux is what a route registration needs: *Registrar, or a test's recorder. An alias, so every
// handler package declaring the same alias has a RegisterRoutes signature identical to routeHandler's.
type routeMux = interface{ Bind(route.Binding) }

var _ routeMux = (*Registrar)(nil)

// Registrar binds every route to its endpoint-table entry in front of one http.ServeMux, so the
// (Path, Method) pairs a handler is reached by are exactly the table's.
type Registrar struct {
	mux        *http.ServeMux
	entries    map[route.Key]route.Entry
	bound      map[route.Key]bool
	violations []string
	twice      []route.Key
	mu         sync.RWMutex
	sealed     bool
}

// SealError is the refusal Routes returns: every violation Bind recorded and every unbound entry.
type SealError struct {
	Violations []string
}

func (e *SealError) Error() string {
	return fmt.Sprintf("the endpoint table and the bound routes disagree (%d): %s",
		len(e.Violations), strings.Join(e.Violations, "; "))
}

// NewRegistrar binds onto mux against entries.
func NewRegistrar(mux *http.ServeMux, entries []route.Entry) *Registrar {
	g := &Registrar{
		mux:     mux,
		entries: make(map[route.Key]route.Entry, len(entries)),
		bound:   make(map[route.Key]bool, len(entries)),
	}
	for i := range entries {
		g.entries[entries[i].Key()] = entries[i]
	}
	return g
}

// Bind mounts b's handler and binds it to b's table entries. A binding with no entry, a
// type that differs from its entry's, or a second binding of one entry is recorded, and
// seal refuses the boot with every violation listed. A Bind after seal mounts nothing.
func (g *Registrar) Bind(b route.Binding) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.sealed {
		slog.Error("a route was bound after the endpoint table was sealed; it is not served", "pattern", b.Pattern)
		return
	}
	method, path := route.SplitPattern(b.Pattern)
	if !strings.HasPrefix(path, "/") {
		g.violations = append(g.violations, b.Pattern+": a host-qualified pattern")
		return
	}
	before := len(g.twice)
	switch {
	case b.Any:
		for _, m := range route.AnyMethods() {
			g.bindOne(b, &route.MethodTypes{Method: m}, path, true)
		}
	case method != "":
		if len(b.Methods) != 1 || b.Methods[0].Method != method {
			g.violations = append(g.violations, b.Pattern+": a method-qualified binding must type exactly its own method")
			return
		}
		g.bindMethod(b, &b.Methods[0], path)
	case len(b.Methods) == 0:
		g.violations = append(g.violations, b.Pattern+": a plain-path binding with no method")
		return
	default:
		for i := range b.Methods {
			g.bindMethod(b, &b.Methods[i], path)
		}
	}
	// A second binding of a bound key is not mounted: ServeMux panics on a repeated pattern.
	if len(g.twice) == before {
		mount.On(g.mux, b)
	}
}

// bindMethod binds mt, and its HEAD twin when mt is GET.
func (g *Registrar) bindMethod(b route.Binding, mt *route.MethodTypes, path string) {
	g.bindOne(b, mt, path, false)
	if mt.Method == http.MethodGet {
		head := *mt
		head.Method = http.MethodHead
		g.bindOne(b, &head, path, false)
	}
}

func (g *Registrar) bindOne(b route.Binding, mt *route.MethodTypes, path string, anyMethod bool) {
	k := route.Key{Method: mt.Method, Path: path}
	id := k.Method + " " + k.Path
	e, ok := g.entries[k]
	switch {
	case !ok:
		g.violations = append(g.violations, id+": no entry")
		return
	case g.bound[k]:
		g.violations = append(g.violations, id+": bound twice")
		g.twice = append(g.twice, k)
		return
	}
	g.bound[k] = true
	if e.Any != anyMethod {
		g.violations = append(g.violations, id+": Any differs from the entry's")
	}
	if anyMethod {
		return
	}
	for _, d := range differences(&e, b.Kind, mt) {
		g.violations = append(g.violations, id+": "+d+" differs from the entry's")
	}
}

func differences(e *route.Entry, k route.Kind, mt *route.MethodTypes) []string {
	var out []string
	typed := func(field string, entry, bound reflect.Type) {
		if entry != bound {
			out = append(out, fmt.Sprintf("%s (bound %s, entry %s)", field, typeName(bound), typeName(entry)))
		}
	}
	if e.Kind != k {
		out = append(out, "Kind")
	}
	typed("Params", e.Params, mt.Params)
	typed("Request", e.Request, mt.Request)
	typed("Query", e.Query, mt.Query)
	typed("Response", e.Response, mt.Response)
	if e.Status != mt.Status {
		out = append(out, fmt.Sprintf("Status (bound %d, entry %d)", mt.Status, e.Status))
	}
	if !slices.Equal(e.Headers, mt.Headers) {
		out = append(out, "Headers")
	}
	return out
}

// seal fixes the mux; it fails when any table entry is unbound or Bind recorded a violation.
func (g *Registrar) seal() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sealed = true
	vs := slices.Clone(g.violations)
	var unbound []string
	for k := range g.entries {
		if !g.bound[k] {
			unbound = append(unbound, k.Method+" "+k.Path+": unbound")
		}
	}
	slices.Sort(unbound)
	vs = append(vs, unbound...)
	if len(vs) == 0 {
		return nil
	}
	return &SealError{Violations: vs}
}

// entryOf maps the pattern mux.Handler returned and r's method to the entry ServeMux will
// dispatch to; ok is false for "" and for a method the entry set lacks.
//
//deadset:ignore DS1102 -- the access gate's route lookup, wired with the gate; the registrar tests read it until then
func (g *Registrar) entryOf(pattern, method string) (route.Entry, bool) {
	if pattern == "" {
		return route.Entry{}, false
	}
	pm, path := route.SplitPattern(pattern)
	if pm != "" && pm != method && (pm != http.MethodGet || method != http.MethodHead) {
		return route.Entry{}, false
	}
	k := route.Key{Method: method, Path: path}
	g.mu.RLock()
	defer g.mu.RUnlock()
	if !g.bound[k] {
		return route.Entry{}, false
	}
	e, ok := g.entries[k]
	return e, ok
}

// typeName is t's name for a violation message.
func typeName(t reflect.Type) string {
	if t == nil {
		return "none"
	}
	return t.String()
}
